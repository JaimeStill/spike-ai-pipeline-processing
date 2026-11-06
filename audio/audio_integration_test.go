//go:build integration && linux

package audio_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// speechUtterances are the durations of the fixture's utterances, LibriSpeech dev-clean
// 2277-149896-0000 to 0003, in milliseconds, read twice; speechGap is the digital silence after
// each but the last.
var (
	speechUtterances = []time.Duration{6590, 7145, 4835, 2840, 6590, 7145, 4835, 2840}
	speechGap        = time.Second
)

// speechGaps returns the fixture's inserted gaps, in order, and its duration.
func speechGaps() (gaps []audio.Span, duration time.Duration) {
	for i, d := range speechUtterances {
		duration += d * time.Millisecond
		if i < len(speechUtterances)-1 {
			gaps = append(gaps, audio.Span{Start: duration, End: duration + speechGap})
			duration += speechGap
		}
	}
	return gaps, duration
}

// TestSpeech probes the speech fixture through both runners, detects its silences, and plans its
// windows; logs them; and checks that the runners agree, that every inserted gap lies within a
// silence detected, that the plan's one cut lies in the last gap before 30 s, and that a second
// plan is the first.
func TestSpeech(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("testdata", "speech.flac"))
	if err != nil {
		t.Fatal(err)
	}
	gaps, duration := speechGaps()
	got := map[string]audio.Detection{}
	for name, runner := range speechRunners {
		t.Run(name, func(t *testing.T) {
			s := runner(t)
			req := audio.Request{Path: path}
			det, err := s.Detect(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			got[name] = det
			src := det.Source
			t.Logf("recorded: source %v, stream %d, %s, %d Hz, %d channel(s)", src.Duration, src.Stream, src.Codec, src.SampleRate, src.Channels)
			want := audio.Source{Duration: duration, Stream: 0, Codec: "flac", SampleRate: 16000, Channels: 1}
			if src != want {
				t.Errorf("source %+v, want %+v", src, want)
			}
			t.Logf("recorded: mean volume %.1f dB, threshold %.1f dB, minimum silence %v", det.MeanVolume, det.Threshold, det.MinSilence)
			for i, s := range det.Silences {
				t.Logf("recorded: silence %d: %v (%.3fs)", i, s, s.Duration().Seconds())
			}
			for _, g := range gaps {
				if !within(g, det.Silences) {
					t.Errorf("the inserted gap %v is in no silence detected", g)
				}
			}
			plan, err := s.Plan(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			logPlan(t, plan)
			if len(plan.Windows) != 2 {
				t.Fatalf("%d windows, want 2", len(plan.Windows))
			}
			if cut := plan.Windows[0]; cut.Cut != audio.CutSilence || !within(audio.Span{Start: cut.End, End: cut.End}, []audio.Span{gaps[3]}) {
				t.Errorf("the first window %v, cut %v; want it cut at a silence in the gap %v", cut.Span, cut.Cut, gaps[3])
			}
			again, err := s.Plan(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan, again) {
				t.Errorf("planned twice: %+v, then %+v", plan, again)
			}
		})
	}
	if e, c := got["exec"], got["container"]; len(got) == 2 && !reflect.DeepEqual(e, c) {
		t.Errorf("the exec runner detected %+v, the container runner %+v", e, c)
	}
}

// TestSpeechFixedThreshold detects the fixture's silences at a fixed threshold through the exec
// runner: every inserted gap, digital silence, is below -60 dB.
func TestSpeechFixedThreshold(t *testing.T) {
	s := speechRunners["exec"](t)
	gaps, _ := speechGaps()
	det, err := s.Detect(t.Context(), audio.Request{Path: filepath.Join("testdata", "speech.flac"), Threshold: -60, MinSilence: time.Second / 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded: threshold %.1f dB: %v", det.Threshold, det.Silences)
	for _, g := range gaps {
		if !within(g, det.Silences) {
			t.Errorf("the inserted gap %v is in no silence detected", g)
		}
	}
}

// within reports whether s lies within one of silences, to the millisecond: silencedetect's times
// fall on its sample boundaries, and the fixture's utterances end on a millisecond's.
func within(s audio.Span, silences []audio.Span) bool {
	for _, x := range silences {
		if x.Start <= s.Start+time.Millisecond && s.End <= x.End+time.Millisecond {
			return true
		}
	}
	return false
}

// logPlan logs plan's windows and seams.
func logPlan(t *testing.T, plan audio.Plan) {
	t.Helper()
	t.Logf("recorded: plan of %v, overlap %v: %d windows", plan.Duration, plan.Overlap, len(plan.Windows))
	for i, w := range plan.Windows {
		t.Logf("recorded: window %d: %v (%.3fs), cut at %v", i, w.Span, w.Duration().Seconds(), w.Cut)
	}
	for i, s := range plan.Seams {
		t.Logf("recorded: seam %d: %v", i, s)
	}
}
