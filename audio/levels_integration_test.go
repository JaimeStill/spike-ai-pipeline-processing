//go:build integration && linux

package audio_test

import (
	"path/filepath"
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
