//go:build linux

package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

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

// operators maps each Correction to its ImageMagick operator and arguments.
var operators = map[Correction][]string{
	Deskew:          {"-deskew", "40%"},
	Despeckle:       {"-despeckle"},
	ContrastStretch: {"-contrast-stretch", "0.5%x0.5%"},
	Unsharp:         {"-unsharp", "0x1"},
	Grayscale:       {"-colorspace", "Gray"},
}

var correctionNames = map[Correction]string{
	Deskew:          "Deskew",
	Despeckle:       "Despeckle",
	ContrastStretch: "ContrastStretch",
	Unsharp:         "Unsharp",
	Grayscale:       "Grayscale",
}

// String returns the correction's name, or Correction(N) for a value that is none.
func (c Correction) String() string {
	if name, ok := correctionNames[c]; ok {
		return name
	}
	return fmt.Sprintf("Correction(%d)", int(c))
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
		if _, ok := operators[corr]; !ok {
			return fmt.Errorf("document: unknown correction %v", corr)
		}
	}
	if len(corrections) == 0 {
		_, err := io.Copy(out, in)
		return err
	}
	ordered := slices.Compact(slices.Sorted(slices.Values(corrections)))
	args := []string{"png:-"}
	for _, corr := range ordered {
		args = append(args, operators[corr]...)
	}
	args = append(args, "png:-")
	return c.magick.Run(ctx, tool.Invocation{Args: args, Stdin: in, Stdout: out})
}
