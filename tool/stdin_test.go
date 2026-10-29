//go:build linux

package tool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// largeStdin is more than a pipe's buffer holds, so a copy of it blocks until the program reads.
const largeStdin = 1 << 20

// runnerCase builds the stand-in tool, with spec's KillAfter period, through one runner, and
// returns it and a check that the runner left nothing running.
type runnerCase func(t *testing.T, killAfter time.Duration) (*tool.Tool, func(t *testing.T))

// runnerCases are the runners the stdin tests run through: the exec runner, and the container
// runner on the stand-in docker.
var runnerCases = map[string]runnerCase{
	"exec": func(t *testing.T, killAfter time.Duration) (*tool.Tool, func(t *testing.T)) {
		installStandin(t)
		spec := standinSpec("standin", "1.0", "1.0")
		spec.KillAfter = killAfter
		tl, err := tool.NewExec(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		return tl, func(*testing.T) {}
	},
	"container": func(t *testing.T, killAfter time.Duration) (*tool.Tool, func(t *testing.T)) {
		dir := installDocker(t, "29.0.1")
		addImage(t, dir, testRef)
		spec := standinSpec("standin", "1.2.3", "1.0")
		spec.KillAfter = killAfter
		tl, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: testRef}, spec)
		if err != nil {
			t.Fatal(err)
		}
		return tl, func(t *testing.T) {
			t.Helper()
			if running := runningContainers(t, dir); len(running) != 0 {
				t.Errorf("containers %q outlived the run", running)
			}
		}
	},
}

// awaitRun waits for Run's error on done, failing the test when Run hasn't returned within
// within, and returns how long Run took from start and the error.
func awaitRun(t *testing.T, done <-chan error, start time.Time, within time.Duration) (time.Duration, error) {
	t.Helper()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(within):
		t.Fatalf("Run hadn't returned after %v", within)
		return 0, nil
	}
}

// TestCancelHeldReaderStdin checks that a cancelled run returns the context's error promptly
// though its stdin is a reader, not an *os.File, that the caller holds open, so a Read of it
// never returns; and that nothing the run started outlives it.
func TestCancelHeldReaderStdin(t *testing.T) {
	for label, build := range runnerCases {
		t.Run(label, func(t *testing.T) {
			tl, assertNothingRunning := build(t, 0) // the default KillAfter period, 10 s
			stdin, held := io.Pipe()
			defer func() { _ = held.Close() }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := newReadyWriter()
			args := []string{"sleep", "1m"}
			var pidFile string
			pid := 0
			if label == "exec" {
				// spawn's child, in the run's process group, shows the group ended too.
				pidFile = filepath.Join(t.TempDir(), "child.pid")
				args = []string{"spawn", pidFile}
			}
			done := runAsync(ctx, tl, tool.Invocation{Args: args, Stdin: stdin, Stdout: ready})
			ready.wait(t)
			if pidFile != "" {
				pid = readPid(t, pidFile)
			}
			start := time.Now()
			cancel()
			elapsed, err := awaitRun(t, done, start, 15*time.Second)
			assertContextError(t, err, context.Canceled)
			if elapsed > 5*time.Second {
				t.Errorf("Run took %v after cancel, want well under the 10 s KillAfter period", elapsed)
			}
			if pid != 0 {
				assertGone(t, pid)
			}
			assertNothingRunning(t)
		})
	}
}

// TestRunUnreadHeldReaderStdin checks that a program that exits without reading its stdin, a
// reader the caller holds open, ends its run within the bound on the copy, with no error.
func TestRunUnreadHeldReaderStdin(t *testing.T) {
	for label, build := range runnerCases {
		t.Run(label, func(t *testing.T) {
			tl, assertNothingRunning := build(t, 200*time.Millisecond)
			stdin, held := io.Pipe()
			defer func() { _ = held.Close() }()
			var stdout bytes.Buffer
			start := time.Now()
			done := runAsync(t.Context(), tl, tool.Invocation{Args: []string{"print", "x"}, Stdin: stdin, Stdout: &stdout})
			// The bound is the KillAfter period through the exec runner, the CLI's 2 s through
			// the container runner.
			elapsed, err := awaitRun(t, done, start, 15*time.Second)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if elapsed > 5*time.Second {
				t.Errorf("Run took %v, want the bound on the copy, a few seconds at most", elapsed)
			}
			if stdout.String() != "x\n" {
				t.Errorf("stdout = %q, want %q", stdout.String(), "x\n")
			}
			assertNothingRunning(t)
		})
	}
}

// TestRunUnreadLargeStdin checks that a program that exits without reading a stdin larger than
// a pipe holds ends its run promptly, with no error.
func TestRunUnreadLargeStdin(t *testing.T) {
	for label, build := range runnerCases {
		t.Run(label, func(t *testing.T) {
			tl, assertNothingRunning := build(t, 0) // the default KillAfter period, 10 s
			start := time.Now()
			done := runAsync(t.Context(), tl, tool.Invocation{Args: []string{"print", "x"}, Stdin: bytes.NewReader(letters(largeStdin))})
			elapsed, err := awaitRun(t, done, start, 15*time.Second)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if elapsed > 5*time.Second {
				t.Errorf("Run took %v, want well under the 10 s KillAfter period", elapsed)
			}
			assertNothingRunning(t)
		})
	}
}

// TestRunLargeStdin checks that a stdin larger than a pipe holds, from a reader that isn't an
// *os.File, reaches the program whole.
func TestRunLargeStdin(t *testing.T) {
	for label, build := range runnerCases {
		t.Run(label, func(t *testing.T) {
			tl, _ := build(t, 0)
			want := letters(largeStdin)
			var stdout bytes.Buffer
			err := tl.Run(t.Context(), tool.Invocation{Args: []string{"echo"}, Stdin: bytes.NewReader(want), Stdout: &stdout})
			if err != nil {
				t.Fatal(err)
			}
			var got echo
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decoding %d bytes of stdout: %v", stdout.Len(), err)
			}
			if got.Stdin != string(want) {
				t.Errorf("the program read %d bytes of stdin, want all %d intact", len(got.Stdin), len(want))
			}
		})
	}
}

// errStdinRead is the failure failingReader reports.
var errStdinRead = errors.New("stdin read failed")

// failingReader yields its bytes, then fails with errStdinRead.
type failingReader struct{ r io.Reader }

func (f *failingReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		return n, errStdinRead
	}
	return n, err
}

// TestRunStdinReadError checks that a stdin whose Read fails ends the program's stdin there, and
// that the failure is Run's error when the program otherwise succeeds.
func TestRunStdinReadError(t *testing.T) {
	for label, build := range runnerCases {
		t.Run(label, func(t *testing.T) {
			tl, _ := build(t, 0)
			var stdout bytes.Buffer
			err := tl.Run(t.Context(), tool.Invocation{
				Args:   []string{"echo"},
				Stdin:  &failingReader{r: bytes.NewReader([]byte("partial"))},
				Stdout: &stdout,
			})
			if !errors.Is(err, errStdinRead) {
				t.Fatalf("Run error = %v, want one matching %v", err, errStdinRead)
			}
			var got echo
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decoding %q: %v", stdout.String(), err)
			}
			if got.Stdin != "partial" {
				t.Errorf("the program read stdin %q, want %q", got.Stdin, "partial")
			}
		})
	}
}
