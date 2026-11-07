//go:build integration

package dispatch_test

import (
	"maps"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// TestTarDocs dispatches ~/docs' mixed-bundle.tar.gz, read only, when this machine has it: the
// gzip stream's content, named by its header "mixed-bundle.tar", is a tar, found "gzip content
// mixed-bundle.tar" and prepared below it; the tar's entries, stored under "./", are each routed
// by their content and names as mixed-bundle.zip's are, its directories no parts; and nothing is
// left once the dispatch is done.
func TestTarDocs(t *testing.T) {
	d, _, work := archiveDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, docsArchive(t, "mixed-bundle.tar.gz"), nil)
	tar := byName(t, events, "mixed-bundle.tar")
	if tar.Part.Kind != route.Tar || tar.Part.Depth != 1 || tar.Part.Where != "gzip content mixed-bundle.tar" || tar.Outcome != dispatch.Prepared {
		t.Errorf("mixed-bundle.tar: %s at depth %d, %q, %s", tar.Part.Kind, tar.Part.Depth, tar.Part.Where, tar.Outcome)
	}
	got := map[string]route.Route{}
	for _, ev := range events {
		if ev.Part.Depth == 2 {
			got[ev.Part.Name] = ev.Part.Route
			if ev.Part.Where != "tar entry "+ev.Part.Name {
				t.Errorf("%s found at %q", ev.Part.Name, ev.Part.Where)
			}
		}
	}
	want := map[string]route.Route{
		"./meeting-notes.txt": route.Text, "./mail/04-attachment-cyrillic.eml": route.Nested,
		"./docs/shapes.png": route.Vision, "./docs/mgrs-field-report.pdf": route.Document,
		"./docs/SampleSS.xls": route.Office, "./docs/word-tables.docx": route.Office,
	}
	if !maps.Equal(got, want) {
		t.Errorf("entries' routes:\n got %v\nwant %v", got, want)
	}
	if tar.Children != len(want) {
		t.Errorf("mixed-bundle.tar: %d children, want %d", tar.Children, len(want))
	}
	if last := events[len(events)-1]; last.Outcome != dispatch.Prepared || last.Part.Kind != route.Gzip || last.Children != 1 {
		t.Errorf("mixed-bundle.tar.gz: %s %s, %d children", last.Part.Kind, last.Outcome, last.Children)
	}
	wantEmpty(t, work)
}

// TestSevenZipDocs dispatches ~/docs' mixed-bundle.7z and mixed-bundle.tar.zst, read only, when
// this machine has them, through the 7zz mise pins: the 7z's entries, stored under "./", are found
// "7z entry <name>" and routed as the tar's are, its directories no parts; the zstd stream's
// content, named by the stream's stem "mixed-bundle.tar", is a tar, found "zstd content
// mixed-bundle.tar", whose entries are seen, the same content as the 7z's; and nothing is left
// once the dispatch is done.
func TestSevenZipDocs(t *testing.T) {
	d, _, work := sevenZipDispatcher(t, inspect.Options{})
	want := map[string]route.Route{
		"./meeting-notes.txt": route.Text, "./mail/04-attachment-cyrillic.eml": route.Nested,
		"./docs/shapes.png": route.Vision, "./docs/mgrs-field-report.pdf": route.Document,
		"./docs/SampleSS.xls": route.Office, "./docs/word-tables.docx": route.Office,
	}
	events := dispatchAll(t, d, docsArchive(t, "mixed-bundle.7z"), nil)
	got := map[string]route.Route{}
	for _, ev := range events {
		if ev.Part.Depth == 1 {
			got[ev.Part.Name] = ev.Part.Route
			if ev.Part.Where != "7z entry "+ev.Part.Name {
				t.Errorf("%s found at %q", ev.Part.Name, ev.Part.Where)
			}
		}
	}
	if !maps.Equal(got, want) {
		t.Errorf("mixed-bundle.7z's entries' routes:\n got %v\nwant %v", got, want)
	}
	if last := events[len(events)-1]; last.Outcome != dispatch.Prepared || last.Part.Kind != route.SevenZip || last.Children != len(want) {
		t.Errorf("mixed-bundle.7z: %s %s, %d children", last.Part.Kind, last.Outcome, last.Children)
	}

	events = dispatchAll(t, d, docsArchive(t, "mixed-bundle.tar.zst"), nil)
	tar := byName(t, events, "mixed-bundle.tar")
	if tar.Part.Kind != route.Tar || tar.Part.Depth != 1 || tar.Part.Where != "zstd content mixed-bundle.tar" || tar.Outcome != dispatch.Prepared || tar.Children != len(want) {
		t.Errorf("mixed-bundle.tar: %s at depth %d, %q, %s, %d children", tar.Part.Kind, tar.Part.Depth, tar.Part.Where, tar.Outcome, tar.Children)
	}
	got = map[string]route.Route{}
	for _, ev := range events {
		if ev.Part.Depth == 2 {
			got[ev.Part.Name] = ev.Part.Route
			if ev.Outcome != dispatch.Seen && ev.Outcome != dispatch.NoProcessor {
				t.Errorf("%s: %s, want seen, the 7z's content, or no processor yet, which the index doesn't hold", ev.Part.Name, ev.Outcome)
			}
		}
	}
	if !maps.Equal(got, want) {
		t.Errorf("mixed-bundle.tar's entries' routes:\n got %v\nwant %v", got, want)
	}
	if last := events[len(events)-1]; last.Outcome != dispatch.Prepared || last.Part.Kind != route.Zstd || last.Children != 1 {
		t.Errorf("mixed-bundle.tar.zst: %s %s, %d children", last.Part.Kind, last.Outcome, last.Children)
	}
	wantEmpty(t, work)
}
