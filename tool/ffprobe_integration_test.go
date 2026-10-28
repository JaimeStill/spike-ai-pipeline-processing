//go:build integration && linux

package tool_test

import (
	"regexp"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// ffmpegImage is linuxserver/ffmpeg's 9.0.2 CLI image, build ls87, pinned by its index digest.
const ffmpegImage = "linuxserver/ffmpeg:9.0.2-cli-ls87@sha256:" +
	"6547359fac23a2747e606d380e10267b2923a38f68485d9cacefd039e74d3aa8"

// ffprobeSpec declares ffprobe, minimum 9.0. ffprobe prints "ffprobe version n9.0.2 ..." on the
// host and "ffprobe version 9.0.2 ..." in the image; the pattern skips the n.
func ffprobeSpec() tool.Spec {
	return tool.Spec{
		Program:        "ffprobe",
		VersionArgs:    []string{"-version"},
		VersionPattern: regexp.MustCompile(`ffprobe version n?(\d+(?:\.\d+)*)`),
		Minimum:        "9.0",
	}
}

// TestFFprobeVersionOnHost checks the host's ffprobe through the exec runner.
func TestFFprobeVersionOnHost(t *testing.T) {
	tl, err := tool.NewExec(t.Context(), ffprobeSpec())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s", tl.Name(), tl.Version())
}

// TestFFprobeVersionInImage checks ffprobe inside the pinned image through the container runner,
// pulling the image first when it isn't present.
func TestFFprobeVersionInImage(t *testing.T) {
	tl, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: ffmpegImage}, ffprobeSpec())
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.Name(); got != "ffprobe" {
		t.Errorf("Name() = %q, want ffprobe", got)
	}
	t.Logf("%s %s in %s", tl.Name(), tl.Version(), ffmpegImage)
}
