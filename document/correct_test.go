//go:build linux

package document_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// page stands in for a page image: the stand-in magick copies it through unchanged.
var page = []byte("\x89PNG\r\n\x1a\nnot really a page")

// all is every correction, in the fixed order.
var all = []document.Correction{
	document.Deskew, document.Despeckle, document.ContrastStretch, document.Unsharp, document.Grayscale,
}

func TestCorrectOperators(t *testing.T) {
	cases := []struct {
		corrections []document.Correction
		want        []string
	}{
		{[]document.Correction{document.Deskew}, []string{"-deskew", "40%"}},
		{[]document.Correction{document.Despeckle}, []string{"-despeckle"}},
		{[]document.Correction{document.ContrastStretch}, []string{"-contrast-stretch", "0.5%x0.5%"}},
		{[]document.Correction{document.Unsharp}, []string{"-unsharp", "0x1"}},
		{[]document.Correction{document.Grayscale}, []string{"-colorspace", "Gray"}},
		{all, []string{
			"-deskew", "40%", "-despeckle", "-contrast-stretch", "0.5%x0.5%", "-unsharp", "0x1",
			"-colorspace", "Gray",
		}},
		// The order the caller lists them in, and repeats, change nothing.
		{[]document.Correction{
			document.Grayscale, document.Unsharp, document.Grayscale, document.ContrastStretch,
			document.Despeckle, document.Deskew, document.Unsharp,
		}, []string{
			"-deskew", "40%", "-despeckle", "-contrast-stretch", "0.5%x0.5%", "-unsharp", "0x1",
			"-colorspace", "Gray",
		}},
		{[]document.Correction{document.Grayscale, document.Deskew}, []string{"-deskew", "40%", "-colorspace", "Gray"}},
	}
	for _, tc := range cases {
		t.Run(names(tc.corrections), func(t *testing.T) {
			c, runs := standinCorrector(t)
			var out bytes.Buffer
			if err := c.Correct(t.Context(), tc.corrections, bytes.NewReader(page), &out); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), page) {
				t.Errorf("Correct wrote %q, want the stand-in's copy of the input %q", out.Bytes(), page)
			}
			want := append(append([]string{"png:-"}, tc.want...), "png:-")
			if got := runs(); len(got) != 1 || !slices.Equal(got[0], want) {
				t.Errorf("magick ran with %q, want one run with %q", got, want)
			}
		})
	}
}

// TestCorrectOneRunPerPage checks that each page is one magick run, whatever the corrections.
func TestCorrectOneRunPerPage(t *testing.T) {
	c, runs := standinCorrector(t)
	for i := range 3 {
		in := append(slices.Clone(page), byte('0'+i))
		var out bytes.Buffer
		if err := c.Correct(t.Context(), all, bytes.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), in) {
			t.Errorf("page %d: Correct wrote %q, want %q", i+1, out.Bytes(), in)
		}
	}
	if got := len(runs()); got != 3 {
		t.Errorf("magick ran %d times for 3 pages, want 3", got)
	}
}

// TestCorrectEmptyRunsNothing checks that no corrections copy the page without running magick.
func TestCorrectEmptyRunsNothing(t *testing.T) {
	c, runs := standinCorrector(t)
	for _, corrections := range [][]document.Correction{nil, {}} {
		var out bytes.Buffer
		if err := c.Correct(t.Context(), corrections, bytes.NewReader(page), &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), page) {
			t.Errorf("Correct(%v) wrote %q, want the input %q", corrections, out.Bytes(), page)
		}
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("magick ran %q with no corrections, want no run", got)
	}
}

// TestCorrectRefusesUnknown checks that a value that is no Correction is refused before magick
// runs, even beside valid ones.
func TestCorrectRefusesUnknown(t *testing.T) {
	c, runs := standinCorrector(t)
	for _, bad := range []document.Correction{0, document.Grayscale + 1, -1} {
		var out bytes.Buffer
		err := c.Correct(t.Context(), []document.Correction{document.Deskew, bad}, bytes.NewReader(page), &out)
		if err == nil || !strings.Contains(err.Error(), bad.String()) {
			t.Errorf("Correct with %v returned %v, want an error naming it", bad, err)
		}
		if out.Len() != 0 {
			t.Errorf("Correct with %v wrote %q, want nothing", bad, out.Bytes())
		}
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("magick ran %q, want no run", got)
	}
}

// TestCorrectMagickFails checks that a magick that exits nonzero is the Tool's *tool.ExitError.
func TestCorrectMagickFails(t *testing.T) {
	c, runs := standinCorrector(t)
	t.Setenv(failEnv, "magick: improper image header")
	err := c.Correct(t.Context(), all, bytes.NewReader(page), &bytes.Buffer{})
	var exit *tool.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(exit.Stderr, "improper image header") {
		t.Errorf("Correct returned %v, want a *tool.ExitError with code 1 and magick's stderr", err)
	}
	if got := len(runs()); got != 1 {
		t.Errorf("magick ran %d times, want 1", got)
	}
}

func TestNewCorrectorVersion(t *testing.T) {
	// A Spec with a lower minimum lets the stand-in's old version through the runner; the
	// stage's own check still refuses it.
	lenient := document.MagickSpec()
	lenient.Minimum = "6"
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"6.9.13-25", false},
		{"7.0.11-14", false},
		{"7.1", true},
		{"7.1.0-0", true},
		{"7.1.2-34", true},
		{"8.0.0", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			tl, _ := standinMagick(t, tc.version, lenient)
			c, err := document.NewCorrector(tl)
			if tc.ok {
				if err != nil || c == nil {
					t.Errorf("NewCorrector over %s returned %v, %v, want a stage", tc.version, c, err)
				}
				return
			}
			var verr *tool.VersionError
			if !errors.As(err, &verr) || !errors.Is(err, tool.ErrTooOld) || verr.Minimum != document.MagickMinimum {
				t.Errorf("NewCorrector over %s returned %v, want a *tool.VersionError, ErrTooOld, minimum %s",
					tc.version, err, document.MagickMinimum)
			}
			if c != nil {
				t.Errorf("NewCorrector over %s returned a stage with its error", tc.version)
			}
		})
	}
}

func TestNewCorrectorNil(t *testing.T) {
	if c, err := document.NewCorrector(nil); err == nil || c != nil {
		t.Errorf("NewCorrector(nil) returned %v, %v, want an error", c, err)
	}
}

// TestMagickSpecRefusesOld checks that MagickSpec's own minimum refuses a magick before 7.1 at
// construction, through the runner.
func TestMagickSpecRefusesOld(t *testing.T) {
	installMagick(t, "7.0.11-14")
	_, err := tool.NewExec(t.Context(), document.MagickSpec())
	if !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewExec(MagickSpec()) over 7.0.11-14 returned %v, want ErrTooOld", err)
	}
}

// names names corrections for a subtest, joined by +.
func names(cs []document.Correction) string {
	n := make([]string, len(cs))
	for i, c := range cs {
		n[i] = c.String()
	}
	return strings.Join(n, "+")
}

// TestCorrectionsCatalog checks the catalog: every correction in the fixed order, each with its
// name and a one-line description, and a value that is no correction with neither.
func TestCorrectionsCatalog(t *testing.T) {
	got := document.Corrections()
	if !slices.Equal(got, all) {
		t.Fatalf("Corrections() = %v, want %v", got, all)
	}
	got[0] = 0 // The slice is the caller's own.
	if document.Corrections()[0] != document.Deskew {
		t.Error("changing Corrections()'s slice changed the catalog")
	}
	names := map[string]bool{}
	for _, c := range all {
		name, desc := c.String(), c.Description()
		if name == "" || strings.HasPrefix(name, "Correction(") || names[name] {
			t.Errorf("%d's name is %q, want a name of its own", int(c), name)
		}
		names[name] = true
		if desc == "" || strings.Contains(desc, "\n") || !strings.Contains(desc, "use it when") {
			t.Errorf("%s's description is %q, want one line saying when to use it", name, desc)
		}
	}
	for _, bad := range []document.Correction{0, document.Grayscale + 1} {
		if d := bad.Description(); d != "" {
			t.Errorf("%v's description is %q, want none", bad, d)
		}
	}
}

// TestParseCorrection checks a correction's name reads back as the correction, whatever its case,
// and an unknown name is an error naming it.
func TestParseCorrection(t *testing.T) {
	for _, c := range all {
		for _, name := range []string{c.String(), strings.ToLower(c.String()), strings.ToUpper(c.String())} {
			got, err := document.ParseCorrection(name)
			if err != nil || got != c {
				t.Errorf("ParseCorrection(%q) = %v, %v, want %v", name, got, err, c)
			}
		}
	}
	for _, name := range []string{"", "sharpen", "Deskew ", "Correction(1)", "contrast stretch"} {
		got, err := document.ParseCorrection(name)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) || got != 0 {
			t.Errorf("ParseCorrection(%q) = %v, %v, want an error naming it", name, got, err)
		}
	}
}

// assessArgs are Assess's magick arguments, as the stand-in logs them.
var assessArgs = []string{
	"png:-", "-background", "white", "-alpha", "remove", "-alpha", "off",
	"-format", `width=%w\nheight=%h\n`, "-write", "info:",
	"(", "+clone", "-deskew", "40%", "-format", `skew=%[deskew:angle]\n`, "-write", "info:", "+delete", ")",
	"(", "+clone", "-colorspace", "HCL", "-channel", "G", "-separate", "+channel", "-threshold", "20%",
	"-format", `colored=%[fx:mean]\n`, "-write", "info:", "+delete", ")",
	"-colorspace", "Gray", "-format", `contrast=%[fx:standard_deviation]\n`, "-write", "info:",
	"(", "+clone", "-define", "convolve:scale=!", "-morphology", "Convolve", "Laplacian:0",
	"-format", `sharpness=%[fx:standard_deviation]\n`, "-write", "info:", "+delete", ")",
	"(", "+clone", "-statistic", "Median", "3x3", ")", "-compose", "Difference", "-composite",
	"-format", `noise=%[fx:mean]\n`, "info:",
}

// assessOutput is magick's output for Assess, as the host's magick prints it for degraded.pdf's
// page 1 at 150 dpi.
const assessOutput = "width=1275\nheight=1650\nskew=-2.488338848312285\ncolored=0\n" +
	"contrast=0.108651\nsharpness=0.0461152\nnoise=0.00404191\n"

// TestAssess checks Assess's one magick run, its arguments, and its parsing of magick's output, a
// page with color's included.
func TestAssess(t *testing.T) {
	cases := []struct {
		name, out string
		want      document.Assessment
	}{
		{"skewed", assessOutput, document.Assessment{
			Width: 1275, Height: 1650, Skew: -2.488338848312285, Contrast: 0.108651,
			Noise: 0.00404191, Sharpness: 0.0461152, Colored: 0, Grayscale: true,
		}},
		{"colored", "width=850\nheight=1100\nskew=-0\ncolored=0.00257635\ncontrast=0.49\nsharpness=0.03\nnoise=9.42593e-05\n",
			document.Assessment{
				Width: 850, Height: 1100, Skew: 0, Contrast: 0.49, Noise: 9.42593e-05,
				Sharpness: 0.03, Colored: 0.00257635, Grayscale: false,
			}},
		{"in any order", "noise=0\nsharpness=0\ncontrast=0\ncolored=0.0009\nskew=7\nheight=2\nwidth=1\n",
			document.Assessment{Width: 1, Height: 2, Skew: 7, Colored: 0.0009, Grayscale: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, runs := standinCorrector(t)
			t.Setenv(stdoutEnv, tc.out)
			got, err := c.Assess(t.Context(), bytes.NewReader(page))
			if err != nil || got != tc.want {
				t.Errorf("Assess with magick printing %q = %+v, %v, want %+v", tc.out, got, err, tc.want)
			}
			if r := runs(); len(r) != 1 || !slices.Equal(r[0], assessArgs) {
				t.Errorf("magick ran %q, want one run of %q", r, assessArgs)
			}
		})
	}
}

// TestAssessMalformed checks that magick output missing a measure, repeating one, naming one it
// doesn't know, or giving one that isn't a number is an error quoting the output.
func TestAssessMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"missing a value": strings.Replace(assessOutput, "noise=0.00404191\n", "", 1),
		"repeated":        assessOutput + "noise=0.1\n",
		"unknown":         strings.Replace(assessOutput, "noise=", "speckle=", 1),
		"extra":           assessOutput + "speckle=0.1\n",
		"no equals":       strings.Replace(assessOutput, "noise=", "noise ", 1),
		"not a number":    strings.Replace(assessOutput, "skew=-2.488338848312285", "skew=", 1),
		"not an integer":  strings.Replace(assessOutput, "width=1275", "width=1275.5", 1),
		"NaN":             strings.Replace(assessOutput, "contrast=0.108651", "contrast=nan", 1),
		"infinite":        strings.Replace(assessOutput, "contrast=0.108651", "contrast=inf", 1),
		"magick's error":  "magick: no decode delegate for this image format\n",
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := standinCorrector(t)
			t.Setenv(stdoutEnv, out)
			got, err := c.Assess(t.Context(), bytes.NewReader(page))
			if err == nil || got != (document.Assessment{}) {
				t.Errorf("Assess with magick printing %q = %+v, %v, want an error", out, got, err)
			}
		})
	}
}

// TestAssessMagickFails checks that a magick that exits nonzero assessing is the Tool's
// *tool.ExitError.
func TestAssessMagickFails(t *testing.T) {
	c, _ := standinCorrector(t)
	t.Setenv(failEnv, "magick: improper image header")
	_, err := c.Assess(t.Context(), bytes.NewReader(page))
	var exit *tool.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(exit.Stderr, "improper image header") {
		t.Errorf("Assess returned %v, want a *tool.ExitError with code 1 and magick's stderr", err)
	}
}
