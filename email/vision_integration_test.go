//go:build integration && linux

package email_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"fmt"
	"image"
	_ "image/gif"  // decodes a GIF's size
	_ "image/jpeg" // decodes a JPEG's size
	_ "image/png"  // decodes a PNG's size
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-client/format/completions"
	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
)

// The vision part of the integration tier: every distinct picture the fixtures' attachments route
// to vision, as stored or as office's picture conversion makes it a PNG, sent in one request each
// to Gemma 4 26B-A4B on the router SPIKE_ROUTER_URL names, which set A loads, by the package's
// own test-only router client, as office's and document's vision tests send theirs. It asserts
// only completion and records each picture's wall time, image tokens, and output tokens. There is
// no stand-in tier: the requests are test code, and the layer above owns inference.

const (
	// visionModel describes the pictures, as it does office's and transcribes document's pages.
	visionModel = "google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0"
	// describePrompt is the text beside each picture, office's.
	describePrompt = "Describe this picture in two or three sentences."
	// describeOutputCap bounds a description's output: two or three sentences run well under
	// it, and a model that loops on its text stops at it.
	describeOutputCap = 512
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

// picture is a distinct picture an attachment routes to vision: the bytes sent, their content
// type, and the attachments it came from, the first first.
type picture struct {
	data        []byte
	contentType string
	sources     []string
}

// describePictures sends each of pictures, in order, to visionModel beside describePrompt, one at
// a time, so each wall time is the picture's alone, and asserts only that each response is
// completed with text. It logs a recorded line per picture: its first source and the others that
// carry the same content, its content type and pixel size, wall time, image tokens, and output
// tokens; and one for them all. Image tokens are the request's input tokens less those of the
// same request without the picture, as office's TestVisionPictures and document's
// TestVisionImageTokensHost measure them: the image's tokens and the few that mark where an image
// begins and ends in the model's chat template.
func describePictures(t *testing.T, c *client.Client, pictures []*picture) {
	t.Helper()
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
	for i, p := range pictures {
		size := pixelSize(p.data)
		resp, elapsed, err := describe(describeOutputCap, `{"temperature":0}`, prompt, protocol.InputImage{ImageURL: protocol.DataURL(p.contentType, p.data)})
		if err != nil {
			failed++
			t.Errorf("%s: %s %s, %d bytes: %v", p.sources[0], p.contentType, size, len(p.data), err)
			t.Logf("recorded: vision picture %d of %d, %s: %s %s, %d bytes: %v wall, failed: %v",
				i+1, len(pictures), p.sources[0], p.contentType, size, len(p.data), elapsed.Round(time.Millisecond), err)
			continue
		}
		out := strings.TrimSpace(responseText(resp))
		if resp.Status != protocol.StatusCompleted || out == "" {
			failed++
			t.Errorf("%s: the response is %s with %q, want completed with text", p.sources[0], responseStatus(resp), out)
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
			i+1, len(pictures), p.sources[0], also, p.contentType, size, len(p.data),
			elapsed.Round(time.Millisecond), img, resp.Usage.OutputTokens, responseStatus(resp))
		t.Logf("description: %s: %q", p.sources[0], out)
	}
	t.Logf("recorded: vision pictures, %s, %d distinct pictures, one at a time: %v wall, %d image tokens, %d output tokens, %d failed; text-only baseline %d input tokens",
		visionModel, len(pictures), wall.Round(time.Millisecond), imageTokens, outputTokens, failed, without)
}

// pixelSize returns a picture's width and height as "WxH": a PNG's, JPEG's, or GIF's by
// image.DecodeConfig, a BMP's from its header, which the standard library doesn't decode, and
// "?x?" for any other.
func pixelSize(data []byte) string {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
	}
	// A BMP's info header gives its width and height as signed 32-bit little-endian integers at
	// offsets 18 and 22; a negative height marks a top-down bitmap.
	if len(data) >= 26 && string(data[:2]) == "BM" {
		w := int32(binary.LittleEndian.Uint32(data[18:22]))
		h := int32(binary.LittleEndian.Uint32(data[22:26]))
		return fmt.Sprintf("%dx%d", w, max(h, -h))
	}
	return "?x?"
}

// responseText returns a response's output text, the text of every OutputText part of its
// messages, in order, joined.
func responseText(r *protocol.Response) string {
	var s strings.Builder
	for _, it := range r.Output {
		if m, ok := it.(protocol.Message); ok {
			for _, p := range m.Content {
				if o, ok := p.(protocol.OutputText); ok {
					s.WriteString(o.Text)
				}
			}
		}
	}
	return s.String()
}

// responseStatus returns a response's status, with its incomplete reason or error when it has
// one.
func responseStatus(r *protocol.Response) string {
	switch {
	case r.IncompleteDetails != nil:
		return fmt.Sprintf("%s (%s)", r.Status, r.IncompleteDetails.Reason)
	case r.Error != nil:
		return fmt.Sprintf("%s (%s: %s)", r.Status, r.Error.Code, r.Error.Message)
	}
	return string(r.Status)
}
