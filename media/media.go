//go:build linux

package media

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// DefaultSceneThreshold is the scene threshold a request's zero SceneThreshold asks for: 0.2, on
// the 0 to 1 scale of ffmpeg's select filter's scene score. On the committed fixture it takes the
// four hard cuts, scored 1, and no other frame; on Tears of Steel's 180 s clip it takes the same 30
// frames, at the same times, as scdet at its default threshold, 10 on scdet's own 0 to 100 scale,
// as any threshold from 0.15 to 0.3 does.
const DefaultSceneThreshold = 0.2

// DefaultMaxGap is the longest a request's zero MaxGap lets the stream go without a frame: 10 s.
const DefaultMaxGap = 10 * time.Second

var (
	// ErrRequest is the cause of a refused request: a field out of range, before anything runs. It
	// is audio's own, so a refusal of a request's windowing fields, which audio validates, matches
	// it as a refusal of media's own fields does.
	ErrRequest = audio.ErrRequest
	// ErrNoMedia is the cause of the error for a source without the streams the request takes: a
	// file ffprobe finds neither an audio stream nor a video stream in, or without one its Tracks
	// requires, whose probe has run and ffmpeg hasn't; or a reader or URL without one its Tracks
	// requires, which ffmpeg found as it opened the source, before any event.
	ErrNoMedia = errors.New("no audio or video stream")
)

// Config is what a Streamer is built over: the Tools the caller built, through either runner,
// typically from audio's FFmpegSpec and FFprobeSpec, on the host or in audio.FFmpegImage. Both are
// required.
type Config struct {
	// FFmpeg is FFmpeg's ffmpeg: the one run that reads a source, decodes its audio, and writes
	// its frames.
	FFmpeg *tool.Tool
	// FFprobe is FFmpeg's ffprobe: it finds a file's audio and video streams before ffmpeg runs.
	FFprobe *tool.Tool
}

// Streamer reads a source through one ffmpeg run and streams its windows and frames as they are
// complete. Its methods are safe for concurrent use.
type Streamer struct {
	ffmpeg  *tool.Tool
	ffprobe *tool.Tool
}

// NewStreamer builds a Streamer over cfg's Tools. It refuses a nil FFmpeg or FFprobe, and either
// older than audio.FFmpegMinimum, whatever minimum the Tool's own Spec declared, with a
// *tool.VersionError wrapping tool.ErrTooOld.
func NewStreamer(cfg Config) (*Streamer, error) {
	if cfg.FFmpeg == nil {
		return nil, errors.New("media: no ffmpeg tool")
	}
	if cfg.FFprobe == nil {
		return nil, errors.New("media: no ffprobe tool")
	}
	m, err := tool.ParseVersion(audio.FFmpegMinimum)
	if err != nil {
		return nil, err
	}
	for _, t := range []*tool.Tool{cfg.FFmpeg, cfg.FFprobe} {
		if v := t.Version(); v.Compare(m) < 0 {
			return nil, &tool.VersionError{Program: t.Name(), Found: v.String(), Minimum: audio.FFmpegMinimum, Err: tool.ErrTooOld}
		}
	}
	return &Streamer{ffmpeg: cfg.FFmpeg, ffprobe: cfg.FFprobe}, nil
}

// Source is a request's source: exactly one of File, Reader, and URL. A Source naming none, or
// more than one, is refused.
type Source struct {
	// File is a file, any container and codecs FFmpeg reads, which is read and never changed.
	// ffmpeg reads it by its absolute path, in its own directory, which the container runner
	// mounts; it writes its frames into a hidden directory of its own there, so the file's
	// directory must be writable. Its streams are found by ffprobe before ffmpeg runs.
	File string
	// Paced reads File at real time, as a live source arrives, through ffmpeg's -readrate 1;
	// unpaced, ffmpeg reads it as fast as it decodes. Pacing changes when events arrive, never
	// what they are. It is refused without File: a reader or a URL comes as fast as it comes.
	Paced bool
	// Reader is a stream the caller holds, any container FFmpeg reads from a pipe, fed to ffmpeg's
	// stdin as it is read. The run reads it once, to its EOF or the run's end, and never closes it.
	// A container that needs to seek, such as an MP4 whose index follows its media, can't be read
	// from it.
	Reader io.Reader
	// URL is a live or remote source ffmpeg reads itself: a URL whose scheme is srt, rtmp, http,
	// https, or udp, an HLS playlist's included. Any other scheme is refused, rtsp among them. The
	// run allows ffmpeg only its scheme's protocols, so a playlist can't open another protocol or a
	// local file. Through the container runner, the ffmpeg Tool's image must name the docker
	// network the run joins, its Network; a URL run through a Tool with none is refused. The URL
	// is passed to ffmpeg as given: its options, such as udp's timeout, are the caller's.
	URL string
	// Tracks is which of the source's streams the run takes; zero is TracksAuto.
	Tracks Tracks
}

// Tracks is which of a source's streams a run takes: its first audio stream, its first video
// stream that isn't an attached picture, or both. A file's streams are found by its probe; a
// reader's or a URL's can't be, since probing would read what only the run may read, so ffmpeg
// finds them as it opens the source, and a stream the request takes that the source lacks fails
// the run with ErrNoMedia, before any event.
type Tracks int

const (
	// TracksAuto takes what the source has: a file's audio and video streams, whichever its probe
	// finds, refusing one with neither; a reader's or a URL's, both, each required.
	TracksAuto Tracks = iota
	// TracksBoth takes the audio and the video, each required.
	TracksBoth
	// TracksAudio takes the audio alone, which is required; the video, if any, is ignored.
	TracksAudio
	// TracksVideo takes the video alone, which is required; the audio, if any, is ignored.
	TracksVideo
)

// schemes are the URL schemes a Source may name, each with the protocols, ffmpeg's
// -protocol_whitelist, its run allows: its own and those it is carried over. http and https
// include crypto for an HLS playlist's encrypted segments, which ffmpeg opens as crypto+http or
// crypto+https and whose inner protocol the list still checks; neither includes the other, nor
// file.
var schemes = map[string]string{
	"srt":   "srt",
	"rtmp":  "rtmp,tcp",
	"http":  "http,tcp,crypto",
	"https": "https,tls,tcp,crypto",
	"udp":   "udp",
}

// The protocols a file's run and a reader's allow, so a file that is a playlist, or a reader that
// carries one, opens nothing but itself.
const (
	fileProtocols   = "file"
	readerProtocols = "pipe"
)

// Request is one source's stream of windows and frames.
type Request struct {
	// Source is the one source the run reads; it is required.
	Source Source
	// Dir is the media directory the windows and frames are written into and left, which the
	// caller owns. It is required, and must be a directory. A reader's or a URL's run, which has
	// no directory of its own, works in a hidden directory it makes in Dir, the only directory
	// the container runner mounts for it, and removes at its end.
	Dir string

	// Overlap is how long before a window's end the next window starts, as audio.Request's: zero
	// is audio.DefaultOverlap, 2 s, and it must be under audio.WindowLimit/2.
	Overlap time.Duration
	// MinSilence is the shortest silence a window may end in, as audio.Request's: zero is
	// audio.DefaultMinSilence, 0.3 s; below zero is refused.
	MinSilence time.Duration
	// Threshold, when not zero, is a fixed silence threshold in dB relative to full scale, from
	// -120 to below zero, in place of the tracker's rolling one, as audio.Request's; Percentile,
	// FloorWindow, and Margin must then be zero.
	Threshold float64
	// Percentile, FloorWindow, and Margin are the tracker's rolling threshold, as audio.Request's
	// and audio.NewTracker say: zero is audio.DefaultPercentile, audio.DefaultFloorWindow, and
	// audio.DefaultMargin.
	Percentile  float64
	FloorWindow time.Duration
	Margin      float64

	// SceneThreshold is the scene score past which a frame is taken as a scene change, on the 0
	// to 1 scale of ffmpeg's select filter: zero is DefaultSceneThreshold, 0.2; it must otherwise
	// be above zero and under 1.
	SceneThreshold float64
	// MaxGap is the longest the stream goes without a frame: a frame MaxGap or more after the last
	// one taken is taken. Zero is DefaultMaxGap, 10 s; below zero is refused.
	MaxGap time.Duration
	// MaxSide, when not zero, is the largest side a frame is written at: a frame larger is scaled
	// down to fit, its aspect kept, and a smaller one is written as it is. Zero writes every frame
	// at the source's resolution; below zero is refused.
	MaxSide int
}

// checked is a request validated, its defaults applied.
type checked struct {
	// path is a file source's absolute path; reader a reader source's; url a URL source's.
	path   string
	reader io.Reader
	url    string
	// protocols is the run's -protocol_whitelist.
	protocols string
	tracks    Tracks
	paced     bool
	dir       string
	audio     audio.Request
	fixed     bool
	scene     float64
	gap       time.Duration
	maxSide   int
}

// live reports whether the source arrives as it plays: a paced file, a reader, or a URL.
func (c checked) live() bool { return c.paced || c.path == "" }

// name names the source in an error: the file's path, "the reader", or the URL, its password
// redacted.
func (c checked) name() string {
	switch {
	case c.path != "":
		return c.path
	case c.reader != nil:
		return "the reader"
	}
	if u, err := url.Parse(c.url); err == nil {
		return u.Redacted()
	}
	return c.url
}

// input returns ffmpeg's input: the file's path, its stdin for a reader, or the URL.
func (c checked) input() string {
	switch {
	case c.path != "":
		return c.path
	case c.reader != nil:
		return "pipe:0"
	}
	return c.url
}

// check validates req and applies its defaults; an invalid field is an error wrapping ErrRequest.
// It runs nothing.
func (req Request) check() (checked, error) {
	src := req.Source
	named := 0
	for _, set := range []bool{src.File != "", src.Reader != nil, src.URL != ""} {
		if set {
			named++
		}
	}
	switch {
	case src.Paced && src.File == "":
		return checked{}, fmt.Errorf("media: a paced source with no file: %w", ErrRequest)
	case named == 0:
		return checked{}, fmt.Errorf("media: request names no source: %w", ErrRequest)
	case named > 1:
		return checked{}, fmt.Errorf("media: request names %d sources, of a file, a reader, and a URL: %w", named, ErrRequest)
	case src.Tracks < TracksAuto || src.Tracks > TracksVideo:
		return checked{}, fmt.Errorf("media: tracks %d isn't one of the Tracks: %w", src.Tracks, ErrRequest)
	}
	c := checked{
		reader: src.Reader,
		tracks: src.Tracks,
		paced:  src.Paced,
		dir:    req.Dir,
		audio: audio.Request{
			Overlap: req.Overlap, MinSilence: req.MinSilence, Threshold: req.Threshold,
			Detector: audio.DetectorTracker, Percentile: req.Percentile, FloorWindow: req.FloorWindow, Margin: req.Margin,
		},
		fixed:   req.Threshold != 0,
		scene:   cmp.Or(req.SceneThreshold, DefaultSceneThreshold),
		gap:     cmp.Or(req.MaxGap, DefaultMaxGap),
		maxSide: req.MaxSide,
	}
	// audio validates its own fields, each refusal wrapping ErrRequest, audio's.
	if _, err := audio.NewPlanner(c.audio); err != nil {
		return checked{}, fmt.Errorf("media: %w", err)
	}
	if _, err := audio.NewTracker(c.audio); err != nil {
		return checked{}, fmt.Errorf("media: %w", err)
	}
	switch {
	case math.IsNaN(req.SceneThreshold) || req.SceneThreshold < 0 || req.SceneThreshold >= 1:
		return checked{}, fmt.Errorf("media: scene threshold %v isn't 0 or above it and under 1: %w", req.SceneThreshold, ErrRequest)
	case req.MaxGap < 0:
		return checked{}, fmt.Errorf("media: maximum gap %v is below zero: %w", req.MaxGap, ErrRequest)
	case req.MaxSide < 0:
		return checked{}, fmt.Errorf("media: maximum side %d is below zero: %w", req.MaxSide, ErrRequest)
	case req.Dir == "":
		return checked{}, fmt.Errorf("media: request has no media directory: %w", ErrRequest)
	}
	switch {
	case src.File != "":
		path, err := filepath.Abs(src.File)
		if err != nil {
			return checked{}, fmt.Errorf("media: source file %s: %w: %w", src.File, ErrRequest, err)
		}
		c.path, c.protocols = path, fileProtocols
	case src.Reader != nil:
		c.protocols = readerProtocols
	default:
		protocols, err := checkURL(src.URL)
		if err != nil {
			return checked{}, err
		}
		c.url, c.protocols = src.URL, protocols
	}
	info, err := os.Stat(req.Dir)
	if err != nil {
		return checked{}, fmt.Errorf("media: media directory: %w: %w", ErrRequest, err)
	}
	if !info.IsDir() {
		return checked{}, fmt.Errorf("media: media directory %s isn't a directory: %w", req.Dir, ErrRequest)
	}
	return c, nil
}

// checkURL validates a URL source and returns the protocols its run allows. A URL that doesn't
// parse, that has no host, or whose scheme isn't one of schemes is an error wrapping ErrRequest.
func checkURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("media: source URL: %w: %w", ErrRequest, err)
	}
	// url.Parse lowercases the scheme.
	if u.Scheme == "rtsp" || u.Scheme == "rtsps" {
		return "", fmt.Errorf("media: source URL scheme %s isn't supported: ffmpeg can't serve it to a test, so it is unproven: %w", u.Scheme, ErrRequest)
	}
	protocols, ok := schemes[u.Scheme]
	if !ok {
		return "", fmt.Errorf("media: source URL scheme %q isn't srt, rtmp, http, https, or udp: %w", u.Scheme, ErrRequest)
	}
	if u.Opaque != "" || u.Host == "" {
		return "", fmt.Errorf("media: source URL %s has no host: %w", u.Redacted(), ErrRequest)
	}
	return protocols, nil
}

// streams are the streams a run takes, each as ffmpeg's -map names it, empty when the run doesn't
// take one: a file's by the index its probe found, and a reader's or a URL's by its type.
type streams struct {
	audio, video string
}

// unprobed returns the streams a reader's or a URL's run takes: its first audio stream and its
// first video stream that isn't an attached picture, as tracks says.
func unprobed(tracks Tracks) streams {
	st := streams{audio: "0:a:0", video: "0:V:0"}
	switch tracks {
	case TracksAudio:
		st.video = ""
	case TracksVideo:
		st.audio = ""
	}
	return st
}

// probe finds the audio and video streams of the file at path, absolute, through ffprobe, and
// returns those tracks takes. A video stream that is an attached picture, such as an album's cover
// art, isn't video. A file with neither, or without one that tracks requires, is an error wrapping
// ErrNoMedia; one ffprobe can't read is its *tool.ExitError, wrapped.
func (s *Streamer) probe(ctx context.Context, path string, tracks Tracks) (streams, error) {
	var stdout bytes.Buffer
	err := s.ffprobe.Run(ctx, tool.Invocation{
		Args: []string{
			"-v", "error", "-protocol_whitelist", fileProtocols, "-print_format", "json",
			"-show_entries", "stream=index,codec_type:stream_disposition=attached_pic", path,
		},
		Dir:    filepath.Dir(path),
		Stdout: &stdout,
	})
	if err != nil {
		return streams{}, fmt.Errorf("media: probe %s: %w", path, err)
	}
	var report struct {
		Streams []struct {
			Index       int    `json:"index"`
			CodecType   string `json:"codec_type"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return streams{}, fmt.Errorf("media: probe %s: ffprobe's report: %w", path, err)
	}
	var st streams
	for _, x := range report.Streams {
		switch {
		case x.CodecType == "audio" && st.audio == "":
			st.audio = "0:" + strconv.Itoa(x.Index)
		case x.CodecType == "video" && x.Disposition.AttachedPic == 0 && st.video == "":
			st.video = "0:" + strconv.Itoa(x.Index)
		}
	}
	switch {
	case st.audio == "" && st.video == "":
		return streams{}, fmt.Errorf("media: probe %s: %w", path, ErrNoMedia)
	case st.audio == "" && (tracks == TracksBoth || tracks == TracksAudio):
		return streams{}, fmt.Errorf("media: probe %s: no audio stream: %w", path, ErrNoMedia)
	case st.video == "" && (tracks == TracksBoth || tracks == TracksVideo):
		return streams{}, fmt.Errorf("media: probe %s: no video stream: %w", path, ErrNoMedia)
	}
	switch tracks {
	case TracksAudio:
		st.video = ""
	case TracksVideo:
		st.audio = ""
	}
	return st, nil
}

// Event is one event of a source's stream: a window of its audio or a frame of its video. Exactly
// one of Window and Frame is set; the zero Event, which a stream yields with an error that ends it,
// has neither.
type Event struct {
	// Window is the window, for a window event.
	Window *Window
	// Frame is the frame, for a frame event.
	Frame *Frame
	// Time is the event's source time, an offset from the source's start: a window's start, its
	// Span's Start, or a frame's presentation time.
	Time time.Duration
	// Arrived is the wall-clock time the event's last sample or frame arrived from ffmpeg: for a
	// window, when the stream's bytes holding its last sample were read from ffmpeg's stdout; for
	// a frame, when ffmpeg's report of it, after its selection, was read from its stderr.
	Arrived time.Time
}

// Window is one window of a source's audio, written to the media directory as WAV: 16 kHz,
// audio.SampleRate, mono 16-bit PCM, its header the canonical 44 bytes, through audio.WAV, so the
// same window is the same bytes as audio's file path cuts.
type Window struct {
	// Index is the window's index in the stream's plan, from 0.
	Index int
	// Span is the window's span of the source, overlaps included.
	Span audio.Span
	// Cut is what ended the window: audio.CutSilence, audio.CutLimit, or audio.CutEnd, the last.
	Cut audio.Cut
	// SeamBefore is the seam the window starts in, shared with the window before it; zero for the
	// first. SeamAfter is the seam it ends in, shared with the window after it; zero for the last.
	SeamBefore audio.Span
	SeamAfter  audio.Span
	// Samples is the number of samples the WAV holds, at audio.SampleRate.
	Samples int
	// Threshold is the silence threshold, in dB relative to full scale, the window was cut under:
	// the request's fixed Threshold; or, with the tracker's rolling threshold, for a window cut at
	// a silence, the one in force when that silence opened, and for one cut at the limit or the
	// end, the one its last sample's frame was decided against, under which the tracker found no
	// silence to end it in.
	Threshold float64
	// Path is the WAV's path in the media directory, named by SHA256 and ".wav".
	Path string
	// SHA256 is the lowercase hex SHA-256 of the WAV's bytes, header included.
	SHA256 string
}

// Frame is one frame of a source's video, written to the media directory as PNG.
type Frame struct {
	// Index is the frame's index among the frames the stream takes, from 0.
	Index int
	// Reason is why the frame was taken.
	Reason Reason
	// Width and Height are the PNG's, the source's or, scaled to the request's MaxSide, smaller.
	Width  int
	Height int
	// Path is the PNG's path in the media directory, named by SHA256 and ".png".
	Path string
	// SHA256 is the lowercase hex SHA-256 of the PNG's bytes.
	SHA256 string
}

// Reason is why a frame was taken.
type Reason int

const (
	// ReasonFirst is the source's first frame.
	ReasonFirst Reason = iota + 1
	// ReasonScene is a scene change: a frame whose scene score is past the request's
	// SceneThreshold.
	ReasonScene
	// ReasonInterval is a frame taken because the request's MaxGap passed with none.
	ReasonInterval
)

// String returns the reason's name: first, scene, or interval.
func (r Reason) String() string {
	switch r {
	case ReasonFirst:
		return "first"
	case ReasonScene:
		return "scene"
	case ReasonInterval:
		return "interval"
	}
	return fmt.Sprintf("Reason(%d)", int(r))
}
