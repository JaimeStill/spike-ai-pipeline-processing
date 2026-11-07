//go:build linux

package dispatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strconv"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// QPDF is what a [DocumentStage] reads a PDF's structure through before rendering it:
// document's QPDF, which asks qpdf whether the PDF needs a password and reads its AcroForm.
type QPDF interface {
	RequiresPassword(ctx context.Context, pdf string) (bool, error)
	Form(ctx context.Context, pdf string) (document.Form, error)
}

// PDFDetach is what a [DocumentStage] reads a PDF's embedded files through: document's
// Detacher, which lists them and saves one to a writer through pdfdetach.
type PDFDetach interface {
	List(ctx context.Context, pdf string) ([]document.Attachment, error)
	Save(ctx context.Context, pdf string, number int, w io.Writer) error
}

// DocumentConfig is what a [DocumentStage] is built over: the caller's Preparer, the version of
// the tools behind it, the request each PDF's pages are prepared under, and, optionally, qpdf
// and pdfdetach.
type DocumentConfig struct {
	// Preparer prepares the pages; it is required.
	Preparer *document.Preparer
	// Version names the tools behind the stage, its renderer's, its corrections stage's, and
	// qpdf's and pdfdetach's when given, as [Versions] gives them; it is required.
	Version string
	// Request is the request every PDF's pages are prepared under: its DPI, corrections,
	// concurrency, text, and region. Its Dir is ignored: a part's pages are prepared in the
	// part's own directory. Every field but Concurrency, which changes no page, is part of the
	// stage's identity.
	Request document.ImagesRequest
	// QPDF, when not nil, checks each PDF for a password before it is rendered and reads its
	// AcroForm; nil renders every PDF, as the stage did before it had qpdf. Whether it is given
	// is part of the stage's identity.
	QPDF QPDF
	// PDFDetach, when not nil, saves each PDF's embedded files as parts; nil finds none. Whether
	// it is given, and with it the bomb thresholds, is part of the stage's identity.
	PDFDetach PDFDetach
	// Inspect is the dispatcher's Config.Inspect: its BombRatio and BombSize, inspect's defaults
	// when zero, are the thresholds an embedded file's saved bytes may not pass both of, the
	// ratio against the whole PDF's size.
	Inspect inspect.Options
}

// DocumentStage prepares a PDF, route.Document's parts, through document's Preparer: each page's
// text layer, when the request reads it, as a [TextOutput], and its image as an [ImageOutput], a
// PNG in the part's directory, each named "page N" with the page's number and the PDF's page
// count in its Meta. A page whose text layer holds no text has no text output, and a page a
// SkipText request didn't render no image. A page that fails doesn't stop the others: the stage
// ends with an error naming every page that failed, once the rest are prepared. An encrypted PDF
// the renderer can't read is [Encrypted] by its flags, as the dispatcher reports any stage's
// failure on content flagged /Encrypt.
//
// Given qpdf, the stage first asks it whether the PDF needs a password to open: one that does
// ends with [ErrEncrypted] at once, nothing rendered, so its outcome is Encrypted; one encrypted
// but restricted only by its permissions, which opens with no password, is prepared as any other.
// It then reads the PDF's AcroForm, and a PDF whose AcroForm has fields yields, before anything
// else, a [RecordOutput] named "forms" holding a document.Form, each field's full name, type, and
// value, a checkbox's or radio group's state and a choice's options and selection among them, its
// Meta's "fields" their count. A PDF with no AcroForm yields none; an XFA form is read only for
// the AcroForm fields beside it, if any, inspect flagging its /XFA as before.
//
// Given pdfdetach, the stage then saves each of the PDF's embedded files, the name tree's and the
// file attachment annotations', not the media behind a Screen or Rendition annotation: each is
// written into the part's directory under its content's SHA-256, never its stored name, streamed
// from pdfdetach's standard output while it is hashed, and is a [Found] part, the dispatcher's to
// inspect and route again, named by its stored name and found "attachment <name>". A file whose
// saved bytes pass both bomb thresholds, its ratio against the whole PDF's size, is stopped and
// its partial file removed, and is a record named by its place, as the archive stage records an
// entry, its Meta's check "pdf-attachment-bomb", its class "unsafe", and its outcome
// "quarantined"; a file pdfdetach can't save is a record of why.
//
// The pages come last. A failure of qpdf or pdfdetach, other than a password, doesn't stop the
// pages: the stage ends with an error naming it once they are prepared, as it does a failed page.
// A PDF qpdf can't open has no forms record.
type DocumentStage struct {
	preparer *document.Preparer
	request  document.ImagesRequest
	qpdf     QPDF
	detach   PDFDetach
	ratio    float64
	size     uint64
	id       Identity
}

// NewDocumentStage builds a DocumentStage over cfg, refusing one with no Preparer or Version, and
// bomb thresholds the archive stage would refuse.
func NewDocumentStage(cfg DocumentConfig) (*DocumentStage, error) {
	if cfg.Preparer == nil {
		return nil, errors.New("dispatch: document stage: no preparer")
	}
	ratio, size, err := bombRule("document stage", cfg.Inspect)
	if err != nil {
		return nil, err
	}
	req := cfg.Request
	corrections := make([]string, len(req.Corrections))
	for i, c := range req.Corrections {
		corrections[i] = c.String()
	}
	// The thresholds weigh only what pdfdetach saves, so they are hashed only with it; with
	// neither tool, the configuration hashes as it did before either was given.
	type attachments struct {
		BombRatio float64 `json:"bomb_ratio"`
		BombSize  uint64  `json:"bomb_size"`
	}
	var detach *attachments
	if cfg.PDFDetach != nil {
		detach = &attachments{ratio, size}
	}
	id, err := adapterIdentity("document", cfg.Version, struct {
		DPI         int              `json:"dpi"`
		Corrections []string         `json:"corrections"`
		Text        bool             `json:"text"`
		SkipText    bool             `json:"skip_text"`
		Region      *document.Region `json:"region"`
		QPDF        bool             `json:"qpdf,omitzero"`
		Attachments *attachments     `json:"attachments,omitzero"`
	}{cmp.Or(req.DPI, 150), corrections, req.Text, req.SkipText, req.Region, cfg.QPDF != nil, detach})
	if err != nil {
		return nil, err
	}
	return &DocumentStage{
		preparer: cfg.Preparer, request: req, qpdf: cfg.QPDF, detach: cfg.PDFDetach,
		ratio: ratio, size: size, id: id,
	}, nil
}

// Identity returns the stage's identity: "document", its tools' version, and its request's hash.
func (s *DocumentStage) Identity() Identity { return s.id }

// Prepare prepares the PDF at p.Path, as the type's doc says.
func (s *DocumentStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		var failed []error
		if s.qpdf != nil {
			locked, err := s.qpdf.RequiresPassword(ctx, p.Path)
			switch {
			case ctx.Err() != nil:
				yield(Item{}, ctx.Err())
				return
			case locked:
				yield(Item{}, fmt.Errorf("document stage: the PDF needs a password: %w", ErrEncrypted))
				return
			case err != nil:
				failed = append(failed, err)
			default:
				ok, err := s.forms(ctx, p, yield)
				if !ok {
					return
				}
				if err != nil {
					failed = append(failed, err)
				}
			}
		}
		if s.detach != nil {
			ok, err := s.attachments(ctx, p, dir, yield)
			if !ok {
				return
			}
			if err != nil {
				failed = append(failed, err)
			}
		}
		s.pages(ctx, p, dir, failed, yield)
	}
}

// forms yields the forms record of the PDF p, when its AcroForm has fields. It returns false when
// the loop stopped or ctx ended, the stage's error yielded, and qpdf's failure, which doesn't stop
// the stage.
func (s *DocumentStage) forms(ctx context.Context, p Part, yield func(Item, error) bool) (bool, error) {
	form, err := s.qpdf.Form(ctx, p.Path)
	switch {
	case ctx.Err() != nil:
		yield(Item{}, ctx.Err())
		return false, nil
	case err != nil:
		return true, err
	case len(form.Fields) == 0:
		return true, nil
	}
	o, err := record("forms", form, map[string]string{"fields": strconv.Itoa(len(form.Fields))})
	if err != nil {
		yield(Item{}, err)
		return false, nil
	}
	return yield(OutputItem(o), nil), nil
}

// attachments saves each embedded file of the PDF p into dir and yields its item: its part, or
// its record. It returns false when the loop stopped or the stage failed, its error yielded, and
// pdfdetach's failure to list the files, which doesn't stop the stage.
func (s *DocumentStage) attachments(ctx context.Context, p Part, dir string, yield func(Item, error) bool) (bool, error) {
	list, err := s.detach.List(ctx, p.Path)
	switch {
	case ctx.Err() != nil:
		yield(Item{}, ctx.Err())
		return false, nil
	case err != nil:
		return true, err
	}
	for _, a := range list {
		item, err := s.attachment(ctx, p, a, dir)
		if err != nil {
			yield(Item{}, err)
			return false, nil
		}
		if !yield(item, nil) {
			return false, nil
		}
	}
	return true, nil
}

// savedFile is what an embedded file the stage wrote no part for is recorded as: its number and
// stored name, the bytes written before its saving stopped, and why, with the inspect check, its
// class, and its evidence, when a check names it.
type savedFile struct {
	Number   int    `json:"number"`
	Name     string `json:"name"`
	Written  uint64 `json:"written,omitzero"`
	Reason   string `json:"reason"`
	Check    string `json:"check,omitzero"`
	Class    string `json:"class,omitzero"`
	Evidence string `json:"evidence,omitzero"`
}

// attachment returns the item of embedded file a of the PDF p: its part, written into dir by its
// SHA-256, or its record. An error is the stage's own, the directory unwritable or ctx ending; a
// file's content never fails it.
func (s *DocumentStage) attachment(ctx context.Context, p Part, a document.Attachment, dir string) (Item, error) {
	where := "attachment " + a.Name
	whole := uint64(p.Size)
	path, written, why, err := writeBySum(ctx, "document stage", s.ratio, s.size, func(w io.Writer) error {
		return s.detach.Save(ctx, p.Path, a.Number, w)
	}, dir, whole, func(_ *bombWriter, saveErr error) error {
		if saveErr != nil {
			return fmt.Errorf("doesn't save: %w", saveErr)
		}
		return nil
	})
	rec := savedFile{Number: a.Number, Name: a.Name, Written: written}
	switch {
	case err != nil:
		return Item{}, err
	case errors.Is(why, errBomb):
		rec.Reason, rec.Check, rec.Class = "past the bomb thresholds as written", string(inspect.PDFAttachmentBomb), string(inspect.Unsafe)
		rec.Evidence = fmt.Sprintf("attachment %q: %d bytes written from a %d-byte PDF, %.0f:1; past %d bytes and %g:1",
			a.Name, written, whole, float64(written)/float64(max(whole, 1)), s.size, s.ratio)
	case why != nil:
		rec.Reason = why.Error()
	default:
		return PartItem(Found{Path: path, Name: a.Name, Where: where}), nil
	}
	return unwritten(where, rec, a.Name, rec.Reason, rec.Check, rec.Class, rec.Evidence)
}

// pages prepares the PDF p's pages into dir and yields each one's outputs, ending with an error
// naming every page that failed and each of failed, the stage's earlier failures, when any.
func (s *DocumentStage) pages(ctx context.Context, p Part, dir string, failed []error, yield func(Item, error) bool) {
	f, err := os.Open(p.Path)
	if err != nil {
		yield(Item{}, fmt.Errorf("document stage: %w", err))
		return
	}
	defer func() { _ = f.Close() }()
	req := s.request
	req.Dir = dir
	reads := req.Text || req.SkipText
	var pages []error
	count := 0
	for page, err := range s.preparer.Images(ctx, f, req) {
		if err != nil && page.Number == 0 {
			// The request, the spool, or the count failed, or ctx ended: no page follows.
			yield(Item{}, errors.Join(append([]error{err}, failed...)...))
			return
		}
		count = page.Count
		if err != nil {
			pages = append(pages, err)
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
	if len(pages) > 0 {
		failed = append(failed, fmt.Errorf("document stage: %d of %d pages failed: %w", len(pages), count, errors.Join(pages...)))
	}
	if len(failed) > 0 {
		yield(Item{}, errors.Join(failed...))
	}
}

// pageMeta returns a page's outputs' Meta: its number and the PDF's page count.
func pageMeta(page document.PageImage) map[string]string {
	return map[string]string{"page": strconv.Itoa(page.Number), "count": strconv.Itoa(page.Count)}
}
