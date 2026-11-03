//go:build linux

package email

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
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// MailMinimum is the oldest mail the Reader accepts.
const MailMinimum = "0.1.0"

// MailSpec declares mail's spike-ai-pipeline-mail, minimum 0.1.0, for either runner's
// constructor. mail --version prints the version alone, as "0.1.0", and the pattern takes the
// dotted numbers, as office's does. A caller that needs another program path or a KillAfter
// period sets them on the Spec returned.
func MailSpec() tool.Spec {
	return tool.Spec{
		Program:        "spike-ai-pipeline-mail",
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`^(\d+(?:\.\d+)*)`),
		Minimum:        MailMinimum,
	}
}

// Config is what a Reader is built over: the Tool the caller built.
type Config struct {
	// Mail is mail's spike-ai-pipeline-mail, typically built from MailSpec through the exec
	// runner. It is required.
	Mail *tool.Tool
}

// Reader reads mail stores, .pst and .msg, through mail: an input in, its records streamed out
// as mail writes them. It runs no inference. Its methods are safe for concurrent use; each
// Stream is one read, used by one goroutine.
type Reader struct {
	mail *tool.Tool
}

// NewReader builds a Reader over cfg's Tool. It refuses a nil Mail, and a Mail whose version is
// older than MailMinimum, whatever minimum the Tool's own Spec declared, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func NewReader(cfg Config) (*Reader, error) {
	if cfg.Mail == nil {
		return nil, errors.New("email: no mail tool")
	}
	m, err := tool.ParseVersion(MailMinimum)
	if err != nil {
		return nil, err
	}
	if v := cfg.Mail.Version(); v.Compare(m) < 0 {
		return nil, &tool.VersionError{Program: cfg.Mail.Name(), Found: v.String(), Minimum: MailMinimum, Err: tool.ErrTooOld}
	}
	return &Reader{mail: cfg.Mail}, nil
}

// Request is one input's read.
type Request struct {
	// Input is the path of the .pst or .msg to read; mail sniffs its format from its content,
	// never its name. It is made absolute before mail runs, so a name that begins with "-" is
	// never taken as an option. It is required.
	Input string
}

// The outcomes of a mail run that exits nonzero, each its exit code's; an *ExitError matches its
// code's through errors.Is.
var (
	// ErrNotWritten is exit code 1: mail couldn't write its output, or an attachment's content.
	ErrNotWritten = errors.New("not written")
	// ErrUsage is exit code 2: mail refused its arguments, a fault in this package.
	ErrUsage = errors.New("usage error")
	// ErrRefused is exit code 3: mail refused the input before any output, giving its reason,
	// such as an .ost's.
	ErrRefused = errors.New("refused")
	// ErrFailed is exit code 4: a fault of mail's own.
	ErrFailed = errors.New("failed")
)

// outcomes maps mail's exit codes to their errors.
var outcomes = map[int]error{
	1: ErrNotWritten,
	2: ErrUsage,
	3: ErrRefused,
	4: ErrFailed,
}

// ExitError reports a mail run that exited 1, 2, 3, or 4: its exit code and mail's message. It
// matches the code's error, ErrNotWritten, ErrUsage, ErrRefused, or ErrFailed, through
// errors.Is, and unwraps to the Tool's *tool.ExitError.
type ExitError struct {
	// Code is mail's exit code.
	Code int
	// Message is mail's message on stderr, its first line that isn't a warning, with mail's own
	// prefix, its error label, and the input's path taken off: for a refusal, mail's reason.
	Message string
	err     error
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("email: mail: exit code %d", e.Code)
	if outcome, ok := outcomes[e.Code]; ok {
		msg = "email: mail: " + outcome.Error()
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Is reports whether target is the error of e's exit code.
func (e *ExitError) Is(target error) bool { return outcomes[e.Code] == target }

// Unwrap returns the Tool's *tool.ExitError.
func (e *ExitError) Unwrap() error { return e.err }

// Stream is one read of an input: its records, streamed while mail runs, and mail's warnings once
// they have all been read.
type Stream struct {
	mail     *tool.Tool
	ctx      context.Context
	input    string
	started  bool
	warnings []string
}

// Read returns the read of req's input under ctx. Nothing runs until the Stream's Records are
// ranged over.
func (r *Reader) Read(ctx context.Context, req Request) *Stream {
	return &Stream{mail: r.mail, ctx: ctx, input: req.Input}
}

// Warnings returns mail's warnings, one per line it wrote to stderr as a warning, with mail's
// prefix and its warning label taken off: "<where>: <what>", what it skipped among them. After a
// read that succeeded, any other line mail wrote to stderr is taken as a warning too. A read with
// warnings has still succeeded. They are complete once Records has ended; nil before.
func (s *Stream) Warnings() []string { return s.warnings }

// stderrLimit is the most of mail's standard error a read keeps, for its warnings and its
// message.
const stderrLimit = 1 << 20

// Records runs mail once on the input and yields each line it writes as a Record while it runs:
// the store first, then, for a .pst, folders, messages, and skipped records in mail's order, or,
// for a .msg, its message and skipped records. mail's stdout passes through a pipe and is decoded
// one line at a time, so memory follows the record being read, never the store; a slow consumer
// holds mail back rather than buffering its output.
//
// A read that fails yields one last pair, nil and its error. A store line whose schema isn't
// Schema is an error matching ErrSchema, naming the schema. mail's exit codes 1 to 4 are an
// *ExitError matching the code's error. When ctx ends the read, the error matches ctx's, as every
// Tool run's does. A loop that stops early stops mail too, and nothing more is yielded. A Stream's
// records are read once: ranging over them again yields an error.
func (s *Stream) Records() iter.Seq2[Record, error] {
	return func(yield func(Record, error) bool) {
		if s.started {
			yield(nil, errors.New("email: the stream's records have been read"))
			return
		}
		s.started = true
		if err := s.read(yield); err != nil {
			yield(nil, err)
		}
	}
}

// read runs mail and yields its records, returning the read's error; nil too when yield stopped
// the loop, which then has nothing more yielded.
func (s *Stream) read(yield func(Record, error) bool) error {
	if s.input == "" {
		return errors.New("email: no input")
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	input, err := filepath.Abs(s.input)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	// An *os.File passes straight to mail as its stdout, so no copy sits between them that Run
	// would wait for once mail exits; the read end holds at most the pipe's buffer.
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("email: stdout: %w", err)
	}
	defer func() { _ = pr.Close() }()
	stderr := &limitedBuffer{limit: stderrLimit}
	done := make(chan error, 1)
	go func() {
		err := s.mail.Run(ctx, tool.Invocation{Args: []string{input}, Stdout: pw, Stderr: stderr})
		// mail has exited, and its write end with it: closing this one, the last, ends the read
		// end at EOF.
		_ = pw.Close()
		done <- err
	}()
	// stop ends a read before mail's output does: closing the read end makes mail's next write
	// fail, and the cancel ends it should it be writing nothing. It returns the run's error.
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
			// A line cut short is mail's output ended mid-line, so mail has exited, and its
			// outcome, should it have failed, is the cause.
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
			return fmt.Errorf("email: reading mail's output: %w", err)
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
	s.warnings, _ = readStderr(stderr.String(), input, true)
	if first {
		return errors.New("email: mail wrote no store line")
	}
	return nil
}

// ended returns the error of a read that its context ended: runErr, the run's, when it matches
// the context's, else the context's own.
func (s *Stream) ended(runErr error) error {
	if runErr != nil && errors.Is(runErr, s.ctx.Err()) {
		return fmt.Errorf("email: %w", runErr)
	}
	return fmt.Errorf("email: %w", s.ctx.Err())
}

// outcome returns the error of a run that failed, err the Tool's, and sets the Stream's warnings
// from stderr, mail's standard error, of a run on input: an *ExitError for exit codes 1 to 4,
// unless the context ended the run; else err with mail's message, if any.
func (s *Stream) outcome(err error, stderr, input string) error {
	var message string
	s.warnings, message = readStderr(stderr, input, false)
	if s.ctx.Err() != nil {
		return s.ended(err)
	}
	if exit, ok := errors.AsType[*tool.ExitError](err); ok {
		if _, ok := outcomes[exit.Code]; ok {
			return &ExitError{Code: exit.Code, Message: message, err: err}
		}
	}
	if message != "" {
		return fmt.Errorf("email: %w: %s", err, message)
	}
	return fmt.Errorf("email: %w", err)
}

// mailPrefix begins each line mail writes to stderr but its usage.
const mailPrefix = "spike-ai-pipeline-mail: "

// The labels after mailPrefix: a warning's, and an error's for exit codes 1 to 4.
const (
	warningLabel = "warning: "
	errorLabel   = "error: "
)

// readStderr reads the stderr of mail's run on input: its warnings, in order, and the run's
// message, the first line that isn't blank or a warning. A warning is the line with mail's prefix
// and the warning label taken off; the message, the line with mail's prefix, the error label, and
// the input's path, which a refusal begins with, taken off. succeeded says the run exited zero,
// which writes only warnings, so any other line is taken as one too.
func readStderr(stderr, input string, succeeded bool) (warnings []string, message string) {
	for line := range strings.Lines(stderr) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rest, own := strings.CutPrefix(line, mailPrefix)
		if w, ok := strings.CutPrefix(rest, warningLabel); own && ok {
			warnings = append(warnings, w)
			continue
		}
		if r, ok := strings.CutPrefix(rest, errorLabel); own && ok {
			rest = strings.TrimPrefix(r, input+": ")
		}
		if succeeded {
			warnings = append(warnings, rest)
		} else if message == "" {
			message = rest
		}
	}
	return warnings, message
}

// limitedBuffer is a writer that keeps the first limit bytes written to it and discards the rest.
type limitedBuffer struct {
	strings.Builder
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room > 0 {
		b.Builder.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
