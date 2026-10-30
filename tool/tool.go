//go:build linux

package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Spec declares a tool: its program, and how to check the program's version at construction.
type Spec struct {
	// Program is a name looked up on PATH, or a path, which is taken as given when it contains a
	// separator.
	Program string
	// VersionArgs are the arguments that make the program print its version.
	VersionArgs []string
	// VersionPattern matches the version command's output, stdout and stderr together; its first
	// capture group is the version, dotted numbers only.
	VersionPattern *regexp.Regexp
	// Minimum is the oldest version the tool accepts, dotted numbers.
	Minimum string
	// KillAfter is the KillAfter period: how long a cancelled run has to exit after SIGTERM
	// before SIGKILL ends it; zero or less is 10 s.
	KillAfter time.Duration
}

// defaultKillAfter is a Spec's KillAfter period when it sets none.
const defaultKillAfter = 10 * time.Second

// killAfter returns the spec's KillAfter period, defaultKillAfter when unset.
func (s Spec) killAfter() time.Duration {
	if s.KillAfter <= 0 {
		return defaultKillAfter
	}
	return s.KillAfter
}

// Invocation is one run of a tool. It names nothing about the runner, so an adapter builds the
// same Invocation whichever runner built its Tool.
type Invocation struct {
	// Args are the program's arguments, the program's name not among them.
	Args []string
	// Dir is the working directory, which the caller owns; empty is the process's own.
	Dir string
	// Stdin is the program's standard input; nil is none. An *os.File passes straight to the
	// program. Any other reader is copied into a pipe by a goroutine of Run's own, which closes
	// the pipe at the reader's EOF. Once the program has exited, Run waits for that copy at most
	// a short bound, the KillAfter period through the exec runner and 2 s through the container
	// runner, then returns: a program that exits without reading all of Stdin is no error, and a
	// failed Read of Stdin is Run's error only when the program otherwise succeeds. A run that
	// ctx ended doesn't wait for the copy. The cost: after Run returns, the goroutine may make
	// one more Read of Stdin, and it exits when that Read returns.
	Stdin io.Reader
	// Stdout receives the program's standard output; nil discards it.
	Stdout io.Writer
	// Stderr receives all of the program's standard error; nil keeps its last 4 KiB for the
	// ExitError instead.
	Stderr io.Writer
	// Usage, when not nil, receives the run's resource usage. Run sets it whatever the run's
	// outcome: once the program has been reaped, whether it exited zero, exited nonzero, or was
	// cancelled, the exec runner fills it from the program's rusage; a program that never
	// started leaves it zero. The container runner always leaves it zero: the only process it
	// reaps is the docker CLI, whose usage isn't the container's. nil asks for nothing.
	Usage *Usage
}

// Usage is a run's CPU time, as the kernel accounts it for the program once it is reaped. On
// Linux that accounting covers the children the program itself waited for, such as a
// converter's delegate, but not a child it left unreaped.
//
// It has no peak memory. Go starts a program with a vfork-style clone, so Linux records the Go
// parent's resident memory in the program's peak at exec, and the rusage figure reads the
// caller's size rather than the program's whenever the caller is the larger.
type Usage struct {
	// UserCPU is the CPU time spent in user mode, the program's and its reaped children's.
	UserCPU time.Duration
	// SystemCPU is the CPU time spent in the kernel on their behalf.
	SystemCPU time.Duration
}

// runFunc runs one invocation of a bound program; each runner supplies its own.
type runFunc func(ctx context.Context, inv Invocation) error

// Tool is one program, found and version-checked by the runner's constructor that built it.
type Tool struct {
	name    string
	version Version
	run     runFunc
}

// Name returns the program's name; for a script, the script's.
func (t *Tool) Name() string { return t.name }

// Version returns the program's version, found at construction; for a script, the interpreter's.
func (t *Tool) Version() Version { return t.version }

// Run runs the program once, under ctx. A program that exits nonzero gives an *ExitError. When ctx
// ends the run, the error matches ctx's (errors.Is context.Canceled or
// context.DeadlineExceeded), and is never an *ExitError; it names the program, ctx's error, and
// the program's own outcome once each, as in "tool: ffprobe: context canceled (exit status 143)".
func (t *Tool) Run(ctx context.Context, inv Invocation) error { return t.run(ctx, inv) }

// stderrTail is how much of a program's standard error an ExitError keeps.
const stderrTail = 4 << 10

// ExitError reports a program that exited nonzero.
type ExitError struct {
	// Program is the program's name.
	Program string
	// Code is the exit code, -1 when a signal ended the program.
	Code int
	// Stderr is the last 4 KiB of the program's standard error, empty when the caller passed a
	// writer for it.
	Stderr string
	err    error
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("tool: %s: exit code %d", e.Program, e.Code)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

// Unwrap returns the runner's own error, an *exec.ExitError for the exec runner.
func (e *ExitError) Unwrap() error { return e.err }

// VersionError reports a tool its constructor refused over its version: the version command
// failed, its output didn't match the pattern, or the version is older than the minimum.
type VersionError struct {
	// Program is the program's name.
	Program string
	// Found is the version found, empty when none was.
	Found string
	// Minimum is the oldest version the tool accepts.
	Minimum string
	// Err is the cause: ErrTooOld, ErrNoVersion, or the version command's error.
	Err error
}

func (e *VersionError) Error() string {
	found := e.Found
	if found == "" {
		found = "none"
	}
	return fmt.Sprintf("tool: %s: version %s, minimum %s: %v", e.Program, found, e.Minimum, e.Err)
}

func (e *VersionError) Unwrap() error { return e.Err }

var (
	// ErrTooOld is a VersionError's cause when the version is older than the minimum.
	ErrTooOld = errors.New("older than the minimum")
	// ErrNoVersion is a VersionError's cause when the version output doesn't match the pattern,
	// or its capture isn't dotted numbers.
	ErrNoVersion = errors.New("no version in the output")
)

// minimum validates the spec's own fields, its version pattern and its minimum, so a malformed
// spec fails as itself before anything is looked up or run, and returns the minimum version.
func (s Spec) minimum(name string) (Version, error) {
	if s.VersionPattern == nil || s.VersionPattern.NumSubexp() < 1 {
		return Version{}, fmt.Errorf("tool: %s: version pattern must have a capture group", name)
	}
	minimum, err := ParseVersion(s.Minimum)
	if err != nil {
		return Version{}, fmt.Errorf("tool: %s: minimum: %w", name, err)
	}
	return minimum, nil
}

// checkVersion runs the spec's version command through run and checks the version it prints
// against minimum, the spec's, returning the version found.
func checkVersion(ctx context.Context, name string, spec Spec, minimum Version, run runFunc) (Version, error) {
	var out bytes.Buffer
	verr := &VersionError{Program: name, Minimum: spec.Minimum}
	if err := run(ctx, Invocation{Args: spec.VersionArgs, Stdout: &out, Stderr: &out}); err != nil {
		verr.Err = fmt.Errorf("version command: %w", err)
		if line := firstLine(out.Bytes()); line != "" {
			verr.Err = fmt.Errorf("%w: %s", verr.Err, line)
		}
		return Version{}, verr
	}
	m := spec.VersionPattern.FindSubmatch(out.Bytes())
	if m == nil {
		verr.Err = fmt.Errorf("%w: %s matched none of %q", ErrNoVersion, spec.VersionPattern, firstLine(out.Bytes()))
		return Version{}, verr
	}
	verr.Found = string(m[1])
	found, err := ParseVersion(verr.Found)
	if err != nil {
		verr.Err = fmt.Errorf("%w: %w", ErrNoVersion, err)
		return Version{}, verr
	}
	if found.Compare(minimum) < 0 {
		verr.Err = ErrTooOld
		return Version{}, verr
	}
	return found, nil
}

// firstLine returns the first line of out, at most 200 bytes, for an error's context.
func firstLine(out []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
	if len(line) > 200 {
		line = line[:200]
	}
	return string(line)
}

// tailWriter keeps the last n bytes written to it.
type tailWriter struct {
	n   int
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - w.n; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
	}
	return len(p), nil
}
