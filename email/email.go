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
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// MailMinimum is the oldest mail the Reader accepts.
const MailMinimum = "0.2.0"

// MailAfterMinimum is the oldest mail that resumes a read, mail's --after: Read refuses a
// Request with After under an older one. A Reader without After needs MailMinimum alone, so a
// caller that never resumes may stay on mail 0.2.0.
const MailAfterMinimum = "0.3.0"

// MailSpec declares mail's spike-ai-pipeline-mail, minimum 0.2.0, for either runner's
// constructor. mail --version prints the version alone, as "0.2.0", and the pattern takes the
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
	// never its name. Read makes it absolute, so a name that begins with "-" is never taken as an
	// option. It is required.
	Input string
	// Attachments is the directory mail writes each distinct attachment content into once,
	// named by its File, created if missing; the caller owns it. Each Attachment's Path then
	// names its file there, and Attachment.Route sniffs it. Read makes it absolute. "" writes
	// nothing, and routes nothing by content.
	Attachments string
	// After, when not zero, resumes the read of a .pst or an mbox after the message whose
	// Message.ID it is: in a .pst its node id, in an mbox its 1-based ordinal. The resumed read is
	// the tail of the whole read, as mail writes it byte for byte: the store, every folder, and
	// every folder's skipped record, then only the messages after this one, with their skipped
	// records. It needs mail MailAfterMinimum or later, and an id from 1 to 4,294,967,295, mail's
	// range; Read refuses either otherwise. mail refuses After with a .msg or an .eml, a single
	// message, as a usage error, an *ExitError matching ErrUsage, and an id that names no message
	// in the store, a folder's among them, as an *ExitError matching ErrRefused.
	After uint64
}

// ErrRequest is the cause of a request Read refuses before anything runs: one with no Input,
// whose Input or Attachments can't be made absolute, or whose After is past mail's range or
// given a mail older than MailAfterMinimum.
var ErrRequest = errors.New("invalid request")

// The outcomes of a mail run that exits nonzero, each its exit code's; an *ExitError matches its
// code's through errors.Is. Each is the shared exit error of every CLI this module runs, so
// ErrRefused matches the office package's refusal too, and office.ErrRefused mail's.
var (
	// ErrNotWritten is exit code 1: mail couldn't write its output, or an attachment's content.
	ErrNotWritten = cli.ErrNotWritten
	// ErrUsage is exit code 2: mail refused its arguments, a fault in this package.
	ErrUsage = cli.ErrUsage
	// ErrRefused is exit code 3: mail refused the input before any output, giving its reason,
	// such as an .ost's.
	ErrRefused = cli.ErrRefused
	// ErrFailed is exit code 4: a fault of mail's own.
	ErrFailed = cli.ErrFailed
)

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

func (e *ExitError) Error() string { return cli.ExitText("email: mail", e.Code, e.Message) }

// Is reports whether target is the error of e's exit code.
func (e *ExitError) Is(target error) bool { return cli.Is(e.Code, target) }

// Unwrap returns the Tool's *tool.ExitError.
func (e *ExitError) Unwrap() error { return e.err }

// Stream is one read of an input: its records, streamed while mail runs, and mail's warnings once
// they have all been read.
type Stream struct {
	mail *tool.Tool
	ctx  context.Context
	// input and dir are the request's Input and Attachments, made absolute; dir "" for none.
	input string
	dir   string
	// after is the request's After, 0 for none.
	after    uint64
	started  bool
	warnings []string
}

// Read returns the read of req's input under ctx. It refuses an invalid request at once, with an
// error wrapping ErrRequest: an empty Input, an Input or Attachments that can't be made absolute,
// which filepath.Abs does from the working directory, or an After past 4,294,967,295; and an
// After given a mail older than MailAfterMinimum, with an error wrapping ErrRequest and a
// *tool.VersionError wrapping tool.ErrTooOld. Nothing runs until the Stream's Records are ranged
// over.
func (r *Reader) Read(ctx context.Context, req Request) (*Stream, error) {
	if req.Input == "" {
		return nil, fmt.Errorf("email: %w: no input", ErrRequest)
	}
	if req.After > math.MaxUint32 {
		return nil, fmt.Errorf("email: %w: after %d, past mail's last id, %d", ErrRequest, req.After, uint64(math.MaxUint32))
	}
	if req.After != 0 {
		m, err := tool.ParseVersion(MailAfterMinimum)
		if err != nil {
			return nil, err
		}
		if v := r.mail.Version(); v.Compare(m) < 0 {
			verr := &tool.VersionError{Program: r.mail.Name(), Found: v.String(), Minimum: MailAfterMinimum, Err: tool.ErrTooOld}
			return nil, fmt.Errorf("email: %w: after needs mail %s or later: %w", ErrRequest, MailAfterMinimum, verr)
		}
	}
	input, err := filepath.Abs(req.Input)
	if err != nil {
		return nil, fmt.Errorf("email: %w: input: %w", ErrRequest, err)
	}
	var dir string
	if req.Attachments != "" {
		if dir, err = filepath.Abs(req.Attachments); err != nil {
			return nil, fmt.Errorf("email: %w: attachments: %w", ErrRequest, err)
		}
	}
	return &Stream{mail: r.mail, ctx: ctx, input: input, dir: dir, after: req.After}, nil
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
// *ExitError matching the code's error. When the context Read was given ends the read, the error
// matches that context's, as every Tool run's does. A loop that stops early stops mail too, and
// nothing more is yielded. A Stream's records are read once: ranging over them again yields an
// error.
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
	if err := s.ctx.Err(); err != nil {
		return err
	}
	input, dir := s.input, s.dir
	var args []string
	if dir != "" {
		args = append(args, "--attachments", dir)
	}
	if s.after != 0 {
		args = append(args, "--after", strconv.FormatUint(s.after, 10))
	}
	args = append(args, input)
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	// An *os.File passes straight to mail as its stdout, so no copy sits between them that Run
	// would wait for once mail exits; the read end holds at most the pipe's buffer.
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("email: stdout: %w", err)
	}
	defer func() { _ = pr.Close() }()
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	done := make(chan error, 1)
	go func() {
		err := s.mail.Run(ctx, tool.Invocation{Args: args, Stdout: pw, Stderr: stderr})
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
		if m, ok := rec.(*Message); ok && dir != "" {
			setPaths(m, dir)
		}
		if !yield(rec, nil) {
			_ = stop()
			return nil
		}
	}
	runErr := <-done
	if runErr != nil {
		return s.outcome(runErr, stderr.String(), input)
	}
	s.warnings, _ = mailProgram.ReadStderr(stderr.String(), input, true)
	if first {
		return errors.New("email: mail wrote no store line")
	}
	return nil
}

// setPaths sets the Path of each of m's attachments with a File, those of its nested messages
// included, to the File's path in dir, the read's attachments directory. A File that isn't a
// bare name, which mail never writes, is given no Path, so no Path leaves dir.
func setPaths(m *Message, dir string) {
	for i := range m.Attachments {
		a := &m.Attachments[i]
		if a.File != "" && filepath.IsLocal(a.File) && filepath.Base(a.File) == a.File {
			a.Path = filepath.Join(dir, a.File)
		}
		if a.Message != nil {
			setPaths(a.Message, dir)
		}
	}
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
	s.warnings, message = mailProgram.ReadStderr(stderr, input, false)
	if s.ctx.Err() != nil {
		return s.ended(err)
	}
	if exit, ok := errors.AsType[*tool.ExitError](err); ok {
		if cli.Outcome(exit.Code) != nil {
			return &ExitError{Code: exit.Code, Message: message, err: err}
		}
	}
	if message != "" {
		return fmt.Errorf("email: %w: %s", err, message)
	}
	return fmt.Errorf("email: %w", err)
}

// mailProgram is mail as its stderr shows it: each line but its usage begins with its name, and
// a failure's, for exit codes 1 to 4, with its error label.
var mailProgram = cli.Program{Name: "spike-ai-pipeline-mail", Labels: []string{"error"}}
