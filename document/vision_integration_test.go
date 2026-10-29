//go:build integration && linux

package document_test

import (
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
// request to gpt-oss-120b. Each test records its wall time and the pages' character error rate
// against their ground truth, and asserts only completion and page order.

const (
	// visionModel transcribes the pages, as in the renderer bake-off.
	visionModel = "ggml-org/gemma-4-26B-A4B-it-GGUF:Q4_0"
	// summaryModel writes Document's summary.
	summaryModel = "ggml-org/gpt-oss-120b-GGUF:MXFP4"
	// visionConcurrency is the pages in flight, the router's parallel slots for the model.
	visionConcurrency = 4
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
// stage, and a request for clean.pdf: each page transcribed as the bake-off transcribes, greedy
// and with reasoning off, at 150 dpi and 4 pages at once, and a final request for a short
// summary.
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
			MaxOutputTokens: 8192,
			Native:          jsontext.Value(`{"temperature":0}`),
		},
		Concurrency: visionConcurrency,
		Final: &protocol.Request{
			Model:           summaryModel,
			Instructions:    "Summarize this document in three sentences, as plain text.",
			Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortLow},
			Text:            protocol.TextParam{Format: protocol.PlainTextFormat{}},
			MaxOutputTokens: 2048,
		},
	}
}

// cleanTruth returns clean.pdf's pages' ground truth, in page order.
func cleanTruth(t *testing.T) []string {
	t.Helper()
	var truth []string
	for page := 1; ; page++ {
		b, err := os.ReadFile(filepath.Join("testdata", "clean", fmt.Sprintf("page-%d.txt", page)))
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
	f, err := os.Open(filepath.Join("testdata", "clean.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestVisionPagesHost checks every page of clean.pdf comes back completed, in page order, and
// records the stream's wall time and the pages' character error rate.
func TestVisionPagesHost(t *testing.T) {
	v, req := hostVision(t)
	truth := cleanTruth(t)
	start := time.Now()
	var edits, chars int
	next := 1
	for p, err := range v.Pages(t.Context(), openClean(t), req) {
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
		t.Logf("recorded: vision Pages clean page %d of %d: CER %.4f, %d output tokens", p.Number, p.Count, rate(e, n), p.Response.Usage.OutputTokens)
		next++
	}
	if next-1 != len(truth) {
		t.Errorf("the stream yielded %d pages, want %d", next-1, len(truth))
	}
	t.Logf("recorded: vision Pages clean.pdf, %d pages at concurrency %d: %v wall, CER %.4f",
		next-1, req.Concurrency, time.Since(start).Round(time.Millisecond), rate(edits, chars))
}

// TestVisionDocumentHost checks Document on clean.pdf completes every page, in page order, and
// the final summary, and records its wall time, the pages' character error rate, and the
// summary.
func TestVisionDocumentHost(t *testing.T) {
	v, req := hostVision(t)
	truth := cleanTruth(t)
	start := time.Now()
	res, err := v.Document(t.Context(), openClean(t), req)
	elapsed := time.Since(start)
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
	if res.Final.Status != protocol.StatusCompleted || strings.TrimSpace(text(res.Final)) == "" {
		t.Errorf("the final response is %s with %q, want completed with a summary", res.Final.Status, text(res.Final))
	}
	t.Logf("recorded: vision Document clean.pdf, %d pages at concurrency %d and a %s summary: %v wall, CER %.4f, summary %d output tokens (%d reasoning)",
		len(res.Pages), req.Concurrency, summaryModel, elapsed.Round(time.Millisecond), rate(edits, chars),
		res.Final.Usage.OutputTokens, res.Final.Usage.OutputTokensDetails.ReasoningTokens)
	t.Logf("recorded: vision Document clean.pdf summary: %q", text(res.Final))
}
