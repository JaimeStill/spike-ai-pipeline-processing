//go:build linux

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strconv"

	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// PictureConfig is what a [PictureStage] is built over: the caller's Converter, the version of
// the tools behind it, and the scale metafiles are rendered at.
type PictureConfig struct {
	// Converter converts the pictures; it is required.
	Converter *picture.Converter
	// Version names the tools behind the Converter, magick's and LibreOffice's, as [Versions]
	// gives them; it is required.
	Version string
	// MetafileScale is the scale an EMF or WMF is rendered at, as picture.Request's; part of the
	// stage's identity.
	MetafileScale int
}

// PictureStage prepares a picture a vision model decodes only once converted, route.
// VisionConverted's parts, through picture's Converter, to PNGs in the part's directory: a TIFF
// to one [ImageOutput] per page, in page order, named "page N", its Meta naming the page and the
// TIFF's page count; an EMF, WMF, SVG, HEIC, WebP, or AVIF to one ImageOutput named "picture",
// a HEIC's primary image, a WebP's or AVIF's first frame. Every output's Meta names the kind it
// was converted from. It has no processor yet for a picture whose converter the Converter wasn't
// given, magick for any, LibreOffice too for a metafile, nor for a HEIC, WebP, or AVIF whose
// reader the Converter's magick lacks: each ends with [ErrNoProcessor], its reason naming why.
type PictureStage struct {
	converter *picture.Converter
	scale     int
	id        Identity
}

// NewPictureStage builds a PictureStage over cfg, refusing one with no Converter or Version.
func NewPictureStage(cfg PictureConfig) (*PictureStage, error) {
	if cfg.Converter == nil {
		return nil, errors.New("dispatch: picture stage: no converter")
	}
	id, err := adapterIdentity("picture", cfg.Version, struct {
		MetafileScale int `json:"metafile_scale"`
	}{cfg.MetafileScale})
	if err != nil {
		return nil, err
	}
	return &PictureStage{converter: cfg.Converter, scale: cfg.MetafileScale, id: id}, nil
}

// Identity returns the stage's identity: "picture", its tools' version, and its options' hash.
func (s *PictureStage) Identity() Identity { return s.id }

// Prepare converts the picture at p.Path, as the type's doc says.
func (s *PictureStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		pngs, err := s.converter.ConvertPages(ctx, picture.Request{Dir: dir, Path: p.Path, MetafileScale: s.scale})
		switch {
		case errors.Is(err, picture.ErrNoMagick), errors.Is(err, picture.ErrNoLibreOffice), errors.Is(err, picture.ErrNoReader):
			yield(Item{}, fmt.Errorf("picture stage: %w: %w", ErrNoProcessor, err))
			return
		case err != nil:
			yield(Item{}, fmt.Errorf("picture stage: %w", err))
			return
		}
		for i, png := range pngs {
			o := Output{
				Kind: ImageOutput, Name: "picture", MediaType: "image/png", Path: png,
				Meta: map[string]string{"from": string(p.Kind)},
			}
			if p.Kind == route.TIFF {
				// A TIFF yields its every page, as the document stage yields a PDF's.
				o.Name = "page " + strconv.Itoa(i+1)
				o.Meta["page"], o.Meta["count"] = strconv.Itoa(i+1), strconv.Itoa(len(pngs))
			}
			if !yield(OutputItem(o), nil) {
				return
			}
		}
	}
}
