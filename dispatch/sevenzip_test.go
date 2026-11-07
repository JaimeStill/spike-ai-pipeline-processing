package dispatch_test

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
)

// sevenZipSignature begins a 7z archive, so a test input routes to the archive stage as a 7z.
const sevenZipSignature = "7z\xbc\xaf\x27\x1c\x00\x04"

// fakeSevenZip stands in for sevenzip's Reader: List returns its listing, or its listErr, and
// Extract writes the content its contents hold for the name asked, recording each name.
type fakeSevenZip struct {
	listing   sevenzip.Listing
	listErr   error
	contents  map[string]string
	mu        sync.Mutex
	extracted []string
}

func (f *fakeSevenZip) List(context.Context, sevenzip.ListRequest) (sevenzip.Listing, error) {
	return f.listing, f.listErr
}

func (f *fakeSevenZip) Extract(_ context.Context, req sevenzip.ExtractRequest, w io.Writer) error {
	f.mu.Lock()
	f.extracted = append(f.extracted, req.Name)
	f.mu.Unlock()
	_, err := io.WriteString(w, f.contents[req.Name])
	return err
}

// fakeEntries returns a listing's file entries of contents' names, in names' order, each sized.
func fakeEntries(contents map[string]string, names ...string) []sevenzip.Entry {
	var entries []sevenzip.Entry
	for _, n := range names {
		entries = append(entries, sevenzip.Entry{Path: n, Size: uint64(len(contents[n])), Sized: true, Mode: 0o644})
	}
	return entries
}

// TestListingQuarantine: a 7z whose listing names a traversing entry is quarantined by the stage
// before anything is extracted: its outcome is quarantined, its flags hold the listing's flag
// beside its inspection's and its reason the evidence, and 7-Zip extracts nothing. Under a policy
// that lets 7z-traversal proceed, each entry is extracted and found, "7z entry <name>".
func TestListingQuarantine(t *testing.T) {
	contents := map[string]string{"notes.txt": "an ordinary entry\n", "docs/../../evil.txt": "evil\n"}
	fake := func() *fakeSevenZip {
		return &fakeSevenZip{
			listing:  sevenzip.Listing{Type: "7z", Entries: fakeEntries(contents, "notes.txt", "docs/../../evil.txt")},
			contents: contents,
		}
	}
	sz := fake()
	d, zs, work := archiveDispatcherWith(t, dispatch.ArchiveConfig{SevenZip: sz, Version: "7zz 26.5"})
	events := dispatchAll(t, d, input(t, "a.7z", sevenZipSignature), nil)
	wantSteps(t, events, []step{{"a.7z", 0, dispatch.Quarantined}})
	ev := events[0]
	if !slices.ContainsFunc(ev.Part.Flags, func(f inspect.Flag) bool {
		return f.Check == inspect.SevenZipTraversal && f.Evidence == `entry "docs/../../evil.txt"`
	}) || !strings.Contains(ev.Reason, `7z-traversal: entry "docs/../../evil.txt"`) {
		t.Errorf("a.7z: flags %v, reason %q", ev.Part.Flags, ev.Reason)
	}
	if ev.Stage.Name != "archive" || zs.calls.Load() != 1 || len(sz.extracted) != 0 || len(ev.Outputs) != 0 {
		t.Errorf("a.7z: stage %v, called %d times, extracted %q, %d outputs", ev.Stage, zs.calls.Load(), sz.extracted, len(ev.Outputs))
	}
	wantEmpty(t, work)

	permissive := inspect.Options{Policy: map[inspect.Check]inspect.Verdict{inspect.SevenZipTraversal: inspect.Proceed}}
	sz = fake()
	d, _, work = archiveDispatcherWith(t, dispatch.ArchiveConfig{Inspect: permissive, SevenZip: sz, Version: "7zz 26.5"})
	events = dispatchAll(t, d, input(t, "a.7z", sevenZipSignature), nil)
	wantSteps(t, events, []step{
		{"notes.txt", 1, dispatch.NoProcessor},
		{"docs/../../evil.txt", 1, dispatch.NoProcessor},
		{"a.7z", 0, dispatch.Prepared},
	})
	if got, want := wheres(events), []string{"7z entry notes.txt", "7z entry docs/../../evil.txt"}; !slices.Equal(got, want) {
		t.Errorf("wheres:\n got %q\nwant %q", got, want)
	}
	if !slices.Equal(sz.extracted, []string{"notes.txt", "docs/../../evil.txt"}) {
		t.Errorf("extracted %q", sz.extracted)
	}
	wantEmpty(t, work)
}

// TestListedMismatch: an entry whose bytes from 7-Zip aren't the size or CRC-32 its listing
// declares is a record naming the mismatch, its partial file removed, and the archive is
// prepared.
func TestListedMismatch(t *testing.T) {
	contents := map[string]string{"short.txt": "short\n", "crc.txt": "crc\n"}
	entries := fakeEntries(contents, "short.txt", "crc.txt")
	entries[0].Size = 100
	entries[1].CRC, entries[1].HasCRC = 0xdeadbeef, true
	sz := &fakeSevenZip{listing: sevenzip.Listing{Entries: entries}, contents: contents}
	d, _, work := archiveDispatcherWith(t, dispatch.ArchiveConfig{SevenZip: sz, Version: "7zz 26.5"})
	events := dispatchAll(t, d, input(t, "a.7z", sevenZipSignature), nil)
	wantSteps(t, events, []step{{"a.7z", 0, dispatch.Prepared}})
	for name, want := range map[string]string{"short.txt": "6 bytes written, 100 listed", "crc.txt": "CRC-32 "} {
		if m := outputNamed(t, events[0], dispatch.RecordOutput, "7z entry "+name).Meta; !strings.HasPrefix(m["reason"], want) || m["name"] != name {
			t.Errorf("%s's record meta %v, want reason %q", name, m, want)
		}
	}
	wantEmpty(t, work)
}

// TestListedFailures: a 7z whose headers are encrypted ends encrypted, nothing extracted, and a
// listing 7-Zip fails fails the part.
func TestListedFailures(t *testing.T) {
	for _, c := range []struct {
		err     error
		outcome dispatch.Outcome
	}{
		{fmt.Errorf("sevenzip: list: %w: Cannot open encrypted archive", sevenzip.ErrEncrypted), dispatch.Encrypted},
		{fmt.Errorf("sevenzip: list: %w: Cannot open the file as [7z] archive", sevenzip.ErrNotArchive), dispatch.Failed},
	} {
		sz := &fakeSevenZip{listErr: c.err}
		d, _, work := archiveDispatcherWith(t, dispatch.ArchiveConfig{SevenZip: sz, Version: "7zz 26.5"})
		events := dispatchAll(t, d, input(t, "a.7z", sevenZipSignature), nil)
		wantSteps(t, events, []step{{"a.7z", 0, c.outcome}})
		if len(sz.extracted) != 0 || !strings.Contains(events[0].Reason, "a 7z archive") && c.outcome == dispatch.Encrypted {
			t.Errorf("%v: extracted %q, reason %q", c.err, sz.extracted, events[0].Reason)
		}
		wantEmpty(t, work)
	}
}
