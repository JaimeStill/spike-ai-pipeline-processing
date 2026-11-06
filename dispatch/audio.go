//go:build linux

package dispatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// AudioConfig is what an [AudioStage] is built over: the caller's Splitter, the version of the
// tools behind it, the request each source's silences are detected and its windows planned
// under, and how many windows are cut at once.
type AudioConfig struct {
	// Splitter plans and cuts the windows; it is required. A caller with no ffmpeg and ffprobe
	// has no Splitter, and registers no audio stage: its audio parts are no processor yet.
	Splitter *audio.Splitter
	// Version names the tools behind the Splitter, ffmpeg's and ffprobe's, as [Versions] gives
	// them; it is required.
	Version string
	// Request is the request every source's plan is made under: its Overlap, MinSilence, Below,
	// and Threshold, each defaulted as audio.Request says. Its Path is ignored: each part's plan
	// is of the part's own file. Every field it reads is part of the stage's identity, its
	// defaults applied, so a zero field and its default are the same identity. An Overlap
	// audio.CheckOverlap refuses is refused when the stage is built; any other field the Splitter
	// refuses fails each part.
	Request audio.Request
	// Concurrency is how many windows of a part are cut at once, each an ffmpeg run, as
	// audio.WindowsRequest's: zero means 1, and a negative concurrency is refused. It changes no
	// window, so it is no part of the stage's identity.
	Concurrency int
}

// AudioStage prepares speech for a model's audio input, route.Audio's parts, a WAV, FLAC, MP3,
// Ogg, or M4A, through audio's Splitter. It runs no model: it prepares the windows a layer above
// transcribes, and the plan that layer joins their transcripts by, through audio.Join.
//
// It plans the part's windows, its silences detected under the config's Request, and yields the
// plan first, a [RecordOutput] named "plan", the JSON of an [AudioPlan], its Meta the window
// count, "windows", and the source's duration, "duration", in seconds. It then cuts the windows
// into the part's directory, at most the config's Concurrency at once, and yields each in plan
// order as an [AudioOutput] named "window N", N its index in the plan from 0, a 16 kHz mono
// 16-bit PCM WAV, "audio/wav", named by its SHA-256. A window's Meta carries what a caller needs
// to transcribe it and join it without the plan: its index, "window"; the plan's window count,
// "count"; its span of the source, "start" and "end"; what ended it, "cut", silence, limit, or
// end; its samples at 16 kHz, "samples"; and the seams it shares with its neighbours, "seam_before"
// for every window but the first and "seam_after" for every window but the last, each its span of
// the source as "<start>-<end>". Every time is in seconds, as strconv's shortest decimal of the
// span's seconds, such as "24.9791875"; a window's seam_after is the next window's seam_before.
//
// A window that fails doesn't stop the others: the stage ends with an error naming every window
// that failed, once the rest are yielded, so the part fails with the plan and the windows that
// were cut among its outputs. A source with no audio stream, such as an M4A container holding
// none, ends the stage with an error wrapping audio.ErrNoAudio, before ffmpeg runs: the part
// fails, as a part whose tool refuses it does.
type AudioStage struct {
	splitter    *audio.Splitter
	request     audio.Request
	concurrency int
	id          Identity
}

// AudioPlan is the JSON of an audio part's "plan" record: the source as ffprobe read it, its
// silences as silencedetect found them, the threshold and shortest silence it ran with, and the
// windows and seams audio.PlanWindows planned in them, every time in seconds.
type AudioPlan struct {
	// Duration is the source's duration, which the windows cover.
	Duration float64 `json:"duration"`
	// Overlap is the overlap the plan was made with, each seam's length.
	Overlap float64 `json:"overlap"`
	// Stream, Codec, SampleRate, and Channels are the source's audio stream, as audio.Source's.
	Stream     int    `json:"stream"`
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	// MeanVolume is the stream's mean volume in dB, as volumedetect measured it; zero when the
	// request's Threshold was fixed.
	MeanVolume float64 `json:"mean_volume"`
	// Threshold is the silence threshold silencedetect ran with, in dB, and MinSilence the
	// shortest silence it detected.
	Threshold  float64 `json:"threshold"`
	MinSilence float64 `json:"min_silence"`
	// Silences are the silences detected, in the order silencedetect reported them.
	Silences []AudioSpan `json:"silences"`
	// Windows are the plan's windows, in order, each the window output of its Index.
	Windows []AudioWindow `json:"windows"`
	// Seams are the ranges consecutive windows share: Seams[i] is the end of window i and the
	// start of window i+1.
	Seams []AudioSpan `json:"seams"`
}

// AudioSpan is a range of an audio source, in seconds from its start.
type AudioSpan struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// AudioWindow is one window of an [AudioPlan]: its index in the plan, its span, and what ended
// it, as audio.Cut names it: silence, limit, or end.
type AudioWindow struct {
	Index int `json:"index"`
	AudioSpan
	Cut string `json:"cut"`
}

// NewAudioStage builds an AudioStage over cfg, refusing one with no Splitter or Version, an
// Overlap audio.CheckOverlap refuses, or a negative Concurrency.
func NewAudioStage(cfg AudioConfig) (*AudioStage, error) {
	if cfg.Splitter == nil {
		return nil, errors.New("dispatch: audio stage: no splitter")
	}
	if cfg.Concurrency < 0 {
		return nil, fmt.Errorf("dispatch: audio stage: concurrency %d is negative", cfg.Concurrency)
	}
	req := cfg.Request
	overlap, err := audio.CheckOverlap(req.Overlap)
	if err != nil {
		return nil, fmt.Errorf("dispatch: audio stage: %w", err)
	}
	// The identity hashes the detection the Splitter applies: a fixed threshold in place of one
	// below the mean, or the dB below the mean, defaulted.
	below := req.Below
	if req.Threshold == 0 {
		below = cmp.Or(below, audio.DefaultBelow)
	}
	id, err := adapterIdentity("audio", cfg.Version, struct {
		Overlap    time.Duration `json:"overlap"`
		MinSilence time.Duration `json:"min_silence"`
		Below      float64       `json:"below"`
		Threshold  float64       `json:"threshold"`
	}{overlap, cmp.Or(req.MinSilence, audio.DefaultMinSilence), below, req.Threshold})
	if err != nil {
		return nil, err
	}
	req.Path = ""
	return &AudioStage{splitter: cfg.Splitter, request: req, concurrency: cfg.Concurrency, id: id}, nil
}

// Identity returns the stage's identity: "audio", its tools' version, and its request's hash.
func (s *AudioStage) Identity() Identity { return s.id }

// Prepare plans and cuts the windows of the source at p.Path, as the type's doc says.
func (s *AudioStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		req := s.request
		req.Path = p.Path
		det, err := s.splitter.Detect(ctx, req)
		if err != nil {
			yield(Item{}, fmt.Errorf("audio stage: %w", err))
			return
		}
		plan, err := audio.PlanWindows(det.Source.Duration, det.Silences, req)
		if err != nil {
			yield(Item{}, fmt.Errorf("audio stage: %w", err))
			return
		}
		o, err := record("plan", audioPlan(det, plan), map[string]string{
			"windows": strconv.Itoa(len(plan.Windows)), "duration": secs(plan.Duration),
		})
		if err != nil {
			yield(Item{}, fmt.Errorf("audio stage: %w", err))
			return
		}
		if !yield(OutputItem(o), nil) {
			return
		}
		var failed []error
		wreq := audio.WindowsRequest{Path: p.Path, Dir: dir, Concurrency: s.concurrency}
		for w, err := range s.splitter.Windows(ctx, wreq, plan) {
			if err != nil && w.Count == 0 {
				// The request, the plan, or the probe was refused, or ctx ended: no window follows.
				yield(Item{}, fmt.Errorf("audio stage: %w", err))
				return
			}
			if err != nil {
				failed = append(failed, err)
				continue
			}
			o := Output{
				Kind: AudioOutput, Name: "window " + strconv.Itoa(w.Index), MediaType: "audio/wav",
				Path: w.Path, Meta: windowMeta(w, plan),
			}
			if !yield(OutputItem(o), nil) {
				return
			}
		}
		if len(failed) > 0 {
			yield(Item{}, fmt.Errorf("audio stage: %d of %d windows failed: %w", len(failed), len(plan.Windows), errors.Join(failed...)))
		}
	}
}

// audioPlan returns the plan record's JSON of det's source and silences and plan.
func audioPlan(det audio.Detection, plan audio.Plan) AudioPlan {
	out := AudioPlan{
		Duration: plan.Duration.Seconds(), Overlap: plan.Overlap.Seconds(),
		Stream: det.Source.Stream, Codec: det.Source.Codec, SampleRate: det.Source.SampleRate, Channels: det.Source.Channels,
		MeanVolume: det.MeanVolume, Threshold: det.Threshold, MinSilence: det.MinSilence.Seconds(),
		Silences: make([]AudioSpan, 0, len(det.Silences)),
		Windows:  make([]AudioWindow, 0, len(plan.Windows)),
		Seams:    make([]AudioSpan, 0, len(plan.Seams)),
	}
	for _, sp := range det.Silences {
		out.Silences = append(out.Silences, audioSpan(sp))
	}
	for i, w := range plan.Windows {
		out.Windows = append(out.Windows, AudioWindow{Index: i, AudioSpan: audioSpan(w.Span), Cut: w.Cut.String()})
	}
	for _, sp := range plan.Seams {
		out.Seams = append(out.Seams, audioSpan(sp))
	}
	return out
}

// audioSpan returns sp in seconds.
func audioSpan(sp audio.Span) AudioSpan {
	return AudioSpan{Start: sp.Start.Seconds(), End: sp.End.Seconds()}
}

// windowMeta returns a window output's Meta, as AudioStage's doc says.
func windowMeta(w audio.WindowFile, plan audio.Plan) map[string]string {
	meta := map[string]string{
		"window":  strconv.Itoa(w.Index),
		"count":   strconv.Itoa(w.Count),
		"start":   secs(w.Window.Start),
		"end":     secs(w.Window.End),
		"cut":     w.Window.Cut.String(),
		"samples": strconv.Itoa(w.Samples),
	}
	if w.Index > 0 {
		meta["seam_before"] = spanSecs(plan.Seams[w.Index-1])
	}
	if w.Index < len(plan.Seams) {
		meta["seam_after"] = spanSecs(plan.Seams[w.Index])
	}
	return meta
}

// secs returns d in seconds, strconv's shortest decimal of them.
func secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }

// spanSecs returns sp in seconds, as "<start>-<end>".
func spanSecs(sp audio.Span) string { return secs(sp.Start) + "-" + secs(sp.End) }
