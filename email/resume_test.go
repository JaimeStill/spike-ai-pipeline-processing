//go:build linux

package email_test

import (
	"bytes"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestReadAfter: a Request's After reaches mail as --after and its decimal id, after the
// attachments directory and before the input; a zero After passes none. An After past mail's
// range, or given a mail older than MailAfterMinimum, is refused before anything runs, the
// latter with a VersionError naming MailAfterMinimum; the same mail without After reads.
func TestReadAfter(t *testing.T) {
	t.Run("0.3.0", func(t *testing.T) {
		runs := installStandin(t, "0.3.0")
		tl, err := tool.NewExec(t.Context(), email.MailSpec())
		if err != nil {
			t.Fatal(err)
		}
		r, err := email.NewReader(email.Config{Mail: tl})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		input, attachments := filepath.Join(dir, "store.pst"), filepath.Join(dir, "a")
		for _, req := range []email.Request{
			{Input: input, Attachments: attachments, After: 2097188},
			{Input: input, After: 1},
			{Input: input, After: math.MaxUint32},
			{Input: input},
		} {
			if _, err := collect(r.Read(t.Context(), req)); err != nil {
				t.Fatalf("%+v: %v", req, err)
			}
		}
		want := [][]string{
			{"--attachments", attachments, "--after", "2097188", input},
			{"--after", "1", input},
			{"--after", "4294967295", input},
			{input},
		}
		if got := runs(); !reflect.DeepEqual(got, want) {
			t.Errorf("runs %q, want %q", got, want)
		}
		_, err = r.Read(t.Context(), email.Request{Input: input, After: math.MaxUint32 + 1})
		if !errors.Is(err, email.ErrRequest) {
			t.Errorf("after past mail's range: %v, want ErrRequest", err)
		}
		if got := len(runs()); got != len(want) {
			t.Errorf("%d runs, want %d: the refused request ran", got, len(want))
		}
	})
	t.Run("0.2.0", func(t *testing.T) {
		r, runs := standinReader(t)
		input := filepath.Join(t.TempDir(), "store.pst")
		_, err := r.Read(t.Context(), email.Request{Input: input, After: 5})
		var verr *tool.VersionError
		if !errors.Is(err, email.ErrRequest) || !errors.Is(err, tool.ErrTooOld) || !errors.As(err, &verr) ||
			verr.Found != "0.2.0" || verr.Minimum != email.MailAfterMinimum {
			t.Fatalf("after on mail 0.2.0: %v, want ErrRequest and a VersionError under %s", err, email.MailAfterMinimum)
		}
		if got := runs(); len(got) != 0 {
			t.Errorf("runs %q, want none", got)
		}
		if _, err := collect(r.Read(t.Context(), email.Request{Input: input})); err != nil {
			t.Errorf("without after on mail 0.2.0: %v", err)
		}
	})
}

// TestThreaderSaved: a saved Threader restores to one that saves the same bytes and threads the
// same, and goes on threading as the original does when both are fed more; an empty Threader
// saves and restores too. Data of another format, cut short at any byte, or with a byte after its
// last message is refused, the Threader left as it was.
func TestThreaderSaved(t *testing.T) {
	// header is a conversation index's 22-byte header, in hex; a reply's adds a 5-byte block.
	header := "01d0aa" + strings.Repeat("00", 19)
	first := []*email.Message{
		msg(1, "<a@x>", "", ""),
		msg(2, "<b@x>", "<a@x>", "<a@x>"),
		subj(3, "Re: Alone"),
		indexed(4, header),
		msg(5, "<e@x>", "", "<missing@x> <a@x>"),
	}
	more := []*email.Message{
		msg(6, "<f@x>", "<e@x>", ""),
		subj(7, "Alone"),
		indexed(8, header+"0000000001"),
	}
	var orig email.Threader
	for _, m := range first {
		orig.Add(m)
	}
	saved, err := orig.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var restored email.Threader
	if err := restored.UnmarshalBinary(saved); err != nil {
		t.Fatal(err)
	}
	if again, err := restored.MarshalBinary(); err != nil || !bytes.Equal(again, saved) {
		t.Fatalf("the restored Threader saves %x, %v; want %x", again, err, saved)
	}
	if got, want := render(restored.Threads()), render(orig.Threads()); got != want {
		t.Errorf("restored threads %s, the original's %s", got, want)
	}
	for _, m := range more {
		orig.Add(m)
		restored.Add(m)
	}
	if got, want := restored.Threads(), orig.Threads(); !reflect.DeepEqual(got, want) {
		t.Errorf("fed more, restored threads %s, the original's %s", render(got), render(want))
	}

	var empty, back email.Threader
	b, err := empty.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if err := back.UnmarshalBinary(b); err != nil || len(back.Threads()) != 0 {
		t.Errorf("an empty Threader restored: %v, %d threads", err, len(back.Threads()))
	}

	refused := [][]byte{nil, []byte("not a threader"), slices.Concat(saved, []byte{0})}
	for n := range len(saved) {
		refused = append(refused, saved[:n])
	}
	wrongVersion := slices.Clone(saved)
	wrongVersion[len("email.Threader\x00")] = 2
	refused = append(refused, wrongVersion)
	for _, data := range refused {
		kept := restored
		if err := kept.UnmarshalBinary(data); err == nil {
			t.Errorf("restored from %x, want a refusal", data)
		}
		if got, want := kept.Threads(), restored.Threads(); !reflect.DeepEqual(got, want) {
			t.Errorf("a refused restore changed the Threader: %s, want %s", render(got), render(want))
		}
	}
}

// TestRealResume runs the real pinned mail over the generated .pst, Tika's testPST.pst, which
// threads by its headers, and nine.mboxrd. Each is read whole and threaded; then, for its first
// message, one in the middle, its next to last, and its last, read up to and including that
// message, K, the Threader saved, and the read resumed with After K into a Threader restored from
// it. The resumed read's records are the whole read's store, folders, and the records after K's,
// in order, and the restored Threader's threads are the whole read's.
func TestRealResume(t *testing.T) {
	r := realReader(t)
	root := filepath.Join(mailCopy, "fixtures")
	for _, rel := range []string{"generated/pst/mailbox.pst", "pst/tika/testPST.pst", "mime/nine.mboxrd"} {
		t.Run(rel, func(t *testing.T) {
			input := filepath.Join(root, rel)
			whole, err := collect(r.Read(t.Context(), email.Request{Input: input}))
			if err != nil {
				t.Fatal(err)
			}
			var wholeThreader email.Threader
			var ids []uint64
			for _, rec := range whole {
				wholeThreader.Add(rec)
				if m, ok := rec.(*email.Message); ok {
					ids = append(ids, m.ID)
				}
			}
			want := wholeThreader.Threads()
			if len(ids) < 4 {
				t.Fatalf("%d messages, want at least 4", len(ids))
			}
			for _, k := range []uint64{ids[0], ids[len(ids)/2], ids[len(ids)-2], ids[len(ids)-1]} {
				var before email.Threader
				s := mustRead(t)(r.Read(t.Context(), email.Request{Input: input}))
				for rec, err := range s.Records() {
					if err != nil {
						t.Fatal(err)
					}
					before.Add(rec)
					if m, ok := rec.(*email.Message); ok && m.ID == k {
						break
					}
				}
				saved, err := before.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				var after email.Threader
				if err := after.UnmarshalBinary(saved); err != nil {
					t.Fatal(err)
				}
				resumed, err := collect(r.Read(t.Context(), email.Request{Input: input, After: k}))
				if err != nil {
					t.Fatalf("after %d: %v", k, err)
				}
				for _, rec := range resumed {
					after.Add(rec)
				}
				if got, want := describeAll(resumed), describeAll(tail(whole, k)); !slices.Equal(got, want) {
					t.Errorf("after %d, records:\n got %q\nwant %q", k, got, want)
				}
				if got := after.Threads(); !reflect.DeepEqual(got, want) {
					t.Errorf("after %d, threads %s, the whole read's %s", k, render(got), render(want))
				}
			}
		})
	}
}

// tail returns what a read resumed after message k gives of whole, a whole read's records: its
// store, folders, and folders' skipped records, then every record after k's message.
func tail(whole []email.Record, k uint64) []email.Record {
	var out []email.Record
	past := false
	for _, rec := range whole {
		switch r := rec.(type) {
		case *email.Store, *email.Folder:
			out = append(out, rec)
		case *email.Skipped:
			if past || r.Kind == "folder" || r.Kind == "contents" || r.Kind == "hierarchy" {
				out = append(out, rec)
			}
		default:
			if past {
				out = append(out, rec)
			}
		}
		if m, ok := rec.(*email.Message); ok && m.ID == k {
			past = true
		}
	}
	return out
}

// describeAll returns describe of each of recs.
func describeAll(recs []email.Record) []string {
	out := make([]string, len(recs))
	for i, rec := range recs {
		out[i] = describe(rec)
	}
	return out
}
