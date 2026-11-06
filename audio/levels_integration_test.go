//go:build integration && linux

package audio_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// TestLevelsFixedThreshold detects each level-change variant's silences, through both runners, at
// the threshold its truth fixes from the first 30 s: every inserted gap before the change lies
// within a silence detected, and no silence detected ends after the change, though gaps begin
// after it. That is the case a noise-floor tracker is for.
func TestLevelsFixedThreshold(t *testing.T) {
	var f levelsFixture
	readTruth(t, levelsTruth, &f)
	change := ms(f.ChangeMS)
	for name, runner := range speechRunners {
		t.Run(name, func(t *testing.T) {
			s := runner(t)
			for _, v := range f.Variants {
				det, err := s.Detect(t.Context(), audio.Request{
					Path:       filepath.Join("testdata", v.File),
					Threshold:  f.FixedThresholdDB,
					MinSilence: ms(f.MinSilenceMS),
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("recorded: %s at %.1f dB: %v", v.File, det.Threshold, det.Silences)
				for _, g := range spans(f.Gaps) {
					if g.End <= change && !within(g, det.Silences) {
						t.Errorf("%s: the gap %v, before the change, is in no silence detected", v.File, g)
					}
				}
				for _, x := range det.Silences {
					if x.End > change {
						t.Errorf("%s: the silence %v ends after the change at %v", v.File, x, change)
					}
				}
			}
		})
	}
}

// TestLevelsTracker plans each level-change variant through both runners with the tracker at its
// defaults: Detect's silences hold every inserted gap, before the change and after it; and the
// file path's plan, ffmpeg decoding the variant, is the incremental path's over the variant's own
// samples, read without ffmpeg, a Tracker and a Planner fed them as a stream.
func TestLevelsTracker(t *testing.T) {
	var f levelsFixture
	readTruth(t, levelsTruth, &f)
	for name, runner := range speechRunners {
		t.Run(name, func(t *testing.T) {
			s := runner(t)
			for _, v := range f.Variants {
				req := audio.Request{Path: filepath.Join("testdata", v.File), Detector: audio.DetectorTracker}
				det, err := s.Detect(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("recorded: %s: %v at %v dB", v.File, det.Silences, det.Thresholds)
				before, after := gapsFound(t, f, det.Silences)
				if n := len(f.Gaps) - len(f.GapsAfterChange); before != n || after != len(f.GapsAfterChange) {
					t.Errorf("%s: %d of %d gaps found before the change and %d of %d after it",
						v.File, before, n, after, len(f.GapsAfterChange))
				}
				plan, err := s.Plan(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				logPlan(t, plan)
				want := streamPlan(t, samplesOf(t, v.File), req, 1<<14, det.Source.Duration)
				if !reflect.DeepEqual(plan.Windows, want) {
					t.Errorf("%s: windows %v, the incremental path's %v", v.File, plan.Windows, want)
				}
			}
		})
	}
}
