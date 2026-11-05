//go:build linux

package audio_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// flacProbe is ffprobe's report of a 49.82 s FLAC whose audio is its second stream, after a cover
// picture.
const flacProbe = `{"streams":[` +
	`{"index":0,"codec_name":"mjpeg","codec_type":"video"},` +
	`{"index":1,"codec_name":"flac","codec_type":"audio","sample_rate":"16000","channels":1,"duration":"49.820000"}],` +
	`"format":{"duration":"49.820000"}}`

// silenceLog is silencedetect's report: a silence ffmpeg prints a negative start for, one whole,
// one in %g's exponent form, and one still open at the stream's end.
const silenceLog = `[Parsed_silencedetect_0 @ 0x7f6e94003d40] silence_start: -0.002
[Parsed_silencedetect_0 @ 0x7f6e94003d40] silence_end: 0.322625 | silence_duration: 0.324625
[Parsed_silencedetect_0 @ 0x7f6e94003d40] silence_start: 24.22575
[Parsed_silencedetect_0 @ 0x7f6e94003d40] silence_end: 25.732625 | silence_duration: 1.506875
[Parsed_silencedetect_0 @ 0x7f6e94003d40] silence_start: 3.0e+01
[Parsed_silencedetect_0 @ 0x7f6e94003d40] silence_end: 31 | silence_duration: 1
[Parsed_silencedetect_0 @ 0x7f6e94003d40] silence_start: 49.5
`

// volumeLog is volumedetect's report, from two instances of the filter, the first of which saw no
// samples.
const volumeLog = `[Parsed_volumedetect_0 @ 0x55d15edee600] n_samples: 0
[Parsed_volumedetect_0 @ 0x7f9254003c80] n_samples: 797120
[Parsed_volumedetect_0 @ 0x7f9254003c80] mean_volume: -25.1 dB
[Parsed_volumedetect_0 @ 0x7f9254003c80] max_volume: -7.3 dB
`

func TestNewSplitterRefuses(t *testing.T) {
	installStandins(t, "9.0.2")
	ffmpeg, ffprobe := standinTools(t)
	for name, cfg := range map[string]audio.Config{
		"no ffmpeg":  {FFprobe: ffprobe},
		"no ffprobe": {FFmpeg: ffmpeg},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := audio.NewSplitter(cfg); err == nil {
				t.Error("NewSplitter succeeded")
			}
		})
	}
	t.Setenv(versionEnv, "8.1")
	old, oldProbe := standinTools(t)
	for name, cfg := range map[string]audio.Config{
		"ffmpeg 8.1":  {FFmpeg: old, FFprobe: ffprobe},
		"ffprobe 8.1": {FFmpeg: ffmpeg, FFprobe: oldProbe},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := audio.NewSplitter(cfg)
			var verr *tool.VersionError
			if !errors.As(err, &verr) || !errors.Is(err, tool.ErrTooOld) {
				t.Fatalf("err %v, want a *tool.VersionError wrapping ErrTooOld", err)
			}
			if verr.Found != "8.1" || verr.Minimum != audio.FFmpegMinimum {
				t.Errorf("found %s, minimum %s; want 8.1 and %s", verr.Found, verr.Minimum, audio.FFmpegMinimum)
			}
		})
	}
}

func TestSpecsMatchTheirVersions(t *testing.T) {
	for _, tt := range []struct {
		spec       tool.Spec
		host, wrap string
	}{
		{audio.FFmpegSpec(), "ffmpeg version n9.0.2 Copyright", "ffmpeg version 9.0.2 Copyright"},
		{audio.FFprobeSpec(), "ffprobe version n9.0.2 Copyright", "ffprobe version 9.0.2 Copyright"},
	} {
		for _, out := range []string{tt.host, tt.wrap} {
			if m := tt.spec.VersionPattern.FindStringSubmatch(out); m == nil || m[1] != "9.0.2" {
				t.Errorf("%s's pattern on %q: %q, want 9.0.2", tt.spec.Program, out, m)
			}
		}
		if tt.spec.Minimum != audio.FFmpegMinimum {
			t.Errorf("%s's minimum %s, want %s", tt.spec.Program, tt.spec.Minimum, audio.FFmpegMinimum)
		}
	}
}

func TestProbe(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	src, err := s.Probe(t.Context(), "speech.flac")
	if err != nil {
		t.Fatal(err)
	}
	want := audio.Source{Duration: 49820 * time.Millisecond, Stream: 1, Codec: "flac", SampleRate: 16000, Channels: 1}
	if src != want {
		t.Errorf("Probe = %+v, want %+v", src, want)
	}
	rs := runs(t)
	if len(rs) != 1 || rs[0].Program != "ffprobe" {
		t.Fatalf("runs %+v, want one of ffprobe", rs)
	}
	abs, err := filepath.Abs("speech.flac")
	if err != nil {
		t.Fatal(err)
	}
	if args := rs[0].Args; args[len(args)-1] != abs {
		t.Errorf("ffprobe read %q, want the absolute path %s", args[len(args)-1], abs)
	}
}

func TestDetectRefusesNoAudioBeforeFFmpeg(t *testing.T) {
	s := standinSplitter(t)
	for name, report := range map[string]string{
		"no audio stream": `{"streams":[{"index":0,"codec_name":"h264","codec_type":"video"}],"format":{"duration":"5.0"}}`,
		"no streams":      `{"streams":[],"format":{}}`,
		"no duration":     `{"streams":[{"index":0,"codec_name":"flac","codec_type":"audio","sample_rate":"16000","channels":1}],"format":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			t.Setenv(probeEnv, report)
			_, err := s.Detect(t.Context(), audio.Request{Path: "video.mp4"})
			if !errors.Is(err, audio.ErrNoAudio) {
				t.Errorf("err %v, want ErrNoAudio", err)
			}
			for _, r := range runs(t) {
				if r.Program != "ffprobe" {
					t.Errorf("%s ran: %q", r.Program, r.Args)
				}
			}
		})
	}
}

func TestDetectRelative(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	t.Setenv(volumeEnv, volumeLog)
	t.Setenv(silenceEnv, silenceLog)
	det, err := s.Detect(t.Context(), audio.Request{Path: "speech.flac"})
	if err != nil {
		t.Fatal(err)
	}
	if det.MeanVolume != -25.1 || det.Threshold != -40.1 || det.MinSilence != audio.DefaultMinSilence {
		t.Errorf("mean %v dB, threshold %v dB, minimum %v; want -25.1, -40.1, and %v",
			det.MeanVolume, det.Threshold, det.MinSilence, audio.DefaultMinSilence)
	}
	want := []audio.Span{span(0, 0.322625), span(24.22575, 25.732625), span(30, 31), span(49.5, 49.82)}
	if !reflect.DeepEqual(det.Silences, want) {
		t.Errorf("silences %v, want %v", det.Silences, want)
	}
	rs := runs(t)
	if got := programs(rs); !slices.Equal(got, []string{"ffprobe", "ffmpeg", "ffmpeg"}) {
		t.Fatalf("runs %q, want ffprobe, then ffmpeg twice", got)
	}
	if got := filterOf(rs[1]); got != "volumedetect" {
		t.Errorf("the first ffmpeg run's filter %q, want volumedetect", got)
	}
	if got := filterOf(rs[2]); got != "silencedetect=noise=-40.1dB:d=0.3" {
		t.Errorf("the second ffmpeg run's filter %q", got)
	}
	for _, r := range rs[1:] {
		if i := slices.Index(r.Args, "-map"); i < 0 || r.Args[i+1] != "0:1" {
			t.Errorf("ffmpeg's arguments %q don't map the audio stream, 0:1", r.Args)
		}
	}
}

func TestDetectRequestFields(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	t.Setenv(volumeEnv, volumeLog)
	tests := []struct {
		name   string
		req    audio.Request
		filter string
		runs   int
	}{
		{"below and minimum", audio.Request{Below: 20, MinSilence: 500 * time.Millisecond}, "silencedetect=noise=-45.1dB:d=0.5", 3},
		{"fixed threshold", audio.Request{Threshold: -42.5}, "silencedetect=noise=-42.5dB:d=0.3", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			tt.req.Path = "speech.flac"
			det, err := s.Detect(t.Context(), tt.req)
			if err != nil {
				t.Fatal(err)
			}
			rs := runs(t)
			if len(rs) != tt.runs {
				t.Fatalf("runs %q, want %d", programs(rs), tt.runs)
			}
			if got := filterOf(rs[len(rs)-1]); got != tt.filter {
				t.Errorf("filter %q, want %q", got, tt.filter)
			}
			if tt.req.Threshold != 0 && (det.MeanVolume != 0 || det.Threshold != tt.req.Threshold) {
				t.Errorf("mean %v dB, threshold %v dB; want 0 and %v", det.MeanVolume, det.Threshold, tt.req.Threshold)
			}
		})
	}
}

func TestRequestRefusedBeforeRuns(t *testing.T) {
	s := standinSplitter(t)
	for name, req := range map[string]audio.Request{
		"no path":                  {},
		"negative minimum":         {Path: "a.flac", MinSilence: -time.Second},
		"negative below":           {Path: "a.flac", Below: -1},
		"positive threshold":       {Path: "a.flac", Threshold: 3},
		"threshold and below":      {Path: "a.flac", Threshold: -40, Below: 10},
		"overlap of half a window": {Path: "a.flac", Overlap: audio.WindowLimit / 2},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Plan(t.Context(), req)
			if !errors.Is(err, audio.ErrRequest) {
				t.Errorf("err %v, want ErrRequest", err)
			}
			if rs := runs(t); len(rs) != 0 {
				t.Errorf("runs %q, want none", programs(rs))
			}
		})
	}
}

func TestDetectFFmpegFails(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	t.Setenv(failEnv, "noise\n[in#0] Error opening input: Invalid data found when processing input\n")
	_, err := s.Detect(t.Context(), audio.Request{Path: "speech.flac"})
	var exit *tool.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("err %v, want a *tool.ExitError with code 1", err)
	}
	if !strings.Contains(err.Error(), "Invalid data found") {
		t.Errorf("err %q doesn't carry ffmpeg's last line", err)
	}
}

func TestPlanStandin(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	t.Setenv(volumeEnv, volumeLog)
	t.Setenv(silenceEnv, silenceLog)
	plan, err := s.Plan(t.Context(), audio.Request{Path: "speech.flac"})
	if err != nil {
		t.Fatal(err)
	}
	want := []audio.Window{
		{Start: 0, End: 24979187500 * time.Nanosecond, Cut: audio.CutSilence},
		{Start: 22979187500 * time.Nanosecond, End: 49820 * time.Millisecond, Cut: audio.CutEnd},
	}
	if !reflect.DeepEqual(plan.Windows, want) {
		t.Errorf("windows %v, want %v", plan.Windows, want)
	}
}

// programs returns the runs' programs, in order.
func programs(rs []run) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Program)
	}
	return out
}

// filterOf returns an ffmpeg run's audio filter, the argument after -af.
func filterOf(r run) string {
	i := slices.Index(r.Args, "-af")
	if i < 0 || i+1 >= len(r.Args) {
		return ""
	}
	return r.Args[i+1]
}
