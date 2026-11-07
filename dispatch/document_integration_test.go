//go:build linux && integration

package dispatch_test

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// edgePDF returns the path of one of ~/docs' edge-case PDFs, skipping the test on a machine
// without them.
func edgePDF(t *testing.T, name string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	path := filepath.Join(home, "docs", "pdf", "edge", name)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no ~/docs/pdf/edge/%s on this machine", name)
	}
	return path
}

// outputNames returns ev's outputs as their kind and name, in order.
func outputNames(ev dispatched) []string {
	var names []string
	for _, o := range ev.Outputs {
		names = append(names, string(o.Kind)+" "+o.Name)
	}
	return names
}

// TestDocumentStageEncryptedReal: of ~/docs' encrypted PDFs, through this machine's qpdf, the
// three that need a password are Encrypted with nothing rendered; the two restricted only by
// their permissions render their page as any PDF does, as does the unicode baseline, which isn't
// encrypted. inspect flags /Encrypt on the five encrypted, as before.
func TestDocumentStageEncryptedReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	for name, c := range map[string]struct {
		outcome dispatch.Outcome
		flagged bool
	}{
		"encryption-openpassword.pdf":       {dispatch.Encrypted, true},
		"simple-open-password.pdf":          {dispatch.Encrypted, true},
		"unicode-password-corrigendum5.pdf": {dispatch.Encrypted, true},
		"encryption-nocopy.pdf":             {dispatch.Prepared, true},
		"encryption-notextaccess.pdf":       {dispatch.Prepared, true},
		"unicode-password-baseline.pdf":     {dispatch.Prepared, false},
	} {
		ev := dispatchAll(t, d, edgePDF(t, filepath.Join("encrypted", name)), nil)[0]
		flagged := slices.ContainsFunc(ev.Part.Flags, func(f inspect.Flag) bool { return f.Check == inspect.PDFEncrypt })
		if ev.Outcome != c.outcome || flagged != c.flagged {
			t.Errorf("%s: %s, %q, /Encrypt %t; want %s, %t", name, ev.Outcome, ev.Reason, flagged, c.outcome, c.flagged)
		}
		switch {
		case c.outcome == dispatch.Encrypted && (len(ev.Outputs) != 0 || !strings.Contains(ev.Reason, "needs a password")):
			t.Errorf("%s: %d outputs, %q; want none, a password", name, len(ev.Outputs), ev.Reason)
		case c.outcome == dispatch.Prepared && !slices.Contains(outputNames(ev), "image page 1"):
			t.Errorf("%s: outputs %q, want page 1's image", name, outputNames(ev))
		}
	}
}

// TestDocumentStageFormsReal: the generated acroform.pdf yields its forms record first, its
// fields the generator's exactly, then its page; ~/docs' two single-field forms yield their one
// field each; and the clean PDF, with no AcroForm, yields none.
func TestDocumentStageFormsReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	ev := dispatchAll(t, d, filepath.Join("..", "document", "testdata", "forms", "acroform.pdf"), nil)[0]
	if got, want := outputNames(ev), []string{"record forms", "text page 1", "image page 1"}; ev.Outcome != dispatch.Prepared || !slices.Equal(got, want) {
		t.Fatalf("acroform.pdf: %s, %q; want %q", ev.Outcome, got, want)
	}
	want := `{"fields":[` +
		`{"name":"applicant.name","type":"text","value":"Ada Lovelace"},` +
		`{"name":"applicant.city","type":"text","value":"Zürich"},` +
		`{"name":"subscribe","type":"checkbox","value":"Yes","checked":true},` +
		`{"name":"newsletter","type":"checkbox","value":"Off","checked":false},` +
		`{"name":"contact","type":"radio","value":"Phone","checked":true},` +
		`{"name":"colour","type":"choice","value":"Green","options":["Red","Green","Blue"],"selected":["Green"]},` +
		`{"name":"languages","type":"choice","options":["English","French","German"],"selected":["en","de"]}]}`
	if rec := ev.Outputs[0]; string(rec.Value) != want || rec.Meta["fields"] != "7" {
		t.Errorf("forms record %s, %v;\nwant %s", rec.Value, rec.Meta, want)
	}

	for name, want := range map[string]string{
		"forms/isartor-interactive-form.pdf":     `{"fields":[{"name":"field1","type":"text","value":"field"}]}`,
		"forms/pdfa2b-interactive-form-pass.pdf": `{"fields":[{"name":"Button","type":"pushbutton"}]}`,
	} {
		ev := dispatchAll(t, d, edgePDF(t, name), nil)[0]
		if ev.Outcome != dispatch.Prepared || len(ev.Outputs) == 0 || ev.Outputs[0].Name != "forms" || string(ev.Outputs[0].Value) != want {
			t.Errorf("%s: %s, %q", name, ev.Outcome, outputNames(ev))
		} else {
			t.Logf("%s: %s", name, ev.Outputs[0].Value)
		}
	}

	ev = dispatchAll(t, d, filepath.Join("..", "document", "testdata", "clean.pdf"), nil)[0]
	if slices.Contains(outputNames(ev), "record forms") {
		t.Error("clean.pdf, with no AcroForm, has a forms record")
	}
}

// TestDocumentStageAttachmentsReal: ~/docs' name tree attachment, KSBASE.WQ2, and file attachment
// annotation, PF.WK1, are each found "attachment <name>", written by their SHA-256, inspected and
// routed again; the Screen annotation's video in embedded-video-avi.pdf isn't extracted, its page
// rendered alone.
func TestDocumentStageAttachmentsReal(t *testing.T) {
	d, work := newDispatcherIn(t, allStages(t))
	for name, c := range map[string]struct{ file, sum string }{
		"file-attachment.pdf":            {"KSBASE.WQ2", "2c2c99377ac5a575149328845abf417e895a9bca36c03eb051c5ea309a092aa0"},
		"file-attachment-annotation.pdf": {"PF.WK1", "0a181a4e7cc1b8f93f6dc8549a544789526d84949a22dbdbf56a346b1c765424"},
	} {
		events := dispatchAll(t, d, edgePDF(t, filepath.Join("attachments", name)), func(ev *dispatch.Event) {
			if ev.Output == nil && ev.Part.Depth == 1 && filepath.Base(ev.Part.Path) != ev.Part.SHA256 {
				t.Errorf("%s at %s: want it named by its SHA-256", ev.Part.Name, ev.Part.Path)
			}
		})
		if len(events) != 2 {
			t.Errorf("%s: %d events, want the attachment's and the PDF's", name, len(events))
			continue
		}
		att, pdf := events[0], events[1]
		if att.Part.Name != c.file || att.Part.Where != "attachment "+c.file || att.Part.SHA256 != c.sum || att.Part.Depth != 1 {
			t.Errorf("%s: found %s %q at depth %d, SHA-256 %s", name, att.Part.Name, att.Part.Where, att.Part.Depth, att.Part.SHA256)
		}
		if pdf.Outcome != dispatch.Prepared || pdf.Children != 1 || !slices.Contains(outputNames(pdf), "image page 1") {
			t.Errorf("%s: %s, %d children, %q", name, pdf.Outcome, pdf.Children, outputNames(pdf))
		}
		t.Logf("%s: %s, %s by %s, %s", name, att.Part.Name, att.Part.Kind, att.Part.Route, att.Outcome)
	}
	events := dispatchAll(t, d, edgePDF(t, "attachments/embedded-video-avi.pdf"), nil)
	if len(events) != 1 || events[0].Outcome != dispatch.Prepared || events[0].Children != 0 {
		t.Errorf("embedded-video-avi.pdf: %v", steps(events))
	}
	wantEmpty(t, work)
}

// newDispatcherIn returns a dispatcher over stages and the working directory it makes parts' in.
func newDispatcherIn(t *testing.T, stages dispatch.Stages) (*dispatch.Dispatcher, string) {
	t.Helper()
	work := t.TempDir()
	d, err := dispatch.New(dispatch.Config{Stages: stages, Index: dispatch.NewMemoryIndex(), Dir: work})
	if err != nil {
		t.Fatal(err)
	}
	return d, work
}

// attachedPDF returns a one-page PDF whose name tree embeds name holding content, FlateDecode
// compressed.
func attachedPDF(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R /Names << /EmbeddedFiles << /Names [(" + name + ") 4 0 R] >> >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		"<< /Type /Filespec /F (" + name + ") /UF (" + name + ") /EF << /F 5 0 R >> >>",
		fmt.Sprintf("<< /Type /EmbeddedFile /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.Bytes()),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

// TestDocumentStageAttachmentBombReal: a PDF of about 8 KiB embedding 8 MiB of zeros, through this
// machine's pdfdetach, is saved whole under the default thresholds, about 1,000:1 short of both;
// under a 1 MiB size and a 100:1 ratio, pdfdetach is stopped past them, the file is a record, its
// check pdf-attachment-bomb and its outcome quarantined, and nothing of it is left.
func TestDocumentStageAttachmentBombReal(t *testing.T) {
	gs := pinned(t, document.GhostscriptSpec())
	render, err := document.NewGhostscript(gs)
	if err != nil {
		t.Fatal(err)
	}
	prep, err := document.NewPreparer(render, nil)
	if err != nil {
		t.Fatal(err)
	}
	pdf := input(t, "zeros.pdf", string(attachedPDF(t, "zeros.bin", make([]byte, 8<<20))))

	d, work := newDispatcherIn(t, dispatch.Stages{route.Document: pdfStage(t, prep, gs, inspect.Options{})})
	events := dispatchAll(t, d, pdf, nil)
	if len(events) != 2 || events[0].Part.Name != "zeros.bin" || events[0].Part.Size != 8<<20 || events[0].Part.Where != "attachment zeros.bin" {
		t.Errorf("default thresholds: %v", steps(events))
	}
	wantEmpty(t, work)

	d, work = newDispatcherIn(t, dispatch.Stages{route.Document: pdfStage(t, prep, gs, inspect.Options{BombSize: 1 << 20, BombRatio: 100})})
	events = dispatchAll(t, d, pdf, nil)
	wantSteps(t, events, []step{{"zeros.pdf", 0, dispatch.Prepared}})
	rec := outputNamed(t, events[0], dispatch.RecordOutput, "attachment zeros.bin")
	var saved struct {
		Written uint64 `json:"written"`
	}
	if err := json.Unmarshal(rec.Value, &saved); err != nil || saved.Written <= 1<<20 || saved.Written >= 8<<20 {
		t.Errorf("written %d, %v: want past 1 MiB and short of 8 MiB", saved.Written, err)
	}
	if rec.Meta["check"] != string(inspect.PDFAttachmentBomb) || rec.Meta["outcome"] != string(dispatch.Quarantined) {
		t.Errorf("zeros.bin's record: %v", rec.Meta)
	}
	t.Logf("zeros.bin: %s", rec.Meta["evidence"])
	wantEmpty(t, work)
}
