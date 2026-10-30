//go:build bakeoff && linux

package document_test

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// Evidence 11, the corrections matrix: `mise run bakeoff-corrections` runs TestCorrections once
// per DPI, fixture set, and matrix cell, each run capped at 10 minutes, then TestCorrectionsMatrix
// over their results. TestCorrections sends every page of the degraded and scanned sets through
// the vision harness's pages (vision_test.go), on the released Images stream, with the released
// renderer, Ghostscript, and the corrections stage over the host's magick, to Gemma 4 26B-A4B on
// the router SPIKE_ROUTER_URL names, three greedy runs per cell, and scores each page as the
// renderer bake-off does; TestCorrectionsMatrix logs each DPI's tables, per set and cell and per
// page, as recorded lines.
//
// BAKEOFF_DPI names the DPIs to run, BAKEOFF_SET the fixture sets, and BAKEOFF_CELL the matrix
// cells, each comma-separated; unset runs 150 and 72 dpi, degraded and scanned, and every cell.
// BAKEOFF_RESULTS names a directory each run's result is written to as
// corrections-<dpi>-<set>-<cell>.json, which the matrix reads; unset keeps the results in memory,
// so one go test run of both tests reports what it ran.

// matrixCell is one cell of the matrix: its name and the corrections it applies.
type matrixCell struct {
	name        string
	corrections []document.Correction
}

// matrixCells are the seven cells, in the order the tables give them: none, each correction
// alone, and all five together.
var matrixCells = []matrixCell{
	{"none", nil},
	{"Deskew", []document.Correction{document.Deskew}},
	{"Despeckle", []document.Correction{document.Despeckle}},
	{"ContrastStretch", []document.Correction{document.ContrastStretch}},
	{"Unsharp", []document.Correction{document.Unsharp}},
	{"Grayscale", []document.Correction{document.Grayscale}},
	{"all", []document.Correction{document.Deskew, document.Despeckle, document.ContrastStretch, document.Unsharp, document.Grayscale}},
}

var (
	// matrixDPIs are the DPIs the matrix runs at: the default, and 72, since every synthetic
	// fault scored 0 at 150 dpi in the renderer bake-off and a lower resolution may leave a
	// correction something to fix.
	matrixDPIs = []int{150, 72}
	// matrixSets are the fixture sets the matrix runs on: the ones with faults to correct.
	matrixSets = []string{"degraded", "scanned"}
)

// correctionResult is one run of TestCorrections: a cell's three runs over one set at one DPI.
type correctionResult struct {
	DPI  int    `json:"dpi"`
	Set  string `json:"set"`
	Cell string `json:"cell"`
	// Versions are the renderer's and the corrections stage's tools.
	Versions []string `json:"versions"`
	// Cells are its pages, one per page and run, scored. Their render measures are zero: the
	// matrix renders through the vision harness, which measures nothing.
	Cells []cell `json:"cells"`
	// RunTimes are each run's wall-clock time, from the first page's rendering to the last
	// page's response.
	RunTimes []time.Duration `json:"run_times"`
}

// corrections holds the results of this process's TestCorrections runs for
// TestCorrectionsMatrix when BAKEOFF_RESULTS is unset.
var corrections struct {
	sync.Mutex
	runs []correctionResult
}

// selectedDPIs returns the DPIs BAKEOFF_DPI names, matrixDPIs when it is unset, failing t on a
// value that isn't a positive integer.
func selectedDPIs(t *testing.T) []int {
	t.Helper()
	env := os.Getenv("BAKEOFF_DPI")
	if env == "" {
		return matrixDPIs
	}
	var dpis []int
	for s := range strings.SplitSeq(env, ",") {
		dpi, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || dpi <= 0 {
			t.Fatalf("BAKEOFF_DPI names %q, which is no positive DPI", s)
		}
		dpis = append(dpis, dpi)
	}
	return dpis
}

// selectedCells returns the matrix cells BAKEOFF_CELL names, all of them when it is unset,
// failing t on a name that is none.
func selectedCells(t *testing.T) []matrixCell {
	t.Helper()
	env := os.Getenv("BAKEOFF_CELL")
	if env == "" {
		return matrixCells
	}
	var cs []matrixCell
	for name := range strings.SplitSeq(env, ",") {
		i := slices.IndexFunc(matrixCells, func(c matrixCell) bool { return c.name == strings.TrimSpace(name) })
		if i < 0 {
			t.Fatalf("BAKEOFF_CELL names %q, which is no matrix cell", name)
		}
		cs = append(cs, matrixCells[i])
	}
	return cs
}

// timedGenerator bounds each request of a vision's to requestTimeout, as the renderer bake-off
// bounds each of its own: a vision's context spans every page of a run.
type timedGenerator struct{ gen generator }

func (g timedGenerator) Generate(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return g.gen.Generate(ctx, req)
}

// matrixTemplate is each page's request: the renderer bake-off's model, output bound, greedy
// sampling, and reasoning off, with transcribeInstructions as the request's instructions, as the
// integration tier's vision sends them. The vision harness appends the page's image as a user
// message of its own, so the instructions can't share the image's message as they do in the
// renderer bake-off.
func matrixTemplate() *protocol.Request {
	return &protocol.Request{
		Model:           bakeoffModel,
		Instructions:    transcribeInstructions,
		Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
		MaxOutputTokens: maxOutputTokens,
		Native:          jsontext.Value(`{"temperature":0}`),
	}
}

func TestCorrections(t *testing.T) {
	c := bakeoffClient(t)
	gs := hostTool(t, document.GhostscriptSpec())
	g, err := document.NewGhostscript(gs)
	if err != nil {
		t.Fatal(err)
	}
	magick := hostTool(t, document.MagickSpec())
	corr, err := document.NewCorrector(magick)
	if err != nil {
		t.Fatal(err)
	}
	v, err := newVision(timedGenerator{c}, g, corr)
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{fmt.Sprintf("%s %s", gs.Name(), gs.Version()), fmt.Sprintf("%s %s", magick.Name(), magick.Version())}
	if os.Getenv("BAKEOFF_SET") == "" {
		t.Setenv("BAKEOFF_SET", strings.Join(matrixSets, ","))
	}
	sets := loadSets(t)
	for _, dpi := range selectedDPIs(t) {
		for _, s := range sets {
			for _, mc := range selectedCells(t) {
				t.Run(fmt.Sprintf("%ddpi/%s/%s", dpi, s.name, mc.name), func(t *testing.T) {
					res := correctionResult{DPI: dpi, Set: s.name, Cell: mc.name, Versions: versions}
					for run := 1; run <= bakeoffRuns; run++ {
						start := time.Now()
						cells := correctedRun(t, v, s, dpi, mc, run)
						elapsed := time.Since(start)
						res.RunTimes = append(res.RunTimes, elapsed)
						for _, cl := range cells {
							t.Logf("recorded: corrections %d dpi %s %s: page %d run %d: CER %.4f WER %.4f, %d input tokens, %d output tokens, status %s%s",
								dpi, s.name, mc.name, cl.Page, cl.Run, rate(cl.CharEdits, cl.Chars), rate(cl.WordEdits, cl.Words),
								cl.InputTokens, cl.OutputTokens, cl.Status, errSuffix(cl.Error))
						}
						t.Logf("recorded: corrections %d dpi %s %s: run %d, %d pages at concurrency %d in %v",
							dpi, s.name, mc.name, run, len(cells), inFlight, elapsed.Round(time.Millisecond))
						res.Cells = append(res.Cells, cells...)
					}
					saveCorrections(t, res)
				})
			}
		}
	}
}

// correctedRun runs every page of s through v at dpi with mc's corrections, inFlight pages at
// once and no page retries, and returns each page's cell, scored against its truth. A page that
// fails is t's error, and its cell scores as an empty transcription, incomplete; so does a page
// whose response isn't completed, as in the renderer bake-off. A stream that ends with an error
// of its own fails t.
func correctedRun(t *testing.T, v *vision, s fixtureSet, dpi int, mc matrixCell, run int) []cell {
	t.Helper()
	f, err := os.Open(s.pdf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	req := visionRequest{
		Dir:         t.TempDir(),
		Template:    matrixTemplate(),
		DPI:         dpi,
		Corrections: mc.corrections,
		Concurrency: inFlight,
	}
	var cells []cell
	for p, err := range v.pages(t.Context(), f, req) {
		if p.Number == 0 {
			t.Fatalf("%d dpi %s %s run %d: %v", dpi, s.name, mc.name, run, err)
		}
		cl := cell{Set: s.name, Page: p.Number, Run: run}
		switch {
		case err != nil:
			cl.Error = err.Error()
			t.Errorf("%d dpi %s %s page %d run %d: %v", dpi, s.name, mc.name, p.Number, run, err)
		case p.Response != nil:
			cl.Status = string(p.Response.Status)
			cl.Output = text(p.Response)
			cl.InputTokens = p.Response.Usage.InputTokens
			cl.OutputTokens = p.Response.Usage.OutputTokens
			cl.ReasoningTokens = p.Response.Usage.OutputTokensDetails.ReasoningTokens
		}
		truth := s.truth[p.Number-1]
		cl.CharEdits, cl.Chars = charErrors(truth, cl.Output)
		cl.WordEdits, cl.Words = wordErrors(truth, cl.Output)
		cells = append(cells, cl)
	}
	if len(cells) != len(s.truth) {
		t.Fatalf("%d dpi %s %s run %d: %d pages, want %d", dpi, s.name, mc.name, run, len(cells), len(s.truth))
	}
	return cells
}

// saveCorrections keeps res for the matrix: in BAKEOFF_RESULTS when it is set, else in memory.
func saveCorrections(t *testing.T, res correctionResult) {
	t.Helper()
	dir := os.Getenv("BAKEOFF_RESULTS")
	if dir == "" {
		corrections.Lock()
		defer corrections.Unlock()
		corrections.runs = append(corrections.runs, res)
		return
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("corrections-%d-%s-%s.json", res.DPI, res.Set, res.Cell)
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// loadCorrections returns the results saveCorrections kept.
func loadCorrections(t *testing.T) []correctionResult {
	t.Helper()
	dir := os.Getenv("BAKEOFF_RESULTS")
	if dir == "" {
		corrections.Lock()
		defer corrections.Unlock()
		return slices.Clone(corrections.runs)
	}
	files, err := filepath.Glob(filepath.Join(dir, "corrections-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rs []correctionResult
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var res correctionResult
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		rs = append(rs, res)
	}
	return rs
}

// pageSpread returns the spread of one page's character error rate over the runs whose
// transcription completed, and how many runs didn't. ok is false when no run completed.
func pageSpread(cells []cell, page int) (s spread, incomplete int, ok bool) {
	var rates []float64
	for _, cl := range cells {
		if cl.Page != page {
			continue
		}
		if cl.incomplete() {
			incomplete++
			continue
		}
		rates = append(rates, rate(cl.CharEdits, cl.Chars))
	}
	if len(rates) == 0 {
		return spread{}, incomplete, false
	}
	return spreadOf(rates), incomplete, true
}

// verdict names how a cell's spread compares with none's: better, worse, or a tie inside the
// run-to-run spread.
func verdict(s, none spread) string {
	switch s.compare(none) {
	case -1:
		return "helps"
	case 1:
		return "hurts"
	}
	return "tie"
}

// TestCorrectionsMatrix logs the matrix TestCorrections ran as recorded lines, for each DPI and
// set: each cell's CER and WER over the completed transcriptions, with their spread over the
// runs and its incomplete transcriptions, against none; each page's mean CER per cell; and every
// page where a cell's spread clears none's, better or worse.
func TestCorrectionsMatrix(t *testing.T) {
	rs := loadCorrections(t)
	if len(rs) == 0 {
		t.Skip("no corrections results: run TestCorrections first")
	}
	t.Setenv("BAKEOFF_SET", strings.Join(matrixSets, ","))
	truth := map[string]int{}
	for _, s := range loadSets(t) {
		truth[s.name] = len(s.truth)
	}
	byKey := map[string]correctionResult{}
	var dpis []int
	for _, res := range rs {
		byKey[fmt.Sprintf("%d/%s/%s", res.DPI, res.Set, res.Cell)] = res
		if !slices.Contains(dpis, res.DPI) {
			dpis = append(dpis, res.DPI)
		}
		// A run missing a page would score the page as perfect.
		if want := truth[res.Set] * bakeoffRuns; len(res.Cells) != want {
			t.Errorf("%d dpi %s %s has %d cells, want %d: a run is missing", res.DPI, res.Set, res.Cell, len(res.Cells), want)
		}
	}
	slices.SortFunc(dpis, func(a, b int) int { return -cmp.Compare(a, b) })
	t.Log("recorded: corrections: a run's CER and WER are its edits over its truth's length, the set's completed pages together; incomplete is the transcriptions that didn't complete, of all; vs none is a tie when the two cells' spreads over the runs overlap")
	for _, dpi := range dpis {
		for _, set := range matrixSets {
			none, ok := byKey[fmt.Sprintf("%d/%s/none", dpi, set)]
			if !ok {
				t.Logf("recorded: corrections %d dpi %s: no none cell, so no comparison", dpi, set)
				continue
			}
			noneSum := summarize(result{Cells: none.Cells}, set)
			rows := []string{"cell\tCER mean\tCER min..max\tWER mean\tWER min..max\tincomplete\tCER vs none\trun s mean\t"}
			var present []correctionResult
			for _, mc := range matrixCells {
				res, ok := byKey[fmt.Sprintf("%d/%s/%s", dpi, set, mc.name)]
				if !ok {
					continue
				}
				present = append(present, res)
				s := summarize(result{Cells: res.Cells}, set)
				var secs []float64
				for _, d := range res.RunTimes {
					secs = append(secs, d.Seconds())
				}
				rows = append(rows, fmt.Sprintf("%s\t%.4f\t%.4f..%.4f\t%.4f\t%.4f..%.4f\t%s\t%+.4f %s\t%.1f\t",
					mc.name, s.cer.mean, s.cer.lo, s.cer.hi, s.wer.mean, s.wer.lo, s.wer.hi, s.incompleteRate(),
					s.cer.mean-noneSum.cer.mean, verdict(s.cer, noneSum.cer), spreadOf(secs).mean))
			}
			for _, line := range table(rows) {
				t.Logf("recorded: corrections %d dpi %s: %s", dpi, set, line)
			}
			header := "page"
			for _, res := range present {
				header += "\t" + res.Cell
			}
			pageRows := []string{header + "\t"}
			var notes []string
			for page := 1; page <= truth[set]; page++ {
				ns, _, nok := pageSpread(none.Cells, page)
				row := strconv.Itoa(page)
				for _, res := range present {
					s, inc, ok := pageSpread(res.Cells, page)
					switch {
					case !ok:
						row += "\t-"
					default:
						row += fmt.Sprintf("\t%.4f", s.mean)
					}
					if inc > 0 {
						row += fmt.Sprintf(" (%d inc)", inc)
					}
					if ok && nok && res.Cell != "none" {
						if d := verdict(s, ns); d != "tie" {
							notes = append(notes, fmt.Sprintf("page %d: %s %s, CER %.4f (%.4f..%.4f) against none's %.4f (%.4f..%.4f)",
								page, res.Cell, d, s.mean, s.lo, s.hi, ns.mean, ns.lo, ns.hi))
						}
					}
				}
				pageRows = append(pageRows, row+"\t")
			}
			for _, line := range table(pageRows) {
				t.Logf("recorded: corrections %d dpi %s per page, mean CER over completed runs: %s", dpi, set, line)
			}
			if len(notes) == 0 {
				t.Logf("recorded: corrections %d dpi %s: no page's CER under any correction clears none's spread", dpi, set)
			}
			for _, n := range notes {
				t.Logf("recorded: corrections %d dpi %s: %s", dpi, set, n)
			}
		}
	}
}
