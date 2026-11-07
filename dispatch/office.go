//go:build linux

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// OfficeConfig is what an [OfficeStage] is built over: the caller's Converter, the version of
// the tools behind it, and the scale metafiles are rendered at.
type OfficeConfig struct {
	// Converter converts the documents; it is required.
	Converter *office.Converter
	// Version names the tools behind the Converter, office's, LibreOffice's, and magick's, as
	// [Versions] gives them; it is required.
	Version string
	// MetafileScale is the scale a document's EMF and WMF pictures are rendered at, as
	// office.Request's; part of the stage's identity.
	MetafileScale int
}

// OfficeStage prepares an office document, route.Office's parts, through office's Converter:
// office's Markdown as a [MarkdownOutput] named "document", its Meta naming the format a legacy
// document or an RTF was converted from, "rtf" for an RTF, and office's warnings, a line each; and each file office
// wrote in its media directory as a [Found] part, the dispatcher's to route and inspect again:
// a picture converted to PNG as its PNG, any other as stored, a picture whose converter the
// Converter wasn't given among them, so its own stage may convert it. A part's Where names the
// medium by office's name for it, "media <SHA-256>.<ext>", and ", as PNG" for a converted one.
//
// An RTF is office's to convert through LibreOffice to docx, as a .doc is. The stage has no
// processor yet for an ODF package, a WordPerfect document, or an Excel binary workbook, which
// LibreOffice would convert, nor for a legacy document or an RTF when the Converter has no
// LibreOffice converter: each ends with [ErrNoProcessor], its reason naming the kind. office's
// refusal of an encrypted package ends it with [ErrEncrypted].
//
// It asks for the Markdown alone, not office's JSON model too, which would run office twice
// over each document: the Markdown is what a model reads, and the model's structure is office's
// to give when a caller needs it.
type OfficeStage struct {
	converter *office.Converter
	scale     int
	id        Identity
}

// NewOfficeStage builds an OfficeStage over cfg, refusing one with no Converter or Version.
func NewOfficeStage(cfg OfficeConfig) (*OfficeStage, error) {
	if cfg.Converter == nil {
		return nil, errors.New("dispatch: office stage: no converter")
	}
	id, err := adapterIdentity("office", cfg.Version, struct {
		Format        string `json:"format"`
		MetafileScale int    `json:"metafile_scale"`
	}{office.Markdown.String(), cfg.MetafileScale})
	if err != nil {
		return nil, err
	}
	return &OfficeStage{converter: cfg.Converter, scale: cfg.MetafileScale, id: id}, nil
}

// Identity returns the stage's identity: "office", its tools' version, and its options' hash.
func (s *OfficeStage) Identity() Identity { return s.id }

// unread names the office kinds the stage has no processor for yet, each with its article.
var unread = map[route.Kind]string{
	route.ODF:         "an ODF package",
	route.WordPerfect: "a WordPerfect document",
}

// Prepare converts the document at p.Path, as the type's doc says.
func (s *OfficeStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		what, ok := unread[p.Kind]
		if !ok && p.Kind == route.ZIP && strings.EqualFold(filepath.Ext(p.Name), ".xlsb") {
			what, ok = "an Excel binary workbook", true
		}
		if ok {
			yield(Item{}, fmt.Errorf("office stage: %w for %s", ErrNoProcessor, what))
			return
		}
		f, err := os.Open(p.Path)
		if err != nil {
			yield(Item{}, fmt.Errorf("office stage: %w", err))
			return
		}
		defer func() { _ = f.Close() }()
		res, err := s.converter.Convert(ctx, f, office.Request{
			Dir: dir, Format: office.Markdown, Name: p.Name, MetafileScale: s.scale,
		})
		if err != nil {
			yield(Item{}, officeError(err))
			return
		}
		meta := map[string]string{}
		if res.ConvertedFrom != "" {
			meta["converted_from"] = res.ConvertedFrom
		}
		if len(res.Warnings) > 0 {
			meta["warnings"] = strings.Join(res.Warnings, "\n")
		}
		if !yield(OutputItem(markdown("document", res.Markdown, meta)), nil) {
			return
		}
		// Two stored files of the same bytes under two extensions share one PNG: it is found once.
		found := map[string]bool{}
		for _, m := range res.Media {
			path, mime, where := m.File, m.ContentType, "media "+m.Name
			switch {
			case path == "":
				path = filepath.Join(res.MediaDir, m.Name)
			case m.Converted:
				mime, where = "image/png", where+", as PNG"
			}
			if found[path] {
				continue
			}
			found[path] = true
			if !yield(PartItem(Found{Path: path, Name: filepath.Base(path), MIME: mime, Where: where}), nil) {
				return
			}
		}
	}
}

// officeError returns err, a conversion's, as the stage's: a legacy document or an RTF without a
// LibreOffice converter matches ErrNoProcessor, and office's refusal of an encrypted package
// ErrEncrypted.
func officeError(err error) error {
	var exit *office.ExitError
	switch {
	case errors.Is(err, office.ErrNoLibreOffice):
		return fmt.Errorf("office stage: %w: %w", ErrNoProcessor, err)
	case errors.As(err, &exit) && errors.Is(err, office.ErrRefused) && strings.Contains(exit.Message, "encrypted"):
		return fmt.Errorf("office stage: %w: %w", ErrEncrypted, err)
	}
	return fmt.Errorf("office stage: %w", err)
}
