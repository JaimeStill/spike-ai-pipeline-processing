//go:build linux

package office

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// pictureKinds are the kinds the picture package converts a document's picture as, by its stored
// extension, lowercased: a metafile, EMF or WMF, rendered through LibreOffice and trimmed
// through ImageMagick, and a TIFF through ImageMagick. Every other kind is handed on as stored,
// an SVG among them, since an Office application stores a PNG beside the SVG it shows.
var pictureKinds = map[string]route.Kind{
	".emf":  route.EMF,
	".wmf":  route.WMF,
	".tif":  route.TIFF,
	".tiff": route.TIFF,
}

// convertPictures converts the pictures in media, the media list of a conversion whose media
// directory is in dir, the working directory, to PNG, as the picture standard says, through the
// picture package's ConvertBatch, in one batch, metafiles rendered at scale: each EMF and WMF
// rendered through LibreOffice, all of them in one soffice run, and trimmed to the picture
// through magick, and each TIFF through magick. A converted picture's PNG is named by the stored
// name's stem, its SHA-256, and .png, and written into the media directory beside the stored
// file, which stays; its entry's File becomes the PNG and Converted true. A picture whose
// converter the Converter wasn't given, for a metafile either of LibreOffice and magick, is left
// with no File. Every other picture keeps its stored file. Two stored files that share a stem,
// the same bytes under two extensions, are converted once and handed on as the same PNG. A
// conversion that fails is an error, and media is then only partly converted.
func (c *Converter) convertPictures(ctx context.Context, dir string, scale int, media []MediaFile) error {
	var batch []picture.Picture
	converted := map[string][]int{}
	for i := range media {
		kind, ok := pictureKinds[strings.ToLower(filepath.Ext(media[i].Name))]
		switch {
		case !ok:
			continue
		case c.magick == nil, kind != route.TIFF && c.libreoffice == nil:
			media[i].File = ""
			continue
		}
		png := filepath.Join(filepath.Dir(media[i].File), pngName(media[i].Name))
		if _, ok := converted[png]; !ok {
			batch = append(batch, picture.Picture{Path: media[i].File, Kind: kind, PNG: png})
		}
		converted[png] = append(converted[png], i)
	}
	if err := c.pictures.ConvertBatch(ctx, picture.Batch{Dir: dir, MetafileScale: scale, Pictures: batch}); err != nil {
		return fmt.Errorf("office: %w", err)
	}
	for png, indexes := range converted {
		for _, i := range indexes {
			media[i].File, media[i].Converted = png, true
		}
	}
	return nil
}

// pngName returns the name of a converted picture's PNG: the stored name's stem, its SHA-256,
// and .png.
func pngName(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name)) + ".png"
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
