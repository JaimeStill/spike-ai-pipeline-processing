//go:build linux

package tool_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// assertUsage checks that u holds at least the spin's user CPU time, which only the spinning
// stand-in spends: neither a shell nor the stand-in's own start comes near it.
func assertUsage(t *testing.T, u tool.Usage) {
	t.Helper()
	if u.UserCPU < spinCPU {
		t.Errorf("UserCPU = %v, want at least the spin's %v", u.UserCPU, spinCPU)
	}
}

func TestRunUsage(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	var u tool.Usage
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{"spin", "print", "done"}, Usage: &u})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, u)
}

func TestRunUsageOnExitError(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	var u tool.Usage
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{"spin", "exit", "3"}, Usage: &u})
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
	done := runAsync(ctx, tl, tool.Invocation{Args: []string{"spin", "sleep", "1m"}, Stdout: ready, Usage: &u})
	ready.wait(t)
	cancel()
	assertContextError(t, <-done, context.Canceled)
	assertUsage(t, u)
}

// TestRunUsageCountsReapedChildren checks that the usage covers a child the program waited for:
// a script whose shell runs the spinning stand-in and reaps it before exiting. The echo after it
// keeps the shell from exec'ing the stand-in in its own place, so the stand-in is a child.
func TestRunUsageCountsReapedChildren(t *testing.T) {
	installStandin(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "spin.sh", "standin spin print done\necho reaped")
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
	if err := tl.Run(t.Context(), tool.Invocation{Args: []string{"print", "done"}}); err != nil {
		t.Fatal(err)
	}
}

// TestContainerUsageZero checks that the container runner reports no usage, even overwriting what
// the caller left in it: the stand-in docker CLI reaps the spinning stand-in itself, so a CLI
// rusage passed through would hold its CPU time.
func TestContainerUsageZero(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	u := tool.Usage{UserCPU: 1, SystemCPU: 1}
	if err := tl.Run(t.Context(), tool.Invocation{Args: []string{"spin", "print", "done"}, Usage: &u}); err != nil {
		t.Fatal(err)
	}
	if u != (tool.Usage{}) {
		t.Errorf("Usage = %+v, want zero", u)
	}
}
