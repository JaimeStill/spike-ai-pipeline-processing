//go:build bakeoff && linux

package document_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// Ruling L of task documents, instruction placement: `mise run bakeoff-placement` runs
// TestPlacement once per placement and fixture set, each run capped at 10 minutes, then
// TestPlacementReport over their results. TestPlacement sends every page of the clean, degraded,
// and scanned sets through the vision harness's pages, with the released renderer at 150 dpi and
// no correction, to Gemma 4 26B-A4B on the router SPIKE_ROUTER_URL names, three greedy runs per
// set, with transcribeInstructions in one of two places: beside the image, a text part before it
// in the page's user message, as the renderer bake-off sent it; or in the request's Instructions,
// the image in a message of its own, as the corrections matrix and the integration tier send it.
// TestPlacementReport logs each set's CER and WER per placement as recorded lines. The finding is
// about the inference layer: processing prepares the same image either way.
//
// BAKEOFF_PLACEMENT names the placements to run and BAKEOFF_SET the fixture sets, each
// comma-separated; unset runs both placements on every set. BAKEOFF_RESULTS names a directory
// each run's result is written to as placement-<placement>-<set>.json, which the report reads;
// unset keeps the results in memory, so one go test run of both tests reports what it ran.

// placements are the two placements of the instructions, in the order the tables give them.
var placements = []string{"beside", "instructions"}

// placementRequest returns the request that runs the pages of a set with the instructions where
// placement puts them, its template the corrections matrix's.
func placementRequest(t *testing.T, placement string) visionRequest {
	t.Helper()
	req := visionRequest{Dir: t.TempDir(), Template: matrixTemplate(), DPI: bakeoffDPI, Concurrency: inFlight}
	if placement == "beside" {
		req.Template.Instructions = ""
		req.Prompt = transcribeInstructions
	}
	return req
}

// placementResult is one run of TestPlacement: a placement's three runs over one set.
type placementResult struct {
	Placement string          `json:"placement"`
	Set       string          `json:"set"`
	Versions  []string        `json:"versions"`
	Cells     []cell          `json:"cells"`
	RunTimes  []time.Duration `json:"run_times"`
}

// placementRuns holds the results of this process's TestPlacement runs for TestPlacementReport
// when BAKEOFF_RESULTS is unset.
var placementRuns struct {
	sync.Mutex
	runs []placementResult
}

// selectedNames returns the names env names, comma-separated, all of them when it is unset,
// failing t on a name that isn't one of all.
func selectedNames(t *testing.T, env string, all []string) []string {
	t.Helper()
	value := os.Getenv(env)
	if value == "" {
		return all
	}
	var names []string
	for name := range strings.SplitSeq(value, ",") {
		name = strings.TrimSpace(name)
		if !slices.Contains(all, name) {
			t.Fatalf("%s names %q, which is none of %v", env, name, all)
		}
		names = append(names, name)
	}
	return names
}

func TestPlacement(t *testing.T) {
	c := bakeoffClient(t)
	gs := hostTool(t, document.GhostscriptSpec())
	g, err := document.NewGhostscript(gs)
	if err != nil {
		t.Fatal(err)
	}
	v, err := newVision(timedGenerator{c}, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{fmt.Sprintf("%s %s", gs.Name(), gs.Version())}
	for _, placement := range selectedNames(t, "BAKEOFF_PLACEMENT", placements) {
		for _, s := range loadSets(t) {
			t.Run(placement+"/"+s.name, func(t *testing.T) {
				res := placementResult{Placement: placement, Set: s.name, Versions: versions}
				for run := 1; run <= bakeoffRuns; run++ {
					start := time.Now()
					label := fmt.Sprintf("placement %s %s run %d", placement, s.name, run)
					cells := harnessRun(t, v, s, placementRequest(t, placement), label, run)
					elapsed := time.Since(start)
					res.RunTimes = append(res.RunTimes, elapsed)
					for _, cl := range cells {
						t.Logf("recorded: placement %s %s: page %d run %d: CER %.4f WER %.4f, %d input tokens, %d output tokens, status %s%s",
							placement, s.name, cl.Page, cl.Run, rate(cl.CharEdits, cl.Chars), rate(cl.WordEdits, cl.Words),
							cl.InputTokens, cl.OutputTokens, cl.Status, errSuffix(cl.Error))
					}
					t.Logf("recorded: placement %s %s: run %d, %d pages at concurrency %d in %v",
						placement, s.name, run, len(cells), inFlight, elapsed.Round(time.Millisecond))
					res.Cells = append(res.Cells, cells...)
				}
				saveJSON(t, fmt.Sprintf("placement-%s-%s.json", placement, s.name), res, func() {
					placementRuns.Lock()
					defer placementRuns.Unlock()
					placementRuns.runs = append(placementRuns.runs, res)
				})
			})
		}
	}
}

// saveJSON writes v as name in BAKEOFF_RESULTS when it is set, and else calls keep, which holds
// it in memory.
func saveJSON(t *testing.T, name string, v any, keep func()) {
	t.Helper()
	dir := os.Getenv("BAKEOFF_RESULTS")
	if dir == "" {
		keep()
		return
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// loadJSON returns the results in BAKEOFF_RESULTS whose names match pattern, each decoded into a
// T, or held() when BAKEOFF_RESULTS is unset.
func loadJSON[T any](t *testing.T, pattern string, held func() []T) []T {
	t.Helper()
	dir := os.Getenv("BAKEOFF_RESULTS")
	if dir == "" {
		return held()
	}
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	var rs []T
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var res T
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		rs = append(rs, res)
	}
	return rs
}

// TestPlacementReport logs the placements TestPlacement ran as recorded lines, for each set:
// each placement's CER and WER over the completed transcriptions, with their spread over the
// runs, its incomplete transcriptions, and its mean run time; then each page's mean CER per
// placement.
func TestPlacementReport(t *testing.T) {
	rs := loadJSON(t, "placement-*.json", func() []placementResult {
		placementRuns.Lock()
		defer placementRuns.Unlock()
		return slices.Clone(placementRuns.runs)
	})
	if len(rs) == 0 {
		t.Skip("no placement results: run TestPlacement first")
	}
	t.Setenv("BAKEOFF_SET", "")
	truth := map[string]int{}
	for _, s := range loadSets(t) {
		truth[s.name] = len(s.truth)
	}
	byKey := map[string]placementResult{}
	for _, res := range rs {
		byKey[res.Placement+"/"+res.Set] = res
		// A run missing a page would score the page as perfect.
		if want := truth[res.Set] * bakeoffRuns; len(res.Cells) != want {
			t.Errorf("placement %s %s has %d cells, want %d: a run is missing", res.Placement, res.Set, len(res.Cells), want)
		}
	}
	t.Log("recorded: placement: beside is transcribeInstructions as a text part before the image in its user message; instructions is the same text as the request's Instructions, the image alone in its message; a run's CER and WER are its edits over its truth's length, the set's completed pages together; incomplete is the transcriptions that didn't complete, of all")
	for _, set := range setNames {
		rows := []string{"placement\tCER mean\tCER min..max\tWER mean\tWER min..max\tincomplete\trun s mean\t"}
		var present []placementResult
		for _, placement := range placements {
			res, ok := byKey[placement+"/"+set]
			if !ok {
				continue
			}
			present = append(present, res)
			s := summarize(result{Cells: res.Cells}, set)
			rows = append(rows, fmt.Sprintf("%s\t%.4f\t%.4f..%.4f\t%.4f\t%.4f..%.4f\t%s\t%.1f\t",
				placement, s.cer.mean, s.cer.lo, s.cer.hi, s.wer.mean, s.wer.lo, s.wer.hi, s.incompleteRate(), seconds(res.RunTimes).mean))
		}
		if len(present) == 0 {
			continue
		}
		for _, line := range table(rows) {
			t.Logf("recorded: placement %s: %s", set, line)
		}
		header := "page"
		for _, res := range present {
			header += "\t" + res.Placement
		}
		pageRows := []string{header + "\t"}
		for page := 1; page <= truth[set]; page++ {
			row := fmt.Sprint(page)
			for _, res := range present {
				row += "\t" + pageMean(res.Cells, page)
			}
			pageRows = append(pageRows, row+"\t")
		}
		for _, line := range table(pageRows) {
			t.Logf("recorded: placement %s per page, mean CER over completed runs: %s", set, line)
		}
	}
}

// seconds returns the spread of durations in seconds.
func seconds(ds []time.Duration) spread {
	var secs []float64
	for _, d := range ds {
		secs = append(secs, d.Seconds())
	}
	return spreadOf(secs)
}

// pageMean formats one page's mean CER over the runs that completed, with its range when the
// runs differ and its incomplete runs, or "-" when none completed.
func pageMean(cells []cell, page int) string {
	s, inc, ok := pageSpread(cells, page)
	out := "-"
	if ok {
		out = fmt.Sprintf("%.4f", s.mean)
		if s.lo != s.hi {
			out += fmt.Sprintf(" (%.4f..%.4f)", s.lo, s.hi)
		}
	}
	if inc > 0 {
		out += fmt.Sprintf(" (%d inc)", inc)
	}
	return out
}
