//go:build linux

package dispatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// fakeQPDF is a dispatch.QPDF that answers as it is told: whether the PDF needs a password, or
// lockErr, and its form, or formErr; it counts the forms it is asked for.
type fakeQPDF struct {
	locked  bool
	lockErr error
	form    document.Form
	formErr error
	forms   atomic.Int32
}

func (q *fakeQPDF) RequiresPassword(context.Context, string) (bool, error) {
	return q.locked, q.lockErr
}

func (q *fakeQPDF) Form(context.Context, string) (document.Form, error) {
	q.forms.Add(1)
	return q.form, q.formErr
}

// fakeDetach is a dispatch.PDFDetach over files, each attachment's stored name and content, in
// order, numbered from 1; a content of "fail" fails its save, and listErr fails the listing.
type fakeDetach struct {
	files   []string
	listErr error
}

func (d *fakeDetach) List(context.Context, string) ([]document.Attachment, error) {
	if d.listErr != nil {
		return nil, d.listErr
	}
	var list []document.Attachment
	for i := 0; i < len(d.files); i += 2 {
		list = append(list, document.Attachment{Number: i/2 + 1, Name: d.files[i]})
	}
	return list, nil
}

func (d *fakeDetach) Save(_ context.Context, _ string, number int, w io.Writer) error {
	content := d.files[(number-1)*2+1]
	if content == "fail" {
		return errors.New("told to fail")
	}
	// Written in 32 KiB pieces, as a pipe delivers them, so a refused write stops the save.
	_, err := io.Copy(w, struct{ io.Reader }{strings.NewReader(content)})
	return err
}

// formsDocumentStage returns a document stage over three pages of their own, reading their text,
// given qpdf and pdfdetach as q and d, either nil, and the bomb thresholds of opts.
func formsDocumentStage(t *testing.T, q dispatch.QPDF, d dispatch.PDFDetach, opts inspect.Options) *dispatch.DocumentStage {
	t.Helper()
	prep, err := document.NewPreparer(&pages{n: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dispatch.DocumentConfig{
		Preparer: prep, Version: "pages 1", Request: document.ImagesRequest{Text: true}, Inspect: opts,
	}
	// A nil interface, not a nil pointer in one, is no tool.
	if q != nil {
		cfg.QPDF = q
	}
	if d != nil {
		cfg.PDFDetach = d
	}
	s, err := dispatch.NewDocumentStage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// documentOrder dispatches pdf through s and returns the dispatch's events, each with its
// outputs, and every event's place in order: a part's output by its name, a found part by its
// Where.
func documentOrder(t *testing.T, s *dispatch.DocumentStage, pdf string) ([]dispatched, []string) {
	t.Helper()
	d := newDispatcher(t, dispatch.Stages{route.Document: s}, dispatch.NewMemoryIndex())
	var order []string
	events := dispatchAll(t, d, pdf, func(ev *dispatch.Event) {
		switch {
		case ev.Output != nil && ev.Part.Depth == 0:
			order = append(order, string(ev.Output.Kind)+" "+ev.Output.Name)
		case ev.Output == nil && ev.Part.Depth > 0:
			order = append(order, "part "+ev.Part.Where)
		}
	})
	return events, order
}

// pagesOrder are the outputs of formsDocumentStage's three pages, in order.
var pagesOrder = []string{"text page 1", "image page 1", "text page 2", "image page 2", "text page 3", "image page 3"}

// TestDocumentStagePassword: a PDF qpdf says needs a password is Encrypted, its reason saying so,
// with no output and no form read; one encrypted only by its permissions, which opens without a
// password, has its pages prepared as any other.
func TestDocumentStagePassword(t *testing.T) {
	pdf := input(t, "locked.pdf", "%PDF-1.7\n%%EOF\n")
	q := &fakeQPDF{locked: true}
	events, _ := documentOrder(t, formsDocumentStage(t, q, &fakeDetach{files: []string{"a.txt", "a"}}, inspect.Options{}), pdf)
	wantSteps(t, events, []step{{"locked.pdf", 0, dispatch.Encrypted}})
	if ev := events[0]; len(ev.Outputs) != 0 || !strings.Contains(ev.Reason, "needs a password") || !errors.Is(ev.Err, dispatch.ErrEncrypted) {
		t.Errorf("locked.pdf: %d outputs, %q, %v", len(ev.Outputs), ev.Reason, ev.Err)
	}
	if n := q.forms.Load(); n != 0 {
		t.Errorf("%d forms read of a PDF that needs a password", n)
	}

	_, order := documentOrder(t, formsDocumentStage(t, &fakeQPDF{}, nil, inspect.Options{}), input(t, "permissions.pdf", "%PDF-1.7\n%%EOF\n"))
	if !slices.Equal(order, pagesOrder) {
		t.Errorf("permissions only:\n got %q\nwant %q", order, pagesOrder)
	}
}

// TestDocumentStageForms: a PDF whose AcroForm has fields yields its forms record first, the
// fields as qpdf's form gives them, as JSON, and their count in its Meta, then its pages; a PDF
// whose form has no fields yields none; and qpdf failing to read the form, or to answer the
// password check, leaves the pages prepared and fails the part once they are, naming qpdf's
// failure, with no record.
func TestDocumentStageForms(t *testing.T) {
	form := document.Form{Fields: []document.FormField{
		{Name: "applicant.name", Type: document.TextField, Value: "Ada Lovelace"},
		{Name: "subscribe", Type: document.CheckboxField, Value: "Yes", Checked: new(true)},
		{
			Name: "colour", Type: document.ChoiceField, Value: "Green",
			Options: []string{"Red", "Green"}, Selected: []string{"Green"},
		},
	}}
	pdf := input(t, "form.pdf", "%PDF-1.7\n%%EOF\n")
	events, order := documentOrder(t, formsDocumentStage(t, &fakeQPDF{form: form}, nil, inspect.Options{}), pdf)
	wantSteps(t, events, []step{{"form.pdf", 0, dispatch.Prepared}})
	if want := append([]string{"record forms"}, pagesOrder...); !slices.Equal(order, want) {
		t.Errorf("order:\n got %q\nwant %q", order, want)
	}
	rec := events[0].Outputs[0]
	want := `{"fields":[` +
		`{"name":"applicant.name","type":"text","value":"Ada Lovelace"},` +
		`{"name":"subscribe","type":"checkbox","value":"Yes","checked":true},` +
		`{"name":"colour","type":"choice","value":"Green","options":["Red","Green"],"selected":["Green"]}]}`
	if string(rec.Value) != want || rec.MediaType != "application/json" || rec.Meta["fields"] != "3" {
		t.Errorf("forms record %s, %s, %v;\nwant %s", rec.Value, rec.MediaType, rec.Meta, want)
	}
	var back document.Form
	if err := json.Unmarshal(rec.Value, &back); err != nil || len(back.Fields) != 3 {
		t.Errorf("the record doesn't read back as a Form: %v", err)
	}

	_, order = documentOrder(t, formsDocumentStage(t, &fakeQPDF{form: document.Form{Fields: []document.FormField{}}}, nil, inspect.Options{}), pdf)
	if !slices.Equal(order, pagesOrder) {
		t.Errorf("no fields:\n got %q\nwant %q", order, pagesOrder)
	}

	for name, q := range map[string]*fakeQPDF{
		"form":     {form: form, formErr: errors.New("qpdf: told to fail the form")},
		"password": {form: form, lockErr: errors.New("qpdf: told to fail the password check")},
	} {
		events, order := documentOrder(t, formsDocumentStage(t, q, nil, inspect.Options{}), pdf)
		if ev := events[0]; ev.Outcome != dispatch.Failed || !strings.Contains(ev.Reason, "told to fail the "+name) {
			t.Errorf("%s failing: %s, %q", name, ev.Outcome, ev.Reason)
		}
		if !slices.Equal(order, pagesOrder) {
			t.Errorf("%s failing:\n got %q\nwant %q", name, order, pagesOrder)
		}
	}
}

// TestDocumentStageAttachments: each embedded file is a part written under its SHA-256 inside
// the working directory, never its stored name, a traversing one among them, named by its stored
// name, found "attachment <name>", and routed by its content, after the forms record and before
// the pages; a file that doesn't save is a record of why; and nothing is left once the dispatch
// is done.
func TestDocumentStageAttachments(t *testing.T) {
	png := pngOf(t)
	detach := &fakeDetach{files: []string{
		"KSBASE.WQ2", "a Quattro Pro file's bytes\n",
		"../../evil.png", png,
		"broken.bin", "fail",
	}}
	q := &fakeQPDF{form: document.Form{Fields: []document.FormField{{Name: "f", Type: document.TextField, Value: "v"}}}}
	prep, err := document.NewPreparer(&pages{n: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{
		Preparer: prep, Version: "pages 1", Request: document.ImagesRequest{Text: true}, QPDF: q, PDFDetach: detach,
	})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	d, err := dispatch.New(dispatch.Config{
		Stages: dispatch.Stages{route.Document: s, route.Vision: dispatch.NewVisionStage()}, Index: dispatch.NewMemoryIndex(), Dir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	events := dispatchAll(t, d, input(t, "attached.pdf", "%PDF-1.7\n%%EOF\n"), func(ev *dispatch.Event) {
		switch {
		case ev.Output != nil && ev.Part.Depth == 0:
			order = append(order, string(ev.Output.Kind)+" "+ev.Output.Name)
		case ev.Output == nil && ev.Part.Depth > 0:
			order = append(order, "part "+ev.Part.Where)
			if filepath.Base(ev.Part.Path) != ev.Part.SHA256 || !inside(work, ev.Part.Path) {
				t.Errorf("%s at %s: want its SHA-256 inside the working directory", ev.Part.Name, ev.Part.Path)
			}
		}
	})
	wantSteps(t, events, []step{
		{"KSBASE.WQ2", 1, dispatch.NoProcessor},
		{"../../evil.png", 1, dispatch.Prepared},
		{"attached.pdf", 0, dispatch.Prepared},
	})
	want := append([]string{
		"record forms", "part attachment KSBASE.WQ2", "part attachment ../../evil.png", "record attachment broken.bin",
	}, pagesOrder...)
	if !slices.Equal(order, want) {
		t.Errorf("order:\n got %q\nwant %q", order, want)
	}
	if ev := events[0]; ev.Part.SHA256 != sum("a Quattro Pro file's bytes\n") {
		t.Errorf("KSBASE.WQ2: SHA-256 %s", ev.Part.SHA256)
	}
	if ev := events[1]; ev.Part.Route != route.Vision || ev.Part.SHA256 != sum(png) {
		t.Errorf("../../evil.png: %s, %s", ev.Part.Route, ev.Part.SHA256)
	}
	rec := outputNamed(t, events[2], dispatch.RecordOutput, "attachment broken.bin")
	if rec.Meta["name"] != "broken.bin" || !strings.Contains(rec.Meta["reason"], "doesn't save: told to fail") {
		t.Errorf("broken.bin's record: %v", rec.Meta)
	}
	wantEmpty(t, work)

	detach.listErr = errors.New("pdfdetach: told to fail the listing")
	events, order = documentOrder(t, formsDocumentStage(t, nil, detach, inspect.Options{}), input(t, "unlisted.pdf", "%PDF-1.7\n%%EOF\n"))
	if ev := events[0]; ev.Outcome != dispatch.Failed || !strings.Contains(ev.Reason, "told to fail the listing") || !slices.Equal(order, pagesOrder) {
		t.Errorf("a failed listing: %s, %q, %q", ev.Outcome, ev.Reason, order)
	}
}

// pngOf returns a 1x1 PNG, as the pages renderer draws page 1.
func pngOf(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := (&pages{n: 1}).Render(t.Context(), "", 1, 72, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestDocumentStageAttachmentBomb: an embedded file of 8 MiB of zeros from a PDF of a few bytes
// passes the ratio at once; under a 1 MiB bomb size, its saving stops just past it, its partial
// file is removed, and it is a record named by its place, its check pdf-attachment-bomb, unsafe,
// its outcome quarantined, with the bytes written as evidence; the PDF's pages are prepared.
func TestDocumentStageAttachmentBomb(t *testing.T) {
	zeros := string(make([]byte, 8<<20))
	s := formsDocumentStage(t, nil, &fakeDetach{files: []string{"zeros.bin", zeros}}, inspect.Options{BombSize: 1 << 20})
	work := t.TempDir()
	d, err := dispatch.New(dispatch.Config{Stages: dispatch.Stages{route.Document: s}, Index: dispatch.NewMemoryIndex(), Dir: work})
	if err != nil {
		t.Fatal(err)
	}
	events := dispatchAll(t, d, input(t, "bomb.pdf", "%PDF-1.7\n%%EOF\n"), nil)
	wantSteps(t, events, []step{{"bomb.pdf", 0, dispatch.Prepared}})
	rec := outputNamed(t, events[0], dispatch.RecordOutput, "attachment zeros.bin")
	if rec.Meta["check"] != string(inspect.PDFAttachmentBomb) || rec.Meta["class"] != string(inspect.Unsafe) ||
		rec.Meta["outcome"] != string(dispatch.Quarantined) || !strings.Contains(rec.Meta["evidence"], "from a 15-byte PDF") {
		t.Errorf("zeros.bin's record: %v", rec.Meta)
	}
	var saved struct {
		Written uint64 `json:"written"`
	}
	if err := json.Unmarshal(rec.Value, &saved); err != nil || saved.Written <= 1<<20 || saved.Written >= 8<<20 {
		t.Errorf("written %d, %v: want past 1 MiB and short of 8 MiB", saved.Written, err)
	}
	if len(events[0].Outputs) != 7 {
		t.Errorf("%d outputs, want the record and six pages'", len(events[0].Outputs))
	}
	wantEmpty(t, work)
}

// TestDocumentIdentity: with neither qpdf nor pdfdetach, the stage's identity is the one it had
// before it had either; qpdf changes it, and pdfdetach, with its bomb thresholds, which change
// nothing without it.
func TestDocumentIdentity(t *testing.T) {
	prep, err := document.NewPreparer(&pages{n: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := func(cfg dispatch.DocumentConfig) string {
		t.Helper()
		cfg.Preparer, cfg.Version = prep, "gs 10.5"
		s, err := dispatch.NewDocumentStage(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return s.Identity().Config
	}
	// The configuration's hash as the stage had it before qpdf and pdfdetach: adapterVersion's
	// "4", and the default request's fields.
	type request struct {
		DPI         int              `json:"dpi"`
		Corrections []string         `json:"corrections"`
		Text        bool             `json:"text"`
		SkipText    bool             `json:"skip_text"`
		Region      *document.Region `json:"region"`
	}
	before, err := dispatch.HashConfig(struct {
		Adapter string  `json:"adapter"`
		Config  request `json:"config"`
	}{"4", request{DPI: 150, Corrections: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	base := id(dispatch.DocumentConfig{})
	if base != before {
		t.Errorf("no qpdf or pdfdetach: config %s, want %s, as before either", base, before)
	}
	if id(dispatch.DocumentConfig{Inspect: inspect.Options{BombSize: 1}}) != base {
		t.Error("bomb thresholds changed the identity of a stage with no pdfdetach")
	}
	withQPDF := id(dispatch.DocumentConfig{QPDF: &fakeQPDF{}})
	withDetach := id(dispatch.DocumentConfig{PDFDetach: &fakeDetach{}})
	lowered := id(dispatch.DocumentConfig{PDFDetach: &fakeDetach{}, Inspect: inspect.Options{BombSize: 1}})
	all := []string{base, withQPDF, withDetach, lowered}
	slices.Sort(all)
	if len(slices.Compact(all)) != 4 {
		t.Errorf("configs %s, %s, %s, %s: want four", base, withQPDF, withDetach, lowered)
	}
	if _, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{Preparer: prep, Version: "x", Inspect: inspect.Options{BombRatio: -1}}); err == nil {
		t.Error("a negative bomb ratio was taken")
	}
}
