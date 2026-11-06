//go:build linux

package media_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
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
	// missingEnv, when set to a or V, makes the stand-in ffmpeg refuse its map of that type, as
	// ffmpeg does a reader's or a URL's whose source lacks the stream, and exit 1 before any output.
	missingEnv = "MEDIA_TEST_MISSING"
	// dockerServerEnv is the version the stand-in docker's daemon reports.
	dockerServerEnv = "MEDIA_TEST_DOCKER_SERVER"
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

// run is one stand-in run, as the log records it: its program and arguments, and, for an ffmpeg
// run whose input is pipe:0, the SHA-256 of what it read from its stdin.
type run struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Stdin   string   `json:"stdin,omitempty"`
}

// standin is the stand-in named name, ffmpeg, ffprobe, or docker.
func standin(name string, args []string) int {
	if name == "docker" {
		return dockerStandin(args)
	}
	if name != "ffmpeg" && name != "ffprobe" {
		fmt.Fprintf(os.Stderr, "stand-in: no tool named %s\n", name)
		return 2
	}
	if len(args) == 1 && args[0] == "-version" {
		fmt.Printf("%s version n%s Copyright (c) 2000-2026 the FFmpeg developers\n", name, os.Getenv(versionEnv))
		return 0
	}
	r := run{Program: name, Args: args}
	if name == "ffmpeg" && slices.Contains(args, "pipe:0") {
		in, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		sum := sha256.Sum256(in)
		r.Stdin = hex.EncodeToString(sum[:])
	}
	if err := logRun(r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if name == "ffprobe" {
		fmt.Print(os.Getenv(probeEnv))
		return 0
	}
	if m := os.Getenv(missingEnv); m != "" {
		fmt.Fprintln(os.Stderr, "Stream map '' matches no streams.")
		fmt.Fprintln(os.Stderr, "To ignore this, add a trailing '?' to the map.")
		fmt.Fprintf(os.Stderr, "Failed to set value '0:%s:0' for option 'map': Invalid argument\n", m)
		fmt.Fprintln(os.Stderr, "Error parsing options for output file pipe:1.")
		return 1
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

// dockerStandin is the stand-in docker CLI: version prints dockerServerEnv's version; image
// inspect finds every image present; stop and rm do nothing; and run logs its arguments as a run
// of docker's and runs the stand-in its --entrypoint names on the arguments after the image, in
// this process, its stdin the CLI's.
func dockerStandin(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println(os.Getenv(dockerServerEnv))
		return 0
	case "image":
		fmt.Println("sha256:" + strings.Repeat("0", 64))
		return 0
	case "stop", "rm":
		return 0
	case "run":
	default:
		fmt.Fprintf(os.Stderr, "docker: unknown command %q\n", args[0])
		return 2
	}
	entrypoint, i := "", 1
	for ; i < len(args) && strings.HasPrefix(args[i], "--"); i++ {
		if v, ok := strings.CutPrefix(args[i], "--entrypoint="); ok {
			entrypoint = v
		}
	}
	if i == len(args) {
		fmt.Fprintln(os.Stderr, "docker: run: no image")
		return 125
	}
	rest := args[i+1:]
	if len(rest) != 1 || rest[0] != "-version" {
		if err := logRun(run{Program: "docker", Args: args}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	return standin(entrypoint, rest)
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

// installStandins links the stand-ins as ffmpeg, ffprobe, and docker in a directory of their own,
// makes it the whole PATH, points the log at a file of the test's, and sets the stand-ins'
// version and the stand-in docker daemon's.
func installStandins(t *testing.T, version string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe", "docker"} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
	t.Setenv(versionEnv, version)
	t.Setenv(dockerServerEnv, "29.0.1")
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

// containerStreamer builds a Streamer over the stand-ins through the container runner, the
// stand-in docker running them, its ffmpeg's image joining network, none when empty; the
// stand-ins must be installed.
func containerStreamer(t *testing.T, network string) *media.Streamer {
	t.Helper()
	build := func(spec tool.Spec) *tool.Tool {
		spec.Minimum = "1"
		spec.KillAfter = time.Second
		tl, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage, Network: network}, spec)
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	s, err := media.NewStreamer(media.Config{FFmpeg: build(audio.FFmpegSpec()), FFprobe: build(audio.FFprobeSpec())})
	if err != nil {
		t.Fatal(err)
	}
	return s
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
