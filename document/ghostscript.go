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

var (
	_ RegionRenderer = (*Ghostscript)(nil)
	_ TextExtractor  = (*Ghostscript)(nil)
)

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
	return g.png(ctx, tool.Invocation{Args: renderArgs(pdf, page, dpi), Dir: dir, Stdin: stdin, Usage: usage}, pdf, page, w)
}

// RenderRegion writes the region of page, 1-based, of the PDF at pdf to w as PNG at dpi, one gs
// run for the page, as Render writes the whole page: the same device and anti-aliasing, so the
// region's pixels are the whole page's, cropped. It refuses what Render refuses, and a region
// CheckRegion refuses, before gs runs. A gs that exits nonzero gives the Tool's error, and w may
// hold part of a PNG then; a page past the last writes nothing and is an error carrying the
// message the run prints in place of a PNG.
//
// gs crops in its own PostScript, run with -c, as regionArgs says: the region's size, in the
// page's points, is the device's page size, and the page is drawn translated so the region's
// corner is the device's.
func (g *Ghostscript) RenderRegion(ctx context.Context, pdf string, page, dpi int, region Region, w io.Writer) error {
	if err := CheckRender(page, dpi); err != nil {
		return err
	}
	if err := CheckRegion(region); err != nil {
		return err
	}
	abs, err := filepath.Abs(pdf)
	if err != nil {
		return fmt.Errorf("document: ghostscript: %w", err)
	}
	return g.png(ctx, tool.Invocation{Args: regionArgs(abs, page, dpi, region), Dir: filepath.Dir(abs)}, abs, page, w)
}

// png runs gs as inv says, its stdout a page's PNG, and writes the PNG to w, holding back a stdout
// that doesn't begin as a PNG and returning it as the error.
func (g *Ghostscript) png(ctx context.Context, inv tool.Invocation, pdf string, page int, w io.Writer) error {
	out := &pngGuard{w: w}
	inv.Stdout = out
	if err := g.gs.Run(ctx, inv); err != nil {
		return err
	}
	if !out.png {
		return fmt.Errorf("document: ghostscript: page %d of %s: no PNG on stdout: %s", page, pdf, strings.TrimSpace(string(out.head)))
	}
	return nil
}

// pastLast is the start of the message gs prints for a FirstPage past a PDF's last page.
const pastLast = "Requested FirstPage is greater than the number of pages"

// textStderrLimit bounds how much of gs's stderr Text keeps, for its message and its errors.
const textStderrLimit = 4 << 10

// Text returns the text of page, 1-based, of the PDF at pdf, as its text layer holds it, one gs
// run with the txtwrite device: the page's text laid out in lines, gs's own spacing kept. A page
// with no text layer, such as a scanned page, returns blank text, which HasText reports as none.
// It refuses a page below 1 before gs runs, as CheckRender does. A gs that exits nonzero gives
// the Tool's error, a *tool.ExitError carrying gs's stderr.
//
// gs runs with -sstdout=%stderr, so its own messages go to stderr and stdout holds the text
// alone. A page past the last prints gs's message there, "Requested FirstPage is greater than
// the number of pages in the file", and exits zero; Text returns it as an error wrapping
// ErrPageRange.
func (g *Ghostscript) Text(ctx context.Context, pdf string, page int) (string, error) {
	if page < 1 {
		return "", fmt.Errorf("document: page %d: %w", page, ErrPageRange)
	}
	abs, err := filepath.Abs(pdf)
	if err != nil {
		return "", fmt.Errorf("document: ghostscript: %w", err)
	}
	var out strings.Builder
	stderr := limitedBuffer{limit: textStderrLimit}
	err = g.gs.Run(ctx, tool.Invocation{Args: textArgs(abs, page), Dir: filepath.Dir(abs), Stdout: &out, Stderr: &stderr})
	if err != nil {
		// Run keeps stderr for its *tool.ExitError only when the caller doesn't take it.
		if exit, ok := errors.AsType[*tool.ExitError](err); ok && exit.Stderr == "" {
			exit.Stderr = stderr.String()
		}
		return "", err
	}
	if msg := strings.TrimSpace(stderr.String()); strings.Contains(msg, pastLast) {
		return "", fmt.Errorf("document: ghostscript: page %d of %s: %s: %w", page, abs, msg, ErrPageRange)
	}
	return out.String(), nil
}

// renderArgs returns gs's arguments for page of the PDF at pdf at dpi, as the bake-off measured
// them, reading stdin when pdf is "". png16m is 24-bit RGB; the alpha bits anti-alias text and
// graphics, as poppler's pdftoppm and pdftocairo do by default and gs doesn't. -sOutputFile=-
// writes the PNG to stdout.
func renderArgs(pdf string, page, dpi int) []string {
	if pdf == "" {
		pdf = "-"
	}
	return []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=png16m",
		"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-r" + strconv.Itoa(dpi),
		"-dFirstPage=" + strconv.Itoa(page), "-dLastPage=" + strconv.Itoa(page), "-sOutputFile=-", pdf}
}

// textArgs returns gs's arguments for the text of page of the PDF at pdf: the txtwrite device,
// its text to stdout, and gs's own messages to stderr.
func textArgs(pdf string, page int) []string {
	return []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sstdout=%stderr", "-sDEVICE=txtwrite",
		"-dFirstPage=" + strconv.Itoa(page), "-dLastPage=" + strconv.Itoa(page), "-sOutputFile=-", pdf}
}

// regionArgs returns gs's arguments for region of page of the PDF at pdf at dpi, the device and
// anti-aliasing renderArgs gives, the PNG to stdout. gs reads the PDF itself in PostScript,
// permitted to read that one file, checks the page, and draws it in pdfshowpage's steps, the
// region set between them:
//
//   - A page past the last prints a message in place of a PNG and quits.
//   - pdfshowpage_setpage sets the device's page size to the page's, rotation applied, and the
//     CTM to place the page on it. The page's own transform is taken out of that CTM, against
//     the device's default matrix.
//   - setpagedevice sets the device's page size to the region's, its width and height the page's
//     times the region's fractions, and resets the CTM to the new default. The page is then
//     translated left by the region's left edge and down by the band below its bottom edge, in
//     the page's points, and its own transform applied again, so the region alone lands on the
//     device.
//   - pdfshowpage_finish draws the page and shows it.
//
// The path is a PostScript string, escaped as countArgs escapes it, and the fractions are
// decimals, CheckRegion having refused any that isn't a number.
func regionArgs(pdf string, page, dpi int, r Region) []string {
	n := strconv.Itoa(page)
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	ps := "(" + psString(pdf) + ") (r) file runpdfbegin " +
		n + " pdfpagecount gt { (Requested page " + n + " is greater than the number of pages in the file) = quit } if " +
		n + " pdfgetpage pdfshowpage_setpage " +
		"matrix currentmatrix matrix defaultmatrix matrix invertmatrix matrix concatmatrix " +
		"currentpagedevice /PageSize get aload pop /ph exch def /pw exch def " +
		"<< /PageSize [pw " + f(r.Right) + " " + f(r.Left) + " sub mul ph " + f(r.Bottom) + " " + f(r.Top) + " sub mul] >> setpagedevice " +
		"pw " + f(r.Left) + " mul neg ph 1 " + f(r.Bottom) + " sub mul neg translate concat " +
		"pdfshowpage_finish runpdfend quit"
	return []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "--permit-file-read=" + pdf, "-sDEVICE=png16m",
		"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-r" + strconv.Itoa(dpi), "-sOutputFile=-", "-c", ps}
}

// countArgs returns gs's arguments to print the page count of the PDF at pdf. The path is a
// PostScript string there, whose backslashes and parentheses are escaped.
func countArgs(pdf string) []string {
	return []string{"-q", "-dNODISPLAY", "-dSAFER", "--permit-file-read=" + pdf,
		"-c", "(" + psString(pdf) + ") (r) file runpdfbegin pdfpagecount = quit"}
}

// psString escapes s for a PostScript string: its backslashes and parentheses.
func psString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(s)
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
