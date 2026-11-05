//go:build linux

package dispatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strconv"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// DocumentConfig is what a [DocumentStage] is built over: the caller's Preparer, the version of
// the tools behind it, and the request each PDF's pages are prepared under.
type DocumentConfig struct {
	// Preparer prepares the pages; it is required.
	Preparer *document.Preparer
	// Version names the tools behind the Preparer, its renderer's and its corrections stage's,
	// as [Versions] gives them; it is required.
	Version string
	// Request is the request every PDF's pages are prepared under: its DPI, corrections,
	// concurrency, text, and region. Its Dir is ignored: a part's pages are prepared in the
	// part's own directory. Every field but Concurrency, which changes no page, is part of the
	// stage's identity.
	Request document.ImagesRequest
}

// DocumentStage prepares a PDF, route.Document's parts, through document's Preparer: each page's
// text layer, when the request reads it, as a [TextOutput], and its image as an [ImageOutput], a
// PNG in the part's directory, each named "page N" with the page's number and the PDF's page
// count in its Meta. A page whose text layer holds no text has no text output, and a page a
// SkipText request didn't render no image. A page that fails doesn't stop the others: the stage
// ends with an error naming every page that failed, once the rest are prepared. An encrypted PDF
// the renderer can't read is [Encrypted] by its flags, as the dispatcher reports any stage's
// failure on content flagged /Encrypt.
type DocumentStage struct {
	preparer *document.Preparer
	request  document.ImagesRequest
	id       Identity
}

// NewDocumentStage builds a DocumentStage over cfg, refusing one with no Preparer or Version.
func NewDocumentStage(cfg DocumentConfig) (*DocumentStage, error) {
	if cfg.Preparer == nil {
		return nil, errors.New("dispatch: document stage: no preparer")
	}
	req := cfg.Request
	corrections := make([]string, len(req.Corrections))
	for i, c := range req.Corrections {
		corrections[i] = c.String()
	}
	id, err := adapterIdentity("document", cfg.Version, struct {
		DPI         int              `json:"dpi"`
		Corrections []string         `json:"corrections"`
		Text        bool             `json:"text"`
		SkipText    bool             `json:"skip_text"`
		Region      *document.Region `json:"region"`
	}{cmp.Or(req.DPI, 150), corrections, req.Text, req.SkipText, req.Region})
	if err != nil {
		return nil, err
	}
	return &DocumentStage{preparer: cfg.Preparer, request: req, id: id}, nil
}

// Identity returns the stage's identity: "document", its tools' version, and its request's hash.
func (s *DocumentStage) Identity() Identity { return s.id }

// Prepare prepares the PDF at p.Path's pages, as the type's doc says.
func (s *DocumentStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		f, err := os.Open(p.Path)
		if err != nil {
			yield(Item{}, fmt.Errorf("document stage: %w", err))
			return
		}
		defer func() { _ = f.Close() }()
		req := s.request
		req.Dir = dir
		reads := req.Text || req.SkipText
		var failed []error
		count := 0
		for page, err := range s.preparer.Images(ctx, f, req) {
			if err != nil && page.Number == 0 {
				// The request, the spool, or the count failed, or ctx ended: no page follows.
				yield(Item{}, err)
				return
			}
			count = page.Count
			if err != nil {
				failed = append(failed, err)
				continue
			}
			name := "page " + strconv.Itoa(page.Number)
			if reads && document.HasText(page.Text) {
				o := Output{Kind: TextOutput, Name: name, MediaType: "text/plain", Value: []byte(page.Text), Meta: pageMeta(page)}
				if !yield(OutputItem(o), nil) {
					return
				}
			}
			if page.PNG == nil {
				continue
			}
			path := filepath.Join(dir, fmt.Sprintf("page-%d.png", page.Number))
			if err := os.WriteFile(path, page.PNG, 0o600); err != nil {
				yield(Item{}, fmt.Errorf("document stage: %w", err))
				return
			}
			o := Output{Kind: ImageOutput, Name: name, MediaType: "image/png", Path: path, Meta: pageMeta(page)}
			if !yield(OutputItem(o), nil) {
				return
			}
		}
		if len(failed) > 0 {
			yield(Item{}, fmt.Errorf("document stage: %d of %d pages failed: %w", len(failed), count, errors.Join(failed...)))
		}
	}
}

// pageMeta returns a page's outputs' Meta: its number and the PDF's page count.
func pageMeta(page document.PageImage) map[string]string {
	return map[string]string{"page": strconv.Itoa(page.Number), "count": strconv.Itoa(page.Count)}
}
