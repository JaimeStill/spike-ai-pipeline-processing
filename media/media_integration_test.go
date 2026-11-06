//go:build integration && linux

package media_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// runners build ffmpeg and ffprobe through one runner each: the host's through the exec runner,
// and audio.FFmpegImage's through the container runner, which pulls the image first when it isn't
// present.
var runners = map[string]func(t *testing.T) media.Config{
	"exec": func(t *testing.T) media.Config {
		return tools(t, func(spec tool.Spec) (*tool.Tool, error) { return tool.NewExec(t.Context(), spec) })
	},
	"container": func(t *testing.T) media.Config {
		return tools(t, func(spec tool.Spec) (*tool.Tool, error) {
			return tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage}, spec)
		})
	},
}

// tools builds ffmpeg and ffprobe through build, logging their versions.
func tools(t *testing.T, build func(tool.Spec) (*tool.Tool, error)) media.Config {
	t.Helper()
	ffmpeg, err := build(audio.FFmpegSpec())
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := build(audio.FFprobeSpec())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s, %s %s", ffmpeg.Name(), ffmpeg.Version(), ffprobe.Name(), ffprobe.Version())
	return media.Config{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// streamer builds a Streamer over cfg.
func streamer(t *testing.T, cfg media.Config) *media.Streamer {
	t.Helper()
	s, err := media.NewStreamer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// scenesTruth is the part of speech-scenes.truth.json the tests read: the frames the default
// policy takes, and the video's size.
type scenesTruth struct {
	Video struct {
		Width  int `json:"width"`
		Height int `json:"height"`
		Frames []struct {
			Reason string `json:"reason"`
			TimeMS int64  `json:"time_ms"`
		} `json:"frames"`
	} `json:"video"`
}

// readTruth reads the fixture's truth.
func readTruth(t *testing.T) scenesTruth {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "audio", "testdata", "speech-scenes.truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var truth scenesTruth
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	return truth
}

// fixture copies the audio-and-video fixture into a directory of the test's own, since the run
// writes its frames beside its source, and returns its path.
func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "audio", "testdata", "speech-scenes.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "speech-scenes.mkv")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScenes reads the audio-and-video fixture unpaced through both runners and checks its frames
// against the truth, reasons and times exactly, at the source's 320x180; its windows against
// audio's file path under the tracker detector, byte for byte; and that the runners write the
// same windows and frames.
func TestScenes(t *testing.T) {
	path := fixture(t)
	truth := readTruth(t)
	got := map[string][]string{}
	for name, runner := range runners {
		t.Run(name, func(t *testing.T) {
			cfg := runner(t)
			dir := t.TempDir()
			events := collect(t, streamer(t, cfg).Events(t.Context(), media.Request{Source: media.Source{File: path}, Dir: dir}))
			ws, fs := split(events)
			if len(fs) != len(truth.Video.Frames) {
				t.Errorf("%d frames, want the truth's %d", len(fs), len(truth.Video.Frames))
			}
			for i, e := range fs {
				f := e.Frame
				t.Logf("recorded: frame %d: %s at %v, %dx%d, sha256 %s", f.Index, f.Reason, e.Time, f.Width, f.Height, f.SHA256)
				got[name] = append(got[name], f.SHA256)
				if i < len(truth.Video.Frames) {
					want := truth.Video.Frames[i]
					if f.Index != i || f.Reason.String() != want.Reason || e.Time != time.Duration(want.TimeMS)*time.Millisecond {
						t.Errorf("frame %d: %d, %s at %v; want %d, %s at %d ms", i, f.Index, f.Reason, e.Time, i, want.Reason, want.TimeMS)
					}
				}
				if f.Width != truth.Video.Width || f.Height != truth.Video.Height {
					t.Errorf("frame %d is %dx%d, want %dx%d", i, f.Width, f.Height, truth.Video.Width, truth.Video.Height)
				}
				checkPNG(t, f)
			}
			// audio's file path, on the same runner, under the tracker detector.
			sp, err := audio.NewSplitter(audio.Config{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := sp.Plan(t.Context(), audio.Request{Path: path, Detector: audio.DetectorTracker})
			if err != nil {
				t.Fatal(err)
			}
			var want []audio.WindowFile
			for w, err := range sp.Windows(t.Context(), audio.WindowsRequest{Path: path, Dir: t.TempDir()}, plan) {
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, w)
			}
			if len(ws) != len(want) {
				t.Fatalf("%d windows, want audio's %d", len(ws), len(want))
			}
			for i, e := range ws {
				w := e.Window
				t.Logf("recorded: window %d: %v, %s, %d samples, threshold %.2f dB, sha256 %s", w.Index, w.Span, w.Cut, w.Samples, w.Threshold, w.SHA256)
				got[name] = append(got[name], w.SHA256)
				if w.Index != i || w.SHA256 != want[i].SHA256 || w.Samples != want[i].Samples || w.Cut != want[i].Window.Cut || w.Span.Start != want[i].Window.Start {
					t.Errorf("window %d: %d, %v %s, %d samples, %s; audio's %v %s, %d samples, %s",
						i, w.Index, w.Span, w.Cut, w.Samples, w.SHA256, want[i].Window.Span, want[i].Window.Cut, want[i].Samples, want[i].SHA256)
				}
				if w.Cut == audio.CutSilence && w.Threshold >= 0 {
					t.Errorf("window %d, cut at a silence, names threshold %v dB", i, w.Threshold)
				}
			}
			checkDir(t, dir, events)
			checkNoScratch(t, filepath.Dir(path))
		})
	}
	if e, c := got["exec"], got["container"]; len(got) == 2 && !slices.Equal(e, c) {
		t.Errorf("the exec runner wrote %q, the container runner %q", e, c)
	}
}

// TestScenesMaxSide reads the fixture through the exec runner with a largest side of 160, and
// checks that its frames are scaled to 160x90, at the truth's times.
func TestScenesMaxSide(t *testing.T) {
	path := fixture(t)
	truth := readTruth(t)
	s := streamer(t, runners["exec"](t))
	events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{File: path}, Dir: t.TempDir(), MaxSide: 160}))
	_, fs := split(events)
	if len(fs) != len(truth.Video.Frames) {
		t.Fatalf("%d frames, want %d", len(fs), len(truth.Video.Frames))
	}
	for i, e := range fs {
		if e.Frame.Width != 160 || e.Frame.Height != 90 || e.Time != time.Duration(truth.Video.Frames[i].TimeMS)*time.Millisecond {
			t.Errorf("frame %d: %dx%d at %v, want 160x90 at %d ms", i, e.Frame.Width, e.Frame.Height, e.Time, truth.Video.Frames[i].TimeMS)
		}
		checkPNG(t, e.Frame)
	}
}

// excerpt cuts the fixture's first d into a directory of the test's own, streams copied, and
// returns its path.
func excerpt(t *testing.T, d time.Duration) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "audio", "testdata", "speech-scenes.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg, err := tool.NewExec(t.Context(), audio.FFmpegSpec())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "excerpt.mkv")
	err = ffmpeg.Run(t.Context(), tool.Invocation{
		Args: []string{"-nostdin", "-loglevel", "error", "-i", src, "-t", d.String(), "-map", "0", "-c", "copy", path},
		Dir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPaced reads a 36 s excerpt of the fixture at real time through both runners, and checks
// that its events arrive as the source plays, each frame yielded within a second of its arrival:
// its first frame within a few seconds of the start; each frame after the one before by about
// their difference in source time; each window's last sample about when the source plays it; and
// its first window yielded once the stream passes its 30 s limit, well before the run ends. 36 s
// is about the shortest excerpt whose first window is yielded with seconds to spare before its
// end.
func TestPaced(t *testing.T) {
	const length = 36 * time.Second
	path := excerpt(t, length)
	truth := readTruth(t)
	frames := 0
	for _, f := range truth.Video.Frames {
		if time.Duration(f.TimeMS)*time.Millisecond < length {
			frames++
		}
	}
	for name, runner := range runners {
		t.Run(name, func(t *testing.T) {
			s := streamer(t, runner(t))
			start := time.Now()
			var events []media.Event
			var yielded []time.Duration
			for e, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: path, Paced: true}, Dir: t.TempDir()}) {
				if err != nil {
					t.Fatal(err)
				}
				// A frame is yielded as soon as its PNG is whole, within a second of its arrival. A
				// window is complete only once the stream passes its limit, after its last sample.
				if lag := time.Since(e.Arrived); e.Frame != nil && lag > time.Second {
					t.Errorf("the event at source time %v was yielded %v after it arrived", e.Time, lag)
				}
				events = append(events, e)
				yielded = append(yielded, time.Since(start))
			}
			total := time.Since(start)
			t.Logf("recorded: %d events over %v", len(events), total)
			var first time.Duration
			for i, e := range events {
				kind := "frame"
				if e.Window != nil {
					kind = "window"
					if e.Window.Index == 0 {
						first = yielded[i]
					}
				}
				t.Logf("recorded: %s at source time %v arrived at %v, yielded at %v", kind, e.Time, e.Arrived.Sub(start), yielded[i])
			}
			ws, fs := split(events)
			if len(fs) != frames || len(ws) != 2 {
				t.Fatalf("%d frames and %d windows, want %d and 2", len(fs), len(ws), frames)
			}
			// The first frame comes once the run has started, well before the source has played.
			if at := fs[0].Arrived.Sub(start); at > 5*time.Second {
				t.Errorf("the first frame arrived %v after the start", at)
			}
			for i := 1; i < len(fs); i++ {
				gap, played := fs[i].Arrived.Sub(fs[i-1].Arrived), fs[i].Time-fs[i-1].Time
				if gap < played-time.Second {
					t.Errorf("frame %d arrived %v after frame %d, %v apart in the source", i, gap, i-1, played)
				}
			}
			// Each window's last sample arrives about when the source plays it.
			for _, e := range ws {
				if at := e.Arrived.Sub(start); at < e.Window.Span.End-2*time.Second || at > e.Window.Span.End+2*time.Second {
					t.Errorf("window %d's last sample, at %v, arrived %v after the start", e.Window.Index, e.Window.Span.End, at)
				}
			}
			// The first window is yielded once the stream passes its limit, well before the run ends.
			if first < audio.WindowLimit-2*time.Second || first > total-3*time.Second {
				t.Errorf("the first window was yielded %v after the start, the run %v long; want it past its limit and before the end", first, total)
			}
			if total < length-time.Second {
				t.Errorf("the run took %v, under the source's %v", total, length)
			}
		})
	}
}

// TestCancel reads the fixture at real time through both runners and ends the context once the
// first frame is yielded: the stream yields the context's error once and ends, well before the
// source has played, and the media directory holds the one frame yielded.
func TestCancel(t *testing.T) {
	path := fixture(t)
	for name, runner := range runners {
		t.Run(name, func(t *testing.T) {
			s := streamer(t, runner(t))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			dir := t.TempDir()
			start := time.Now()
			var events []media.Event
			var errs []error
			for e, err := range s.Events(ctx, media.Request{Source: media.Source{File: path, Paced: true}, Dir: dir}) {
				if err != nil {
					errs = append(errs, err)
					continue
				}
				events = append(events, e)
				cancel()
			}
			took := time.Since(start)
			t.Logf("recorded: cancelled after %d events; the stream ended %v after the start", len(events), took)
			if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
				t.Errorf("errors %v, want context.Canceled once", errs)
			}
			if took > 10*time.Second {
				t.Errorf("the stream ended %v after the start; the source is 50 s", took)
			}
			checkDir(t, dir, events)
			checkNoScratch(t, filepath.Dir(path))
		})
	}
}

// checkPNG checks that f's file is a PNG of its width and height, named by its SHA-256.
func checkPNG(t *testing.T, f *media.Frame) {
	t.Helper()
	b, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) || filepath.Base(f.Path) != f.SHA256+".png" || sum(b) != f.SHA256 {
		t.Errorf("frame %d's file %s isn't a PNG named by its SHA-256", f.Index, f.Path)
	}
}
