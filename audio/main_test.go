//go:build linux

package audio_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as a stand-in: the test binary, re-executed through a link named
// ffmpeg or ffprobe, acting on its arguments as FFmpeg's tool would instead of running the tests.
const standinEnv = "AUDIO_TEST_STANDIN"

// The stand-ins' settings, which they read from the environment they inherit.
const (
	// versionEnv is the version either stand-in's -version prints, after an n.
	versionEnv = "AUDIO_TEST_VERSION"
	// logEnv names the file each run but a version command appends its program and arguments to,
	// one JSON object per line.
	logEnv = "AUDIO_TEST_LOG"
	// probeEnv is the report the stand-in ffprobe writes to stdout.
	probeEnv = "AUDIO_TEST_PROBE"
	// volumeEnv and silenceEnv are what the stand-in ffmpeg writes to stderr for a run of
	// volumedetect and of silencedetect.
	volumeEnv  = "AUDIO_TEST_VOLUMEDETECT"
	silenceEnv = "AUDIO_TEST_SILENCEDETECT"
	// failEnv, when set, makes an ffmpeg run write its value to stderr and exit 1.
	failEnv = "AUDIO_TEST_FFMPEG_FAIL"
)

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
	if msg := os.Getenv(failEnv); msg != "" {
		fmt.Fprint(os.Stderr, msg)
		return 1
	}
	filter := args[slices.Index(args, "-af")+1]
	switch {
	case filter == "volumedetect":
		fmt.Fprint(os.Stderr, os.Getenv(volumeEnv))
	case strings.HasPrefix(filter, "silencedetect="):
		fmt.Fprint(os.Stderr, os.Getenv(silenceEnv))
	}
	return 0
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

// standinTools builds the stand-ins through the exec runner, each Spec's minimum lowered to 1 so
// any version passes the runner, leaving the Splitter's own minimum to decide.
func standinTools(t *testing.T) (ffmpeg, ffprobe *tool.Tool) {
	t.Helper()
	build := func(spec tool.Spec) *tool.Tool {
		spec.Minimum = "1"
		tl, err := tool.NewExec(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	return build(audio.FFmpegSpec()), build(audio.FFprobeSpec())
}

// standinSplitter installs the stand-ins at 9.0.2 and builds a Splitter over them.
func standinSplitter(t *testing.T) *audio.Splitter {
	t.Helper()
	installStandins(t, "9.0.2")
	ffmpeg, ffprobe := standinTools(t)
	s, err := audio.NewSplitter(audio.Config{FFmpeg: ffmpeg, FFprobe: ffprobe})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
