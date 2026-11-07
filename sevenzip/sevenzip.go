//go:build linux

package sevenzip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// SevenZipMinimum is the oldest 7-Zip the package accepts: 24.01, whose zstd decoder reads a zstd
// stream and a ZIP's zstd entries.
const SevenZipMinimum = "24.01"

// SevenZipSpec declares 7-Zip's 7zz, minimum 24.01, for either runner's constructor. 7zz with no
// arguments prints "7-Zip (z) 26.05 (x64) : Copyright ...", and the pattern takes the dotted
// numbers after the name. A caller that needs another program path or a KillAfter period sets
// them on the Spec returned.
func SevenZipSpec() tool.Spec {
	return tool.Spec{
		Program:        "7zz",
		VersionPattern: regexp.MustCompile(`7-Zip(?: \([a-z]+\))? (\d+(?:\.\d+)*)`),
		Minimum:        SevenZipMinimum,
	}
}

var (
	// ErrNoSevenZip is the cause of NewReader's refusal of a Config with no 7zz Tool.
	ErrNoSevenZip = errors.New("no 7zz")
	// ErrEncrypted is the cause of a listing whose headers are encrypted, so its items can't be
	// listed without the password, and of an extraction that needs a password: the package
	// gives none.
	ErrEncrypted = errors.New("encrypted")
	// ErrNotArchive is the cause of a listing or an extraction 7-Zip refuses because the file
	// can't be opened as the format the request names: it isn't one, or it is cut short.
	ErrNotArchive = errors.New("can't be opened as the archive format")
	// ErrRequest is the cause of a request refused before anything runs.
	ErrRequest = errors.New("invalid request")
)

// Config is what a Reader is built over: the Tool the caller built.
type Config struct {
	// SevenZip is 7-Zip's 7zz, typically built from SevenZipSpec; it is required.
	SevenZip *tool.Tool
}

// Reader lists and extracts archives through 7zz. Its methods are safe for concurrent use.
type Reader struct {
	sevenZip *tool.Tool
}

// NewReader builds a Reader over cfg's Tool. It refuses a missing one, with ErrNoSevenZip, and one
// older than SevenZipMinimum, whatever minimum the Tool's own Spec declared, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func NewReader(cfg Config) (*Reader, error) {
	if cfg.SevenZip == nil {
		return nil, fmt.Errorf("sevenzip: %w", ErrNoSevenZip)
	}
	m, err := tool.ParseVersion(SevenZipMinimum)
	if err != nil {
		return nil, err
	}
	if v := cfg.SevenZip.Version(); v.Compare(m) < 0 {
		return nil, &tool.VersionError{Program: cfg.SevenZip.Name(), Found: v.String(), Minimum: SevenZipMinimum, Err: tool.ErrTooOld}
	}
	return &Reader{sevenZip: cfg.SevenZip}, nil
}

// ListRequest names the archive to list and the format to read it as.
type ListRequest struct {
	// Path is the archive's file; it is only read.
	Path string
	// Format is the format 7-Zip reads it as.
	Format Format
}

// ExtractRequest names one item to extract: the archive, the format to read it as, and the
// item's stored name, as its Listing gives it.
type ExtractRequest struct {
	// Path is the archive's file; it is only read.
	Path string
	// Format is the format 7-Zip reads it as.
	Format Format
	// Name is the item's Path in the archive's Listing, matched whole, never as a wildcard; empty
	// for a zstd stream's one item, and only then.
	Name string
}

// common are the switches every run takes: -p, an empty password, so 7zz never prompts for one,
// an encrypted item or header failing instead; and -sccUTF-8, its console output in UTF-8.
var common = []string{"-p", "-sccUTF-8"}

// stderrLimit is how much of 7zz's standard error a run keeps for its error.
const stderrLimit = 16 << 10

// List lists the archive req names, through one 7zz run of its technical listing, -slt, which
// reads the archive's headers and extracts nothing. A zstd stream's one item takes its size from
// the content size 7-Zip reports its frame header declares, in the archive's Method, when the
// item lists none. A listing 7zz ends with a warning, exit code
// 1, such as data after the archive's end, is a listing all the same. An archive whose headers are
// encrypted is refused with ErrEncrypted, one 7zz can't open as req's format with ErrNotArchive.
func (r *Reader) List(ctx context.Context, req ListRequest) (Listing, error) {
	path, err := check(req.Path, req.Format)
	if err != nil {
		return Listing{}, err
	}
	var stdout bytes.Buffer
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	args := append([]string{"l", "-slt", "-t" + string(req.Format)}, common...)
	args = append(args, "--", path)
	if err := r.run(ctx, args, &stdout, stderr); err != nil {
		return Listing{}, fmt.Errorf("sevenzip: list %s: %w", filepath.Base(req.Path), err)
	}
	l, err := parseListing(stdout.Bytes())
	if err != nil {
		return Listing{}, fmt.Errorf("sevenzip: list %s: %w", filepath.Base(req.Path), err)
	}
	if req.Format == Zstd {
		zstdSize(&l)
	}
	return l, nil
}

// Extract writes the content of the item req names to w, through one 7zz run whose output is its
// standard output alone, -so: 7zz writes no file. The name is matched whole, -spd keeping it from
// being a wildcard and -i! from being a list file. An item that needs a password is refused with
// ErrEncrypted. A write to w that fails stops 7zz, and Extract returns once it has exited. An item
// the name matches none of writes nothing and is no error: the caller holds what it reads to the
// listing's size and CRC.
func (r *Reader) Extract(ctx context.Context, req ExtractRequest, w io.Writer) error {
	path, err := check(req.Path, req.Format)
	if err != nil {
		return err
	}
	if (req.Name == "") != (req.Format == Zstd) {
		return fmt.Errorf("sevenzip: %w: a name is required for %s and only for it absent", ErrRequest, req.Format)
	}
	args := append([]string{"e", "-so", "-spd", "-bd", "-t" + string(req.Format)}, common...)
	if req.Name != "" {
		args = append(args, "-i!"+req.Name)
	}
	args = append(args, "--", path)
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	if err := r.run(ctx, args, w, stderr); err != nil {
		return fmt.Errorf("sevenzip: extract %q from %s: %w", req.Name, filepath.Base(req.Path), err)
	}
	return nil
}

// check refuses a request with no path or a format the package doesn't read, and returns the
// path made absolute, so it never begins with a "-" or "@".
func check(path string, format Format) (string, error) {
	if path == "" {
		return "", fmt.Errorf("sevenzip: %w: no path", ErrRequest)
	}
	if !formats[format] {
		return "", fmt.Errorf("sevenzip: %w: format %q", ErrRequest, format)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("sevenzip: %w: %w", ErrRequest, err)
	}
	return abs, nil
}

// run runs 7zz with args, its stdout to stdout and its stderr to stderr, and returns its outcome:
// nil for exit code 0, or 1, a warning; and otherwise an error naming 7zz's message, matching
// ErrEncrypted or ErrNotArchive when the message says so, and the run's *tool.ExitError.
func (r *Reader) run(ctx context.Context, args []string, stdout io.Writer, stderr *cli.LimitedBuffer) error {
	err := r.sevenZip.Run(ctx, tool.Invocation{Args: args, Stdout: stdout, Stderr: stderr})
	exit, ok := errors.AsType[*tool.ExitError](err)
	switch {
	case err == nil:
		return nil
	case !ok:
		return err
	case exit.Code == 1:
		return nil
	}
	message := errorLines(stderr.String())
	switch {
	case strings.Contains(message, "Cannot open encrypted archive"), strings.Contains(message, "Wrong password"):
		return fmt.Errorf("%w: %s: %w", ErrEncrypted, message, err)
	case strings.Contains(message, "Cannot open the file as"):
		return fmt.Errorf("%w: %s: %w", ErrNotArchive, message, err)
	case message != "":
		return fmt.Errorf("%s: %w", message, err)
	}
	return err
}

// errorLines returns 7zz's stderr lines that say what went wrong, its "ERROR:" lines and those
// after an "ERRORS:" line, joined by "; ".
func errorLines(stderr string) string {
	var lines []string
	after := false
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			after = false
		case line == "ERRORS:":
			after = true
		case strings.HasPrefix(line, "ERROR:"), strings.HasPrefix(line, "Open ERROR:"), after:
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "; ")
}
