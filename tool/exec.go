//go:build linux

package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// NewExec builds a Tool that runs spec's program directly. It validates the spec, then finds the
// program on PATH, or takes a path containing a separator as given, then runs the version command
// under ctx and refuses a program it can't find, whose version command fails, or whose version
// is unmatched or older than the minimum.
func NewExec(ctx context.Context, spec Spec) (*Tool, error) {
	name := filepath.Base(spec.Program)
	minimum, err := spec.minimum(name)
	if err != nil {
		return nil, err
	}
	path, err := lookPath(spec.Program)
	if err != nil {
		return nil, err
	}
	run := execRun(name, path, nil, spec.grace())
	version, err := checkVersion(ctx, name, spec, minimum, run)
	if err != nil {
		return nil, err
	}
	return &Tool{name: name, version: version, run: run}, nil
}

// NewScript builds a Tool that runs script through spec's program, its interpreter. It validates
// the spec, refuses a script that doesn't exist or is a directory, and finds and checks the
// interpreter as NewExec does a binary, its version command run without the script. Run executes
// the interpreter with the script and then the invocation's arguments. The Tool is named for the
// script; its version is the interpreter's.
func NewScript(ctx context.Context, spec Spec, script string) (*Tool, error) {
	interpreter := filepath.Base(spec.Program)
	minimum, err := spec.minimum(interpreter)
	if err != nil {
		return nil, err
	}
	// The script's path is made absolute so an invocation's Dir doesn't change what it names.
	abs, err := filepath.Abs(script)
	if err != nil {
		return nil, fmt.Errorf("tool: script %s: %w", script, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("tool: script %s: %w", script, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("tool: script %s: is a directory", script)
	}
	path, err := lookPath(spec.Program)
	if err != nil {
		return nil, err
	}
	version, err := checkVersion(ctx, interpreter, spec, minimum, execRun(interpreter, path, nil, spec.grace()))
	if err != nil {
		return nil, err
	}
	name := filepath.Base(script)
	return &Tool{name: name, version: version, run: execRun(name, path, []string{abs}, spec.grace())}, nil
}

// lookPath finds program on PATH, or takes a path containing a separator as given.
func lookPath(program string) (string, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		return "", fmt.Errorf("tool: %s: not found: %w", program, err)
	}
	return path, nil
}

// execRun returns the exec runner's runFunc for the program at path, which runs it with lead and
// then the invocation's arguments, and gives a cancelled run grace between SIGTERM and SIGKILL.
//
// The program leads a process group of its own, so a cancel reaches everything it started:
// SIGTERM to the group when ctx ends; SIGKILL to the program once grace passes (WaitDelay); and,
// once the program has exited, SIGKILL to the group, so no child outlives it.
func execRun(name, path string, lead []string, grace time.Duration) runFunc {
	return func(ctx context.Context, inv Invocation) error {
		cmd := exec.CommandContext(ctx, path, slices.Concat(lead, inv.Args)...)
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
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return signalGroup(cmd.Process, syscall.SIGTERM) }
		// WaitDelay also bounds the wait for a child that holds the program's output pipes after
		// the program exits; the group's SIGKILL below then ends that child.
		cmd.WaitDelay = grace
		err := cmd.Run()
		if ctx.Err() != nil && cmd.Process != nil {
			// The leader is reaped, but a live member keeps the group's ID from reuse; an empty
			// group gives ESRCH, which is ignored.
			_ = signalGroup(cmd.Process, syscall.SIGKILL)
		}
		if err != nil && ctx.Err() != nil {
			// ctx ended the run: its error is the cause, whatever the program's exit.
			return fmt.Errorf("tool: %s: %w (%v)", name, ctx.Err(), err)
		}
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

// signalGroup sends sig to the process group p leads, reporting a group already gone as
// os.ErrProcessDone.
func signalGroup(p *os.Process, sig syscall.Signal) error {
	err := syscall.Kill(-p.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
