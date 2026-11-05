//go:build (integration || findings) && linux

package audiotest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
)

// Model is set A's audio model on gb10-2's router, Gemma 4 E4B at Q8_0: 4 slots, and 30 s of
// audio a request, audio.WindowLimit.
const Model = "ggml-org/gemma-4-E4B-it-GGUF:Q8_0"

// Transcriber is what each window is sent to the transcription endpoint with.
type Transcriber struct {
	// Client sends the requests. It is required.
	Client *client.Client
	// Model is the audio model's id. It is required.
	Model string
	// Language is the windows' language, such as "en"; empty sends none.
	Language string
	// Prompt replaces the endpoint's transcription instruction; empty sends none.
	Prompt string
	// Native is sent beside the typed fields, such as temperature. It is shared by every request
	// and never changed.
	Native url.Values
}

// Greedy returns the Transcriber of the router SPIKE_ROUTER_URL names for Model, decoding
// greedily, temperature 0 through Native, in English. It skips t when SPIKE_ROUTER_URL is unset.
func Greedy(t testing.TB) Transcriber {
	t.Helper()
	return Transcriber{
		Client:   visiontest.Router(t),
		Model:    Model,
		Language: "en",
		Native:   url.Values{"temperature": {"0"}},
	}
}

// Request is one source through the harness.
type Request struct {
	// Splitter plans and cuts the source. It is required.
	Splitter *audio.Splitter
	// Source is the source and how it is planned: its Path, required, and the Overlap and
	// silence detection, as audio.Splitter.Plan takes them.
	Source audio.Request
	// Dir is the media directory the windows are cut into, which the caller owns. It is required.
	// Each window's file is removed once it is transcribed, unless Keep.
	Dir string
	// Keep keeps each window's file in Dir after it is transcribed.
	Keep bool
	// Transcriber sends the windows.
	Transcriber Transcriber
	// Concurrency is how many Transcribe calls are in flight at once. Zero means 1; a negative
	// concurrency is refused.
	Concurrency int
	// CutConcurrency is how many windows are cut at once, as audio.WindowsRequest takes it. Zero
	// means Concurrency.
	CutConcurrency int
	// Join is how the transcripts are joined.
	Join audio.JoinRequest
}

// Window is one window of a run: its file and its transcript, or the error that took its place.
// Its times are offsets from the start of the run's windows, after the plan.
type Window struct {
	// File is the window as the stream yielded it. Its Path no longer names a file unless the
	// request kept it.
	File audio.WindowFile
	// Text is the window's transcript, empty when Err is set.
	Text string
	// Usage counts the tokens the window's transcription took.
	Usage client.TranscriptionUsage
	// Err is the window's failure, its cut's or its transcription's; nil when it was transcribed.
	Err error
	// Ready is when the stream yielded the window, cut.
	Ready time.Duration
	// Start and End are when the window's Transcribe call began and returned; zero for a window
	// whose cut failed.
	Start, End time.Duration
}

// Latency returns the window's Transcribe call's duration: End less Start.
func (w Window) Latency() time.Duration { return w.End - w.Start }

// Result is a run of the harness over one source.
type Result struct {
	// Plan is the source's plan.
	Plan audio.Plan
	// Windows are the plan's windows, in plan order, whatever order they were transcribed in.
	Windows []Window
	// Transcript is the windows' transcripts joined, a failed window's as the empty string.
	Transcript audio.Transcript
	// PlanTime is how long the plan took: the probe and the silence detection.
	PlanTime time.Duration
	// Wall is how long the windows took, from the start of their stream to the last window's
	// transcript, the plan excluded.
	Wall time.Duration
	// MaxInFlight is the most Transcribe calls that were in flight at once.
	MaxInFlight int
}

// Texts returns the windows' transcripts, in plan order.
func (r Result) Texts() []string {
	out := make([]string, len(r.Windows))
	for i, w := range r.Windows {
		out[i] = w.Text
	}
	return out
}

// Failed returns the windows that failed, in plan order.
func (r Result) Failed() []Window {
	var out []Window
	for _, w := range r.Windows {
		if w.Err != nil {
			out = append(out, w)
		}
	}
	return out
}

// Run plans req's source, streams its windows, transcribes each as it is cut, at most
// req.Concurrency Transcribe calls in flight, and joins the transcripts in plan order.
//
// A window whose cut or transcription fails is recorded in its Window's Err, joined as the empty
// string, and the run carries on; Transcribe is never retried. An error that ends the run, a
// refused request, a failed plan, a stream that ends on an error, or ctx ending, is returned once
// the calls in flight have returned, with the Result so far.
func Run(ctx context.Context, req Request) (Result, error) {
	var r Result
	switch {
	case req.Splitter == nil:
		return r, errors.New("audiotest: no splitter")
	case req.Transcriber.Client == nil || req.Transcriber.Model == "":
		return r, errors.New("audiotest: no client or model")
	case req.Concurrency < 0 || req.CutConcurrency < 0:
		return r, fmt.Errorf("audiotest: concurrency %d, cut concurrency %d", req.Concurrency, req.CutConcurrency)
	}
	concurrency := max(req.Concurrency, 1)
	cut := req.CutConcurrency
	if cut == 0 {
		cut = concurrency
	}
	begun := time.Now()
	plan, err := req.Splitter.Plan(ctx, req.Source)
	r.PlanTime = time.Since(begun)
	if err != nil {
		return r, err
	}
	r.Plan = plan
	r.Windows = make([]Window, len(plan.Windows))

	// slots bounds the Transcribe calls in flight: the loop takes one before it starts a call,
	// so the stream is not ranged further, and no further window is held, while every slot is
	// busy.
	slots := make(chan struct{}, concurrency)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		inFlight int
	)
	start := time.Now()
	stream := req.Splitter.Windows(ctx, audio.WindowsRequest{Path: req.Source.Path, Dir: req.Dir, Concurrency: cut}, plan)
	for f, err := range stream {
		if err != nil && f.Count == 0 {
			// The stream ends on this error.
			wg.Wait()
			r.Wall = time.Since(start)
			return r, err
		}
		w := &r.Windows[f.Index]
		w.File, w.Ready = f, time.Since(start)
		if err != nil {
			w.Err = err
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			r.Wall = time.Since(start)
			return r, ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-slots }()
			mu.Lock()
			inFlight++
			r.MaxInFlight = max(r.MaxInFlight, inFlight)
			mu.Unlock()
			w.Start = time.Since(start)
			tr, err := transcribe(ctx, req.Transcriber, f)
			w.End = time.Since(start)
			mu.Lock()
			inFlight--
			mu.Unlock()
			if err != nil {
				w.Err = err
			} else {
				w.Text, w.Usage = tr.Text, tr.Usage
			}
			if !req.Keep {
				if err := os.Remove(f.Path); err != nil && w.Err == nil {
					w.Err = err
				}
			}
		})
	}
	wg.Wait()
	r.Wall = time.Since(start)
	r.Transcript, err = audio.Join(r.Texts(), req.Join)
	return r, err
}

// transcribe sends window f's file to tr's model, read as it is uploaded.
func transcribe(ctx context.Context, tr Transcriber, f audio.WindowFile) (*client.Transcription, error) {
	file, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	out, err := tr.Client.Transcribe(ctx, &client.TranscriptionRequest{
		Model:    tr.Model,
		File:     file,
		FileName: fmt.Sprintf("window-%d.wav", f.Index),
		Language: tr.Language,
		Prompt:   tr.Prompt,
		Native:   tr.Native,
	})
	if err != nil {
		return nil, fmt.Errorf("audiotest: window %d: %w", f.Index, err)
	}
	return out, nil
}
