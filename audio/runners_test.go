//go:build (integration || findings) && linux

package audio_test

import (
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// speechRunners build a Splitter over ffmpeg and ffprobe through one runner each: the host's
// through the exec runner, and FFmpegImage's through the container runner, which pulls the image
// first when it isn't present.
var speechRunners = map[string]func(t *testing.T) *audio.Splitter{
	"exec": func(t *testing.T) *audio.Splitter {
		return splitter(t, func(spec tool.Spec) (*tool.Tool, error) { return tool.NewExec(t.Context(), spec) })
	},
	"container": func(t *testing.T) *audio.Splitter {
		return splitter(t, func(spec tool.Spec) (*tool.Tool, error) {
			return tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage}, spec)
		})
	},
}

// splitter builds ffmpeg and ffprobe through build and a Splitter over them, logging their
// versions.
func splitter(t *testing.T, build func(tool.Spec) (*tool.Tool, error)) *audio.Splitter {
	t.Helper()
	ffmpeg, err := build(audio.FFmpegSpec())
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := build(audio.FFprobeSpec())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s, %s %s", ffmpeg.Name(), ffmpeg.Version(), ffprobe.Name(), ffprobe.Version())
	s, err := audio.NewSplitter(audio.Config{FFmpeg: ffmpeg, FFprobe: ffprobe})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
