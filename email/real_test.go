//go:build linux

package email_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// mailCopy is the copy of mail's fixtures and goldens, pinned by its MANIFEST.
var mailCopy = filepath.Join("testdata", "mail")

// manifestLine matches a file's line in the MANIFEST: its SHA-256, two spaces, and its path.
var manifestLine = regexp.MustCompile(`^([0-9a-f]{64})  (\S.*)$`)

// commitHash matches a full git commit hash.
var commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

// readManifest reads the copy's MANIFEST: its header, each key and its value, and the files it
// lists, each path and its SHA-256.
func readManifest(t *testing.T) (header, listed map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(mailCopy, "MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	header, listed = map[string]string{}, map[string]string{}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\n")
		if m := manifestLine.FindStringSubmatch(line); m != nil {
			listed[m[2]] = m[1]
		} else if key, value, ok := strings.Cut(line, " "); ok && key != "#" {
			header[key] = value
		}
	}
	return header, listed
}

// TestManifest: the copy of mail's fixtures and goldens is exactly the files its MANIFEST lists,
// each with the SHA-256 listed, and the MANIFEST names the tag and the commit it was copied from.
func TestManifest(t *testing.T) {
	header, listed := readManifest(t)
	if header["tag"] == "" || !commitHash.MatchString(header["commit"]) {
		t.Errorf("manifest pins tag %q at commit %q, want a tag and a full commit hash", header["tag"], header["commit"])
	}
	found := map[string]string{}
	for _, top := range []string{"fixtures", "golden"} {
		err := filepath.WalkDir(filepath.Join(mailCopy, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(mailCopy, path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(content)
			found[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(listed) == 0 {
		t.Fatal("the manifest lists no files")
	}
	for _, path := range slices.Sorted(maps.Keys(listed)) {
		if got, ok := found[path]; !ok {
			t.Errorf("%s: listed, not in the copy", path)
		} else if got != listed[path] {
			t.Errorf("%s: SHA-256 %s, the manifest's %s", path, got, listed[path])
		}
	}
	for _, path := range slices.Sorted(maps.Keys(found)) {
		if _, ok := listed[path]; !ok {
			t.Errorf("%s: in the copy, not listed", path)
		}
	}
}

// TestRealVersion: the mail the check's mise pins is the version whose tag the copy of its
// fixtures and goldens was taken from, so the goldens TestReal compares against are that mail's.
func TestRealVersion(t *testing.T) {
	tl, err := tool.NewExec(t.Context(), email.MailSpec())
	if err != nil {
		t.Fatalf("mail, which mise pins, run the check through mise run check: %v", err)
	}
	header, _ := readManifest(t)
	if got, want := tl.Version().String(), strings.TrimPrefix(header["tag"], "v"); got != want {
		t.Errorf("mail %s, the fixtures' tag %s: refresh them with mise run fixtures:mail", got, header["tag"])
	}
}

// realReader builds a Reader over the real mail the check's mise pins, found on PATH through the
// exec runner.
func realReader(t *testing.T) *email.Reader {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), email.MailSpec())
	if err != nil {
		t.Fatalf("mail, which mise pins, run the check through mise run check: %v", err)
	}
	r, err := email.NewReader(email.Config{Mail: tl})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// golden returns the golden mail ships for the fixture at rel, a path under fixtures/: its
// stdout's path and its stderr's, under golden/. The generated fixtures keep their directories;
// the public ones are named for their source and file.
func golden(rel string) (stdout, stderr string) {
	stem := strings.TrimSuffix(rel, filepath.Ext(rel))
	if !strings.HasPrefix(rel, "generated/") {
		dir, source := filepath.Split(filepath.Dir(stem))
		stem = filepath.Join(dir, source+"-"+filepath.Base(stem))
	}
	base := filepath.Join(mailCopy, "golden", stem)
	return base + ".jsonl", base + ".stderr"
}

// TestReal runs the real pinned mail over every .pst and .msg among the copied fixtures. Each
// read succeeds: its first record is the store, of Schema and the input's format, and it yields
// the golden's records, one per line, in order and of the same kinds, each of which marshals back
// to its line's fields; its warnings are the golden stderr's lines, mail's prefix taken off, none
// where mail ships no stderr golden. Between them the fixtures hold folders, messages, skipped
// records, warnings, and a .msg's nested messages.
func TestReal(t *testing.T) {
	r := realReader(t)
	root := filepath.Join(mailCopy, "fixtures")
	inputs := realInputs(t)
	kinds := map[string]bool{}
	warned, nested := false, false
	for _, rel := range inputs {
		t.Run(rel, func(t *testing.T) {
			stdoutPath, stderrPath := golden(rel)
			want := goldenLines(t, stdoutPath)
			wantWarnings := goldenWarnings(t, stderrPath)

			s := mustRead(t)(r.Read(t.Context(), email.Request{Input: filepath.Join(root, rel)}))
			recs, err := collect(s, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != len(want) {
				t.Fatalf("%d records, the golden's %d lines", len(recs), len(want))
			}
			store, ok := recs[0].(*email.Store)
			if !ok || store.Schema != email.Schema || store.Format != strings.TrimPrefix(filepath.Ext(rel), ".") {
				t.Errorf("first record %#v", recs[0])
			}
			for i, rec := range recs {
				kinds[rec.Line()] = true
				if _, ok := rec.(*email.UnknownRecord); ok {
					t.Errorf("record %d: unknown kind %s", i, rec.Line())
				}
				if m, ok := rec.(*email.Message); ok && hasNested(m) {
					nested = true
				}
				line := want[i]
				if kind := line["line"]; kind != rec.Line() {
					t.Errorf("record %d: kind %s, the golden's %v", i, rec.Line(), kind)
				}
				delete(line, "line")
				got := marshalled(t, rec)
				dropFalse(line)
				if !reflect.DeepEqual(got, line) {
					t.Errorf("record %d marshals to\n%v\nthe golden's line is\n%v", i, got, line)
				}
			}
			if got := s.Warnings(); !slices.Equal(got, wantWarnings) {
				t.Errorf("warnings %q, the golden's %q", got, wantWarnings)
			}
			if len(wantWarnings) > 0 {
				warned = true
			}
		})
	}
	for _, kind := range []string{"store", "folder", "message", "skipped"} {
		if !kinds[kind] {
			t.Errorf("no fixture gives a %s record", kind)
		}
	}
	if !warned || !nested {
		t.Errorf("warnings among the fixtures %t, nested messages %t; want both", warned, nested)
	}
}

// realInputs returns every .pst and .msg among the copied fixtures, each a path under fixtures/,
// and fails the test unless they are mail's 22.
func realInputs(t *testing.T) []string {
	t.Helper()
	var inputs []string
	root := filepath.Join(mailCopy, "fixtures")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ext := filepath.Ext(path); !d.IsDir() && (ext == ".pst" || ext == ".msg") {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			inputs = append(inputs, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 22 {
		t.Errorf("%d inputs, want mail's 22", len(inputs))
	}
	return inputs
}

// hasNested reports whether any of m's attachments nests a message.
func hasNested(m *email.Message) bool {
	return slices.ContainsFunc(m.Attachments, func(a email.Attachment) bool { return a.Message != nil })
}

// goldenLines reads the golden at path, one JSON object per line.
func goldenLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, len(data)+1)
	for scanner.Scan() {
		var line map[string]any
		if err := jsonv2.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// goldenWarnings reads the stderr golden at path, each line a warning, mail's prefix and the
// warning label taken off; nil when mail ships none.
func goldenWarnings(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var warnings []string
	for line := range strings.Lines(string(data)) {
		w, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "spike-ai-pipeline-mail: warning: ")
		if !ok {
			t.Fatalf("%s: not a warning: %q", path, line)
		}
		warnings = append(warnings, w)
	}
	return warnings
}

// marshalled returns rec marshalled through encoding/json/v2 and read back as a JSON object.
func marshalled(t *testing.T, rec email.Record) map[string]any {
	t.Helper()
	data, err := jsonv2.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := jsonv2.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

// dropFalse removes every false boolean from v, nested objects and arrays included: a record
// marshals a false boolean as none.
func dropFalse(v any) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if x == false {
				delete(v, k)
			} else {
				dropFalse(x)
			}
		}
	case []any:
		for _, x := range v {
			dropFalse(x)
		}
	}
}

// TestRealRefused: the real mail refuses an .ost, and a file that is neither a .pst nor a .msg,
// with exit code 3, and the error carries its reason without the input's path.
func TestRealRefused(t *testing.T) {
	r := realReader(t)
	// An .ost's header: a PST's, its client magic SO, padded to the header's size.
	ost := append([]byte("!BDN\x00\x00\x00\x00SO\x24\x00"), make([]byte, 552)...)
	cases := []struct {
		name    string
		content []byte
		reason  string
	}{
		{"mailbox.ost", ost, "an .ost, the offline cache of a server mailbox"},
		{"notes.txt", []byte("plain text, not a store"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), c.name)
			if err := os.WriteFile(path, c.content, 0o600); err != nil {
				t.Fatal(err)
			}
			recs, err := collect(r.Read(t.Context(), email.Request{Input: path}))
			var exit *email.ExitError
			if !errors.Is(err, email.ErrRefused) || !errors.As(err, &exit) {
				t.Fatalf("got %v, want a refusal", err)
			}
			if len(recs) != 0 {
				t.Errorf("records %v, want none", recs)
			}
			if exit.Message == "" || strings.Contains(exit.Message, path) || !strings.HasPrefix(exit.Message, c.reason) {
				t.Errorf("message %q: want mail's reason alone, beginning %q", exit.Message, c.reason)
			}
		})
	}
}
