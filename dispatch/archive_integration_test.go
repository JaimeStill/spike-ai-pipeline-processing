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
	d, _, work := zipDispatcher(t, inspect.Options{})
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
