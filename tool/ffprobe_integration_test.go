//go:build integration && linux

package tool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// ffmpegImage is linuxserver/ffmpeg's 9.0.2 CLI image, build ls87, pinned by its index digest.
const ffmpegImage = "linuxserver/ffmpeg:9.0.2-cli-ls87@sha256:" +
	"6547359fac23a2747e606d380e10267b2923a38f68485d9cacefd039e74d3aa8"

// ffprobeSpec declares ffprobe, minimum 9.0. ffprobe prints "ffprobe version n9.0.2 ..." on the
// host and "ffprobe version 9.0.2 ..." in the image; the pattern skips the n.
func ffprobeSpec() tool.Spec {
	return tool.Spec{
		Program:        "ffprobe",
		VersionArgs:    []string{"-version"},
		VersionPattern: regexp.MustCompile(`ffprobe version n?(\d+(?:\.\d+)*)`),
		Minimum:        "9.0",
	}
}

// ffprobeRunner builds ffprobe through one runner, checking its version, and returns it and a
// check that the runs through it left nothing behind.
type ffprobeRunner func(t *testing.T) (*tool.Tool, func(t *testing.T))

// ffprobeRunners are the runners ffprobe runs through: the host's ffprobe through the exec
// runner, and ffprobe in the pinned image through the container runner, which pulls the image
// first when it isn't present. The runner is the only difference between the runs the tests
// below compare.
var ffprobeRunners = map[string]ffprobeRunner{
	"exec": func(t *testing.T) (*tool.Tool, func(t *testing.T)) {
		tl, err := tool.NewExec(t.Context(), ffprobeSpec())
		if err != nil {
			t.Fatal(err)
		}
		return tl, func(*testing.T) {}
	},
	"container": func(t *testing.T) (*tool.Tool, func(t *testing.T)) {
		tl, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: ffmpegImage}, ffprobeSpec())
		if err != nil {
			t.Fatal(err)
		}
		if got := tl.Name(); got != "ffprobe" {
			t.Errorf("Name() = %q, want ffprobe", got)
		}
		// A run's container name isn't observable, so the check is that no container named as
		// the runner names its runs appears that wasn't there before; other runs' are left out.
		before := toolContainers(t)
		return tl, func(t *testing.T) {
			t.Helper()
			if left := slices.DeleteFunc(toolContainers(t), func(id string) bool {
				return slices.Contains(before, id)
			}); len(left) != 0 {
				t.Errorf("containers %q outlived the run", left)
			}
		}
	},
}

// toolContainers returns the IDs of every container, running or not, whose name begins tool-,
// the container runner's prefix.
func toolContainers(t *testing.T) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", "ps", "--all", "--quiet", "--no-trunc",
		"--filter=name=^tool-").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	return strings.Fields(string(out))
}

// probeArgs are ffprobe's arguments for a JSON report of input's format and streams.
func probeArgs(input string) []string {
	return []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", input}
}

// probeStream is the part of a stream in ffprobe's JSON report that the tests compare.
type probeStream struct {
	CodecName  string `json:"codec_name"`
	SampleRate string `json:"sample_rate"`
}

// probe is the part of ffprobe's JSON report that the tests compare.
type probe struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []probeStream `json:"streams"`
}

func (p probe) equal(q probe) bool {
	return p.Format == q.Format && slices.Equal(p.Streams, q.Streams)
}

// toneProbe is testdata/tone.wav's report: one second of 16 kHz 16-bit PCM in a WAV.
const toneProbe = `{"format":{"format_name":"wav","duration":"1.000000"},` +
	`"streams":[{"codec_name":"pcm_s16le","sample_rate":"16000"}]}`

func TestFFprobeVersion(t *testing.T) {
	for name, runner := range ffprobeRunners {
		t.Run(name, func(t *testing.T) {
			tl, _ := runner(t)
			t.Logf("%s %s", tl.Name(), tl.Version())
		})
	}
}

// TestFFprobeProbesTone checks that ffprobe, given the same invocation through both runners,
// reports the same format and streams for the fixed tone, and that the report is the tone's.
func TestFFprobeProbesTone(t *testing.T) {
	var want probe
	if err := json.Unmarshal([]byte(toneProbe), &want); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]probe{}
	for name, runner := range ffprobeRunners {
		t.Run(name, func(t *testing.T) {
			tl, leftover := runner(t)
			var stdout bytes.Buffer
			start := time.Now()
			err := tl.Run(t.Context(), tool.Invocation{Args: probeArgs("tone.wav"), Dir: dir, Stdout: &stdout})
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			var p probe
			if err := json.Unmarshal(stdout.Bytes(), &p); err != nil {
				t.Fatalf("ffprobe's report %q: %v", stdout.String(), err)
			}
			if !p.equal(want) {
				t.Errorf("ffprobe reported %+v, want %+v", p, want)
			}
			got[name] = p
			t.Logf("%s %s in %v: %+v", tl.Name(), tl.Version(), elapsed, p)
			leftover(t)
		})
	}
	if e, c := got["exec"], got["container"]; len(got) == 2 && !e.equal(c) {
		t.Errorf("the exec runner reported %+v, the container runner %+v", e, c)
	}
}

// TestFFprobeCancelHeldStdin checks that ffprobe, reading a stdin the test holds open, is
// cancelled mid-run through both runners: Run returns the context's error, and the run leaves no
// container behind. The test writes only the tone's 44-byte header, so ffprobe waits on stdin for
// the samples until the cancel.
func TestFFprobeCancelHeldStdin(t *testing.T) {
	dir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	tone, err := os.ReadFile(filepath.Join(dir, "tone.wav"))
	if err != nil {
		t.Fatal(err)
	}
	for name, runner := range ffprobeRunners {
		t.Run(name, func(t *testing.T) {
			tl, leftover := runner(t)
			stdin, held := io.Pipe()
			defer func() { _ = held.Close() }()
			go func() { _, _ = held.Write(tone[:44]) }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := runAsync(ctx, tl, tool.Invocation{Args: probeArgs("pipe:0"), Dir: dir, Stdin: stdin, Stdout: io.Discard})
			// Long enough for docker to start the container, so the cancel comes mid-run.
			select {
			case err := <-done:
				t.Fatalf("Run returned %v before the cancel, want it waiting on stdin", err)
			case <-time.After(2 * time.Second):
			}
			start := time.Now()
			cancel()
			elapsed, err := awaitRun(t, done, start, 15*time.Second)
			assertContextError(t, err, context.Canceled)
			if elapsed > 10*time.Second {
				t.Errorf("Run took %v after cancel, want well under 10 s", elapsed)
			}
			t.Logf("Run returned %v after cancel: %v", elapsed, err)
			leftover(t)
		})
	}
}
