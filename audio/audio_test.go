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
	}
}

// TestSpecsRefuseOld builds the stand-ins through the exec runner from the Specs as the package
// returns them: 9.0.2 passes, and the runner itself refuses 8.1, below the Specs' minimum.
func TestSpecsRefuseOld(t *testing.T) {
	for version, ok := range map[string]bool{"9.0.2": true, "8.1": false} {
		t.Run(version, func(t *testing.T) {
			installStandins(t, version)
			for _, spec := range []tool.Spec{audio.FFmpegSpec(), audio.FFprobeSpec()} {
				tl, err := tool.NewExec(t.Context(), spec)
				switch {
				case ok && (err != nil || tl.Version().String() != version):
					t.Errorf("NewExec(%s) over %s = %v, %v; want %s", spec.Program, version, tl, err, version)
				case !ok && !errors.Is(err, tool.ErrTooOld):
					t.Errorf("NewExec(%s) over %s returned %v, want ErrTooOld", spec.Program, version, err)
				}
			}
		})
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
		"no path":                            {},
		"negative minimum":                   {Path: "a.flac", MinSilence: -time.Second},
		"negative below":                     {Path: "a.flac", Below: -1},
		"positive threshold":                 {Path: "a.flac", Threshold: 3},
		"threshold and below":                {Path: "a.flac", Threshold: -40, Below: 10},
		"overlap of half a window":           {Path: "a.flac", Overlap: audio.WindowLimit / 2},
		"an unknown detector":                {Path: "a.flac", Detector: 2},
		"silencedetect's percentile":         {Path: "a.flac", Percentile: 10},
		"silencedetect's window":             {Path: "a.flac", FloorWindow: time.Minute},
		"silencedetect's margin":             {Path: "a.flac", Margin: 6},
		"the tracker below the mean":         {Path: "a.flac", Detector: audio.DetectorTracker, Below: 15},
		"the tracker's threshold and margin": {Path: "a.flac", Detector: audio.DetectorTracker, Threshold: -40, Margin: 6},
		"the tracker's short window":         {Path: "a.flac", Detector: audio.DetectorTracker, FloorWindow: time.Millisecond},
		"the tracker with no path":           {Detector: audio.DetectorTracker},
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

// gainProbe is ffprobe's report of speech-gain.wav: 75.23 s of 16 kHz mono 16-bit PCM.
const gainProbe = `{"streams":[` +
	`{"index":0,"codec_name":"pcm_s16le","codec_type":"audio","sample_rate":"16000","channels":1,"duration":"75.230000"}],` +
	`"format":{"duration":"75.230000"}}`

// TestPlanTracker plans speech-gain.wav with the tracker, the stand-in ffmpeg decoding it to its
// own samples: one ffmpeg run decodes the audio stream to 16 kHz mono 16-bit PCM, and the plan is
// the incremental path's over the same samples, a Tracker and a Planner fed them as a stream, as
// it is PlanWindows' of the silences Detect reports. Each silence reports its threshold.
func TestPlanTracker(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, gainProbe)
	t.Setenv(decodeEnv, filepath.Join("testdata", "speech-gain.wav"))
	pcm := samplesOf(t, "speech-gain.wav")
	for name, req := range map[string]audio.Request{
		"defaults": {},
		"settings": {Percentile: 20, FloorWindow: 10 * time.Second, Margin: 9, MinSilence: 400 * time.Millisecond, Overlap: time.Second},
		"fixed":    {Threshold: -46.2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			req.Path, req.Detector = "speech-gain.wav", audio.DetectorTracker
			plan, err := s.Plan(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			duration := 75230 * time.Millisecond
			if want := streamPlan(t, pcm, req, 1<<15, duration); !reflect.DeepEqual(plan.Windows, want) {
				t.Errorf("windows %v, the incremental path's %v", plan.Windows, want)
			}
			rs := runs(t)
			if got := programs(rs); !slices.Equal(got, []string{"ffprobe", "ffmpeg"}) {
				t.Fatalf("runs %q, want ffprobe, then ffmpeg once", got)
			}
			if got := filterOf(rs[1]); got != "aresample=16000" {
				t.Errorf("ffmpeg's filter %q, want aresample=16000", got)
			}
			args := strings.Join(rs[1].Args, " ")
			for _, want := range []string{"-map 0:0", "-ac 1 -ar 16000 -c:a pcm_s16le", "-f s16le -"} {
				if !strings.Contains(args, want) {
					t.Errorf("ffmpeg's arguments %q, without %q", args, want)
				}
			}
			det, err := s.Detect(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if det.Detector != audio.DetectorTracker || det.MeanVolume != 0 || det.Threshold != req.Threshold {
				t.Errorf("detector %v, mean %v dB, threshold %v dB; want the tracker, 0, and %v",
					det.Detector, det.MeanVolume, det.Threshold, req.Threshold)
			}
			if len(det.Silences) == 0 || len(det.Thresholds) != len(det.Silences) {
				t.Fatalf("%d silences and %d thresholds", len(det.Silences), len(det.Thresholds))
			}
			for i, th := range det.Thresholds {
				if th >= 0 || (req.Threshold != 0 && th != req.Threshold) {
					t.Errorf("silence %v's threshold %v dB", det.Silences[i], th)
				}
			}
			again, err := audio.PlanWindows(det.Source.Duration, det.Silences, req)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, plan) {
				t.Errorf("PlanWindows of Detect's silences %+v, Plan %+v", again, plan)
			}
		})
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
