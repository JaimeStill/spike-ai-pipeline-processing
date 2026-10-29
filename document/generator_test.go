//go:build linux

package document_test

import (
	"github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// The composition root passes its *client.Client as the Generator; this fails to compile if the
// Client stops satisfying it.
var _ document.Generator = (*client.Client)(nil)
