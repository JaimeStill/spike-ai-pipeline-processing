//go:build linux

package document_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinGhostscript installs the stand-in gs at version, builds the Tool from spec through the
// exec runner, and returns it with the stand-in's run log.
func standinGhostscript(t *testing.T, version string, spec tool.Spec) (*tool.Tool, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "gs", version)
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return tl, runs
}

// standinRenderer builds the released renderer over the stand-in gs at 10.08.0, and returns it
// with the stand-in's run log and a PDF path, relative to the working directory, in a directory
// of its own. The stand-in reads no PDF, so the file is empty.
func standinRenderer(t *testing.T) (*document.Ghostscript, func() []standinRun, string) {
	t.Helper()
	tl, runs := standinGhostscript(t, "10.08.0", document.GhostscriptSpec())
	g, err := document.NewGhostscript(tl)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("in (a)", 0o700); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join("in (a)", "doc.pdf")
	if err := os.WriteFile(pdf, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return g, runs, pdf
}

// TestGhostscriptRenderArgs checks gs's arguments for a page at a DPI, as the bake-off measured
// them, and that the run reads the PDF by its absolute path from the PDF's directory.
func TestGhostscriptRenderArgs(t *testing.T) {
	for _, tc := range []struct{ page, dpi int }{{1, 150}, {7, 300}, {1 << 20, 72}} {
		g, runs, pdf := standinRenderer(t)
		abs, err := filepath.Abs(pdf)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := g.Render(t.Context(), pdf, tc.page, tc.dpi, &out); err != nil {
			t.Fatalf("Render(page %d, %d dpi): %v", tc.page, tc.dpi, err)
		}
		if !bytes.Equal(out.Bytes(), standinPage) {
			t.Errorf("Render wrote %q, want the stand-in's page %q", out.Bytes(), standinPage)
		}
		want := []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=png16m",
			"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-r" + strconv.Itoa(tc.dpi),
			"-dFirstPage=" + strconv.Itoa(tc.page), "-dLastPage=" + strconv.Itoa(tc.page), "-sOutputFile=-", abs}
		got := runs()
		if len(got) != 1 || !slices.Equal(got[0].Args, want) {
			t.Errorf("Render(page %d, %d dpi): gs ran %q, want one run with %q", tc.page, tc.dpi, got, want)
		}
		if len(got) == 1 && got[0].Dir != filepath.Dir(abs) {
			t.Errorf("Render ran gs in %s, want the PDF's directory %s", got[0].Dir, filepath.Dir(abs))
		}
	}
}

// TestGhostscriptRenderOneRunPerPage checks that each page is one gs run.
func TestGhostscriptRenderOneRunPerPage(t *testing.T) {
	g, runs, pdf := standinRenderer(t)
	for page := 1; page <= 3; page++ {
		if err := g.Render(t.Context(), pdf, page, 150, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(runs()); got != 3 {
		t.Errorf("gs ran %d times for 3 pages, want 3", got)
	}
}

// TestGhostscriptRenderRefuses checks that CheckRender's refusals come before gs runs.
func TestGhostscriptRenderRefuses(t *testing.T) {
	g, runs, pdf := standinRenderer(t)
	for _, tc := range []struct {
		page, dpi int
		want      error
	}{
		{1, 0, document.ErrDPI},
		{1, -150, document.ErrDPI},
		{0, 150, document.ErrPageRange},
		{-1, 150, document.ErrPageRange},
	} {
		var out bytes.Buffer
		if err := g.Render(t.Context(), pdf, tc.page, tc.dpi, &out); !errors.Is(err, tc.want) {
			t.Errorf("Render(page %d, %d dpi) returned %v, want %v", tc.page, tc.dpi, err, tc.want)
		}
		if out.Len() != 0 {
			t.Errorf("Render(page %d, %d dpi) wrote %q, want nothing", tc.page, tc.dpi, out.Bytes())
		}
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("gs ran %q, want no run", got)
	}
}

// TestGhostscriptRenderNotPNG checks that a gs that exits zero with no PNG on stdout, as gs does
// for a page past the last, is an error carrying gs's message, and that w gets none of it.
func TestGhostscriptRenderNotPNG(t *testing.T) {
	msg := "\n   Requested FirstPage is greater than the number of pages in the file: 6\n"
	for _, stdout := range []string{msg, "", "\x89PN"} {
		g, _, pdf := standinRenderer(t)
		t.Setenv(stdoutEnv, stdout)
		var out bytes.Buffer
		err := g.Render(t.Context(), pdf, 9, 150, &out)
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(stdout)) {
			t.Errorf("Render with gs writing %q returned %v, want an error carrying it", stdout, err)
		}
		if out.Len() != 0 {
			t.Errorf("Render with gs writing %q wrote %q, want nothing", stdout, out.Bytes())
		}
	}
}

// TestGhostscriptRenderFails checks that a gs that exits nonzero is the Tool's *tool.ExitError.
func TestGhostscriptRenderFails(t *testing.T) {
	g, runs, pdf := standinRenderer(t)
	t.Setenv(failEnv, "Error: /undefined in --runpdf--")
	err := g.Render(t.Context(), pdf, 1, 150, &bytes.Buffer{})
	var exit *tool.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(exit.Stderr, "/undefined") {
		t.Errorf("Render returned %v, want a *tool.ExitError with code 1 and gs's stderr", err)
	}
	if got := len(runs()); got != 1 {
		t.Errorf("gs ran %d times, want 1", got)
	}
}

// TestGhostscriptCount checks Count's arguments, its working directory, and its parsing of gs's
// output, the PDF's path escaped as a PostScript string.
func TestGhostscriptCount(t *testing.T) {
	for _, tc := range []struct {
		stdout string
		want   int
		ok     bool
	}{
		{"6\n", 6, true},
		{"  7  \n", 7, true},
		{"1", 1, true},
		{"", 0, false},
		{"Error: /undefined in runpdfbegin\n", 0, false},
	} {
		g, runs, pdf := standinRenderer(t)
		abs, err := filepath.Abs(pdf)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(stdoutEnv, tc.stdout)
		n, err := g.Count(t.Context(), pdf)
		if tc.ok && (err != nil || n != tc.want) {
			t.Errorf("Count with gs printing %q returned %d, %v, want %d", tc.stdout, n, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("Count with gs printing %q returned %d, want an error", tc.stdout, n)
		}
		ps := strings.NewReplacer("(", `\(`, ")", `\)`).Replace(abs)
		want := []string{"-q", "-dNODISPLAY", "-dSAFER", "--permit-file-read=" + abs,
			"-c", "(" + ps + ") (r) file runpdfbegin pdfpagecount = quit"}
		got := runs()
		if len(got) != 1 || !slices.Equal(got[0].Args, want) || got[0].Dir != filepath.Dir(abs) {
			t.Errorf("Count ran gs as %q, want one run with %q in %s", got, want, filepath.Dir(abs))
		}
	}
}

// TestGhostscriptCountFails checks that a gs that exits nonzero counting is a *tool.ExitError.
func TestGhostscriptCountFails(t *testing.T) {
	g, _, pdf := standinRenderer(t)
	t.Setenv(failEnv, "Error: /invalidfileaccess")
	_, err := g.Count(t.Context(), pdf)
	var exit *tool.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Errorf("Count returned %v, want a *tool.ExitError with code 1", err)
	}
}

func TestNewGhostscriptVersion(t *testing.T) {
	// A Spec with a lower minimum lets the stand-in's old version through the runner; the
	// renderer's own check still refuses it.
	lenient := document.GhostscriptSpec()
	lenient.Minimum = "9"
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"9.56.1", false},
		{"9.99", false},
		{"10.0", true},
		{"10.00.0", true},
		{"10.08.0", true},
		{"11.0", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			tl, _ := standinGhostscript(t, tc.version, lenient)
			g, err := document.NewGhostscript(tl)
			if tc.ok {
				if err != nil || g == nil {
					t.Errorf("NewGhostscript over %s returned %v, %v, want a renderer", tc.version, g, err)
				}
				return
			}
			var verr *tool.VersionError
			if !errors.As(err, &verr) || !errors.Is(err, tool.ErrTooOld) || verr.Minimum != document.GhostscriptMinimum {
				t.Errorf("NewGhostscript over %s returned %v, want a *tool.VersionError, ErrTooOld, minimum %s",
					tc.version, err, document.GhostscriptMinimum)
			}
			if g != nil {
				t.Errorf("NewGhostscript over %s returned a renderer with its error", tc.version)
			}
		})
	}
}

func TestNewGhostscriptNil(t *testing.T) {
	if g, err := document.NewGhostscript(nil); err == nil || g != nil {
		t.Errorf("NewGhostscript(nil) returned %v, %v, want an error", g, err)
	}
}

// TestGhostscriptSpecRefusesOld checks that GhostscriptSpec's own minimum refuses a gs before
// 10.0 at construction, through the runner.
func TestGhostscriptSpecRefusesOld(t *testing.T) {
	installStandin(t, "gs", "9.56.1")
	_, err := tool.NewExec(t.Context(), document.GhostscriptSpec())
	if !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewExec(GhostscriptSpec()) over 9.56.1 returned %v, want ErrTooOld", err)
	}
}

// TestGhostscriptText checks Text's one gs run: its arguments, its working directory, and gs's
// stdout passed through as the page's text, a blank one included.
func TestGhostscriptText(t *testing.T) {
	for _, tc := range []struct {
		page   int
		stdout *string
		want   string
	}{
		{1, nil, standinText},
		{7, new(""), ""},
		{3, new("\n\n  \n"), "\n\n  \n"},
		{2, new("ofÏce\ttoday\r\n"), "ofÏce\ttoday\r\n"},
	} {
		g, runs, pdf := standinRenderer(t)
		abs, err := filepath.Abs(pdf)
		if err != nil {
			t.Fatal(err)
		}
		if tc.stdout != nil {
			t.Setenv(stdoutEnv, *tc.stdout)
		}
		got, err := g.Text(t.Context(), pdf, tc.page)
		if err != nil || got != tc.want {
			t.Errorf("Text(page %d) = %q, %v, want %q", tc.page, got, err, tc.want)
		}
		want := []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sstdout=%stderr", "-sDEVICE=txtwrite",
			"-dFirstPage=" + strconv.Itoa(tc.page), "-dLastPage=" + strconv.Itoa(tc.page), "-sOutputFile=-", abs}
		r := runs()
		if len(r) != 1 || !slices.Equal(r[0].Args, want) || r[0].Dir != filepath.Dir(abs) {
			t.Errorf("Text(page %d) ran gs as %q, want one run with %q in %s", tc.page, r, want, filepath.Dir(abs))
		}
	}
}

// TestGhostscriptTextRefuses checks that a page below 1 is refused before gs runs.
func TestGhostscriptTextRefuses(t *testing.T) {
	g, runs, pdf := standinRenderer(t)
	for _, page := range []int{0, -1} {
		if _, err := g.Text(t.Context(), pdf, page); !errors.Is(err, document.ErrPageRange) {
			t.Errorf("Text(page %d) returned %v, want document.ErrPageRange", page, err)
		}
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("gs ran %q, want no run", got)
	}
}

// TestGhostscriptTextPastLast checks that gs's message for a page past the last, which it prints
// to stderr and exits zero, is an error carrying it and wrapping ErrPageRange, and that other
// stderr beside a page's text is no error.
func TestGhostscriptTextPastLast(t *testing.T) {
	g, _, pdf := standinRenderer(t)
	t.Setenv(stdoutEnv, "")
	t.Setenv(stderrEnv, "\n   Requested FirstPage is greater than the number of pages in the file: 6\n")
	_, err := g.Text(t.Context(), pdf, 9)
	if !errors.Is(err, document.ErrPageRange) || !strings.Contains(err.Error(), "number of pages in the file: 6") {
		t.Errorf("Text past the last page returned %v, want document.ErrPageRange with gs's message", err)
	}
	t.Setenv(stdoutEnv, "text")
	t.Setenv(stderrEnv, "   **** Warning: a font is missing\n")
	if got, err := g.Text(t.Context(), pdf, 1); err != nil || got != "text" {
		t.Errorf("Text with a warning on stderr = %q, %v, want the text", got, err)
	}
}

// TestGhostscriptTextFails checks that a gs that exits nonzero is the Tool's *tool.ExitError,
// carrying gs's stderr though Text takes it.
func TestGhostscriptTextFails(t *testing.T) {
	g, _, pdf := standinRenderer(t)
	t.Setenv(failEnv, "Error: /undefined in --runpdf--")
	_, err := g.Text(t.Context(), pdf, 1)
	var exit *tool.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(exit.Stderr, "/undefined") {
		t.Errorf("Text returned %v, want a *tool.ExitError with code 1 and gs's stderr", err)
	}
}

// TestGhostscriptRenderRegionArgs checks RenderRegion's one gs run, in the PDF's directory: the
// device, anti-aliasing, and DPI Render uses, the PNG to stdout, the PDF permitted alone and read
// in PostScript by its escaped absolute path, the page and the region's fractions as PostScript
// decimals, and the page's PNG passed through. That the PostScript crops the page to the region
// is the integration tier's to prove, on the host's gs.
func TestGhostscriptRenderRegionArgs(t *testing.T) {
	for _, tc := range []struct {
		page, dpi                int
		region                   document.Region
		top, bottom, left, right string
	}{
		{7, 300, document.Region{Top: 0.9, Bottom: 1, Left: 0, Right: 1}, "0.9", "1", "0", "1"},
		{1, 150, document.Region{Top: 0, Bottom: 0.125, Left: 0.5, Right: 1}, "0", "0.125", "0.5", "1"},
		{2, 72, document.Region{Top: 1e-7, Bottom: 1, Left: 0, Right: 0.3333333333333333}, "0.0000001", "1", "0", "0.3333333333333333"},
	} {
		g, runs, pdf := standinRenderer(t)
		abs, err := filepath.Abs(pdf)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := g.RenderRegion(t.Context(), pdf, tc.page, tc.dpi, tc.region, &out); err != nil {
			t.Fatalf("RenderRegion(page %d, %+v): %v", tc.page, tc.region, err)
		}
		if !bytes.Equal(out.Bytes(), standinPage) {
			t.Errorf("RenderRegion wrote %q, want the stand-in's page %q", out.Bytes(), standinPage)
		}
		r := runs()
		if len(r) != 1 || r[0].Dir != filepath.Dir(abs) {
			t.Fatalf("RenderRegion(page %d, %+v) ran gs as %q, want one run in %s", tc.page, tc.region, r, filepath.Dir(abs))
		}
		args := r[0].Args
		for _, flag := range []string{"-dSAFER", "--permit-file-read=" + abs, "-sDEVICE=png16m",
			"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-r" + strconv.Itoa(tc.dpi), "-sOutputFile=-"} {
			if !slices.Contains(args, flag) {
				t.Errorf("RenderRegion(page %d, %+v) ran gs with %q, want %s", tc.page, tc.region, args, flag)
			}
		}
		if len(args) < 2 || args[len(args)-2] != "-c" {
			t.Fatalf("RenderRegion(page %d, %+v) ran gs with %q, want PostScript last, after -c", tc.page, tc.region, args)
		}
		ps := args[len(args)-1]
		escaped := strings.NewReplacer("(", `\(`, ")", `\)`).Replace(abs)
		if !strings.HasPrefix(ps, "("+escaped+") (r) file ") {
			t.Errorf("RenderRegion's PostScript %q doesn't open the PDF by its escaped path %q", ps, escaped)
		}
		tokens := strings.Fields(ps)
		for _, want := range []string{strconv.Itoa(tc.page), tc.top, tc.bottom, tc.left, tc.right} {
			if !slices.Contains(tokens, want) {
				t.Errorf("RenderRegion(page %d, %+v)'s PostScript %q has no token %s", tc.page, tc.region, ps, want)
			}
		}
	}
}

// TestGhostscriptRenderRegionRefuses checks that CheckRender's and CheckRegion's refusals come
// before gs runs.
func TestGhostscriptRenderRegionRefuses(t *testing.T) {
	g, runs, pdf := standinRenderer(t)
	whole := document.Region{Top: 0, Bottom: 1, Left: 0, Right: 1}
	for _, tc := range []struct {
		page, dpi int
		region    document.Region
		want      error
	}{
		{1, 0, whole, document.ErrDPI},
		{0, 150, whole, document.ErrPageRange},
		{1, 150, document.Region{}, document.ErrRegion},
		{1, 150, document.Region{Top: 0.5, Bottom: 0.4, Left: 0, Right: 1}, document.ErrRegion},
		{1, 150, document.Region{Top: 0, Bottom: 1, Left: 0, Right: 1.5}, document.ErrRegion},
	} {
		var out bytes.Buffer
		if err := g.RenderRegion(t.Context(), pdf, tc.page, tc.dpi, tc.region, &out); !errors.Is(err, tc.want) {
			t.Errorf("RenderRegion(page %d, %d dpi, %+v) returned %v, want %v", tc.page, tc.dpi, tc.region, err, tc.want)
		}
		if out.Len() != 0 {
			t.Errorf("RenderRegion wrote %q, want nothing", out.Bytes())
		}
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("gs ran %q, want no run", got)
	}
}

// TestGhostscriptRenderRegionNotPNG checks that a run that prints a message in place of a PNG, as
// RenderRegion's PostScript does for a page past the last, is an error carrying it, and w gets
// none of it.
func TestGhostscriptRenderRegionNotPNG(t *testing.T) {
	g, _, pdf := standinRenderer(t)
	msg := "Requested page 9 is greater than the number of pages in the file\n"
	t.Setenv(stdoutEnv, msg)
	var out bytes.Buffer
	err := g.RenderRegion(t.Context(), pdf, 9, 150, document.Region{Top: 0, Bottom: 1, Left: 0, Right: 1}, &out)
	if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(msg)) {
		t.Errorf("RenderRegion with gs writing %q returned %v, want an error carrying it", msg, err)
	}
	if out.Len() != 0 {
		t.Errorf("RenderRegion wrote %q, want nothing", out.Bytes())
	}
}
