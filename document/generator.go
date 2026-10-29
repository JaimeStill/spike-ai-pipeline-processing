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
