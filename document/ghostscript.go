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

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// GhostscriptMinimum is the oldest Ghostscript the released renderer accepts.
const GhostscriptMinimum = "10.0"

// GhostscriptSpec declares Ghostscript's gs, minimum 10.0, for either runner's constructor.
// gs --version prints the version alone, as "10.08.0", and the pattern takes it whole. A caller
// that needs another program path or a KillAfter period sets them on the Spec returned.
func GhostscriptSpec() tool.Spec {
	return tool.Spec{
		Program:        "gs",
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`^(\d+(?:\.\d+)*)`),
		Minimum:        GhostscriptMinimum,
	}
}

// Ghostscript is the released Renderer, the renderer bake-off's winner: it counts and renders a
// PDF's pages with Ghostscript's gs, one process per call. Ghostscript is AGPL-licensed; the
// renderer runs gs as a separate program through a tool.Tool and never links it.
//
// gs reads the PDF by its path, which Count and Render make absolute, and each run's working
// directory is the PDF's directory. The container runner mounts a run's working directory at the
// same absolute path, so the path names the same file through either runner.
type Ghostscript struct {
	gs *tool.Tool
}

var _ Renderer = (*Ghostscript)(nil)

// NewGhostscript builds the released renderer over gs, which the caller built through either
// runner, typically from GhostscriptSpec. It refuses a nil Tool, and a Tool whose version is
// older than GhostscriptMinimum, whatever minimum its own Spec declared, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func NewGhostscript(gs *tool.Tool) (*Ghostscript, error) {
	if gs == nil {
		return nil, errors.New("document: ghostscript: no gs tool")
	}
	minimum, err := tool.ParseVersion(GhostscriptMinimum)
	if err != nil {
		return nil, err
	}
	if v := gs.Version(); v.Compare(minimum) < 0 {
		return nil, &tool.VersionError{Program: gs.Name(), Found: v.String(), Minimum: GhostscriptMinimum, Err: tool.ErrTooOld}
	}
	return &Ghostscript{gs: gs}, nil
}

// Count returns the number of pages in the PDF at pdf, as gs's own PDF interpreter counts them
// with pdfpagecount. gs runs with -dSAFER, permitted to read that one file.
func (g *Ghostscript) Count(ctx context.Context, pdf string) (int, error) {
	abs, err := filepath.Abs(pdf)
	if err != nil {
		return 0, fmt.Errorf("document: ghostscript: %w", err)
	}
	var out bytes.Buffer
	if err := g.gs.Run(ctx, tool.Invocation{Args: countArgs(abs), Dir: filepath.Dir(abs), Stdout: &out}); err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		return 0, fmt.Errorf("document: ghostscript: page count of %s: gs printed %q", abs, out.String())
	}
	return n, nil
}

// Render writes page, 1-based, of the PDF at pdf to w as PNG at dpi, one gs run for the page. It
// refuses a DPI of zero or less and a page below 1 before gs runs, as CheckRender does. A gs that
// exits nonzero gives the Tool's error, a *tool.ExitError, and w may hold part of a PNG then.
//
// gs asked for a page past the last exits zero, writing its message to stdout in place of a PNG:
// "Requested FirstPage is greater than the number of pages in the file". Render writes nothing to
// w unless stdout begins as a PNG, and returns gs's message as its error when it doesn't.
func (g *Ghostscript) Render(ctx context.Context, pdf string, page, dpi int, w io.Writer) error {
	return g.render(ctx, pdf, nil, page, dpi, w, nil)
}

// render renders page as Render does, from the PDF at pdf, or from stdin when pdf is "", and
// fills usage, when not nil, as Invocation.Usage does. Render reads by path only; the renderer
// bake-off reads from stdin to probe streaming, and measures the run's usage.
func (g *Ghostscript) render(ctx context.Context, pdf string, stdin io.Reader, page, dpi int, w io.Writer, usage *tool.Usage) error {
	if err := CheckRender(page, dpi); err != nil {
		return err
	}
	var dir string
	if pdf != "" {
		abs, err := filepath.Abs(pdf)
		if err != nil {
			return fmt.Errorf("document: ghostscript: %w", err)
		}
		pdf, dir = abs, filepath.Dir(abs)
	}
	out := &pngGuard{w: w}
	err := g.gs.Run(ctx, tool.Invocation{Args: renderArgs(pdf, page, dpi), Dir: dir, Stdin: stdin, Stdout: out, Usage: usage})
	if err != nil {
		return err
	}
	if !out.png {
		return fmt.Errorf("document: ghostscript: page %d of %s: no PNG on stdout: %s", page, pdf, strings.TrimSpace(string(out.head)))
	}
	return nil
}

// renderArgs returns gs's arguments for page of the PDF at pdf at dpi, as the bake-off measured
// them, reading stdin when pdf is "". png16m is 24-bit RGB; the alpha bits anti-alias text and
// graphics, as the poppler renderers do by default and gs doesn't. -sOutputFile=- writes the
// PNG to stdout.
func renderArgs(pdf string, page, dpi int) []string {
	if pdf == "" {
		pdf = "-"
	}
	return []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=png16m",
		"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-r" + strconv.Itoa(dpi),
		"-dFirstPage=" + strconv.Itoa(page), "-dLastPage=" + strconv.Itoa(page), "-sOutputFile=-", pdf}
}

// countArgs returns gs's arguments to print the page count of the PDF at pdf. The path is a
// PostScript string there, whose backslashes and parentheses are escaped.
func countArgs(pdf string) []string {
	ps := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(pdf)
	return []string{"-q", "-dNODISPLAY", "-dSAFER", "--permit-file-read=" + pdf,
		"-c", "(" + ps + ") (r) file runpdfbegin pdfpagecount = quit"}
}

// pngSignature is the eight bytes every PNG begins with.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// pngGuardLimit bounds how much of a stdout that isn't a PNG pngGuard keeps for the error.
const pngGuardLimit = 4 << 10

// pngGuard passes a PNG through to w, and holds back a stdout that doesn't begin with the PNG
// signature, keeping its first pngGuardLimit bytes, so a gs message never reaches w as a page.
type pngGuard struct {
	w io.Writer
	// head holds stdout until its first eight bytes are known, and all of it, to the limit, once
	// they aren't the signature.
	head []byte
	// png is whether stdout began with the signature, and decided whether that is known.
	png, decided bool
}

func (g *pngGuard) Write(p []byte) (int, error) {
	if g.png {
		return g.w.Write(p)
	}
	if g.decided {
		g.keep(p)
		return len(p), nil
	}
	g.keep(p)
	n := min(len(g.head), len(pngSignature))
	if !bytes.Equal(g.head[:n], pngSignature[:n]) {
		g.decided = true
		g.head = g.head[:min(len(g.head), pngGuardLimit)]
		return len(p), nil
	}
	if n < len(pngSignature) {
		return len(p), nil
	}
	g.png, g.decided = true, true
	// head holds all of stdout so far, which is at most the signature plus p.
	if _, err := g.w.Write(g.head); err != nil {
		return 0, err
	}
	g.head = nil
	return len(p), nil
}

// keep appends p to head, up to pngGuardLimit for a stdout that isn't a PNG. Until the signature
// is decided head keeps everything, which is at most seven bytes plus p.
func (g *pngGuard) keep(p []byte) {
	if g.decided {
		p = p[:min(len(p), max(pngGuardLimit-len(g.head), 0))]
	}
	g.head = append(g.head, p...)
}
