//go:build integration && linux

package office_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
)

// TestRTFRoundTrip converts an RTF through soffice in the default image to docx, then office, as
// Markdown: the committed testdata/rtf/memo.rtf, and ~/docs's office/rtf/test-rtf.rtf, skipped
// without it. Each result is converted from "rtf", and its Markdown holds the text the RTF holds,
// as its source reads, each line given here as office's Markdown writes it: memo.rtf's heading,
// its paragraph with a windows-1252 é and a bold run, which office's Markdown writes plain, its
// table's rows, and its last paragraph; test-rtf.rtf's paragraph, one sentence eleven times. The
// "title" above that paragraph is a \tc table-of-contents entry, hidden text RTF doesn't display,
// and the Markdown holds none of it. No golden Markdown is recorded: the asserted lines are the
// content, not LibreOffice's layout.
func TestRTFRoundTrip(t *testing.T) {
	c := containerConverter(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sentence := "Sluwe Sjaantje sloeg de slome slager."
	for _, tc := range []struct {
		path string
		want []string
	}{
		{filepath.Join("testdata", "rtf", "memo.rtf"), []string{
			"# Harbor memo",
			"The survey boat left at dawn and returned with three samples from the café pier.",
			"| Sample | Depth |",
			"| North | 12 m |",
			"| South | 9 m |",
			"Next survey: Thursday.",
		}},
		{filepath.Join(home, "docs", "office", "rtf", "test-rtf.rtf"), []string{
			strings.TrimSpace(strings.Repeat(sentence+" ", 11)),
		}},
	} {
		t.Run(filepath.Base(tc.path), func(t *testing.T) {
			f, err := os.Open(tc.path)
			if os.IsNotExist(err) {
				t.Skipf("%s isn't here", tc.path)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			res, err := c.Convert(t.Context(), f, office.Request{Dir: t.TempDir(), Format: office.Markdown, Name: filepath.Base(tc.path)})
			if err != nil {
				t.Fatal(err)
			}
			if res.ConvertedFrom != "rtf" {
				t.Errorf("converted from %q, want rtf", res.ConvertedFrom)
			}
			for _, w := range res.Warnings {
				t.Logf("warning: %s", w)
			}
			lines := map[string]bool{}
			for line := range strings.Lines(res.Markdown) {
				lines[strings.TrimSpace(line)] = true
			}
			for _, w := range tc.want {
				if !lines[w] {
					t.Errorf("no line %q in the Markdown:\n%s", w, res.Markdown)
				}
			}
		})
	}
}
