//go:build linux

package office_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestNewConverter: the constructor takes an office at or past 0.1.0, a development build's
// version included, and refuses none at all and an older one, whatever minimum the Tool's own
// Spec declared; the spec's own minimum refuses an older office at the Tool's construction.
func TestNewConverter(t *testing.T) {
	if _, err := office.NewConverter(office.Config{}); err == nil {
		t.Error("NewConverter with no office: no error")
	}

	installStandin(t, "0.1.0+b9bb1a2")
	tl, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.Version().String(); got != "0.1.0" {
		t.Errorf("version: got %s, want 0.1.0", got)
	}
	if _, err := office.NewConverter(office.Config{Office: tl}); err != nil {
		t.Errorf("NewConverter over 0.1.0+b9bb1a2: %v", err)
	}

	installStandin(t, "0.0.9")
	if _, err := tool.NewExec(t.Context(), office.OfficeSpec()); !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewExec over 0.0.9: got %v, want ErrTooOld", err)
	}
	lax := office.OfficeSpec()
	lax.Minimum = "0.0.1"
	old, err := tool.NewExec(t.Context(), lax)
	if err != nil {
		t.Fatal(err)
	}
	_, err = office.NewConverter(office.Config{Office: old})
	var verr *tool.VersionError
	if !errors.As(err, &verr) || !errors.Is(err, tool.ErrTooOld) {
		t.Fatalf("NewConverter over 0.0.9: got %v, want a VersionError wrapping ErrTooOld", err)
	}
	if verr.Found != "0.0.9" || verr.Minimum != office.OfficeMinimum {
		t.Errorf("VersionError: found %s, minimum %s", verr.Found, verr.Minimum)
	}
}

// TestConvertRun: a conversion runs office once in the request's working directory, on the
// document spooled there, with the format and a media directory of its own made there, and
// removes the spooled document, leaving the media directory.
func TestConvertRun(t *testing.T) {
	c, runs := standinConverter(t)
	for _, tc := range []struct {
		format office.Format
		arg    string
	}{{office.JSON, "json"}, {office.Markdown, "markdown"}} {
		t.Run(tc.arg, func(t *testing.T) {
			dir := t.TempDir()
			before := len(runs())
			t.Setenv(stdoutEnv, map[office.Format]string{office.JSON: standinDocument, office.Markdown: "# Title\n"}[tc.format])
			res, err := c.Convert(t.Context(), strings.NewReader("the document"), office.Request{Dir: dir, Format: tc.format})
			if err != nil {
				t.Fatal(err)
			}
			all := runs()
			if len(all) != before+1 {
				t.Fatalf("runs: got %d, want 1", len(all)-before)
			}
			run := all[before]
			if run.Dir != dir {
				t.Errorf("working directory: got %s, want %s", run.Dir, dir)
			}
			if run.Input != "the document" {
				t.Errorf("input: got %q, want the document", run.Input)
			}
			if len(run.Args) != 5 || filepath.Dir(run.Args[0]) != dir || !strings.HasPrefix(filepath.Base(run.Args[0]), "spool-") ||
				!slices.Equal(run.Args[1:4], []string{"--format", tc.arg, "--media"}) || run.Args[4] != res.MediaDir {
				t.Errorf("arguments: got %q, want [<dir>/spool-*, --format, %s, --media, %s]", run.Args, tc.arg, res.MediaDir)
			}
			if filepath.Dir(res.MediaDir) != dir || !strings.HasPrefix(filepath.Base(res.MediaDir), "media-") {
				t.Errorf("media directory: got %s, want %s/media-*", res.MediaDir, dir)
			}
			if got := entries(t, dir); !slices.Equal(got, []string{filepath.Base(res.MediaDir)}) {
				t.Errorf("working directory holds %q, want the media directory alone", got)
			}
			switch tc.format {
			case office.JSON:
				if res.Document == nil || res.Document.Format != "docx" || res.Markdown != "" {
					t.Errorf("JSON result: document %+v, markdown %q", res.Document, res.Markdown)
				}
			case office.Markdown:
				if res.Document != nil || res.Markdown != "# Title\n" {
					t.Errorf("Markdown result: document %+v, markdown %q", res.Document, res.Markdown)
				}
			}
			if res.Media != nil || res.Warnings != nil {
				t.Errorf("media %v, warnings %q: want none", res.Media, res.Warnings)
			}
		})
	}
}

// TestConvertExits: office's exit codes 1 to 4 are each an *ExitError matching its own error and
// no other's, carrying office's message without its prefix or the spooled path, and a failed
// conversion leaves nothing in the working directory.
func TestConvertExits(t *testing.T) {
	c, _ := standinConverter(t)
	all := []error{office.ErrMediaNotWritten, office.ErrUsage, office.ErrRefused, office.ErrFailed}
	for _, tc := range []struct {
		code    string
		want    error
		stderr  string
		message string
	}{
		{"1", office.ErrMediaNotWritten,
			"spike-ai-pipeline-office: warning: {path}: a header skipped\nspike-ai-pipeline-office: media not written: Access to the path is denied.\n",
			"Access to the path is denied."},
		{"2", office.ErrUsage,
			"Unrecognized command or argument '--bogus'.\n\nDescription:\n  Converts a docx...\n",
			"Unrecognized command or argument '--bogus'."},
		{"3", office.ErrRefused,
			"spike-ai-pipeline-office: refused: {path}: an OLE compound file, which is an encrypted package or a legacy binary format (doc, xls, ppt); decrypt it or convert it to docx, xlsx, or pptx first\n",
			"an OLE compound file, which is an encrypted package or a legacy binary format (doc, xls, ppt); decrypt it or convert it to docx, xlsx, or pptx first"},
		{"4", office.ErrFailed,
			"spike-ai-pipeline-office: failed: {path}: an unexpected System.NullReferenceException: Object reference not set\n",
			"an unexpected System.NullReferenceException: Object reference not set"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Setenv(exitEnv, tc.code)
			t.Setenv(stderrEnv, tc.stderr)
			t.Setenv(mediaEnv, "a.png")
			dir := t.TempDir()
			res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: dir})
			if res != nil {
				t.Errorf("result: got %+v, want nil", res)
			}
			for _, e := range all {
				if is := errors.Is(err, e); is != (e == tc.want) {
					t.Errorf("errors.Is(%v, %v): got %t", err, e, is)
				}
			}
			var exit *office.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("got %v, want an *office.ExitError", err)
			}
			if exit.Message != tc.message {
				t.Errorf("message: got %q, want %q", exit.Message, tc.message)
			}
			if want := "office: " + tc.want.Error() + ": " + tc.message; err.Error() != want {
				t.Errorf("error: got %q, want %q", err.Error(), want)
			}
			var texit *tool.ExitError
			if !errors.As(err, &texit) || texit.Program != "spike-ai-pipeline-office" || strings.TrimSpace(texit.Stderr) != "" {
				t.Errorf("tool error: got %#v, want office's *tool.ExitError", texit)
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("working directory holds %q, want nothing", got)
			}
		})
	}
}

// TestConvertWarnings: each warning line on stderr is a warning of the result, without office's
// prefix or the spooled path, and the conversion succeeds.
func TestConvertWarnings(t *testing.T) {
	c, _ := standinConverter(t)
	t.Setenv(stderrEnv, "spike-ai-pipeline-office: warning: {path}: header reference rId9 names no header\n"+
		"spike-ai-pipeline-office: warning: {path}: sheet Hidden names no part\n")
	res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"header reference rId9 names no header", "sheet Hidden names no part"}
	if !slices.Equal(res.Warnings, want) {
		t.Errorf("warnings: got %q, want %q", res.Warnings, want)
	}
}

// TestConvertCancel: a conversion whose context ends while office runs returns an error matching
// the context's, never an *ExitError, and leaves nothing in the working directory.
func TestConvertCancel(t *testing.T) {
	c, _ := standinConverter(t)
	t.Setenv(sleepEnv, "1")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The stand-in logs its run before it sleeps, so the log's existence says it is running. The
	// poll ends with ctx, so it never outlives the test when office never runs.
	log := os.Getenv(logEnv)
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(log); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_, err := c.Convert(ctx, strings.NewReader("doc"), office.Request{Dir: dir})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if _, ok := errors.AsType[*office.ExitError](err); ok {
		t.Errorf("got an *office.ExitError: %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("working directory holds %q, want nothing", got)
	}

	if _, err := c.Convert(ctx, strings.NewReader("doc"), office.Request{Dir: dir}); !errors.Is(err, context.Canceled) {
		t.Errorf("Convert on an ended context: got %v, want context.Canceled", err)
	}
}

// TestConvertRefuses: a request with no working directory, or a format that is none, is refused
// before office runs.
func TestConvertRefuses(t *testing.T) {
	c, runs := standinConverter(t)
	for name, req := range map[string]office.Request{
		"no directory": {},
		"format":       {Dir: t.TempDir(), Format: office.Format(7)},
	} {
		if _, err := c.Convert(t.Context(), strings.NewReader("doc"), req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(runs()); n != 0 {
		t.Errorf("office ran %d times, want none", n)
	}
}

// TestConvertMedia: the media list has one entry per file in the media directory, in name order,
// each with its name, its content type by its extension, and the stored file handed onward, not
// converted, in a JSON and a Markdown result alike.
func TestConvertMedia(t *testing.T) {
	c, _ := standinConverter(t)
	t.Setenv(mediaEnv, "b.emf,a.png,c.WMF,d.jpeg,e.bin")
	for _, format := range []office.Format{office.JSON, office.Markdown} {
		res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: t.TempDir(), Format: format})
		if err != nil {
			t.Fatal(err)
		}
		want := []office.MediaFile{
			{Name: "a.png", ContentType: "image/png"},
			{Name: "b.emf", ContentType: "image/x-emf"},
			{Name: "c.WMF", ContentType: "image/x-wmf"},
			{Name: "d.jpeg", ContentType: "image/jpeg"},
			{Name: "e.bin", ContentType: "application/octet-stream"},
		}
		for i := range want {
			want[i].File = filepath.Join(res.MediaDir, want[i].Name)
		}
		if !slices.Equal(res.Media, want) {
			t.Errorf("%v media: got %+v, want %+v", format, res.Media, want)
		}
		for _, m := range res.Media {
			if b, err := os.ReadFile(m.File); err != nil || string(b) != m.Name {
				t.Errorf("%s: got %q, %v", m.File, b, err)
			}
		}
	}
}

// TestExitErrorUnknownCode: an ExitError of a code office doesn't use, such as one a caller
// builds, names its code and matches none of the exit codes' errors.
func TestExitErrorUnknownCode(t *testing.T) {
	err := &office.ExitError{Code: 9, Message: "m"}
	if got, want := err.Error(), "office: exit code 9: m"; got != want {
		t.Errorf("error: got %q, want %q", got, want)
	}
	for _, e := range []error{office.ErrMediaNotWritten, office.ErrUsage, office.ErrRefused, office.ErrFailed} {
		if errors.Is(err, e) {
			t.Errorf("matches %v", e)
		}
	}
}
