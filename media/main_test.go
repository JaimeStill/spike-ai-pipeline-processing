//go:build linux

package media_test

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

// standinEnv marks a process as a stand-in: the test binary, re-executed through a link named
// ffmpeg or ffprobe, acting on its arguments as FFmpeg's tool would instead of running the tests.
const standinEnv = "MEDIA_TEST_STANDIN"

// The stand-ins' settings, which they read from the environment they inherit.
const (
	// versionEnv is the version either stand-in's -version prints, after an n.
	versionEnv = "MEDIA_TEST_VERSION"
	// logEnv names the file each run but a version command appends its program and arguments to,
	// one JSON object per line.
	logEnv = "MEDIA_TEST_LOG"
	// probeEnv is the report the stand-in ffprobe writes to stdout.
	probeEnv = "MEDIA_TEST_PROBE"
	// pcmEnv names a canonical 44-byte WAV whose samples the stand-in ffmpeg writes to its stdout,
	// when it has an audio output, half a second at a time.
	pcmEnv = "MEDIA_TEST_PCM"
	// framesEnv is a JSON array of the frames the stand-in ffmpeg takes, each a standinFrame,
	// when it has a video output: it reports each on stderr as the metadata filter does, and then
	// writes its PNG, each before the PCM from its time on.
	framesEnv = "MEDIA_TEST_FRAMES"
	// noPNGEnv, when set, makes the stand-in report its frames and write no PNG for them.
	noPNGEnv = "MEDIA_TEST_NO_PNG"
	// failEnv, when set, makes the stand-in ffmpeg, once it has written its output, write its
	// value to stderr and exit 1.
	failEnv = "MEDIA_TEST_FAIL"
	// holdEnv, when set, is how many milliseconds the stand-in ffmpeg sleeps once it has written
	// its output, before it exits.
	holdEnv = "MEDIA_TEST_HOLD"
)

// standinFrame is a frame the stand-in ffmpeg takes: its time, in milliseconds, its scene score,
// and its PNG's size.
type standinFrame struct {
	MS     int64   `json:"ms"`
	Score  float64 `json:"score"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
}

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		os.Exit(standin(filepath.Base(os.Args[0]), os.Args[1:]))
	}
	// The stand-in inherits the environment. Under -race, GORACE's atexit_sleep_ms keeps it from
	// sleeping a second as it exits.
	for key, value := range map[string]string{standinEnv: "1", "GORACE": "atexit_sleep_ms=0"} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// run is one stand-in run, as the log records it.
type run struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

// standin is the stand-in named name, ffmpeg or ffprobe.
func standin(name string, args []string) int {
	if name != "ffmpeg" && name != "ffprobe" {
		fmt.Fprintf(os.Stderr, "stand-in: no tool named %s\n", name)
		return 2
	}
	if len(args) == 1 && args[0] == "-version" {
		fmt.Printf("%s version n%s Copyright (c) 2000-2026 the FFmpeg developers\n", name, os.Getenv(versionEnv))
		return 0
	}
	if err := logRun(run{Program: name, Args: args}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if name == "ffprobe" {
		fmt.Print(os.Getenv(probeEnv))
		return 0
	}
	if err := standinRun(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if v := os.Getenv(holdEnv); v != "" {
		var ms int
		if _, err := fmt.Sscan(v, &ms); err != nil {
			return 2
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if msg := os.Getenv(failEnv); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	return 0
}

// standinRun is the stand-in ffmpeg's run: the PCM of pcmEnv's WAV to stdout, when its arguments
// have an audio output, half a second at a time; and the frames of framesEnv, when they have a
// video output, its last argument the image2 pattern, each reported and written before the PCM
// from its time on.
func standinRun(args []string) error {
	var pcm []byte
	if slices.Contains(args, "pipe:1") {
		data, err := os.ReadFile(os.Getenv(pcmEnv))
		if err != nil {
			return err
		}
		pcm = data[44:]
	}
	var frames []standinFrame
	pattern := ""
	if slices.Contains(args, "image2") {
		if err := json.Unmarshal([]byte(os.Getenv(framesEnv)), &frames); err != nil {
			return err
		}
		pattern = args[len(args)-1]
	}
	const chunk = 2 * audio.SampleRate / 2
	next := 0
	for at := 0; at < len(pcm) || next < len(frames); at += chunk {
		ms := int64(at) * 1000 / (2 * audio.SampleRate)
		for next < len(frames) && (frames[next].MS <= ms || at >= len(pcm)) {
			if err := standinFrameOut(pattern, next, frames[next]); err != nil {
				return err
			}
			next++
		}
		if at < len(pcm) {
			if _, err := os.Stdout.Write(pcm[at:min(at+chunk, len(pcm))]); err != nil {
				return err
			}
		}
	}
	return nil
}

// standinFrameOut reports frame i, f, on stderr as the metadata filter does, after settb, and
// writes its PNG to pattern's file for i, through a temporary file, as the image2 muxer's atomic
// writing does; noPNGEnv writes none.
func standinFrameOut(pattern string, i int, f standinFrame) error {
	fmt.Fprintf(os.Stderr, "[Parsed_metadata_2 @ 0x55d0c0de] frame:%-4d pts:%-7d pts_time:%g\n", i, f.MS*1000, float64(f.MS)/1000)
	fmt.Fprintf(os.Stderr, "[Parsed_metadata_2 @ 0x55d0c0de] lavfi.scene_score=%f\n", f.Score)
	if os.Getenv(noPNGEnv) != "" {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
	c := color.RGBA{R: uint8(f.MS / 1000), G: uint8(i), B: 7, A: 255}
	for y := range f.Height {
		for x := range f.Width {
			img.SetRGBA(x, y, c)
		}
	}
	path := fmt.Sprintf(pattern, i)
	out, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	err = png.Encode(out, img)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// logRun appends r to the log logEnv names.
func logRun(r run) error {
	f, err := os.OpenFile(os.Getenv(logEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// installStandins links the stand-ins as ffmpeg and ffprobe in a directory of their own, makes it
// the whole PATH, points the log at a file of the test's, and sets the stand-ins' version.
func installStandins(t *testing.T, version string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
	t.Setenv(versionEnv, version)
}

// runs returns the runs the log records, in order.
func runs(t *testing.T) []run {
	t.Helper()
	data, err := os.ReadFile(os.Getenv(logEnv))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []run
	for line := range strings.Lines(string(data)) {
		var r run
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// standinConfig builds the stand-ins through the exec runner, each Spec's minimum lowered to 1 so
// any version passes the runner, leaving the Streamer's own minimum to decide.
func standinConfig(t *testing.T) media.Config {
	t.Helper()
	build := func(spec tool.Spec) *tool.Tool {
		spec.Minimum = "1"
		spec.KillAfter = time.Second
		tl, err := tool.NewExec(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	return media.Config{FFmpeg: build(audio.FFmpegSpec()), FFprobe: build(audio.FFprobeSpec())}
}

// standinStreamer installs the stand-ins at 9.0.2 and builds a Streamer over them.
func standinStreamer(t *testing.T) *media.Streamer {
	t.Helper()
	installStandins(t, "9.0.2")
	s, err := media.NewStreamer(standinConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
