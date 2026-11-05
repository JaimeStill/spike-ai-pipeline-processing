//go:build linux

package onenote_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestNewReader: a stand-in at the minimum, or a build past it, is accepted; an older one fails
// at construction, whether the Tool's Spec or NewReader refuses it; a nil Tool is refused, and so
// is a missing program, by the Tool's construction.
func TestNewReader(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.1.0+8ea6b2d", "0.2.0"} {
		t.Run(version, func(t *testing.T) {
			installStandin(t, version)
			tl, err := tool.NewExec(t.Context(), onenote.OneNoteSpec())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := onenote.NewReader(onenote.Config{OneNote: tl}); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("older", func(t *testing.T) {
		installStandin(t, "0.0.9")
		_, err := tool.NewExec(t.Context(), onenote.OneNoteSpec())
		if !errors.Is(err, tool.ErrTooOld) {
			t.Fatalf("spec's own minimum: got %v, want ErrTooOld", err)
		}
		spec := onenote.OneNoteSpec()
		spec.Minimum = "0.0.1"
		tl, err := tool.NewExec(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		_, err = onenote.NewReader(onenote.Config{OneNote: tl})
		verr, ok := errors.AsType[*tool.VersionError](err)
		if !errors.Is(err, tool.ErrTooOld) || !ok || verr.Found != "0.0.9" || verr.Minimum != onenote.OneNoteMinimum {
			t.Fatalf("NewReader: got %v, want a VersionError for 0.0.9 under %s", err, onenote.OneNoteMinimum)
		}
	})
	t.Run("nil", func(t *testing.T) {
		if _, err := onenote.NewReader(onenote.Config{}); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := tool.NewExec(t.Context(), onenote.OneNoteSpec()); err == nil {
			t.Fatal("want an error for a onenote not on PATH")
		}
	})
}

// fullLines are a notebook's lines with every top-level field of schema "1" set, a section
// group, a nested section, a page with ink and recognised text, a skipped section, and an
// unknown kind among them.
const fullLines = `{"line":"notebook","schema":"1","input":"notebook","name":"Work","id":"f1da443f-a65f-4513-b200-78d8a9910b8d","color":"#9bbbd2"}
{"line":"section_group","id":"2ed96381-1fac-4ca5-bda9-0e3a35478644","path":["Group"],"display_name":"Group"}
{"line":"section","id":"07e1868e-e7d0-c24e-85c6-7cbe04486764","parent":"2ed96381-1fac-4ca5-bda9-0e3a35478644","path":["Group","Notes"],"display_name":"Notes","color":"#ab9cb7"}
{"line":"page","id":"5eaf76b1-8dde-8745-a8a7-1a9865cc478a","section":"07e1868e-e7d0-c24e-85c6-7cbe04486764","level":2,"title":"Test Page","creation_time":"2020-10-27T10:47:23Z","modification_time":"2020-10-27T10:52:39Z","author":"Markus Siemens","content":[{"kind":"title","offset_horizontal":0.0,"offset_vertical":0.0,"items":[]},{"kind":"ink","strokes":2}],"ink":{"file":"95c9.svg","strokes":62,"bounds":{"x":1363.0,"y":0,"width":16563.5,"height":15749.0}},"ink_text":[[{"text":"Hello","alternatives":["Hallo","Hell"],"language":1031}],[{"text":"World"}]],"markdown":"# Test Page"}
{"line":"skipped","kind":"section","name":"Encryption.one","reason":"Unexpected end of file"}
{"line":"tag_index","when":"later"}
`

// TestRead: the stand-in's lines are yielded in its order, each decoded into its typed record,
// every top-level field of schema "1" set, a page's content kept as its JSON; a line of an
// unknown kind is kept with its JSON. onenote runs once, on the input made absolute.
func TestRead(t *testing.T) {
	r, runs := standinReader(t)
	t.Setenv(stdoutEnv, fullLines)
	t.Chdir(t.TempDir())
	recs, err := collect(r.Read(t.Context(), onenote.Request{Input: "-Work.onetoc2"}))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, rec := range recs {
		kinds = append(kinds, rec.Line())
	}
	if want := []string{"notebook", "section_group", "section", "page", "skipped", "tag_index"}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds %q, want %q", kinds, want)
	}
	if got, want := recs[0], (&onenote.Notebook{Schema: "1", Input: "notebook", Name: "Work", ID: "f1da443f-a65f-4513-b200-78d8a9910b8d", Color: "#9bbbd2"}); !reflect.DeepEqual(got, want) {
		t.Errorf("notebook %+v, want %+v", got, want)
	}
	if got, want := recs[1], (&onenote.SectionGroup{ID: "2ed96381-1fac-4ca5-bda9-0e3a35478644", Path: []string{"Group"}, DisplayName: "Group"}); !reflect.DeepEqual(got, want) {
		t.Errorf("section group %+v, want %+v", got, want)
	}
	if got, want := recs[2], (&onenote.Section{ID: "07e1868e-e7d0-c24e-85c6-7cbe04486764", Parent: "2ed96381-1fac-4ca5-bda9-0e3a35478644", Path: []string{"Group", "Notes"}, DisplayName: "Notes", Color: "#ab9cb7"}); !reflect.DeepEqual(got, want) {
		t.Errorf("section %+v, want %+v", got, want)
	}
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	page, ok := recs[3].(*onenote.Page)
	if !ok {
		t.Fatalf("record 3 %#v", recs[3])
	}
	if want := `[{"kind":"title","offset_horizontal":0.0,"offset_vertical":0.0,"items":[]},{"kind":"ink","strokes":2}]`; string(page.Content) != want {
		t.Errorf("content %s, want %s", page.Content, want)
	}
	want := &onenote.Page{
		ID: "5eaf76b1-8dde-8745-a8a7-1a9865cc478a", Section: "07e1868e-e7d0-c24e-85c6-7cbe04486764",
		Level: 2, Title: "Test Page",
		CreationTime: at("2020-10-27T10:47:23Z"), ModificationTime: at("2020-10-27T10:52:39Z"),
		Author:  "Markus Siemens",
		Content: page.Content,
		Ink:     &onenote.Ink{File: "95c9.svg", Strokes: 62, Bounds: &onenote.Bounds{X: 1363, Width: 16563.5, Height: 15749}},
		InkText: [][]onenote.Word{
			{{Text: "Hello", Alternatives: []string{"Hallo", "Hell"}, Language: 1031}},
			{{Text: "World"}},
		},
		Markdown: "# Test Page",
	}
	if !reflect.DeepEqual(page, want) {
		t.Errorf("page %+v\nwant %+v", page, want)
	}
	if got, want := recs[4], (&onenote.Skipped{Kind: "section", Name: "Encryption.one", Reason: "Unexpected end of file"}); !reflect.DeepEqual(got, want) {
		t.Errorf("skipped %+v, want %+v", got, want)
	}
	unknown, ok := recs[5].(*onenote.UnknownRecord)
	if !ok || string(unknown.Raw) != `{"line":"tag_index","when":"later"}` {
		t.Errorf("unknown record %#v", recs[5])
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runs(), [][]string{{filepath.Join(dir, "-Work.onetoc2")}}; !reflect.DeepEqual(got, want) {
		t.Errorf("runs %q, want %q", got, want)
	}
}

// TestReadMedia: a Request's Media, made absolute, is passed as --media before the input.
func TestReadMedia(t *testing.T) {
	r, runs := standinReader(t)
	t.Chdir(t.TempDir())
	if _, err := collect(r.Read(t.Context(), onenote.Request{Input: "Notes.one", Media: "media"})); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"--media", filepath.Join(dir, "media"), filepath.Join(dir, "Notes.one")}}
	if got := runs(); !reflect.DeepEqual(got, want) {
		t.Errorf("runs %q, want %q", got, want)
	}
}

// TestReadWarnings: a read whose stderr holds warnings, with a section skipped, succeeds; each
// warning line is one warning, onenote's prefix taken off, in order.
func TestReadWarnings(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(stdoutEnv, `{"line":"notebook","schema":"1","input":"notebook","name":"SUT"}
{"line":"skipped","kind":"section","name":"Encryption.one","reason":"Unexpected end of file"}
`)
	t.Setenv(stderrEnv, "spike-ai-pipeline-onenote: warning: /Encryption.one: the section is skipped: Unexpected end of file\n"+
		"spike-ai-pipeline-onenote: warning: /Group/Section, page 5eaf76b1-8dde-8745-a8a7-1a9865cc478a, image 2: its data is missing\n")
	s := mustRead(t)(r.Read(t.Context(), onenote.Request{Input: "SUT"}))
	recs, err := collect(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1].Line() != "skipped" {
		t.Fatalf("records %v", recs)
	}
	want := []string{
		"/Encryption.one: the section is skipped: Unexpected end of file",
		"/Group/Section, page 5eaf76b1-8dde-8745-a8a7-1a9865cc478a, image 2: its data is missing",
	}
	if got := s.Warnings(); !slices.Equal(got, want) {
		t.Errorf("warnings %q, want %q", got, want)
	}
}

// TestReadExit: exit codes 1 to 4 are an *ExitError matching the code's error, the error every
// CLI this module runs shares, so email's and office's match it too; it carries onenote's
// message without its prefix, label, and the input's path, the usage after a usage error's
// message left out, and unwraps to the Tool's *tool.ExitError; a warning before the error is
// still a warning.
func TestReadExit(t *testing.T) {
	cases := []struct {
		code   int
		want   error
		shared []error
		stderr string
		msg    string
	}{
		{1, onenote.ErrNotWritten, []error{email.ErrNotWritten, office.ErrMediaNotWritten}, "spike-ai-pipeline-onenote: error: writing stdout: Broken pipe\n", "writing stdout: Broken pipe"},
		{2, onenote.ErrUsage, []error{email.ErrUsage, office.ErrUsage}, "spike-ai-pipeline-onenote: error: unknown option --bogus\nusage: spike-ai-pipeline-onenote [--media <dir>] <input>\n       spike-ai-pipeline-onenote --version\n", "unknown option --bogus"},
		{3, onenote.ErrRefused, []error{email.ErrRefused, office.ErrRefused}, "spike-ai-pipeline-onenote: error: {path}: Unexpected end of file\n", "Unexpected end of file"},
		{4, onenote.ErrFailed, []error{email.ErrFailed, office.ErrFailed}, "spike-ai-pipeline-onenote: warning: /: odd\nspike-ai-pipeline-onenote: error: internal\n", "internal"},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.code), func(t *testing.T) {
			r, _ := standinReader(t)
			t.Setenv(stdoutEnv, "")
			t.Setenv(stderrEnv, c.stderr)
			t.Setenv(exitEnv, strconv.Itoa(c.code))
			s := mustRead(t)(r.Read(t.Context(), onenote.Request{Input: "in.one"}))
			recs, err := collect(s, nil)
			if len(recs) != 0 {
				t.Errorf("records %v, want none", recs)
			}
			exit, isExit := errors.AsType[*onenote.ExitError](err)
			_, isTool := errors.AsType[*tool.ExitError](err)
			if !errors.Is(err, c.want) || !isExit || !isTool {
				t.Fatalf("got %v, want an ExitError matching %v", err, c.want)
			}
			for _, shared := range c.shared {
				if !errors.Is(err, shared) {
					t.Errorf("doesn't match %v, the shared error", shared)
				}
			}
			for _, other := range []error{onenote.ErrNotWritten, onenote.ErrUsage, onenote.ErrRefused, onenote.ErrFailed} {
				if other != c.want && errors.Is(err, other) {
					t.Errorf("matches %v too", other)
				}
			}
			if exit.Code != c.code || exit.Message != c.msg {
				t.Errorf("code %d, message %q; want %d, %q", exit.Code, exit.Message, c.code, c.msg)
			}
			if c.code == 4 && !slices.Equal(s.Warnings(), []string{"/: odd"}) {
				t.Errorf("warnings %q", s.Warnings())
			}
		})
	}
}

// TestReadExitOther: an exit code outside 1 to 4 is no *ExitError of the package's, but carries
// onenote's message.
func TestReadExitOther(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(stdoutEnv, "")
	t.Setenv(stderrEnv, "spike-ai-pipeline-onenote: error: odd\n")
	t.Setenv(exitEnv, "9")
	_, err := collect(r.Read(t.Context(), onenote.Request{Input: "in.one"}))
	_, isExit := errors.AsType[*onenote.ExitError](err)
	toolExit, isTool := errors.AsType[*tool.ExitError](err)
	if isExit || !isTool || toolExit.Code != 9 || !strings.Contains(err.Error(), "odd") {
		t.Fatalf("got %v", err)
	}
}

// TestReadSchema: a notebook line of another schema, or of none, is refused with ErrSchema naming
// it, and nothing is yielded.
func TestReadSchema(t *testing.T) {
	cases := []struct {
		name, stdout, want string
	}{
		{"foreign", `{"line":"notebook","schema":"2","input":"section"}` + "\n" + `{"line":"section","id":"x"}` + "\n", `"2"`},
		{"none", `{"line":"notebook","input":"section"}` + "\n", "has none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := standinReader(t)
			t.Setenv(stdoutEnv, c.stdout)
			recs, err := collect(r.Read(t.Context(), onenote.Request{Input: "in.one"}))
			if !errors.Is(err, onenote.ErrSchema) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want ErrSchema naming %s", err, c.want)
			}
			if len(recs) != 0 {
				t.Errorf("records %v, want none", recs)
			}
		})
	}
}

// TestReadMalformed: output that isn't onenote's lines is an error, whatever was yielded before
// it.
func TestReadMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"not notebook":    `{"line":"section","id":"x"}` + "\n",
		"second notebook": standinLines + `{"line":"notebook","schema":"1"}` + "\n",
		"no line":         `{"line":"notebook","schema":"1"}` + "\n" + `{"id":"x"}` + "\n",
		"not JSON":        `{"line":"notebook","schema":"1"}` + "\nnot JSON\n",
		"cut short":       `{"line":"notebook","schema":"1"}` + "\n" + `{"line":"section",`,
		"wrong type":      `{"line":"notebook","schema":"1"}` + "\n" + `{"line":"page","level":"x"}` + "\n",
	}
	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := standinReader(t)
			t.Setenv(stdoutEnv, stdout)
			if _, err := collect(r.Read(t.Context(), onenote.Request{Input: "in.one"})); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// TestReadCutShortExit: output cut short by a onenote that then failed is onenote's exit error.
func TestReadCutShortExit(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(stdoutEnv, `{"line":"notebook","schema":"1"}`+"\n"+`{"line":"section",`)
	t.Setenv(stderrEnv, "spike-ai-pipeline-onenote: error: panicked\n")
	t.Setenv(exitEnv, "4")
	recs, err := collect(r.Read(t.Context(), onenote.Request{Input: "in.one"}))
	if !errors.Is(err, onenote.ErrFailed) || len(recs) != 1 {
		t.Fatalf("got %v after %d records, want ErrFailed after the notebook", err, len(recs))
	}
}

// TestReadCancel: records are yielded while onenote runs, and a read whose context is cancelled
// while onenote runs stops it and returns an error matching context.Canceled, never an
// ExitError, well before onenote would have exited.
func TestReadCancel(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(sleepEnv, "1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Now()
	var recs []onenote.Record
	var err error
	for rec, rerr := range mustRead(t)(r.Read(ctx, onenote.Request{Input: "in.one"})).Records() {
		if rerr != nil {
			err = rerr
			break
		}
		recs = append(recs, rec)
		if len(recs) == 2 {
			cancel()
		}
	}
	if _, isExit := errors.AsType[*onenote.ExitError](err); !errors.Is(err, context.Canceled) || isExit {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if len(recs) != 2 {
		t.Errorf("%d records before the cancel, want 2", len(recs))
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("took %v", d)
	}
}

// TestReadDeadline: a read whose deadline passes while onenote runs returns an error matching
// context.DeadlineExceeded.
func TestReadDeadline(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(sleepEnv, "1")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	recs, err := collect(r.Read(ctx, onenote.Request{Input: "in.one"}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded", err)
	}
	if len(recs) != 3 {
		t.Errorf("%d records, want 3", len(recs))
	}
}

// TestReadStop: a loop that stops early stops onenote, without waiting for it to exit, and
// nothing more is yielded.
func TestReadStop(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(sleepEnv, "1")
	start := time.Now()
	n := 0
	for _, err := range mustRead(t)(r.Read(t.Context(), onenote.Request{Input: "in.one"})).Records() {
		if err != nil {
			t.Fatal(err)
		}
		n++
		break
	}
	if n != 1 {
		t.Errorf("%d records", n)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("took %v", d)
	}
}

// TestReadOnce: a Stream's records are read once; ranging again yields an error and runs
// nothing. A cancelled context runs nothing.
func TestReadOnce(t *testing.T) {
	r, runs := standinReader(t)
	s := mustRead(t)(r.Read(t.Context(), onenote.Request{Input: "in.one"}))
	if _, err := collect(s, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(s, nil); err == nil {
		t.Error("second range: want an error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collect(r.Read(ctx, onenote.Request{Input: "in.one"})); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: got %v", err)
	}
	if got := len(runs()); got != 1 {
		t.Errorf("%d runs, want 1", got)
	}
}

// TestReadRefused: Read refuses an invalid request at once, with an error matching ErrRequest
// and no Stream: an empty Input, and, from a working directory that has been removed, a relative
// Input or Media, which can't be made absolute. onenote never runs; an absolute Input with no
// Media is still read from there.
func TestReadRefused(t *testing.T) {
	r, runs := standinReader(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(t.TempDir(), "in.one")
	cases := map[string]onenote.Request{
		"no input":       {Media: "media"},
		"relative input": {Input: "in.one"},
		"relative media": {Input: abs, Media: "media"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := r.Read(t.Context(), req)
			if !errors.Is(err, onenote.ErrRequest) || s != nil {
				t.Errorf("got %v, %v; want ErrRequest and no Stream", s, err)
			}
		})
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("runs %q, want none", got)
	}
	if _, err := collect(r.Read(t.Context(), onenote.Request{Input: abs})); err != nil {
		t.Errorf("absolute input: %v", err)
	}
}
