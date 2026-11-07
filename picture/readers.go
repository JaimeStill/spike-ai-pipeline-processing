//go:build linux

package picture

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// ErrNoReader is the cause of the error a conversion returns for a HEIC, WebP, or AVIF when the
// Converter's magick has no reader for it: its format listing, read when the Converter was built,
// doesn't mark the format readable, as when ImageMagick was built without libheif or libwebp, or
// libheif without an AV1 decoder. Nothing runs for it.
var ErrNoReader = errors.New("no magick reader")

// rasterFormats are the kinds magick reads through a coder that a build may leave out, each
// with the format's name in magick's listing, which also names its coder in an argument.
var rasterFormats = map[route.Kind]string{
	route.HEIC: "HEIC",
	route.WebP: "WEBP",
	route.AVIF: "AVIF",
}

// raster reports whether kind is read through one of rasterFormats' coders: a HEIC, WebP, or
// AVIF.
func raster(kind route.Kind) bool {
	_, ok := rasterFormats[kind]
	return ok
}

// listFormatArgs are magick's arguments for its format listing, which marks each format's
// coder's modes: r when it reads the format, w when it writes it.
var listFormatArgs = []string{"-list", "format"}

// listFormatLimit is the most of magick's format listing a Converter reads: 1 MiB, some fifty
// times what ImageMagick 7.1.2 prints.
const listFormatLimit = 1 << 20

// formatLine matches a line of magick's format listing: the format's name, an asterisk when its
// coder reads from a blob, its module, and its mode, such as "     WEBP* WEBP      rw+   WebP
// Image Format (libwebp 1.6.0 [0210])".
var formatLine = regexp.MustCompile(`^\s*([A-Z0-9-]+)\*?\s+\S+\s+([r-])[w-][+-](?:\s|$)`)

// listReaders runs magick's format listing and returns the kinds of rasterFormats whose format
// it marks readable. A listing that fails is an error naming magick's message.
func listReaders(ctx context.Context, magick *tool.Tool) (map[route.Kind]bool, error) {
	out := &cli.LimitedBuffer{Limit: listFormatLimit}
	var stderr strings.Builder
	if err := magick.Run(ctx, tool.Invocation{Args: slices.Clone(listFormatArgs), Stdout: out, Stderr: &stderr}); err != nil {
		err = fmt.Errorf("picture: magick's format listing: %w", err)
		if msg := firstLine(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return parseReaders(out.String()), nil
}

// parseReaders returns the kinds of rasterFormats that listing, magick's format listing, marks
// readable.
func parseReaders(listing string) map[route.Kind]bool {
	readable := map[string]bool{}
	for line := range strings.Lines(listing) {
		if m := formatLine.FindStringSubmatch(line); m != nil && m[2] == "r" {
			readable[m[1]] = true
		}
	}
	readers := map[route.Kind]bool{}
	for kind, format := range rasterFormats {
		if readable[format] {
			readers[kind] = true
		}
	}
	return readers
}

// rasterArgs are magick's arguments for s, a HEIC, WebP, or AVIF: its first image alone, read
// through the coder its kind names whatever its content, then turned upright by its stored
// orientation. The first image of a HEIC is its primary image, which magick's libheif coder reads
// before any other top-level image, and never a thumbnail, which isn't one; of an animated WebP or
// AVIF it is the first frame, whole. libheif applies a HEIC's or AVIF's own rotation and mirror as
// it decodes, leaving the image's orientation top-left, and -auto-orient applies a WebP's EXIF
// orientation.
func rasterArgs(s staged) []string {
	coder := strings.ToLower(rasterFormats[s.Kind])
	return slices.Concat([]string{coder + ":" + s.copied + "[0]", "-auto-orient"}, reproducible, []string{"png:" + s.png})
}
