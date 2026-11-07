//go:build linux

package picture

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// DefaultMetafileScale is the scale an EMF or WMF is rendered at when a request leaves its
// MetafileScale zero: 3, three times its size at 96 dpi.
const DefaultMetafileScale = 3

// MaxMetafileScale is the largest MetafileScale a request may ask for: 8, at which the page each
// metafile is rendered on is 6352x8984 pixels, some 57 million, before the trim.
const MaxMetafileScale = 8

// SVGDensity is the density an SVG is rendered at, in dots per inch: 150, the DPI a PDF's pages
// are rendered at for vision. An SVG's CSS pixel is a 96th of an inch, so an SVG 400 pixels wide
// is 625 pixels wide as PNG.
const SVGDensity = 150

// ErrScale is the cause of a refused MetafileScale: below zero, or above MaxMetafileScale.
var ErrScale = errors.New("metafile scale out of range")

// CheckMetafileScale returns the scale a request's MetafileScale asks for, DefaultMetafileScale
// when it is zero, or an error wrapping ErrScale when it is below zero or above MaxMetafileScale.
// A caller that validates a request before anything runs, as office's Convert does, calls it.
func CheckMetafileScale(scale int) (int, error) {
	s := cmp.Or(scale, DefaultMetafileScale)
	if s < 1 || s > MaxMetafileScale {
		return 0, fmt.Errorf("picture: metafile scale %d isn't from 1 to %d: %w", scale, MaxMetafileScale, ErrScale)
	}
	return s, nil
}

var (
	// ErrNoMagick is the cause of the error a conversion returns for any picture when the
	// Converter has no magick Tool. Nothing runs for it.
	ErrNoMagick = errors.New("no magick converter")
	// ErrNoLibreOffice is the cause of the error a conversion returns for an EMF or WMF when the
	// Converter has no LibreOffice Tool. Nothing runs for it.
	ErrNoLibreOffice = errors.New("no LibreOffice converter")
)

// Config is what a Converter is built over: the Tools the caller built. Either may be nil, which
// leaves the Converter without it, and a picture that needs it is then refused.
type Config struct {
	// Magick is ImageMagick's magick, typically built from MagickSpec. It converts TIFF, SVG,
	// HEIC, WebP, and AVIF to PNG and trims an EMF's or WMF's rendered page to its picture.
	Magick *tool.Tool
	// LibreOffice is LibreOffice's soffice, typically built from LibreOfficeSpec through the
	// container runner in LibreOfficeImage. It renders EMF and WMF to PNG, with Magick.
	LibreOffice *tool.Tool
}

// Converter converts pictures to PNG, the one form a vision model decodes them in: TIFF, EMF,
// WMF, SVG, HEIC, WebP, and AVIF. Its methods are safe for concurrent use.
type Converter struct {
	magick      *tool.Tool
	libreoffice *tool.Tool
	// readers are the kinds of HEIC, WebP, and AVIF that magick's format listing marked readable
	// when the Converter was built.
	readers map[route.Kind]bool
}

// NewConverter builds a Converter over cfg's Tools. It refuses a Magick older than
// MagickMinimum and a LibreOffice older than LibreOfficeMinimum, whatever minimum the Tool's own
// Spec declared, with a *tool.VersionError wrapping tool.ErrTooOld. Given a Magick, it runs
// magick's format listing, magick -list format, once, under ctx, and keeps which of HEIC, WebP,
// and AVIF it reads: a kind it doesn't read is refused with ErrNoReader. A listing that fails is
// an error, and no Converter is built.
func NewConverter(ctx context.Context, cfg Config) (*Converter, error) {
	if cfg.Magick != nil {
		if err := atLeast(cfg.Magick, MagickMinimum); err != nil {
			return nil, err
		}
	}
	if cfg.LibreOffice != nil {
		if err := atLeast(cfg.LibreOffice, LibreOfficeMinimum); err != nil {
			return nil, err
		}
	}
	c := &Converter{magick: cfg.Magick, libreoffice: cfg.LibreOffice}
	if cfg.Magick != nil {
		readers, err := listReaders(ctx, cfg.Magick)
		if err != nil {
			return nil, err
		}
		c.readers = readers
	}
	return c, nil
}

// Request is one picture's conversion to PNG, such as an attachment's.
type Request struct {
	// Dir is the working directory, which the caller owns: the picture is copied into a directory
	// of its own made in it, picture-*, which is removed when the conversion ends, and the PNG is
	// written into it and left. It is required; an empty Dir is refused rather than taken as the
	// system's temporary directory.
	Dir string
	// Path is the picture's file, which is read and never changed. Its kind, TIFF, EMF, WMF, SVG,
	// HEIC, WebP, or AVIF, is told by its content's signature, as route.Identify tells it, never by
	// its name, which may carry any extension or none.
	Path string
	// MetafileScale is the scale an EMF or WMF is rendered at, a multiple of its size at 96 dpi:
	// zero is DefaultMetafileScale, 3, and it may be at most MaxMetafileScale, 8. Every other
	// kind ignores it.
	MetafileScale int
}

// kinds are the picture kinds the package converts, with each one's extension, which a copy is
// stored under, and its name with its article, for an error.
var kinds = map[route.Kind]struct{ ext, name string }{
	route.TIFF: {".tiff", "a TIFF"},
	route.EMF:  {".emf", "an EMF"},
	route.WMF:  {".wmf", "a WMF"},
	route.SVG:  {".svg", "an SVG"},
	route.HEIC: {".heic", "a HEIC"},
	route.WebP: {".webp", "a WebP"},
	route.AVIF: {".avif", "an AVIF"},
}

// metafile reports whether kind is rendered through LibreOffice: an EMF or a WMF.
func metafile(kind route.Kind) bool { return kind == route.EMF || kind == route.WMF }

// Convert converts the picture at req.Path, a TIFF, EMF, WMF, SVG, HEIC, WebP, or AVIF told by
// its content's signature, to PNG, through ConvertBatch, as a batch of one: a TIFF's first image
// through magick; an EMF or WMF rendered at req.MetafileScale through soffice and trimmed to its
// picture through magick; an SVG rendered at SVGDensity through magick's librsvg and flattened
// onto white; a HEIC's primary image, or a WebP's or AVIF's first frame, through magick, turned
// upright by its stored orientation. It returns the PNG's absolute path: req.Dir's file named by
// the lowercase hex SHA-256 of the picture's bytes and .png, which an earlier conversion's PNG of
// the same picture is replaced by. A picture mail named by its SHA-256 thus gives the PNG of its
// own stem. ConvertPages converts a TIFF's every page instead.
//
// Content of no other kind is an error before anything runs, as is a picture whose converter
// the Converter wasn't given: an EMF or WMF without LibreOffice matches ErrNoLibreOffice, any
// picture without magick ErrNoMagick, and a HEIC, WebP, or AVIF whose reader magick lacks
// ErrNoReader. A MetafileScale below zero or above MaxMetafileScale is an error matching
// ErrScale. A conversion that fails is ConvertBatch's error, and leaves no PNG.
func (c *Converter) Convert(ctx context.Context, req Request) (string, error) {
	r, err := c.request(ctx, req)
	if err != nil {
		return "", err
	}
	return c.convertOne(ctx, r)
}

// ConvertPages converts the picture at req.Path as Convert does, but a TIFF to one PNG for each
// of its pages, its images in the order the file holds them, in one magick run: the PNGs are
// req.Dir's files named by the TIFF's SHA-256, "-page", the page's number from 1, and .png, so
// no page's PNG shares a stem with another's or with a picture's PNG Convert names. It returns
// their absolute paths in page order, the first page's first; a picture of any other kind gives
// the one PNG Convert gives. Every page is written as PNG without the time of the run, as every
// conversion is, so a page converted twice is the same bytes. A run that fails, or leaves a gap
// in the pages, is an error naming the TIFF, and the pages it wrote are removed. Its refusals
// are Convert's.
func (c *Converter) ConvertPages(ctx context.Context, req Request) ([]string, error) {
	r, err := c.request(ctx, req)
	if err != nil {
		return nil, err
	}
	if r.kind != route.TIFF {
		png, err := c.convertOne(ctx, r)
		if err != nil {
			return nil, err
		}
		return []string{png}, nil
	}
	return c.tiffPages(ctx, r)
}

// request is a Request checked: its working directory, absolute, its metafile scale, and its
// picture's path, name, kind, and SHA-256.
type request struct {
	dir, path, name, sum string
	kind                 route.Kind
	scale                int
}

// request checks req, refusing a scale out of range, an ended ctx, an empty working directory,
// and a picture of no converted kind, and reads the picture's kind and SHA-256.
func (c *Converter) request(ctx context.Context, req Request) (request, error) {
	scale, err := CheckMetafileScale(req.MetafileScale)
	if err != nil {
		return request{}, err
	}
	if err := ctx.Err(); err != nil {
		return request{}, err
	}
	dir, err := workingDir(req.Dir)
	if err != nil {
		return request{}, err
	}
	kind, sum, err := identify(req.Path)
	if err != nil {
		return request{}, err
	}
	name := filepath.Base(req.Path)
	if _, ok := kinds[kind]; !ok {
		return request{}, fmt.Errorf("picture: %s isn't a TIFF, EMF, WMF, SVG, HEIC, WebP, or AVIF picture", name)
	}
	return request{dir: dir, path: req.Path, name: name, sum: sum, kind: kind, scale: scale}, nil
}

// convertOne converts r's picture to the PNG Convert names, through ConvertBatch.
func (c *Converter) convertOne(ctx context.Context, r request) (string, error) {
	png := filepath.Join(r.dir, r.sum+".png")
	err := c.ConvertBatch(ctx, Batch{
		Dir:           r.dir,
		MetafileScale: r.scale,
		Pictures:      []Picture{{Path: r.path, Kind: r.kind, PNG: png}},
	})
	if err != nil {
		return "", err
	}
	return png, nil
}

// workingDir returns dir, a request's working directory, as an absolute path, refusing an empty
// one.
func workingDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("picture: no working directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("picture: %w", err)
	}
	return abs, nil
}

// identify reads the picture at path: its kind, by its first route.HeadLen bytes' signature, and
// the lowercase hex SHA-256 of all its bytes.
func identify(path string) (route.Kind, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("picture: %w", err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, route.HeadLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", "", fmt.Errorf("picture: %w", err)
	}
	sum := sha256.New()
	sum.Write(head[:n])
	if _, err := io.Copy(sum, f); err != nil {
		return "", "", fmt.Errorf("picture: %w", err)
	}
	return route.Identify(head[:n]), hex.EncodeToString(sum.Sum(nil)), nil
}

// Batch is several pictures' conversion to PNG at once, such as an office document's: its EMFs
// and WMFs are rendered in one soffice run.
type Batch struct {
	// Dir is the working directory, which the caller owns and the runners mount: the pictures are
	// copied into a directory made in it, picture-*, which is removed when the batch ends. It is
	// required; an empty Dir is refused.
	Dir string
	// MetafileScale is the scale the batch's EMFs and WMFs are rendered at, as Request's.
	MetafileScale int
	// Pictures are the batch's pictures, converted in order, but for the EMFs and WMFs, which are
	// rendered together after the rest.
	Pictures []Picture
}

// Picture is one picture of a Batch.
type Picture struct {
	// Path is the picture's file, which is read and never changed.
	Path string
	// Kind is how the picture is read, whatever its content: route.TIFF, route.EMF, route.WMF,
	// route.SVG, route.HEIC, route.WebP, or route.AVIF. A picture of the wrong kind fails in its
	// converter.
	Kind route.Kind
	// PNG is the path its PNG is written to, replacing any file there. Its name's stem, the name
	// less its extension, names the picture's copy and, in an error, the picture; no two of a
	// batch's pictures may share it.
	PNG string
}

// ConvertBatch converts b's pictures to PNG, each in a directory of its own: each picture is
// copied, under its PNG's stem and its kind's extension, into a fresh directory made for it in a
// picture-* directory in b.Dir, which holds nothing else, so an SVG's relative references find
// nothing, and its converter reads the copy. A TIFF's first image is converted through magick, read
// as TIFF whatever its content, one run each. An SVG is rendered through magick's librsvg coder at
// SVGDensity and flattened onto white, one run each; librsvg runs no script and fetches nothing
// over a network, and reads no file the SVG names outside its directory, which holds the copy
// alone. A HEIC's primary image, or a WebP's or AVIF's first frame, is converted through magick,
// read through the coder its kind names whatever its content and turned upright by its stored
// orientation, one run each. Every EMF and WMF is rendered in one soffice run, whose profile gives
// LibreOffice a British English locale, so Draw places each on an A4 page at b.MetafileScale times
// 96 dpi, and each page is trimmed to its picture through magick, one run each. Each writes PNG, so
// nothing is encoded lossily, and without the time of the run, so a picture converted twice is the
// same bytes both times. Each PNG is written beside its copy and then moved to its Picture's PNG,
// so no PNG is written in part. The picture-* directory is removed when ConvertBatch returns,
// however it ends.
//
// A picture of no converted kind, a picture whose converter the Converter wasn't given, two
// pictures that share a PNG stem, an empty b.Dir, and a MetafileScale CheckMetafileScale refuses
// are each an error before anything runs: a missing LibreOffice matches ErrNoLibreOffice, a missing
// magick ErrNoMagick, and a HEIC, WebP, or AVIF whose reader magick lacks ErrNoReader. A conversion
// that fails, or writes no PNG, is an error naming the picture by its copy's name, with the
// converter's message, and the PNGs the batch wrote are removed. When ctx ends a run, the error
// matches ctx's, as every Tool run's does.
func (c *Converter) ConvertBatch(ctx context.Context, b Batch) error {
	scale, err := CheckMetafileScale(b.MetafileScale)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := workingDir(b.Dir)
	if err != nil {
		return err
	}
	stems := map[string]bool{}
	for _, p := range b.Pictures {
		name := filepath.Base(p.Path)
		k, ok := kinds[p.Kind]
		switch {
		case !ok:
			return fmt.Errorf("picture: %s: no converter for kind %q", name, p.Kind)
		case metafile(p.Kind) && c.libreoffice == nil:
			return fmt.Errorf("picture: %s is %s picture: %w", name, k.name, ErrNoLibreOffice)
		case c.magick == nil:
			return fmt.Errorf("picture: %s is %s picture: %w", name, k.name, ErrNoMagick)
		case raster(p.Kind) && !c.readers[p.Kind]:
			return fmt.Errorf("picture: %s is %s picture: %w", name, k.name, ErrNoReader)
		}
		stem := pngStem(p.PNG)
		if stems[stem] {
			return fmt.Errorf("picture: two pictures' PNGs are named %s", filepath.Base(p.PNG))
		}
		stems[stem] = true
	}
	if len(b.Pictures) == 0 {
		return nil
	}
	work, err := os.MkdirTemp(dir, "picture-*")
	if err != nil {
		return fmt.Errorf("picture: picture directory: %w", err)
	}
	var written []string
	err = c.convertBatch(ctx, dir, work, scale, b.Pictures, &written)
	if rerr := os.RemoveAll(work); rerr != nil {
		err = errors.Join(err, fmt.Errorf("picture: removing the picture directory: %w", rerr))
	}
	if err != nil {
		for _, png := range written {
			err = errors.Join(err, os.Remove(png))
		}
		return err
	}
	return nil
}

// pngStem returns the stem of png, a Picture's PNG: its name less its extension.
func pngStem(png string) string {
	base := filepath.Base(png)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// staged is one picture of a batch copied into its own directory: the copy, the name the copy
// goes by in an error, and the PNG written beside it before it is moved to the Picture's PNG.
type staged struct {
	Picture
	copied, name, png string
}

// convertBatch copies pictures into directories of their own in work, a directory in dir, the
// working directory, converts them as ConvertBatch says, metafiles rendered at scale, and moves
// each PNG to its Picture's PNG, appending each moved PNG to written.
func (c *Converter) convertBatch(ctx context.Context, dir, work string, scale int, pictures []Picture, written *[]string) error {
	var metafiles []staged
	for i, p := range pictures {
		s, err := stage(work, i, p)
		if err != nil {
			return err
		}
		if metafile(p.Kind) {
			metafiles = append(metafiles, s)
			continue
		}
		args := tiffArgs(s)
		switch {
		case p.Kind == route.SVG:
			args = svgArgs(s)
		case raster(p.Kind):
			args = rasterArgs(s)
		}
		if err := c.runMagick(ctx, dir, s.name, args, s.png); err != nil {
			return err
		}
		if err := place(s, written); err != nil {
			return err
		}
	}
	if len(metafiles) == 0 {
		return nil
	}
	return c.metafilesToPNG(ctx, dir, scale, metafiles, written)
}

// stage copies p, the batch's picture at index i, into a directory of its own in work, under its
// PNG's stem and its kind's extension.
func stage(work string, i int, p Picture) (staged, error) {
	own := filepath.Join(work, strconv.Itoa(i))
	if err := os.Mkdir(own, 0o700); err != nil {
		return staged{}, fmt.Errorf("picture: %w", err)
	}
	stem := pngStem(p.PNG)
	name := stem + kinds[p.Kind].ext
	s := staged{Picture: p, copied: filepath.Join(own, name), name: name, png: filepath.Join(own, stem+".png")}
	in, err := os.Open(p.Path)
	if err != nil {
		return staged{}, fmt.Errorf("picture: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(s.copied, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return staged{}, fmt.Errorf("picture: %w", err)
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return staged{}, fmt.Errorf("picture: %s: %w", name, err)
	}
	return s, nil
}

// place moves s's PNG, written beside its copy, to its Picture's PNG, and appends that to
// written.
func place(s staged, written *[]string) error {
	if err := os.Rename(s.png, s.PNG); err != nil {
		return fmt.Errorf("picture: %s: %w", s.name, err)
	}
	*written = append(*written, s.PNG)
	return nil
}

// reproducible is the define every magick run that writes a converted PNG gives before its
// output: it leaves out the date:create, date:modify, and date:timestamp text chunks and the
// tIME chunk, which magick otherwise writes with the time of the run, so the same picture
// converted twice gives the same PNG, byte for byte.
var reproducible = []string{"-define", "png:exclude-chunks=date,time"}

// tiffArgs are magick's arguments for s, a TIFF: its first image alone, the one an Office
// application displays, read as TIFF whatever its content, so a multi-image TIFF gives one PNG.
func tiffArgs(s staged) []string {
	return slices.Concat([]string{"tiff:" + s.copied + "[0]"}, reproducible, []string{"png:" + s.png})
}

// svgArgs are magick's arguments for s, an SVG: rendered at SVGDensity through the rsvg coder,
// librsvg, named so that magick never falls back to its own MSVG renderer or a delegate, then
// flattened onto white, its alpha removed, so a transparent SVG is dark on white as a page is.
func svgArgs(s staged) []string {
	return slices.Concat(
		[]string{"-density", strconv.Itoa(SVGDensity), "-background", "white", "rsvg:" + s.copied, "-alpha", "remove", "-alpha", "off"},
		reproducible, []string{"png:" + s.png})
}

// magickOutputLimit is the most of magick's output a conversion keeps, for its error.
const magickOutputLimit = 64 << 10

// runMagick runs magick once with args, in dir, to write the PNG png for the picture whose copy
// is named name, and checks that it wrote one. An error names the picture and carries magick's
// own message, its first line.
func (c *Converter) runMagick(ctx context.Context, dir, name string, args []string, png string) error {
	out := &cli.LimitedBuffer{Limit: magickOutputLimit}
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
		err = fmt.Errorf("picture: magick: %s: %w", name, err)
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

// checkPNG reports whether path is a regular file that begins with PNG's signature, as
// route.Identify tells it: an error wrapping errNotWritten when nothing is there.
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
	head := make([]byte, len(route.PNGSignature))
	if _, err := io.ReadFull(f, head); err != nil || route.Identify(head) != route.PNG {
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
