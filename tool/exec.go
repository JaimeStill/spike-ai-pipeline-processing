//go:build linux

package tool

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
)

// NewExec builds a Tool that runs spec's program directly. It finds the program on PATH, or takes
// a path containing a separator as given, then runs the version command under ctx and refuses a
// program it can't find, whose version command fails, or whose version is unmatched or older
// than the minimum.
func NewExec(ctx context.Context, spec Spec) (*Tool, error) {
	path, err := exec.LookPath(spec.Program)
	if err != nil {
		return nil, fmt.Errorf("tool: %s: not found: %w", spec.Program, err)
	}
	name := filepath.Base(spec.Program)
	run := execRun(name, path)
	version, err := checkVersion(ctx, name, spec, run)
	if err != nil {
		return nil, err
	}
	return &Tool{name: name, version: version, run: run}, nil
}

// execRun returns the exec runner's runFunc for the program at path.
func execRun(name, path string) runFunc {
	return func(ctx context.Context, inv Invocation) error {
		cmd := exec.CommandContext(ctx, path, inv.Args...)
		cmd.Dir = inv.Dir
		cmd.Stdin = inv.Stdin
		cmd.Stdout = inv.Stdout
		var tail *tailWriter
		if inv.Stderr != nil {
			cmd.Stderr = inv.Stderr
		} else {
			tail = &tailWriter{n: stderrTail}
			cmd.Stderr = tail
		}
		err := cmd.Run()
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			e := &ExitError{Program: name, Code: exitErr.ExitCode(), err: err}
			if tail != nil {
				e.Stderr = string(tail.buf)
			}
			return e
		}
		if err != nil {
			return fmt.Errorf("tool: %s: %w", name, err)
		}
		return nil
	}
}
