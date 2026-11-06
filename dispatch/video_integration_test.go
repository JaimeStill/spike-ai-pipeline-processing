//go:build linux && integration

package dispatch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// scenesFixture is the audio package's audio-and-video fixture: speech.flac's stream beside a
// generated video of five test patterns, its truth the frames media's defaults take.
var (
	scenesFixture = filepath.Join("..", "audio", "testdata", "speech-scenes.mkv")
	scenesTruth   = filepath.Join("..", "audio", "testdata", "speech-scenes.truth.json")
)

// scenesFrames reads the fixture's truth: each frame media's defaults take, its reason and time.
func scenesFrames(t *testing.T) []struct {
	Reason string `json:"reason"`
	TimeMS int64  `json:"time_ms"`
} {
	t.Helper()
	b, err := os.ReadFile(scenesTruth)
	if err != nil {
		t.Fatal(err)
	}
	var truth struct {
		Video struct {
			Frames []struct {
				Reason string `json:"reason"`
				TimeMS int64  `json:"time_ms"`
			} `json:"frames"`
		} `json:"video"`
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	return truth.Video.Frames
}

// sumOf returns the SHA-256 an output's file is named by.
func sumOf(o dispatch.Output) string {
	sum, _, _ := strings.Cut(filepath.Base(o.Path), ".")
	return sum
}

// streamed returns the SHA-256s of the frames and windows media's Streamer writes for a copy of
// the file at path in a directory of the test's, as media's own caller has them.
func streamed(t *testing.T, s *media.Streamer, path string) (frames, windows []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), filepath.Base(path))
	if err := os.WriteFile(src, b, 0o600); err != nil {
		t.Fatal(err)
	}
	for ev, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: src}, Dir: t.TempDir()}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Frame != nil {
			frames = append(frames, ev.Frame.SHA256)
		}
		if ev.Window != nil {
			windows = append(windows, ev.Window.SHA256)
		}
	}
	return frames, windows
}

// stripAudio writes a copy of the file at path with its audio stream removed, its video stream
// copied, into a directory of the test's, through ffmpeg, and returns its path.
func stripAudio(t *testing.T, ffmpeg *tool.Tool, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "with-audio.mkv"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	err = ffmpeg.Run(t.Context(), tool.Invocation{
		Args: []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-i", "with-audio.mkv", "-map", "0:v", "-c", "copy", "video-only.mkv"},
		Dir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "with-audio.mkv")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "video-only.mkv")
}

// TestVideoStageReal dispatches the audio-and-video fixture through the video stage over each
// runner's ffmpeg and ffprobe, and checks that its plan comes first; that its frames are the
// truth's, reasons and times exactly, and media's own, byte for byte, as are its windows; that
// nothing is left beside the input; and that the runners prepare the same frames and windows. A
// copy of the fixture with its audio stripped yields the plan and the same frames alone.
func TestVideoStageReal(t *testing.T) {
	truth := scenesFrames(t)
	path, err := filepath.Abs(scenesFixture)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
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
			s, err := media.NewStreamer(media.Config{FFmpeg: ffmpeg, FFprobe: ffprobe})
			if err != nil {
				t.Fatal(err)
			}
			stage, err := dispatch.NewVideoStage(dispatch.VideoConfig{Streamer: s, Version: dispatch.Versions(ffmpeg, ffprobe)})
			if err != nil {
				t.Fatal(err)
			}
			wantFrames, wantWindows := streamed(t, s, path)

			d := newDispatcher(t, dispatch.Stages{route.Video: stage}, dispatch.NewMemoryIndex())
			events := dispatchAll(t, d, path, func(ev *dispatch.Event) {
				o := ev.Output
				switch {
				case o == nil:
				case o.Kind == dispatch.ImageOutput:
					wantPNG(t, *o)
				case o.Kind == dispatch.AudioOutput:
					checkWAV(t, *o)
				}
			})
			wantSteps(t, events, []step{{"speech-scenes.mkv", 0, dispatch.Prepared}})
			outs := events[0].Outputs
			if len(outs) == 0 || outs[0].Name != "plan" {
				t.Fatalf("outputs %q, want the plan first", kinds(outs))
			}
			t.Logf("recorded: plan %s", outs[0].Value)
			var frames, windows []string
			for _, o := range outs[1:] {
				t.Logf("recorded: %s %s, %s, Meta %v", o.Kind, o.Name, filepath.Base(o.Path), o.Meta)
				switch o.Kind {
				case dispatch.ImageOutput:
					i := len(frames)
					if i < len(truth) {
						want := truth[i]
						if o.Name != "frame "+strconv.Itoa(i) || o.Meta["reason"] != want.Reason ||
							o.Meta["time"] != secsOf(time.Duration(want.TimeMS)*time.Millisecond) {
							t.Errorf("%s: %s at %s; want frame %d, %s at %d ms", o.Name, o.Meta["reason"], o.Meta["time"], i, want.Reason, want.TimeMS)
						}
					}
					frames = append(frames, sumOf(o))
				case dispatch.AudioOutput:
					if o.Name != "window "+strconv.Itoa(len(windows)) {
						t.Errorf("%s, want window %d", o.Name, len(windows))
					}
					windows = append(windows, sumOf(o))
				}
			}
			if len(frames) != len(truth) {
				t.Errorf("%d frames, want the truth's %d", len(frames), len(truth))
			}
			if !slices.Equal(frames, wantFrames) || !slices.Equal(windows, wantWindows) {
				t.Errorf("frames %q and windows %q; media's %q and %q", frames, windows, wantFrames, wantWindows)
			}
			sums[name] = append(frames, windows...)

			only := stripAudio(t, ffmpeg, path)
			events = dispatchAll(t, d, only, nil)
			wantSteps(t, events, []step{{"video-only.mkv", 0, dispatch.Prepared}})
			outs = events[0].Outputs
			var onlyFrames []string
			for _, o := range outs[1:] {
				if o.Kind != dispatch.ImageOutput {
					t.Errorf("the video-only copy yielded %s %s", o.Kind, o.Name)
				}
				onlyFrames = append(onlyFrames, sumOf(o))
			}
			if outs[0].Name != "plan" || !slices.Equal(onlyFrames, frames) {
				t.Errorf("the video-only copy's outputs %q, frames %q; want the plan and frames %q", kinds(outs), onlyFrames, frames)
			}
			if entries, err := os.ReadDir(filepath.Dir(only)); err != nil || len(entries) != 1 {
				t.Errorf("the video-only copy's directory holds %v, %v", entries, err)
			}
		})
	}
	if e, c := sums["exec"], sums["container"]; len(sums) == 2 && !slices.Equal(e, c) {
		t.Errorf("the exec runner prepared %q, the container runner %q", e, c)
	}
	after, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the input's directory held %d entries, now %d: the run wrote beside the input", len(before), len(after))
	}
}

// secsOf returns d in seconds, as the stage's Meta gives a time.
func secsOf(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }
