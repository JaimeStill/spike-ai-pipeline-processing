//go:build integration && linux

package document_test

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-client/format/completions"
	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// The vision tier: Pages and Document on clean.pdf through the host's gs, each page sent to
// Gemma 4 26B-A4B on the router SPIKE_ROUTER_URL names, which set A loads, and Document's final
// request to gpt-oss-120b for a digest for search under a JSON schema. Each test records its wall
// time and the pages' character error rate against their ground truth, and asserts only
// completion, page order, and the digest's schema; Document runs on scanned.pdf too. A page is
// sent again once when its response isn't completed, and a recorded line names every page that
// needed it. Document's tests record the final request's input tokens against the context of one
// of the digest model's slots, and TestVisionImageTokensHost records how many tokens a letter
// page's image costs at each DPI.

const (
	// visionModel transcribes the pages, as in the renderer bake-off.
	visionModel = "ggml-org/gemma-4-26B-A4B-it-GGUF:Q4_0"
	// digestModel writes Document's digest.
	digestModel = "ggml-org/gpt-oss-120b-GGUF:MXFP4"
	// visionConcurrency is the pages in flight, the router's parallel slots for the model.
	visionConcurrency = 4
	// pageOutputCap is a page's output limit. The fixtures' transcripts run 80 to 1,100 tokens,
	// and a page whose model loops on its text runs to the cap: scanned.pdf's page 7 looped on
	// its seal's text to 8192 tokens, about 148 s, on about 1 run in 4. 2048 ends a loop sooner.
	pageOutputCap = 2048
	// pageRetries is the times a page is sent again when its response isn't completed: once, for
	// a loop that runs on one run and not the next.
	pageRetries = 1
)

// routerClient returns a Client of the router SPIKE_ROUTER_URL names, speaking Chat Completions,
// with autoload=false so a request fails rather than load a model. It skips t when
// SPIKE_ROUTER_URL is unset.
func routerClient(t *testing.T) *client.Client {
	t.Helper()
	base := os.Getenv("SPIKE_ROUTER_URL")
	if base == "" {
		t.Skip("SPIKE_ROUTER_URL is unset: set it to the router to test against, such as http://gb10-2:8080")
	}
	c, err := client.New(client.Config{
		BaseURL: strings.TrimSuffix(base, "/") + "/v1",
		Query:   url.Values{"autoload": {"false"}},
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
		Format:  completions.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// hostVision builds a Vision over the router's Client and the host's gs, with no corrections
// stage, and a request for a PDF: each page transcribed as the bake-off transcribes, greedy and
// with reasoning off, at 150 dpi and 4 pages at once, capped at pageOutputCap and retried
// pageRetries times, and a final request for a digest.
func hostVision(t *testing.T) (*document.Vision, document.Request) {
	t.Helper()
	c := routerClient(t)
	g, _ := hostGhostscript(t)
	v, err := document.NewVision(c, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v, document.Request{
		Dir: t.TempDir(),
		Template: &protocol.Request{
			Model:           visionModel,
			Instructions:    document.TranscribeInstructions,
			Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
			MaxOutputTokens: pageOutputCap,
			Native:          jsontext.Value(`{"temperature":0}`),
		},
		Concurrency: visionConcurrency,
		PageRetries: pageRetries,
		Final: &protocol.Request{
			Model:        digestModel,
			Instructions: digestInstructions,
			Reasoning:    protocol.ReasoningParam{Effort: protocol.ReasoningEffortLow},
			Text: protocol.TextParam{Format: protocol.JSONSchemaFormat{
				Name:        "digest",
				Description: "A lightweight digest of a document for search.",
				Schema:      jsontext.Value(digestSchema),
				Strict:      true,
			}},
			MaxOutputTokens: 4096,
		},
	}
}

// digestInstructions asks for a digest of a document's page transcriptions, the caller's final
// template in the package's terms: the package imposes no schema.
const digestInstructions = "The user's message is a document's pages, transcribed, each under a " +
	"\"Page N of M:\" heading. Write a lightweight digest of the document for search, from the " +
	"transcriptions alone: a summary of 2 to 3 sentences; its title; its kind, the type of " +
	"document; the dates it names, as written; its markings, such as classification banners and " +
	"handling caveats, as written; and keywords for search. Leave an array empty when the " +
	"document has none."

// digestSchema is the digest's JSON schema, an example of a final template's: every field
// required and no others, as strict structured output requires.
const digestSchema = `{
	"type": "object",
	"properties": {
		"summary": {"type": "string", "description": "The document in 2 to 3 sentences."},
		"title": {"type": "string"},
		"kind": {"type": "string", "description": "The type of document, such as a memorandum or a report."},
		"dates": {"type": "array", "items": {"type": "string"}},
		"markings": {"type": "array", "items": {"type": "string"}, "description": "Markings such as classification banners, as written."},
		"keywords": {"type": "array", "items": {"type": "string"}}
	},
	"required": ["summary", "title", "kind", "dates", "markings", "keywords"],
	"additionalProperties": false
}`

// digest is a digest's fields, as the schema requires them.
type digest struct {
	Summary  *string   `json:"summary"`
	Title    *string   `json:"title"`
	Kind     *string   `json:"kind"`
	Dates    *[]string `json:"dates"`
	Markings *[]string `json:"markings"`
	Keywords *[]string `json:"keywords"`
}

// checkDigest fails t unless the final response is completed and its output text is a JSON
// object holding every field the schema requires, of its type, and returns the text. A field of
// the wrong type fails decoding; a missing one is left nil. The text is returned compacted, so a
// recorded line holds it whole on one line.
func checkDigest(t *testing.T, final *protocol.Response) string {
	t.Helper()
	out := text(final)
	if final.Status != protocol.StatusCompleted {
		t.Fatalf("the final response is %s with %q, want completed", final.Status, out)
	}
	var d digest
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("the digest %q doesn't decode as the schema's object: %v", out, err)
	}
	fields := map[string]bool{
		"summary": d.Summary != nil, "title": d.Title != nil, "kind": d.Kind != nil,
		"dates": d.Dates != nil, "markings": d.Markings != nil, "keywords": d.Keywords != nil,
	}
	for name, ok := range fields {
		if !ok {
			t.Errorf("the digest %q has no %s", out, name)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(out)); err != nil {
		t.Fatal(err)
	}
	return compact.String()
}

// recordRetries logs a recorded line for every page of a run that was sent more than once, with
// its attempts and its last response's status, and returns how many pages that is.
func recordRetries(t *testing.T, run string, pages []document.Page) int {
	t.Helper()
	retried := 0
	for _, p := range pages {
		if p.Attempts <= 1 {
			continue
		}
		retried++
		state := "no response"
		if p.Response != nil {
			state = fmt.Sprintf("%s, %d output tokens", p.Response.Status, p.Response.Usage.OutputTokens)
		}
		t.Logf("recorded: %s page %d of %d retried: %d attempts, last %s", run, p.Number, p.Count, p.Attempts, state)
	}
	return retried
}

// resultPages returns a Document result's pages, for recordRetries, or none for a nil result.
func resultPages(res *document.Result) []document.Page {
	if res == nil {
		return nil
	}
	pages := make([]document.Page, 0, len(res.Pages))
	for _, p := range res.Pages {
		pages = append(pages, p.Page)
	}
	return pages
}

// heading matches the "Page N of M:" heading lines Result.Text gives each page.
var heading = regexp.MustCompile(`(?m)^Page \d+ of \d+:$`)

// stripHeadings returns a Result's Text with its page headings removed, for scoring against the
// ground truth, which has none.
func stripHeadings(s string) string { return heading.ReplaceAllString(s, "") }

// cleanTruth returns clean.pdf's pages' ground truth, in page order.
func cleanTruth(t *testing.T) []string {
	t.Helper()
	return truthOf(t, "clean")
}

// truthOf returns a set's pages' ground truth, in page order.
func truthOf(t *testing.T, set string) []string {
	t.Helper()
	var truth []string
	for page := 1; ; page++ {
		b, err := os.ReadFile(filepath.Join("testdata", set, fmt.Sprintf("page-%d.txt", page)))
		if os.IsNotExist(err) {
			return truth
		}
		if err != nil {
			t.Fatal(err)
		}
		truth = append(truth, string(b))
	}
}

// openClean opens clean.pdf, closed when t ends.
func openClean(t *testing.T) *os.File {
	t.Helper()
	return openSet(t, "clean")
}

// openSet opens a set's PDF, closed when t ends.
func openSet(t *testing.T, set string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", set+".pdf"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// slotContext returns the context of one of model's slots on the router SPIKE_ROUTER_URL names,
// as its /props reports it: n_ctx in its default generation settings. Under set A's unified KV
// cache each slot may fill the whole pool, so it is the model's c, which the slots share.
func slotContext(t *testing.T, model string) int {
	t.Helper()
	u := strings.TrimSuffix(os.Getenv("SPIKE_ROUTER_URL"), "/") + "/props?" +
		url.Values{"model": {model}, "autoload": {"false"}}.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var props struct {
		Settings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("/props for %s: status %d, %v", model, resp.StatusCode, err)
	}
	return props.Settings.NCtx
}

// recordFinal logs a recorded line with Document's final request's input tokens against the
// context of one of the digest model's slots, and fails t when the request filled it.
func recordFinal(t *testing.T, run string, final *protocol.Response) {
	t.Helper()
	ctx := slotContext(t, digestModel)
	in := final.Usage.InputTokens
	t.Logf("recorded: %s final request: %d input tokens, %.1f%% of a %s slot's %d-token context",
		run, in, 100*rate(in, ctx), digestModel, ctx)
	if in >= ctx {
		t.Errorf("%s: the final request's %d input tokens fill the slot's %d-token context", run, in, ctx)
	}
}

// TestVisionPagesHost checks every page of clean.pdf comes back completed, in page order, and
// records the stream's wall time and the pages' character error rate.
func TestVisionPagesHost(t *testing.T) {
	v, req := hostVision(t)
	truth := cleanTruth(t)
	start := time.Now()
	var edits, chars int
	var pages []document.Page
	next := 1
	for p, err := range v.Pages(t.Context(), openClean(t), req) {
		pages = append(pages, p)
		if err != nil {
			t.Fatalf("page %d: %v", p.Number, err)
		}
		if p.Number != next || p.Count != len(truth) {
			t.Errorf("yield %d is page %d of %d, want page %d of %d", next, p.Number, p.Count, next, len(truth))
		}
		if p.Response.Status != protocol.StatusCompleted {
			t.Errorf("page %d's response is %s, want completed", p.Number, p.Response.Status)
		}
		e, n := charErrors(truth[p.Number-1], text(p.Response))
		edits, chars = edits+e, chars+n
		t.Logf("recorded: vision Pages clean page %d of %d: CER %.4f, %d output tokens, %d attempts", p.Number, p.Count, rate(e, n), p.Response.Usage.OutputTokens, p.Attempts)
		next++
	}
	if next-1 != len(truth) {
		t.Errorf("the stream yielded %d pages, want %d", next-1, len(truth))
	}
	retried := recordRetries(t, "vision Pages clean.pdf", pages)
	t.Logf("recorded: vision Pages clean.pdf, %d pages at concurrency %d: %v wall, CER %.4f, %d pages retried",
		next-1, req.Concurrency, time.Since(start).Round(time.Millisecond), rate(edits, chars), retried)
}

// TestVisionDocumentHost checks Document on clean.pdf completes every page, in page order, and
// a digest that decodes as the schema's object, and records its wall time, the pages' character
// error rate, the digest in full, and the result's Text against the joined ground truth: its
// length, and its character error rate with its page headings stripped.
func TestVisionDocumentHost(t *testing.T) {
	v, req := hostVision(t)
	truth := cleanTruth(t)
	start := time.Now()
	res, err := v.Document(t.Context(), openClean(t), req)
	elapsed := time.Since(start)
	retried := recordRetries(t, "vision Document clean.pdf", resultPages(res))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != len(truth) {
		t.Fatalf("the result holds %d pages, want %d", len(res.Pages), len(truth))
	}
	var edits, chars int
	for i, p := range res.Pages {
		if p.Number != i+1 || p.Count != len(truth) || p.Err != nil {
			t.Errorf("result page %d is page %d of %d, %v, want page %d of %d", i+1, p.Number, p.Count, p.Err, i+1, len(truth))
			continue
		}
		e, n := charErrors(truth[i], text(p.Response))
		edits, chars = edits+e, chars+n
	}
	out := checkDigest(t, res.Final)
	recordFinal(t, "vision Document clean.pdf", res.Final)
	joined := strings.Join(truth, "\n\n")
	te, tn := charErrors(joined, stripHeadings(res.Text()))
	t.Logf("recorded: vision Document clean.pdf, %d pages at concurrency %d and a %s digest: %v wall, CER %.4f, digest %d output tokens (%d reasoning), %d pages retried",
		len(res.Pages), req.Concurrency, digestModel, elapsed.Round(time.Millisecond), rate(edits, chars),
		res.Final.Usage.OutputTokens, res.Final.Usage.OutputTokensDetails.ReasoningTokens, retried)
	t.Logf("recorded: vision Document clean.pdf Text: %d bytes against the joined ground truth's %d, CER %.4f with headings stripped, whitespace collapsed",
		len(res.Text()), len(joined), rate(te, tn))
	t.Logf("recorded: vision Document clean.pdf digest: %s", out)
}

// TestVisionDocumentScannedHost checks Document on scanned.pdf, real scans of marked documents,
// completes every page and a digest that decodes as the schema's object, and records its wall
// time, the pages' character error rate, the final request's input tokens, and the digest in
// full.
func TestVisionDocumentScannedHost(t *testing.T) {
	v, req := hostVision(t)
	truth := truthOf(t, "scanned")
	start := time.Now()
	res, err := v.Document(t.Context(), openSet(t, "scanned"), req)
	elapsed := time.Since(start)
	retried := recordRetries(t, "vision Document scanned.pdf", resultPages(res))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != len(truth) {
		t.Fatalf("the result holds %d pages, want %d", len(res.Pages), len(truth))
	}
	var edits, chars int
	for i, p := range res.Pages {
		e, n := charErrors(truth[i], text(p.Response))
		edits, chars = edits+e, chars+n
	}
	out := checkDigest(t, res.Final)
	recordFinal(t, "vision Document scanned.pdf", res.Final)
	t.Logf("recorded: vision Document scanned.pdf, %d pages at concurrency %d and a %s digest: %v wall, CER %.4f, digest %d output tokens (%d reasoning), %d pages retried",
		len(res.Pages), req.Concurrency, digestModel, elapsed.Round(time.Millisecond), rate(edits, chars),
		res.Final.Usage.OutputTokens, res.Final.Usage.OutputTokensDetails.ReasoningTokens, retried)
	t.Logf("recorded: vision Document scanned.pdf digest: %s", out)
}

// TestVisionImageTokensHost records the tokens Gemma 4 26B-A4B counts for a letter page's image
// at 72, 100, 150, and 200 dpi: clean.pdf's page 1, rendered by the host's gs, sent in a request
// for one output token beside a short text, its input tokens less those of the same request
// without the image. The difference holds the image's tokens and the few that mark where an image
// begins and ends in the model's chat template. The request has no output bound of its own, which
// the protocol sets no lower than 16; Native's max_completion_tokens of 1 bounds it instead.
func TestVisionImageTokensHost(t *testing.T) {
	c := routerClient(t)
	g, _ := hostGhostscript(t)
	ask := func(parts ...protocol.ContentPart) int {
		t.Helper()
		resp, err := c.Generate(t.Context(), &protocol.Request{
			Model:     visionModel,
			Input:     []protocol.Item{protocol.Message{Role: protocol.RoleUser, Content: parts}},
			Reasoning: protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
			Native:    jsontext.Value(`{"temperature":0,"max_completion_tokens":1}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Usage.InputTokens
	}
	prompt := protocol.InputText{Text: "Reply with one word."}
	without := ask(prompt)
	for _, dpi := range []int{72, 100, 150, 200} {
		var page bytes.Buffer
		if err := g.Render(t.Context(), filepath.Join("testdata", "clean.pdf"), 1, dpi, &page); err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(page.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		with := ask(prompt, protocol.InputImage{ImageURL: protocol.DataURL("image/png", page.Bytes())})
		if with <= without {
			t.Errorf("%d dpi: %d input tokens with the image, %d without, want more with it", dpi, with, without)
		}
		t.Logf("recorded: vision image tokens, %s, clean.pdf page 1 at %d dpi, %dx%d, %d PNG bytes: %d image tokens (%d input tokens with the image, %d without)",
			visionModel, dpi, cfg.Width, cfg.Height, page.Len(), with-without, with, without)
	}
}
