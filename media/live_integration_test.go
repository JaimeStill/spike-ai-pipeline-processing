//go:build integration && linux

package media_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/audiotest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
)

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
// inFlight after the second frame's description is sent, while it is in flight: the stream yields
// the context's error once and ends, well before the source has played; that request returns the
// context's error; every request either returned before the cancel or returns the context's error
// within a second of it; and the media directory holds just the events yielded, with no frames'
// directory left beside the source.
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
