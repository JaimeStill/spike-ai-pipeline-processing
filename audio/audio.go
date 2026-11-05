//go:build linux

package audio

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// DefaultBelow is how far below the source's mean volume the silence threshold is when a request
// sets neither Below nor Threshold: 15 dB.
const DefaultBelow = 15.0

// DefaultMinSilence is the shortest silence a request's zero MinSilence asks for: 0.3 s, about a
// pause between words, which a cut there doesn't split.
const DefaultMinSilence = 300 * time.Millisecond

var (
	// ErrRequest is the cause of a refused request: a field out of range, before anything runs.
	ErrRequest = errors.New("invalid request")
	// ErrNoAudio is the cause of the error for a source ffprobe finds no audio stream in. Its
	// probe has run; ffmpeg hasn't.
	ErrNoAudio = errors.New("no audio stream")
)

// Config is what a Splitter is built over: the Tools the caller built, through either runner.
// Both are required.
type Config struct {
	// FFmpeg is FFmpeg's ffmpeg, typically built from FFmpegSpec, on the host or in FFmpegImage.
	// It measures the source's volume and detects its silences.
	FFmpeg *tool.Tool
	// FFprobe is FFmpeg's ffprobe, typically built from FFprobeSpec, on the host or in
	// FFmpegImage. It reads the source's duration and audio stream.
	FFprobe *tool.Tool
}

// Splitter plans a source's windows: it probes the source, detects its silences, and plans
// windows that end in them. Its methods are safe for concurrent use.
type Splitter struct {
	ffmpeg  *tool.Tool
	ffprobe *tool.Tool
}

// NewSplitter builds a Splitter over cfg's Tools. It refuses a nil FFmpeg or FFprobe, and either
// older than FFmpegMinimum, whatever minimum the Tool's own Spec declared, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func NewSplitter(cfg Config) (*Splitter, error) {
	if cfg.FFmpeg == nil {
		return nil, errors.New("audio: no ffmpeg tool")
	}
	if cfg.FFprobe == nil {
		return nil, errors.New("audio: no ffprobe tool")
	}
	for _, t := range []*tool.Tool{cfg.FFmpeg, cfg.FFprobe} {
		if err := atLeast(t, FFmpegMinimum); err != nil {
			return nil, err
		}
	}
	return &Splitter{ffmpeg: cfg.FFmpeg, ffprobe: cfg.FFprobe}, nil
}

// Request is one source's silence detection and plan.
type Request struct {
	// Path is the source's file, any container and codec FFmpeg reads, which is read and never
	// changed. It is required; the runs read it by its absolute path, in its own directory, which
	// the container runner mounts.
	Path string
	// Overlap is how long before a window's end the next window starts: zero is DefaultOverlap,
	// 2 s, and it must be under WindowLimit/2, as CheckOverlap says.
	Overlap time.Duration
	// MinSilence is the shortest silence detected: zero is DefaultMinSilence, 0.3 s. Below zero
	// is refused.
	MinSilence time.Duration
	// Below is how far below the source's mean volume, in dB, the silence threshold is: zero is
	// DefaultBelow, 15 dB, unless Threshold is set. Below zero is refused.
	Below float64
	// Threshold, when not zero, is a fixed silence threshold in dB relative to full scale, such
	// as -40, in place of one relative to the mean volume, which then isn't measured. It must be
	// below zero, and Below must be zero with it.
	Threshold float64
}

// detection is a request's detection settings, its defaults applied.
type detection struct {
	path       string
	minSilence time.Duration
	below      float64
	threshold  float64
	fixed      bool
}

// check validates req's detection fields and applies their defaults; an invalid field is an error
// wrapping ErrRequest.
func (req Request) check() (detection, error) {
	if req.Path == "" {
		return detection{}, fmt.Errorf("audio: no source path: %w", ErrRequest)
	}
	path, err := filepath.Abs(req.Path)
	if err != nil {
		return detection{}, fmt.Errorf("audio: source path %s: %w: %w", req.Path, ErrRequest, err)
	}
	d := detection{path: path, minSilence: cmp.Or(req.MinSilence, DefaultMinSilence)}
	switch {
	case req.MinSilence < 0:
		return detection{}, fmt.Errorf("audio: minimum silence %v is below zero: %w", req.MinSilence, ErrRequest)
	case !finite(req.Below) || req.Below < 0:
		return detection{}, fmt.Errorf("audio: %v dB below the mean isn't zero or more: %w", req.Below, ErrRequest)
	case !finite(req.Threshold) || req.Threshold > 0:
		return detection{}, fmt.Errorf("audio: threshold %v dB isn't below zero: %w", req.Threshold, ErrRequest)
	case req.Threshold != 0 && req.Below != 0:
		return detection{}, fmt.Errorf("audio: a threshold of %v dB and %v dB below the mean: %w", req.Threshold, req.Below, ErrRequest)
	case req.Threshold != 0:
		d.threshold, d.fixed = req.Threshold, true
	default:
		d.below = cmp.Or(req.Below, DefaultBelow)
	}
	return d, nil
}

// finite reports whether f is neither NaN nor infinite.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Source is what ffprobe reports of a source: its duration and its first audio stream.
type Source struct {
	// Duration is the source's duration, the container's, or its audio stream's when the
	// container gives none.
	Duration time.Duration
	// Stream is the audio stream's index in the container, which ffmpeg's runs map.
	Stream int
	// Codec is the stream's codec name, as ffprobe gives it, such as flac or pcm_s16le.
	Codec string
	// SampleRate is the stream's sample rate, in Hz.
	SampleRate int
	// Channels is the stream's channel count.
	Channels int
}

// probeReport is the part of ffprobe's JSON report Probe reads.
type probeReport struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		Index      int    `json:"index"`
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
		Duration   string `json:"duration"`
	} `json:"streams"`
}

// Probe reads the source at path through ffprobe: its duration and its first audio stream's
// index, codec, sample rate, and channels. A source with no audio stream is an error wrapping
// ErrNoAudio, as is one ffprobe reports no duration for; a source ffprobe can't read is its
// *tool.ExitError, wrapped.
func (s *Splitter) Probe(ctx context.Context, path string) (Source, error) {
	if path == "" {
		return Source{}, fmt.Errorf("audio: no source path: %w", ErrRequest)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Source{}, fmt.Errorf("audio: source path %s: %w: %w", path, ErrRequest, err)
	}
	return s.probe(ctx, abs)
}

// probe is Probe on an absolute path.
func (s *Splitter) probe(ctx context.Context, path string) (Source, error) {
	var stdout bytes.Buffer
	err := s.ffprobe.Run(ctx, tool.Invocation{
		Args: []string{
			"-v", "error", "-print_format", "json",
			"-show_entries", "format=duration:stream=index,codec_type,codec_name,sample_rate,channels,duration",
			path,
		},
		Dir:    filepath.Dir(path),
		Stdout: &stdout,
	})
	if err != nil {
		return Source{}, fmt.Errorf("audio: probe %s: %w", path, err)
	}
	var report probeReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return Source{}, fmt.Errorf("audio: probe %s: ffprobe's report: %w", path, err)
	}
	for _, st := range report.Streams {
		if st.CodecType != "audio" {
			continue
		}
		src := Source{Stream: st.Index, Codec: st.CodecName, Channels: st.Channels}
		if src.SampleRate, err = strconv.Atoi(st.SampleRate); err != nil {
			return Source{}, fmt.Errorf("audio: probe %s: stream %d's sample rate %q: %w", path, st.Index, st.SampleRate, err)
		}
		d := cmp.Or(report.Format.Duration, st.Duration)
		if d == "" || d == "N/A" {
			return Source{}, fmt.Errorf("audio: probe %s: no duration: %w", path, ErrNoAudio)
		}
		if src.Duration, err = seconds(d); err != nil {
			return Source{}, fmt.Errorf("audio: probe %s: duration: %w", path, err)
		}
		if src.Duration <= 0 {
			return Source{}, fmt.Errorf("audio: probe %s: duration %v: %w", path, src.Duration, ErrNoAudio)
		}
		return src, nil
	}
	return Source{}, fmt.Errorf("audio: probe %s: %w", path, ErrNoAudio)
}

// seconds parses a count of seconds as FFmpeg prints one, such as "49.820000" or "5e-05", to the
// nearest nanosecond.
func seconds(s string) (time.Duration, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if !finite(f) {
		return 0, fmt.Errorf("%q seconds isn't finite", s)
	}
	return time.Duration(math.Round(f * float64(time.Second))), nil
}

// Detection is a source's silences, as Detect found them.
type Detection struct {
	// Source is the source, as Probe reads it.
	Source Source
	// MeanVolume is the audio stream's mean volume in dB, as volumedetect measures it; zero when
	// the request's Threshold is fixed, which needs no measure.
	MeanVolume float64
	// Threshold is the silence threshold silencedetect ran with, in dB: MeanVolume less the
	// request's Below, or the request's fixed Threshold.
	Threshold float64
	// MinSilence is the shortest silence silencedetect ran with.
	MinSilence time.Duration
	// Silences are the silences, in order, each within 0 and the source's Duration: a silence
	// still open at the stream's end ends at Duration.
	Silences []Span
}

// meanVolume matches volumedetect's mean volume line; ffmpeg prints it to the tenth of a dB.
var meanVolume = regexp.MustCompile(`\] mean_volume: (\S+) dB`)

// silenceStart and silenceEnd match silencedetect's lines, each a time in seconds as FFmpeg prints
// one, %.6g.
var (
	silenceStart = regexp.MustCompile(`\] silence_start: (\S+)`)
	silenceEnd   = regexp.MustCompile(`\] silence_end: (\S+)`)
)

// Detect finds the silences of the source req.Path names. It validates req's detection fields
// first, an invalid one an error wrapping ErrRequest before anything runs; then it probes the
// source, refusing one with no audio stream, with ErrNoAudio, before ffmpeg runs. With a relative
// threshold, one ffmpeg run's volumedetect measures the audio stream's mean volume, and the
// threshold is that less req.Below; a second run's silencedetect then finds the silences of at
// least req.MinSilence below the threshold, the only run with a fixed one. Every run maps the
// audio stream Probe found, and reads the source by its absolute path.
func (s *Splitter) Detect(ctx context.Context, req Request) (Detection, error) {
	d, err := req.check()
	if err != nil {
		return Detection{}, err
	}
	return s.detect(ctx, d)
}

// detect is Detect on a checked request.
func (s *Splitter) detect(ctx context.Context, d detection) (Detection, error) {
	src, err := s.probe(ctx, d.path)
	if err != nil {
		return Detection{}, err
	}
	det := Detection{Source: src, Threshold: d.threshold, MinSilence: d.minSilence}
	if !d.fixed {
		mean := math.NaN()
		err := s.filter(ctx, d.path, src, "volumedetect", func(line string) error {
			m := meanVolume.FindStringSubmatch(line)
			if m == nil {
				return nil
			}
			// volumedetect prints a line for each instance of the filter, the last the one that
			// saw the stream; an earlier one may have seen no samples.
			v, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				return fmt.Errorf("mean volume %q: %w", m[1], err)
			}
			mean = v
			return nil
		})
		if err != nil {
			return Detection{}, err
		}
		if !finite(mean) {
			return Detection{}, fmt.Errorf("audio: volumedetect on %s: no finite mean volume", d.path)
		}
		det.MeanVolume, det.Threshold = mean, mean-d.below
	}
	filter := fmt.Sprintf("silencedetect=noise=%sdB:d=%s",
		strconv.FormatFloat(det.Threshold, 'f', -1, 64), strconv.FormatFloat(d.minSilence.Seconds(), 'f', -1, 64))
	open, opened := time.Duration(0), false
	err = s.filter(ctx, d.path, src, filter, func(line string) error {
		if m := silenceStart.FindStringSubmatch(line); m != nil {
			t, err := seconds(m[1])
			if err != nil {
				return fmt.Errorf("silence start: %w", err)
			}
			open, opened = t, true
			return nil
		}
		if m := silenceEnd.FindStringSubmatch(line); m != nil && opened {
			t, err := seconds(m[1])
			if err != nil {
				return fmt.Errorf("silence end: %w", err)
			}
			det.Silences = appendSilence(det.Silences, Span{open, t}, src.Duration)
			opened = false
		}
		return nil
	})
	if err != nil {
		return Detection{}, err
	}
	if opened {
		det.Silences = appendSilence(det.Silences, Span{open, src.Duration}, src.Duration)
	}
	return det, nil
}

// appendSilence appends s, clipped to 0 and duration, to silences, unless the clip leaves it empty.
func appendSilence(silences []Span, s Span, duration time.Duration) []Span {
	s = Span{max(s.Start, 0), min(s.End, duration)}
	if s.Start >= s.End {
		return silences
	}
	return append(silences, s)
}

// filter runs ffmpeg over the source's audio stream through one audio filter, discarding the
// output, and passes each line of its stderr, where the filter reports, to line as it comes. A
// line's error stops the reading and is the run's; a failed run's error carries its last stderr
// line.
func (s *Splitter) filter(ctx context.Context, path string, src Source, filter string, line func(string) error) error {
	w := &lineWriter{line: line}
	err := s.ffmpeg.Run(ctx, tool.Invocation{
		Args: []string{
			"-nostdin", "-hide_banner", "-nostats", "-loglevel", "info",
			"-i", path, "-map", "0:" + strconv.Itoa(src.Stream), "-af", filter, "-f", "null", "-",
		},
		Dir:    filepath.Dir(path),
		Stderr: w,
	})
	w.flush()
	if err == nil {
		err = w.err
	}
	if err != nil {
		if w.last != "" {
			return fmt.Errorf("audio: ffmpeg %s on %s: %w: %s", filter, path, err, w.last)
		}
		return fmt.Errorf("audio: ffmpeg %s on %s: %w", filter, path, err)
	}
	return nil
}

// maxLine is the longest stderr line a lineWriter passes whole; a longer one is cut there.
const maxLine = 4 << 10

// lineWriter splits what is written to it into lines, passing each to line, without its newline,
// and keeping the last that isn't empty. Once line returns an error, it keeps the error and passes
// nothing more. Its memory is one line, at most maxLine.
type lineWriter struct {
	line    func(string) error
	partial []byte
	last    string
	err     error
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexAny(p, "\r\n")
		if i < 0 {
			w.partial = append(w.partial, p[:min(len(p), maxLine-len(w.partial))]...)
			break
		}
		w.partial = append(w.partial, p[:min(i, maxLine-len(w.partial))]...)
		w.emit()
		p = p[i+1:]
	}
	return n, nil
}

// flush passes the line left without a newline, if any.
func (w *lineWriter) flush() {
	if len(w.partial) > 0 {
		w.emit()
	}
}

// emit passes the partial line to line and empties it.
func (w *lineWriter) emit() {
	s := string(bytes.TrimSpace(w.partial))
	w.partial = w.partial[:0]
	if s == "" {
		return
	}
	w.last = s
	if w.err == nil {
		w.err = w.line(s)
	}
}

// Plan plans the windows of the source req.Path names: Detect's silences, through PlanWindows,
// with req.Overlap. An Overlap CheckOverlap refuses is an error before anything runs. The same
// source and request give the same plan.
func (s *Splitter) Plan(ctx context.Context, req Request) (Plan, error) {
	if _, err := CheckOverlap(req.Overlap); err != nil {
		return Plan{}, err
	}
	det, err := s.Detect(ctx, req)
	if err != nil {
		return Plan{}, err
	}
	return PlanWindows(det.Source.Duration, det.Silences, req)
}
