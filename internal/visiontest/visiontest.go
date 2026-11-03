//go:build integration && linux

package visiontest

import (
	"bytes"
	"encoding/binary"
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

// Router returns a Client of the router SPIKE_ROUTER_URL names, speaking Chat Completions, with
// autoload=false so a request fails rather than load a model. It skips t when SPIKE_ROUTER_URL
// is unset.
func Router(t testing.TB) *client.Client {
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

// PixelSize returns a picture's width and height as "WxH": a PNG's, JPEG's, or GIF's by
// image.DecodeConfig, a BMP's from its header, which the standard library doesn't decode, and
// "?x?" for any other.
func PixelSize(data []byte) string {
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

// ResponseText returns a response's output text, the text of every OutputText part of its
// messages, in order, joined.
func ResponseText(r *protocol.Response) string {
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

// ResponseStatus returns a response's status, with its incomplete reason or error when it has
// one.
func ResponseStatus(r *protocol.Response) string {
	switch {
	case r.IncompleteDetails != nil:
		return fmt.Sprintf("%s (%s)", r.Status, r.IncompleteDetails.Reason)
	case r.Error != nil:
		return fmt.Sprintf("%s (%s: %s)", r.Status, r.Error.Code, r.Error.Message)
	}
	return string(r.Status)
}
