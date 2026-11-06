//go:build integration && linux

package audio_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/audiotest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
)

// The speech tier: the speech fixture's windows, cut through the exec runner, transcribed by
// audiotest.Model on the router SPIKE_ROUTER_URL names, which set A loads, and joined with
// Join. There is no stand-in tier: the requests are test code, and the layer above owns
// inference.

// TestSpeechTranscript transcribes the speech fixture's two windows through audiotest.Run at
// concurrency 1 and at 4, E4B's slots, greedily and in English, and checks the joined transcript
// against the fixture's truth: every window transcribed, no more calls in flight than the
// concurrency, every seam matched, the normalized WER under audiotest.SpeechWER, and the edits at
// each seam at most audiotest.SpeechSeamErrors. It logs a recorded line per window, its span,
// transcript, and latency, the seam's report, the WER, and the wall time at each concurrency.
func TestSpeechTranscript(t *testing.T) {
	tr := audiotest.Greedy(t)
	path, err := filepath.Abs(filepath.Join("testdata", "speech.flac"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "speech.truth.txt"))
	if err != nil {
		t.Fatal(err)
	}
	truth := strings.Join(strings.Fields(string(data)), " ")
	s := speechRunners["exec"](t)
	for _, concurrency := range []int{1, 4} {
		r, err := audiotest.Run(t.Context(), audiotest.Request{
			Splitter:    s,
			Source:      audio.Request{Path: path},
			Dir:         t.TempDir(),
			Transcriber: tr,
			Concurrency: concurrency,
		})
		if err != nil {
			t.Fatalf("concurrency %d: %v", concurrency, err)
		}
		for _, w := range r.Windows {
			t.Logf("recorded: concurrency %d: window %d %v: ready %v, latency %v, %d input and %d output tokens: %q",
				concurrency, w.File.Index, w.File.Window.Span, w.Ready.Round(time.Millisecond), w.Latency().Round(time.Millisecond),
				w.Usage.InputTokens, w.Usage.OutputTokens, w.Text)
			if w.Err != nil {
				t.Errorf("concurrency %d: window %d: %v", concurrency, w.File.Index, w.Err)
			}
		}
		if r.MaxInFlight > concurrency {
			t.Errorf("concurrency %d: %d calls in flight at once", concurrency, r.MaxInFlight)
		}
		joined := r.Transcript.Text()
		edits, length := textscore.NormalizedWordErrors(truth, joined)
		wer := float64(edits) / float64(length)
		var positions []int
		for _, seam := range r.Transcript.Seams {
			positions = append(positions, seam.Position)
		}
		seamErrors := textscore.SeamErrors(truth, joined, positions)
		t.Logf("recorded: concurrency %d: plan %v, %d windows, seams %v; plan time %v, wall %v, %d in flight at most",
			concurrency, r.Plan.Duration, len(r.Plan.Windows), r.Plan.Seams, r.PlanTime.Round(time.Millisecond),
			r.Wall.Round(time.Millisecond), r.MaxInFlight)
		if len(r.Transcript.Seams) != len(r.Plan.Seams) {
			t.Errorf("concurrency %d: %d seams joined, want the plan's %d", concurrency, len(r.Transcript.Seams), len(r.Plan.Seams))
		}
		for i, seam := range r.Transcript.Seams {
			t.Logf("recorded: concurrency %d: seam %d: matched %t, %d matches of %d overlapping words, position %d, %d edits within %d words",
				concurrency, i, seam.Matched, seam.Matches, seam.Overlap, seam.Position, seamErrors[i], textscore.SeamRadius)
			if !seam.Matched {
				t.Errorf("concurrency %d: seam %d is unmatched", concurrency, i)
			}
			if seamErrors[i] > audiotest.SpeechSeamErrors {
				t.Errorf("concurrency %d: seam %d has %d edits near it, want at most %d", concurrency, i, seamErrors[i], audiotest.SpeechSeamErrors)
			}
		}
		t.Logf("recorded: concurrency %d: WER %.3f (%d edits of %d words): %q", concurrency, wer, edits, length, joined)
		if wer >= audiotest.SpeechWER {
			t.Errorf("concurrency %d: WER %.3f, want under %.3f", concurrency, wer, audiotest.SpeechWER)
		}
	}
}
