//go:build linux

package document_test

import (
	"bytes"
	"errors"
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
