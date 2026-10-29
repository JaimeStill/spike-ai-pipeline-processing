//go:build linux

package document

import (
	"context"

	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
)

// Generator sends one request to a model and returns its response: the one method the package
// needs of spike-ai-pipeline-client, which *client.Client satisfies. The composition root passes
// its Client; a test passes a stand-in.
type Generator interface {
	Generate(ctx context.Context, req *protocol.Request) (*protocol.Response, error)
}

// TranscribeInstructions asks a vision model for a page's text: every word on the page, in reading
// order, as plain text, and nothing else. A request carries it as the text of one user message
// beside the page's image. It asks for no layout or formatting, so the answer compares with a
// page's ground truth as text.
const TranscribeInstructions = "Transcribe every word on this page, in reading order, as plain text. " +
	"Reply with the page's text and nothing else: no commentary, no description of the page, " +
	"and no Markdown or other formatting."
