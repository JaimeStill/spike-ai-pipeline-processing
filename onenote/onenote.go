//go:build linux

package onenote

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"regexp"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// OneNoteMinimum is the oldest onenote the Reader accepts.
const OneNoteMinimum = "0.1.0"

// OneNoteSpec declares onenote's spike-ai-pipeline-onenote, minimum 0.1.0, for either runner's
// constructor. onenote --version prints the version alone, as "0.1.0", and the pattern takes the
// dotted numbers, as mail's does. A caller that needs another program path or a KillAfter period
// sets them on the Spec returned.
func OneNoteSpec() tool.Spec {
	return tool.Spec{
		Program:        "spike-ai-pipeline-onenote",
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`^(\d+(?:\.\d+)*)`),
		Minimum:        OneNoteMinimum,
	}
}

// Config is what a Reader is built over: the Tool the caller built.
type Config struct {
	// OneNote is onenote's spike-ai-pipeline-onenote, typically built from OneNoteSpec through
	// the exec runner. It is required.
	OneNote *tool.Tool
}

// Reader reads OneNote sections, notebooks, and packages through onenote: an input in, its
// records streamed out as onenote writes them. It runs no inference. Its methods are safe for
// concurrent use; each Stream is one read, used by one goroutine.
type Reader struct {
	onenote *tool.Tool
}

// NewReader builds a Reader over cfg's Tool. It refuses a nil OneNote, and a OneNote whose
// version is older than OneNoteMinimum, whatever minimum the Tool's own Spec declared, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func NewReader(cfg Config) (*Reader, error) {
	if cfg.OneNote == nil {
		return nil, errors.New("onenote: no onenote tool")
	}
	m, err := tool.ParseVersion(OneNoteMinimum)
	if err != nil {
		return nil, err
	}
	if v := cfg.OneNote.Version(); v.Compare(m) < 0 {
		return nil, &tool.VersionError{Program: cfg.OneNote.Name(), Found: v.String(), Minimum: OneNoteMinimum, Err: tool.ErrTooOld}
	}
	return &Reader{onenote: cfg.OneNote}, nil
}

// Request is one input's read.
type Request struct {
	// Input is the path of the .one section, the .onetoc2 notebook or notebook directory, or the
	// .onepkg package to read; onenote sniffs its kind from its content, never its name. Read
	// makes it absolute, so a name that begins with "-" is never taken as an option. It is
	// required.
	Input string
	// Media is the directory onenote writes each distinct image, embedded file, and ink drawing
	// into once, named by its SHA-256, created if missing; the caller owns it. Read makes it
	// absolute. "" writes nothing, though the records name the files all the same.
	Media string
}

// ErrRequest is the cause of a request Read refuses before anything runs: one with no Input, or
// whose Input or Media can't be made absolute.
var ErrRequest = errors.New("invalid request")

// The outcomes of a onenote run that exits nonzero, each its exit code's; an *ExitError matches
// its code's through errors.Is. Each is the shared exit error of every CLI this module runs, so
// ErrRefused matches the office and email packages' refusals too, and email.ErrRefused
// onenote's.
var (
	// ErrNotWritten is exit code 1: onenote couldn't write its output, the media directory, or a
	// file in it.
	ErrNotWritten = cli.ErrNotWritten
	// ErrUsage is exit code 2: onenote refused its arguments, a fault in this package.
	ErrUsage = cli.ErrUsage
	// ErrRefused is exit code 3: onenote refused the input before any output, giving its reason,
	// such as a password-protected section's.
	ErrRefused = cli.ErrRefused
	// ErrFailed is exit code 4: a fault of onenote's own.
	ErrFailed = cli.ErrFailed
)

// ExitError reports a onenote run that exited 1, 2, 3, or 4: its exit code and onenote's
// message. It matches the code's error, ErrNotWritten, ErrUsage, ErrRefused, or ErrFailed,
// through errors.Is, and unwraps to the Tool's *tool.ExitError.
type ExitError struct {
	// Code is onenote's exit code.
	Code int
	// Message is onenote's message on stderr, its first line that isn't a warning, with
	// onenote's own prefix, its error label, and the input's path taken off: for a refusal,
	// onenote's reason.
	Message string
	err     error
}

func (e *ExitError) Error() string { return cli.ExitText("onenote", e.Code, e.Message) }

// Is reports whether target is the error of e's exit code.
func (e *ExitError) Is(target error) bool { return cli.Is(e.Code, target) }

// Unwrap returns the Tool's *tool.ExitError.
func (e *ExitError) Unwrap() error { return e.err }

// Stream is one read of an input: its records, streamed while onenote runs, and onenote's
// warnings once they have all been read.
type Stream struct {
	onenote *tool.Tool
	ctx     context.Context
	// input and media are the request's Input and Media, made absolute; media "" for none.
	input    string
	media    string
	started  bool
	warnings []string
}

// Read returns the read of req's input under ctx. It refuses an invalid request at once, with an
// error wrapping ErrRequest: an empty Input, or an Input or Media that can't be made absolute,
// which filepath.Abs does from the working directory. Nothing runs until the Stream's Records
// are ranged over.
func (r *Reader) Read(ctx context.Context, req Request) (*Stream, error) {
	if req.Input == "" {
		return nil, fmt.Errorf("onenote: %w: no input", ErrRequest)
	}
	input, err := filepath.Abs(req.Input)
	if err != nil {
		return nil, fmt.Errorf("onenote: %w: input: %w", ErrRequest, err)
	}
	var media string
	if req.Media != "" {
		if media, err = filepath.Abs(req.Media); err != nil {
			return nil, fmt.Errorf("onenote: %w: media: %w", ErrRequest, err)
		}
	}
	return &Stream{onenote: r.onenote, ctx: ctx, input: input, media: media}, nil
}

// Warnings returns onenote's warnings, one per line it wrote to stderr as a warning, with
// onenote's prefix and its warning label taken off: "<where>: <what>", where "<where>" is the
// record's path from the notebook's root, such as "/Group/Section, page <id>", or "/" for a
// notebook-wide one; a skipped section's and the recycle bin's among them. After a read that
// succeeded, any other line onenote wrote to stderr is taken as a warning too. A read with
// warnings has still succeeded. They are complete once Records has ended; nil before.
func (s *Stream) Warnings() []string { return s.warnings }

// stderrLimit is the most of onenote's standard error a read keeps, for its warnings and its
// message.
const stderrLimit = 1 << 20

// Records runs onenote once on the input and yields each line it writes as a Record while it
// runs: the notebook first, then the notebook's entries depth-first in its table of contents'
// order, a section group before the entries it holds and a section before its pages, then a
// skipped record for each section the read left out. A section input gives the notebook, its
// section, and its pages. onenote's stdout passes through a pipe and is decoded one line at a
// time, so memory follows the record being read, never the notebook; a slow consumer holds
// onenote back rather than buffering its output.
//
// A read that fails yields one last pair, nil and its error. A notebook line whose schema isn't
// Schema is an error matching ErrSchema, naming the schema. onenote's exit codes 1 to 4 are an
// *ExitError matching the code's error. When the context Read was given ends the read, the
// error matches that context's, as every Tool run's does. A loop that stops early stops onenote
// too, and nothing more is yielded. A Stream's records are read once: ranging over them again
// yields an error.
func (s *Stream) Records() iter.Seq2[Record, error] {
	return func(yield func(Record, error) bool) {
		if s.started {
			yield(nil, errors.New("onenote: the stream's records have been read"))
			return
		}
		s.started = true
		if err := s.read(yield); err != nil {
			yield(nil, err)
		}
	}
}

// read runs onenote and yields its records, returning the read's error; nil too when yield
// stopped the loop, which then has nothing more yielded.
func (s *Stream) read(yield func(Record, error) bool) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	input := s.input
	args := []string{input}
	if s.media != "" {
		args = []string{"--media", s.media, input}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	// An *os.File passes straight to onenote as its stdout, so no copy sits between them that
	// Run would wait for once onenote exits; the read end holds at most the pipe's buffer.
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("onenote: stdout: %w", err)
	}
	defer func() { _ = pr.Close() }()
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	done := make(chan error, 1)
	go func() {
		err := s.onenote.Run(ctx, tool.Invocation{Args: args, Stdout: pw, Stderr: stderr})
		// onenote has exited, and its write end with it: closing this one, the last, ends the
		// read end at EOF.
		_ = pw.Close()
		done <- err
	}()
	// stop ends a read before onenote's output does: closing the read end makes onenote's next
	// write fail, and the cancel ends it should it be writing nothing. It returns the run's
	// error.
	stop := func() error {
		cancel()
		_ = pr.Close()
		return <-done
	}

	dec := jsontext.NewDecoder(bufio.NewReader(pr))
	first := true
	for {
		if s.ctx.Err() != nil {
			return s.ended(stop())
		}
		raw, err := dec.ReadValue()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A line cut short is onenote's output ended mid-line, so onenote has exited, and
			// its outcome, should it have failed, is the cause.
			if errors.Is(err, io.ErrUnexpectedEOF) {
				if runErr := <-done; runErr != nil {
					return s.outcome(runErr, stderr.String(), input)
				}
			} else {
				_ = stop()
			}
			if s.ctx.Err() != nil {
				return s.ended(nil)
			}
			return fmt.Errorf("onenote: reading onenote's output: %w", err)
		}
		rec, err := decodeLine(raw, first)
		if err != nil {
			_ = stop()
			return err
		}
		first = false
		if !yield(rec, nil) {
			_ = stop()
			return nil
		}
	}
	runErr := <-done
	if runErr != nil {
		return s.outcome(runErr, stderr.String(), input)
	}
	s.warnings, _ = onenoteProgram.ReadStderr(stderr.String(), input, true)
	if first {
		return errors.New("onenote: onenote wrote no notebook line")
	}
	return nil
}

// ended returns the error of a read that its context ended: runErr, the run's, when it matches
// the context's, else the context's own.
func (s *Stream) ended(runErr error) error {
	if runErr != nil && errors.Is(runErr, s.ctx.Err()) {
		return fmt.Errorf("onenote: %w", runErr)
	}
	return fmt.Errorf("onenote: %w", s.ctx.Err())
}

// outcome returns the error of a run that failed, err the Tool's, and sets the Stream's warnings
// from stderr, onenote's standard error, of a run on input: an *ExitError for exit codes 1 to 4,
// unless the context ended the run; else err with onenote's message, if any.
func (s *Stream) outcome(err error, stderr, input string) error {
	var message string
	s.warnings, message = onenoteProgram.ReadStderr(stderr, input, false)
	if s.ctx.Err() != nil {
		return s.ended(err)
	}
	if exit, ok := errors.AsType[*tool.ExitError](err); ok {
		if cli.Outcome(exit.Code) != nil {
			return &ExitError{Code: exit.Code, Message: message, err: err}
		}
	}
	if message != "" {
		return fmt.Errorf("onenote: %w: %s", err, message)
	}
	return fmt.Errorf("onenote: %w", err)
}

// onenoteProgram is onenote as its stderr shows it: each line but its usage begins with its
// name, and a failure's, for exit codes 1 to 4, with its error label.
var onenoteProgram = cli.Program{Name: "spike-ai-pipeline-onenote", Labels: []string{"error"}}
