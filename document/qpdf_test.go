//go:build linux

package document_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// formFixture returns the path of one of the forms fixtures, under testdata/forms.
func formFixture(name string) string { return filepath.Join("testdata", "forms", name) }

// acroformFields are the fields of the generated acroform.pdf, as the generator's doc lists
// them, in the AcroForm's order: what Form reads from qpdf's JSON of it.
var acroformFields = []document.FormField{
	{Name: "applicant.name", Type: document.TextField, Value: "Ada Lovelace"},
	{Name: "applicant.city", Type: document.TextField, Value: "Zürich"},
	{Name: "subscribe", Type: document.CheckboxField, Value: "Yes", Checked: new(true)},
	{Name: "newsletter", Type: document.CheckboxField, Value: "Off", Checked: new(false)},
	{Name: "contact", Type: document.RadioField, Value: "Phone", Checked: new(true)},
	{
		Name: "colour", Type: document.ChoiceField, Value: "Green",
		Options: []string{"Red", "Green", "Blue"}, Selected: []string{"Green"},
	},
	{
		Name: "languages", Type: document.ChoiceField,
		Options: []string{"English", "French", "German"}, Selected: []string{"en", "de"},
	},
}

// standinQPDF installs the stand-in qpdf at version and builds a QPDF over it, returning the
// stand-in's run log.
func standinQPDF(t *testing.T, version string) (*document.QPDF, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "qpdf", version)
	tl, err := tool.NewExec(t.Context(), document.QPDFSpec())
	if err != nil {
		t.Fatal(err)
	}
	q, err := document.NewQPDF(tl)
	if err != nil {
		t.Fatal(err)
	}
	return q, runs
}

// TestFixtureSums holds the forms fixtures to the SHA-256 the testdata README gives them.
func TestFixtureSums(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("testdata", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acroform.pdf", "acroform.json"} {
		b, err := os.ReadFile(formFixture(name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if line := hex.EncodeToString(sum[:]) + "  forms/" + name; !bytes.Contains(readme, []byte(line)) {
			t.Errorf("the testdata README has no line %q", line)
		}
	}
}

// TestNewQPDF: a nil Tool is refused, and a qpdf older than 11.0, whatever its Spec's minimum,
// with a *tool.VersionError wrapping tool.ErrTooOld; qpdf's version line gives its version.
func TestNewQPDF(t *testing.T) {
	if _, err := document.NewQPDF(nil); err == nil {
		t.Error("NewQPDF(nil) succeeded")
	}
	installStandin(t, "qpdf", "10.6.3")
	spec := document.QPDFSpec()
	spec.Minimum = "1"
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Version().String() != "10.6.3" {
		t.Errorf("version %s, want 10.6.3", tl.Version())
	}
	if _, err := document.NewQPDF(tl); !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewQPDF over 10.6.3: %v, want ErrTooOld", err)
	}
}

// TestRequiresPassword: qpdf --requires-password's exit code is the answer, 0 a password needed,
// 2 not encrypted and 3 encrypted but opening without one, warnings aside; a run exiting 2 with
// qpdf's error, as it does for a file it can't open, fails with it, and any other exit fails.
// The PDF is named by its absolute path, in its own directory.
func TestRequiresPassword(t *testing.T) {
	q, runs := standinQPDF(t, "12.4.2")
	pdf := filepath.Join(t.TempDir(), "x.pdf")
	for _, c := range []struct {
		exit, stderr string
		want         bool
		fails        string
	}{
		{exit: "0", want: true},
		{exit: "2"},
		{exit: "2", stderr: "WARNING: x.pdf: file is damaged\nWARNING: x.pdf: Attempting to reconstruct cross-reference table\n"},
		{exit: "3"},
		{exit: "2", stderr: "WARNING: x.pdf: can't find PDF header\nqpdf: x.pdf: unable to find trailer dictionary while recovering damaged file\n", fails: "unable to find trailer dictionary"},
		{exit: "1", stderr: "qpdf: something else\n", fails: "something else"},
	} {
		t.Setenv(exitEnv, c.exit)
		t.Setenv(stderrEnv, c.stderr)
		got, err := q.RequiresPassword(t.Context(), pdf)
		switch {
		case c.fails != "":
			if err == nil || !strings.Contains(err.Error(), c.fails) {
				t.Errorf("exit %s, %q: %v, want an error naming %q", c.exit, c.stderr, err, c.fails)
			}
		case err != nil || got != c.want:
			t.Errorf("exit %s, %q: %t, %v; want %t", c.exit, c.stderr, got, err, c.want)
		}
	}
	last := runs()[len(runs())-1]
	if !slices.Equal(last.Args, []string{"--requires-password", pdf}) || last.Dir != filepath.Dir(pdf) {
		t.Errorf("run %q in %s", last.Args, last.Dir)
	}
}

// TestForm: qpdf's JSON of the generated acroform.pdf, recorded beside it, reads as the
// generator's fields exactly, a radio group's two widgets as one field; a run ending in warnings,
// exit 3, has succeeded; a PDF with no AcroForm has no fields; and a run that fails, or JSON that
// isn't version 2, fails.
func TestForm(t *testing.T) {
	q, runs := standinQPDF(t, "12.4.2")
	b, err := os.ReadFile(formFixture("acroform.json"))
	if err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(t.TempDir(), "x.pdf")
	for _, exit := range []string{"0", "3"} {
		t.Setenv(stdoutEnv, string(b))
		t.Setenv(exitEnv, exit)
		f, err := q.Form(t.Context(), pdf)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.Fields, acroformFields) {
			t.Errorf("exit %s: fields\n got %+v\nwant %+v", exit, f.Fields, acroformFields)
		}
	}
	if args := runs()[0].Args; !slices.Equal(args, []string{"--json=2", "--json-key=acroform", pdf}) {
		t.Errorf("args %q", args)
	}

	t.Setenv(exitEnv, "0")
	t.Setenv(stdoutEnv, `{"version": 2, "acroform": {"fields": [], "hasacroform": false, "needappearances": false}}`)
	if f, err := q.Form(t.Context(), pdf); err != nil || f.Fields == nil || len(f.Fields) != 0 {
		t.Errorf("no AcroForm: %+v, %v; want no fields", f, err)
	}
	t.Setenv(stdoutEnv, `{"version": 1, "acroform": {}}`)
	if _, err := q.Form(t.Context(), pdf); err == nil || !strings.Contains(err.Error(), "version 1") {
		t.Errorf("JSON version 1: %v", err)
	}
	t.Setenv(stdoutEnv, "")
	t.Setenv(exitEnv, "2")
	t.Setenv(stderrEnv, "qpdf: x.pdf: invalid password\n")
	if _, err := q.Form(t.Context(), pdf); err == nil || !strings.Contains(err.Error(), "invalid password") {
		t.Errorf("a failed run: %v", err)
	}
}

// TestFormValues: qpdf's JSON version 2 encodings read as text: a binary string's bytes, a name's
// text with its slash or "n:/" taken off; a pushbutton, a signature, and a field of a type none
// names are typed so, and a value that is neither a string, a name, nor an array of them fails.
func TestFormValues(t *testing.T) {
	q, _ := standinQPDF(t, "12.4.2")
	pdf := filepath.Join(t.TempDir(), "x.pdf")
	t.Setenv(stdoutEnv, `{"version": 2, "acroform": {"hasacroform": true, "fields": [
		{"fullname": "bin", "fieldtype": "/Tx", "istext": true, "value": "b:48c3a9ff"},
		{"fullname": "box", "fieldtype": "/Btn", "ischeckbox": true, "value": "n:/On"},
		{"fullname": "push", "fieldtype": "/Btn", "fieldflags": 65536, "value": null},
		{"fullname": "sig", "fieldtype": "/Sig", "value": null},
		{"fullname": "odd", "fieldtype": "/Xx", "value": "u:x"}
	]}}`)
	f, err := q.Form(t.Context(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	want := []document.FormField{
		{Name: "bin", Type: document.TextField, Value: "Hé\uFFFD"},
		{Name: "box", Type: document.CheckboxField, Value: "On", Checked: new(true)},
		{Name: "push", Type: document.PushbuttonField},
		{Name: "sig", Type: document.SignatureField},
		{Name: "odd", Type: "Xx", Value: "x"},
	}
	if !reflect.DeepEqual(f.Fields, want) {
		t.Errorf("fields\n got %+v\nwant %+v", f.Fields, want)
	}
	t.Setenv(stdoutEnv, `{"version": 2, "acroform": {"hasacroform": true, "fields": [{"fullname": "n", "value": 7}]}}`)
	if _, err := q.Form(t.Context(), pdf); err == nil {
		t.Error("a numeric value was read")
	}
}
