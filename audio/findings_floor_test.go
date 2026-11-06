//go:build findings && linux

package audio_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestFindingsFloor is the noise-floor tracker's tuning harness, in the findings tier: it runs no
// model. AUDIO_FLOOR_SOURCES names the long sources of findingSources it measures, such as "ami
// earnings21 earnings22"; unset skips it. `mise run findings-floor` runs it.
//
// Each source, checked against the corpus manifest, is decoded by the host's ffmpeg to 16 kHz mono
// 16-bit WAV three ways: unchanged; stepped up by floorGainDB from floorChangeAt on; and with a
// pink noise added from floorChangeAt on, anoisesrc's at floorNoiseAmplitude and seed 2. Each is
// planned through silencedetect, at the request's default, 15 dB under the whole source's mean
// volume, and at a threshold fixed 15 dB under its first 30 s's, the simulation's fixed threshold;
// and through the tracker at each setting of floorGrid. Each plan's windows, and those cut at the
// limit, are logged; for each tracker plan of an unchanged source, so is the share of its cuts at
// a silence that lie within a silence silencedetect found at its default, which a cut in speech
// silencedetect hears doesn't. Each setting is first run over the level-change fixtures, its gaps
// found before and after the change logged, a gap found when a silence lies within it or it within
// a silence, to the millisecond.
func TestFindingsFloor(t *testing.T) {
	names := strings.Fields(os.Getenv(floorSourcesEnv))
	if len(names) == 0 {
		t.Skipf("%s names no source", floorSourcesEnv)
	}
	logFixtureGrid(t)
	docs := corpusRoot(t)
	s := splitter(t, func(spec tool.Spec) (*tool.Tool, error) { return tool.NewExec(t.Context(), spec) })
	for _, name := range names {
		var src *findingSource
		for i := range findingSources {
			if findingSources[i].Name == name {
				src = &findingSources[i]
			}
		}
		if src == nil {
			t.Fatalf("%s: %s names no source", floorSourcesEnv, name)
		}
		t.Run(name, func(t *testing.T) {
			if _, err := checkCorpus(docs, []string{src.Audio}); err != nil {
				t.Fatal(err)
			}
			var reference []audio.Span
			for _, cond := range floorConditions {
				wav := filepath.Join(t.TempDir(), cond.name+".wav")
				if err := decodeCondition(t.Context(), filepath.Join(docs, src.Audio), wav, cond.filter); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(wav)
				if err != nil {
					t.Fatal(err)
				}
				pcm := data[44:]
				duration := time.Duration(len(pcm)/2) * time.Second / audio.SampleRate
				det, err := s.Detect(t.Context(), audio.Request{Path: wav})
				if err != nil {
					t.Fatal(err)
				}
				if cond.name == "unchanged" {
					reference = det.Silences
				}
				logFloorPlan(t, name, cond.name, fmt.Sprintf("silencedetect, mean %.1f dB less 15", det.MeanVolume),
					det.Source.Duration, det.Silences, nil)
				first := meanVolume(pcm[:min(len(pcm), 2*30*audio.SampleRate)])
				fixed, err := s.Detect(t.Context(), audio.Request{Path: wav, Threshold: math.Round((first-audio.DefaultBelow)*10) / 10})
				if err != nil {
					t.Fatal(err)
				}
				logFloorPlan(t, name, cond.name, fmt.Sprintf("silencedetect, fixed %.1f dB", fixed.Threshold),
					fixed.Source.Duration, fixed.Silences, nil)
				for _, g := range floorGrid {
					silences := track(t, pcm, g, 1<<16)
					var agree []audio.Span
					if cond.name == "unchanged" {
						agree = reference
					}
					logFloorPlan(t, name, cond.name, floorSetting(g), duration, silences, agree)
				}
			}
		})
	}
}

// floorSourcesEnv names the sources TestFindingsFloor measures.
const floorSourcesEnv = "AUDIO_FLOOR_SOURCES"

// The simulated level changes: from floorChangeAt on, a gain step of floorGainDB, or a pink noise
// at floorNoiseAmplitude, the simulation's settings.
const (
	floorChangeAt       = 600
	floorGainDB         = 12
	floorNoiseAmplitude = 0.004
)

// floorConditions are the ways TestFindingsFloor decodes a source: its ffmpeg filter graph, from
// the source's first audio stream, labelled a, to 16 kHz mono.
var floorConditions = []struct{ name, filter string }{
	{"unchanged", "[0:a:0]aresample=16000,aformat=channel_layouts=mono"},
	{"gain", fmt.Sprintf("[0:a:0]aresample=16000,aformat=channel_layouts=mono,volume=volume=%ddB:enable='gte(t,%d)'",
		floorGainDB, floorChangeAt)},
	{"noise", fmt.Sprintf("[0:a:0]aresample=16000,aformat=channel_layouts=mono[s];"+
		"anoisesrc=r=16000:c=pink:a=%g:seed=2,volume=volume=0:enable='lt(t,%d)'[n];"+
		"[s][n]amix=inputs=2:duration=first:normalize=0", floorNoiseAmplitude, floorChangeAt)},
}

// floorGrid are the tracker settings TestFindingsFloor measures: a coarse grid, the simulation's
// untuned setting, the 10th percentile over 30 s plus 6 dB, among them; and a fine one about the
// defaults, the 10th percentile over 4 s plus 12 dB.
var floorGrid = func() []audio.Request {
	var out []audio.Request
	for _, w := range []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second} {
		for _, p := range []float64{10, 20, 30} {
			for _, m := range []float64{6, 9, 12} {
				out = append(out, audio.Request{Percentile: p, FloorWindow: w, Margin: m})
			}
		}
	}
	for _, w := range []time.Duration{4 * time.Second, 5 * time.Second, 6 * time.Second, 8 * time.Second} {
		for _, p := range []float64{5, 10, 15} {
			for _, m := range []float64{10, 11, 12, 13, 14} {
				if r := (audio.Request{Percentile: p, FloorWindow: w, Margin: m}); !slices.Contains(out, r) {
					out = append(out, r)
				}
			}
		}
	}
	return out
}()

// floorSetting names a tracker setting.
func floorSetting(r audio.Request) string {
	return fmt.Sprintf("tracker, p%g over %v, +%g dB", r.Percentile, r.FloorWindow, r.Margin)
}

// decodeCondition writes the source's first audio stream, through filter, to out as 16 kHz mono
// 16-bit WAV with ffmpeg's canonical 44-byte header.
func decodeCondition(ctx context.Context, source, out, filter string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-i", source,
		"-filter_complex", filter, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le",
		"-flags:a", "+bitexact", "-fflags", "+bitexact", "-y", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, b)
	}
	return nil
}

// meanVolume is pcm's mean volume, in dB relative to full scale, as volumedetect measures it: the
// mean of its samples' squares.
func meanVolume(pcm []byte) float64 {
	var sum float64
	n := len(pcm) / 2
	for i := range n {
		s := float64(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
		sum += s * s
	}
	return 10 * math.Log10(sum/float64(n)/(32768*32768))
}

// logFloorPlan logs the plan of a source duration long at silences: its windows, and those cut at
// the limit; and, with reference, the share of its cuts at a silence that lie within one of
// reference.
func logFloorPlan(t *testing.T, source, condition, detector string, duration time.Duration, silences, reference []audio.Span) {
	t.Helper()
	plan, err := audio.PlanWindows(duration, silences, audio.Request{})
	if err != nil {
		t.Fatal(err)
	}
	limit, cuts, agreed := 0, 0, 0
	for _, w := range plan.Windows {
		switch w.Cut {
		case audio.CutLimit:
			limit++
		case audio.CutSilence:
			cuts++
			if within(audio.Span{Start: w.End, End: w.End}, reference) {
				agreed++
			}
		}
	}
	line := fmt.Sprintf("recorded: floor: %s | %s | %s | %d silences | %d windows | %d at the limit",
		source, condition, detector, len(silences), len(plan.Windows), limit)
	if reference != nil {
		line += fmt.Sprintf(" | %d of %d cuts in silencedetect's silences", agreed, cuts)
	}
	t.Log(line)
}

// logFixtureGrid logs, for each setting of floorGrid and the fixed threshold of the level-change
// fixtures, the gaps the tracker finds in each fixture before and after the change.
func logFixtureGrid(t *testing.T) {
	var f levelsFixture
	readTruth(t, levelsTruth, &f)
	settings := append([]audio.Request{{Threshold: f.FixedThresholdDB}}, floorGrid...)
	for _, g := range settings {
		name := floorSetting(g)
		if g.Threshold != 0 {
			name = fmt.Sprintf("tracker, fixed %.1f dB", g.Threshold)
		}
		line := "recorded: floor: fixtures | " + name
		for _, v := range f.Variants {
			before, after := gapsFound(t, f, trackFile(t, v.File, g, 1<<16))
			line += fmt.Sprintf(" | %s %d/%d before, %d/%d after", v.Change, before, len(f.Gaps)-len(f.GapsAfterChange),
				after, len(f.GapsAfterChange))
		}
		t.Log(line)
	}
}
