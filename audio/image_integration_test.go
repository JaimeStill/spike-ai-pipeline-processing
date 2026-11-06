//go:build integration && linux

package audio_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestImageCapabilities checks that FFmpegImage's ffmpeg has what media asks of it: the filters
// scdet, select, and showinfo, listed and run together over a lavfi test source, and the filters
// of media's run, settb, metadata, scale, format, and aresample beside select, listed and the
// frames' chain run as media runs it; the protocols srt, rtmp, http, https, and udp for input and
// output; and HLS, which FFmpeg reads and writes as a format, its demuxer and muxer, not as a
// protocol. It records whether the image has the x11grab device and requires nothing of it.
func TestImageCapabilities(t *testing.T) {
	ffmpeg, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage}, audio.FFmpegSpec())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s in %s", ffmpeg.Name(), ffmpeg.Version(), audio.FFmpegImage)
	list := func(t *testing.T, flag string) string {
		t.Helper()
		var out bytes.Buffer
		err := ffmpeg.Run(t.Context(), tool.Invocation{Args: []string{"-nostdin", "-hide_banner", flag}, Stdout: &out})
		if err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	t.Run("filters", func(t *testing.T) {
		// A line of -filters is its flags, the filter's name, its pads, and its description.
		have := map[string]bool{}
		for line := range strings.Lines(list(t, "-filters")) {
			if f := strings.Fields(line); len(f) > 1 {
				have[f[1]] = true
			}
		}
		for _, name := range []string{"scdet", "select", "showinfo", "settb", "metadata", "scale", "format", "aresample"} {
			if !have[name] {
				t.Errorf("the image's ffmpeg has no %s filter", name)
			}
		}
		// Ten frames of a test source through the three filters: showinfo logs each frame select
		// passes, and select passes every frame whose scene score is at least zero.
		var stderr bytes.Buffer
		err := ffmpeg.Run(t.Context(), tool.Invocation{
			Args: []string{
				"-nostdin", "-hide_banner", "-f", "lavfi", "-i", "testsrc=duration=1:rate=10:size=160x120",
				"-vf", "scdet=threshold=1,select='gte(scene,0)',showinfo", "-f", "null", "-",
			},
			Stderr: &stderr,
		})
		if err != nil {
			t.Fatalf("%v: %s", err, stderr.String())
		}
		if n := strings.Count(stderr.String(), " pts_time:"); n != 10 {
			t.Errorf("showinfo logged %d frames, want 10", n)
		}
		// The same ten frames through media's chain of frame filters, scaled to fit 80 pixels:
		// metadata reports each frame select passes.
		stderr.Reset()
		err = ffmpeg.Run(t.Context(), tool.Invocation{
			Args: []string{
				"-nostdin", "-hide_banner", "-f", "lavfi", "-i", "testsrc=duration=1:rate=10:size=160x120",
				"-vf", "select='gte(scene,0)',settb=AVTB,metadata=mode=print:key=lavfi.scene_score," +
					"scale=w='min(iw,80)':h='min(ih,80)':force_original_aspect_ratio=decrease:" +
					"flags=bicubic+accurate_rnd+full_chroma_int+bitexact,format=rgb24",
				"-f", "null", "-",
			},
			Stderr: &stderr,
		})
		if err != nil {
			t.Fatalf("%v: %s", err, stderr.String())
		}
		if n := strings.Count(stderr.String(), "lavfi.scene_score="); n != 10 {
			t.Errorf("metadata reported %d frames, want 10", n)
		}
	})

	t.Run("protocols", func(t *testing.T) {
		// -protocols lists the input protocols under "Input:" and the output protocols under
		// "Output:", a name to a line.
		have := map[string]map[string]bool{"Input:": {}, "Output:": {}}
		var section map[string]bool
		for line := range strings.Lines(list(t, "-protocols")) {
			name := strings.TrimSpace(line)
			if s, ok := have[name]; ok {
				section = s
			} else if section != nil {
				section[name] = true
			}
		}
		for _, name := range []string{"srt", "rtmp", "http", "https", "udp"} {
			for dir, s := range have {
				if !s[name] {
					t.Errorf("the image's ffmpeg has no %s protocol for %s", name, strings.ToLower(strings.TrimSuffix(dir, ":")))
				}
			}
		}
	})

	t.Run("hls", func(t *testing.T) {
		// A line of -formats is its flags, D for a demuxer and E for a muxer, then its names.
		for line := range strings.Lines(list(t, "-formats")) {
			if f := strings.Fields(line); len(f) > 1 && f[1] == "hls" {
				if f[0] != "DE" {
					t.Errorf("the image's hls format is %q, want DE, a demuxer and a muxer", f[0])
				}
				return
			}
		}
		t.Error("the image's ffmpeg has no hls format")
	})

	t.Run("x11grab", func(t *testing.T) {
		have := false
		for line := range strings.Lines(list(t, "-devices")) {
			if f := strings.Fields(line); len(f) > 1 && f[1] == "x11grab" {
				have = true
			}
		}
		t.Logf("recorded: x11grab device present: %t", have)
	})
}
