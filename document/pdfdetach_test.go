//go:build linux

package document_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinDetacher installs the stand-in pdfdetach at version and builds a Detacher over it,
// returning the stand-in's run log.
func standinDetacher(t *testing.T, version string) (*document.Detacher, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "pdfdetach", version)
	tl, err := tool.NewExec(t.Context(), document.PDFDetachSpec())
	if err != nil {
		t.Fatal(err)
	}
	d, err := document.NewDetacher(tl)
	if err != nil {
		t.Fatal(err)
	}
	return d, runs
}

// TestNewDetacher: pdfdetach's version, printed by -v, which exits 99, is read; a nil Tool is
// refused, and a pdfdetach older than 22.02, whatever its Spec's minimum, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func TestNewDetacher(t *testing.T) {
	if _, err := document.NewDetacher(nil); err == nil {
		t.Error("NewDetacher(nil) succeeded")
	}
	installStandin(t, "pdfdetach", "22.01.0")
	spec := document.PDFDetachSpec()
	spec.Minimum = "1"
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Version().String() != "22.1.0" {
		t.Errorf("version %s, want 22.1.0", tl.Version())
	}
	if _, err := document.NewDetacher(tl); !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewDetacher over 22.01.0: %v, want ErrTooOld", err)
	}
	d, _ := standinDetacher(t, "26.10.0")
	if d == nil {
		t.Error("no Detacher over 26.10.0")
	}
}

// TestList: pdfdetach -list's count and numbered names read as attachments, in order, in UTF-8;
// a name with a line break in it is read whole; a listing whose count its lines don't match, or
// that begins with no count, fails, and so does a failed run, naming pdfdetach's message.
func TestList(t *testing.T) {
	d, runs := standinDetacher(t, "26.10.0")
	pdf := filepath.Join(t.TempDir(), "x.pdf")
	t.Setenv(stdoutEnv, "3 embedded files\n1: KSBASE.WQ2\n2: two\nlines.txt\n3: ../../etc/passwd\n")
	got, err := d.List(t.Context(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	want := []document.Attachment{{1, "KSBASE.WQ2"}, {2, "two\nlines.txt"}, {3, "../../etc/passwd"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list %+v, want %+v", got, want)
	}
	if r := runs()[0]; !slices.Equal(r.Args, []string{"-list", "-enc", "UTF-8", pdf}) || r.Dir != filepath.Dir(pdf) {
		t.Errorf("run %q in %s", r.Args, r.Dir)
	}
	t.Setenv(stdoutEnv, "0 embedded files\n")
	if got, err := d.List(t.Context(), pdf); err != nil || len(got) != 0 {
		t.Errorf("none: %+v, %v", got, err)
	}
	for _, out := range []string{"2 embedded files\n1: a\n", "nothing\n", "1 embedded files\nstray\n1: a\n"} {
		t.Setenv(stdoutEnv, out)
		if _, err := d.List(t.Context(), pdf); err == nil {
			t.Errorf("listing %q was read", out)
		}
	}
	t.Setenv(stdoutEnv, "")
	t.Setenv(stderrEnv, "Command Line Error: Incorrect password\n")
	t.Setenv(exitEnv, "1")
	if _, err := d.List(t.Context(), pdf); err == nil || !strings.Contains(err.Error(), "Incorrect password") {
		t.Errorf("a failed run: %v", err)
	}
}

// TestSave: a file is saved to /dev/stdout, never to its stored name, and its bytes reach the
// writer; a number pdfdetach refuses, or one below 1, is ErrNoAttachment.
func TestSave(t *testing.T) {
	d, runs := standinDetacher(t, "26.10.0")
	pdf := filepath.Join(t.TempDir(), "x.pdf")
	t.Setenv(stdoutEnv, "the attachment's bytes")
	var b bytes.Buffer
	if err := d.Save(t.Context(), pdf, 2, &b); err != nil {
		t.Fatal(err)
	}
	if b.String() != "the attachment's bytes" {
		t.Errorf("saved %q", b.String())
	}
	if r := runs()[0]; !slices.Equal(r.Args, []string{"-save", "2", "-o", "/dev/stdout", pdf}) {
		t.Errorf("args %q", r.Args)
	}
	t.Setenv(stdoutEnv, "")
	t.Setenv(stderrEnv, "Command Line Error: Invalid file number\n")
	t.Setenv(exitEnv, "99")
	if err := d.Save(t.Context(), pdf, 9, &b); !errors.Is(err, document.ErrNoAttachment) {
		t.Errorf("number 9: %v, want ErrNoAttachment", err)
	}
	if err := d.Save(t.Context(), pdf, 0, &b); !errors.Is(err, document.ErrNoAttachment) {
		t.Errorf("number 0: %v, want ErrNoAttachment", err)
	}
}
