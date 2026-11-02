//go:build linux

package office

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// MagickMinimum is the oldest ImageMagick the Converter accepts for TIFF pictures: 7.1, the
// minimum the document package's corrections stage declares too.
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

// DefaultMetafileScale is the scale an EMF or WMF picture is rendered at when its Request leaves
// MetafileScale zero: 3, three times its size at 96 dpi.
const DefaultMetafileScale = 3

// MaxMetafileScale is the largest MetafileScale a Request may ask for: 8, at which the page each
// picture is rendered on is 6352x8984 pixels, some 57 million, before the trim.
const MaxMetafileScale = 8

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

// pictureConverter is the converter a picture kind takes to PNG. Its zero value is none: the
// picture is handed on as stored, as one vision decodes, PNG, JPEG, GIF, or BMP, is, and any kind
// the picture standard names no converter for.
type pictureConverter int

const (
	// viaLibreOffice is a metafile, EMF or WMF, rendered to PNG through LibreOffice and trimmed
	// to the picture through ImageMagick.
	viaLibreOffice pictureConverter = iota + 1
	// viaMagick is a TIFF, converted to PNG through ImageMagick.
	viaMagick
)

// pictureConverters are the converters of the picture kinds that are converted, by their stored
// extension, lowercased; every other kind is handed on as stored.
var pictureConverters = map[string]pictureConverter{
	".emf":  viaLibreOffice,
	".wmf":  viaLibreOffice,
	".tif":  viaMagick,
	".tiff": viaMagick,
}

// metafileFilter returns soffice's --convert-to for metafiles rendered at scale: LibreOffice's
// Draw PNG export, with the page's pixel size, scale times its size at 96 dpi, as its filter
// options. Naming the filter, not only the extension, keeps a file soffice doesn't open as a
// picture, such as text named .emf, from being exported as a page of a text document; soffice
// fails on it instead.
func metafileFilter(scale int) string {
	return fmt.Sprintf(`png:draw_png_Export:{"PixelWidth":{"type":"long","value":"%d"},"PixelHeight":{"type":"long","value":"%d"}}`,
		scale*pageWidth, scale*pageHeight)
}

// pngSignature begins every PNG file.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// convertPictures converts the pictures in media, the media list of a conversion whose media
// directory is in dir, the working directory, to PNG, as the picture standard says: each EMF and
// WMF rendered at scale through the LibreOffice converter, all of them in one soffice run, and
// trimmed to the picture through magick, one run each; and each TIFF through magick, one run
// each. A converted picture's PNG is named by the stored name's stem, its
// SHA-256, and .png, and written into the media directory beside the stored file, which stays;
// its entry's File becomes the PNG and Converted true. A picture whose converter the Converter
// wasn't given, for a metafile either of LibreOffice and magick, is left with no File. Every other picture keeps its stored file. Two stored files
// that share a stem, the same bytes under two extensions, are converted once and handed on as the
// same PNG. A conversion that fails, or writes no PNG, is an error, and media is then only partly
// converted.
func (c *Converter) convertPictures(ctx context.Context, dir string, scale int, media []MediaFile) error {
	var metafiles []int
	tiffs := map[string]string{}
	for i := range media {
		switch pictureConverters[strings.ToLower(filepath.Ext(media[i].Name))] {
		case viaLibreOffice:
			if c.libreoffice == nil || c.magick == nil {
				media[i].File = ""
				continue
			}
			metafiles = append(metafiles, i)
		case viaMagick:
			if c.magick == nil {
				media[i].File = ""
				continue
			}
			if png, ok := tiffs[pngName(media[i].Name)]; ok {
				media[i].File, media[i].Converted = png, true
				continue
			}
			if err := c.tiffToPNG(ctx, dir, &media[i]); err != nil {
				return err
			}
			tiffs[pngName(media[i].Name)] = media[i].File
		}
	}
	if len(metafiles) == 0 {
		return nil
	}
	return c.metafilesToPNG(ctx, dir, scale, media, metafiles)
}

// pngName returns the name of a converted picture's PNG: the stored name's stem, its SHA-256,
// and .png.
func pngName(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name)) + ".png"
}

// metafilesToPNG converts the metafiles at media's indexes, EMF and WMF, to PNG: it renders each
// at scale through the LibreOffice converter, in one soffice run in dir, whose profile starts
// with metafileRegistry, so the page each is rendered on is A4 at scale times 96 dpi, and trims
// each page to its picture through magick, writing the PNG into the media directory beside its
// stored file. soffice names each PNG for its input's stem, so two stored files that share a
// stem, the same bytes under two extensions, are converted once and handed on as the same PNG.
// soffice exits zero having written nothing for a picture it can't export, so a PNG that isn't
// there is an error too, as is a trim that fails.
func (c *Converter) metafilesToPNG(ctx context.Context, dir string, scale int, media []MediaFile, indexes []int) error {
	var inputs []string
	seen := map[string]bool{}
	for _, i := range indexes {
		if stem := pngName(media[i].Name); !seen[stem] {
			seen[stem] = true
			inputs = append(inputs, media[i].File)
		}
	}
	outdir, remove, msg, err := c.soffice(ctx, dir, metafileFilter(scale), metafileRegistry, inputs)
	if err != nil {
		return err
	}
	trimmed := map[string]string{}
	for _, i := range indexes {
		m := &media[i]
		name := pngName(m.Name)
		dest, ok := trimmed[name]
		if !ok {
			page := filepath.Join(outdir, name)
			if err := checkPNG(page); err != nil {
				return errors.Join(sofficeError(fmt.Errorf("%s: %w", m.Name, err), msg), remove())
			}
			dest = filepath.Join(filepath.Dir(m.File), name)
			// -trim crops the page to the picture, the bounding box of what differs from the
			// corners' color, and +repage drops the crop's offset from the PNG; PNG in and out,
			// nothing is encoded lossily.
			args := slices.Concat([]string{"png:" + page, "-trim", "+repage"}, reproducible, []string{"png:" + dest})
			if err := c.runMagick(ctx, dir, m.Name, args, dest); err != nil {
				return errors.Join(err, remove())
			}
			trimmed[name] = dest
		}
		m.File, m.Converted = dest, true
	}
	if err := remove(); err != nil {
		return fmt.Errorf("office: removing the libreoffice directory: %w", err)
	}
	return nil
}

// reproducible is the define every magick run that writes a PNG gives before its output: it
// leaves out the date:create, date:modify, and date:timestamp text chunks and the tIME chunk,
// which magick otherwise writes with the time of the run, so the same picture converted twice
// gives the same PNG, byte for byte.
var reproducible = []string{"-define", "png:exclude-chunks=date,time"}

// magickOutputLimit is the most of magick's output a conversion keeps, for its error.
const magickOutputLimit = 64 << 10

// tiffToPNG converts m's stored TIFF to PNG through magick, in dir, writing the PNG into the
// media directory beside it, and hands the PNG on. It reads the stored file as TIFF whatever its
// content and converts its first image alone, the one an Office application displays, so a
// multi-image TIFF gives one PNG, written without the run's time, so it is reproducible. An error carries magick's own message, its first line.
func (c *Converter) tiffToPNG(ctx context.Context, dir string, m *MediaFile) error {
	png := filepath.Join(filepath.Dir(m.File), pngName(m.Name))
	args := slices.Concat([]string{"tiff:" + m.File + "[0]"}, reproducible, []string{"png:" + png})
	if err := c.runMagick(ctx, dir, m.Name, args, png); err != nil {
		return err
	}
	m.File, m.Converted = png, true
	return nil
}

// runMagick runs magick once with args, in dir, to write the PNG png for the picture stored as
// name, and checks that it wrote one. An error names the picture and carries magick's own
// message, its first line.
func (c *Converter) runMagick(ctx context.Context, dir, name string, args []string, png string) error {
	out := &limitedBuffer{limit: magickOutputLimit}
	err := c.magick.Run(ctx, tool.Invocation{
		Args:   args,
		Dir:    dir,
		Stdout: out,
		Stderr: out,
	})
	if err == nil {
		err = checkPNG(png)
	}
	if err != nil {
		err = fmt.Errorf("office: magick: %s: %w", name, err)
		if msg := firstLine(out.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// errNotWritten is the cause of a converter's error when it wrote no output where it should
// have.
var errNotWritten = errors.New("no PNG written")

// checkPNG reports whether path is a regular file that begins with PNG's signature: an error
// wrapping errNotWritten when nothing is there.
func checkPNG(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errNotWritten
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len(pngSignature))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, pngSignature) {
		return fmt.Errorf("%s isn't a PNG", filepath.Base(path))
	}
	return nil
}

// firstLine returns the first line of out that isn't blank, trimmed.
func firstLine(out string) string {
	for line := range strings.Lines(out) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// rewriteLinks returns md, a Markdown result, with each converted picture's stored name, as the
// media list gives it, swapped for its PNG's, wherever it appears; it parses no Markdown. A
// picture handed on as stored, or with no file handed onward, keeps the name office wrote. The
// longer of two names that begin alike, such as a stored .tif and .tiff of the same bytes, is
// swapped first, and a swapped-in name is never swapped again.
func rewriteLinks(md string, media []MediaFile) string {
	var swaps [][2]string
	for _, m := range media {
		if m.Converted {
			swaps = append(swaps, [2]string{m.Name, filepath.Base(m.File)})
		}
	}
	if len(swaps) == 0 {
		return md
	}
	slices.SortStableFunc(swaps, func(a, b [2]string) int { return cmp.Compare(len(b[0]), len(a[0])) })
	var pairs []string
	for _, s := range swaps {
		pairs = append(pairs, s[0], s[1])
	}
	return strings.NewReplacer(pairs...).Replace(md)
}
