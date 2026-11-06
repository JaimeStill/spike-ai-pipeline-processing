//go:build findings && linux

package media_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/audiotest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/corpustest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
)

// The findings tier measures evidence 13 on real videos, never in CI or the check: each source of
// streamSources, in the test corpus at ~/docs and checked there against its manifest, is read at
// real time through media's paced stream with the request's defaults, its windows sent to
// audiotest.Model and its frames to liveVisionModel through the live harness as each event is
// yielded, and each event's lags recorded: from its last sample's or frame's arrival to the event,
// and from the event to its model's answer. Each run's joined transcript is scored against the
// source's truth, and beside it the same audio through audio's file path, under either detector.
// A further paced run is cancelled midway, and what it took to stop, and what it left, recorded.
// Before any of it, the source is read once unpaced, with no model, for the stream's own speed.
// `mise run findings-streams` runs it; what it found is in docs/findings/streams.md. Its
// settings, from the environment:
//
//   - STREAMS_FINDINGS_SOURCE names the one source TestFindingsStreams runs; unset skips it. The
//     task runs each source as a go test run of its own.
//   - DOCS names the corpus root, ~/docs when unset.
//   - STREAMS_FINDINGS_RUNS, when set, is the paced runs of the source, in place of its own count.
//   - STREAMS_FINDINGS_RESULTS names a directory TestFindingsStreams writes its source's results
//     to as JSON, after each step, and TestFindingsStreamsReport reads them from; unset writes
//     nothing.
const (
	streamsSourceEnv  = "STREAMS_FINDINGS_SOURCE"
	streamsRunsEnv    = "STREAMS_FINDINGS_RUNS"
	streamsResultsEnv = "STREAMS_FINDINGS_RESULTS"
)

// streamSource is one source of the streams finding: its video, the audio muxed with it when the
// corpus keeps them apart, its truth, and how many paced runs it gets.
type streamSource struct {
	// Name names the source in STREAMS_FINDINGS_SOURCE and its results file.
	Name string
	// Title describes it.
	Title string
	// Video is the video's path in the corpus: the source itself, or its picture when Audio is set.
	Video string
	// Audio, when set, is the path in the corpus of the audio muxed with Video's first video
	// stream, by stream copy, into a Matroska file the run reads.
	Audio string
	// Truth are the truth files' paths in the corpus, each checked against the manifest.
	Truth []string
	// Runs is the paced runs, unless STREAMS_FINDINGS_RUNS sets another count.
	Runs int
	// TruthNote is the truth's caveat, logged and kept in the results.
	TruthNote string
	// TruthEnd, when not zero, is where the truth ends in the source: only the windows that end by
	// it are scored, against the truth's cues whose middle is before the last of them ends.
	TruthEnd time.Duration
	// read reads the truth from the corpus root: the cues or words whose middle is before within,
	// or all of them when within is zero.
	read func(docs string, within time.Duration) (string, error)
}

// streamSources are the finding's sources, in the order the task runs them.
var streamSources = []streamSource{
	{
		Name:  "tears",
		Title: "Tears of Steel, the 180 s clip at 1150x480, H.264 24 fps, AAC 44.1 kHz stereo",
		Video: "video/tears-of-steel/tos-clip-180s-480p.mp4",
		Truth: []string{"video/tears-of-steel/tos-en.srt"},
		Runs:  3,
		TruthNote: "approximate: the film's English subtitles, the cues whose middle is before 180 s; " +
			"subtitles condense speech and carry a title card (\"Fourty years later\"), and music and effects are in no truth",
		read: func(docs string, within time.Duration) (string, error) {
			text, _, err := corpustest.ReadSRT(filepath.Join(docs, "video/tears-of-steel/tos-en.srt"), cmp.Or(within, 180*time.Second))
			return text, err
		},
	},
	{
		Name:  "ami",
		Title: "AMI ES2002a, the overhead camera (352x288, MPEG-4 Part 2, 25 fps) muxed with the Mix-Headset WAV (16 kHz mono)",
		Video: "video/ami-es2002a/ami-es2002a-overhead.avi",
		Audio: "audio/long/ami-es2002a-mix-headset.wav",
		Truth: corpustest.AMITruth,
		Runs:  1,
		TruthNote: "AMI's manual word-level transcription of each speaker, ordered by its segments' starts, " +
			"as the audio finding scores it; overlapping speech is ordered by segment, which no single-stream transcript can match exactly",
		read: func(docs string, within time.Duration) (string, error) {
			text, excerpt, err := corpustest.ReadAMI(docs, within)
			if within > 0 {
				return excerpt, err
			}
			return text, err
		},
	},
	{
		Name:     "nasa",
		Title:    "NASA, ISS Expedition 60 in-flight event with Pittsburgh stations, 640x360 H.264 30 fps, AAC 22.05 kHz stereo",
		Video:    "video/nasa-iss-inflight-pittsburgh/nasa-iss-inflight-pittsburgh-640x360.mp4",
		Truth:    []string{"video/nasa-iss-inflight-pittsburgh/nasa-iss-inflight-pittsburgh.srt"},
		Runs:     1,
		TruthEnd: 18*time.Minute + 36*time.Second,
		TruthNote: "approximate: NASA's live captions, upper case, \">>\" at each turn, which end at 18:36 of 25:31; " +
			"only the windows that end by then are scored",
		read: func(docs string, within time.Duration) (string, error) {
			text, _, err := corpustest.ReadSRT(filepath.Join(docs, "video/nasa-iss-inflight-pittsburgh/nasa-iss-inflight-pittsburgh.srt"), within)
			return strings.ReplaceAll(text, ">>", " "), err
		},
	},
}

// cancelInFlight is how long after the cancel's trigger request is sent that the cancel comes, so
// the request is on the wire: well inside a window's transcription, about 2 s, and a frame's
// description, about 1 s, as TestHarnessCancel waits.
const cancelInFlight = 250 * time.Millisecond

// score is a word error count: edits over the truth's normalized words.
type score struct {
	Edits  int     `json:"edits"`
	Length int     `json:"length"`
	Rate   float64 `json:"rate"`
}

// wer scores got against truth by NormalizedWordErrors.
func wer(truth, got string) score {
	e, n := textscore.NormalizedWordErrors(truth, got)
	return score{Edits: e, Length: n, Rate: textscore.Rate(e, n)}
}

// dist is a distribution of one lag over a run's events of one kind, by nearest rank.
type dist struct {
	N    int           `json:"n"`
	Mean time.Duration `json:"mean"`
	P50  time.Duration `json:"p50"`
	P90  time.Duration `json:"p90"`
	Max  time.Duration `json:"max"`
}

// distOf returns ds' distribution.
func distOf(ds []time.Duration) dist {
	if len(ds) == 0 {
		return dist{}
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	rank := func(p int) time.Duration { return s[max(0, (p*len(s)+99)/100-1)] }
	return dist{N: len(s), Mean: sum / time.Duration(len(s)), P50: rank(50), P90: rank(90), Max: s[len(s)-1]}
}

// lags are a kind's lag distributions over a run, as lagLine names them.
type lags struct {
	ArrivalToEvent dist `json:"arrival_to_event"`
	EventToAnswer  dist `json:"event_to_answer"`
	Queued         dist `json:"queued"`
	Request        dist `json:"request"`
	// Waited counts the events yielded while every slot of their kind was busy, which waited for
	// one, and MaxInFlight is the most requests in flight at once.
	Waited      int `json:"waited"`
	MaxInFlight int `json:"max_in_flight"`
}

// lagsOf returns the lags of answers, all of one kind.
func lagsOf(answers []*answer) lags {
	var arrival, toAnswer, queued, request []time.Duration
	for _, a := range answers {
		arrival = append(arrival, a.yielded.Sub(a.arrived))
		toAnswer = append(toAnswer, a.answered.Sub(a.yielded))
		queued = append(queued, a.sent.Sub(a.yielded))
		request = append(request, a.answered.Sub(a.sent))
	}
	l := lags{ArrivalToEvent: distOf(arrival), EventToAnswer: distOf(toAnswer), Queued: distOf(queued), Request: distOf(request)}
	// At each event's yield, count the others' requests in flight: all liveSlots busy means it
	// waited. At each send, count the requests in flight, its own included.
	for _, a := range answers {
		busy, inFlight := 0, 0
		for _, b := range answers {
			if b != a && !b.sent.After(a.yielded) && b.answered.After(a.yielded) {
				busy++
			}
			if !b.sent.After(a.sent) && b.answered.After(a.sent) {
				inFlight++
			}
		}
		if busy >= liveSlots {
			l.Waited++
		}
		l.MaxInFlight = max(l.MaxInFlight, inFlight)
	}
	return l
}

// lagRecord is one event's lag line, as fields.
type lagRecord struct {
	Kind           string `json:"kind"`
	Index          int    `json:"index"`
	SourceMS       int64  `json:"source_ms"`
	ArrivalToEvent int64  `json:"arrival_to_event_ms"`
	EventToAnswer  int64  `json:"event_to_answer_ms"`
	Queued         int64  `json:"queued_ms"`
	Request        int64  `json:"request_ms"`
	OK             bool   `json:"ok"`
}

// recordOf returns a's lag record, lagLine's fields.
func recordOf(a *answer) lagRecord {
	return lagRecord{
		Kind: a.kind, Index: a.index, SourceMS: a.source.Milliseconds(),
		ArrivalToEvent: a.yielded.Sub(a.arrived).Milliseconds(), EventToAnswer: a.answered.Sub(a.yielded).Milliseconds(),
		Queued: a.sent.Sub(a.yielded).Milliseconds(), Request: a.answered.Sub(a.sent).Milliseconds(), OK: a.err == nil,
	}
}

// unpacedRun is the source read once unpaced, with no model: the stream's own speed.
type unpacedRun struct {
	Wall    time.Duration  `json:"wall"`
	Speed   float64        `json:"speed"`
	Windows int            `json:"windows"`
	Frames  int            `json:"frames"`
	Reasons map[string]int `json:"reasons"`
	Cuts    map[string]int `json:"cuts"`
	// windows and frames are each event's file name, its SHA-256, in order, for the paced runs to
	// be checked against.
	windows, frames []string
}

// filePathRun is the source's audio through audio's file path, audiotest.Run's harness: planned
// whole by one detector, its windows cut and transcribed liveSlots at once, and joined.
type filePathRun struct {
	Detector string        `json:"detector"`
	Windows  int           `json:"windows"`
	Failed   int           `json:"failed"`
	PlanTime time.Duration `json:"plan_time"`
	Wall     time.Duration `json:"wall"`
	WER      score         `json:"wer"`
	// Scored is the windows scored, those that end by the truth's end.
	Scored     int    `json:"scored"`
	Transcript string `json:"transcript,omitempty"`
}

// frameAnswer is one frame of a paced run and its description.
type frameAnswer struct {
	Index       int           `json:"index"`
	Time        time.Duration `json:"time"`
	Reason      string        `json:"reason"`
	Description string        `json:"description"`
}

// pacedRun is one paced run through the live harness.
type pacedRun struct {
	Run int `json:"run"`
	// Wall is from the run's start to its last answer; Ended to the stream's end.
	Wall  time.Duration `json:"wall"`
	Ended time.Duration `json:"ended"`
	// LastWindow is from the last window's last sample's arrival to its transcript, the wait for
	// the whole transcript once the source has played.
	LastWindow time.Duration  `json:"last_window"`
	Windows    int            `json:"windows"`
	Frames     int            `json:"frames"`
	Reasons    map[string]int `json:"reasons"`
	Cuts       map[string]int `json:"cuts"`
	Failed     int            `json:"failed"`
	StreamErrs []string       `json:"stream_errors,omitempty"`
	// SameAsUnpaced is whether the run's windows and frames are the unpaced run's, by SHA-256.
	SameAsUnpaced bool `json:"same_as_unpaced"`
	// WindowLags and FrameLags are the lags' distributions; Lags each event's.
	WindowLags lags        `json:"window_lags"`
	FrameLags  lags        `json:"frame_lags"`
	Lags       []lagRecord `json:"lags"`
	// Steps is the settled transcript's steps, and SettledGrew whether each step's settled words
	// led every later step's join.
	Steps       int  `json:"steps"`
	SettledGrew bool `json:"settled_grew"`
	// WER scores the joined transcript of the scored windows; Seams and Unmatched count its seams.
	WER          score         `json:"wer"`
	Scored       int           `json:"scored"`
	Seams        int           `json:"seams"`
	Unmatched    int           `json:"unmatched"`
	Transcript   string        `json:"transcript,omitempty"`
	Descriptions []frameAnswer `json:"descriptions,omitempty"`
}

// cancelRun is a paced run cancelled midway: when, and what it took to stop and left behind.
type cancelRun struct {
	// Trigger is the event the cancel followed, cancelInFlight after its request was sent, the
	// first yielded at or after half the source's length into the run; At is the cancel's time
	// from the run's start, and SourceAt the trigger's source time.
	Trigger  string        `json:"trigger"`
	At       time.Duration `json:"at"`
	SourceAt time.Duration `json:"source_at"`
	Events   int           `json:"events"`
	// StreamStopped is from the cancel to the stream's end, and Returned to the last request's
	// return; StreamErrs are the errors the stream yielded.
	StreamStopped time.Duration `json:"stream_stopped"`
	Returned      time.Duration `json:"returned"`
	StreamErrs    []string      `json:"stream_errors"`
	// AnsweredBefore counts the requests answered before the cancel. InFlight counts those sent
	// before it and not answered, InFlightCancelled those of them that returned the context's
	// error, and InFlightReturn the longest any took to return after the cancel. Queued counts the
	// events waiting for a slot at the cancel, QueuedCancelled those that returned the context's
	// error unsent.
	AnsweredBefore    int           `json:"answered_before"`
	InFlight          int           `json:"in_flight"`
	InFlightCancelled int           `json:"in_flight_cancelled"`
	InFlightReturn    time.Duration `json:"in_flight_return"`
	Queued            int           `json:"queued"`
	QueuedCancelled   int           `json:"queued_cancelled"`
	// Files is the media directory's files after the run, Yielded the events' distinct files;
	// Stray the files in it no event named. Scratch are the entries left beside the source, and
	// Processes the ffmpeg processes still reading it.
	Files     int      `json:"files"`
	Yielded   int      `json:"yielded"`
	Stray     []string `json:"stray,omitempty"`
	Scratch   []string `json:"scratch,omitempty"`
	Processes int      `json:"processes"`
}

// streamResult is one source's results.
type streamResult struct {
	Source struct {
		Name            string   `json:"name"`
		Title           string   `json:"title"`
		Files           []string `json:"files"`
		SHA256          []string `json:"sha256"`
		License         string   `json:"license"`
		Redistributable bool     `json:"redistributable"`
		Seconds         float64  `json:"seconds"`
		TruthNote       string   `json:"truth_note"`
		TruthWords      int      `json:"truth_words"`
	} `json:"source"`
	Settings struct {
		AudioModel     string        `json:"audio_model"`
		VisionModel    string        `json:"vision_model"`
		Prompt         string        `json:"prompt"`
		OutputCap      int           `json:"output_cap"`
		Slots          int           `json:"slots"`
		Overlap        time.Duration `json:"overlap"`
		MinSilence     time.Duration `json:"min_silence"`
		Percentile     float64       `json:"percentile"`
		FloorWindow    time.Duration `json:"floor_window"`
		Margin         float64       `json:"margin"`
		SceneThreshold float64       `json:"scene_threshold"`
		MaxGap         time.Duration `json:"max_gap"`
		MaxSide        int           `json:"max_side"`
	} `json:"settings"`
	Started   time.Time      `json:"started"`
	Unpaced   *unpacedRun    `json:"unpaced"`
	FilePaths []*filePathRun `json:"file_paths"`
	Runs      []*pacedRun    `json:"runs"`
	Cancel    *cancelRun     `json:"cancel"`
}

// TestFindingsStreams runs the source STREAMS_FINDINGS_SOURCE names: one unpaced read with no
// model; the source's audio through audio's file path under silencedetect and the tracker; its
// paced runs through the live harness; and a paced run cancelled midway. It writes the source's
// results after each step, logs a recorded line for each, and fails on a window or frame that
// failed, a stream that ended on an error, a settled prefix that changed, or a cancellation that
// left anything behind.
func TestFindingsStreams(t *testing.T) {
	name := os.Getenv(streamsSourceEnv)
	if name == "" {
		t.Skipf("%s is unset: name one of the finding's sources", streamsSourceEnv)
	}
	i := slices.IndexFunc(streamSources, func(s streamSource) bool { return s.Name == name })
	if i < 0 {
		t.Fatalf("%s=%s names no source", streamsSourceEnv, name)
	}
	src := streamSources[i]
	runs := src.Runs
	if v := os.Getenv(streamsRunsEnv); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("%s=%q isn't a positive count", streamsRunsEnv, v)
		}
		runs = n
	}
	docs := corpustest.Root(t)
	files := []string{src.Video}
	if src.Audio != "" {
		files = append(files, src.Audio)
	}
	entries, err := corpustest.Check(docs, slices.Concat(files, src.Truth))
	if err != nil {
		t.Fatal(err)
	}
	h := newLiveHarness(t)
	h.brief = true
	path := prepare(t, docs, src)
	seconds := probeSeconds(t, path)

	var res streamResult
	res.Source.Name, res.Source.Title, res.Source.Files = src.Name, src.Title, files
	for _, e := range entries[:len(files)] {
		res.Source.SHA256 = append(res.Source.SHA256, e.SHA256)
	}
	res.Source.License, res.Source.Redistributable = entries[0].License, entries[0].Redistributable
	res.Source.Seconds, res.Source.TruthNote = seconds.Seconds(), src.TruthNote
	set := &res.Settings
	set.AudioModel, set.VisionModel, set.Prompt, set.OutputCap, set.Slots = audiotest.Model, liveVisionModel, livePrompt, liveOutputCap, liveSlots
	set.Overlap, set.MinSilence = audio.DefaultOverlap, audio.DefaultMinSilence
	set.Percentile, set.FloorWindow, set.Margin = audio.DefaultPercentile, audio.DefaultFloorWindow, audio.DefaultMargin
	set.SceneThreshold, set.MaxGap = media.DefaultSceneThreshold, media.DefaultMaxGap
	res.Started = time.Now().UTC()
	t.Logf("recorded: %s: %s, %v, %.1f s; truth: %s", src.Name, src.Title, files, seconds.Seconds(), src.TruthNote)

	res.Unpaced = unpaced(t, h.streamer, path, seconds)
	writeStreamResult(t, &res)

	for _, d := range []audio.Detector{audio.DetectorSilencedetect, audio.DetectorTracker} {
		fp := filePath(t, h, docs, src, path, d)
		if !res.Source.Redistributable {
			fp.Transcript = ""
		}
		res.FilePaths = append(res.FilePaths, fp)
		writeStreamResult(t, &res)
	}

	for run := 1; run <= runs; run++ {
		p := paced(t, h, docs, src, path, run, res.Unpaced)
		res.Source.TruthWords = p.WER.Length
		if !res.Source.Redistributable {
			p.Transcript, p.Descriptions = "", nil
		}
		res.Runs = append(res.Runs, p)
		writeStreamResult(t, &res)
	}

	res.Cancel = cancelled(t, h, path, seconds)
	writeStreamResult(t, &res)
}

// prepare returns the path of a copy of src's video in a directory of the test's own, since a
// file's run writes its frames beside it: the video itself, or, when src names an audio, its first
// video stream and the audio's first audio stream muxed by stream copy into Matroska.
func prepare(t *testing.T, docs string, src streamSource) string {
	t.Helper()
	dir := t.TempDir()
	if src.Audio == "" {
		path := filepath.Join(dir, filepath.Base(src.Video))
		if err := copyFile(filepath.Join(docs, src.Video), path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, src.Name+".mkv")
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", filepath.Join(docs, src.Video), "-i", filepath.Join(docs, src.Audio),
		"-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-y", path)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg mux: %v: %s", err, b)
	}
	return path
}

// copyFile copies the file at from to to.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// probeSeconds returns the length of the file at path, its container's, through the host's
// ffprobe.
func probeSeconds(t *testing.T, path string) time.Duration {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	s, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("ffprobe %s: duration %q: %v", path, out, err)
	}
	return time.Duration(s * float64(time.Second))
}

// unpaced reads the source once unpaced, with the request's defaults and no model, and records
// its events and how fast the stream read them.
func unpaced(t *testing.T, s *media.Streamer, path string, seconds time.Duration) *unpacedRun {
	t.Helper()
	u := &unpacedRun{Reasons: map[string]int{}, Cuts: map[string]int{}}
	start := time.Now()
	for e, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: path}, Dir: t.TempDir()}) {
		if err != nil {
			t.Fatalf("unpaced: %v", err)
		}
		if e.Window != nil {
			u.Windows++
			u.Cuts[e.Window.Cut.String()]++
			u.windows = append(u.windows, filepath.Base(e.Window.Path))
		} else {
			u.Frames++
			u.Reasons[e.Frame.Reason.String()]++
			u.frames = append(u.frames, filepath.Base(e.Frame.Path))
		}
	}
	u.Wall = time.Since(start)
	u.Speed = seconds.Seconds() / u.Wall.Seconds()
	t.Logf("recorded: unpaced: %d windows %v, %d frames %v in %v, %.0f times real time",
		u.Windows, u.Cuts, u.Frames, u.Reasons, u.Wall.Round(time.Millisecond), u.Speed)
	return u
}

// filePath transcribes the source's audio through audio's file path under detector: audiotest.Run
// over the prepared file, which holds the corpus' audio stream as it is, at liveSlots, the
// windows each paced run sends at once. It is scored as the paced runs are.
func filePath(t *testing.T, h liveHarness, docs string, src streamSource, path string, detector audio.Detector) *filePathRun {
	t.Helper()
	cfg := runners["exec"](t)
	sp, err := audio.NewSplitter(audio.Config{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe})
	if err != nil {
		t.Fatal(err)
	}
	r, err := audiotest.Run(t.Context(), audiotest.Request{
		Splitter:    sp,
		Source:      audio.Request{Path: path, Detector: detector},
		Dir:         t.TempDir(),
		Transcriber: h.tr,
		Concurrency: liveSlots,
	})
	if err != nil {
		t.Fatalf("file path, %v: %v", detector, err)
	}
	fp := &filePathRun{Detector: detector.String(), Windows: len(r.Windows), Failed: len(r.Failed()), PlanTime: r.PlanTime, Wall: r.Wall}
	spans := make([]audio.Span, len(r.Windows))
	for i, w := range r.Windows {
		spans[i] = w.File.Window.Span
	}
	truth, text, scored := scored(t, docs, src, spans, r.Texts())
	fp.WER, fp.Scored = wer(truth, text), scored
	if fp.Failed > 0 {
		t.Errorf("file path, %v: %d windows failed", detector, fp.Failed)
	}
	fp.Transcript = text
	t.Logf("recorded: file path, %s: %d windows, %d failed; plan %v, wall %v; WER %.4f (%d of %d) over %d windows",
		fp.Detector, fp.Windows, fp.Failed, fp.PlanTime.Round(time.Millisecond), fp.Wall.Round(time.Millisecond),
		fp.WER.Rate, fp.WER.Edits, fp.WER.Length, fp.Scored)
	return fp
}

// scored joins the windows that end by the source's truth's end, all of them when it has none, and
// returns the truth they are scored against, their joined transcript, and how many there are.
func scored(t *testing.T, docs string, src streamSource, spans []audio.Span, texts []string) (truth, text string, n int) {
	t.Helper()
	n = len(texts)
	within := time.Duration(0)
	if src.TruthEnd > 0 {
		n = 0
		for n < len(spans) && spans[n].End <= src.TruthEnd {
			n++
		}
		if n == 0 {
			t.Fatalf("no window ends by the truth's end, %v", src.TruthEnd)
		}
		within = spans[n-1].End
	}
	truth, err := src.read(docs, within)
	if err != nil {
		t.Fatal(err)
	}
	join, err := audio.Join(texts[:n], audio.JoinRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return truth, join.Text(), n
}

// paced runs the source once at real time through the live harness and records its lags, its
// settled steps, and its transcript's score.
func paced(t *testing.T, h liveHarness, docs string, src streamSource, path string, run int, u *unpacedRun) *pacedRun {
	t.Helper()
	r := h.run(t.Context(), t, media.Request{Source: media.Source{File: path, Paced: true}, Dir: t.TempDir()})
	p := &pacedRun{
		Run: run, Wall: r.returned.Sub(r.start), Ended: r.ended.Sub(r.start),
		Windows: len(r.windows), Frames: len(r.frames), Reasons: map[string]int{}, Cuts: map[string]int{},
		WindowLags: lagsOf(r.windows), FrameLags: lagsOf(r.frames),
	}
	for _, err := range r.errs {
		p.StreamErrs = append(p.StreamErrs, err.Error())
		t.Errorf("run %d: the stream ended with %v", run, err)
	}
	var windows, frames []string
	spans := make([]audio.Span, 0, len(r.windows))
	for _, e := range r.events {
		if e.Window != nil {
			p.Cuts[e.Window.Cut.String()]++
			windows = append(windows, filepath.Base(e.Window.Path))
			spans = append(spans, e.Window.Span)
		} else {
			p.Reasons[e.Frame.Reason.String()]++
			frames = append(frames, filepath.Base(e.Frame.Path))
			p.Descriptions = append(p.Descriptions, frameAnswer{Index: e.Frame.Index, Time: e.Time, Reason: e.Frame.Reason.String()})
		}
	}
	p.SameAsUnpaced = slices.Equal(windows, u.windows) && slices.Equal(frames, u.frames)
	for i, a := range r.frames {
		p.Descriptions[i].Description = a.text
	}
	for _, a := range slices.Concat(r.windows, r.frames) {
		t.Logf("recorded: run %d: %s", run, lagLine(a))
		p.Lags = append(p.Lags, recordOf(a))
		if a.err != nil {
			p.Failed++
			t.Errorf("run %d: %s %d: %v", run, a.kind, a.index, a.err)
		}
	}
	if n := len(r.windows); n > 0 {
		last := r.windows[n-1]
		p.LastWindow = last.answered.Sub(last.arrived)
	}

	// The settled text only grows: each step's settled words lead every later step's join.
	p.Steps, p.SettledGrew = len(r.steps), true
	for i, s := range r.steps {
		for _, later := range r.steps[i+1:] {
			if later.settled < s.settled || !slices.Equal(later.join.Words[:s.settled], s.join.Words[:s.settled]) {
				p.SettledGrew = false
			}
		}
	}
	if !p.SettledGrew {
		t.Errorf("run %d: a step's settled words changed in a later step", run)
	}
	texts := make([]string, len(r.windows))
	for i, a := range r.windows {
		texts[i] = a.text
	}
	truth, text, n := scored(t, docs, src, spans, texts)
	p.WER, p.Scored, p.Transcript = wer(truth, text), n, text
	if len(r.steps) > 0 {
		for _, seam := range r.steps[len(r.steps)-1].join.Seams {
			p.Seams++
			if !seam.Matched {
				p.Unmatched++
			}
		}
	}
	t.Logf("recorded: run %d: %d windows %v, %d frames %v, %d failed, same as unpaced %t; stream ended %v, last answer %v after the start, last window's transcript %v after its arrival; %d steps, settled grew %t; WER %.4f (%d of %d) over %d windows, %d seams (%d unmatched)",
		run, p.Windows, p.Cuts, p.Frames, p.Reasons, p.Failed, p.SameAsUnpaced, p.Ended.Round(time.Millisecond),
		p.Wall.Round(time.Millisecond), p.LastWindow.Round(time.Millisecond), p.Steps, p.SettledGrew,
		p.WER.Rate, p.WER.Edits, p.WER.Length, p.Scored, p.Seams, p.Unmatched)
	logLags(t, fmt.Sprintf("run %d: windows", run), p.WindowLags)
	logLags(t, fmt.Sprintf("run %d: frames", run), p.FrameLags)
	return p
}

// logLags logs a kind's lag distributions as a recorded line.
func logLags(t *testing.T, what string, l lags) {
	t.Helper()
	d := func(x dist) string {
		ms := func(v time.Duration) int64 { return v.Milliseconds() }
		return fmt.Sprintf("n %d mean %d p50 %d p90 %d max %d ms", x.N, ms(x.Mean), ms(x.P50), ms(x.P90), ms(x.Max))
	}
	t.Logf("recorded: %s: arrival to event %s; event to answer %s; queued %s; request %s; %d waited for a slot, %d in flight at most",
		what, d(l.ArrivalToEvent), d(l.EventToAnswer), d(l.Queued), d(l.Request), l.Waited, l.MaxInFlight)
}

// cancelled runs the source at real time through the live harness and cancels it midway: once an
// event is yielded at or after half the source's length into the run, cancelInFlight after its
// request is sent. It records how long the stream and the requests took to stop, how the requests
// in flight and waiting ended, and what the run left behind.
func cancelled(t *testing.T, h liveHarness, path string, seconds time.Duration) *cancelRun {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &cancelRun{}
	var (
		at      time.Time
		trigger *answer
	)
	start := time.Now()
	h.yielded = func(a *answer) {
		if trigger != nil || a.yielded.Sub(start) < seconds/2 {
			return
		}
		select {
		case <-a.sending:
			time.Sleep(cancelInFlight)
		case <-time.After(10 * time.Second):
			t.Errorf("cancel: %s %d's request wasn't sent within 10 s", a.kind, a.index)
		}
		trigger, at = a, time.Now()
		cancel()
	}
	dir := t.TempDir()
	r := h.run(ctx, t, media.Request{Source: media.Source{File: path, Paced: true}, Dir: dir})
	if trigger == nil {
		t.Fatal("cancel: no event was yielded after half the source")
	}
	c.Trigger = fmt.Sprintf("%s %d", trigger.kind, trigger.index)
	c.At, c.SourceAt, c.Events = at.Sub(r.start), trigger.source, len(r.events)
	c.StreamStopped, c.Returned = r.ended.Sub(at), r.returned.Sub(at)
	for _, err := range r.errs {
		c.StreamErrs = append(c.StreamErrs, err.Error())
	}
	if len(r.errs) != 1 || !errors.Is(r.errs[0], context.Canceled) {
		t.Errorf("cancel: stream errors %v, want context.Canceled once", r.errs)
	}
	for _, a := range slices.Concat(r.windows, r.frames) {
		t.Logf("recorded: cancel: %s, err %v", lagLine(a), a.err)
		switch {
		case a.answered.Before(at) && a.err == nil:
			c.AnsweredBefore++
		case a.sent.Before(at):
			c.InFlight++
			c.InFlightReturn = max(c.InFlightReturn, a.answered.Sub(at))
			if errors.Is(a.err, context.Canceled) {
				c.InFlightCancelled++
			}
		default:
			c.Queued++
			if errors.Is(a.err, context.Canceled) {
				c.QueuedCancelled++
			}
		}
	}
	if c.InFlightCancelled != c.InFlight || c.QueuedCancelled != c.Queued {
		t.Errorf("cancel: %d of %d in flight and %d of %d waiting returned context.Canceled",
			c.InFlightCancelled, c.InFlight, c.QueuedCancelled, c.Queued)
	}

	yielded := map[string]bool{}
	for _, e := range r.events {
		if e.Window != nil {
			yielded[filepath.Base(e.Window.Path)] = true
		} else {
			yielded[filepath.Base(e.Frame.Path)] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Files, c.Yielded = len(entries), len(yielded)
	for _, e := range entries {
		if !yielded[e.Name()] {
			c.Stray = append(c.Stray, e.Name())
		}
	}
	beside, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range beside {
		if e.Name() != filepath.Base(path) {
			c.Scratch = append(c.Scratch, e.Name())
		}
	}
	// pgrep exits 1 when no process matches.
	out, _ := exec.CommandContext(t.Context(), "pgrep", "-f", "--", path).Output()
	c.Processes = len(strings.Fields(string(out)))
	if len(c.Stray) > 0 || len(yielded) != len(entries) || len(c.Scratch) > 0 || c.Processes > 0 {
		t.Errorf("cancel: the media directory holds %d files for %d yielded, stray %q; beside the source %q; %d ffmpeg processes left",
			c.Files, c.Yielded, c.Stray, c.Scratch, c.Processes)
	}
	t.Logf("recorded: cancel: after %s, %v into the run at %v of the source, %d events; the stream ended %v after the cancel, the last request returned %v after it; stream errors %v; %d answered before, %d in flight (%d cancelled, the last returned %v after), %d waiting (%d cancelled); media directory %d files for %d yielded, stray %q; beside the source %q; %d ffmpeg processes left",
		c.Trigger, c.At.Round(time.Millisecond), c.SourceAt, c.Events, c.StreamStopped.Round(time.Millisecond),
		c.Returned.Round(time.Millisecond), c.StreamErrs, c.AnsweredBefore, c.InFlight, c.InFlightCancelled,
		c.InFlightReturn.Round(time.Millisecond), c.Queued, c.QueuedCancelled, c.Files, c.Yielded, c.Stray, c.Scratch, c.Processes)
	return c
}

// writeStreamResult writes res to STREAMS_FINDINGS_RESULTS as <source>.json, when it is set.
func writeStreamResult(t *testing.T, res *streamResult) {
	t.Helper()
	dir := os.Getenv(streamsResultsEnv)
	if dir == "" {
		return
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, res.Source.Name+".json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindingsStreamsReport reads the sources' results from STREAMS_FINDINGS_RESULTS, those
// present, and logs each source as the finding's tables give it: the unpaced read, the file
// paths, each paced run's lags and score, and the cancellation. Unset skips it.
func TestFindingsStreamsReport(t *testing.T) {
	dir := os.Getenv(streamsResultsEnv)
	if dir == "" {
		t.Skipf("%s is unset: name the directory the sources' results are in", streamsResultsEnv)
	}
	s := func(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + " s" }
	ds := func(x dist) string { return fmt.Sprintf("%s / %s / %s", s(x.P50), s(x.P90), s(x.Max)) }
	found := 0
	for _, src := range streamSources {
		data, err := os.ReadFile(filepath.Join(dir, src.Name+".json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var res streamResult
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatalf("%s: %v", src.Name, err)
		}
		found++
		u := res.Unpaced
		t.Logf("recorded: unpaced | %s | %.1f s | %d windows %v | %d frames %v | %s | %.0fx |",
			src.Name, res.Source.Seconds, u.Windows, u.Cuts, u.Frames, u.Reasons, s(u.Wall), u.Speed)
		for _, fp := range res.FilePaths {
			t.Logf("recorded: file path | %s | %s | %d windows (%d scored), %d failed | plan %s, wall %s | WER %.3f (%d/%d) |",
				src.Name, fp.Detector, fp.Windows, fp.Scored, fp.Failed, s(fp.PlanTime), s(fp.Wall), fp.WER.Rate, fp.WER.Edits, fp.WER.Length)
		}
		for _, p := range res.Runs {
			t.Logf("recorded: paced | %s | %d | %d windows, %d frames %v, %d failed, same as unpaced %t | ended %s, last answer %s, last window %s | WER %.3f (%d/%d) over %d windows, %d seams (%d unmatched) | %d steps, grew %t |",
				src.Name, p.Run, p.Windows, p.Frames, p.Reasons, p.Failed, p.SameAsUnpaced, s(p.Ended), s(p.Wall), s(p.LastWindow),
				p.WER.Rate, p.WER.Edits, p.WER.Length, p.Scored, p.Seams, p.Unmatched, p.Steps, p.SettledGrew)
			for _, kl := range []struct {
				kind string
				l    lags
			}{{"windows", p.WindowLags}, {"frames", p.FrameLags}} {
				kind, l := kl.kind, kl.l
				t.Logf("recorded: lags | %s | %d | %s | arrival to event %s | event to answer %s | queued %s | request %s | %d waited, %d in flight at most |",
					src.Name, p.Run, kind, ds(l.ArrivalToEvent), ds(l.EventToAnswer), ds(l.Queued), ds(l.Request), l.Waited, l.MaxInFlight)
			}
		}
		if c := res.Cancel; c != nil {
			t.Logf("recorded: cancel | %s | %s at %s (source %s), %d events | stream ended %s, returned %s after | %d answered, %d/%d in flight cancelled (last %s), %d/%d waiting cancelled | %d files for %d yielded, stray %q, beside %q, %d processes |",
				src.Name, c.Trigger, s(c.At), s(c.SourceAt), c.Events, s(c.StreamStopped), s(c.Returned), c.AnsweredBefore,
				c.InFlightCancelled, c.InFlight, s(c.InFlightReturn), c.QueuedCancelled, c.Queued, c.Files, c.Yielded, c.Stray, c.Scratch, c.Processes)
		}
	}
	if found == 0 {
		t.Fatalf("no source's results in %s", dir)
	}
}
