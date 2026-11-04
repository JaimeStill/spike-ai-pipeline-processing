//go:build linux

package email_test

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
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestNewReader: a stand-in at the minimum, or a build past it, is accepted; an older one fails at
// construction, whether the Tool's Spec or NewReader refuses it; a nil Tool is refused.
func TestNewReader(t *testing.T) {
	for _, version := range []string{"0.2.0", "0.2.0+0b6b5a4", "0.3.0"} {
		t.Run(version, func(t *testing.T) {
			installStandin(t, version)
			tl, err := tool.NewExec(t.Context(), email.MailSpec())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := email.NewReader(email.Config{Mail: tl}); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("older", func(t *testing.T) {
		installStandin(t, "0.1.0")
		_, err := tool.NewExec(t.Context(), email.MailSpec())
		if !errors.Is(err, tool.ErrTooOld) {
			t.Fatalf("spec's own minimum: got %v, want ErrTooOld", err)
		}
		spec := email.MailSpec()
		spec.Minimum = "0.0.1"
		tl, err := tool.NewExec(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		_, err = email.NewReader(email.Config{Mail: tl})
		var verr *tool.VersionError
		if !errors.Is(err, tool.ErrTooOld) || !errors.As(err, &verr) || verr.Found != "0.1.0" || verr.Minimum != email.MailMinimum {
			t.Fatalf("NewReader: got %v, want a VersionError for 0.1.0 under %s", err, email.MailMinimum)
		}
	})
	t.Run("nil", func(t *testing.T) {
		if _, err := email.NewReader(email.Config{}); err == nil {
			t.Fatal("want an error")
		}
	})
}

// fullLines are a .pst's lines with every field of schema "1" set, an unknown kind among them.
const fullLines = `{"line":"store","schema":"1","format":"pst","encoding":"unicode","password":true,"display_name":"Personal Folders"}
{"line":"folder","id":32802,"path":["Top"],"display_name":"Top"}
{"line":"folder","id":32898,"parent":32802,"path":["Top","Inbox"],"display_name":"Inbox","container_class":"IPF.Note"}
{"line":"message","id":2097188,"folder":32898,"class":"IPM.Note","subject":"Notes","sender":{"name":"Ada","address":"ada@example.org","address_type":"SMTP"},"to":[{"name":"Charles","address":"charles@example.org","address_type":"SMTP"},{"address":"engine@example.org"}],"cc":[{"address":"mary@example.org"}],"bcc":[{"address":"x@example.org"}],"submit_time":"2026-01-12T14:15:00Z","delivery_time":"2026-01-12T14:16:30Z","creation_time":"2026-01-12T14:17:00Z","modification_time":"2026-01-12T14:18:00Z","importance":"high","sensitivity":"private","transport_headers":"Received: x\r\n","message_id":"<b@example.org>","in_reply_to":"<a@example.org>","references":"<a@example.org>","conversation_topic":"Notes","conversation_index":"0101cf32","body":{"text":"plain","html":"<p>html</p>","rtf":"{\\rtf1}","derived":{"html":"<p>from rtf</p>","markdown":"plain"}},"attachments":[{"file":"1bd6.pdf","filename":"a.pdf","mime_type":"application/pdf","content_id":"part1","inline":true,"method":"by_value","size":0},{"filename":"Fwd","inline":false,"method":"embedded_message","message":{"class":"IPM.Note","subject":"Inner","attachments":[{"filename":"deep.txt","inline":false,"method":"by_value","size":4}]}}]}
{"line":"skipped","kind":"attachment","folder":32898,"message":2097188,"id":0,"reason":"broken"}
{"line":"skipped","kind":"contents","folder":32802,"reason":"unreadable table"}
{"line":"calendar","when":"later"}
`

// TestRead: the stand-in's lines are yielded in its order, each decoded into its typed record,
// every field of schema "1" set; a line of an unknown kind is kept with its JSON. mail runs once,
// on the input made absolute.
func TestRead(t *testing.T) {
	r, runs := standinReader(t)
	t.Setenv(stdoutEnv, fullLines)
	t.Chdir(t.TempDir())
	recs, err := collect(r.Read(t.Context(), email.Request{Input: "-store.pst"}))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, rec := range recs {
		kinds = append(kinds, rec.Line())
	}
	if want := []string{"store", "folder", "folder", "message", "skipped", "skipped", "calendar"}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds %q, want %q", kinds, want)
	}
	if got, want := recs[0], (&email.Store{Schema: "1", Format: "pst", Encoding: "unicode", Password: true, DisplayName: "Personal Folders"}); !reflect.DeepEqual(got, want) {
		t.Errorf("store %+v, want %+v", got, want)
	}
	if got, want := recs[2], (&email.Folder{ID: 32898, Parent: 32802, Path: []string{"Top", "Inbox"}, DisplayName: "Inbox", ContainerClass: "IPF.Note"}); !reflect.DeepEqual(got, want) {
		t.Errorf("folder %+v, want %+v", got, want)
	}
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	zero, four := int64(0), int64(4)
	want := &email.Message{
		ID: 2097188, Folder: 32898, Class: "IPM.Note", Subject: "Notes",
		Sender: email.Address{Name: "Ada", Address: "ada@example.org", AddressType: "SMTP"},
		To: []email.Address{
			{Name: "Charles", Address: "charles@example.org", AddressType: "SMTP"},
			{Address: "engine@example.org"},
		},
		CC:               []email.Address{{Address: "mary@example.org"}},
		BCC:              []email.Address{{Address: "x@example.org"}},
		SubmitTime:       at("2026-01-12T14:15:00Z"),
		DeliveryTime:     at("2026-01-12T14:16:30Z"),
		CreationTime:     at("2026-01-12T14:17:00Z"),
		ModificationTime: at("2026-01-12T14:18:00Z"),
		Importance:       "high", Sensitivity: "private",
		TransportHeaders: "Received: x\r\n",
		MessageID:        "<b@example.org>", InReplyTo: "<a@example.org>", References: "<a@example.org>",
		ConversationTopic: "Notes", ConversationIndex: "0101cf32",
		Body: email.Body{Text: "plain", HTML: "<p>html</p>", RTF: `{\rtf1}`,
			Derived: email.Derived{HTML: "<p>from rtf</p>", Markdown: "plain"}},
		Attachments: []email.Attachment{
			{File: "1bd6.pdf", Filename: "a.pdf", MimeType: "application/pdf", ContentID: "part1", Inline: true, Method: "by_value", Size: &zero},
			{Filename: "Fwd", Method: "embedded_message", Message: &email.Message{
				Class: "IPM.Note", Subject: "Inner",
				Attachments: []email.Attachment{{Filename: "deep.txt", Method: "by_value", Size: &four}},
			}},
		},
	}
	if got := recs[3]; !reflect.DeepEqual(got, want) {
		t.Errorf("message %+v\nwant %+v", got, want)
	}
	id := uint64(0)
	if got, want := recs[4], (&email.Skipped{Kind: "attachment", Folder: 32898, Message: 2097188, ID: &id, Reason: "broken"}); !reflect.DeepEqual(got, want) {
		t.Errorf("skipped %+v, want %+v", got, want)
	}
	if got, want := recs[5], (&email.Skipped{Kind: "contents", Folder: 32802, Reason: "unreadable table"}); !reflect.DeepEqual(got, want) {
		t.Errorf("skipped table %+v, want %+v", got, want)
	}
	unknown, ok := recs[6].(*email.UnknownRecord)
	if !ok || string(unknown.Raw) != `{"line":"calendar","when":"later"}` {
		t.Errorf("unknown record %#v", recs[6])
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runs(), [][]string{{filepath.Join(dir, "-store.pst")}}; !reflect.DeepEqual(got, want) {
		t.Errorf("runs %q, want %q", got, want)
	}
}

// TestReadMIMESkipped: an .eml's and an mbox's skipped records, which none of the real inputs
// gives, decode as mail writes them: an .eml's skipped attachment names no message, an mbox's
// names its message's ordinal, and an mbox's skipped message has its ordinal for its id.
func TestReadMIMESkipped(t *testing.T) {
	one, two, three := uint64(1), uint64(2), uint64(3)
	cases := []struct {
		name, lines string
		want        []email.Record
	}{
		{"eml", `{"line":"store","schema":"1","format":"eml"}
{"line":"message","subject":"Notes"}
{"line":"skipped","kind":"attachment","id":1,"reason":"broken"}
`, []email.Record{
			&email.Store{Schema: "1", Format: "eml"},
			&email.Message{Subject: "Notes"},
			&email.Skipped{Kind: "attachment", ID: &one, Reason: "broken"},
		}},
		{"mbox", `{"line":"store","schema":"1","format":"mbox"}
{"line":"message","id":1,"subject":"Notes"}
{"line":"skipped","kind":"attachment","message":1,"id":2,"reason":"broken"}
{"line":"skipped","kind":"message","id":3,"reason":"it has no header block with a From or Date field"}
`, []email.Record{
			&email.Store{Schema: "1", Format: "mbox"},
			&email.Message{ID: 1, Subject: "Notes"},
			&email.Skipped{Kind: "attachment", Message: 1, ID: &two, Reason: "broken"},
			&email.Skipped{Kind: "message", ID: &three, Reason: "it has no header block with a From or Date field"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := standinReader(t)
			t.Setenv(stdoutEnv, c.lines)
			recs, err := collect(r.Read(t.Context(), email.Request{Input: "mail." + c.name}))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(recs, c.want) {
				t.Errorf("records %+v\nwant %+v", recs, c.want)
			}
		})
	}
}

// TestReadWarnings: a read whose stderr holds warnings, with records skipped, succeeds; each
// warning line is one warning, mail's prefix taken off, in order.
func TestReadWarnings(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(stdoutEnv, `{"line":"store","schema":"1","format":"pst"}
{"line":"skipped","kind":"message","folder":32898,"id":2097188,"reason":"Missing Sub-Node Tree on message"}
`)
	t.Setenv(stderrEnv, "spike-ai-pipeline-mail: warning: /Top/Contacts, message 2097188: skipped: Missing Sub-Node Tree on message\n"+
		"spike-ai-pipeline-mail: warning: message, attachment 2: an embedded message\n")
	s := mustRead(t)(r.Read(t.Context(), email.Request{Input: "store.pst"}))
	recs, err := collect(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1].Line() != "skipped" {
		t.Fatalf("records %v", recs)
	}
	want := []string{
		"/Top/Contacts, message 2097188: skipped: Missing Sub-Node Tree on message",
		"message, attachment 2: an embedded message",
	}
	if got := s.Warnings(); !slices.Equal(got, want) {
		t.Errorf("warnings %q, want %q", got, want)
	}
}

// TestReadExit: exit codes 1 to 4 are an *ExitError matching the code's error, carrying mail's
// message without its prefix, label, and the input's path, the usage after a usage error's message left out, and
// unwrapping to the Tool's *tool.ExitError; a warning before the error is still a warning.
func TestReadExit(t *testing.T) {
	cases := []struct {
		code   int
		want   error
		stderr string
		msg    string
	}{
		{1, email.ErrNotWritten, "spike-ai-pipeline-mail: error: writing stdout: Broken pipe\n", "writing stdout: Broken pipe"},
		{2, email.ErrUsage, "spike-ai-pipeline-mail: error: unexpected argument '--bogus'\nUsage: spike-ai-pipeline-mail [--attachments <dir>] <input>\n", "unexpected argument '--bogus'"},
		{3, email.ErrRefused, "spike-ai-pipeline-mail: error: {path}: an .ost, the offline cache of a server mailbox: export the mailbox from Outlook as a .pst\n", "an .ost, the offline cache of a server mailbox: export the mailbox from Outlook as a .pst"},
		{4, email.ErrFailed, "spike-ai-pipeline-mail: warning: store: odd\nspike-ai-pipeline-mail: error: internal\n", "internal"},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.code), func(t *testing.T) {
			r, _ := standinReader(t)
			t.Setenv(stdoutEnv, "")
			t.Setenv(stderrEnv, c.stderr)
			t.Setenv(exitEnv, strconv.Itoa(c.code))
			s := mustRead(t)(r.Read(t.Context(), email.Request{Input: "in.pst"}))
			recs, err := collect(s, nil)
			if len(recs) != 0 {
				t.Errorf("records %v, want none", recs)
			}
			var exit *email.ExitError
			var toolExit *tool.ExitError
			if !errors.Is(err, c.want) || !errors.As(err, &exit) || !errors.As(err, &toolExit) {
				t.Fatalf("got %v, want an ExitError matching %v", err, c.want)
			}
			for _, other := range []error{email.ErrNotWritten, email.ErrUsage, email.ErrRefused, email.ErrFailed} {
				if other != c.want && errors.Is(err, other) {
					t.Errorf("matches %v too", other)
				}
			}
			if exit.Code != c.code || exit.Message != c.msg {
				t.Errorf("code %d, message %q; want %d, %q", exit.Code, exit.Message, c.code, c.msg)
			}
			if c.code == 4 && !slices.Equal(s.Warnings(), []string{"store: odd"}) {
				t.Errorf("warnings %q", s.Warnings())
			}
		})
	}
}

// TestReadExitOther: an exit code outside 1 to 4 is no *ExitError of the package's, but carries
// mail's message.
func TestReadExitOther(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(stdoutEnv, "")
	t.Setenv(stderrEnv, "spike-ai-pipeline-mail: error: odd\n")
	t.Setenv(exitEnv, "9")
	_, err := collect(r.Read(t.Context(), email.Request{Input: "in.pst"}))
	var exit *email.ExitError
	var toolExit *tool.ExitError
	if errors.As(err, &exit) || !errors.As(err, &toolExit) || toolExit.Code != 9 || !strings.Contains(err.Error(), "odd") {
		t.Fatalf("got %v", err)
	}
}

// TestReadSchema: a store line of another schema, or of none, is refused with ErrSchema naming
// it, and nothing is yielded; so is output that doesn't begin with the store line.
func TestReadSchema(t *testing.T) {
	cases := []struct {
		name, stdout, want string
	}{
		{"foreign", `{"line":"store","schema":"2","format":"pst"}` + "\n" + `{"line":"folder","id":1}` + "\n", `"2"`},
		{"none", `{"line":"store","format":"pst"}` + "\n", "has none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := standinReader(t)
			t.Setenv(stdoutEnv, c.stdout)
			recs, err := collect(r.Read(t.Context(), email.Request{Input: "in.pst"}))
			if !errors.Is(err, email.ErrSchema) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want ErrSchema naming %s", err, c.want)
			}
			if len(recs) != 0 {
				t.Errorf("records %v, want none", recs)
			}
		})
	}
}

// TestReadMalformed: output that isn't mail's lines is an error, whatever was yielded before it.
func TestReadMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"not store":    `{"line":"folder","id":1}` + "\n",
		"second store": standinLines + `{"line":"store","schema":"1"}` + "\n",
		"no line":      `{"line":"store","schema":"1"}` + "\n" + `{"id":1}` + "\n",
		"not JSON":     `{"line":"store","schema":"1"}` + "\nnot JSON\n",
		"cut short":    `{"line":"store","schema":"1"}` + "\n" + `{"line":"folder",`,
		"wrong type":   `{"line":"store","schema":"1"}` + "\n" + `{"line":"folder","id":"x"}` + "\n",
	}
	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := standinReader(t)
			t.Setenv(stdoutEnv, stdout)
			if _, err := collect(r.Read(t.Context(), email.Request{Input: "in.pst"})); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// TestReadCutShortExit: output cut short by a mail that then failed is mail's exit error.
func TestReadCutShortExit(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(stdoutEnv, `{"line":"store","schema":"1"}`+"\n"+`{"line":"folder",`)
	t.Setenv(stderrEnv, "spike-ai-pipeline-mail: error: panicked\n")
	t.Setenv(exitEnv, "4")
	recs, err := collect(r.Read(t.Context(), email.Request{Input: "in.pst"}))
	if !errors.Is(err, email.ErrFailed) || len(recs) != 1 {
		t.Fatalf("got %v after %d records, want ErrFailed after the store", err, len(recs))
	}
}

// TestReadCancel: records are yielded while mail runs, and a read whose context is cancelled
// while mail runs stops it and returns an error matching context.Canceled, never an ExitError,
// well before mail would have exited.
func TestReadCancel(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(sleepEnv, "1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Now()
	var recs []email.Record
	var err error
	for rec, rerr := range mustRead(t)(r.Read(ctx, email.Request{Input: "in.msg"})).Records() {
		if rerr != nil {
			err = rerr
			break
		}
		recs = append(recs, rec)
		if len(recs) == 2 {
			cancel()
		}
	}
	var exit *email.ExitError
	if !errors.Is(err, context.Canceled) || errors.As(err, &exit) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if len(recs) != 2 {
		t.Errorf("%d records before the cancel, want 2", len(recs))
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("took %v", d)
	}
}

// TestReadDeadline: a read whose deadline passes while mail runs returns an error matching
// context.DeadlineExceeded.
func TestReadDeadline(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(sleepEnv, "1")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	recs, err := collect(r.Read(ctx, email.Request{Input: "in.msg"}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded", err)
	}
	if len(recs) != 2 {
		t.Errorf("%d records, want 2", len(recs))
	}
}

// TestReadStop: a loop that stops early stops mail, without waiting for it to exit, and nothing
// more is yielded.
func TestReadStop(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(sleepEnv, "1")
	start := time.Now()
	n := 0
	for _, err := range mustRead(t)(r.Read(t.Context(), email.Request{Input: "in.msg"})).Records() {
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

// TestReadOnce: a Stream's records are read once; ranging again yields an error and runs nothing.
// A cancelled context runs nothing.
func TestReadOnce(t *testing.T) {
	r, runs := standinReader(t)
	s := mustRead(t)(r.Read(t.Context(), email.Request{Input: "in.msg"}))
	if _, err := collect(s, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(s, nil); err == nil {
		t.Error("second range: want an error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collect(r.Read(ctx, email.Request{Input: "in.msg"})); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: got %v", err)
	}
	if got := len(runs()); got != 1 {
		t.Errorf("%d runs, want 1", got)
	}
}

// TestReadRefused: Read refuses an invalid request at once, with an error matching ErrRequest and
// no Stream: an empty Input, and, from a working directory that has been removed, a relative
// Input or Attachments, which can't be made absolute. mail never runs; an absolute Input with no
// Attachments is still read from there.
func TestReadRefused(t *testing.T) {
	r, runs := standinReader(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(t.TempDir(), "in.msg")
	cases := map[string]email.Request{
		"no input":             {Attachments: "att"},
		"relative input":       {Input: "in.msg"},
		"relative attachments": {Input: abs, Attachments: "att"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := r.Read(t.Context(), req)
			if !errors.Is(err, email.ErrRequest) || s != nil {
				t.Errorf("got %v, %v; want ErrRequest and no Stream", s, err)
			}
		})
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("runs %q, want none", got)
	}
	if _, err := collect(r.Read(t.Context(), email.Request{Input: abs})); err != nil {
		t.Errorf("absolute input: %v", err)
	}
}
