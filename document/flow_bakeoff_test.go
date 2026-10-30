//go:build bakeoff && linux

package document_test

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"fmt"
	"image/png"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// Ruling N of task documents, the staged flow the inference layer will follow, in the bake-off
// tier: `mise run bakeoff-flow` runs TestFlow once per fixture set, variant, and run, each capped
// at 10 minutes, then TestFlowReport over their results. Every page goes to Gemma 4 26B-A4B on the
// router SPIKE_ROUTER_URL names, greedy and with reasoning off, the released renderer at 150 dpi,
// inFlight pages at once. The variants:
//
//   - flow: the staged flow. Images with SkipText reads each page's text layer, and a page with
//     one is scored from it, never sent. A page without one is assessed (Corrector.Assess), and
//     its first pass sends it under firstPassSchema, the first pass's instructions listing the
//     catalog and its figures beside the image; the answer gives the transcription, a judgement,
//     and the corrections, which ParseCorrection reads, unknown names counted and dropped. A page
//     the first pass flagged with corrections is corrected as it asked (Corrector.Correct) and
//     sent again with the plain transcription request; its second pass's text is scored, and the
//     first pass's for the rest.
//   - none: one vision pass through the harness, no correction.
//   - all-five: one vision pass through the harness, every correction, the matrix's all.
//   - regions: the scanned set only, each page's header and footer bands, the top and bottom
//     12% of the page rendered by RenderRegion at 300 dpi, each band sent with the plain
//     transcription request. A page's truth is the whole page's, so a band isn't scored: the
//     report gives what each band read beside the full page's reads.
//
// Every request puts its instructions beside the image, a text part before it in its user
// message, the placement TestPlacement measured the better (ruling L): the plain transcription
// request is TestPlacement's beside, and the first pass's text is its instructions and then the
// page's figures. So none and all-five are the matrix's 150-dpi cells with the instructions
// beside the image, and none repeats TestPlacement's beside. A page's model calls are its vision
// requests: 0 for a text-layer page, 1 or 2 in the flow, 1 in a baseline.
//
// BAKEOFF_SET names the fixture sets, BAKEOFF_VARIANT the variants, and BAKEOFF_RUN the runs, 1 to
// 3, each comma-separated; unset runs all of them. BAKEOFF_RESULTS names a directory each run's
// result is written to as flow-<variant>-<set>-<run>.json, which the report reads; unset keeps
// the results in memory, so one go test run of both tests reports what it ran.

// flowVariants are the variants, in the order the tables give them.
var flowVariants = []string{"flow", "none", "all-five", "regions"}

const (
	// regionDPI is the DPI a band is rendered at: twice the page's, so the band's pixels carry
	// more detail per image token than the whole page's do.
	regionDPI = 300
	// bandFraction is a band's height, a fraction of the page.
	bandFraction = 0.12
)

// bands are the regions the regions variant reads, by name.
var bands = []struct {
	name   string
	region document.Region
}{
	{"header", document.Region{Top: 0, Bottom: bandFraction, Left: 0, Right: 1}},
	{"footer", document.Region{Top: 1 - bandFraction, Bottom: 1, Left: 0, Right: 1}},
}

// flowCell is one page of one run of a variant: the cell it is scored as, and how the flow got
// there.
type flowCell struct {
	cell
	// Source is where the scored text came from: text-layer, first, or second in the flow;
	// vision in a baseline; a band's name in regions.
	Source string `json:"source"`
	// VisionCalls is how many vision requests the page took.
	VisionCalls int `json:"vision_calls"`
	// Assessment is the page's measures, for a page the flow sent.
	Assessment *document.Assessment `json:"assessment,omitempty"`
	// Quality and Reason are the first pass's judgement; Asked the corrections it named, as it
	// named them; Applied those ParseCorrection read, in the stage's order; Unknown the rest.
	Quality string   `json:"quality,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Asked   []string `json:"asked,omitempty"`
	Applied []string `json:"applied,omitempty"`
	Unknown []string `json:"unknown,omitempty"`
	// First is the first pass's own transcription, scored, for a page the flow sent: its status,
	// its transcription, and its edits. For a page with a second pass, it is what the second pass
	// is compared with.
	FirstStatus    string        `json:"first_status,omitempty"`
	FirstOutput    string        `json:"first_output,omitempty"`
	FirstCharEdits int           `json:"first_char_edits,omitempty"`
	FirstWordEdits int           `json:"first_word_edits,omitempty"`
	FirstTime      time.Duration `json:"first_time,omitempty"`
}

// flowResult is one run of TestFlow: one run of a variant over one set.
type flowResult struct {
	Variant  string        `json:"variant"`
	Set      string        `json:"set"`
	Run      int           `json:"run"`
	Versions []string      `json:"versions"`
	Cells    []flowCell    `json:"cells"`
	Elapsed  time.Duration `json:"elapsed"`
}

// flowRuns holds the results of this process's TestFlow runs for TestFlowReport when
// BAKEOFF_RESULTS is unset.
var flowRuns struct {
	sync.Mutex
	runs []flowResult
}

// selectedRuns returns the runs BAKEOFF_RUN names, 1 to bakeoffRuns when it is unset.
func selectedRuns(t *testing.T) []int {
	t.Helper()
	all := make([]string, bakeoffRuns)
	for i := range all {
		all[i] = strconv.Itoa(i + 1)
	}
	var runs []int
	for _, s := range selectedNames(t, "BAKEOFF_RUN", all) {
		n, _ := strconv.Atoi(s)
		runs = append(runs, n)
	}
	return runs
}

// firstPassTemplate is the first pass's request: the matrix's model, output bound, greedy
// sampling, and reasoning off, with the first pass's strict schema. Its instructions go beside
// the image, with the page's figures, as firstPassPrompt gives them.
func firstPassTemplate() *protocol.Request {
	return &protocol.Request{
		Model:           bakeoffModel,
		Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
		MaxOutputTokens: maxOutputTokens,
		Native:          jsontext.Value(`{"temperature":0}`),
		Text: protocol.TextParam{Format: protocol.JSONSchemaFormat{
			Name:        "page",
			Description: "A page's transcription, a judgement of its image, and the corrections to apply.",
			Schema:      jsontext.Value(firstPassSchema()),
			Strict:      true,
		}},
	}
}

// firstPassPrompt is the text beside a page's image in its first pass: the first pass's
// instructions, then the page's figures.
func firstPassPrompt(a document.Assessment) string {
	return firstPassInstructions() + "\n" + figures(a)
}

func TestFlow(t *testing.T) {
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
	for _, s := range loadSets(t) {
		for _, variant := range selectedNames(t, "BAKEOFF_VARIANT", flowVariants) {
			if variant == "regions" && s.name != "scanned" {
				continue
			}
			for _, run := range selectedRuns(t) {
				t.Run(fmt.Sprintf("%s/%s/%d", s.name, variant, run), func(t *testing.T) {
					res := flowResult{Variant: variant, Set: s.name, Run: run, Versions: versions}
					start := time.Now()
					switch variant {
					case "flow":
						res.Cells = flowRun(t, v, corr, s, run)
					case "none", "all-five":
						req := placementRequest(t, "beside")
						if variant == "all-five" {
							req.Corrections = matrixCells[len(matrixCells)-1].corrections
						}
						for _, cl := range harnessRun(t, v, s, req, fmt.Sprintf("flow %s %s run %d", s.name, variant, run), run) {
							res.Cells = append(res.Cells, flowCell{cell: cl, Source: "vision", VisionCalls: 1})
						}
					case "regions":
						res.Cells = regionRun(t, v, s, run)
					}
					res.Elapsed = time.Since(start)
					for _, cl := range res.Cells {
						t.Logf("recorded: flow %s %s: page %d run %d: %s, CER %.4f WER %.4f, %d vision calls, %d input tokens, %d output tokens, status %s%s%s",
							s.name, variant, cl.Page, cl.Run, cl.Source, rate(cl.CharEdits, cl.Chars), rate(cl.WordEdits, cl.Words),
							cl.VisionCalls, cl.InputTokens, cl.OutputTokens, cl.Status, asked(cl), errSuffix(cl.Error))
					}
					t.Logf("recorded: flow %s %s: run %d, %d cells at concurrency %d in %v",
						s.name, variant, run, len(res.Cells), inFlight, res.Elapsed.Round(time.Millisecond))
					saveJSON(t, fmt.Sprintf("flow-%s-%s-%d.json", variant, s.name, run), res, func() {
						flowRuns.Lock()
						defer flowRuns.Unlock()
						flowRuns.runs = append(flowRuns.runs, res)
					})
				})
			}
		}
	}
}

// asked describes a flow page's first pass for its recorded line: its judgement and the
// corrections it asked for, or "" for a page with none.
func asked(cl flowCell) string {
	if cl.Quality == "" {
		return ""
	}
	s := fmt.Sprintf(", judged %s, asked %v", cl.Quality, cl.Asked)
	if len(cl.Unknown) > 0 {
		s += fmt.Sprintf(", unknown %v", cl.Unknown)
	}
	return s
}

// flowRun runs every page of s through the staged flow, inFlight pages at once, and returns each
// page's cell of run, scored against its truth. Images prepares the pages first, reading each
// page's text layer and rendering only the pages without one; a page that fails in preparation
// is t's error, and its cell scores as an empty transcription, incomplete.
func flowRun(t *testing.T, v *vision, corr *document.Corrector, s fixtureSet, run int) []flowCell {
	t.Helper()
	f, err := os.Open(s.pdf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var imgs []document.PageImage
	var errs []error
	req := document.ImagesRequest{Dir: t.TempDir(), DPI: bakeoffDPI, Concurrency: inFlight, SkipText: true}
	for img, err := range v.prepare.Images(t.Context(), f, req) {
		if img.Number == 0 {
			t.Fatalf("flow %s run %d: %v", s.name, run, err)
		}
		imgs = append(imgs, img)
		errs = append(errs, err)
	}
	if len(imgs) != len(s.truth) {
		t.Fatalf("flow %s run %d: %d pages, want %d", s.name, run, len(imgs), len(s.truth))
	}
	cells := make([]flowCell, len(imgs))
	work := make(chan int)
	var wg sync.WaitGroup
	for range inFlight {
		wg.Go(func() {
			for i := range work {
				cl := flowCell{cell: cell{Set: s.name, Page: imgs[i].Number, Run: run}}
				if errs[i] != nil {
					cl.Error = errs[i].Error()
					t.Errorf("flow %s page %d run %d: %v", s.name, cl.Page, run, errs[i])
				} else {
					flowPage(t, v, corr, imgs[i], &cl)
				}
				score(&cl.cell, s.truth[cl.Page-1])
				if cl.FirstStatus != "" {
					cl.FirstCharEdits, _ = charErrors(s.truth[cl.Page-1], cl.FirstOutput)
					cl.FirstWordEdits, _ = wordErrors(s.truth[cl.Page-1], cl.FirstOutput)
				}
				cells[i] = cl
			}
		})
	}
	for i := range imgs {
		work <- i
	}
	close(work)
	wg.Wait()
	return cells
}

// flowPage takes one prepared page through the flow, setting cl's text, status, and how it got
// there. A step that fails is t's error, and leaves cl incomplete.
func flowPage(t *testing.T, v *vision, corr *document.Corrector, img document.PageImage, cl *flowCell) {
	t.Helper()
	if img.PNG == nil && document.HasText(img.Text) {
		cl.Source = "text-layer"
		cl.Status = string(protocol.StatusCompleted)
		cl.Output = img.Text
		return
	}
	ctx := t.Context()
	a, err := corr.Assess(ctx, bytes.NewReader(img.PNG))
	if err != nil {
		cl.Error = fmt.Sprintf("assess: %v", err)
		t.Errorf("flow %s page %d run %d: %s", cl.Set, cl.Page, cl.Run, cl.Error)
		return
	}
	cl.Assessment = &a
	cl.Source = "first"
	start := time.Now()
	resp, _, err := v.send(ctx, img, visionRequest{Template: firstPassTemplate(), Prompt: firstPassPrompt(a)})
	cl.FirstTime = time.Since(start)
	cl.RequestTime = cl.FirstTime
	cl.VisionCalls++
	if err != nil {
		cl.Error = err.Error()
		t.Errorf("flow %s page %d run %d: first pass: %v", cl.Set, cl.Page, cl.Run, err)
		return
	}
	fill(&cl.cell, resp)
	cl.FirstStatus = cl.Status
	if resp.Status != protocol.StatusCompleted {
		// A first pass cut off holds no answer to read, so its raw text isn't scored.
		cl.Output = ""
		return
	}
	fp, applied, unknown, err := parseFirstPass(text(resp))
	if err != nil {
		cl.Status, cl.Output, cl.Error = "unreadable", "", err.Error()
		cl.FirstStatus = cl.Status
		return
	}
	cl.Output, cl.FirstOutput = *fp.Transcription, *fp.Transcription
	cl.Quality, cl.Reason, cl.Asked, cl.Unknown = *fp.Quality, *fp.Reason, fp.Corrections, unknown
	for _, c := range applied {
		cl.Applied = append(cl.Applied, c.String())
	}
	if len(applied) == 0 {
		return
	}
	var corrected bytes.Buffer
	if err := corr.Correct(ctx, applied, bytes.NewReader(img.PNG), &corrected); err != nil {
		cl.Status, cl.Output, cl.Error = "", "", fmt.Sprintf("correct: %v", err)
		t.Errorf("flow %s page %d run %d: %s", cl.Set, cl.Page, cl.Run, cl.Error)
		return
	}
	cl.Source = "second"
	second := document.PageImage{Number: img.Number, Count: img.Count, PNG: corrected.Bytes()}
	start = time.Now()
	resp, _, err = v.send(ctx, second, placementRequest(t, "beside"))
	cl.RequestTime += time.Since(start)
	cl.VisionCalls++
	if err != nil {
		cl.Status, cl.Output, cl.Error = "", "", err.Error()
		t.Errorf("flow %s page %d run %d: second pass: %v", cl.Set, cl.Page, cl.Run, err)
		return
	}
	fill(&cl.cell, resp)
}

// regionRun reads every page of s's header and footer bands through v's pages, at regionDPI with
// the plain transcription request, its instructions beside the image, and returns each band's
// cell of run, its Source the band's name. A band's cell is scored against its whole page's
// truth, which the report doesn't use.
func regionRun(t *testing.T, v *vision, s fixtureSet, run int) []flowCell {
	t.Helper()
	var cells []flowCell
	for _, b := range bands {
		req := placementRequest(t, "beside")
		req.DPI, req.Region = regionDPI, &b.region
		for _, cl := range harnessRun(t, v, s, req, fmt.Sprintf("regions %s %s run %d", s.name, b.name, run), run) {
			cells = append(cells, flowCell{cell: cl, Source: b.name, VisionCalls: 1})
		}
	}
	return cells
}

// Ruling O of task documents, tile shape, in the bake-off tier: `mise run bakeoff-tiles` runs
// TestTiles once per DPI. The regions variant's full-width bands at 300 dpi, 2550 by 396 pixels,
// cost nearly a page's image tokens and still misread page 7's faded foot marking; the hypothesis
// is that the vision encoder resizes an image toward a fixed shape, squashing a wide one, so a
// near-square tile keeps its detail. Each tile below is rendered once by RenderRegion and sent
// tileRuns times, greedy, reasoning off, with the plain transcription request beside it; its
// image tokens are its requests' input tokens less those of the same text without the image, as
// TestVisionImageTokensHost measures a page's. Its reads are logged verbatim, not scored: a
// page's truth is the whole page's.
//
// BAKEOFF_DPI names the DPIs, comma-separated, tileDPIs when unset. BAKEOFF_RESULTS names a
// directory each DPI's result is written to as tiles-<dpi>.json.

// tileDPIs are the DPIs TestTiles renders at: the regions variant's, and 200 for how a tile's
// image tokens follow its pixels.
var tileDPIs = []int{regionDPI, 200}

// tileRuns is how many times each tile is sent.
const tileRuns = 3

// tiles are the regions TestTiles reads, placed from the scanned pages' 300-dpi images: page 7's
// foot marking, its "SECRET" and "NOFORN" with margin, and NOFORN alone, against the regions
// variant's footer band; page 5's seal, ringed by "DEPARTMENT OF DEFENSE"; and, the control, page
// 5's clearly printed "WASHINGTON, D.C. 20340".
var tiles = []struct {
	name   string
	page   int
	region document.Region
}{
	{"p7-foot", 7, document.Region{Top: 0.68, Bottom: 1, Left: 0.35, Right: 0.81}},
	{"p7-noforn", 7, document.Region{Top: 0.86, Bottom: 1, Left: 0.60, Right: 0.82}},
	{"p7-footer-band", 7, bands[1].region},
	{"p5-seal", 5, document.Region{Top: 0.05, Bottom: 0.18, Left: 0.05, Right: 0.22}},
	{"p5-washington", 5, document.Region{Top: 0.075, Bottom: 0.205, Left: 0.39, Right: 0.60}},
}

// tileRead is one read of a tile.
type tileRead struct {
	Tile          string          `json:"tile"`
	Page          int             `json:"page"`
	Region        document.Region `json:"region"`
	DPI           int             `json:"dpi"`
	Width         int             `json:"width"`
	Height        int             `json:"height"`
	PNGBytes      int             `json:"png_bytes"`
	Run           int             `json:"run"`
	InputTokens   int             `json:"input_tokens"`
	TextOnly      int             `json:"text_only_tokens"`
	OutputTokens  int             `json:"output_tokens"`
	Status        string          `json:"status"`
	Output        string          `json:"output"`
	Error         string          `json:"error,omitempty"`
	RequestMillis int64           `json:"request_ms"`
	// png is the tile's image, held until the read is sent.
	png []byte
}

func TestTiles(t *testing.T) {
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
	pdf := fixturePDF(t, "scanned")
	req := placementRequest(t, "beside")
	// The text-only request is a page's without the image, bounded to one output token as the
	// integration tier's probe bounds it.
	textOnly := withMessage(req.Template, protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{protocol.InputText{Text: req.Prompt}}})
	textOnly.MaxOutputTokens = 0
	textOnly.Native = jsontext.Value(`{"temperature":0,"max_completion_tokens":1}`)
	resp, err := c.Generate(t.Context(), textOnly)
	if err != nil {
		t.Fatal(err)
	}
	without := resp.Usage.InputTokens
	t.Logf("recorded: tiles: the plain transcription request without an image is %d input tokens", without)
	for _, dpi := range selectedDPIs(t, tileDPIs) {
		t.Run(strconv.Itoa(dpi), func(t *testing.T) {
			var reads []tileRead
			for _, tl := range tiles {
				var img bytes.Buffer
				if err := g.RenderRegion(t.Context(), pdf, tl.page, dpi, tl.region, &img); err != nil {
					t.Fatal(err)
				}
				cfg, err := png.DecodeConfig(bytes.NewReader(img.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				for run := 1; run <= tileRuns; run++ {
					reads = append(reads, tileRead{Tile: tl.name, Page: tl.page, Region: tl.region, DPI: dpi,
						Width: cfg.Width, Height: cfg.Height, PNGBytes: img.Len(), Run: run, TextOnly: without,
						png: img.Bytes()})
				}
			}
			// inFlight reads are sent at once.
			work := make(chan int)
			var wg sync.WaitGroup
			for range inFlight {
				wg.Go(func() {
					for i := range work {
						r := &reads[i]
						page := document.PageImage{Number: r.Page, Count: 7, PNG: r.png}
						start := time.Now()
						resp, _, err := v.send(t.Context(), page, req)
						r.RequestMillis = time.Since(start).Milliseconds()
						if err != nil {
							r.Error = err.Error()
							t.Errorf("tiles %s %d dpi run %d: %v", r.Tile, dpi, r.Run, err)
							continue
						}
						r.Status, r.Output = string(resp.Status), text(resp)
						r.InputTokens, r.OutputTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
					}
				})
			}
			for i := range reads {
				work <- i
			}
			close(work)
			wg.Wait()
			for _, r := range reads {
				t.Logf("recorded: tiles %s at %d dpi, page %d region top %g bottom %g left %g right %g, %dx%d, %d PNG bytes: run %d, %d input tokens, %d image tokens, %d output tokens, %s in %d ms: %q",
					r.Tile, dpi, r.Page, r.Region.Top, r.Region.Bottom, r.Region.Left, r.Region.Right, r.Width, r.Height, r.PNGBytes,
					r.Run, r.InputTokens, r.InputTokens-r.TextOnly, r.OutputTokens, r.Status, r.RequestMillis, collapse(r.Output))
			}
			saveJSON(t, fmt.Sprintf("tiles-%d.json", dpi), reads, func() {})
		})
	}
}

// TestFlowReport logs the runs TestFlow ran as recorded lines: for each set, each variant's CER
// and WER with their spread over the runs, incomplete pages, model calls per page, and run time;
// the text layers' CER per page; the corrections the first pass asked for, on which pages and how
// often; each second pass against its first; each page's mean CER per variant; and, for the
// scanned set, what each page's bands read beside its full page's reads.
func TestFlowReport(t *testing.T) {
	rs := loadJSON(t, "flow-*.json", func() []flowResult {
		flowRuns.Lock()
		defer flowRuns.Unlock()
		return slices.Clone(flowRuns.runs)
	})
	if len(rs) == 0 {
		t.Skip("no flow results: run TestFlow first")
	}
	t.Setenv("BAKEOFF_SET", "")
	truth := map[string]int{}
	for _, s := range loadSets(t) {
		truth[s.name] = len(s.truth)
	}
	byKey := map[string][]flowResult{}
	for _, res := range rs {
		key := res.Variant + "/" + res.Set
		byKey[key] = append(byKey[key], res)
		want := truth[res.Set]
		if res.Variant == "regions" {
			want *= len(bands)
		}
		// A run missing a page would score the page as perfect.
		if len(res.Cells) != want {
			t.Errorf("flow %s %s run %d has %d cells, want %d", res.Variant, res.Set, res.Run, len(res.Cells), want)
		}
	}
	t.Log("recorded: flow: a run's CER and WER are its edits over its truth's length, the set's completed pages together; incomplete is the pages whose scored text didn't complete, of all; calls/page is vision requests per page, a text-layer page 0; run s is a run's wall time, preparation to last response")
	for _, set := range setNames {
		rows := []string{"variant\tCER mean\tCER min..max\tWER mean\tWER min..max\tincomplete\tcalls/page\trun s mean\trun s min..max\t"}
		for _, variant := range flowVariants[:3] {
			runs := byKey[variant+"/"+set]
			if len(runs) == 0 {
				continue
			}
			var cells []cell
			var calls, pages int
			var times []time.Duration
			for _, res := range runs {
				for _, cl := range res.Cells {
					cells = append(cells, cl.cell)
					calls += cl.VisionCalls
					pages++
				}
				times = append(times, res.Elapsed)
			}
			s := summarize(result{Cells: cells}, set)
			secs := seconds(times)
			rows = append(rows, fmt.Sprintf("%s\t%.4f\t%.4f..%.4f\t%.4f\t%.4f..%.4f\t%s\t%.2f\t%.1f\t%.1f..%.1f\t",
				variant, s.cer.mean, s.cer.lo, s.cer.hi, s.wer.mean, s.wer.lo, s.wer.hi, s.incompleteRate(),
				float64(calls)/float64(max(pages, 1)), secs.mean, secs.lo, secs.hi))
		}
		if len(rows) == 1 {
			continue
		}
		for _, line := range table(rows) {
			t.Logf("recorded: flow %s: %s", set, line)
		}
		flows := byKey["flow/"+set]
		reportTextLayers(t, set, flows)
		reportCorrections(t, set, flows)
		reportPages(t, set, truth[set], byKey)
	}
	reportBands(t, truth["scanned"], byKey)
}

// sortedRuns returns rs's cells in run and page order.
func sortedRuns(rs []flowResult) []flowCell {
	var cells []flowCell
	for _, res := range rs {
		cells = append(cells, res.Cells...)
	}
	slices.SortFunc(cells, func(a, b flowCell) int {
		return cmp.Or(cmp.Compare(a.Run, b.Run), cmp.Compare(a.Page, b.Page), strings.Compare(a.Source, b.Source))
	})
	return cells
}

// reportTextLayers logs each text-layer page's CER over the flow's runs.
func reportTextLayers(t *testing.T, set string, flows []flowResult) {
	t.Helper()
	byPage := map[int][]string{}
	for _, cl := range sortedRuns(flows) {
		if cl.Source == "text-layer" {
			byPage[cl.Page] = append(byPage[cl.Page], fmt.Sprintf("%.4f", rate(cl.CharEdits, cl.Chars)))
		}
	}
	if len(byPage) == 0 {
		t.Logf("recorded: flow %s: no page has a text layer; every page went to vision", set)
		return
	}
	for _, page := range slices.Sorted(maps.Keys(byPage)) {
		t.Logf("recorded: flow %s: page %d read from its text layer, 0 vision calls, CER per run %s",
			set, page, strings.Join(byPage[page], ", "))
	}
}

// reportCorrections logs the flow's first-pass judgements and the corrections they asked for:
// how often each was asked, on which pages, the unknown names dropped, and each second pass's
// CER against its first pass's.
func reportCorrections(t *testing.T, set string, flows []flowResult) {
	t.Helper()
	byCorrection := map[string][]string{}
	judged := map[string]int{}
	var sent, flagged, unknown, helped, hurt, same int
	for _, cl := range sortedRuns(flows) {
		if cl.FirstStatus == "" {
			continue
		}
		sent++
		if cl.Quality != "" {
			judged[cl.Quality]++
		}
		unknown += len(cl.Unknown)
		for _, name := range cl.Applied {
			byCorrection[name] = append(byCorrection[name], fmt.Sprintf("p%d/r%d", cl.Page, cl.Run))
		}
		t.Logf("recorded: flow %s: page %d run %d: first pass %s, judged %q (%s), asked %v, first CER %.4f",
			set, cl.Page, cl.Run, cl.FirstStatus, cl.Quality, cl.Reason, cl.Asked, rate(cl.FirstCharEdits, cl.Chars))
		if cl.Source != "second" {
			continue
		}
		flagged++
		first, second := rate(cl.FirstCharEdits, cl.Chars), rate(cl.CharEdits, cl.Chars)
		verdict := "same"
		switch {
		case cl.incomplete():
			verdict = "hurt (second pass incomplete)"
			hurt++
		case second < first:
			verdict = "helped"
			helped++
		case second > first:
			verdict = "hurt"
			hurt++
		default:
			same++
		}
		t.Logf("recorded: flow %s: page %d run %d: second pass with %v: CER %.4f against the first's %.4f, %s",
			set, cl.Page, cl.Run, cl.Applied, second, first, verdict)
	}
	if sent == 0 {
		return
	}
	t.Logf("recorded: flow %s: %d first passes, judged %v; %d flagged with corrections, %d unknown names dropped; second pass helped %d, hurt %d, same %d",
		set, sent, judged, flagged, unknown, helped, hurt, same)
	for _, c := range document.Corrections() {
		pages := byCorrection[c.String()]
		t.Logf("recorded: flow %s: %s asked %d times: %s", set, c, len(pages), strings.Join(pages, " "))
	}
}

// reportPages logs each page's mean CER over the completed runs, per variant.
func reportPages(t *testing.T, set string, n int, byKey map[string][]flowResult) {
	t.Helper()
	rows := []string{"page\tflow\tnone\tall-five\t"}
	for page := 1; page <= n; page++ {
		row := strconv.Itoa(page)
		for _, variant := range flowVariants[:3] {
			var cells []cell
			for _, cl := range sortedRuns(byKey[variant+"/"+set]) {
				cells = append(cells, cl.cell)
			}
			row += "\t" + pageMean(cells, page)
		}
		rows = append(rows, row+"\t")
	}
	for _, line := range table(rows) {
		t.Logf("recorded: flow %s per page, mean CER over completed runs: %s", set, line)
	}
}

// reportBands logs what each scanned page's header and footer bands read on each run, beside
// whether the page's full reads, in every variant, hold NOFORN; and each band's NOFORN.
func reportBands(t *testing.T, n int, byKey map[string][]flowResult) {
	t.Helper()
	regions := sortedRuns(byKey["regions/scanned"])
	if len(regions) == 0 {
		return
	}
	t.Logf("recorded: flow scanned regions: the top and bottom %.0f%% of each page at %d dpi, plain transcription", bandFraction*100, regionDPI)
	for page := 1; page <= n; page++ {
		for _, cl := range regions {
			if cl.Page != page {
				continue
			}
			t.Logf("recorded: flow scanned regions: page %d run %d %s (%d input tokens, %s): %q",
				page, cl.Run, cl.Source, cl.InputTokens, cl.Status, collapse(cl.Output))
		}
		var full []string
		for _, variant := range flowVariants[:3] {
			hold, of := 0, 0
			for _, cl := range sortedRuns(byKey[variant+"/scanned"]) {
				if cl.Page == page {
					of++
					if strings.Contains(cl.Output, "NOFORN") {
						hold++
					}
				}
			}
			full = append(full, fmt.Sprintf("%s %d/%d", variant, hold, of))
		}
		bandHold := map[string]int{}
		for _, cl := range regions {
			if cl.Page == page && strings.Contains(cl.Output, "NOFORN") {
				bandHold[cl.Source]++
			}
		}
		t.Logf("recorded: flow scanned regions: page %d NOFORN in the full page's reads: %s; in the bands' reads: %v",
			page, strings.Join(full, ", "), bandHold)
	}
}
