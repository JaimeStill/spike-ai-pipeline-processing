//go:build findings && linux

package inspect_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// The findings tier records what inspection finds over ~/docs for task onenote's finding,
// docs/findings/onenote.md, never in CI or the check: TestDocs holds the same files to docsFlags,
// and this records each one's flags and verdict. `mise run findings-onenote` runs it. An
// ONENOTE_FINDINGS_RESULTS the shell sets keeps its JSON there.
const findingsResultsEnv = "ONENOTE_FINDINGS_RESULTS"

// hazardCovers are the manifest's covers that name a hazard: active content, encryption,
// embedded files, and the zip bomb.
var hazardCovers = []string{
	"security", "javascript", "macro", "vba", "xlm", "script", "encrypted", "password-protected",
	"embedded-files", "zip-bomb-lite (200 MB of zeros",
}

// docsFinding is one file of ~/docs as inspected: its path, the hazard covers its manifest entry
// names, its kind, its flags, and its verdict, under the default options or, for the bomb, the
// lowered size.
type docsFinding struct {
	Path    string          `json:"path"`
	Covers  []string        `json:"covers,omitzero"`
	Kind    string          `json:"kind"`
	Flags   []inspect.Flag  `json:"flags,omitzero"`
	Verdict inspect.Verdict `json:"verdict"`
	// BombSize is the expanded size the zip bomb rule was lowered to, when it was.
	BombSize int64 `json:"bomb_size,omitzero"`
}

// TestFindingsDocs inspects every file of ~/docs by its manifest, as TestDocs does, and records
// each that its manifest names a hazard or that yields a flag: its checks, their evidence, and
// its verdict; then the bomb under docsBombSize. It records how many files it inspected and how
// many came to each verdict.
func TestFindingsDocs(t *testing.T) {
	root, _ := docsRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "MANIFEST.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files []struct {
			Path   string   `json:"path"`
			Covers []string `json:"covers"`
		} `json:"files"`
	}
	if err := jsonv2.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	var found []docsFinding
	verdicts := map[inspect.Verdict]int{}
	flagged := 0
	for _, f := range manifest.Files {
		r, err := inspect.Inspect(t.Context(), filepath.Join(root, f.Path), filepath.Base(f.Path), inspect.Options{})
		if err != nil {
			t.Errorf("%s: %v", f.Path, err)
			continue
		}
		verdicts[r.Verdict]++
		var covers []string
		for _, c := range f.Covers {
			if slices.Contains(hazardCovers, c) {
				covers = append(covers, c)
			}
		}
		if len(r.Flags) > 0 {
			flagged++
		}
		if len(covers) == 0 && len(r.Flags) == 0 {
			continue
		}
		found = append(found, docsFinding{f.Path, covers, string(r.Kind), r.Flags, r.Verdict, 0})
		t.Logf("recorded: docs %s: covers %s; kind %s; checks %s; verdict %s", f.Path, strings.Join(covers, " "), r.Kind,
			checkList(r), r.Verdict)
	}
	bomb := filepath.Join("archive", "high-ratio-200mb.zip")
	r, err := inspect.Inspect(t.Context(), filepath.Join(root, bomb), filepath.Base(bomb), inspect.Options{BombSize: docsBombSize})
	if err != nil {
		t.Fatal(err)
	}
	found = append(found, docsFinding{bomb, nil, string(r.Kind), r.Flags, r.Verdict, docsBombSize})
	t.Logf("recorded: docs %s, the bomb rule's size lowered to %d: checks %s; verdict %s", bomb, docsBombSize, checkList(r), r.Verdict)
	t.Logf("recorded: docs: %d files inspected, %d flagged, %d recorded; verdicts %v", len(manifest.Files), flagged, len(found), verdicts)
	if dir := os.Getenv(findingsResultsEnv); dir != "" {
		b, err := json.MarshalIndent(found, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docs-inspect.json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// checkList writes r's checks, "none" for none.
func checkList(r inspect.Report) string {
	var cs []string
	for _, c := range checks(r) {
		cs = append(cs, string(c))
	}
	if len(cs) == 0 {
		return "none"
	}
	return strings.Join(cs, " ")
}
