//go:build (integration || findings) && linux

package media_test

import (
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// runners build ffmpeg and ffprobe through one runner each: the host's through the exec runner,
// and audio.FFmpegImage's through the container runner, which pulls the image first when it isn't
// present.
var runners = map[string]func(t *testing.T) media.Config{
	"exec": func(t *testing.T) media.Config {
		return tools(t, func(spec tool.Spec) (*tool.Tool, error) { return tool.NewExec(t.Context(), spec) })
	},
	"container": func(t *testing.T) media.Config {
		return tools(t, func(spec tool.Spec) (*tool.Tool, error) {
			return tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage}, spec)
		})
	},
}

// tools builds ffmpeg and ffprobe through build, logging their versions.
func tools(t *testing.T, build func(tool.Spec) (*tool.Tool, error)) media.Config {
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
	return media.Config{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// streamer builds a Streamer over cfg.
func streamer(t *testing.T, cfg media.Config) *media.Streamer {
	t.Helper()
	s, err := media.NewStreamer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
