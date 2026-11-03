//go:build linux

package email_test

import (
	"errors"
	"iter"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
)

// render writes threads as text, the form the tests' expected trees are written in: roots
// separated by spaces, each a message's store id or a placeholder's "*" and the Message-ID it
// stands for, its children after it in brackets.
func render(threads []*email.Thread) string {
	var b strings.Builder
	var write func([]*email.Thread)
	write = func(list []*email.Thread) {
		for i, t := range list {
			if i > 0 {
				b.WriteString(" ")
			}
			if t.Placeholder {
				b.WriteString("*" + t.MessageID)
			} else {
				b.WriteString(strconv.FormatUint(t.ID, 10))
			}
			if len(t.Children) > 0 {
				b.WriteString("[")
				write(t.Children)
				b.WriteString("]")
			}
		}
	}
	write(threads)
	return b.String()
}

// day is the date the constructed messages are sent on, each a minute after it per its store id
// unless a test says otherwise.
var day = time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)

// msg returns a message with store id id, sent id minutes after day, and the threading fields
// given: its Message-ID, In-Reply-To, and References.
func msg(id uint64, messageID, inReplyTo, references string) *email.Message {
	return &email.Message{
		ID:         id,
		SubmitTime: day.Add(time.Duration(id) * time.Minute),
		MessageID:  messageID,
		InReplyTo:  inReplyTo,
		References: references,
	}
}

// subj returns a message with store id id, sent id minutes after day, with subject and no
// threading fields.
func subj(id uint64, subject string) *email.Message {
	m := msg(id, "", "", "")
	m.Subject = subject
	return m
}

// indexed returns a message with store id id, sent id minutes after day, whose conversation index
// is index, and no other threading field.
func indexed(id uint64, index string) *email.Message {
	m := msg(id, "", "", "")
	m.ConversationIndex = index
	return m
}

// threadOf returns the rendered threads of msgs, added in order to a Threader.
func threadOf(msgs ...*email.Message) string {
	var t email.Threader
	for _, m := range msgs {
		t.Add(m)
	}
	return render(t.Threads())
}

// TestThreadsReferences: RFC 5256's links by Message-ID. References chain their IDs parent to
// child, the last the message's parent; without References, In-Reply-To's first valid ID is the
// parent. A Message-ID no message has is a placeholder, kept only where it holds several roots
// together; one a later message also has belongs to the first; a message without a valid one is
// linked by its references alone.
func TestThreadsReferences(t *testing.T) {
	cases := []struct {
		name string
		msgs []*email.Message
		want string
	}{
		{
			name: "chain",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "<b@x>", "<a@x>", "<a@x>"),
				msg(3, "<c@x>", "<b@x>", "<a@x> <b@x>"),
			},
			want: "1[2[3]]",
		},
		{
			// The References chain links a to b even though b's own message names no parent; b
			// is read before c, so c's chain gives it its parent.
			name: "chain links earlier messages",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "<b@x>", "", ""),
				msg(3, "<c@x>", "", "<a@x> <b@x>"),
			},
			want: "1[2[3]]",
		},
		{
			// A message with no references has no parent, whatever an earlier message's
			// References gave it (step 1.B).
			name: "no references breaks a link",
			msgs: []*email.Message{
				msg(3, "<c@x>", "", "<a@x> <b@x>"),
				msg(1, "<a@x>", "", ""),
				msg(2, "<b@x>", "", ""),
			},
			want: "1 2[3]",
		},
		{
			name: "in-reply-to",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "<b@x>", "<a@x> (sent by Ada <ada@x>)", ""),
			},
			want: "1[2]",
		},
		{
			name: "references over in-reply-to",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "<b@x>", "", ""),
				msg(3, "<c@x>", "<a@x>", "<b@x>"),
			},
			want: "1 2[3]",
		},
		{
			name: "missing parent, two children",
			msgs: []*email.Message{
				msg(1, "<b@x>", "", "<gone@x>"),
				msg(2, "<c@x>", "", "<gone@x>"),
			},
			want: "*<gone@x>[1 2]",
		},
		{
			name: "missing parent, one child",
			msgs: []*email.Message{
				msg(1, "<b@x>", "", "<gone@x>"),
			},
			want: "1",
		},
		{
			name: "missing ancestors in a chain",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "<d@x>", "", "<a@x> <gone@x> <gone2@x>"),
			},
			want: "1[2]",
		},
		{
			name: "missing root, nested placeholders",
			msgs: []*email.Message{
				msg(1, "<b@x>", "", "<root@x> <mid@x>"),
				msg(2, "<c@x>", "", "<root@x> <mid@x>"),
				msg(3, "<d@x>", "", "<root@x> <other@x>"),
			},
			want: "*<root@x>[1 2 3]",
		},
		{
			name: "duplicate message-id",
			msgs: []*email.Message{
				msg(2, "<a@x>", "", ""),
				msg(1, "<a@x>", "", ""),
				msg(3, "<r@x>", "", "<a@x>"),
			},
			want: "1 2[3]",
		},
		{
			name: "without message-id",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "", "", "<a@x>"),
				msg(3, "not-an-id", "", "<a@x>"),
				msg(4, "", "", ""),
				msg(5, "", "", ""),
			},
			want: "1[2 3] 4 5",
		},
		{
			name: "invalid references",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "<b@x>", "<a@x>", "<no-at-sign>"),
			},
			want: "1[2]",
		},
		{
			name: "without angle brackets",
			msgs: []*email.Message{
				msg(1, "a@x", "", ""),
				msg(2, "b@x", "a@x", ""),
			},
			want: "1[2]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := threadOf(c.msgs...); got != c.want {
				t.Errorf("threads %s, want %s", got, c.want)
			}
		})
	}
}

// TestThreadsLoops: a link that would make a loop isn't made, whether two messages refer to each
// other, a References field repeats an ID, or a message refers to itself.
func TestThreadsLoops(t *testing.T) {
	cases := []struct {
		name string
		msgs []*email.Message
		want string
	}{
		{
			// 1 is made 2's child; 2's reference to 1 would close the loop.
			name: "each other",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", "<b@x>"),
				msg(2, "<b@x>", "", "<a@x>"),
			},
			want: "2[1]",
		},
		{
			// a is b's parent; b to a would loop, so 3 sits under a.
			name: "repeated reference",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", ""),
				msg(2, "<b@x>", "", "<a@x>"),
				msg(3, "<c@x>", "", "<a@x> <b@x> <a@x>"),
			},
			want: "1[2 3]",
		},
		{
			name: "itself",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", "<a@x>"),
			},
			want: "1",
		},
		{
			name: "longer loop",
			msgs: []*email.Message{
				msg(1, "<a@x>", "", "<c@x>"),
				msg(2, "<b@x>", "", "<a@x>"),
				msg(3, "<c@x>", "", "<b@x>"),
			},
			want: "3[1[2]]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := threadOf(c.msgs...); got != c.want {
				t.Errorf("threads %s, want %s", got, c.want)
			}
		})
	}
}

// TestThreadsSubjects: roots whose base subjects match are gathered, RFC 5256's step 5, the base
// subject as its section 2.1 extracts it, compared with ASCII letters' case folded. A reply or
// forward goes under the message it shares a subject with; two messages that aren't, or two that
// are, go under a placeholder; a placeholder takes in the others. An empty base subject gathers
// nothing.
func TestThreadsSubjects(t *testing.T) {
	cases := []struct {
		name string
		msgs []*email.Message
		want string
	}{
		{
			name: "reply",
			msgs: []*email.Message{subj(1, "Lunch plans"), subj(2, "Re: Lunch plans")},
			want: "1[2]",
		},
		{
			name: "reply first",
			msgs: []*email.Message{subj(1, "RE: Lunch plans"), subj(2, "Lunch plans")},
			want: "2[1]",
		},
		{
			name: "neither a reply",
			msgs: []*email.Message{subj(1, "Budget"), subj(2, "budget")},
			want: "*[1 2]",
		},
		{
			name: "both replies",
			msgs: []*email.Message{subj(1, "Re: Status"), subj(2, "Fwd: Status")},
			want: "*[1 2]",
		},
		{
			name: "three alike",
			msgs: []*email.Message{subj(1, "Budget"), subj(2, "Budget"), subj(3, "Budget")},
			want: "*[1 2 3]",
		},
		{
			name: "placeholder takes the others",
			msgs: []*email.Message{
				func() *email.Message { m := msg(1, "<k@x>", "", "<z@x>"); m.Subject = "Re: Plan"; return m }(),
				func() *email.Message { m := msg(2, "<l@x>", "", "<z@x>"); m.Subject = "Re: Plan"; return m }(),
				subj(0, "Plan"),
				subj(3, "Re: Plan"),
			},
			want: "*<z@x>[0 1 2 3]",
		},
		{
			name: "empty base subject",
			msgs: []*email.Message{subj(1, "Re:"), subj(2, "Re: "), subj(3, "")},
			want: "1 2 3",
		},
		{
			name: "threaded and gathered",
			msgs: []*email.Message{
				func() *email.Message { m := msg(1, "<a@x>", "", ""); m.Subject = "Report"; return m }(),
				func() *email.Message { m := msg(2, "<b@x>", "", "<a@x>"); m.Subject = "Re: Report"; return m }(),
				subj(3, "Re: Report"),
			},
			want: "1[2 3]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := threadOf(c.msgs...); got != c.want {
				t.Errorf("threads %s, want %s", got, c.want)
			}
		})
	}
}

// TestThreadsBaseSubject: each subject is gathered with "Notes", sent before it, by RFC 5256's
// base subject extraction: under it when the extraction removed a reply or forward marker, under
// a placeholder with it otherwise. A subject whose base differs isn't gathered.
func TestThreadsBaseSubject(t *testing.T) {
	const reply, alike, apart = "1[2]", "*[1 2]", "1 2"
	cases := []struct {
		subject string
		want    string
	}{
		{"Notes", alike},
		{"NOTES", alike},
		{"  Notes  ", alike},
		{"Notes\t", alike},
		{"Re: Notes", reply},
		{"re:Notes", reply},
		{"RE : Notes", reply},
		{"Fw: Notes", reply},
		{"FWD: Notes", reply},
		{"Re: Re: Fwd: Notes", reply},
		{"Re[2]: Notes", reply},
		{"Re [2]: Notes", reply},
		{"[list] Re: Notes", reply},
		{"Re: [list] Notes", reply},
		{"[list] [other] Notes", alike},
		{"[list] Notes", alike},
		{"Notes (fwd)", reply},
		{"Notes (FWD) ", reply},
		{"[Fwd: Notes]", reply},
		{"[fwd: Re: Notes]", reply},
		{"Re: [Fwd: [list] Notes (fwd)]", reply},
		{"=?utf-8?q?Re=3A_Notes?=", reply},
		{"Re:  Notes", reply},
		{"Notes again", apart},
		{"Fwd: Notes (draft)", apart},
		{"[Notes]", apart},
		{"Notes:", apart},
		{"Ré: Notes", apart},
	}
	for _, c := range cases {
		t.Run(c.subject, func(t *testing.T) {
			if got := threadOf(subj(1, "Notes"), subj(2, c.subject)); got != c.want {
				t.Errorf("threads %s, want %s", got, c.want)
			}
		})
	}
	// A subject that is a subj-blob alone keeps it as its base.
	if got := threadOf(subj(1, "[PATCH]"), subj(2, "Re: [PATCH]")); got != reply {
		t.Errorf("[PATCH]: threads %s, want %s", got, reply)
	}
}

// The conversation indexes of TestThreadsIndex: a 22-byte header, then 5-byte child blocks.
const (
	header  = "01d321c5c2dc64ee4b2f50da41d68f42001487a4d229"
	block1  = "0000049f70"
	block2  = "00000b50b0"
	header2 = "01d6c441ee0a7d408d320c2c40f6ac88aae17faebcda"
)

// TestThreadsIndex: a message whose headers name no parent takes as parent the message whose
// conversation index is its own less its last 5-byte child block. A parent the store doesn't
// hold is a placeholder, as a missing Message-ID is, kept only where it holds several roots
// together. An index shorter than its 22-byte header, or not a header and whole child blocks,
// or not hex, is ignored. Headers that name a parent take precedence.
func TestThreadsIndex(t *testing.T) {
	cases := []struct {
		name string
		msgs []*email.Message
		want string
	}{
		{
			name: "parent found",
			msgs: []*email.Message{indexed(1, header), indexed(2, header+block1)},
			want: "1[2]",
		},
		{
			name: "child read first",
			msgs: []*email.Message{indexed(2, header+block1), indexed(1, header)},
			want: "1[2]",
		},
		{
			name: "grandchildren",
			msgs: []*email.Message{
				indexed(1, header),
				indexed(2, header+block1),
				indexed(3, header+block1+block2),
				indexed(4, header+block2),
			},
			want: "1[2[3] 4]",
		},
		{
			name: "upper case hex",
			msgs: []*email.Message{indexed(1, header), indexed(2, strings.ToUpper(header+block1))},
			want: "1[2]",
		},
		{
			name: "parent missing, two children",
			msgs: []*email.Message{indexed(1, header2+block1), indexed(2, header2+block2)},
			want: "*[1 2]",
		},
		{
			name: "parent missing, one child",
			msgs: []*email.Message{indexed(1, header2+block1)},
			want: "1",
		},
		{
			name: "grandparent missing",
			msgs: []*email.Message{indexed(1, header2+block1), indexed(2, header2+block1+block2)},
			want: "1[2]",
		},
		{
			name: "shorter than the header",
			msgs: []*email.Message{indexed(1, header[:24]), indexed(2, header[:24]+block1)},
			want: "1 2",
		},
		{
			name: "partial child block",
			msgs: []*email.Message{indexed(1, header), indexed(2, header+block1[:8])},
			want: "1 2",
		},
		{
			name: "partial child block in the parent",
			msgs: []*email.Message{indexed(1, header+"00"), indexed(2, header+"00"+block1)},
			want: "1 2",
		},
		{
			name: "not hex",
			msgs: []*email.Message{indexed(1, header), indexed(2, header+"zz00000000")},
			want: "1 2",
		},
		{
			name: "odd hex digits",
			msgs: []*email.Message{indexed(1, header), indexed(2, header+block1+"0")},
			want: "1 2",
		},
		{
			name: "headers take precedence",
			msgs: []*email.Message{
				indexed(1, header),
				msg(2, "<o@x>", "", ""),
				func() *email.Message {
					m := msg(3, "<c@x>", "", "<o@x>")
					m.ConversationIndex = header + block1
					return m
				}(),
			},
			want: "1 2[3]",
		},
		{
			name: "missing header parent over the index",
			msgs: []*email.Message{
				indexed(1, header),
				func() *email.Message {
					m := msg(2, "<c@x>", "<gone@x>", "")
					m.ConversationIndex = header + block1
					return m
				}(),
			},
			want: "1 2",
		},
		{
			name: "duplicate index",
			msgs: []*email.Message{indexed(2, header), indexed(1, header), indexed(3, header+block1)},
			want: "1 2[3]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := threadOf(c.msgs...); got != c.want {
				t.Errorf("threads %s, want %s", got, c.want)
			}
		})
	}
}

// TestThreadsOrder: roots and siblings are ordered by sent date, the submit time, else the
// delivery time, else none, first; then by store id; then by order in the read. A placeholder
// sorts by its first child. Building again, or from the same messages again, gives the same
// threads.
func TestThreadsOrder(t *testing.T) {
	at := func(m *email.Message, submit, delivery time.Time) *email.Message {
		m.SubmitTime, m.DeliveryTime = submit, delivery
		return m
	}
	msgs := []*email.Message{
		at(msg(30, "<a@x>", "", ""), day, time.Time{}),
		at(msg(10, "<b@x>", "", ""), day, time.Time{}),
		at(msg(20, "<c@x>", "", ""), time.Time{}, day.Add(-time.Hour)),
		at(msg(5, "<d@x>", "", ""), time.Time{}, time.Time{}),
		at(msg(7, "<e@x>", "", "<a@x>"), day.Add(2*time.Hour), time.Time{}),
		at(msg(6, "<f@x>", "", "<a@x>"), day.Add(time.Hour), time.Time{}),
		at(msg(8, "<g@x>", "", "<a@x>"), day.Add(time.Hour), time.Time{}),
		at(msg(0, "", "", "<gone@x>"), day.Add(-2*time.Hour), time.Time{}),
		at(msg(0, "", "", "<gone@x>"), day.Add(-2*time.Hour), time.Time{}),
	}
	msgs[7].Subject, msgs[8].Subject = "first", "second"
	var tr email.Threader
	for _, m := range msgs {
		tr.Add(m)
	}
	first := tr.Threads()
	const want = "5 *<gone@x>[0 0] 20 10 30[6 8 7]"
	if got := render(first); got != want {
		t.Fatalf("threads %s, want %s", got, want)
	}
	if got := render(tr.Threads()); got != want {
		t.Errorf("built again: threads %s, want %s", got, want)
	}
	if got := threadOf(msgs...); got != want {
		t.Errorf("from the same messages: threads %s, want %s", got, want)
	}
}

// TestThreadsRecords: Threads takes a read's records, ignoring all but messages and the messages
// nested under attachment records, and returns the error that ends them, with no threads.
func TestThreadsRecords(t *testing.T) {
	nested := msg(3, "<n@x>", "", "<a@x>")
	recs := []email.Record{
		&email.Store{Schema: email.Schema, Format: "pst"},
		&email.Folder{ID: 32802},
		msg(1, "<a@x>", "", ""),
		&email.Skipped{Kind: "message", Folder: 32802},
		&email.UnknownRecord{Kind: "calendar"},
		func() *email.Message {
			m := msg(2, "<b@x>", "", "<a@x>")
			m.Attachments = []email.Attachment{{Method: "embedded_message", Message: nested}}
			return m
		}(),
	}
	seq := func(err error) iter.Seq2[email.Record, error] {
		return func(yield func(email.Record, error) bool) {
			for _, rec := range recs {
				if !yield(rec, nil) {
					return
				}
			}
			if err != nil {
				yield(nil, err)
			}
		}
	}
	threads, err := email.Threads(seq(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := render(threads), "1[2]"; got != want {
		t.Errorf("threads %s, want %s", got, want)
	}
	if threads[0].MessageID != "<a@x>" || threads[0].Placeholder {
		t.Errorf("root %+v, want message <a@x>", threads[0])
	}
	failed := errors.New("read failed")
	threads, err = email.Threads(seq(failed))
	if !errors.Is(err, failed) || threads != nil {
		t.Errorf("got %v, %v; want no threads and the read's error", threads, err)
	}
}

// TestThreadsStandin: Threads takes a Stream's Records whole.
func TestThreadsStandin(t *testing.T) {
	r, _ := standinReader(t)
	t.Setenv(stdoutEnv, `{"line":"store","schema":"1","format":"pst"}
{"line":"folder","id":32802}
{"line":"message","id":2097156,"folder":32802,"subject":"Hello","message_id":"<a@x>","submit_time":"2026-01-12T14:15:00Z"}
{"line":"folder","id":32866,"parent":32802}
{"line":"message","id":2097188,"folder":32866,"subject":"Re: Hello","in_reply_to":"<a@x>","submit_time":"2026-01-12T15:15:00Z"}
`)
	threads, err := email.Threads(r.Read(t.Context(), email.Request{Input: "store.pst"}).Records())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := render(threads), "2097156[2097188]"; got != want {
		t.Errorf("threads %s, want %s", got, want)
	}
}

// realThreads is the threads of the copied stores that have a hand truth, each by its path under
// fixtures/, rendered: Tika's two, derived by hand from the stored fields tika-threading.json holds
// and the dates mail's goldens give, and the generated store's, by subject alone. TestRealThreads,
// TestRealThreadsIndex, and TestRealThreadsGenerated check them, and the findings tier scores
// against them.
var realThreads = map[string]string{
	"pst/tika/testPST.pst":                  "2097348 2097252[2097188] 2097220 2097316 2097284 2097380",
	"pst/tika/testPST_variousBodyTypes.pst": "2097188[2097220 2097252 2097284]",
	"generated/pst/mailbox.pst":             "2097156 2097188 2097220 2097252 2097284 2097316 2097348 2097380",
}

// TestRealThreads runs the real pinned mail over Tika's two stores and threads them, against
// realThreads. One store is threaded through a Threader fed while reading, the other through
// Threads.
//
// testPST.pst: Re: Feature Generators (2097188) refers to Feature Generators (2097252). The two
// JIRA mails (2097284, 2097316) and FW: First email (2097380) refer to messages the store doesn't
// hold, each placeholder with one child, so each is a root. The base subjects are all different,
// "[WEBINAR] " and "[jira] [Resolved] " removed as subj-blobs, so no roots are gathered. By date:
// WEBINAR 02-24, Feature Generators 02-25 10:17, init tokenizer 02-25 21:20, TIKA-1250 02-26
// 12:12, TIKA-1249 02-26 12:20, FW: First email 2020.
//
// testPST_variousBodyTypes.pst: the three FW: original email (2097220, 2097252, 2097284) each
// refer to original email (2097188), and are its children by date.
func TestRealThreads(t *testing.T) {
	r := realReader(t)
	root := filepath.Join(mailCopy, "fixtures", "pst", "tika")
	t.Run("testPST.pst", func(t *testing.T) {
		var tr email.Threader
		for rec, err := range r.Read(t.Context(), email.Request{Input: filepath.Join(root, "testPST.pst")}).Records() {
			if err != nil {
				t.Fatal(err)
			}
			tr.Add(rec)
		}
		want := realThreads["pst/tika/testPST.pst"]
		if got := render(tr.Threads()); got != want {
			t.Errorf("threads %s, want %s", got, want)
		}
	})
	t.Run("testPST_variousBodyTypes.pst", func(t *testing.T) {
		threads, err := email.Threads(r.Read(t.Context(), email.Request{Input: filepath.Join(root, "testPST_variousBodyTypes.pst")}).Records())
		if err != nil {
			t.Fatal(err)
		}
		want := realThreads["pst/tika/testPST_variousBodyTypes.pst"]
		if got := render(threads); got != want {
			t.Errorf("threads %s, want %s", got, want)
		}
		if got := threads[0].MessageID; got != "<MWHPR09MB1391E30131B0D193163AA6E0C79C0@MWHPR09MB1391.namprd09.prod.outlook.com>" {
			t.Errorf("root Message-ID %s", got)
		}
	})
}

// TestRealThreadsIndex threads Tika's two stores by their conversation indexes alone: each
// message's Message-ID, In-Reply-To, References, and subject cleared as it is read. By the
// indexes in tika-threading.json: in testPST.pst, Re: Feature Generators' is Feature Generators'
// and one child block, and FW: First email's names a parent the store doesn't hold, a placeholder
// with one child, so a root; the others are 22-byte headers. In testPST_variousBodyTypes.pst, each
// FW: original email's is original email's and one child block. The trees are those the headers
// give, realThreads'.
func TestRealThreadsIndex(t *testing.T) {
	r := realReader(t)
	root := filepath.Join(mailCopy, "fixtures", "pst", "tika")
	for _, store := range []string{"testPST.pst", "testPST_variousBodyTypes.pst"} {
		t.Run(store, func(t *testing.T) {
			var tr email.Threader
			for rec, err := range r.Read(t.Context(), email.Request{Input: filepath.Join(root, store)}).Records() {
				if err != nil {
					t.Fatal(err)
				}
				if m, ok := rec.(*email.Message); ok {
					if m.ConversationIndex == "" {
						t.Fatalf("message %d: no conversation index", m.ID)
					}
					m.MessageID, m.InReplyTo, m.References, m.Subject = "", "", "", ""
				}
				tr.Add(rec)
			}
			if got, want := render(tr.Threads()), realThreads["pst/tika/"+store]; got != want {
				t.Errorf("threads %s, want %s", got, want)
			}
		})
	}
}

// TestRealThreadsGenerated: the generated store carries no threading fields, so its messages
// thread by subject alone. None shares a base subject with another: "Re: Notes on the engine à la
// française" and "Fwd: Notes on the engine (draft)" are not "Notes on the engine". Each of its 8
// messages is a root, by date.
func TestRealThreadsGenerated(t *testing.T) {
	r := realReader(t)
	path := filepath.Join(mailCopy, "fixtures", "generated", "pst", "mailbox.pst")
	threads, err := email.Threads(r.Read(t.Context(), email.Request{Input: path}).Records())
	if err != nil {
		t.Fatal(err)
	}
	want := realThreads["generated/pst/mailbox.pst"]
	if got := render(threads); got != want {
		t.Errorf("threads %s, want %s", got, want)
	}
}
