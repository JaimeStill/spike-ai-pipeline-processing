//go:build linux

package email_test

import (
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
)

// generated is the copy of mail's generated fixtures: the stores, their layout.json, and the hand
// truth, truth.json.
var generated = filepath.Join(mailCopy, "fixtures", "generated")

// layout is mail's generated layout.json: each store's file and folders, each folder listing the
// .eml sources of its messages, or the item sources of its items, in the store's order.
type layout struct {
	Stores []struct {
		File    string `json:"file"`
		Folders []struct {
			Path     []string `json:"path"`
			Messages []string `json:"messages"`
			Items    []string `json:"items"`
		} `json:"folders"`
	} `json:"stores"`
}

// itemTruth is one item source's hand truth in truth.json's "items": the item as the store holds
// it, field by field.
type itemTruth struct {
	Class            string          `json:"class"`
	Subject          string          `json:"subject"`
	Sender           email.Address   `json:"sender"`
	To               []email.Address `json:"to"`
	CC               []email.Address `json:"cc"`
	SubmitTime       time.Time       `json:"submit_time"`
	DeliveryTime     time.Time       `json:"delivery_time"`
	CreationTime     time.Time       `json:"creation_time"`
	ModificationTime time.Time       `json:"modification_time"`
	HTML             string          `json:"html"`
	Markdown         string          `json:"markdown"`
}

// readJSON decodes the file at path into v.
func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// bare returns addrs with each address type dropped: the hand truth gives a name and an address
// alone, and mail gives a recipient's type as stored, SMTP.
func bare(addrs []email.Address) []email.Address {
	out := make([]email.Address, len(addrs))
	for i, a := range addrs {
		out[i] = email.Address{Name: a.Name, Address: a.Address}
	}
	return out
}

// TestRealConversations runs the real pinned mail over the copied conversations.pst and holds
// each item of every folder layout.json lists items for to its hand truth in truth.json, in the
// layout's order. An item of a conversation class reads as a Conversation equal to the one the
// truth gives: its class, its topic the truth's subject, as the store's conversation topic is,
// its participants the sender then the recipients, its start and end the submit and delivery
// times, missed by its class, and its Markdown and stored HTML. An item of any other class, the
// IPM.Note in the same folder, reads as a Message, as before: its class, subject, sender,
// recipients, four times, stored HTML, and Markdown the truth's. The fixture holds every kind:
// a conversation, a missed one, and a note.
func TestRealConversations(t *testing.T) {
	var l layout
	readJSON(t, filepath.Join(generated, "layout.json"), &l)
	var truth struct {
		Items map[string]itemTruth `json:"items"`
	}
	readJSON(t, filepath.Join(generated, "truth.json"), &truth)
	r := realReader(t)
	classes := map[string]int{}
	stores := 0
	for _, store := range l.Stores {
		var items []string
		for _, f := range store.Folders {
			items = append(items, f.Items...)
		}
		if len(items) == 0 {
			continue
		}
		stores++
		t.Run(store.File, func(t *testing.T) {
			s := mustRead(t)(r.Read(t.Context(), email.Request{Input: filepath.Join(generated, store.File)}))
			recs, err := collect(s, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []email.Record
			for _, rec := range recs {
				switch rec.(type) {
				case *email.Message, *email.Conversation:
					got = append(got, rec)
				}
			}
			if len(got) != len(items) {
				t.Fatalf("%d items, the layout's %d", len(got), len(items))
			}
			for i, name := range items {
				want, ok := truth.Items[name]
				if !ok {
					t.Fatalf("%s: no hand truth", name)
				}
				classes[want.Class]++
				checkItem(t, name, got[i], want)
			}
		})
	}
	if stores == 0 {
		t.Fatal("layout.json lists items in no store")
	}
	for _, class := range []string{email.ConversationClass, email.MissedClass, "IPM.Note"} {
		if classes[class] == 0 {
			t.Errorf("no item of class %s", class)
		}
	}
}

// checkItem checks rec, the record read for the item source name, against want, its hand truth.
func checkItem(t *testing.T, name string, rec email.Record, want itemTruth) {
	t.Helper()
	if !email.IsConversation(want.Class) {
		m, ok := rec.(*email.Message)
		if !ok {
			t.Errorf("%s: a %s record, want a message of class %s", name, rec.Line(), want.Class)
			return
		}
		got := itemTruth{
			Class: m.Class, Subject: m.Subject, Sender: m.Sender, To: bare(m.To), CC: bare(m.CC),
			SubmitTime: m.SubmitTime, DeliveryTime: m.DeliveryTime,
			CreationTime: m.CreationTime, ModificationTime: m.ModificationTime,
			HTML: m.Body.HTML, Markdown: m.Body.Derived.Markdown,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: message\n%+v\nthe truth's\n%+v", name, got, want)
		}
		return
	}
	c, ok := rec.(*email.Conversation)
	if !ok {
		t.Errorf("%s: a %s record, want a conversation of class %s", name, rec.Line(), want.Class)
		return
	}
	if c.ID == 0 || c.Folder == 0 {
		t.Errorf("%s: conversation id %d in folder %d, want both", name, c.ID, c.Folder)
	}
	expect := email.Conversation{
		ID:           c.ID,
		Folder:       c.Folder,
		Class:        want.Class,
		Topic:        want.Subject,
		Participants: slices.Concat([]email.Address{want.Sender}, want.To, want.CC),
		Start:        want.SubmitTime,
		End:          want.DeliveryTime,
		Missed:       want.Class == email.MissedClass,
		Markdown:     want.Markdown,
		HTML:         want.HTML,
	}
	got := *c
	got.Participants = bare(c.Participants)
	if !reflect.DeepEqual(got, expect) {
		t.Errorf("%s: conversation\n%+v\nthe truth's\n%+v", name, got, expect)
	}
}

// TestMessageConversation: Message.Conversation reads a message of a conversation class, matched
// without regard to case, and no other: a Voice subclass, a note, and no class are messages. The
// topic is the stored conversation topic, else the subject; the participants are the sender, when
// there is one, then To, CC, and BCC in order; the start and end are the submit and delivery
// times; and missed is the Missed class's.
func TestMessageConversation(t *testing.T) {
	a := email.Address{Name: "A", Address: "a@example.com"}
	b := email.Address{Name: "B", Address: "b@example.com", AddressType: "SMTP"}
	c := email.Address{Name: "C", Address: "c@example.com"}
	d := email.Address{Name: "D", Address: "d@example.com"}
	start := time.Date(2026, 3, 10, 14, 2, 11, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	full := email.Message{
		ID: 7, Folder: 3, Class: email.ConversationClass, Subject: "subject",
		ConversationTopic: "topic", Sender: a, To: []email.Address{b}, CC: []email.Address{c},
		BCC: []email.Address{d}, SubmitTime: start, DeliveryTime: end,
		Body: email.Body{HTML: "<p>x</p>", Derived: email.Derived{Markdown: "x"}},
	}
	tests := []struct {
		name string
		edit func(m *email.Message)
		want *email.Conversation
	}{
		{"conversation", func(*email.Message) {}, &email.Conversation{
			ID: 7, Folder: 3, Class: email.ConversationClass, Topic: "topic",
			Participants: []email.Address{a, b, c, d}, Start: start, End: end,
			Markdown: "x", HTML: "<p>x</p>",
		}},
		{"missed, its class in another case", func(m *email.Message) { m.Class = "ipm.note.microsoft.missed" }, &email.Conversation{
			ID: 7, Folder: 3, Class: "ipm.note.microsoft.missed", Topic: "topic",
			Participants: []email.Address{a, b, c, d}, Start: start, End: end, Missed: true,
			Markdown: "x", HTML: "<p>x</p>",
		}},
		{"no topic, no sender", func(m *email.Message) { m.ConversationTopic, m.Sender = "", email.Address{} }, &email.Conversation{
			ID: 7, Folder: 3, Class: email.ConversationClass, Topic: "subject",
			Participants: []email.Address{b, c, d}, Start: start, End: end,
			Markdown: "x", HTML: "<p>x</p>",
		}},
		{"voice", func(m *email.Message) { m.Class = email.ConversationClass + ".Voice" }, nil},
		{"missed voice", func(m *email.Message) { m.Class = email.MissedClass + ".Voice" }, nil},
		{"note", func(m *email.Message) { m.Class = "IPM.Note" }, nil},
		{"no class", func(m *email.Message) { m.Class = "" }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := full
			tt.edit(&m)
			got, ok := m.Conversation()
			if ok != (tt.want != nil) || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Conversation() = %+v, %t; want %+v", got, ok, tt.want)
			}
		})
	}
}
