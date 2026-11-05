//go:build linux

package inspect_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// cleanDirs are the directories of the repository's clean fixtures: the office, document,
// picture, tool, and email packages' testdata.
var cleanDirs = []string{"../office/testdata", "../document/testdata", "../picture/testdata", "../tool/testdata", "../email/testdata"}

// cleanSkipped are the files under cleanDirs that aren't clean fixtures: picture's
// script-bearing.svg, ~/docs' SVG hazard, copied as the conversions' security fixture.
var cleanSkipped = map[string]bool{"../picture/testdata/script-bearing.svg": true}

// cleanFlags are the clean fixtures that carry what a check reads, and its checks. A linked
// picture is an external relationship, fetched when a reader renders the document, as an SVG's
// external href is, so it is active content though the fixture is clean; an external
// hyperlink, which word-with-attachments.docx holds three of, isn't. Its OLE object is one of
// the embeddings the checks name.
var cleanFlags = map[string][]inspect.Check{
	"../office/testdata/ooxml/media.libreoffice.docx":     {inspect.ExternalRelationship},
	"../office/testdata/ooxml/word-with-attachments.docx": {inspect.OLEObject},
}

// TestCleanFixtures inspects the repository's clean fixtures, every file under cleanDirs but the
// generator scripts, which are executable by design, and cleanSkipped: each yields no flag, but
// cleanFlags' files, which yield exactly their checks, and every verdict is proceed.
func TestCleanFixtures(t *testing.T) {
	n := 0
	for _, dir := range cleanDirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(path) == ".sh" || cleanSkipped[path] {
				return err
			}
			n++
			r := mustInspect(t, path, d.Name(), inspect.Options{})
			if got, want := checks(r), cleanFlags[path]; !slices.Equal(got, want) {
				t.Errorf("%s: checks %v, want %v (%+v)", path, got, want, r.Flags)
			}
			if r.Verdict != inspect.Proceed {
				t.Errorf("%s: verdict %s", path, r.Verdict)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("inspected %d clean fixtures", n)
}

// attachmentFlags are the email fixtures' attachments that carry what a check reads, by their
// input and stored filename, and their checks: Apache POI's alfresco.gif is a BMP, a signature
// that disagrees with its extension, as real stores have it.
var attachmentFlags = map[string][]inspect.Check{
	"../email/testdata/mail/fixtures/msg/poi/example_sent_regular.msg alfresco.gif": {inspect.SignatureMismatch},
}

// TestCleanAttachments inspects the attachments of the email package's fixtures, as the real
// pinned mail writes them: it reads every .pst, .msg, .eml, and mbox under email's testdata, each
// into an attachments directory of its own, and inspects each attachment's file under the name it
// was stored with. None yields a flag, but attachmentFlags', which yield exactly their checks, and
// every verdict is proceed.
func TestCleanAttachments(t *testing.T) {
	tl, err := tool.NewExec(t.Context(), email.MailSpec())
	if err != nil {
		t.Fatalf("mail, which mise pins, run the check through mise run check: %v", err)
	}
	reader, err := email.NewReader(email.Config{Mail: tl})
	if err != nil {
		t.Fatal(err)
	}
	var inputs []string
	err = filepath.WalkDir("../email/testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch ext := filepath.Ext(path); {
		case ext == ".pst" || ext == ".msg" || ext == ".eml" || ext == ".mboxrd" || strings.HasPrefix(d.Name(), "mbox."):
			inputs = append(inputs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, input := range inputs {
		dir := t.TempDir()
		s, err := reader.Read(t.Context(), email.Request{Input: input, Attachments: dir})
		if err != nil {
			t.Fatal(err)
		}
		for rec, err := range s.Records() {
			if err != nil {
				t.Fatalf("%s: %v", input, err)
			}
			if msg, ok := rec.(*email.Message); ok {
				n += inspectAttachments(t, input, msg)
			}
		}
	}
	t.Logf("inspected %d attachments of %d inputs", n, len(inputs))
	if n == 0 {
		t.Error("no attachment inspected")
	}
}

// inspectAttachments inspects msg's attachments that have a file, its nested messages' among
// them, and returns how many it inspected.
func inspectAttachments(t *testing.T, input string, msg *email.Message) int {
	t.Helper()
	n := 0
	for _, a := range msg.Attachments {
		if a.Message != nil {
			n += inspectAttachments(t, input, a.Message)
		}
		if a.Path == "" {
			continue
		}
		if _, err := os.Stat(a.Path); err != nil {
			t.Errorf("%s: %q: %v", input, a.Filename, err)
			continue
		}
		n++
		name := a.Filename
		if name == "" {
			name = a.File
		}
		r := mustInspect(t, a.Path, name, inspect.Options{})
		if got, want := checks(r), attachmentFlags[input+" "+a.Filename]; !slices.Equal(got, want) {
			t.Errorf("%s: %q: checks %v, want %v (%+v)", input, a.Filename, got, want, r.Flags)
		}
		if r.Verdict != inspect.Proceed {
			t.Errorf("%s: %q: verdict %s", input, a.Filename, r.Verdict)
		}
	}
	return n
}
