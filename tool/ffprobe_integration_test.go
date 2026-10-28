//go:build integration && linux

package tool_test

import (
	"regexp"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestFFprobeVersionOnHost checks the host's ffprobe through the exec runner. ffprobe prints
// "ffprobe version n9.0.2 ..."; the pattern skips the n.
func TestFFprobeVersionOnHost(t *testing.T) {
	tl, err := tool.NewExec(t.Context(), tool.Spec{
		Program:        "ffprobe",
		VersionArgs:    []string{"-version"},
		VersionPattern: regexp.MustCompile(`ffprobe version n?(\d+(?:\.\d+)*)`),
		Minimum:        "9.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s", tl.Name(), tl.Version())
}
