//go:build linux

package dispatch_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/mediatest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// The video stage's hermetic tests run media's Streamer over the audio tests' stand-ins, whose
// ffmpeg, given media's run, acts as mediaStandin says.
const (
	// videoPCMEnv is how many milliseconds of PCM the stand-in writes to its stdout when the run
	// has an audio output, each sample its index modulo 30011.
	videoPCMEnv = "DISPATCH_VIDEO_PCM_MS"
	// videoFramesEnv is a JSON array of the frames the stand-in takes when the run has a video
	// output, each a mediatest.Frame.
	videoFramesEnv = "DISPATCH_VIDEO_FRAMES"
	// videoFailEnv, when set, makes the stand-in, once it has written its output, write its value
	// to stderr and exit 1.
	videoFailEnv = "DISPATCH_VIDEO_FAIL"
	// videoHoldEnv, when set, is how many milliseconds the stand-in sleeps once it has written
	// its first frame.
	videoHoldEnv = "DISPATCH_VIDEO_HOLD"
	// videoArgsEnv names the file the stand-in appends its run's input and frame pattern to, a
	// line each.
	videoArgsEnv = "DISPATCH_VIDEO_ARGS"
)

// mediaStandin is the stand-in ffmpeg's run for media: it records its input and frame pattern, then
// writes videoPCMEnv's PCM to its stdout, when its arguments have an audio output, half a second
// at a time, and videoFramesEnv's frames, when they have a video output, its last argument the
// image2 pattern, each reported on stderr as the metadata filter does and written before the PCM
// from its time on.
func mediaStandin(args []string) int {
	if err := mediaStandinRun(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if msg := os.Getenv(videoFailEnv); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	return 0
}

// mediaStandinRun is mediaStandin's output.
func mediaStandinRun(args []string) error {
	pattern := ""
	if slices.Contains(args, "image2") {
		pattern = args[len(args)-1]
	}
	if f, err := os.OpenFile(os.Getenv(videoArgsEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(f, "%s\n%s\n", args[slices.Index(args, "-i")+1], pattern)
		_ = f.Close()
	}
	var pcm []byte
	if slices.Contains(args, "pipe:1") {
		ms, err := strconv.Atoi(os.Getenv(videoPCMEnv))
		if err != nil {
			return err
		}
		n := ms * audio.SampleRate / 1000
		for i := range n {
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(i%30011))
		}
	}
	var frames []mediatest.Frame
	if pattern != "" {
		if err := json.Unmarshal([]byte(os.Getenv(videoFramesEnv)), &frames); err != nil {
			return err
		}
	}
	return mediatest.Stream(pcm, frames,
		func(at, end int) error {
			_, err := os.Stdout.Write(pcm[at:end])
			return err
		},
		func(i int) error {
			mediatest.Report(i, frames[i])
			if err := mediatest.WritePNG(pattern, i, frames[i]); err != nil {
				return err
			}
			if v := os.Getenv(videoHoldEnv); v != "" && i == 0 {
				hold, _ := strconv.Atoi(v)
				time.Sleep(time.Duration(hold) * time.Millisecond)
			}
			return nil
		})
}

// ffprobe's reports of the stand-in videos: one with a video stream and an audio stream, one with
// video alone, one with audio alone, and one with neither, its one stream a subtitle's.
const (
	videoProbeBoth  = `{"streams":[{"index":0,"codec_type":"video"},{"index":1,"codec_type":"audio"}]}`
	videoProbeVideo = `{"streams":[{"index":0,"codec_type":"video"}]}`
	videoProbeAudio = `{"streams":[{"index":0,"codec_type":"audio"}]}`
	videoProbeNone  = `{"streams":[{"index":0,"codec_type":"subtitle"}]}`
)

// The inputs the video tests dispatch, by their signatures, each routed route.Video: a Matroska,
// a WebM, and an AVI file; the MP4 is the audio tests' mp4Bytes.
const (
	mkvBytes  = "\x1a\x45\xdf\xa3\x9f\x42\x82\x88matroska" + "stand-in Matroska"
	webmBytes = "\x1a\x45\xdf\xa3\x9f\x42\x82\x84webm" + "stand-in WebM"
	aviBytes  = "RIFF\x24\x00\x00\x00AVI LIST" + "stand-in AVI"
)

// videoFrames are the stand-in's frames: the first, a scene change at 1.2 s, and one taken at
// 11.2 s that is past no threshold, which media names an interval frame, the gap since the last
// being 10 s.
var videoFrames = []mediatest.Frame{
	{MS: 0, Score: 0, Width: 32, Height: 18},
	{MS: 1200, Score: 0.9, Width: 32, Height: 18},
	{MS: 11200, Score: 0.05, Width: 32, Height: 18},
}

// videoStage installs the stand-ins, ffprobe reporting probe, ffmpeg writing 3 s of PCM and
// videoFrames, and returns a video stage over them under req.
func videoStage(t *testing.T, probe string, req media.Request) *dispatch.VideoStage {
	t.Helper()
	ffmpeg, ffprobe := standinTools(t, probe)
	frames, err := json.Marshal(videoFrames)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(videoPCMEnv, "3000")
	t.Setenv(videoFramesEnv, string(frames))
	t.Setenv(videoArgsEnv, filepath.Join(t.TempDir(), "args"))
	s, err := media.NewStreamer(media.Config{FFmpeg: ffmpeg, FFprobe: ffprobe})
	if err != nil {
		t.Fatal(err)
	}
	st, err := dispatch.NewVideoStage(dispatch.VideoConfig{Streamer: s, Version: "ffmpeg 9.0.2, ffprobe 9.0.2", Request: req})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// videoArgs returns each media run's input and frame pattern, as the stand-in recorded them.
func videoArgs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(os.Getenv(videoArgsEnv))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// videoDispatch dispatches the file at path through a dispatcher over stages in a working
// directory of its own, which it returns, with its events. Each image output's PNG and each
// audio output's WAV is checked while its part is in flight, and each must be inside the working
// directory.
func videoDispatch(t *testing.T, stages dispatch.Stages, path string) ([]dispatched, string) {
	t.Helper()
	work := t.TempDir()
	d, err := dispatch.New(dispatch.Config{Stages: stages, Index: dispatch.NewMemoryIndex(), Dir: work})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, d.Dispatch(t.Context(), dispatch.Input{Path: path}), func(ev *dispatch.Event) {
		o := ev.Output
		if o == nil || o.Path == "" {
			return
		}
		if !strings.HasPrefix(o.Path, work+string(filepath.Separator)) {
			t.Errorf("%s at %s, outside the working directory %s", o.Name, o.Path, work)
		}
		switch o.Kind {
		case dispatch.ImageOutput:
			checkFrame(t, *o)
		case dispatch.AudioOutput:
			checkStandinWAV(t, *o)
		}
	})
	return events, work
}

// checkFrame fails the test unless the file at o.Path, read while its part is in flight, is a PNG
// of o's Meta's size, named by its SHA-256.
func checkFrame(t *testing.T, o dispatch.Output) {
	t.Helper()
	b, err := os.ReadFile(o.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if name := hex.EncodeToString(sum[:]) + ".png"; filepath.Base(o.Path) != name {
		t.Errorf("%s: named %s, not by its SHA-256", o.Name, filepath.Base(o.Path))
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || strconv.Itoa(cfg.Width) != o.Meta["width"] || strconv.Itoa(cfg.Height) != o.Meta["height"] {
		t.Errorf("%s: %dx%d, %v; want a PNG of %sx%s", o.Name, cfg.Width, cfg.Height, err, o.Meta["width"], o.Meta["height"])
	}
}

// wantClean fails the test unless the working directory work is empty and the input at path is
// alone in its directory, as the caller left it.
func wantClean(t *testing.T, work, path string) {
	t.Helper()
	if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
		t.Errorf("the working directory holds %v, %v", entries, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Errorf("the input's directory holds %v, %v; want the input alone", entries, err)
	}
}

// kinds returns each output's kind and name.
func kinds(outs []dispatch.Output) []string {
	var got []string
	for _, o := range outs {
		got = append(got, string(o.Kind)+" "+o.Name)
	}
	return got
}

// TestVideoStage: a Matroska file's plan comes first, a record of its windows' and frames'
// settings, defaults applied; then its frames, PNGs, and its window, a WAV, each with its Meta.
// ffmpeg reads a link to the input in the part's directory and writes its frames there, never
// beside the input, and the working directory is empty once the dispatch is done.
func TestVideoStage(t *testing.T) {
	stage := videoStage(t, videoProbeBoth, media.Request{})
	path := writeInput(t, "talk.mkv", mkvBytes)
	events, work := videoDispatch(t, dispatch.Stages{route.Video: stage}, path)
	wantSteps(t, events, []step{{"talk.mkv", 0, dispatch.Prepared}})
	ev := events[0]
	if ev.Part.Route != route.Video || ev.Stage != stage.Identity() || stage.Identity().Name != "video" {
		t.Errorf("the part's route %s, stage %v", ev.Part.Route, ev.Stage)
	}
	got := kinds(ev.Outputs)
	if len(got) == 0 || got[0] != "record plan" {
		t.Fatalf("outputs %q, want the plan first", got)
	}
	// The frames and the window interleave as the run completes them.
	slices.Sort(got[1:])
	want := []string{"record plan", "audio window 0", "image frame 0", "image frame 1", "image frame 2"}
	if !slices.Equal(got, want) {
		t.Fatalf("outputs %q, want %q", got, want)
	}

	var p dispatch.VideoPlan
	if err := json.Unmarshal(ev.Outputs[0].Value, &p); err != nil {
		t.Fatal(err)
	}
	wantPlan := dispatch.VideoPlan{
		Windows: dispatch.VideoWindowing{
			Detector: "tracker", Limit: 30, Overlap: 2, MinSilence: 0.3,
			Percentile: audio.DefaultPercentile, FloorWindow: audio.DefaultFloorWindow.Seconds(), Margin: audio.DefaultMargin,
		},
		Frames: dispatch.VideoSampling{SceneThreshold: media.DefaultSceneThreshold, MaxGap: 10},
	}
	if p != wantPlan || ev.Outputs[0].MediaType != "application/json" {
		t.Errorf("the plan %+v, want %+v", p, wantPlan)
	}

	wantMeta := map[string]map[string]string{
		"frame 0": {"frame": "0", "time": "0", "reason": "first", "width": "32", "height": "18"},
		"frame 1": {"frame": "1", "time": "1.2", "reason": "scene", "width": "32", "height": "18"},
		"frame 2": {"frame": "2", "time": "11.2", "reason": "interval", "width": "32", "height": "18"},
	}
	for _, o := range ev.Outputs[1:] {
		if o.Kind == dispatch.AudioOutput {
			threshold, err := strconv.ParseFloat(o.Meta["threshold"], 64)
			if o.MediaType != "audio/wav" || err != nil || threshold >= 0 {
				t.Errorf("%s: %s, threshold %q", o.Name, o.MediaType, o.Meta["threshold"])
			}
			delete(o.Meta, "threshold")
			want := map[string]string{"window": "0", "start": "0", "end": "3", "cut": "end", "samples": strconv.Itoa(samplesOf(0, 3))}
			if !maps.Equal(o.Meta, want) {
				t.Errorf("%s's Meta %v, want %v", o.Name, o.Meta, want)
			}
			continue
		}
		if o.MediaType != "image/png" || !maps.Equal(o.Meta, wantMeta[o.Name]) {
			t.Errorf("%s: %s, Meta %v; want image/png, %v", o.Name, o.MediaType, o.Meta, wantMeta[o.Name])
		}
	}

	args := videoArgs(t)
	if len(args) != 2 {
		t.Fatalf("the runs' input and pattern %q, want one run's", args)
	}
	in, pattern := args[0], args[1]
	if filepath.Dir(in) == filepath.Dir(path) || !strings.HasPrefix(in, work+string(filepath.Separator)) {
		t.Errorf("ffmpeg read %s, not a link in the working directory %s", in, work)
	}
	if filepath.Dir(filepath.Dir(pattern)) != filepath.Dir(in) || !strings.HasPrefix(filepath.Base(filepath.Dir(pattern)), ".media-") {
		t.Errorf("ffmpeg wrote its frames to %s, not media's directory beside %s", pattern, in)
	}
	wantClean(t, work, path)
}

// TestVideoStageRoutes: an MP4, a Matroska, a WebM, and an AVI file are each routed video and
// prepared by the video stage.
func TestVideoStageRoutes(t *testing.T) {
	stage := videoStage(t, videoProbeBoth, media.Request{})
	for _, in := range []struct{ name, content string }{
		{"a.mp4", mp4Bytes}, {"a.mkv", mkvBytes}, {"a.webm", webmBytes}, {"a.avi", aviBytes},
	} {
		events, _ := videoDispatch(t, dispatch.Stages{route.Video: stage}, writeInput(t, in.name, in.content))
		wantSteps(t, events, []step{{in.name, 0, dispatch.Prepared}})
		if ev := events[0]; ev.Part.Route != route.Video || len(ev.Outputs) != 5 {
			t.Errorf("%s: route %s, %d outputs", in.name, ev.Part.Route, len(ev.Outputs))
		}
	}
}

// TestVideoStageTracks: a video with no audio stream yields its plan and frames only, its run
// writing no PCM; one with no video stream, such as an MP4 holding audio alone, its plan and
// windows only.
func TestVideoStageTracks(t *testing.T) {
	for _, tc := range []struct {
		name, probe string
		want        []string
	}{
		{"video only", videoProbeVideo, []string{"record plan", "image frame 0", "image frame 1", "image frame 2"}},
		{"audio only", videoProbeAudio, []string{"record plan", "audio window 0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := videoStage(t, tc.probe, media.Request{})
			path := writeInput(t, "clip.mp4", mp4Bytes)
			events, work := videoDispatch(t, dispatch.Stages{route.Video: stage}, path)
			wantSteps(t, events, []step{{"clip.mp4", 0, dispatch.Prepared}})
			if got := kinds(events[0].Outputs); !slices.Equal(got, tc.want) {
				t.Errorf("outputs %q, want %q", got, tc.want)
			}
			wantClean(t, work, path)
		})
	}
}

// TestVideoNoStream: a video file holding neither an audio nor a video stream fails, its error
// wrapping media.ErrNoMedia, after ffprobe alone ran: it has no outputs, the plan among them.
func TestVideoNoStream(t *testing.T) {
	index := dispatch.NewMemoryIndex()
	d := newDispatcher(t, dispatch.Stages{route.Video: videoStage(t, videoProbeNone, media.Request{})}, index)
	events := dispatchAll(t, d, writeInput(t, "subtitles.mkv", mkvBytes), nil)
	wantSteps(t, events, []step{{"subtitles.mkv", 0, dispatch.Failed}})
	ev := events[0]
	if !errors.Is(ev.Err, media.ErrNoMedia) || len(ev.Outputs) != 0 {
		t.Errorf("err %v, %d outputs; want media.ErrNoMedia, none", ev.Err, len(ev.Outputs))
	}
	if runs := audioRuns(t); !slices.Equal(runs, []string{"ffprobe"}) {
		t.Errorf("runs %q, want ffprobe alone", runs)
	}
	if _, ok, err := index.Lookup(t.Context(), dispatch.Key{SHA256: ev.Part.SHA256, Stage: ev.Stage}); err != nil || ok {
		t.Errorf("the failed part's entry is recorded: %v, %v", ok, err)
	}
}

// TestVideoFailedRun: a run that fails ends the part failed, its error ffmpeg's last line, after
// the plan and the frames and window it completed, and leaves nothing behind.
func TestVideoFailedRun(t *testing.T) {
	stage := videoStage(t, videoProbeBoth, media.Request{})
	t.Setenv(videoFailEnv, "[vist#0:0/mpeg4] Error while decoding stream: Invalid data found")
	path := writeInput(t, "broken.mkv", mkvBytes)
	events, work := videoDispatch(t, dispatch.Stages{route.Video: stage}, path)
	wantSteps(t, events, []step{{"broken.mkv", 0, dispatch.Failed}})
	ev := events[0]
	if !strings.Contains(ev.Reason, "Invalid data found") || len(ev.Outputs) == 0 || ev.Outputs[0].Name != "plan" {
		t.Errorf("reason %q, outputs %q; want ffmpeg's last line, after the plan", ev.Reason, kinds(ev.Outputs))
	}
	wantClean(t, work, path)
}

// TestVideoCancel: a context that ends while the run is held stops ffmpeg; the dispatch yields
// the context's error last, and nothing is left behind.
func TestVideoCancel(t *testing.T) {
	stage := videoStage(t, videoProbeBoth, media.Request{})
	t.Setenv(videoHoldEnv, "30000")
	path := writeInput(t, "held.mkv", mkvBytes)
	work := t.TempDir()
	d, err := dispatch.New(dispatch.Config{Stages: dispatch.Stages{route.Video: stage}, Index: dispatch.NewMemoryIndex(), Dir: work})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Now()
	var last error
	var outputs []string
	for ev, err := range d.Dispatch(ctx, dispatch.Input{Path: path}) {
		if err != nil {
			last = err
			continue
		}
		if ev.Output != nil {
			outputs = append(outputs, ev.Output.Name)
			if ev.Output.Kind == dispatch.ImageOutput {
				cancel()
			}
		}
	}
	if !errors.Is(last, context.Canceled) || !slices.Equal(outputs, []string{"plan", "frame 0"}) {
		t.Errorf("last error %v, outputs %q; want context.Canceled after the plan and frame 0", last, outputs)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("the dispatch took %v: ffmpeg wasn't stopped", elapsed)
	}
	wantClean(t, work, path)
}

// TestVideoStageInArchive: an AVI in a ZIP is found by the archive stage and prepared by the video
// stage, as any part is; ffmpeg reads a link to the entry in the AVI's own directory.
func TestVideoStageInArchive(t *testing.T) {
	zs, err := dispatch.NewArchiveStage(dispatch.ArchiveConfig{})
	if err != nil {
		t.Fatal(err)
	}
	stages := dispatch.Stages{route.Video: videoStage(t, videoProbeBoth, media.Request{}), route.Archive: zs}
	path := writeInput(t, "clips.zip", zipBytes(t, zent{name: "clips/a.avi", body: aviBytes}))
	events, work := videoDispatch(t, stages, path)
	wantSteps(t, events, []step{{"clips/a.avi", 1, dispatch.Prepared}, {"clips.zip", 0, dispatch.Prepared}})
	if n := len(events[0].Outputs); n != 5 {
		t.Errorf("the AVI's %d outputs, want its plan, three frames, and a window", n)
	}
	wantClean(t, work, path)
}

// TestVideoNoProcessor: with no video stage, as a caller with no ffmpeg and ffprobe registers, a
// video part is no processor yet, and nothing runs.
func TestVideoNoProcessor(t *testing.T) {
	videoStage(t, videoProbeBoth, media.Request{})
	d := newDispatcher(t, dispatch.Stages{}, dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, writeInput(t, "talk.webm", webmBytes), nil)
	wantSteps(t, events, []step{{"talk.webm", 0, dispatch.NoProcessor}})
	if ev := events[0]; ev.Part.Route != route.Video || ev.Reason != "no stage for route video" {
		t.Errorf("route %s, reason %q", ev.Part.Route, ev.Reason)
	}
	if runs := audioRuns(t); len(runs) != 0 {
		t.Errorf("the video part ran %q", runs)
	}
}

// TestVideoStageIdentity: the video stage hashes its windows' and frames' settings, defaults
// applied, and not its Source or Dir; it refuses no streamer, no version, and a windowing field
// audio refuses.
func TestVideoStageIdentity(t *testing.T) {
	ffmpeg, ffprobe := standinTools(t, videoProbeBoth)
	s, err := media.NewStreamer(media.Config{FFmpeg: ffmpeg, FFprobe: ffprobe})
	if err != nil {
		t.Fatal(err)
	}
	const version = "ffmpeg 9.0.2, ffprobe 9.0.2"
	id := func(req media.Request) dispatch.Identity {
		t.Helper()
		st, err := dispatch.NewVideoStage(dispatch.VideoConfig{Streamer: s, Version: version, Request: req})
		if err != nil {
			t.Fatal(err)
		}
		return st.Identity()
	}
	base := id(media.Request{})
	if base.Name != "video" || base.Version != version || len(base.Config) != 64 {
		t.Errorf("identity %v", base)
	}
	same := media.Request{
		Source: media.Source{File: "elsewhere.mkv", Paced: true, Tracks: media.TracksVideo}, Dir: "elsewhere",
		Overlap: audio.DefaultOverlap, MinSilence: audio.DefaultMinSilence, Percentile: audio.DefaultPercentile,
		FloorWindow: audio.DefaultFloorWindow, Margin: audio.DefaultMargin,
		SceneThreshold: media.DefaultSceneThreshold, MaxGap: media.DefaultMaxGap,
	}
	if id(same) != base {
		t.Error("the defaults, a Source, or a Dir changed the video identity")
	}
	for name, req := range map[string]media.Request{
		"overlap":         {Overlap: time.Second},
		"min silence":     {MinSilence: time.Second},
		"threshold":       {Threshold: -40},
		"percentile":      {Percentile: 20},
		"floor window":    {FloorWindow: 8 * time.Second},
		"margin":          {Margin: 6},
		"scene threshold": {SceneThreshold: 0.4},
		"max gap":         {MaxGap: 5 * time.Second},
		"max side":        {MaxSide: 768},
	} {
		if id(req).Config == base.Config {
			t.Errorf("%s left the video identity's config unchanged", name)
		}
	}
	for name, cfg := range map[string]dispatch.VideoConfig{
		"no streamer":            {Version: version},
		"no version":             {Streamer: s},
		"overlap of the limit":   {Streamer: s, Version: version, Request: media.Request{Overlap: audio.WindowLimit / 2}},
		"threshold and a margin": {Streamer: s, Version: version, Request: media.Request{Threshold: -40, Margin: 6}},
	} {
		if _, err := dispatch.NewVideoStage(cfg); err == nil {
			t.Errorf("%s: a video stage was built", name)
		}
	}
}
