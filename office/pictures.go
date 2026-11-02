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

// pictureConverter is the converter a picture kind takes to PNG. Its zero value is none: the
// picture is handed on as stored, as one vision decodes, PNG, JPEG, GIF, or BMP, is, and any kind
// the picture standard names no converter for.
type pictureConverter int

const (
	// viaLibreOffice is a metafile, EMF or WMF, converted to PNG through LibreOffice.
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

// pictureFilter is LibreOffice's PNG export filter for a picture it opens. Naming it, not only
// the extension, keeps a file soffice doesn't open as a picture, such as text named .emf, from
// being exported as a page of a text document; soffice fails on it instead.
const pictureFilter = "png:draw_png_Export"

// pngSignature begins every PNG file.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// convertPictures converts the pictures in media, the media list of a conversion whose media
// directory is in dir, the working directory, to PNG, as the picture standard says: each EMF and
// WMF through the LibreOffice converter, all of them in one soffice run, and each TIFF through
// magick, one run each. A converted picture's PNG is named by the stored name's stem, its
// SHA-256, and .png, and written into the media directory beside the stored file, which stays;
// its entry's File becomes the PNG and Converted true. A picture whose converter the Converter
// wasn't given is left with no File. Every other picture keeps its stored file. Two stored files
// that share a stem, the same bytes under two extensions, are converted once and handed on as the
// same PNG. A conversion that fails, or writes no PNG, is an error, and media is then only partly
// converted.
func (c *Converter) convertPictures(ctx context.Context, dir string, media []MediaFile) error {
	var metafiles []int
	tiffs := map[string]string{}
	for i := range media {
		switch pictureConverters[strings.ToLower(filepath.Ext(media[i].Name))] {
		case viaLibreOffice:
			if c.libreoffice == nil {
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
	return c.metafilesToPNG(ctx, dir, media, metafiles)
}

// pngName returns the name of a converted picture's PNG: the stored name's stem, its SHA-256,
// and .png.
func pngName(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name)) + ".png"
}

// metafilesToPNG converts the metafiles at media's indexes, EMF and WMF, to PNG through the
// LibreOffice converter, in one soffice run in dir, and moves each PNG into the media directory
// beside its stored file. soffice names each PNG for its input's stem, so two stored files that
// share a stem, the same bytes under two extensions, are converted once and handed on as the
// same PNG. soffice exits zero having written nothing for a picture it can't export, so a PNG
// that isn't there is an error too.
func (c *Converter) metafilesToPNG(ctx context.Context, dir string, media []MediaFile, indexes []int) error {
	var inputs []string
	seen := map[string]bool{}
	for _, i := range indexes {
		if stem := pngName(media[i].Name); !seen[stem] {
			seen[stem] = true
			inputs = append(inputs, media[i].File)
		}
	}
	outdir, remove, msg, err := c.soffice(ctx, dir, pictureFilter, inputs)
	if err != nil {
		return err
	}
	moved := map[string]string{}
	for _, i := range indexes {
		m := &media[i]
		name := pngName(m.Name)
		dest, ok := moved[name]
		if !ok {
			dest = filepath.Join(filepath.Dir(m.File), name)
			if err := checkPNG(filepath.Join(outdir, name)); err != nil {
				return errors.Join(sofficeError(fmt.Errorf("%s: %w", m.Name, err), msg), remove())
			}
			if err := os.Rename(filepath.Join(outdir, name), dest); err != nil {
				return errors.Join(fmt.Errorf("office: libreoffice: %w", err), remove())
			}
			moved[name] = dest
		}
		m.File, m.Converted = dest, true
	}
	if err := remove(); err != nil {
		return fmt.Errorf("office: removing the libreoffice directory: %w", err)
	}
	return nil
}

// magickOutputLimit is the most of magick's output a conversion keeps, for its error.
const magickOutputLimit = 64 << 10

// tiffToPNG converts m's stored TIFF to PNG through magick, in dir, writing the PNG into the
// media directory beside it, and hands the PNG on. It reads the stored file as TIFF whatever its
// content and converts its first image alone, the one an Office application displays, so a
// multi-image TIFF gives one PNG. An error carries magick's own message, its first line.
func (c *Converter) tiffToPNG(ctx context.Context, dir string, m *MediaFile) error {
	png := filepath.Join(filepath.Dir(m.File), pngName(m.Name))
	out := &limitedBuffer{limit: magickOutputLimit}
	err := c.magick.Run(ctx, tool.Invocation{
		Args:   []string{"tiff:" + m.File + "[0]", "png:" + png},
		Dir:    dir,
		Stdout: out,
		Stderr: out,
	})
	if err == nil {
		err = checkPNG(png)
	}
	if err != nil {
		err = fmt.Errorf("office: magick: %s: %w", m.Name, err)
		if msg := firstLine(out.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	m.File, m.Converted = png, true
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
