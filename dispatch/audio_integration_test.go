//go:build linux && integration

package dispatch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// speechFixture is the audio package's speech fixture: 49.82 s of LibriSpeech speech, its plan
// two windows cut in the inserted gap at 24.41 s.
var speechFixture = filepath.Join("..", "audio", "testdata", "speech.flac")

// audioRunners build ffmpeg and ffprobe through one runner each: the host's through the exec
// runner, and audio.FFmpegImage's through the container runner, which pulls the image first when
// it isn't present.
var audioRunners = map[string]func(t *testing.T, spec tool.Spec) (*tool.Tool, error){
	"exec": func(t *testing.T, spec tool.Spec) (*tool.Tool, error) { return tool.NewExec(t.Context(), spec) },
	"container": func(t *testing.T, spec tool.Spec) (*tool.Tool, error) {
		return tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage}, spec)
	},
}

// TestAudioStageReal dispatches the speech fixture through the audio stage over each runner's
// ffmpeg and ffprobe, and checks that its plan record is the Splitter's own plan of it and that
// each window follows in plan order, a WAV of its Meta's samples, named by its SHA-256; and that
// the runners prepare the same windows.
func TestAudioStageReal(t *testing.T) {
	sums := map[string][]string{}
	for name, build := range audioRunners {
		t.Run(name, func(t *testing.T) {
			ffmpeg, err := build(t, audio.FFmpegSpec())
			if err != nil {
				t.Fatal(err)
			}
			ffprobe, err := build(t, audio.FFprobeSpec())
			if err != nil {
				t.Fatal(err)
			}
			s, err := audio.NewSplitter(audio.Config{FFmpeg: ffmpeg, FFprobe: ffprobe})
			if err != nil {
				t.Fatal(err)
			}
			stage, err := dispatch.NewAudioStage(dispatch.AudioConfig{
				Splitter: s, Version: dispatch.Versions(ffmpeg, ffprobe), Concurrency: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			path, err := filepath.Abs(speechFixture)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := s.Plan(t.Context(), audio.Request{Path: path})
			if err != nil {
				t.Fatal(err)
			}

			d := newDispatcher(t, dispatch.Stages{route.Audio: stage}, dispatch.NewMemoryIndex())
			events := dispatchAll(t, d, path, func(ev *dispatch.Event) {
				if o := ev.Output; o != nil && o.Kind == dispatch.AudioOutput {
					checkWAV(t, *o)
					t.Logf("recorded: %s %s, %s, Meta %v", o.Name, o.MediaType, filepath.Base(o.Path), o.Meta)
				}
			})
			wantSteps(t, events, []step{{"speech.flac", 0, dispatch.Prepared}})
			outs := events[0].Outputs
			if len(outs) != 1+len(plan.Windows) || outs[0].Name != "plan" {
				t.Fatalf("%d outputs, want the plan and its %d windows", len(outs), len(plan.Windows))
			}
			t.Logf("recorded: plan %s", outs[0].Value)
			var p dispatch.AudioPlan
			if err := json.Unmarshal(outs[0].Value, &p); err != nil {
				t.Fatal(err)
			}
			if p.Duration != plan.Duration.Seconds() || len(p.Windows) != len(plan.Windows) || len(p.Seams) != len(plan.Seams) {
				t.Fatalf("the plan record %+v isn't the Splitter's plan %+v", p, plan)
			}
			for i, w := range plan.Windows {
				o := outs[1+i]
				if pw := p.Windows[i]; pw.Start != w.Start.Seconds() || pw.End != w.End.Seconds() || pw.Cut != w.Cut.String() {
					t.Errorf("the plan record's window %d %+v, want %v", i, pw, w)
				}
				if o.Kind != dispatch.AudioOutput || o.Name != "window "+strconv.Itoa(i) || o.Meta["count"] != strconv.Itoa(len(plan.Windows)) {
					t.Errorf("output %d is %s %s of %s", 1+i, o.Kind, o.Name, o.Meta["count"])
				}
				samples, _ := strconv.Atoi(o.Meta["samples"])
				if d := float64(samples) - w.Duration().Seconds()*audio.SampleRate; d < -1 || d > 1 {
					t.Errorf("%s holds %d samples; its span is %v", o.Name, samples, w.Span)
				}
				sums[name] = append(sums[name], filepath.Base(o.Path))
			}
		})
	}
	if e, c := sums["exec"], sums["container"]; len(sums) == 2 && !slices.Equal(e, c) {
		t.Errorf("the exec runner prepared windows %q, the container runner %q", e, c)
	}
	if _, err := os.Stat(speechFixture); err != nil {
		t.Errorf("the dispatcher removed the input: %v", err)
	}
}
