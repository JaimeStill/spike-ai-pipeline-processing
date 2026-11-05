//go:build linux

package picture

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// pageWidth and pageHeight are, in pixels at 96 dpi, the page LibreOffice's Draw places a
// metafile on: A4, which metafileLocale makes its default page. The picture keeps its own size
// on the page, at its center, so rendering the page at a scale renders the picture at that scale.
const (
	pageWidth  = 794
	pageHeight = 1123
)

// metafileLocale is the locale the metafiles' soffice run is given, through its profile, so that
// Draw's default page is A4 whatever the paper of the system it runs on: its default page is the
// locale's paper, and without one, the system's, A4 in LibreOfficeImage and Letter under a US
// locale. A page of another shape than the pixel size asked for would stretch the picture.
const metafileLocale = "en-GB"

// metafileRegistry is the configuration the metafiles' soffice run starts its profile with:
// metafileLocale as LibreOffice's locale.
const metafileRegistry = `<?xml version="1.0" encoding="UTF-8"?>
<oor:items xmlns:oor="http://openoffice.org/2001/registry" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<item oor:path="/org.openoffice.Setup/L10N"><prop oor:name="ooSetupSystemLocale" oor:op="fuse"><value>` + metafileLocale + `</value></prop></item>
</oor:items>
`

// metafileFilter returns soffice's --convert-to for metafiles rendered at scale: LibreOffice's
// Draw PNG export, with the page's pixel size, scale times its size at 96 dpi, as its filter
// options. Naming the filter, not only the extension, keeps a file soffice doesn't open as a
// picture, such as text named .emf, from being exported as a page of a text document; soffice
// fails on it instead.
func metafileFilter(scale int) string {
	return fmt.Sprintf(`png:draw_png_Export:{"PixelWidth":{"type":"long","value":"%d"},"PixelHeight":{"type":"long","value":"%d"}}`,
		scale*pageWidth, scale*pageHeight)
}

// sofficeOutputLimit is the most of soffice's output a conversion keeps, for its error.
const sofficeOutputLimit = 64 << 10

// metafilesToPNG converts metafiles, a batch's EMFs and WMFs copied into directories of their
// own, to PNG: it renders each at scale through soffice, in one run in dir, whose profile starts
// with metafileRegistry, so the page each is rendered on is A4 at scale times 96 dpi, and trims
// each page to its picture through magick, writing the PNG beside its copy, then moves each PNG
// to its Picture's PNG, appending it to written. soffice names each page for its input's stem,
// the PNG's, which no two of a batch share. soffice exits zero having written nothing for a
// picture it can't export, so a page that isn't there is an error too, as is a trim that fails.
func (c *Converter) metafilesToPNG(ctx context.Context, dir string, scale int, metafiles []staged, written *[]string) error {
	var inputs []string
	for _, s := range metafiles {
		inputs = append(inputs, s.copied)
	}
	outdir, remove, msg, err := c.soffice(ctx, dir, metafileFilter(scale), inputs)
	if err != nil {
		return err
	}
	for _, s := range metafiles {
		page := filepath.Join(outdir, pngStem(s.PNG)+".png")
		if err := checkPNG(page); err != nil {
			return errors.Join(sofficeError(fmt.Errorf("%s: %w", s.name, err), msg), remove())
		}
		// -trim crops the page to the picture, the bounding box of what differs from the
		// corners' color, and +repage drops the crop's offset from the PNG; PNG in and out,
		// nothing is encoded lossily.
		args := slices.Concat([]string{"png:" + page, "-trim", "+repage"}, reproducible, []string{"png:" + s.png})
		if err := c.runMagick(ctx, dir, s.name, args, s.png); err != nil {
			return errors.Join(err, remove())
		}
		if err := place(s, written); err != nil {
			return errors.Join(err, remove())
		}
	}
	if err := remove(); err != nil {
		return fmt.Errorf("picture: removing the libreoffice directory: %w", err)
	}
	return nil
}

// soffice runs soffice once in dir, the working directory the runners mount, converting each of
// inputs as convertTo says: the target extension and, after a colon, the export filter. It makes
// a directory of its own there, libreoffice-*, holding soffice's user profile, which starts with
// metafileRegistry, so no run shares or leaves a profile elsewhere, and its output directory,
// which it returns. soffice names each output for its input's stem. It returns a function that
// removes the directory, which the caller runs when it is done with the output, and soffice's
// message, for an error the caller finds in the output. A run that fails removes the directory
// itself, and its error carries soffice's message.
func (c *Converter) soffice(ctx context.Context, dir, convertTo string, inputs []string) (outdir string, remove func() error, msg string, err error) {
	work, err := os.MkdirTemp(dir, "libreoffice-*")
	if err != nil {
		return "", nil, "", fmt.Errorf("picture: libreoffice directory: %w", err)
	}
	remove = func() error { return os.RemoveAll(work) }
	if err := writeRegistry(filepath.Join(work, "profile"), metafileRegistry); err != nil {
		return "", nil, "", errors.Join(fmt.Errorf("picture: libreoffice profile: %w", err), remove())
	}
	profile := url.URL{Scheme: "file", Path: filepath.Join(work, "profile")}
	outdir = filepath.Join(work, "out")
	out := &limitedBuffer{limit: sofficeOutputLimit}
	args := []string{
		"-env:UserInstallation=" + profile.String(),
		"--headless", "--norestore",
		"--convert-to", convertTo,
		"--outdir", outdir,
	}
	err = c.libreoffice.Run(ctx, tool.Invocation{
		Args:   append(args, inputs...),
		Dir:    dir,
		Stdout: out,
		Stderr: out,
	})
	msg = sofficeMessage(out.String())
	if err != nil {
		return "", nil, "", errors.Join(sofficeError(err, msg), remove())
	}
	return outdir, remove, msg, nil
}

// writeRegistry writes registry as the registrymodifications.xcu of the soffice user profile at
// profile, before soffice first runs on it.
func writeRegistry(profile, registry string) error {
	user := filepath.Join(profile, "user")
	if err := os.MkdirAll(user, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(user, "registrymodifications.xcu"), []byte(registry), 0o600)
}

// sofficeError returns err as soffice's error, with soffice's message, msg, when it has one.
func sofficeError(err error, msg string) error {
	err = fmt.Errorf("picture: libreoffice: %w", err)
	if msg != "" {
		err = fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// sofficeMessage returns soffice's message in its output: the first line beginning "Error", or
// else the last line that isn't blank.
func sofficeMessage(out string) string {
	last := ""
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Error") {
			return line
		}
		if line != "" {
			last = line
		}
	}
	return last
}
