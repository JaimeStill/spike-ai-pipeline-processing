//go:build integration && linux

package document_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// hostQPDF returns a QPDF over this machine's qpdf.
func hostQPDF(t *testing.T) *document.QPDF {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), document.QPDFSpec())
	if err != nil {
		t.Fatal(err)
	}
	q, err := document.NewQPDF(tl)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// hostDetacher returns a Detacher over this machine's pdfdetach.
func hostDetacher(t *testing.T) *document.Detacher {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), document.PDFDetachSpec())
	if err != nil {
		t.Fatal(err)
	}
	d, err := document.NewDetacher(tl)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// edgePDFs returns ~/docs/pdf/edge, skipping the test on a machine without it.
func edgePDFs(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(home, "docs", "pdf", "edge")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		t.Skip("no ~/docs/pdf/edge on this machine")
	}
	return dir
}

// TestFormReal: this machine's qpdf reads the generated acroform.pdf as its generator's fields,
// exactly as the recorded JSON does; and ~/docs' two single-field forms as their one field each,
// a text field and a pushbutton.
func TestFormReal(t *testing.T) {
	q := hostQPDF(t)
	f, err := q.Form(t.Context(), formFixture("acroform.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Fields, acroformFields) {
		t.Errorf("acroform.pdf's fields\n got %+v\nwant %+v", f.Fields, acroformFields)
	}
	clean, err := q.Form(t.Context(), filepath.Join("testdata", "clean.pdf"))
	if err != nil || len(clean.Fields) != 0 {
		t.Errorf("clean.pdf, which has no AcroForm: %+v, %v", clean, err)
	}

	edge := edgePDFs(t)
	for name, want := range map[string][]document.FormField{
		"forms/isartor-interactive-form.pdf":     {{Name: "field1", Type: document.TextField, Value: "field"}},
		"forms/pdfa2b-interactive-form-pass.pdf": {{Name: "Button", Type: document.PushbuttonField}},
	} {
		f, err := q.Form(t.Context(), filepath.Join(edge, name))
		if err != nil || !reflect.DeepEqual(f.Fields, want) {
			t.Errorf("%s: %+v, %v; want %+v", name, f.Fields, err, want)
		}
	}
}

// TestRequiresPasswordReal: of ~/docs' encrypted PDFs, the three with an open password need one,
// the two restricted only by their permissions don't, and the unicode baseline, which isn't
// encrypted, doesn't.
func TestRequiresPasswordReal(t *testing.T) {
	q := hostQPDF(t)
	edge := edgePDFs(t)
	for name, want := range map[string]bool{
		"encryption-openpassword.pdf":       true,
		"simple-open-password.pdf":          true,
		"unicode-password-corrigendum5.pdf": true,
		"encryption-nocopy.pdf":             false,
		"encryption-notextaccess.pdf":       false,
		"unicode-password-baseline.pdf":     false,
	} {
		got, err := q.RequiresPassword(t.Context(), filepath.Join(edge, "encrypted", name))
		if err != nil || got != want {
			t.Errorf("%s: %t, %v; want %t", name, got, err, want)
		}
	}
	if _, err := q.RequiresPassword(t.Context(), filepath.Join(t.TempDir(), "missing.pdf")); err == nil {
		t.Error("a missing file was answered")
	}
}

// TestDetacherReal: this machine's pdfdetach lists ~/docs' name tree attachment, KSBASE.WQ2, and
// file attachment annotation, PF.WK1, and saves each whole; the Screen annotation's video in
// embedded-video-avi.pdf isn't listed.
func TestDetacherReal(t *testing.T) {
	d := hostDetacher(t)
	edge := edgePDFs(t)
	for name, want := range map[string]struct {
		file string
		sum  string
	}{
		"file-attachment.pdf":            {"KSBASE.WQ2", "2c2c99377ac5a575149328845abf417e895a9bca36c03eb051c5ea309a092aa0"},
		"file-attachment-annotation.pdf": {"PF.WK1", "0a181a4e7cc1b8f93f6dc8549a544789526d84949a22dbdbf56a346b1c765424"},
	} {
		pdf := filepath.Join(edge, "attachments", name)
		list, err := d.List(t.Context(), pdf)
		if err != nil || len(list) != 1 || list[0] != (document.Attachment{Number: 1, Name: want.file}) {
			t.Errorf("%s: %+v, %v; want %s", name, list, err, want.file)
			continue
		}
		var b bytes.Buffer
		if err := d.Save(t.Context(), pdf, 1, &b); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b.Bytes())
		if got := hex.EncodeToString(sum[:]); got != want.sum {
			t.Errorf("%s's %s: SHA-256 %s, want %s", name, want.file, got, want.sum)
		}
		if err := d.Save(t.Context(), pdf, 2, &b); !errors.Is(err, document.ErrNoAttachment) {
			t.Errorf("%s's second file: %v, want ErrNoAttachment", name, err)
		}
	}
	list, err := d.List(t.Context(), filepath.Join(edge, "attachments", "embedded-video-avi.pdf"))
	if err != nil || len(list) != 0 {
		t.Errorf("embedded-video-avi.pdf: %+v, %v; want none", list, err)
	}
}
