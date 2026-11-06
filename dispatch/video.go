//go:build linux

package dispatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
)

// VideoConfig is what a [VideoStage] is built over: the caller's Streamer, the version of the
// tools behind it, and the request each part's windows and frames are taken under.
type VideoConfig struct {
	// Streamer reads each part through one ffmpeg run; it is required. A caller with no ffmpeg
	// and ffprobe has no Streamer, and registers no video stage: its video parts are no processor
	// yet.
	Streamer *media.Streamer
	// Version names the tools behind the Streamer, ffmpeg's and ffprobe's, as [Versions] gives
	// them; it is required.
	Version string
	// Request is the request every part's run is made under: its windows' Overlap, MinSilence,
	// Threshold, Percentile, FloorWindow, and Margin, and its frames' SceneThreshold, MaxGap, and
	// MaxSide, each defaulted as media.Request says. Its Source and Dir are ignored: each run reads
	// the part's own file, unpaced, taking whichever of its audio and video streams it has, into
	// the part's directory. Every field it reads is part of the stage's identity, its defaults
	// applied, so a zero field and its default are the same identity. A windowing field audio's
	// Planner or Tracker refuses is refused when the stage is built; a frame field media refuses
	// fails each part.
	Request media.Request
}

// VideoStage prepares a video for a model's vision and audio inputs, route.Video's parts, an MP4,
// Matroska, WebM, or AVI, through media's Streamer, one unpaced ffmpeg run per part. It runs no
// model: it prepares the frames a layer above sends to a vision model, and the windows it
// transcribes and joins through audio.Join.
//
// It yields the plan first, a [RecordOutput] named "plan", the JSON of a [VideoPlan]: the settings
// the run's windows are planned and its frames sampled under, each default applied. media plans the
// windows as the audio arrives, so the plan record holds no windows: each window carries its own
// span and seams. The plan is yielded once the run has a stream to read, just before its first
// frame or window, so a source media refuses has no outputs. It then yields each frame and each
// window as the run completes it, frames in source-time order and windows in plan order,
// interleaved as media yields them:
//
//   - A frame is an [ImageOutput] named "frame N", N its index among the frames taken, from 0, a
//     PNG, "image/png", named by its SHA-256, as a document's page image is. Its Meta is its index,
//     "frame"; its source time, "time"; why it was taken, "reason", first, scene, or interval; and
//     its size, "width" and "height".
//   - A window is an [AudioOutput] named "window N", N its index in the plan, a 16 kHz mono 16-bit
//     PCM WAV, "audio/wav", named by its SHA-256, as the [AudioStage]'s windows are. Its Meta is
//     the audio stage's but for "count", which no window knows before the run ends: its index,
//     "window"; its span, "start" and "end"; what ended it, "cut"; its samples, "samples"; its
//     seams, "seam_before" for every window but the first and "seam_after" for every window but
//     the last; and the silence threshold it was cut under, in dB, "threshold".
//
// Every time is in seconds, as strconv's shortest decimal of them. A video with no audio stream
// yields frames only, and a part routed video with no video stream, such as an MP4 holding only
// audio, yields windows only, each after the plan; one with neither fails, its error wrapping
// media.ErrNoMedia, before ffmpeg runs.
//
// media writes a file's frames into a hidden directory beside it, so the stage never runs it over
// the part's file, which is the caller's own for the input: it runs it over a hard link to the file
// in the part's directory, or a copy where the file system refuses one, and the windows, frames,
// and media's directory of frames are all made there, inside the dispatcher's working directory,
// and removed with the part's directory. A run that fails ends the stage with its error, after the
// frames and windows it completed; a context that ends stops ffmpeg.
type VideoStage struct {
	streamer *media.Streamer
	request  media.Request
	plan     VideoPlan
	id       Identity
}

// VideoPlan is the JSON of a video part's "plan" record: the settings its windows are planned and
// its frames sampled under, each default applied, every time in seconds.
type VideoPlan struct {
	Windows VideoWindowing `json:"windows"`
	Frames  VideoSampling  `json:"frames"`
}

// VideoWindowing is how a video's audio is cut into windows, as media's Request and audio's
// tracker detector say: each at most Limit long, overlap included, cut in the latest silence of
// at least MinSilence before its limit, a silence being audio below the threshold.
type VideoWindowing struct {
	// Detector is the silence detector, always "tracker": media finds silences as the audio
	// arrives, through audio.Tracker.
	Detector string `json:"detector"`
	// Limit is a window's longest, audio.WindowLimit, and Overlap each seam's length.
	Limit   float64 `json:"limit"`
	Overlap float64 `json:"overlap"`
	// MinSilence is the shortest silence a window may end in.
	MinSilence float64 `json:"min_silence"`
	// Threshold is a fixed silence threshold, in dB, or zero for the tracker's rolling one, which
	// Percentile, FloorWindow, and Margin set, each zero with a fixed threshold.
	Threshold   float64 `json:"threshold"`
	Percentile  float64 `json:"percentile"`
	FloorWindow float64 `json:"floor_window"`
	Margin      float64 `json:"margin"`
}

// VideoSampling is how a video's frames are taken, as media's Request says: the first, each whose
// scene score is past SceneThreshold, and each MaxGap or more after the last taken, each scaled to
// fit MaxSide, or at the source's size when it is zero.
type VideoSampling struct {
	SceneThreshold float64 `json:"scene_threshold"`
	MaxGap         float64 `json:"max_gap"`
	MaxSide        int     `json:"max_side"`
}

// NewVideoStage builds a VideoStage over cfg, refusing one with no Streamer or Version, or with a
// windowing field audio's Planner or Tracker refuses.
func NewVideoStage(cfg VideoConfig) (*VideoStage, error) {
	if cfg.Streamer == nil {
		return nil, errors.New("dispatch: video stage: no streamer")
	}
	req := cfg.Request
	// media validates its windowing fields through audio's Planner and Tracker, under the tracker
	// detector; the stage does the same, ahead of any part.
	areq := audio.Request{
		Overlap: req.Overlap, MinSilence: req.MinSilence, Threshold: req.Threshold, Detector: audio.DetectorTracker,
		Percentile: req.Percentile, FloorWindow: req.FloorWindow, Margin: req.Margin,
	}
	planner, err := audio.NewPlanner(areq)
	if err != nil {
		return nil, fmt.Errorf("dispatch: video stage: %w", err)
	}
	if _, err := audio.NewTracker(areq); err != nil {
		return nil, fmt.Errorf("dispatch: video stage: %w", err)
	}
	// A fixed threshold takes the place of the rolling one, whose fields are then zero.
	percentile, floorWindow, margin := 0.0, time.Duration(0), 0.0
	if req.Threshold == 0 {
		percentile = cmp.Or(req.Percentile, audio.DefaultPercentile)
		floorWindow = cmp.Or(req.FloorWindow, audio.DefaultFloorWindow)
		margin = cmp.Or(req.Margin, audio.DefaultMargin)
	}
	plan := VideoPlan{
		Windows: VideoWindowing{
			Detector: "tracker", Limit: audio.WindowLimit.Seconds(), Overlap: planner.Overlap().Seconds(),
			MinSilence: cmp.Or(req.MinSilence, audio.DefaultMinSilence).Seconds(), Threshold: req.Threshold,
			Percentile: percentile, FloorWindow: floorWindow.Seconds(), Margin: margin,
		},
		Frames: VideoSampling{
			SceneThreshold: cmp.Or(req.SceneThreshold, media.DefaultSceneThreshold),
			MaxGap:         cmp.Or(req.MaxGap, media.DefaultMaxGap).Seconds(),
			MaxSide:        req.MaxSide,
		},
	}
	// The identity hashes the plan record's settings, every one of which changes what the stage
	// prepares; the run's source and directory are each part's own.
	id, err := adapterIdentity("video", cfg.Version, plan)
	if err != nil {
		return nil, err
	}
	req.Source, req.Dir = media.Source{}, ""
	return &VideoStage{streamer: cfg.Streamer, request: req, plan: plan, id: id}, nil
}

// Identity returns the stage's identity: "video", its tools' version, and its settings' hash.
func (s *VideoStage) Identity() Identity { return s.id }

// Prepare runs media over a link to the file at p.Path in dir, yielding its plan, frames, and
// windows, as the type's doc says.
func (s *VideoStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		// The link is named as no window or frame is, each being named by its SHA-256.
		src := filepath.Join(dir, "source")
		if err := linkOrCopy(p.Path, src); err != nil {
			yield(Item{}, fmt.Errorf("video stage: %w", err))
			return
		}
		defer func() { _ = os.Remove(src) }()
		// The plan comes before the first frame or window, once the probe has found a stream, so a
		// source media refuses fails with no outputs, as an audio part with no audio stream does.
		planned := false
		plan := func() bool {
			planned = true
			o, err := record("plan", s.plan, nil)
			if err != nil {
				yield(Item{}, fmt.Errorf("video stage: %w", err))
				return false
			}
			return yield(OutputItem(o), nil)
		}
		req := s.request
		req.Source, req.Dir = media.Source{File: src}, dir
		for ev, err := range s.streamer.Events(ctx, req) {
			if err != nil {
				yield(Item{}, fmt.Errorf("video stage: %w", err))
				return
			}
			var o Output
			switch {
			case ev.Frame != nil:
				o = frameOutput(ev)
			case ev.Window != nil:
				o = videoWindowOutput(ev.Window)
			default:
				continue
			}
			if !planned && !plan() {
				return
			}
			if !yield(OutputItem(o), nil) {
				return
			}
		}
		if !planned {
			plan()
		}
	}
}

// linkOrCopy makes the file at dst the file at src: a hard link, else a copy where the file
// system refuses one.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

// frameOutput returns a frame event's image output, as VideoStage's doc says.
func frameOutput(ev media.Event) Output {
	f := ev.Frame
	return Output{
		Kind: ImageOutput, Name: "frame " + strconv.Itoa(f.Index), MediaType: "image/png", Path: f.Path,
		Meta: map[string]string{
			"frame":  strconv.Itoa(f.Index),
			"time":   secs(ev.Time),
			"reason": f.Reason.String(),
			"width":  strconv.Itoa(f.Width),
			"height": strconv.Itoa(f.Height),
		},
	}
}

// videoWindowOutput returns a window event's audio output, as VideoStage's doc says.
func videoWindowOutput(w *media.Window) Output {
	meta := map[string]string{
		"window":    strconv.Itoa(w.Index),
		"start":     secs(w.Span.Start),
		"end":       secs(w.Span.End),
		"cut":       w.Cut.String(),
		"samples":   strconv.Itoa(w.Samples),
		"threshold": strconv.FormatFloat(w.Threshold, 'f', -1, 64),
	}
	if w.Index > 0 {
		meta["seam_before"] = spanSecs(w.SeamBefore)
	}
	if w.Cut != audio.CutEnd {
		meta["seam_after"] = spanSecs(w.SeamAfter)
	}
	return Output{
		Kind: AudioOutput, Name: "window " + strconv.Itoa(w.Index), MediaType: "audio/wav", Path: w.Path, Meta: meta,
	}
}
