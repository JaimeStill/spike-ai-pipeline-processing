//go:build linux

package tool_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// allocMiB is what the stand-in allocates and writes in a usage test; its peak RSS is at least
// this much.
const allocMiB = 64

// assertUsage checks that u holds at least allocMiB of peak RSS and some CPU time.
func assertUsage(t *testing.T, u tool.Usage) {
	t.Helper()
	if u.PeakRSS < allocMiB<<20 {
		t.Errorf("PeakRSS = %d bytes, want at least %d MiB", u.PeakRSS, allocMiB)
	}
	if u.UserCPU+u.SystemCPU <= 0 {
		t.Errorf("UserCPU + SystemCPU = %v + %v, want more than zero", u.UserCPU, u.SystemCPU)
	}
	if u.UserCPU <= 0 {
		t.Errorf("UserCPU = %v, want more than zero after a 50 ms spin", u.UserCPU)
	}
}

func TestRunUsage(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	var u tool.Usage
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{"alloc", "64", "print", "done"}, Usage: &u})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, u)
}

func TestRunUsageOnExitError(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	var u tool.Usage
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{"alloc", "64", "exit", "3"}, Usage: &u})
	if _, ok := errors.AsType[*tool.ExitError](err); !ok {
		t.Fatalf("Run error = %v, want a *tool.ExitError", err)
	}
	assertUsage(t, u)
}

func TestRunUsageOnCancel(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var u tool.Usage
	ready := newReadyWriter()
	done := runAsync(ctx, tl, tool.Invocation{Args: []string{"alloc", "64", "sleep", "1m"}, Stdout: ready, Usage: &u})
	ready.wait(t)
	cancel()
	assertContextError(t, <-done, context.Canceled)
	assertUsage(t, u)
}

// TestRunUsageCountsReapedChildren checks that the usage covers a child the program waited for:
// a script whose shell runs the allocating stand-in and reaps it before exiting. The echo after
// it keeps the shell from exec'ing the stand-in in its own place, so the stand-in is a child.
func TestRunUsageCountsReapedChildren(t *testing.T) {
	installStandin(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "alloc.sh", "standin alloc 64 print done\necho reaped")
	tl, err := tool.NewScript(t.Context(), interpreterSpec("1.0", "1.0"), script)
	if err != nil {
		t.Fatal(err)
	}
	var u tool.Usage
	if err := tl.Run(t.Context(), tool.Invocation{Usage: &u}); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, u)
}

func TestRunNilUsage(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	if err := tl.Run(t.Context(), tool.Invocation{Args: []string{"alloc", "1", "print", "done"}}); err != nil {
		t.Fatal(err)
	}
}

// TestContainerUsageZero checks that the container runner reports no usage, even overwriting what
// the caller left in it: the stand-in docker CLI reaps the allocating stand-in itself, so a CLI
// rusage passed through would hold its 64 MiB.
func TestContainerUsageZero(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	u := tool.Usage{PeakRSS: 1, UserCPU: 1, SystemCPU: 1}
	if err := tl.Run(t.Context(), tool.Invocation{Args: []string{"alloc", "64", "print", "done"}, Usage: &u}); err != nil {
		t.Fatal(err)
	}
	if u != (tool.Usage{}) {
		t.Errorf("Usage = %+v, want zero", u)
	}
}
