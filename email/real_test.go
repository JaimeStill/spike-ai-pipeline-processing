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
	"strconv"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
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
	inputs, _ := realInputs(t)
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
			store, ok := recs[0].(*email.Store)
			if !ok || store.Schema != email.Schema || store.Format != strings.TrimPrefix(filepath.Ext(rel), ".") {
				t.Errorf("first record %#v", recs[0])
			}
			checkLines(t, recs, want, "the golden's")
			for _, rec := range recs {
				kinds[rec.Line()] = true
				if m, ok := rec.(*email.Message); ok && hasNested(m) {
					nested = true
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

// checkLines checks recs, a read's records, against want, mail's lines for the same input, whose
// lines its errors call them: there is one record per line, in order and of the line's kind, none
// of an unknown kind, and each marshals back to its line's fields, less "line" and any false
// boolean. A Conversation is a message line's: the line, decoded as a Message and read through
// Message.Conversation, marshals as the record does.
func checkLines(t *testing.T, recs []email.Record, want []map[string]any, whose string) {
	t.Helper()
	if len(recs) != len(want) {
		t.Fatalf("%d records, %s %d lines", len(recs), whose, len(want))
	}
	for i, rec := range recs {
		if _, ok := rec.(*email.UnknownRecord); ok {
			t.Errorf("record %d: unknown kind %s", i, rec.Line())
		}
		line := maps.Clone(want[i])
		kind := rec.Line()
		if _, ok := rec.(*email.Conversation); ok {
			kind = "message"
		}
		if line["line"] != kind {
			t.Errorf("record %d: kind %s, %s %v", i, rec.Line(), whose, line["line"])
		}
		delete(line, "line")
		if _, ok := rec.(*email.Conversation); ok {
			line = conversationLine(t, line)
		}
		got := marshalled(t, rec)
		dropFalse(line)
		if !reflect.DeepEqual(got, line) {
			t.Errorf("record %d marshals to\n%v\n%s line is\n%v", i, got, whose, line)
		}
	}
}

// mimeTruth is each MIME input's records, in order, as describe gives them, keyed by the input's
// path under testdata/. It is written from the inputs themselves, not from mail's output: an .eml
// is a store and its one message, an mbox a store and a message per "From " separator, numbered
// from 1 in file order; each message's attachments are its parts that aren't body text, each
// by_value but a message/rfc822 part, embedded_message, nested with its own subject. mbox.rlug's
// five splits are its messages one per file, each an .eml, without its separator line. No input
// holds a part mail can't read, so none gives a skipped record.
var mimeTruth = map[string][]string{
	"mail/fixtures/generated/eml/01-plain.eml":               {"store eml", "message"},
	"mail/fixtures/generated/eml/02-reply-latin1.eml":        {"store eml", "message"},
	"mail/fixtures/generated/eml/03-html-inline-image.eml":   {"store eml", "message [by_value]"},
	"mail/fixtures/generated/eml/04-attachment-cyrillic.eml": {"store eml", "message [by_value]"},
	"mail/fixtures/generated/eml/05-japanese.eml":            {"store eml", "message"},
	"mail/fixtures/generated/eml/06-chinese.eml":             {"store eml", "message"},
	"mail/fixtures/generated/eml/07-html-only-bcc.eml":       {"store eml", "message"},
	"mail/fixtures/generated/eml/08-forward-attached-message.eml": {
		"store eml", "message [by_value embedded_message(Notes on the engine (draft))]",
	},
	"mail/fixtures/mime/eml/09-threading.eml": {"store eml", "message"},
	"mail/fixtures/mime/nine.mboxrd": {
		"store mbox", "message 1", "message 2", "message 3 [by_value]", "message 4 [by_value]",
		"message 5", "message 6", "message 7",
		"message 8 [by_value embedded_message(Notes on the engine (draft))]", "message 9",
	},
	"mail/fixtures/mime/rlug/mbox.rlug": {
		"store mbox", "message 1", "message 2", "message 3", "message 4", "message 5",
	},
	"mail/fixtures/mime/rlug/mbox.rlug-0": {"store eml", "message"},
	"mail/fixtures/mime/rlug/mbox.rlug-1": {"store eml", "message"},
	"mail/fixtures/mime/rlug/mbox.rlug-2": {"store eml", "message"},
	"mail/fixtures/mime/rlug/mbox.rlug-3": {"store eml", "message"},
	"mail/fixtures/mime/rlug/mbox.rlug-4": {"store eml", "message"},
	"built/" + builtEML: {
		"store eml", "message [by_value by_value by_value by_value by_value]",
	},
}

// describe gives rec's kind, its ids, and its skips in a few words, for mimeTruth: a store's
// format; a message's id, if any, and each attachment's method, an embedded message's subject
// after it; a skipped record's kind, its message, and its id.
func describe(rec email.Record) string {
	switch rec := rec.(type) {
	case *email.Store:
		return "store " + rec.Format
	case *email.Message:
		s := "message"
		if rec.ID != 0 {
			s += " " + strconv.FormatUint(rec.ID, 10)
		}
		if len(rec.Attachments) > 0 {
			var methods []string
			for _, a := range rec.Attachments {
				m := a.Method
				if a.Message != nil {
					m += "(" + a.Message.Subject + ")"
				}
				methods = append(methods, m)
			}
			s += " [" + strings.Join(methods, " ") + "]"
		}
		return s
	case *email.Skipped:
		s := "skipped " + rec.Kind
		if rec.Message != 0 {
			s += " message " + strconv.FormatUint(rec.Message, 10)
		}
		if rec.ID != nil {
			s += " id " + strconv.FormatUint(*rec.ID, 10)
		}
		return s
	}
	return rec.Line()
}

// TestRealMIME runs the real pinned mail over every .eml and mbox among the inputs, for which mail
// ships no goldens. Each read succeeds and is checked twice: against mail's own stdout and stderr
// from a run of its own on the same input, each record marshalling back to its line and the
// warnings mail's; and against mimeTruth's records. Each is as a MIME store is: its store has
// no encoding, password, or display name; no message has a folder, an .eml's no id; and no
// message, nested ones included, has a class or a creation or modification time, every address
// is SMTP, and an embedded message is nested under its attachment record, routed route.Nested.
func TestRealMIME(t *testing.T) {
	r := realReader(t)
	_, inputs := realInputs(t)
	if got := slices.Sorted(maps.Keys(mimeTruth)); !slices.Equal(got, slices.Sorted(slices.Values(inputs))) {
		t.Errorf("mimeTruth covers %q, the inputs are %q", got, inputs)
	}
	for _, rel := range inputs {
		t.Run(rel, func(t *testing.T) {
			input, err := filepath.Abs(filepath.Join("testdata", rel))
			if err != nil {
				t.Fatal(err)
			}
			want, wantWarnings := mailOutput(t, input)

			s := mustRead(t)(r.Read(t.Context(), email.Request{Input: input}))
			recs, err := collect(s, nil)
			if err != nil {
				t.Fatal(err)
			}
			checkLines(t, recs, want, "mail's")
			if got := s.Warnings(); !slices.Equal(got, wantWarnings) {
				t.Errorf("warnings %q, mail's %q", got, wantWarnings)
			}
			var got []string
			for _, rec := range recs {
				got = append(got, describe(rec))
			}
			if want := mimeTruth[rel]; !slices.Equal(got, want) {
				t.Errorf("records %q, want %q", got, want)
			}
			checkMIME(t, recs)
		})
	}
}

// TestRealMboxOrdinals: an mbox message's id is its ordinal in the file, so an empty entry, a
// separator line with the next straight after it, skips a number, and a message mail can't read,
// one whose header block has no From or Date, is a skipped record of kind message whose id is its
// ordinal, with a warning naming it.
func TestRealMboxOrdinals(t *testing.T) {
	r := realReader(t)
	input := filepath.Join(t.TempDir(), "ordinals.mbox")
	content := "From a\nFrom: Ada <ada@example.org>\nSubject: one\n\nfirst\n\n" +
		"From b\n" +
		"From c\nFrom: Ada <ada@example.org>\nSubject: three\n\nthird\n\n" +
		"From d\nSubject: four\n\nno sender, no date\n"
	if err := os.WriteFile(input, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	want, wantWarnings := mailOutput(t, input)
	s := mustRead(t)(r.Read(t.Context(), email.Request{Input: input}))
	recs, err := collect(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkLines(t, recs, want, "mail's")
	var got []string
	for _, rec := range recs {
		got = append(got, describe(rec))
	}
	if want := []string{"store mbox", "message 1", "message 3", "skipped message id 4"}; !slices.Equal(got, want) {
		t.Errorf("records %q, want %q", got, want)
	}
	if got, want := s.Warnings(), []string{"message 4: skipped: it has no header block with a From or Date field"}; !slices.Equal(got, want) || !slices.Equal(got, wantWarnings) {
		t.Errorf("warnings %q, want %q, mail's %q", got, want, wantWarnings)
	}
	checkMIME(t, recs)
}

// mailOutput runs the real pinned mail on input itself, without the Reader, and returns its
// stdout's lines and its stderr's warnings, as goldenLines and goldenWarnings read a golden's.
func mailOutput(t *testing.T, input string) (lines []map[string]any, warnings []string) {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), email.MailSpec())
	if err != nil {
		t.Fatalf("mail, which mise pins, run the check through mise run check: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if err := tl.Run(t.Context(), tool.Invocation{Args: []string{input}, Stdout: &stdout, Stderr: &stderr}); err != nil {
		t.Fatalf("mail on %s: %v: %s", input, err, stderr.String())
	}
	return jsonLines(t, input, stdout.Bytes()), warningLines(t, input, stderr.Bytes())
}

// checkMIME checks recs, an .eml's or an mbox's records, for what a MIME store gives: a store of
// format eml or mbox with no encoding, password, or display name; no folder records; messages with
// no folder, an .eml's with no id and an mbox's each with one, each as checkMIMEMessage checks;
// and an .eml's skipped records naming no message.
func checkMIME(t *testing.T, recs []email.Record) {
	t.Helper()
	store, ok := recs[0].(*email.Store)
	if !ok || (store.Format != "eml" && store.Format != "mbox") || store.Encoding != "" || store.Password || store.DisplayName != "" {
		t.Fatalf("store %#v, want an eml or mbox store alone", recs[0])
	}
	for i, rec := range recs {
		if i == 0 {
			continue
		}
		switch rec := rec.(type) {
		case *email.Message:
			if rec.Folder != 0 || (store.Format == "eml") != (rec.ID == 0) {
				t.Errorf("record %d: message id %d, folder %d", i, rec.ID, rec.Folder)
			}
			checkMIMEMessage(t, rec)
		case *email.Skipped:
			if store.Format == "eml" && rec.Message != 0 {
				t.Errorf("record %d: skipped %+v names a message", i, rec)
			}
		default:
			t.Errorf("record %d: a %s record", i, rec.Line())
		}
	}
}

// checkMIMEMessage checks m and every message nested in it for what a MIME message gives: no
// class, no creation or modification time, every address of type SMTP, and each embedded
// message nested under its attachment record, which routes it route.Nested.
func checkMIMEMessage(t *testing.T, m *email.Message) {
	t.Helper()
	if m.Class != "" || !m.CreationTime.IsZero() || !m.ModificationTime.IsZero() {
		t.Errorf("%q: class %q, created %v, modified %v", m.Subject, m.Class, m.CreationTime, m.ModificationTime)
	}
	addrs := slices.Concat(m.To, m.CC, m.BCC)
	if m.Sender != (email.Address{}) {
		addrs = append(addrs, m.Sender)
	}
	for _, a := range addrs {
		if a.AddressType != "SMTP" {
			t.Errorf("%q: address %+v, want SMTP", m.Subject, a)
		}
	}
	for _, a := range m.Attachments {
		if (a.Method == "embedded_message") != (a.Message != nil) {
			t.Errorf("%q: attachment %q, method %s, nested %t", m.Subject, a.Filename, a.Method, a.Message != nil)
		}
		if a.Message == nil {
			continue
		}
		if rt, _, err := a.Route(); err != nil || rt != route.Nested {
			t.Errorf("%q: embedded message routed %v, %v", m.Subject, rt, err)
		}
		checkMIMEMessage(t, a.Message)
	}
}

// realInputs returns every input the real-binary tests read, and fails the test unless they are
// the 40. stores are the copied fixtures' .pst and .msg, mail's 23, each a path under
// fixtures/, which mail ships goldens for. mime are the 17 .eml and mbox, each a path under
// testdata/: the copied fixtures' .eml, the nine-message mboxrd, mbox.rlug and its five splits,
// and the built .msg's .eml source.
func realInputs(t *testing.T) (stores, mime []string) {
	t.Helper()
	root := filepath.Join(mailCopy, "fixtures")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch ext := filepath.Ext(path); {
		case ext == ".pst" || ext == ".msg":
			stores = append(stores, filepath.ToSlash(rel))
		case ext == ".eml" || ext == ".mboxrd" || strings.HasPrefix(d.Name(), "mbox."):
			mime = append(mime, "mail/fixtures/"+filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mime = append(mime, "built/"+builtEML)
	if len(stores) != 23 || len(mime) != 17 {
		t.Errorf("%d .pst and .msg, %d .eml and mbox, want 23 and 17, 40 in all", len(stores), len(mime))
	}
	return stores, mime
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
	return jsonLines(t, path, data)
}

// jsonLines reads data, mail's stdout from name, one JSON object per line.
func jsonLines(t *testing.T, name string, data []byte) []map[string]any {
	t.Helper()
	var lines []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, len(data)+1)
	for scanner.Scan() {
		var line map[string]any
		if err := jsonv2.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("%s: %v", name, err)
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
	return warningLines(t, path, data)
}

// warningLines reads data, mail's stderr from name, each line a warning, mail's prefix and the
// warning label taken off; nil for none.
func warningLines(t *testing.T, name string, data []byte) []string {
	t.Helper()
	var warnings []string
	for line := range strings.Lines(string(data)) {
		w, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "spike-ai-pipeline-mail: warning: ")
		if !ok {
			t.Fatalf("%s: not a warning: %q", name, line)
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

// conversationLine returns line, a message line's fields, as the Conversation Message.Conversation
// reads from it marshals; it fails the test for a line of no conversation class.
func conversationLine(t *testing.T, line map[string]any) map[string]any {
	t.Helper()
	data, err := jsonv2.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	var m email.Message
	if err := jsonv2.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	c, ok := m.Conversation()
	if !ok {
		t.Fatalf("a conversation from a line of class %q", m.Class)
	}
	return marshalled(t, c)
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

// TestRealRefused: the real mail refuses an .ost, and a file that is no mail, neither a .pst, a
// .msg, an .eml, nor an mbox, with exit code 3, and the error carries its reason without the
// input's path.
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
		{"notes.txt", []byte("plain text, not a store"), "neither a .pst, a .msg, an .eml nor an mbox"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), c.name)
			if err := os.WriteFile(path, c.content, 0o600); err != nil {
				t.Fatal(err)
			}
			recs, err := collect(r.Read(t.Context(), email.Request{Input: path}))
			var exit *email.ExitError
			if !errors.Is(err, email.ErrRefused) || !errors.As(err, &exit) || exit.Code != 3 {
				t.Fatalf("got %v, want a refusal, exit code 3", err)
			}
			if len(recs) != 0 {
				t.Errorf("records %v, want none", recs)
			}
			if strings.Contains(exit.Message, path) || !strings.HasPrefix(exit.Message, c.reason) {
				t.Errorf("message %q: want mail's reason alone, beginning %q", exit.Message, c.reason)
			}
		})
	}
}
