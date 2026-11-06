//go:build linux

package media_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The stand-in ffprobe's reports: a source with video and audio, as the fixture is; one with audio
// only; one with video only; and one whose only video is an attached picture.
const (
	probeBoth    = `{"streams":[{"index":0,"codec_type":"video","disposition":{"attached_pic":0}},{"index":1,"codec_type":"audio","disposition":{"attached_pic":0}}]}`
	probeAudio   = `{"streams":[{"index":0,"codec_type":"audio","disposition":{"attached_pic":0}}]}`
	probeVideo   = `{"streams":[{"index":0,"codec_type":"video","disposition":{"attached_pic":0}}]}`
	probePicture = `{"streams":[{"index":0,"codec_type":"video","disposition":{"attached_pic":1}},{"index":1,"codec_type":"data"}]}`
)

// sceneFrames are the frames the stand-in takes, the fixture's under the default policy, their
// scores as select gives them; the frame at 31 s scores the threshold exactly, as select's printed
// score rounds one just past it, before the gap has passed, so it is a scene change.
var sceneFrames = []standinFrame{
	{MS: 0, Score: 0, Width: 32, Height: 18},
	{MS: 6000, Score: 1, Width: 32, Height: 18},
	{MS: 9000, Score: 0.9, Width: 32, Height: 18},
	{MS: 19000, Score: 0, Width: 32, Height: 18},
	{MS: 29000, Score: 0.01, Width: 32, Height: 18},
	{MS: 31000, Score: media.DefaultSceneThreshold, Width: 32, Height: 18},
	{MS: 37000, Score: 0.5, Width: 32, Height: 18},
	{MS: 47000, Score: 0, Width: 32, Height: 18},
}

// sceneReasons are sceneFrames' reasons.
var sceneReasons = []media.Reason{
	media.ReasonFirst, media.ReasonScene, media.ReasonScene, media.ReasonInterval,
	media.ReasonInterval, media.ReasonScene, media.ReasonScene, media.ReasonInterval,
}

// gainWAV is the gain-step fixture, 75.23 s of canonical WAV, whose samples the stand-in writes.
var gainWAV = filepath.Join("..", "audio", "testdata", "speech-gain.wav")

// source makes an empty source file in a directory of the test's own, which the stand-in never
// reads, and returns its path.
func source(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setStandin sets what the stand-ins report and write: probe's streams, the PCM of gainWAV, and
// frames.
func setStandin(t *testing.T, probe string, frames []standinFrame) {
	t.Helper()
	t.Setenv(probeEnv, probe)
	pcm, err := filepath.Abs(gainWAV)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(pcmEnv, pcm)
	b, err := json.Marshal(frames)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(framesEnv, string(b))
}

// collect ranges over a stream to its end, failing the test on any error.
func collect(t *testing.T, events func(func(media.Event, error) bool)) []media.Event {
	t.Helper()
	var out []media.Event
	for e, err := range events {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// split returns events' window events and frame events, each in the order yielded.
func split(events []media.Event) (windows, frames []media.Event) {
	for _, e := range events {
		switch {
		case e.Window != nil && e.Frame == nil:
			windows = append(windows, e)
		case e.Frame != nil && e.Window == nil:
			frames = append(frames, e)
		}
	}
	return windows, frames
}

// sum returns the lowercase hex SHA-256 of b.
func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// checkDir checks that dir holds exactly the files of events, by name.
func checkDir(t *testing.T, dir string, events []media.Event) {
	t.Helper()
	var want []string
	for _, e := range events {
		switch {
		case e.Window != nil:
			want = append(want, filepath.Base(e.Window.Path))
		case e.Frame != nil:
			want = append(want, filepath.Base(e.Frame.Path))
		}
	}
	slices.Sort(want)
	want = slices.Compact(want)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Errorf("the media directory holds %q, want the events' %q", got, want)
	}
}

// checkNoScratch checks that the source's directory, dir, holds no run directory of frames.
func checkNoScratch(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("the source's directory holds %s", e.Name())
		}
	}
}

// expectedWindows plans the gain fixture's windows as audio's tracker and planner do over its
// samples, at req's settings, and returns them with each one's WAV.
func expectedWindows(t *testing.T, req audio.Request) ([]audio.Window, [][]byte) {
	t.Helper()
	windows, wavs, _ := expectedThresholds(t, req)
	return windows, wavs
}

// frameBytes is the bytes of the tracker's frame of PCM.
const frameBytes = 2 * audio.SampleRate * int(audio.FrameLength) / int(time.Second)

// expectedThresholds is expectedWindows, and the threshold each window was cut under, as the
// tracker, fed a frame at a time, gives them: the fixed threshold; for a window cut at a silence,
// the one that silence, taken up to the window's limit, opened under; for one cut at the limit,
// the one in force after its last sample's frame; and for the last, the one in force at the end.
func expectedThresholds(t *testing.T, req audio.Request) ([]audio.Window, [][]byte, []float64) {
	t.Helper()
	data, err := os.ReadFile(gainWAV)
	if err != nil {
		t.Fatal(err)
	}
	pcm := data[44:]
	tr, err := audio.NewTracker(req)
	if err != nil {
		t.Fatal(err)
	}
	var silences []audio.Silence
	var after []float64
	for at := 0; at < len(pcm); at += frameBytes {
		silences = append(silences, tr.Feed(pcm[at:min(at+frameBytes, len(pcm))])...)
		after = append(after, tr.Threshold())
	}
	silences = append(silences, tr.End()...)
	var spans []audio.Span
	for _, s := range silences {
		spans = append(spans, s.Span)
	}
	plan, err := audio.PlanWindows(tr.Duration(), spans, req)
	if err != nil {
		t.Fatal(err)
	}
	var wavs [][]byte
	var thresholds []float64
	for _, w := range plan.Windows {
		wavs = append(wavs, audio.WAV(pcm[2*audio.Sample(w.Start):2*audio.Sample(w.End)]))
		threshold := tr.Threshold()
		switch w.Cut {
		case audio.CutLimit:
			threshold = after[(audio.Sample(w.End)-1)*2/int64(frameBytes)]
		case audio.CutSilence:
			for _, s := range silences {
				end := min(s.End, w.Start+audio.WindowLimit)
				if s.Start >= w.Start && s.Start+(end-s.Start)/2 == w.End {
					threshold = s.Threshold
				}
			}
		}
		thresholds = append(thresholds, threshold)
	}
	return plan.Windows, wavs, thresholds
}

// TestNewStreamer refuses a missing tool and one older than 9.0.
func TestNewStreamer(t *testing.T) {
	installStandins(t, "8.1")
	cfg := standinConfig(t)
	if _, err := media.NewStreamer(media.Config{FFprobe: cfg.FFprobe}); err == nil {
		t.Error("no ffmpeg: no error")
	}
	if _, err := media.NewStreamer(media.Config{FFmpeg: cfg.FFmpeg}); err == nil {
		t.Error("no ffprobe: no error")
	}
	var verr *tool.VersionError
	if _, err := media.NewStreamer(cfg); !errors.As(err, &verr) || !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("ffmpeg 8.1: %v, want a VersionError wrapping ErrTooOld", err)
	}
}

// TestEventsRefused refuses each invalid request with ErrRequest, once, before anything runs.
func TestEventsRefused(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeBoth, sceneFrames)
	path := source(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ok := media.Request{Source: media.Source{File: path}, Dir: dir}
	for name, change := range map[string]func(*media.Request){
		"no source":            func(r *media.Request) { r.Source = media.Source{} },
		"paced without a file": func(r *media.Request) { r.Source = media.Source{Paced: true} },
		"paced reader":         func(r *media.Request) { r.Source = media.Source{Reader: strings.NewReader("x"), Paced: true} },
		"paced URL":            func(r *media.Request) { r.Source = media.Source{URL: "srt://127.0.0.1:9000", Paced: true} },
		"file and reader":      func(r *media.Request) { r.Source.Reader = strings.NewReader("x") },
		"file and URL":         func(r *media.Request) { r.Source.URL = "udp://127.0.0.1:9000" },
		"reader and URL": func(r *media.Request) {
			r.Source = media.Source{Reader: strings.NewReader("x"), URL: "udp://127.0.0.1:9000"}
		},
		"tracks below":         func(r *media.Request) { r.Source.Tracks = -1 },
		"tracks above":         func(r *media.Request) { r.Source.Tracks = media.TracksVideo + 1 },
		"rtsp":                 func(r *media.Request) { r.Source = media.Source{URL: "rtsp://127.0.0.1:8554/live"} },
		"RTSP":                 func(r *media.Request) { r.Source = media.Source{URL: "RTSP://127.0.0.1:8554/live"} },
		"rtsps":                func(r *media.Request) { r.Source = media.Source{URL: "rtsps://127.0.0.1:8322/live"} },
		"file URL":             func(r *media.Request) { r.Source = media.Source{URL: "file:///etc/passwd"} },
		"rtmps":                func(r *media.Request) { r.Source = media.Source{URL: "rtmps://127.0.0.1/live/s"} },
		"tcp":                  func(r *media.Request) { r.Source = media.Source{URL: "tcp://127.0.0.1:9000"} },
		"crypto":               func(r *media.Request) { r.Source = media.Source{URL: "crypto+http://127.0.0.1/s.ts"} },
		"no scheme":            func(r *media.Request) { r.Source = media.Source{URL: "127.0.0.1:9000"} },
		"no host":              func(r *media.Request) { r.Source = media.Source{URL: "http:///live.m3u8"} },
		"opaque":               func(r *media.Request) { r.Source = media.Source{URL: "udp:127.0.0.1:9000"} },
		"unparsable":           func(r *media.Request) { r.Source = media.Source{URL: "http://[::1"} },
		"no media directory":   func(r *media.Request) { r.Dir = "" },
		"missing directory":    func(r *media.Request) { r.Dir = filepath.Join(dir, "missing") },
		"directory is a file":  func(r *media.Request) { r.Dir = file },
		"overlap":              func(r *media.Request) { r.Overlap = audio.WindowLimit / 2 },
		"min silence":          func(r *media.Request) { r.MinSilence = -time.Second },
		"threshold above 0":    func(r *media.Request) { r.Threshold = 3 },
		"threshold too low":    func(r *media.Request) { r.Threshold = -121 },
		"threshold and margin": func(r *media.Request) { r.Threshold, r.Margin = -40, 6 },
		"percentile":           func(r *media.Request) { r.Percentile = 100 },
		"floor window":         func(r *media.Request) { r.FloorWindow = time.Millisecond },
		"margin":               func(r *media.Request) { r.Margin = -1 },
		"scene threshold":      func(r *media.Request) { r.SceneThreshold = 1 },
		"negative scene":       func(r *media.Request) { r.SceneThreshold = -0.1 },
		"NaN scene":            func(r *media.Request) { r.SceneThreshold = math.NaN() },
		"max gap":              func(r *media.Request) { r.MaxGap = -time.Second },
		"max side":             func(r *media.Request) { r.MaxSide = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			req := ok
			change(&req)
			var errs []error
			for e, err := range s.Events(t.Context(), req) {
				if err == nil || e.Window != nil || e.Frame != nil {
					t.Fatalf("event %+v, %v; want only the refusal", e, err)
				}
				errs = append(errs, err)
			}
			if len(errs) != 1 || !errors.Is(errs[0], media.ErrRequest) {
				t.Errorf("errors %v, want ErrRequest once", errs)
			}
		})
	}
	if r := runs(t); len(r) != 0 {
		t.Errorf("runs %v, want none", r)
	}
}

// TestEventsNoMedia refuses a source whose only video is an attached picture with ErrNoMedia,
// after its probe and before ffmpeg runs.
func TestEventsNoMedia(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probePicture, nil)
	var errs []error
	for _, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: source(t)}, Dir: t.TempDir()}) {
		errs = append(errs, err)
	}
	if len(errs) != 1 || !errors.Is(errs[0], media.ErrNoMedia) {
		t.Errorf("errors %v, want ErrNoMedia once", errs)
	}
	if r := runs(t); len(r) != 1 || r[0].Program != "ffprobe" {
		t.Errorf("runs %v, want the probe alone", r)
	}
}

// TestEventsStandin streams a source with audio and video, unpaced and paced, through the
// stand-in: its frames' reasons, times, and sizes, in order, each its PNG named by its SHA-256;
// its windows, in plan order, the tracker's and planner's over the same PCM, each the same WAV
// bytes, with its seams and the threshold it was cut under; ffmpeg's one run and its arguments;
// and a media directory holding exactly the events' files, the run's directory of frames gone.
func TestEventsStandin(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeBoth, sceneFrames)
	wantWindows, wavs, wantThresholds := expectedThresholds(t, audio.Request{})
	for _, paced := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpaced", true: "paced"}[paced], func(t *testing.T) {
			path := source(t)
			dir := t.TempDir()
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{File: path, Paced: paced}, Dir: dir}))
			ws, fs := split(events)
			if len(ws)+len(fs) != len(events) {
				t.Fatalf("%d events neither or both a window and a frame", len(events)-len(ws)-len(fs))
			}
			if len(fs) != len(sceneFrames) {
				t.Fatalf("%d frames, want %d", len(fs), len(sceneFrames))
			}
			for i, e := range fs {
				f := e.Frame
				if f.Index != i || f.Reason != sceneReasons[i] || e.Time != time.Duration(sceneFrames[i].MS)*time.Millisecond ||
					f.Width != 32 || f.Height != 18 {
					t.Errorf("frame %d: %d, %s at %v, %dx%d; want %s at %d ms, 32x18", i, f.Index, f.Reason, e.Time, f.Width, f.Height,
						sceneReasons[i], sceneFrames[i].MS)
				}
				b, err := os.ReadFile(f.Path)
				if err != nil {
					t.Fatal(err)
				}
				if filepath.Base(f.Path) != f.SHA256+".png" || sum(b) != f.SHA256 || filepath.Dir(f.Path) != dir {
					t.Errorf("frame %d's file %s isn't named by its SHA-256 in the media directory", i, f.Path)
				}
				if e.Arrived.IsZero() {
					t.Errorf("frame %d has no arrival time", i)
				}
			}
			if len(ws) != len(wantWindows) {
				t.Fatalf("%d windows, want %d", len(ws), len(wantWindows))
			}
			for i, e := range ws {
				w, want := e.Window, wantWindows[i]
				if w.Index != i || w.Span != want.Span || w.Cut != want.Cut || e.Time != want.Start || w.Samples != (len(wavs[i])-44)/2 {
					t.Errorf("window %d: %d, %v %s, %d samples at %v; want %v %s, %d samples", i, w.Index, w.Span, w.Cut, w.Samples, e.Time,
						want.Span, want.Cut, (len(wavs[i])-44)/2)
				}
				b, err := os.ReadFile(w.Path)
				if err != nil {
					t.Fatal(err)
				}
				if string(b) != string(wavs[i]) || w.SHA256 != sum(b) || filepath.Base(w.Path) != w.SHA256+".wav" {
					t.Errorf("window %d's file %s isn't the planned samples' WAV, named by its SHA-256", i, w.Path)
				}
				var before, after audio.Span
				if i > 0 {
					before = audio.Span{Start: want.Start, End: wantWindows[i-1].End}
				}
				if i < len(wantWindows)-1 {
					after = audio.Span{Start: wantWindows[i+1].Start, End: want.End}
				}
				if w.SeamBefore != before || w.SeamAfter != after {
					t.Errorf("window %d's seams %v and %v, want %v and %v", i, w.SeamBefore, w.SeamAfter, before, after)
				}
				if w.Threshold != wantThresholds[i] || w.Threshold >= 0 || w.Threshold < -120 {
					t.Errorf("window %d, cut at the %s, names threshold %v dB, want %v dB", i, w.Cut, w.Threshold, wantThresholds[i])
				}
				if e.Arrived.IsZero() || (i > 0 && e.Arrived.Before(ws[i-1].Arrived)) {
					t.Errorf("window %d arrived at %v, before window %d", i, e.Arrived, i-1)
				}
			}
			checkDir(t, dir, events)
			checkNoScratch(t, filepath.Dir(path))
			r := runs(t)
			if len(r) != 2 || r[0].Program != "ffprobe" || r[1].Program != "ffmpeg" {
				t.Fatalf("runs %v, want the probe and one ffmpeg run", r)
			}
			args := strings.Join(r[1].Args, " ")
			for _, want := range []string{
				"-protocol_whitelist file -i " + path,
				"-map 0:1 -af aresample=16000 -ac 1 -ar 16000 -c:a pcm_s16le",
				"-f s16le pipe:1",
				"-map 0:0 -vf select='eq(n,0)+gt(scene,0.2)+gte(t-prev_selected_t,10)',settb=AVTB,metadata=mode=print:key=lavfi.scene_score,",
				"-fps_mode passthrough -c:v png",
				"-f image2 -start_number 0 -atomic_writing 1 " + filepath.Dir(path) + "/.media-",
			} {
				if !strings.Contains(args, want) {
					t.Errorf("ffmpeg's arguments %q lack %q", args, want)
				}
			}
			if strings.Contains(args, "-readrate 1 ") != paced {
				t.Errorf("ffmpeg's arguments %q; paced %v", args, paced)
			}
		})
	}
}

// TestEventsFixedThreshold streams the audio with a fixed threshold: every window names it.
func TestEventsFixedThreshold(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeAudio, nil)
	wantWindows, _ := expectedWindows(t, audio.Request{Threshold: -45})
	events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{File: source(t)}, Dir: t.TempDir(), Threshold: -45}))
	if len(events) != len(wantWindows) {
		t.Fatalf("%d events, want %d windows", len(events), len(wantWindows))
	}
	for i, e := range events {
		if e.Window == nil || e.Window.Span != wantWindows[i].Span || e.Window.Threshold != -45 {
			t.Errorf("event %d: %+v, want window %v at -45 dB", i, e.Window, wantWindows[i].Span)
		}
	}
}

// TestEventsLimitThreshold streams the gain fixture's audio with no silence long enough to end a
// window: each window cut at the limit names the rolling threshold its last sample's frame was
// decided against, and the last the one in force at the end, which the gain step moves.
func TestEventsLimitThreshold(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeAudio, nil)
	const minSilence = 20 * time.Second
	wantWindows, _, wantThresholds := expectedThresholds(t, audio.Request{MinSilence: minSilence})
	events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{File: source(t)}, Dir: t.TempDir(), MinSilence: minSilence}))
	if len(events) != len(wantWindows) || len(events) < 3 {
		t.Fatalf("%d events, want %d windows, at least 3", len(events), len(wantWindows))
	}
	for i, e := range events {
		w := e.Window
		if w == nil || w.Span != wantWindows[i].Span || w.Threshold != wantThresholds[i] || w.Threshold >= 0 {
			t.Errorf("event %d: %+v, want window %v cut at the %s under %v dB", i, w, wantWindows[i].Span, wantWindows[i].Cut, wantThresholds[i])
		}
	}
	if first, last := events[0].Window, events[len(events)-1].Window; first.Cut != audio.CutLimit || last.Cut != audio.CutEnd || first.Threshold == last.Threshold {
		t.Errorf("first window cut at the %s under %v dB, last at the %s under %v dB; want the limit and the end, the gain step between",
			first.Cut, first.Threshold, last.Cut, last.Threshold)
	}
}

// TestEventsOneStream streams a source with audio only, which yields windows only and runs no
// video output, and one with video only, which yields frames only and runs no audio output.
func TestEventsOneStream(t *testing.T) {
	s := standinStreamer(t)
	for _, c := range []struct {
		name, probe string
		windows     bool
	}{{"audio", probeAudio, true}, {"video", probeVideo, false}} {
		t.Run(c.name, func(t *testing.T) {
			setStandin(t, c.probe, sceneFrames)
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			dir := t.TempDir()
			events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{File: source(t)}, Dir: dir}))
			ws, fs := split(events)
			if c.windows && (len(ws) == 0 || len(fs) != 0) || !c.windows && (len(ws) != 0 || len(fs) != len(sceneFrames)) {
				t.Errorf("%d windows and %d frames", len(ws), len(fs))
			}
			checkDir(t, dir, events)
			r := runs(t)
			if len(r) != 2 {
				t.Fatalf("runs %v, want the probe and one ffmpeg run", r)
			}
			args := strings.Join(r[1].Args, " ")
			if strings.Contains(args, "pipe:1") != c.windows || strings.Contains(args, "image2") == c.windows {
				t.Errorf("ffmpeg's arguments %q", args)
			}
		})
	}
}

// TestEventsBreak stops the loop after the first event: the run stops, and the media directory
// holds that event's file alone.
func TestEventsBreak(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeBoth, sceneFrames)
	t.Setenv(holdEnv, "60000")
	path := source(t)
	dir := t.TempDir()
	start := time.Now()
	var events []media.Event
	for e, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: path}, Dir: dir}) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
		break
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("the loop returned %v after the start; the stand-in holds 60 s", took)
	}
	checkDir(t, dir, events)
	checkNoScratch(t, filepath.Dir(path))
}

// TestEventsCancel ends the context after the first event, with the stand-in holding its run
// open: the stream yields the context's error once and ends, and the media directory holds the
// one event's file.
func TestEventsCancel(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeBoth, sceneFrames)
	t.Setenv(holdEnv, "60000")
	path := source(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Now()
	var events []media.Event
	var errs []error
	for e, err := range s.Events(ctx, media.Request{Source: media.Source{File: path}, Dir: dir}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		events = append(events, e)
		cancel()
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("the stream ended %v after the start; the stand-in holds 60 s", took)
	}
	if len(events) != 1 || len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Errorf("%d events and errors %v, want one event and then context.Canceled once", len(events), errs)
	}
	checkDir(t, dir, events)
	checkNoScratch(t, filepath.Dir(path))
}

// TestEventsFail streams a run that fails once it has written its output: the stream yields every
// frame it wrote and the windows already fixed, and then the run's error, carrying ffmpeg's last
// line; the window still open when it failed isn't yielded.
func TestEventsFail(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeBoth, sceneFrames)
	t.Setenv(failEnv, "[in#0/matroska] Error during demuxing: Invalid data found")
	wantWindows, _ := expectedWindows(t, audio.Request{})
	dir := t.TempDir()
	var events []media.Event
	var errs []error
	for e, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: source(t)}, Dir: dir}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(errs) > 0 {
			t.Error("an event after the error")
		}
		events = append(events, e)
	}
	ws, fs := split(events)
	if len(fs) != len(sceneFrames) || len(ws) != len(wantWindows)-1 {
		t.Errorf("%d frames and %d windows, want %d and %d", len(fs), len(ws), len(sceneFrames), len(wantWindows)-1)
	}
	var exit *tool.ExitError
	if len(errs) != 1 || !errors.As(errs[0], &exit) || !strings.Contains(errs[0].Error(), "Invalid data found") {
		t.Errorf("errors %v, want ffmpeg's exit once, with its last line", errs)
	}
	checkDir(t, dir, events)
}

// TestEventsNoPNG streams a run that reports frames and writes no PNG for them: the stream ends
// with an error naming the first.
func TestEventsNoPNG(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probeVideo, sceneFrames)
	t.Setenv(noPNGEnv, "1")
	dir := t.TempDir()
	var errs []error
	for e, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: source(t)}, Dir: dir}) {
		if err == nil {
			t.Errorf("event %+v", e)
			continue
		}
		errs = append(errs, err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "no PNG for frame 0") {
		t.Errorf("errors %v, want the missing PNG once", errs)
	}
	checkDir(t, dir, nil)
}

// TestReasonString names each reason.
func TestReasonString(t *testing.T) {
	for r, want := range map[media.Reason]string{
		media.ReasonFirst: "first", media.ReasonScene: "scene", media.ReasonInterval: "interval", media.Reason(9): "Reason(9)",
	} {
		if got := r.String(); got != want {
			t.Errorf("%d: %q, want %q", int(r), got, want)
		}
	}
}
