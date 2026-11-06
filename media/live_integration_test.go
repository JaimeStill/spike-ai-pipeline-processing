//go:build integration && linux

package media_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/audiotest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
)

// The live tier: the audio-and-video fixture read at real time through the exec runner, each
// window sent to audiotest.Model, Gemma 4 E4B, for its transcript, and each frame to Gemma 4
// 26B-A4B for a description, on the router SPIKE_ROUTER_URL names, which set A loads, as each
// event arrives. As each transcript returns, the harness re-runs audio's Join over the transcripts
// received so far and logs the settled text, the words before the latest seam. There is no
// stand-in tier: the requests are test code, and the layer above owns inference.

const (
	// liveVisionModel describes the frames, as it describes office's pictures.
	liveVisionModel = "google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0"
	// livePrompt is the text beside each frame, office's describePrompt.
	livePrompt = "Describe this picture in two or three sentences."
	// liveOutputCap bounds a description's output, as office's describeOutputCap does.
	liveOutputCap = 512
	// liveSlots is how many requests to each model are in flight at once: the router's parallel
	// slots for each, 4, at which audio's speech tier transcribes and document's vision tier reads
	// pages.
	liveSlots = 4
)

// answer is one event through the live harness and its model's answer. Its times are wall-clock:
// when the event's last sample or frame arrived from ffmpeg, when the stream yielded it, when its
// request was sent, after it waited for a slot, and when the request returned.
type answer struct {
	kind   string
	index  int
	source time.Duration
	path   string

	arrived, yielded, sent, answered time.Time
	// sending is closed just before the request is sent.
	sending chan struct{}
	done    bool
	text    string
	err     error
}

// lagLine returns a's recorded lag line: "lag" and then key=value fields, space-separated, in a
// fixed order, so a later run's lines parse the same way. arrival_to_event_ms is how long the
// stream took to yield the event after its last sample or frame arrived; event_to_answer_ms how
// long its answer took after it was yielded, queued_ms of it waiting for a slot and request_ms
// the request itself.
func lagLine(a *answer) string {
	return fmt.Sprintf("lag kind=%s index=%d source_ms=%d arrival_to_event_ms=%d event_to_answer_ms=%d queued_ms=%d request_ms=%d ok=%t",
		a.kind, a.index, a.source.Milliseconds(), a.yielded.Sub(a.arrived).Milliseconds(),
		a.answered.Sub(a.yielded).Milliseconds(), a.sent.Sub(a.yielded).Milliseconds(),
		a.answered.Sub(a.sent).Milliseconds(), a.err == nil)
}

// step is the transcript as it stood after a transcript returned and extended the windows
// answered from the first on: the Join of those windows' transcripts, and its settled words, those
// before its latest seam.
type step struct {
	windows int
	at      time.Duration
	join    audio.Transcript
	settled int
}

// liveRun is a run of the live harness over one source.
type liveRun struct {
	start   time.Time
	events  []media.Event
	windows []*answer
	frames  []*answer
	steps   []step
	errs    []error
}

// liveHarness sends a source's windows and frames to their models as they arrive.
type liveHarness struct {
	streamer *media.Streamer
	tr       audiotest.Transcriber
	vision   *client.Client
	// yielded, when set, is called from the stream's loop with each event's answer once its
	// request is started, before the loop ranges further.
	yielded func(*answer)
}

// run reads req's source through the streamer and, as each event is yielded, starts its request:
// a window's transcription, at most liveSlots in flight, or a frame's description, at most
// liveSlots in flight. It returns once the stream has ended and every request has returned; it
// never ends the test, so the caller checks the run.
func (h liveHarness) run(ctx context.Context, t *testing.T, req media.Request) *liveRun {
	r := &liveRun{start: time.Now()}
	audioSlots, visionSlots := make(chan struct{}, liveSlots), make(chan struct{}, liveSlots)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	since := func(at time.Time) time.Duration { return at.Sub(r.start).Round(time.Millisecond) }
	for e, err := range h.streamer.Events(ctx, req) {
		now := time.Now()
		if err != nil {
			r.errs = append(r.errs, err)
			t.Logf("recorded: %v: the stream ended: %v", since(now), err)
			continue
		}
		a := &answer{source: e.Time, arrived: e.Arrived, yielded: now, sending: make(chan struct{})}
		slots, send := visionSlots, h.describe
		mu.Lock()
		r.events = append(r.events, e)
		if e.Window != nil {
			a.kind, a.index, a.path = "window", e.Window.Index, e.Window.Path
			slots, send = audioSlots, h.transcribe
			r.windows = append(r.windows, a)
			t.Logf("recorded: %v: window %d %v yielded, %v after it arrived", since(now), a.index, e.Window.Span, now.Sub(e.Arrived).Round(time.Millisecond))
		} else {
			a.kind, a.index, a.path = "frame", e.Frame.Index, e.Frame.Path
			r.frames = append(r.frames, a)
			t.Logf("recorded: %v: frame %d, %s at %v, yielded %v after it arrived", since(now), a.index, e.Frame.Reason, e.Time, now.Sub(e.Arrived).Round(time.Millisecond))
		}
		mu.Unlock()
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				a.answered, a.done, a.err = time.Now(), true, ctx.Err()
				a.sent = a.answered
				mu.Unlock()
				close(a.sending)
				return
			}
			defer func() { <-slots }()
			sent := time.Now()
			close(a.sending)
			text, err := send(ctx, a)
			mu.Lock()
			defer mu.Unlock()
			a.sent, a.answered, a.done, a.text, a.err = sent, time.Now(), true, text, err
			t.Logf("recorded: %v: %s %d answered in %v: %q, err %v", since(a.answered), a.kind, a.index, a.answered.Sub(sent).Round(time.Millisecond), text, err)
			if a.kind == "window" {
				r.settle(t, since(a.answered))
			}
		})
		if h.yielded != nil {
			h.yielded(a)
		}
	}
	wg.Wait()
	return r
}

// settle joins the transcripts of the windows answered from the first on, a failed window's as the
// empty string, when another has been answered since the last step, and logs the step: its
// settled words, before the latest seam, and the words after it, which a later window may still
// change. The caller holds the run's lock.
func (r *liveRun) settle(t *testing.T, at time.Duration) {
	n := 0
	for n < len(r.windows) && r.windows[n].done {
		n++
	}
	if len(r.steps) > 0 && r.steps[len(r.steps)-1].windows >= n {
		return
	}
	texts := make([]string, n)
	for i, w := range r.windows[:n] {
		texts[i] = w.text
	}
	join, err := audio.Join(texts, audio.JoinRequest{})
	if err != nil {
		t.Errorf("join of %d windows: %v", n, err)
		return
	}
	s := step{windows: n, at: at, join: join}
	if len(join.Seams) > 0 {
		s.settled = join.Seams[len(join.Seams)-1].Position
	}
	r.steps = append(r.steps, s)
	t.Logf("recorded: %v: settled after %d windows, %d words: %q; pending: %q",
		at, n, s.settled, strings.Join(join.Words[:s.settled], " "), strings.Join(join.Words[s.settled:], " "))
}

// transcribe sends a window's WAV to the transcription endpoint, as audio's speech tier does.
func (h liveHarness) transcribe(ctx context.Context, a *answer) (string, error) {
	out, err := h.tr.Transcribe(ctx, a.index, a.path)
	if err != nil {
		return "", err
	}
	return out.Text, nil
}

// describe sends a frame's PNG to liveVisionModel beside livePrompt, greedy and with reasoning
// off, as office's vision tier sends a picture. A response that isn't completed is an error.
func (h liveHarness) describe(ctx context.Context, a *answer) (string, error) {
	data, err := os.ReadFile(a.path)
	if err != nil {
		return "", err
	}
	resp, err := h.vision.Generate(ctx, &protocol.Request{
		Model: liveVisionModel,
		Input: []protocol.Item{protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
			protocol.InputText{Text: livePrompt},
			protocol.InputImage{ImageURL: protocol.DataURL("image/png", data)},
		}}},
		Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
		MaxOutputTokens: liveOutputCap,
		Native:          jsontext.Value(`{"temperature":0}`),
	})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(visiontest.ResponseText(resp))
	if resp.Status != protocol.StatusCompleted {
		return text, fmt.Errorf("the response is %s", visiontest.ResponseStatus(resp))
	}
	return text, nil
}

// newLiveHarness builds the harness over the exec runner and the router SPIKE_ROUTER_URL names,
// skipping t when it is unset.
func newLiveHarness(t *testing.T) liveHarness {
	t.Helper()
	tr := audiotest.Greedy(t)
	return liveHarness{streamer: streamer(t, runners["exec"](t)), tr: tr, vision: visiontest.Router(t)}
}

// TestHarness reads the audio-and-video fixture at real time and sends each window and frame to its
// model as it arrives, and checks the run: the stream ends cleanly; the settled text only grows,
// each step's settled words unchanged in every later step's join, behavior 9's property as the
// windows land; every window is transcribed and the joined transcript meets the truth by audio's
// speech tier's measure, its normalized WER under audiotest.SpeechWER, every seam matched, and the
// edits at each at most audiotest.SpeechSeamErrors; and every frame the truth names gets a
// completed, non-empty description, and each frame of a solid colour segment, yellow and white,
// one that names the colour. It logs each event as it is yielded and answered, each step of the
// settled text, a lag line per event, lagLine's, and the WER.
func TestHarness(t *testing.T) {
	h := newLiveHarness(t)
	truth := readTruth(t)
	path := fixture(t)
	r := h.run(t.Context(), t, media.Request{Source: media.Source{File: path, Paced: true}, Dir: t.TempDir()})
	t.Logf("recorded: %d windows and %d frames over %v", len(r.windows), len(r.frames), time.Since(r.start).Round(time.Millisecond))
	if len(r.errs) > 0 {
		t.Fatalf("the stream ended with %v", r.errs)
	}

	for _, a := range slices.Concat(r.windows, r.frames) {
		t.Logf("recorded: %s", lagLine(a))
		if a.err != nil {
			t.Errorf("%s %d: %v", a.kind, a.index, a.err)
		}
	}

	if len(r.frames) != len(truth.Video.Frames) {
		t.Errorf("%d frames, want the truth's %d", len(r.frames), len(truth.Video.Frames))
	}
	for _, a := range r.frames {
		if a.text == "" {
			t.Errorf("frame %d at %v: an empty description", a.index, a.source)
		}
		for _, seg := range truth.Video.Segments {
			colour, ok := strings.CutPrefix(seg.Pattern, "color=c=")
			if ok && a.source.Milliseconds() >= seg.StartMS && a.source.Milliseconds() < seg.EndMS {
				named := strings.Contains(strings.ToLower(a.text), colour)
				t.Logf("recorded: frame %d at %v, solid %s: named %t", a.index, a.source, colour, named)
				if !named {
					t.Errorf("frame %d at %v, solid %s: the description %q doesn't name it", a.index, a.source, colour, a.text)
				}
			}
		}
	}

	if len(r.windows) == 0 {
		t.Fatal("no windows")
	}
	if last := r.steps[len(r.steps)-1]; last.windows != len(r.windows) {
		t.Fatalf("the last step joined %d windows, want all %d", last.windows, len(r.windows))
	}
	// The settled text only grows: each step's settled words lead every later step's join.
	for i, s := range r.steps {
		settled := s.join.Words[:s.settled]
		for _, later := range r.steps[i+1:] {
			if later.settled < s.settled || !slices.Equal(later.join.Words[:s.settled], settled) {
				t.Errorf("after %d windows the settled words were %q; after %d, %q of %d settled",
					s.windows, settled, later.windows, later.join.Words[:min(s.settled, len(later.join.Words))], later.settled)
			}
		}
	}

	joined := r.steps[len(r.steps)-1].join
	want := strings.Join(truth.Audio.Transcript, " ")
	edits, length := textscore.NormalizedWordErrors(want, joined.Text())
	wer := float64(edits) / float64(length)
	var positions []int
	for _, seam := range joined.Seams {
		positions = append(positions, seam.Position)
	}
	seamErrors := textscore.SeamErrors(want, joined.Text(), positions)
	for i, seam := range joined.Seams {
		t.Logf("recorded: seam %d: matched %t, %d matches of %d overlapping words, position %d, %d edits within %d words",
			i, seam.Matched, seam.Matches, seam.Overlap, seam.Position, seamErrors[i], textscore.SeamRadius)
		if !seam.Matched {
			t.Errorf("seam %d is unmatched", i)
		}
		if seamErrors[i] > audiotest.SpeechSeamErrors {
			t.Errorf("seam %d has %d edits near it, want at most %d", i, seamErrors[i], audiotest.SpeechSeamErrors)
		}
	}
	t.Logf("recorded: WER %.3f (%d edits of %d words): %q", wer, edits, length, joined.Text())
	if wer >= audiotest.SpeechWER {
		t.Errorf("WER %.3f, want under %.3f", wer, audiotest.SpeechWER)
	}
}

// TestHarnessCancel reads the fixture at real time through the live harness and ends the context
// inFlight after the second frame's description is sent, while it is in flight: the stream yields the context's error once and ends,
// well before the source has played; that request returns the context's error; every request
// either returned before the cancel or returns the context's error within a second of it; and the
// media directory holds just the events yielded, with no frames' directory left beside the source.
func TestHarnessCancel(t *testing.T) {
	// inFlight is well inside a frame's description, which took 650 ms to 1.3 s on gb10-2, so the
	// request is on the wire, not refused before it is sent.
	const inFlight = 250 * time.Millisecond
	h := newLiveHarness(t)
	path := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var (
		cancelled time.Time
		trigger   *answer
	)
	h.yielded = func(a *answer) {
		if a.kind != "frame" || a.index != 1 {
			return
		}
		select {
		case <-a.sending:
			time.Sleep(inFlight)
		case <-time.After(10 * time.Second):
			t.Error("the second frame's request wasn't sent within 10 s")
		}
		trigger, cancelled = a, time.Now()
		cancel()
	}
	dir := t.TempDir()
	r := h.run(ctx, t, media.Request{Source: media.Source{File: path, Paced: true}, Dir: dir})
	took := time.Since(r.start)
	t.Logf("recorded: cancelled %v after the start, after %d events; the run returned %v after the start",
		cancelled.Sub(r.start).Round(time.Millisecond), len(r.events), took.Round(time.Millisecond))
	if trigger == nil {
		t.Fatal("the stream yielded no second frame")
	}
	if len(r.errs) != 1 || !errors.Is(r.errs[0], context.Canceled) {
		t.Errorf("stream errors %v, want context.Canceled once", r.errs)
	}
	if !errors.Is(trigger.err, context.Canceled) {
		t.Errorf("the second frame's request returned %q, err %v; want context.Canceled", trigger.text, trigger.err)
	}
	for _, a := range slices.Concat(r.windows, r.frames) {
		t.Logf("recorded: %s, err %v", lagLine(a), a.err)
		switch {
		case a.err == nil && a.answered.Before(cancelled):
		case errors.Is(a.err, context.Canceled):
			if late := a.answered.Sub(cancelled); late > time.Second {
				t.Errorf("%s %d returned %v after the cancel", a.kind, a.index, late)
			}
		default:
			t.Errorf("%s %d: answered %v after the cancel, err %v; want an answer before it or context.Canceled",
				a.kind, a.index, a.answered.Sub(cancelled), a.err)
		}
	}
	if took > 15*time.Second {
		t.Errorf("the run returned %v after the start; the source is 50 s", took)
	}
	checkDir(t, dir, r.events)
	checkNoScratch(t, filepath.Dir(path))
}
