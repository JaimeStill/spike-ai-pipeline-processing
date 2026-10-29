//go:build linux

package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"time"
	"unsafe"
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
	run := execRun(name, path, nil, spec.killAfter())
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
	version, err := checkVersion(ctx, interpreter, spec, minimum, execRun(interpreter, path, nil, spec.killAfter()))
	if err != nil {
		return nil, err
	}
	name := filepath.Base(script)
	return &Tool{name: name, version: version, run: execRun(name, path, []string{abs}, spec.killAfter())}, nil
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
// then the invocation's arguments, and gives a cancelled run killAfter, the KillAfter period,
// between SIGTERM and SIGKILL.
//
// The program leads a process group of its own, so a cancel reaches everything it started:
// SIGTERM to the group when ctx ends; SIGKILL to the program once killAfter passes (WaitDelay);
// and, once the program has exited but before it is reaped, SIGKILL to the group, so no child
// outlives it.
//
// An *os.File stdin passes straight to the program; any other reader goes through a stdinFeed,
// so a run never waits on a Read of the caller's.
func execRun(name, path string, lead []string, killAfter time.Duration) runFunc {
	return func(ctx context.Context, inv Invocation) error {
		cmd := exec.CommandContext(ctx, path, slices.Concat(lead, inv.Args)...)
		cmd.Dir = inv.Dir
		cmd.Stdin = inv.Stdin
		var feed *stdinFeed
		if _, ok := inv.Stdin.(*os.File); inv.Stdin != nil && !ok {
			var err error
			if feed, err = newStdinFeed(inv.Stdin); err != nil {
				return fmt.Errorf("tool: %s: stdin: %w", name, err)
			}
			cmd.Stdin = feed.r
		}
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
		// WaitDelay's timer starts when ctx ends, in Start's own goroutine, so it SIGKILLs a
		// program that outlasts killAfter while awaitExit blocks.
		cmd.WaitDelay = killAfter
		err := cmd.Start()
		if feed != nil {
			feed.start(err == nil)
		}
		if err == nil {
			// The program is exited but unreaped once awaitExit returns: a zombie keeps its pid,
			// and so the group's ID, from reuse, so the group's SIGKILL reaches only what the
			// program started. Should waitid fail, which Linux has offered since 2.6.9, the
			// group is left alone rather than risk a reused ID.
			if awaitExit(cmd.Process.Pid) == nil && ctx.Err() != nil {
				_ = signalGroup(cmd.Process, syscall.SIGKILL)
			}
			err = cmd.Wait()
		}
		if feed != nil {
			if ferr := feed.finish(ctx, killAfter); ferr != nil && err == nil {
				err = fmt.Errorf("stdin: %w", ferr)
			}
		}
		if err != nil && ctx.Err() != nil {
			// ctx ended the run: its error is the cause, whatever the program's exit.
			return &cancelError{program: name, ctxErr: ctx.Err(), outcome: err}
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

// cancelError reports a run that its context ended. It matches the context's error alone; the
// program's own outcome, its exit or the runner's failure, is only named in the message, so a
// cancelled run is never an *ExitError.
type cancelError struct {
	program string
	ctxErr  error
	// outcome is the run's own error, nil when the program exited 0.
	outcome error
}

func (e *cancelError) Error() string {
	if e.outcome == nil {
		return fmt.Sprintf("tool: %s: %v", e.program, e.ctxErr)
	}
	return fmt.Sprintf("tool: %s: %v (%v)", e.program, e.ctxErr, e.outcome)
}

func (e *cancelError) Unwrap() error { return e.ctxErr }

// runOutcome returns the run's own outcome within err, a runner's error: a cancelError's outcome,
// an ExitError's exec.ExitError, or err itself. A runner built on another's names the outcome it
// gets once in its own error, without the inner runner's program or context.
func runOutcome(err error) error {
	if ce, ok := errors.AsType[*cancelError](err); ok {
		return ce.outcome
	}
	if ee, ok := errors.AsType[*ExitError](err); ok {
		return ee.err
	}
	return err
}

// stdinFeed is the pipe a run's stdin passes through when the caller's reader isn't an *os.File.
// os/exec would copy such a reader itself, and, once the program has exited, wait for that copy
// with no bound, even past WaitDelay: a copy blocked in the caller's Read, on a pipe the caller
// holds open, would keep Run from ever returning. The feed's copy is execRun's own, so Run decides
// how long to wait for it.
type stdinFeed struct {
	src  io.Reader
	r, w *os.File
	// done carries the copy's outcome; nil until start begins the copy.
	done chan error
}

// newStdinFeed makes the pipe src is copied into, its read end the program's stdin.
func newStdinFeed(src io.Reader) (*stdinFeed, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &stdinFeed{src: src, r: r, w: w}, nil
}

// start runs once the command's Start has returned, started reporting whether it succeeded. The
// program holds the read end of its own, so the parent's copy is closed either way: the program,
// and whatever it starts, are then the pipe's only readers, and a write once they have all exited
// fails with EPIPE rather than blocking. A program that started gets the copy, which closes the
// write end when it ends, so the program reads EOF at the end of src; one that didn't, nothing.
func (f *stdinFeed) start(started bool) {
	_ = f.r.Close()
	if !started {
		_ = f.w.Close()
		return
	}
	f.done = make(chan error, 1)
	go func() {
		_, err := io.Copy(f.w, f.src)
		_ = f.w.Close()
		// EPIPE is a program that exited, or closed its stdin, without reading all of src, and
		// ErrClosed is a write end that finish closed: neither is the copy's failure.
		if errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrClosed) {
			err = nil
		}
		f.done <- err
	}()
}

// finish runs once the program has exited and been reaped, and returns the copy's error, a read
// of src that failed while the program ran. A run that ctx ended doesn't wait for the copy at
// all. Otherwise finish waits for the copy at most killAfter, as WaitDelay bounds the wait for
// the program's output, and stops early should ctx end: a program that read all of src has its
// copy already ended, and one that left src unread ends its copy with EPIPE at the next write,
// but a copy blocked in a Read of src, which the caller holds open, ends only when that Read
// returns, which no bound here can hasten. When finish stops waiting, it closes the write end, so
// the copy's next write fails and it ends, and reports nothing: an exited program can read no
// more of src, and leaving src unread is no error.
func (f *stdinFeed) finish(ctx context.Context, killAfter time.Duration) error {
	if f.done == nil {
		return nil
	}
	if ctx.Err() == nil {
		timer := time.NewTimer(killAfter)
		defer timer.Stop()
		select {
		case err := <-f.done:
			return err
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	_ = f.w.Close()
	return nil
}

// pPID is waitid's idtype for a single pid, P_PID, which syscall doesn't define.
const pPID = 1

// awaitExit blocks until the child pid has exited, without reaping it: waitid with WNOWAIT
// leaves the child waitable, so the os.Process's own wait, through its pidfd, still reaps it and
// collects its status. The os package's wait does the same where it has no pidfd.
func awaitExit(pid int) error {
	// The kernel writes a siginfo_t, 128 bytes on Linux, which nothing here reads.
	var info [128]byte
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid),
			uintptr(unsafe.Pointer(&info)), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno != syscall.EINTR {
			if errno != 0 {
				return errno
			}
			return nil
		}
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
