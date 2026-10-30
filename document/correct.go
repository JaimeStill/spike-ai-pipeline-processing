//go:build linux

package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// Correction is one ImageMagick correction for a scanned page. Its values are in the order the
// stage applies them, whatever order a caller lists them in.
type Correction int

const (
	// Deskew straightens a page scanned at an angle: -deskew 40%.
	Deskew Correction = iota + 1
	// Despeckle removes scanner speckle: -despeckle.
	Despeckle
	// ContrastStretch restores faded print: -contrast-stretch 0.5%x0.5%.
	ContrastStretch
	// Unsharp sharpens blurred print: -unsharp 0x1.
	Unsharp
	// Grayscale drops the page's color: -colorspace Gray.
	Grayscale
)

// correction is a Correction's name, its description for a prompt, and its ImageMagick operator
// with its arguments.
type correction struct {
	name        string
	description string
	operator    []string
}

// known holds every Correction's name, description, and operator: the one table the catalog,
// ParseCorrection, and the stage read.
var known = map[Correction]correction{
	Deskew: {"Deskew",
		"Straightens a page scanned at an angle; use it when lines of print run uphill or downhill.",
		[]string{"-deskew", "40%"}},
	Despeckle: {"Despeckle",
		"Removes scanner speckle, isolated dots across the page; use it when specks litter the background or the print.",
		[]string{"-despeckle"}},
	ContrastStretch: {"ContrastStretch",
		"Darkens faded print against its background; use it when the text is pale or the page looks washed out.",
		[]string{"-contrast-stretch", "0.5%x0.5%"}},
	Unsharp: {"Unsharp",
		"Sharpens soft, blurred print; use it when letters' edges are fuzzy or their strokes run together.",
		[]string{"-unsharp", "0x1"}},
	Grayscale: {"Grayscale",
		"Drops the page's color; use it when color carries nothing the reader needs, as on a tinted or yellowed scan.",
		[]string{"-colorspace", "Gray"}},
}

// String returns the correction's name, or Correction(N) for a value that is none.
func (c Correction) String() string {
	if corr, ok := known[c]; ok {
		return corr.name
	}
	return fmt.Sprintf("Correction(%d)", int(c))
}

// Description returns the correction's one-line description for a prompt: what it fixes and
// when to use it. A value that is no Correction returns "".
func (c Correction) Description() string {
	return known[c].description
}

// Corrections returns every Correction, in the fixed order the stage applies them: the catalog a
// prompt lists, each with its String and Description. The slice is the caller's own.
func Corrections() []Correction {
	return slices.Sorted(maps.Keys(known))
}

// ParseCorrection returns the Correction whose name is name, as String gives it, compared without
// regard to case, so "deskew" and "DESKEW" are Deskew. A name that is no correction's is an error
// naming it.
func ParseCorrection(name string) (Correction, error) {
	for c, corr := range known {
		if strings.EqualFold(corr.name, name) {
			return c, nil
		}
	}
	return 0, fmt.Errorf("document: unknown correction %q", name)
}

// MagickMinimum is the oldest ImageMagick the corrections stage accepts.
const MagickMinimum = "7.1"

// MagickSpec declares ImageMagick's magick, minimum 7.1, for either runner's constructor.
// magick -version prints "Version: ImageMagick 7.1.2-34 Q16-HDRI ...", and the pattern takes
// the dotted part before the patch level. A caller that needs another program path or a
// KillAfter period sets them on the Spec returned.
func MagickSpec() tool.Spec {
	return tool.Spec{
		Program:        "magick",
		VersionArgs:    []string{"-version"},
		VersionPattern: regexp.MustCompile(`Version: ImageMagick (\d+(?:\.\d+)*)`),
		Minimum:        MagickMinimum,
	}
}

// Corrector is the corrections stage: it runs a page image through the ImageMagick operators of
// the corrections a request names.
type Corrector struct {
	magick *tool.Tool
}

// NewCorrector builds the corrections stage over magick, which the caller built through either
// runner, typically from MagickSpec. It refuses a nil Tool, and a Tool whose version is older
// than MagickMinimum, whatever minimum its own Spec declared, with a *tool.VersionError wrapping
// tool.ErrTooOld.
func NewCorrector(magick *tool.Tool) (*Corrector, error) {
	if magick == nil {
		return nil, errors.New("document: corrector: no magick tool")
	}
	minimum, err := tool.ParseVersion(MagickMinimum)
	if err != nil {
		return nil, err
	}
	if v := magick.Version(); v.Compare(minimum) < 0 {
		return nil, &tool.VersionError{Program: magick.Name(), Found: v.String(), Minimum: MagickMinimum, Err: tool.ErrTooOld}
	}
	return &Corrector{magick: magick}, nil
}

// Correct writes the page image read from in to out with corrections applied, for any page
// image, rendered or not. The operators run in the fixed order of the Correction values, each
// once however often it is listed, in one magick run that reads PNG on stdin and writes PNG on
// stdout. No corrections copies in to out without running magick. A value that is no Correction
// is refused before anything runs. A magick that fails gives the Tool's error, and out may hold
// part of an image then.
func (c *Corrector) Correct(ctx context.Context, corrections []Correction, in io.Reader, out io.Writer) error {
	for _, corr := range corrections {
		if _, ok := known[corr]; !ok {
			return fmt.Errorf("document: unknown correction %v", corr)
		}
	}
	if len(corrections) == 0 {
		_, err := io.Copy(out, in)
		return err
	}
	args := []string{"png:-"}
	for _, corr := range slices.Compact(slices.Sorted(slices.Values(corrections))) {
		args = append(args, known[corr].operator...)
	}
	args = append(args, "png:-")
	return c.magick.Run(ctx, tool.Invocation{Args: args, Stdin: in, Stdout: out})
}

// GrayscaleColored is the most of a page's pixels that may be colored, as Assessment.Colored
// measures them, for Assess to call the page grayscale: one in a thousand.
const GrayscaleColored = 0.001

// Assessment is one page image's measures of the defects the corrections fix, from Assess, for a
// layer above to choose corrections by. Each measure is a fraction of ImageMagick's quantum
// range, 0 to 1, unless it says otherwise. They compare best between images of the same page:
// how much print a page holds moves Contrast, Noise, and Sharpness as well as its defects do.
type Assessment struct {
	// Width and Height are the image's size in pixels.
	Width, Height int
	// Skew is the angle of the page's lines of print, in degrees, as -deskew 40% measures it:
	// %[deskew:angle]. 0 is level; a page rotated clockwise by an angle reports about its
	// negative, so a page rotated 2.5 degrees clockwise reports about -2.5. Deskew corrects it.
	Skew float64
	// Contrast is the standard deviation of the image's luminance, 0 to 0.5: 0 for a page of one
	// shade, higher as print stands out from its background. Faded print lowers it;
	// ContrastStretch raises it.
	Contrast float64
	// Noise is the mean difference between the image's luminance and its 3x3 median, 0 to 1: 0
	// for a page the median leaves unchanged, higher as isolated specks the median removes cover
	// more of it. Print's own edges count for a little. Despeckle lowers it.
	Noise float64
	// Sharpness is the standard deviation of the image's luminance under a Laplacian, the
	// variance-of-the-Laplacian measure, from 0 up, about 0.05 for a clean 150-dpi page of
	// print: higher as edges are crisper, lower as blur softens them. Speckle raises it too, so
	// read it beside Noise. Unsharp raises it.
	Sharpness float64
	// Colored is the fraction of the image's pixels whose chroma, as HCL measures it, exceeds
	// 0.2, 0 to 1: 0 for a gray page, small for a gray page with a colored stamp.
	Colored float64
	// Grayscale is whether the page is effectively gray: Colored is below GrayscaleColored.
	// Grayscale drops the color of a page that isn't.
	Grayscale bool
}

// assessArgs are magick's arguments for Assess: PNG on stdin, flattened onto white, and each
// measure printed as name=value on a line of its own. The deskew angle and the colored fraction
// are taken from clones of the color image; the rest from its luminance, -colorspace Gray.
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

// assessOutputLimit bounds how much of magick's stdout Assess reads: its seven lines are far
// shorter.
const assessOutputLimit = 4 << 10

// Assess measures the page image read from in, PNG, for the defects the corrections fix: its
// skew, contrast, noise, sharpness, and color, and its size, in one magick run that reads PNG on
// stdin and prints the measures. It works on any page image, rendered or not. A magick that
// fails gives the Tool's error; output that doesn't hold every measure, each once and a number,
// is an error quoting it.
func (c *Corrector) Assess(ctx context.Context, in io.Reader) (Assessment, error) {
	var out limitedBuffer
	out.limit = assessOutputLimit
	if err := c.magick.Run(ctx, tool.Invocation{Args: assessArgs, Stdin: in, Stdout: &out}); err != nil {
		return Assessment{}, err
	}
	return parseAssessment(out.String())
}

// parseAssessment reads Assess's measures from magick's output, a name=value line for each.
func parseAssessment(out string) (Assessment, error) {
	values := map[string]string{}
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if _, seen := values[name]; !ok || seen {
			return Assessment{}, fmt.Errorf("document: assess: magick printed %q", out)
		}
		values[name] = value
	}
	var a Assessment
	ints := map[string]*int{"width": &a.Width, "height": &a.Height}
	floats := map[string]*float64{
		"skew": &a.Skew, "contrast": &a.Contrast, "noise": &a.Noise,
		"sharpness": &a.Sharpness, "colored": &a.Colored,
	}
	if len(values) != len(ints)+len(floats) {
		return Assessment{}, fmt.Errorf("document: assess: magick printed %q", out)
	}
	for name, p := range ints {
		v, err := strconv.Atoi(values[name])
		if err != nil {
			return Assessment{}, fmt.Errorf("document: assess: %s in magick's %q: %w", name, out, err)
		}
		*p = v
	}
	for name, p := range floats {
		v, err := strconv.ParseFloat(values[name], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return Assessment{}, fmt.Errorf("document: assess: %s in magick's %q is no number", name, out)
		}
		*p = v
	}
	a.Grayscale = a.Colored < GrayscaleColored
	return a, nil
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
