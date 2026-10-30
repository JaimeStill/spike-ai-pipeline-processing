//go:build bakeoff && linux

package document_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"image/png"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-client/format/completions"
	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
)

// The bake-off tier: `mise run bakeoff` runs TestBakeoff once per renderer, each run capped at
// 10 minutes, then TestBakeoffRanking over their results. TestBakeoff renders every fixture page
// with each renderer it is given, sends each page's PNG to Gemma 4 26B-A4B on the router
// SPIKE_ROUTER_URL names, scores the transcription against the page's ground truth, and writes
// the renderer's result; TestBakeoffRanking ranks the renderers and logs the tables as recorded
// lines.
//
// BAKEOFF_RENDERER names the renderers to run and BAKEOFF_SET the fixture sets, each
// comma-separated; unset runs all of them. BAKEOFF_RESULTS names a directory each run's result
// for a renderer is written to as <renderer>-<sets>.json, and the ranking reads and merges them;
// unset keeps the results in memory, so one go test run of both tests ranks what it ran.

const (
	// bakeoffModel is the vision model the transcriptions come from, which set A loads.
	bakeoffModel = "google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0"
	// bakeoffDPI is the DPI every page is rendered at.
	bakeoffDPI = 150
	// bakeoffRuns is how often each page is rendered and transcribed, for the run-to-run spread.
	bakeoffRuns = 3
	// inFlight is how many requests the router has at once.
	inFlight = 4
	// maxOutputTokens bounds a transcription: well over the longest page's text. The request
	// turns reasoning off, so the bound is the text's alone.
	maxOutputTokens = 8192
	// requestTimeout bounds one transcription.
	requestTimeout = 5 * time.Minute
)

// fixtureSet is one fixture PDF and its pages' ground truth.
type fixtureSet struct {
	name, pdf string
	truth     []string
}

// setNames are the fixture sets, in the order the report gives them.
var setNames = []string{"clean", "degraded", "scanned"}

// fixturePDF returns the absolute path of set's PDF in testdata.
func fixturePDF(t *testing.T, set string) string {
	t.Helper()
	pdf, err := filepath.Abs(filepath.Join("testdata", set+".pdf"))
	if err != nil {
		t.Fatal(err)
	}
	return pdf
}

// loadSets reads the fixture sets BAKEOFF_SET names, all of them when it is unset, from
// testdata: each set's PDF by absolute path, and its pages' truth from <set>/page-N.txt, in page
// order.
func loadSets(t *testing.T) []fixtureSet {
	t.Helper()
	names := setNames
	if env := os.Getenv("BAKEOFF_SET"); env != "" {
		names = nil
		for name := range strings.SplitSeq(env, ",") {
			name = strings.TrimSpace(name)
			if !slices.Contains(setNames, name) {
				t.Fatalf("BAKEOFF_SET names %q, which is no fixture set", name)
			}
			names = append(names, name)
		}
	}
	var sets []fixtureSet
	for _, name := range names {
		s := fixtureSet{name: name, pdf: fixturePDF(t, name)}
		for page := 1; ; page++ {
			b, err := os.ReadFile(filepath.Join("testdata", name, fmt.Sprintf("page-%d.txt", page)))
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			s.truth = append(s.truth, string(b))
		}
		sets = append(sets, s)
	}
	return sets
}

// cell is one page of one set from one run of a renderer: its render's measures and its
// transcription's scores.
type cell struct {
	Set  string `json:"set"`
	Page int    `json:"page"`
	Run  int    `json:"run"`
	// RenderTime is the render's wall-clock time, and UserCPU and SystemCPU the rendering
	// tool's usage. PeakRSS is the rendering program's peak resident memory, from a second
	// render of the page through the launcher, since a tool.Usage has none.
	RenderTime time.Duration `json:"render_time"`
	PeakRSS    int64         `json:"peak_rss"`
	UserCPU    time.Duration `json:"user_cpu"`
	SystemCPU  time.Duration `json:"system_cpu"`
	PNGBytes   int           `json:"png_bytes"`
	// CharEdits over Chars is the character error rate, WordEdits over Words the word error
	// rate.
	CharEdits int `json:"char_edits"`
	Chars     int `json:"chars"`
	WordEdits int `json:"word_edits"`
	Words     int `json:"words"`
	// RequestTime is the transcription's wall-clock time, from sending the request to its
	// response.
	RequestTime time.Duration `json:"request_time"`
	// Status is the response's status, and Output its text.
	Status string `json:"status"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
	// InputTokens, OutputTokens, and ReasoningTokens are the response's usage. InputTokens, the
	// page image's tokens and the instructions', is absent from results written before it was
	// recorded.
	InputTokens     int `json:"input_tokens,omitempty"`
	OutputTokens    int `json:"output_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
	// png is the rendered page, held until its request is sent.
	png []byte
}

// incomplete reports whether cl's transcription didn't complete: its request failed, its
// response's status isn't completed, or it has no text. Its error rates aren't the renderer's,
// so the error rates leave it out and the ranking counts it instead.
func (cl cell) incomplete() bool {
	return cl.Status != string(protocol.StatusCompleted) || strings.TrimSpace(cl.Output) == ""
}

// result is one renderer's bake-off: its fixed properties, whether it streams, and its cells.
type result struct {
	Name         string   `json:"name"`
	Versions     []string `json:"versions"`
	Tools        []string `json:"tools"`
	Packages     []string `json:"packages"`
	FootprintMiB float64  `json:"footprint_mib"`
	License      string   `json:"license"`
	LicenseRank  int      `json:"license_rank"`
	// Streams is whether the renderer read a PDF on stdin and wrote a page's PNG on stdout, as
	// probed, one page per process.
	Streams bool `json:"streams"`
	// LaunchFloor is the launcher's floor in bytes: a PeakRSS at it is at most it.
	LaunchFloor int64         `json:"launch_floor"`
	StreamError string        `json:"stream_error,omitempty"`
	Cells       []cell        `json:"cells"`
	Elapsed     time.Duration `json:"elapsed"`
}

// results holds the results of this process's TestBakeoff runs for TestBakeoffRanking when
// BAKEOFF_RESULTS is unset.
var results struct {
	sync.Mutex
	runs []result
}

// bakeoffClient returns a Client of the router SPIKE_ROUTER_URL names, speaking Chat
// Completions, with autoload=false so a request fails rather than load a model. It skips t when
// SPIKE_ROUTER_URL is unset.
func bakeoffClient(t *testing.T) *client.Client {
	t.Helper()
	base := os.Getenv("SPIKE_ROUTER_URL")
	if base == "" {
		t.Skip("SPIKE_ROUTER_URL is unset: set it to the router to test against, such as http://gb10-2:8080")
	}
	c, err := client.New(client.Config{
		BaseURL: strings.TrimSuffix(base, "/") + "/v1",
		Query:   url.Values{"autoload": {"false"}},
		HTTP:    &http.Client{},
		Format:  completions.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// selected returns the candidates BAKEOFF_RENDERER names, all of them when it is unset, failing
// t on a name that is none.
func selected(t *testing.T) []candidate {
	t.Helper()
	env := os.Getenv("BAKEOFF_RENDERER")
	if env == "" {
		return candidates
	}
	var cs []candidate
	for name := range strings.SplitSeq(env, ",") {
		i := slices.IndexFunc(candidates, func(c candidate) bool { return c.name == strings.TrimSpace(name) })
		if i < 0 {
			t.Fatalf("BAKEOFF_RENDERER names %q, which is no renderer", name)
		}
		cs = append(cs, candidates[i])
	}
	return cs
}

func TestBakeoff(t *testing.T) {
	c := bakeoffClient(t)
	sets := loadSets(t)
	for _, cand := range selected(t) {
		t.Run(cand.name, func(t *testing.T) {
			start := time.Now()
			r := cand.build(t)
			res := result{
				Name: cand.name, Tools: cand.tools, Packages: cand.packages,
				FootprintMiB: cand.footprintMiB(), License: cand.license, LicenseRank: cand.licenseRank,
				Versions: []string{fmt.Sprintf("%s %s", r.render.Name(), r.render.Version())},
			}
			for _, p := range cand.packages {
				f := footprint[p]
				t.Logf("recorded: bakeoff %s: package %s %s, %.2f MiB installed, licenses %s (pacman -Qi on the laptop, 2026-10-29)",
					cand.name, p, f.version, f.mib, f.licenses)
			}
			res.Streams, res.StreamError = probeStream(t, r, fixturePDF(t, "clean"))
			floor, err := r.launchFloor(t.Context())
			if err != nil {
				t.Fatalf("the launcher's floor: %v", err)
			}
			res.LaunchFloor = floor
			cells := renderAll(t, r, sets)
			transcribeAll(t, c, sets, cells, func(cl *cell) {
				t.Logf("recorded: bakeoff %s: %s page %d run %d: CER %.4f WER %.4f, render %v (CPU %v user, %v system), peak %.1f MiB, %d PNG bytes; transcribed in %v, %d output tokens (%d reasoning), status %s%s",
					cand.name, cl.Set, cl.Page, cl.Run, rate(cl.CharEdits, cl.Chars), rate(cl.WordEdits, cl.Words),
					cl.RenderTime.Round(time.Millisecond), cl.UserCPU.Round(time.Millisecond), cl.SystemCPU.Round(time.Millisecond),
					mib(cl.PeakRSS), cl.PNGBytes, cl.RequestTime.Round(time.Millisecond),
					cl.OutputTokens, cl.ReasoningTokens, cl.Status, errSuffix(cl.Error))
			})
			res.Cells = cells
			res.Elapsed = time.Since(start)
			var names []string
			for _, s := range sets {
				names = append(names, s.name)
			}
			t.Logf("recorded: bakeoff %s: %s, streams %v%s, launcher floor %.1f MiB, sets %s, %d cells in %v",
				cand.name, strings.Join(res.Versions, ", "), res.Streams, errSuffix(res.StreamError), mib(res.LaunchFloor),
				strings.Join(names, "+"), len(cells), res.Elapsed.Round(time.Second))
			saveResult(t, res, strings.Join(names, "+"))
		})
	}
}

// errSuffix returns ", error: msg" for a non-empty msg.
func errSuffix(msg string) string {
	if msg == "" {
		return ""
	}
	return ", error: " + msg
}

// mib returns bytes in MiB.
func mib(bytes int64) float64 { return float64(bytes) / (1 << 20) }

// probeStream renders page 1 of the PDF at pdf from stdin and reports whether the renderer wrote
// a PNG to stdout, with the error when it didn't.
func probeStream(t *testing.T, r *cliRenderer, pdf string) (bool, string) {
	t.Helper()
	b, err := os.ReadFile(pdf)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := r.measure(t.Context(), "", bytes.NewReader(b), 1, bakeoffDPI, &out); err != nil {
		return false, err.Error()
	}
	if _, err := png.Decode(&out); err != nil {
		return false, fmt.Sprintf("stdout isn't a PNG: %v", err)
	}
	return true, ""
}

// renderAll renders every page of every set bakeoffRuns times, one page at a time so each
// render's time and memory are its own, after checking the renderer's count of each set's pages.
// Each page renders twice: once directly, for its time, CPU, and PNG, and once through the
// launcher, for its peak memory.
// Every page's PNG must decode and be letter size at bakeoffDPI.
func renderAll(t *testing.T, r *cliRenderer, sets []fixtureSet) []cell {
	t.Helper()
	for _, s := range sets {
		n, err := r.Count(t.Context(), s.pdf)
		if err != nil {
			t.Fatalf("Count(%s): %v", s.name, err)
		}
		if n != len(s.truth) {
			t.Fatalf("Count(%s) = %d, want %d, its pages of ground truth", s.name, n, len(s.truth))
		}
	}
	var cells []cell
	for run := 1; run <= bakeoffRuns; run++ {
		for _, s := range sets {
			for page := 1; page <= len(s.truth); page++ {
				var out bytes.Buffer
				start := time.Now()
				usage, err := r.measure(t.Context(), s.pdf, nil, page, bakeoffDPI, &out)
				elapsed := time.Since(start)
				if err != nil {
					t.Fatalf("render %s page %d: %v", s.name, page, err)
				}
				cfg, err := png.DecodeConfig(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatalf("%s page %d isn't a PNG: %v", s.name, page, err)
				}
				if cfg.Width != 1275 || cfg.Height != 1650 {
					t.Errorf("%s page %d is %dx%d, want letter at %d dpi, 1275x1650", s.name, page, cfg.Width, cfg.Height, bakeoffDPI)
				}
				peak, err := r.peakMemory(t.Context(), s.pdf, page, bakeoffDPI)
				if err != nil {
					t.Fatalf("render %s page %d through the launcher: %v", s.name, page, err)
				}
				cells = append(cells, cell{
					Set: s.name, Page: page, Run: run,
					RenderTime: elapsed, PeakRSS: peak,
					UserCPU: usage.UserCPU, SystemCPU: usage.SystemCPU,
					PNGBytes: out.Len(), png: out.Bytes(),
				})
			}
		}
	}
	return cells
}

// transcribeAll sends every cell's page to the model, inFlight at a time, scores each
// transcription against its page's truth, and passes each cell to done as it is scored. A failed
// request is t's error, and its cell scores as an empty transcription.
func transcribeAll(t *testing.T, c *client.Client, sets []fixtureSet, cells []cell, done func(*cell)) {
	t.Helper()
	truth := map[string][]string{}
	for _, s := range sets {
		truth[s.name] = s.truth
	}
	work := make(chan *cell)
	var wg sync.WaitGroup
	for range inFlight {
		wg.Go(func() {
			for cl := range work {
				transcribe(t, c, cl)
				pageTruth := truth[cl.Set][cl.Page-1]
				cl.CharEdits, cl.Chars = charErrors(pageTruth, cl.Output)
				cl.WordEdits, cl.Words = wordErrors(pageTruth, cl.Output)
				cl.png = nil
				done(cl)
			}
		})
	}
	for i := range cells {
		work <- &cells[i]
	}
	close(work)
	wg.Wait()
}

// transcribe sends cl's page to the model with transcribeInstructions in one user message,
// greedy and with reasoning off, and sets cl's output, status, and usage, or its error.
//
// Reasoning is off through the typed Reasoning.Effort, none, which the completions format sends
// as reasoning_effort. Verified on gb10-2's router, llama.cpp b11669, with scanned page 7: with
// no effort the model reasoned to the 8192-token cap and returned no text (8192 completion
// tokens, finish length, about 22,000 characters of reasoning_content); with reasoning_effort
// none it answered in 56 tokens, finish stop, with no reasoning_content, its prompt 533 tokens as
// with chat_template_kwargs {"enable_thinking":false}, Gemma 4's own switch, against 536 with
// thinking on.
func transcribe(t *testing.T, c *client.Client, cl *cell) {
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	defer func() { cl.RequestTime = time.Since(start) }()
	resp, err := c.Generate(ctx, &protocol.Request{
		Model: bakeoffModel,
		Input: []protocol.Item{protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
			protocol.InputText{Text: transcribeInstructions},
			protocol.InputImage{ImageURL: protocol.DataURL("image/png", cl.png)},
		}}},
		Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
		MaxOutputTokens: maxOutputTokens,
		Native:          jsontext.Value(`{"temperature":0}`),
	})
	if err != nil {
		cl.Error = err.Error()
		t.Errorf("%s page %d run %d: Generate: %v", cl.Set, cl.Page, cl.Run, err)
		return
	}
	cl.Status = string(resp.Status)
	cl.InputTokens = resp.Usage.InputTokens
	cl.OutputTokens = resp.Usage.OutputTokens
	cl.ReasoningTokens = resp.Usage.OutputTokensDetails.ReasoningTokens
	var text strings.Builder
	for _, it := range resp.Output {
		if m, ok := it.(protocol.Message); ok {
			for _, p := range m.Content {
				if o, ok := p.(protocol.OutputText); ok {
					text.WriteString(o.Text)
				}
			}
		}
	}
	cl.Output = text.String()
}

// saveResult keeps res, a renderer's run over the sets named, for the ranking: in
// BAKEOFF_RESULTS when it is set, else in memory.
func saveResult(t *testing.T, res result, sets string) {
	t.Helper()
	dir := os.Getenv("BAKEOFF_RESULTS")
	if dir == "" {
		results.Lock()
		defer results.Unlock()
		results.runs = append(results.runs, res)
		return
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, res.Name+"-"+sets+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// loadResults returns the results saveResult kept, in the candidates' order, a renderer's runs
// over several sets merged into one result: their cells together, their streaming the first
// run's, and their elapsed times summed.
func loadResults(t *testing.T) []result {
	t.Helper()
	var kept []result
	if dir := os.Getenv("BAKEOFF_RESULTS"); dir == "" {
		results.Lock()
		kept = slices.Clone(results.runs)
		results.Unlock()
	} else {
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var res result
			if err := json.Unmarshal(b, &res); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			kept = append(kept, res)
		}
	}
	var rs []result
	for _, cand := range candidates {
		var merged *result
		for _, res := range kept {
			if res.Name != cand.name {
				continue
			}
			if merged == nil {
				merged = &res
				continue
			}
			merged.Cells = append(merged.Cells, res.Cells...)
			merged.Elapsed += res.Elapsed
		}
		if merged != nil {
			rs = append(rs, *merged)
		}
	}
	return rs
}

// spread is a measure over the runs: its mean, and its least and greatest run.
type spread struct{ mean, lo, hi float64 }

// spreadOf returns the spread of the runs' values.
func spreadOf(values []float64) spread {
	s := spread{lo: math.Inf(1), hi: math.Inf(-1)}
	for _, v := range values {
		s.mean += v / float64(len(values))
		s.lo = min(s.lo, v)
		s.hi = max(s.hi, v)
	}
	return s
}

// overlaps reports whether two spreads' ranges meet: a difference inside the run-to-run spread,
// which the ranking calls a tie.
func (s spread) overlaps(o spread) bool { return s.lo <= o.hi && o.lo <= s.hi }

// compare orders two spreads, lower first, 0 when they overlap.
func (s spread) compare(o spread) int {
	if s.overlaps(o) {
		return 0
	}
	return cmp.Compare(s.mean, o.mean)
}

// summary is one renderer's measures over a set of cells, each a spread over the runs.
type summary struct {
	// cer and wer are over the completed transcriptions alone.
	cer, wer spread
	// incomplete is the count of the cells whose transcription didn't complete, of cells.
	incomplete, cells int
	// renderTime is the mean render time per page, in seconds, and peakRSS the largest peak
	// over the pages, in MiB.
	renderTime, peakRSS spread
	// pngBytes is the mean PNG size per page, which the runs share.
	pngBytes float64
}

// summarize returns res's measures over its cells in sets. A run's error rates are its edits
// over its truth's length, the completed pages of a set together; over several sets they are
// the mean of the rates of the sets the run completed a page of, so each set weighs the same
// however much text it has. A run that completed no page of the sets has no error rate, and the
// spread leaves it out. An incomplete page counts toward incomplete instead.
func summarize(res result, sets ...string) summary {
	var cer, wer, renderTime, peak []float64
	var pngBytes, pngPages float64
	var sum summary
	for run := 1; run <= bakeoffRuns; run++ {
		var setCER, setWER []float64
		var seconds float64
		var pages int
		var runPeak int64
		for _, set := range sets {
			var ce, cn, we, wn int
			for _, cl := range res.Cells {
				if cl.Run != run || cl.Set != set {
					continue
				}
				sum.cells++
				if cl.incomplete() {
					sum.incomplete++
				} else {
					ce, cn, we, wn = ce+cl.CharEdits, cn+cl.Chars, we+cl.WordEdits, wn+cl.Words
				}
				seconds += cl.RenderTime.Seconds()
				pages++
				runPeak = max(runPeak, cl.PeakRSS)
				if run == 1 {
					pngBytes += float64(cl.PNGBytes)
					pngPages++
				}
			}
			if cn > 0 {
				setCER = append(setCER, rate(ce, cn))
				setWER = append(setWER, rate(we, wn))
			}
		}
		if len(setCER) > 0 {
			cer = append(cer, spreadOf(setCER).mean)
			wer = append(wer, spreadOf(setWER).mean)
		}
		renderTime = append(renderTime, seconds/float64(max(pages, 1)))
		peak = append(peak, mib(runPeak))
	}
	sum.cer, sum.wer = spreadOf(cer), spreadOf(wer)
	sum.renderTime, sum.peakRSS = spreadOf(renderTime), spreadOf(peak)
	sum.pngBytes = pngBytes / max(pngPages, 1)
	return sum
}

// criterion is one of the ranking's criteria: its name, and an order of two renderers, the
// better first, 0 for a tie.
type criterion struct {
	name    string
	compare func(a, b ranked) int
}

// ranked is a renderer with its measures over every set.
type ranked struct {
	res  result
	all  summary
	wins int
}

// criteria are the ranking's criteria in the order they decide: the character error rate over
// the completed transcriptions, tied inside the run-to-run spread; then the incomplete
// transcriptions, fewer first; then render time and peak memory, each tied inside the spread;
// then streaming, the license, the PNG's size, and the number of tools.
//
// The incomplete transcriptions are a count over every page of every run, and any difference
// decides, with no spread: every renderer has the same pages and runs, and an incomplete page is
// a failure its error rate doesn't see.
var criteria = []criterion{
	{"CER", func(a, b ranked) int { return a.all.cer.compare(b.all.cer) }},
	{"incomplete", func(a, b ranked) int { return cmp.Compare(a.all.incomplete, b.all.incomplete) }},
	{"time", func(a, b ranked) int { return a.all.renderTime.compare(b.all.renderTime) }},
	{"memory", func(a, b ranked) int { return a.all.peakRSS.compare(b.all.peakRSS) }},
	{"streaming", func(a, b ranked) int {
		return -cmp.Compare(boolInt(a.res.Streams), boolInt(b.res.Streams))
	}},
	{"license", func(a, b ranked) int { return cmp.Compare(a.res.LicenseRank, b.res.LicenseRank) }},
	{"image size", func(a, b ranked) int { return cmp.Compare(a.all.pngBytes, b.all.pngBytes) }},
	{"tools", func(a, b ranked) int { return cmp.Compare(len(a.res.Tools), len(b.res.Tools)) }},
}

// incompleteRate formats s's incomplete transcriptions as a count of its cells and a rate.
func (s summary) incompleteRate() string {
	return fmt.Sprintf("%d/%d (%.3f)", s.incomplete, s.cells, rate(s.incomplete, s.cells))
}

// boolInt returns 1 for true and 0 for false.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// decide returns which of a and b the criteria put first, -1 for a, 1 for b, 0 for a tie on all,
// and the criterion that decided.
func decide(a, b ranked) (int, string) {
	for _, c := range criteria {
		if d := c.compare(a, b); d != 0 {
			return d, c.name
		}
	}
	return 0, "none"
}

// TestBakeoffRanking ranks the renderers TestBakeoff ran, over all three sets together, and logs
// each set's results and the ranking as recorded lines. A tie inside the spread isn't
// transitive, so the ranking is by pairwise wins: each pair is decided by the first criterion
// that separates them, and a renderer ranks by how many others it beats, then by its mean CER.
func TestBakeoffRanking(t *testing.T) {
	rs := loadResults(t)
	if len(rs) == 0 {
		t.Skip("no bake-off results: run TestBakeoff first")
	}
	// A renderer missing a set's cells would score that set as perfect, so every renderer must
	// have every page of every set from every run.
	t.Setenv("BAKEOFF_SET", "")
	for _, res := range rs {
		for _, s := range loadSets(t) {
			n := 0
			for _, cl := range res.Cells {
				if cl.Set == s.name {
					n++
				}
			}
			if want := len(s.truth) * bakeoffRuns; n != want {
				t.Errorf("%s has %d cells of %s, want %d: a run is missing", res.Name, n, s.name, want)
			}
		}
	}
	for _, set := range setNames {
		rows := []string{"renderer\tCER mean\tCER min..max\tWER mean\tWER min..max\tincomplete\t"}
		for _, res := range rs {
			s := summarize(res, set)
			rows = append(rows, fmt.Sprintf("%s\t%.4f\t%.4f..%.4f\t%.4f\t%.4f..%.4f\t%s\t",
				res.Name, s.cer.mean, s.cer.lo, s.cer.hi, s.wer.mean, s.wer.lo, s.wer.hi, s.incompleteRate()))
		}
		for _, line := range table(rows) {
			t.Logf("recorded: bakeoff %s: %s", set, line)
		}
	}
	var rank []ranked
	for _, res := range rs {
		rank = append(rank, ranked{res: res, all: summarize(res, setNames...)})
	}
	for i := range rank {
		for j := i + 1; j < len(rank); j++ {
			d, by := decide(rank[i], rank[j])
			switch {
			case d < 0:
				rank[i].wins++
			case d > 0:
				rank[j].wins++
			}
			t.Logf("recorded: bakeoff pair %s vs %s: %s, by %s", rank[i].res.Name, rank[j].res.Name, winner(rank[i], rank[j], d), by)
		}
	}
	slices.SortStableFunc(rank, func(a, b ranked) int {
		return cmp.Or(-cmp.Compare(a.wins, b.wins), cmp.Compare(a.all.cer.mean, b.all.cer.mean))
	})
	rows := []string{"rank\trenderer\twins\tCER mean\tCER min..max\tWER mean\tWER min..max\tincomplete\t" +
		"render s/page\ts min..max\tpeak MiB\tMiB min..max\tPNG KiB/page\tstreams\tlicense\ttools\tinstalled MiB\t"}
	for i, r := range rank {
		a := r.all
		rows = append(rows, fmt.Sprintf("%d\t%s\t%d\t%.4f\t%.4f..%.4f\t%.4f\t%.4f..%.4f\t%s\t%.3f\t%.3f..%.3f\t%.1f\t%.1f..%.1f\t%.0f\t%v\t%s\t%s\t%.2f\t",
			i+1, r.res.Name, r.wins, a.cer.mean, a.cer.lo, a.cer.hi, a.wer.mean, a.wer.lo, a.wer.hi, a.incompleteRate(),
			a.renderTime.mean, a.renderTime.lo, a.renderTime.hi, a.peakRSS.mean, a.peakRSS.lo, a.peakRSS.hi,
			a.pngBytes/1024, r.res.Streams, r.res.License, strings.Join(r.res.Tools, "+"), r.res.FootprintMiB))
	}
	t.Log("recorded: bakeoff ranking over clean, degraded, and scanned: a run's CER and WER are the mean of the three sets' rates over the completed transcriptions; incomplete is the transcriptions that didn't complete, of all")
	for _, line := range table(rows) {
		t.Logf("recorded: bakeoff ranking: %s", line)
	}
}

// winner names the pair's winner, or a tie.
func winner(a, b ranked, d int) string {
	switch {
	case d < 0:
		return a.res.Name + " wins"
	case d > 0:
		return b.res.Name + " wins"
	}
	return "tie"
}

// table aligns rows, their cells separated by tabs, and returns its lines.
func table(rows []string) []string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		// A tabwriter over a strings.Builder fails only as the Builder does, which is never.
		_, _ = fmt.Fprintln(w, row)
	}
	_ = w.Flush()
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}
