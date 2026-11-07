//go:build linux

package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// PDFDetachMinimum is the oldest pdfdetach the package accepts: poppler 22.02, the oldest poppler
// the renderer bake-off accepted for its utilities.
const PDFDetachMinimum = "22.02"

// PDFDetachSpec declares poppler's pdfdetach, minimum 22.02, for either runner's constructor.
// pdfdetach -v prints "pdfdetach version 26.10.0" and its copyright to stderr and exits 99, as it
// does for its usage text, so the Spec's VersionExit is 99. A caller that needs another program
// path or a KillAfter period sets them on the Spec returned.
func PDFDetachSpec() tool.Spec {
	return tool.Spec{
		Program:        "pdfdetach",
		VersionArgs:    []string{"-v"},
		VersionPattern: regexp.MustCompile(`pdfdetach version (\d+(?:\.\d+)*)`),
		Minimum:        PDFDetachMinimum,
		VersionExit:    99,
	}
}

// ErrNoAttachment is the cause of a Save refused because the PDF has no attachment of the number
// given.
var ErrNoAttachment = errors.New("no such attachment")

// Detacher lists and saves a PDF's embedded files through poppler's pdfdetach: each file of the
// name tree's EmbeddedFiles and of a file attachment annotation, not the media behind a Screen or
// Rendition annotation, which pdfdetach doesn't read. Each method is one pdfdetach run, which
// reads the PDF by its absolute path in the PDF's directory. Its methods are safe for concurrent
// use.
type Detacher struct {
	pdfdetach *tool.Tool
}

// NewDetacher builds a Detacher over pdfdetach, which the caller built through either runner,
// typically from PDFDetachSpec. It refuses a nil Tool, and one older than PDFDetachMinimum,
// whatever minimum its own Spec declared, with a *tool.VersionError wrapping tool.ErrTooOld.
func NewDetacher(pdfdetach *tool.Tool) (*Detacher, error) {
	if pdfdetach == nil {
		return nil, errors.New("document: pdfdetach: no pdfdetach tool")
	}
	if err := atLeast(pdfdetach, PDFDetachMinimum); err != nil {
		return nil, err
	}
	return &Detacher{pdfdetach: pdfdetach}, nil
}

// Attachment is one embedded file of a PDF, as pdfdetach lists it: its number, 1-based, which
// Save takes, and its stored name, which is only ever a name.
type Attachment struct {
	Number int
	Name   string
}

// List returns the embedded files of the PDF at pdf, in pdfdetach's order, through pdfdetach
// -list, their names in UTF-8, -enc UTF-8. pdfdetach prints a count, then each file's number and
// name, one to a line; a name with a line break in it is read whole, its later lines being no
// number's. A listing whose count its lines don't match fails.
func (d *Detacher) List(ctx context.Context, pdf string) ([]Attachment, error) {
	abs, err := filepath.Abs(pdf)
	if err != nil {
		return nil, fmt.Errorf("document: pdfdetach: %w", err)
	}
	var stdout bytes.Buffer
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	if err := d.pdfdetach.Run(ctx, tool.Invocation{
		Args: []string{"-list", "-enc", "UTF-8", abs}, Dir: filepath.Dir(abs),
		Stdout: &capped{w: &stdout, limit: formLimit}, Stderr: stderr,
	}); err != nil {
		return nil, detachError("list "+filepath.Base(abs), stderr.String(), err)
	}
	list, err := parseDetachList(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("document: pdfdetach: list %s: %w", filepath.Base(abs), err)
	}
	return list, nil
}

// Save writes the content of embedded file number of the PDF at pdf to w, through pdfdetach
// -save N, its output file /dev/stdout: the path is the package's, never the file's stored name,
// and pdfdetach writes no file of its own. A write to w that fails stops pdfdetach, and Save
// returns once it has exited. A number the PDF has no file of is refused with ErrNoAttachment.
func (d *Detacher) Save(ctx context.Context, pdf string, number int, w io.Writer) error {
	if number < 1 {
		return fmt.Errorf("document: pdfdetach: %w: %d", ErrNoAttachment, number)
	}
	abs, err := filepath.Abs(pdf)
	if err != nil {
		return fmt.Errorf("document: pdfdetach: %w", err)
	}
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	if err := d.pdfdetach.Run(ctx, tool.Invocation{
		Args: []string{"-save", strconv.Itoa(number), "-o", "/dev/stdout", abs}, Dir: filepath.Dir(abs),
		Stdout: w, Stderr: stderr,
	}); err != nil {
		if strings.Contains(stderr.String(), "Invalid file number") {
			return fmt.Errorf("document: pdfdetach: %w: %d: %w", ErrNoAttachment, number, err)
		}
		return detachError(fmt.Sprintf("save %d of %s", number, filepath.Base(abs)), stderr.String(), err)
	}
	return nil
}

// detachError returns a failed run's error, naming what it did and pdfdetach's message.
func detachError(what, stderr string, err error) error {
	if message := strings.Join(strings.Fields(stderr), " "); message != "" {
		return fmt.Errorf("document: pdfdetach: %s: %s: %w", what, message, err)
	}
	return fmt.Errorf("document: pdfdetach: %s: %w", what, err)
}

// listCount and listEntry match pdfdetach -list's count line and an entry's first line.
var (
	listCount = regexp.MustCompile(`^(\d+) embedded files$`)
	listEntry = regexp.MustCompile(`^(\d+): (.*)$`)
)

// parseDetachList reads pdfdetach -list's output: "N embedded files", then "i: name" for each, i
// from 1. A line that isn't the next number's continues the name before it.
func parseDetachList(out string) ([]Attachment, error) {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	m := listCount.FindStringSubmatch(lines[0])
	if m == nil {
		return nil, fmt.Errorf("listing begins %q, not a count", lines[0])
	}
	count, err := strconv.Atoi(m[1])
	if err != nil {
		return nil, fmt.Errorf("listing's count %q: %w", m[1], err)
	}
	list := []Attachment{}
	for _, line := range lines[1:] {
		if e := listEntry.FindStringSubmatch(line); e != nil && e[1] == strconv.Itoa(len(list)+1) {
			list = append(list, Attachment{Number: len(list) + 1, Name: e[2]})
			continue
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("listing's line %q names no file", line)
		}
		list[len(list)-1].Name += "\n" + line
	}
	if len(list) != count {
		return nil, fmt.Errorf("listing counts %d files and names %d", count, len(list))
	}
	return list, nil
}
