//go:build integration && linux

package office_test

import (
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
)

// The vision tier: every distinct picture the fixtures hand onward, after conversion, sent in
// one request each to Gemma 4 26B-A4B on the router SPIKE_ROUTER_URL names, which set A loads.
// The tier asserts only completion and records each picture's wall time, image tokens, and
// output tokens. Charts are data in office's model, never media, so none is sent. There is no
// stand-in tier: the requests are test code, and the layer above owns inference.

const (
	// visionModel describes the pictures, as it transcribes document's pages.
	visionModel = "google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0"
	// describePrompt is the text beside each picture.
	describePrompt = "Describe this picture in two or three sentences."
	// describeOutputCap bounds a description's output: two or three sentences run well under
	// it, and a model that loops on its text stops at it.
	describeOutputCap = 512
)

// handedOn is a distinct picture handed onward: the file's bytes, its content type, and the
// fixtures and stored names it came from, the first first. Pictures are distinct by office's name
// for the stored file, the SHA-256 of its bytes: a picture handed on as stored is that file, and
// a converted one's PNG follows from it, the same bytes on every conversion, as TestPictures
// asserts.
type handedOn struct {
	data        []byte
	contentType string
	sources     []string
}

// TestVisionPictures converts every picture fixture as Markdown through the real office, soffice
// in the default image, and the host's magick, and sends each distinct picture handed onward,
// across all fixtures, to visionModel beside describePrompt: a stored PNG, JPEG, GIF, or BMP as
// it is, an EMF, WMF, or TIFF as its PNG. The requests run one at a time, so each wall time is
// the picture's alone. It asserts only that each response is completed with text.
//
// It logs a recorded line per picture: its first fixture and stored name, the others that hand
// on the same file, its content type and pixel size, wall time, image tokens, and output tokens.
// Image tokens are the request's input tokens less those of the same request without the
// picture, as document's TestVisionImageTokensHost measures them: the image's tokens and the few
// that mark where an image begins and ends in the model's chat template.
func TestVisionPictures(t *testing.T) {
	c := visiontest.Router(t)
	conv := fullConverter(t)

	var paths []string
	for _, pattern := range pictureFixtures {
		matches, err := filepath.Glob(filepath.Join("testdata", pattern))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	pictures := map[string]*handedOn{}
	var order []string
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		res, err := conv.Convert(t.Context(), f, office.Request{Dir: t.TempDir(), Name: filepath.Base(path), Format: office.Markdown})
		_ = f.Close()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, m := range res.Media {
			if m.File == "" {
				t.Errorf("%s: %s has no file handed onward", path, m.Name)
				continue
			}
			source := filepath.Base(path) + " " + m.Name[:12] + "…" + filepath.Ext(m.Name)
			if p, ok := pictures[m.Name]; ok {
				p.sources = append(p.sources, source)
				continue
			}
			data, err := os.ReadFile(m.File)
			if err != nil {
				t.Fatal(err)
			}
			ct := m.ContentType
			if m.Converted {
				ct = "image/png"
			}
			pictures[m.Name] = &handedOn{data: data, contentType: ct, sources: []string{source}}
			order = append(order, m.Name)
		}
	}
	if len(order) == 0 {
		t.Fatal("the fixtures hand on no pictures")
	}

	describe := func(maxOut int, native string, parts ...protocol.ContentPart) (*protocol.Response, time.Duration, error) {
		start := time.Now()
		resp, err := c.Generate(t.Context(), &protocol.Request{
			Model:           visionModel,
			Input:           []protocol.Item{protocol.Message{Role: protocol.RoleUser, Content: parts}},
			Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
			MaxOutputTokens: maxOut,
			Native:          jsontext.Value(native),
		})
		return resp, time.Since(start), err
	}
	prompt := protocol.InputText{Text: describePrompt}
	baseline, _, err := describe(0, `{"temperature":0,"max_completion_tokens":1}`, prompt)
	if err != nil {
		t.Fatalf("the text-only baseline: %v", err)
	}
	without := baseline.Usage.InputTokens

	var wall time.Duration
	var imageTokens, outputTokens, failed int
	for i, key := range order {
		p := pictures[key]
		size := visiontest.PixelSize(p.data)
		resp, elapsed, err := describe(describeOutputCap, `{"temperature":0}`, prompt, protocol.InputImage{ImageURL: protocol.DataURL(p.contentType, p.data)})
		if err != nil {
			failed++
			t.Errorf("%s: %s %s, %d bytes: %v", p.sources[0], p.contentType, size, len(p.data), err)
			t.Logf("recorded: vision picture %d of %d, %s: %s %s, %d bytes: %v wall, failed: %v",
				i+1, len(order), p.sources[0], p.contentType, size, len(p.data), elapsed.Round(time.Millisecond), err)
			continue
		}
		out := strings.TrimSpace(visiontest.ResponseText(resp))
		if resp.Status != protocol.StatusCompleted || out == "" {
			failed++
			t.Errorf("%s: the response is %s with %q, want completed with text", p.sources[0], visiontest.ResponseStatus(resp), out)
		}
		img := resp.Usage.InputTokens - without
		wall += elapsed
		imageTokens += img
		outputTokens += resp.Usage.OutputTokens
		also := ""
		if len(p.sources) > 1 {
			also = fmt.Sprintf(", also %s", strings.Join(p.sources[1:], ", "))
		}
		t.Logf("recorded: vision picture %d of %d, %s%s: %s %s, %d bytes: %v wall, %d image tokens, %d output tokens, %s",
			i+1, len(order), p.sources[0], also, p.contentType, size, len(p.data),
			elapsed.Round(time.Millisecond), img, resp.Usage.OutputTokens, visiontest.ResponseStatus(resp))
		t.Logf("description: %s: %q", p.sources[0], out)
	}
	t.Logf("recorded: vision pictures, %s, %d distinct pictures from %d fixtures, one at a time: %v wall, %d image tokens, %d output tokens, %d failed; text-only baseline %d input tokens",
		visionModel, len(order), len(paths), wall.Round(time.Millisecond), imageTokens, outputTokens, failed, without)
}
