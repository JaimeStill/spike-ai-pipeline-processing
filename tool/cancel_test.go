//go:build linux

package tool_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

func TestCancelTerminates(t *testing.T) {
	installStandin(t)
	tl := newStandin(t) // the default grace, 10 s
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := newReadyWriter()
	done := runAsync(ctx, tl, tool.Invocation{Args: []string{"sleep", "1m"}, Stdout: ready})
	ready.wait(t)
	start := time.Now()
	cancel()
	err := <-done
	assertContextError(t, err, context.Canceled)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %v after cancel, want well under the 10 s grace", elapsed)
	}
}

func TestCancelKillsAfterGrace(t *testing.T) {
	installStandin(t)
	const grace = 200 * time.Millisecond
	spec := standinSpec("standin", "1.0", "1.0")
	spec.Grace = grace
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := newReadyWriter()
	done := runAsync(ctx, tl, tool.Invocation{Args: []string{"stubborn", "1m"}, Stdout: ready})
	ready.wait(t)
	start := time.Now()
	cancel()
	err = <-done
	elapsed := time.Since(start)
	assertContextError(t, err, context.Canceled)
	if elapsed < grace {
		t.Errorf("Run took %v after cancel, want at least the %v grace", elapsed, grace)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Run took %v after cancel, want about the %v grace", elapsed, grace)
	}
}

func TestCancelEndsChild(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	done := runAsync(ctx, tl, tool.Invocation{Args: []string{"spawn", pidFile}})
	pid := readPid(t, pidFile)
	start := time.Now()
	cancel()
	assertContextError(t, <-done, context.Canceled)
	// The child ignores SIGTERM; the group's SIGKILL ends it once its parent exits, well
	// before the grace.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %v after cancel, want well under the 10 s grace", elapsed)
	}
	assertGone(t, pid)
}

func TestDeadlineExceeded(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := tl.Run(ctx, tool.Invocation{Args: []string{"sleep", "1m"}})
	assertContextError(t, err, context.DeadlineExceeded)
}

func TestRunContextAlreadyDone(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := tl.Run(ctx, tool.Invocation{Args: []string{"sleep", "1m"}})
	assertContextError(t, err, context.Canceled)
}

// runAsync runs tl in a goroutine, sending Run's error on the channel it returns.
func runAsync(ctx context.Context, tl *tool.Tool, inv tool.Invocation) <-chan error {
	done := make(chan error, 1)
	go func() { done <- tl.Run(ctx, inv) }()
	return done
}

// assertContextError checks that err matches want, the context's error, and is no ExitError.
func assertContextError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want one matching %v", err, want)
	}
	if _, ok := errors.AsType[*tool.ExitError](err); ok {
		t.Errorf("Run error %q is an ExitError, want only the context's", err)
	}
}

// readyWriter is a stand-in's stdout; it closes ready at the first write, the stand-in's
// "ready" once its SIGTERM handling is in place.
type readyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func newReadyWriter() *readyWriter { return &readyWriter{ready: make(chan struct{})} }

func (w *readyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

// wait blocks until the stand-in is ready, failing the test after 10 s.
func (w *readyWriter) wait(t *testing.T) {
	t.Helper()
	select {
	case <-w.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the stand-in never wrote ready")
	}
}

// readPid polls for the pid a stubborn stand-in writes to file, a number and a newline,
// failing the test after 10 s.
func readPid(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(file); err == nil && bytes.HasSuffix(b, []byte("\n")) {
			pid, err := strconv.Atoi(string(bytes.TrimSpace(b)))
			if err != nil {
				t.Fatalf("pid file %q: %v", b, err)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", file)
	return 0
}

// assertGone polls for the process pid to be gone, or a zombie awaiting its reaper, failing the
// test after 5 s.
func assertGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || zombie(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("child %d outlived the cancelled run", pid)
}

// zombie reports whether pid is a zombie: exited, its parent not yet having reaped it.
func zombie(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the command name, which is in parentheses and may contain spaces.
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
