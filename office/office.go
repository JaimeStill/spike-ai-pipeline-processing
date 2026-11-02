//go:build linux

package office

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// OfficeMinimum is the oldest office the Converter accepts.
const OfficeMinimum = "0.1.0"

// OfficeSpec declares office's spike-ai-pipeline-office, minimum 0.1.0, for either runner's
// constructor. office --version prints the version alone, as "0.1.0", a build other than a
// release's appending + and its commit, and the pattern takes the dotted numbers. A caller that
// needs another program path or a KillAfter period sets them on the Spec returned.
func OfficeSpec() tool.Spec {
	return tool.Spec{
		Program:        "spike-ai-pipeline-office",
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`^(\d+(?:\.\d+)*)`),
		Minimum:        OfficeMinimum,
	}
}

// Config is what a Converter is built over: the Tools the caller built.
type Config struct {
	// Office is office's spike-ai-pipeline-office, typically built from OfficeSpec. It is
	// required.
	Office *tool.Tool
}

// Converter prepares office documents for a model through office: a document in, office's JSON
// model or its Markdown out, with the media the document embeds. It runs no inference. Its
// methods are safe for concurrent use.
type Converter struct {
	office *tool.Tool
}

// NewConverter builds a Converter over cfg's Tools. It refuses a nil Office, and an Office whose
// version is older than OfficeMinimum, whatever minimum its own Spec declared, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func NewConverter(cfg Config) (*Converter, error) {
	if cfg.Office == nil {
		return nil, errors.New("office: no office tool")
	}
	minimum, err := tool.ParseVersion(OfficeMinimum)
	if err != nil {
		return nil, err
	}
	if v := cfg.Office.Version(); v.Compare(minimum) < 0 {
		return nil, &tool.VersionError{Program: cfg.Office.Name(), Found: v.String(), Minimum: OfficeMinimum, Err: tool.ErrTooOld}
	}
	return &Converter{office: cfg.Office}, nil
}

// Format is the rendering a conversion returns.
type Format int

const (
	// JSON is office's JSON model, decoded into a Document: the default.
	JSON Format = iota
	// Markdown is office's Markdown rendering of the model.
	Markdown
)

// String returns the format's name, as office's --format takes it.
func (f Format) String() string {
	switch f {
	case JSON:
		return "json"
	case Markdown:
		return "markdown"
	}
	return fmt.Sprintf("Format(%d)", int(f))
}

// Request is one document's conversion.
type Request struct {
	// Dir is the working directory, which the caller owns: the document is spooled into it and
	// removed when the conversion ends, and the media directory is made in it and left. It is
	// required; an empty Dir is refused rather than taken as the system's temporary directory.
	Dir string
	// Format is the rendering returned: JSON, the zero value, or Markdown.
	Format Format
}

// Result is one document's conversion: office's model or Markdown, the media handed onward, and
// office's warnings.
type Result struct {
	// Document is the decoded JSON model, for a JSON request; nil for a Markdown one.
	Document *Document
	// Markdown is office's Markdown, for a Markdown request; "" for a JSON one.
	Markdown string
	// MediaDir is the conversion's media directory, an absolute path in the request's Dir, which
	// the caller owns. It holds the document's embedded media as office wrote them.
	MediaDir string
	// Media lists the files in MediaDir, one entry each, by name.
	Media []MediaFile
	// Warnings are office's warnings, one per line it wrote to stderr: what it skipped and
	// converted the rest without. A conversion with warnings has still succeeded.
	Warnings []string
}

// The outcomes of an office run that exits nonzero, each its exit code's; an *ExitError matches
// its code's through errors.Is.
var (
	// ErrMediaNotWritten is exit code 1: office converted the document but couldn't write its
	// media.
	ErrMediaNotWritten = errors.New("media not written")
	// ErrUsage is exit code 2: office refused its arguments, a fault in this package.
	ErrUsage = errors.New("usage error")
	// ErrRefused is exit code 3: office refused the document, giving its reason.
	ErrRefused = errors.New("refused")
	// ErrFailed is exit code 4: office failed in a way it expects of no document, a fault of
	// office's own.
	ErrFailed = errors.New("failed")
)

// outcomes maps office's exit codes to their errors.
var outcomes = map[int]error{
	1: ErrMediaNotWritten,
	2: ErrUsage,
	3: ErrRefused,
	4: ErrFailed,
}

// ExitError reports an office run that exited 1, 2, 3, or 4: its exit code and office's message.
// It matches the code's error, ErrMediaNotWritten, ErrUsage, ErrRefused, or ErrFailed, through
// errors.Is, and unwraps to the Tool's *tool.ExitError.
type ExitError struct {
	// Code is office's exit code.
	Code int
	// Message is office's message on stderr, its first line that isn't a warning, with office's
	// own prefix and the spooled document's path taken off: for a refusal, office's reason.
	Message string
	err     error
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("office: exit code %d", e.Code)
	if outcome, ok := outcomes[e.Code]; ok {
		msg = "office: " + outcome.Error()
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

// stderrLimit is the most of office's standard error a conversion keeps, for its warnings and
// its message.
const stderrLimit = 1 << 20

// Convert converts the document read from doc as req says. It spools doc into req.Dir, makes a
// media directory of its own there, media-*, and runs office once on the spooled document, with
// req.Format and that media directory, in req.Dir. It returns the JSON model or the Markdown,
// the media directory's files, and office's warnings. The spooled document is removed when
// Convert returns, however it ends.
//
// A JSON model whose schema isn't Schema is an error matching ErrSchema, naming the schema.
// office's exit codes 1 to 4 are an *ExitError matching the code's error. When ctx ends the run,
// the error matches ctx's, as every Tool run's does. A conversion that fails removes its media
// directory.
func (c *Converter) Convert(ctx context.Context, doc io.Reader, req Request) (*Result, error) {
	if req.Format != JSON && req.Format != Markdown {
		return nil, fmt.Errorf("office: unknown format %v", req.Format)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Dir == "" {
		return nil, errors.New("office: no working directory")
	}
	dir, err := filepath.Abs(req.Dir)
	if err != nil {
		return nil, fmt.Errorf("office: %w", err)
	}
	path, remove, err := spool(dir, doc)
	if err != nil {
		return nil, err
	}
	res, err := c.convert(ctx, dir, path, req.Format)
	if rerr := remove(); rerr != nil {
		err = errors.Join(err, fmt.Errorf("office: removing the spooled document: %w", rerr))
	}
	if err != nil {
		if res != nil {
			err = errors.Join(err, os.RemoveAll(res.MediaDir))
		}
		return nil, err
	}
	return res, nil
}

// convert runs office on the document spooled at path, in dir, with a media directory of its own
// made there. It returns the result with its MediaDir set whenever it made the directory, with
// its error, so the caller can remove the directory of a failed conversion.
func (c *Converter) convert(ctx context.Context, dir, path string, format Format) (*Result, error) {
	media, err := os.MkdirTemp(dir, "media-*")
	if err != nil {
		return nil, fmt.Errorf("office: media directory: %w", err)
	}
	res := &Result{MediaDir: media}
	var stdout bytes.Buffer
	stderr := &limitedBuffer{limit: stderrLimit}
	err = c.office.Run(ctx, tool.Invocation{
		Args:   []string{path, "--format", format.String(), "--media", media},
		Dir:    dir,
		Stdout: &stdout,
		Stderr: stderr,
	})
	lines, message := readStderr(stderr.String(), path)
	if err != nil {
		var exit *tool.ExitError
		if errors.As(err, &exit) && ctx.Err() == nil {
			if _, ok := outcomes[exit.Code]; ok {
				return res, &ExitError{Code: exit.Code, Message: message, err: err}
			}
		}
		if message != "" {
			return res, fmt.Errorf("office: %w: %s", err, message)
		}
		return res, fmt.Errorf("office: %w", err)
	}
	res.Warnings = lines
	if format == Markdown {
		res.Markdown = stdout.String()
	} else if res.Document, err = decodeDocument(stdout.Bytes()); err != nil {
		return res, err
	}
	if res.Media, err = listMedia(media); err != nil {
		return res, err
	}
	return res, nil
}

// officePrefix begins each line office writes to stderr but its usage.
const officePrefix = "spike-ai-pipeline-office: "

// warningPrefix begins each of office's warnings, after officePrefix.
const warningPrefix = "warning: "

// readStderr reads office's stderr: each line that isn't blank, in order, and the run's message,
// the first of them that isn't a warning. A warning is taken as the line with office's prefix,
// the warning label, and the spooled document's path taken off; any other line with office's
// prefix, its outcome's label, and the path. A run that exits zero writes only warnings, and any
// other line it writes is taken as one too.
func readStderr(stderr, path string) (lines []string, message string) {
	for line := range strings.Lines(stderr) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rest, own := strings.CutPrefix(line, officePrefix)
		if w, ok := strings.CutPrefix(rest, warningPrefix); own && ok {
			lines = append(lines, strings.TrimPrefix(w, path+": "))
			continue
		}
		if own {
			for _, label := range outcomes {
				if r, ok := strings.CutPrefix(rest, label.Error()+": "); ok {
					rest = r
					break
				}
			}
			rest = strings.TrimPrefix(rest, path+": ")
		}
		lines = append(lines, rest)
		if message == "" {
			message = rest
		}
	}
	return lines, message
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
