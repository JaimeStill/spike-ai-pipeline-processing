//go:build linux

package picture

import (
	"regexp"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// MagickMinimum is the oldest ImageMagick the package accepts, for the conversions and the
// corrections stage alike: 7.1.
const MagickMinimum = "7.1"

// MagickSpec declares ImageMagick's magick, minimum 7.1, for either runner's constructor.
// magick -version prints "Version: ImageMagick 7.1.2-34 Q16-HDRI ...", and the pattern takes the
// dotted part before the patch level. A caller that needs another program path or a KillAfter
// period sets them on the Spec returned.
func MagickSpec() tool.Spec {
	return tool.Spec{
		Program:        "magick",
		VersionArgs:    []string{"-version"},
		VersionPattern: regexp.MustCompile(`Version: ImageMagick (\d+(?:\.\d+)*)`),
		Minimum:        MagickMinimum,
	}
}

// LibreOfficeMinimum is the oldest LibreOffice the package accepts: 24.2, the release Ubuntu
// 24.04 LTS ships, the oldest in a current long-term distribution. The Draw PNG export the
// metafiles' conversion names, and the legacy import and OOXML export filters the office
// package's conversion names, are far older; the integration tier runs the default image's
// release.
const LibreOfficeMinimum = "24.2"

// LibreOfficeImage is the default image soffice runs in through the container runner:
// Gotenberg's 8.39.0 LibreOffice image, which carries LibreOffice 26.8.1.1, pinned by its
// multi-arch index digest. soffice runs in it under the runner's full lockdown, with no
// relaxation, so tool.Image{Ref: LibreOfficeImage} is the whole of it. A caller may run another
// image, or soffice on the host through the exec runner.
const LibreOfficeImage = "gotenberg/gotenberg:8.39.0-libreoffice@sha256:" +
	"4c0eb92ef22e5d5fb0a49eac924b63d85d0b0334ea3365a1f5ca428f367b3054"

// LibreOfficeSpec declares LibreOffice's soffice, minimum 24.2, for either runner's constructor;
// the container runner makes it the entrypoint in place of the image's own. soffice --version
// prints "LibreOffice 26.8.1.1 680(Build:1)", after any warnings of its desktop libraries on
// stderr, and the pattern takes the dotted numbers after the name. A caller that needs another
// program path or a KillAfter period sets them on the Spec returned.
func LibreOfficeSpec() tool.Spec {
	return tool.Spec{
		Program:        "soffice",
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`LibreOffice (\d+(?:\.\d+)*)`),
		Minimum:        LibreOfficeMinimum,
	}
}

// atLeast refuses t when its version is older than minimum, whatever minimum its own Spec
// declared, with a *tool.VersionError wrapping tool.ErrTooOld.
func atLeast(t *tool.Tool, minimum string) error {
	m, err := tool.ParseVersion(minimum)
	if err != nil {
		return err
	}
	if v := t.Version(); v.Compare(m) < 0 {
		return &tool.VersionError{Program: t.Name(), Found: v.String(), Minimum: minimum, Err: tool.ErrTooOld}
	}
	return nil
}
