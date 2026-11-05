//go:build findings && linux

package dispatch_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

const (
	// inkTruth is the hand transcription of handwriting_recognition.one's page ink, as
	// testdata/README.md says.
	inkTruth = "testdata/handwriting-truth.txt"
	// inkModel reads the ink, as it reads document's pages.
	inkModel = "google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0"
	// inkPrompt is the text beside the ink's PNG.
	inkPrompt = "Transcribe the handwritten text in this picture, line by line, top to bottom. Output only the text, with no commentary."
	// inkOutputCap bounds a transcription's output; the page's text runs well under it.
	inkOutputCap = 1024
	// inkRuns is the greedy runs the ink is sent in.
	inkRuns = 3
)

// collapse returns s with every run of whitespace made one space, and none at either end.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// editDistance returns the Levenshtein distance from a to b, as document's bake-off scores it.
func editDistance[T comparable](a, b []T) int {
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(a); i++ {
		diag := row[0]
		row[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			above := row[j]
			row[j] = min(above+1, row[j-1]+1, diag+cost)
			diag = above
		}
	}
	return row[len(b)]
}

// inkScore is a transcription scored against the truth, as document's bake-off scores a page:
// the character and word edit distances over the truth's length, both texts whitespace-collapsed,
// case counting.
type inkScore struct {
	CharEdits  int     `json:"char_edits"`
	Chars      int     `json:"chars"`
	CER        float64 `json:"cer"`
	WordEdits  int     `json:"word_edits"`
	Words      int     `json:"words"`
	WER        float64 `json:"wer"`
	WordsRight int     `json:"words_right"`
}

// scoreInk scores got against truth. WordsRight is the truth's words got holds in order, their
// longest common subsequence.
func scoreInk(truth, got string) inkScore {
	tc, tw, gw := []rune(collapse(truth)), strings.Fields(truth), strings.Fields(got)
	s := inkScore{CharEdits: editDistance(tc, []rune(collapse(got))), Chars: len(tc), WordEdits: editDistance(tw, gw), Words: len(tw)}
	s.CER, s.WER = float64(s.CharEdits)/float64(s.Chars), float64(s.WordEdits)/float64(s.Words)
	// The longest common subsequence of words, by one row.
	row := make([]int, len(gw)+1)
	for i := 1; i <= len(tw); i++ {
		diag := 0
		for j := 1; j <= len(gw); j++ {
			above := row[j]
			if tw[i-1] == gw[j-1] {
				row[j] = diag + 1
			} else {
				row[j] = max(row[j], row[j-1])
			}
			diag = above
		}
	}
	s.WordsRight = row[len(gw)]
	return s
}

// inkRead is one transcription of the ink and its score.
type inkRead struct {
	Run          int           `json:"run"`
	Text         string        `json:"text"`
	Status       string        `json:"status"`
	ImageTokens  int           `json:"image_tokens"`
	OutputTokens int           `json:"output_tokens"`
	Wall         time.Duration `json:"wall"`
	Score        inkScore      `json:"score"`
}

// TestFindingsInk dispatches handwriting_recognition.one through the onenote and picture stages,
// its page ink an SVG rendered to a PNG at 150 dpi on white, and sends the PNG to inkModel beside
// inkPrompt inkRuns times, greedy, each transcription scored against the hand transcription
// inkTruth holds, beside OneNote's own recognition, the page's ink_text, scored the same way. It
// writes the PNG to the results beside its JSON.
func TestFindingsInk(t *testing.T) {
	truthBytes, err := os.ReadFile(inkTruth)
	if err != nil {
		t.Fatal(err)
	}
	truth := string(truthBytes)
	stages, err := findingStages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	section := filepath.Join(onenoteFixtures, "handwriting_recognition.one")
	var png []byte
	var where string
	dispatchAll(t, newDispatcher(t, stages, dispatch.NewMemoryIndex()), section, func(ev *dispatch.Event) {
		if o := ev.Output; o != nil && o.Kind == dispatch.ImageOutput && strings.HasSuffix(ev.Part.Where, ", page ink") {
			if png, err = os.ReadFile(o.Path); err != nil {
				t.Fatal(err)
			}
			where = ev.Part.Where
		}
	})
	if png == nil {
		t.Fatal("no PNG of the page's ink")
	}
	size := visiontest.PixelSize(png)
	digest := sha256.Sum256(png)
	pngSum := hex.EncodeToString(digest[:])
	t.Logf("recorded: ink: %s, a %s PNG of %d bytes, SHA-256 %s", where, size, len(png), pngSum)

	// OneNote's stored recognition: each line's words, best guesses.
	tl, err := tool.NewExec(t.Context(), onenote.OneNoteSpec())
	if err != nil {
		t.Fatal(err)
	}
	r, err := onenote.NewReader(onenote.Config{OneNote: tl})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Read(t.Context(), onenote.Request{Input: section})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rec, err := range s.Records() {
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := rec.(*onenote.Page); ok {
			for _, line := range p.InkText {
				var words []string
				for _, w := range line {
					words = append(words, w.Text)
				}
				lines = append(lines, strings.Join(words, " "))
			}
		}
	}
	stored := strings.Join(lines, "\n")
	storedScore := scoreInk(truth, stored)
	t.Logf("recorded: ink: OneNote's recognition %q: CER %.4f (%d of %d), WER %.4f (%d of %d), %d of the truth's words",
		stored, storedScore.CER, storedScore.CharEdits, storedScore.Chars, storedScore.WER, storedScore.WordEdits, storedScore.Words, storedScore.WordsRight)

	c := visiontest.Router(t)
	ask := func(maxOut int, native string, parts ...protocol.ContentPart) (*protocol.Response, time.Duration, error) {
		start := time.Now()
		resp, err := c.Generate(t.Context(), &protocol.Request{
			Model:           inkModel,
			Input:           []protocol.Item{protocol.Message{Role: protocol.RoleUser, Content: parts}},
			Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
			MaxOutputTokens: maxOut,
			Native:          jsontext.Value(native),
		})
		return resp, time.Since(start), err
	}
	prompt := protocol.InputText{Text: inkPrompt}
	baseline, _, err := ask(0, `{"temperature":0,"max_completion_tokens":1}`, prompt)
	if err != nil {
		t.Fatalf("the text-only baseline: %v", err)
	}
	var reads []inkRead
	for run := 1; run <= inkRuns; run++ {
		resp, wall, err := ask(inkOutputCap, `{"temperature":0}`, prompt, protocol.InputImage{ImageURL: protocol.DataURL("image/png", png)})
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		text := strings.TrimSpace(visiontest.ResponseText(resp))
		rd := inkRead{run, text, visiontest.ResponseStatus(resp), resp.Usage.InputTokens - baseline.Usage.InputTokens,
			resp.Usage.OutputTokens, wall, scoreInk(truth, text)}
		reads = append(reads, rd)
		t.Logf("recorded: ink run %d: %s, %d image tokens, %d output tokens, %v: CER %.4f (%d of %d), WER %.4f (%d of %d), %d of the truth's words: %q",
			run, rd.Status, rd.ImageTokens, rd.OutputTokens, wall.Round(time.Millisecond), rd.Score.CER, rd.Score.CharEdits, rd.Score.Chars,
			rd.Score.WER, rd.Score.WordEdits, rd.Score.Words, rd.Score.WordsRight, text)
		if resp.Status != protocol.StatusCompleted || text == "" {
			t.Errorf("run %d: %s with %q, want completed with text", run, rd.Status, text)
		}
	}
	saveFindings(t, "ink.json", map[string]any{
		"section": section, "where": where, "png": map[string]any{"size": size, "bytes": len(png), "sha256": pngSum},
		"model": inkModel, "prompt": inkPrompt, "truth": truth, "baseline_input_tokens": baseline.Usage.InputTokens,
		"onenote": map[string]any{"text": stored, "score": storedScore}, "runs": reads,
	})
	if dir := os.Getenv(resultsEnv); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "ink.png"), png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
