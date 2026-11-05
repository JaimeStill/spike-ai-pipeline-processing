//go:build integration && linux

package audio_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// TestSpeechWindows cuts the speech fixture's planned windows through both runners and checks
// each window's WAV: 16 kHz mono s16 PCM as ffprobe reads it, its plan's span long to within a
// sample, and its samples the source's own over that span, as one decode of the whole source
// gives them; and that the runners write the same bytes.
func TestSpeechWindows(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("testdata", "speech.flac"))
	if err != nil {
		t.Fatal(err)
	}
	host := hostTools(t)
	source := decode(t, host.ffmpeg, path)
	got := map[string][]string{}
	for name, runner := range speechRunners {
		t.Run(name, func(t *testing.T) {
			s := runner(t)
			plan, err := s.Plan(t.Context(), audio.Request{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			ws := collect(t, s, t.Context(), audio.WindowsRequest{Path: path, Dir: dir, Concurrency: 2}, plan)
			if len(ws) != len(plan.Windows) {
				t.Fatalf("%d windows, want the plan's %d", len(ws), len(plan.Windows))
			}
			for i, w := range ws {
				t.Logf("recorded: window %d: %v, %d samples (%.6fs), sha256 %s", i, w.Window.Span, w.Samples, w.Duration().Seconds(), w.SHA256)
				got[name] = append(got[name], w.SHA256)
				if w.Index != i || w.Count != len(plan.Windows) || w.Window != plan.Windows[i] {
					t.Errorf("window %d of %d, %v; want %d of %d, %v", w.Index, w.Count, w.Window, i, len(plan.Windows), plan.Windows[i])
				}
				checkFormat(t, host.ffprobe, w.Path)
				planned := w.Window.Duration().Seconds() * audio.SampleRate
				if d := float64(w.Samples) - planned; d < -1 || d > 1 {
					t.Errorf("window %d holds %d samples; its span is %.3f samples", i, w.Samples, planned)
				}
				b, err := os.ReadFile(w.Path)
				if err != nil {
					t.Fatal(err)
				}
				a := first(w.Window)
				if want := source[2*a : 2*(a+w.Samples)]; !bytes.Equal(b[44:], want) {
					t.Errorf("window %d's samples aren't the source's from sample %d", i, a)
				}
			}
			if files := files(t, dir); !slices.Equal(files, names(ws)) {
				t.Errorf("the media directory holds %q, want the windows' %q", files, names(ws))
			}
		})
	}
	if e, c := got["exec"], got["container"]; len(got) == 2 && !slices.Equal(e, c) {
		t.Errorf("the exec runner wrote windows %q, the container runner %q", e, c)
	}
}

// TestWindowsResample cuts a 40 s 44.1 kHz stereo AAC source, which ffmpeg makes, through the
// exec runner, and checks that its windows are 16 kHz mono s16 PCM, each its plan's span long at
// 16 kHz to the sample.
func TestWindowsResample(t *testing.T) {
	host := hostTools(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tone.m4a")
	err := host.ffmpeg.Run(t.Context(), tool.Invocation{
		Args: []string{"-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=f=440:r=44100:d=40", "-ac", "2", "-c:a", "aac", path},
		Dir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := speechRunners["exec"](t)
	src, err := s.Probe(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded: source %v, %s, %d Hz, %d channel(s)", src.Duration, src.Codec, src.SampleRate, src.Channels)
	plan, err := audio.PlanWindows(src.Duration, nil, audio.Request{})
	if err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "media")
	if err := os.Mkdir(media, 0o700); err != nil {
		t.Fatal(err)
	}
	ws := collect(t, s, t.Context(), audio.WindowsRequest{Path: path, Dir: media, Concurrency: 2}, plan)
	if len(ws) != 2 {
		t.Fatalf("%d windows, want 2", len(ws))
	}
	for i, w := range ws {
		t.Logf("recorded: window %d: %v, %d samples", i, w.Window.Span, w.Samples)
		checkFormat(t, host.ffprobe, w.Path)
		if want := end(w.Window) - first(w.Window); w.Samples != want {
			t.Errorf("window %d holds %d samples, want %d", i, w.Samples, want)
		}
	}
}

// hostFFmpeg is the host's ffmpeg and ffprobe, through the exec runner, for the tests' own
// decoding and probing.
type hostFFmpeg struct {
	ffmpeg, ffprobe *tool.Tool
}

// hostTools builds the host's ffmpeg and ffprobe.
func hostTools(t *testing.T) hostFFmpeg {
	t.Helper()
	ffmpeg, err := tool.NewExec(t.Context(), audio.FFmpegSpec())
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := tool.NewExec(t.Context(), audio.FFprobeSpec())
	if err != nil {
		t.Fatal(err)
	}
	return hostFFmpeg{ffmpeg: ffmpeg, ffprobe: ffprobe}
}

// decode returns the whole of the source at path as 16 kHz mono 16-bit PCM, through ffmpeg.
func decode(t *testing.T, ffmpeg *tool.Tool, path string) []byte {
	t.Helper()
	var pcm bytes.Buffer
	err := ffmpeg.Run(t.Context(), tool.Invocation{
		Args:   []string{"-nostdin", "-loglevel", "error", "-i", path, "-ac", "1", "-ar", "16000", "-f", "s16le", "-"},
		Dir:    filepath.Dir(path),
		Stdout: &pcm,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pcm.Bytes()
}

// checkFormat checks, through ffprobe, that the WAV at path is one stream of 16 kHz mono s16 PCM.
func checkFormat(t *testing.T, ffprobe *tool.Tool, path string) {
	t.Helper()
	var out bytes.Buffer
	err := ffprobe.Run(t.Context(), tool.Invocation{
		Args: []string{"-v", "error", "-print_format", "json", "-show_entries",
			"format=format_name:stream=codec_name,sample_rate,channels,sample_fmt,bits_per_sample", path},
		Dir:    filepath.Dir(path),
		Stdout: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Format struct {
			Name string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			Codec      string `json:"codec_name"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
			SampleFmt  string `json:"sample_fmt"`
			Bits       int    `json:"bits_per_sample"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Format.Name != "wav" || len(report.Streams) != 1 {
		t.Fatalf("%s: format %q, %d streams; want one stream of wav", path, report.Format.Name, len(report.Streams))
	}
	st := report.Streams[0]
	if st.Codec != "pcm_s16le" || st.SampleRate != strconv.Itoa(audio.SampleRate) || st.Channels != 1 || st.SampleFmt != "s16" || st.Bits != 16 {
		t.Errorf("%s: %+v, want pcm_s16le, 16000 Hz, 1 channel, s16, 16 bits", path, st)
	}
}
