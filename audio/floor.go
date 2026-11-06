//go:build linux

package audio

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// FrameLength is the tracker's frame, 10 ms, 160 samples at SampleRate: it measures each frame's
// energy and decides each frame silent or not.
const FrameLength = 10 * time.Millisecond

// frameSamples is the samples in a frame at SampleRate.
const frameSamples = SampleRate * int(FrameLength) / int(time.Second)

// The tracker's defaults, chosen by measurement: TestFindingsFloor, in the findings tier, measures
// a grid of settings over the level-change fixtures and the corpus's long sources, unchanged and
// with a gain step and a noise floor added at 10 minutes. These are the settings that find every
// inserted gap of both fixtures, before their change and after it, by the middle of a band of
// margins that do, and cut no more windows at the limit than silencedetect at its default does on
// the long sources unchanged, nor more under either change.
const (
	// DefaultPercentile is the percentile of frame energy a request's zero Percentile asks for:
	// the 10th, the quietest tenth of the frames, which in speech is its pauses.
	DefaultPercentile = 10.0
	// DefaultFloorWindow is the rolling window a request's zero FloorWindow asks for: 4 s, the
	// frames whose energy the percentile is taken over, short enough that the floor follows a
	// change within a few seconds, long enough to hold a pause.
	DefaultFloorWindow = 4 * time.Second
	// DefaultMargin is how far above the percentile, in dB, a request's zero Margin puts the
	// threshold: 12 dB.
	DefaultMargin = 12.0
)

// maxFloorWindow is the longest rolling window a request may ask for, 10 minutes: the tracker
// keeps each frame's energy in it, 2 bytes a frame, 120,000 bytes for this one.
const maxFloorWindow = 10 * time.Minute

// The tracker measures energy in centibels, hundredths of a dB, relative to full scale, as an
// integer, so its percentile and its comparisons are exact: from floorCB, -120 dB, where it puts a
// frame of digital silence and any quieter, to 0 dB, a frame at full scale.
const (
	floorCB = -12000
	// bins is the centibel values from floorCB to 0, and fenwickSize the power of two above it
	// the tree's descent needs.
	bins        = -floorCB + 1
	fenwickSize = 1 << 14
)

// Silence is a silence the Tracker found: its span of the stream and the threshold in force when
// it opened.
type Silence struct {
	Span
	// Threshold is the threshold, in dB relative to full scale, in force at the silence's first
	// frame: the rolling percentile and the margin then, or the request's fixed Threshold.
	Threshold float64
}

// tracker is a request's tracker settings, their defaults applied.
type tracker struct {
	minSilence time.Duration
	fixed      bool
	threshold  int // centibels, when fixed
	percentile float64
	window     int // frames
	margin     int // centibels
}

// checkTracker validates req's tracker fields, MinSilence, Threshold, Percentile, FloorWindow, and
// Margin, and applies their defaults; it reads no other field. An invalid field is an error
// wrapping ErrRequest.
func (req Request) checkTracker() (tracker, error) {
	tr := tracker{minSilence: cmp.Or(req.MinSilence, DefaultMinSilence)}
	tuned := req.Percentile != 0 || req.FloorWindow != 0 || req.Margin != 0
	switch {
	case req.MinSilence < 0:
		return tracker{}, fmt.Errorf("audio: minimum silence %v is below zero: %w", req.MinSilence, ErrRequest)
	case !finite(req.Threshold) || req.Threshold > 0 || req.Threshold < floorCB/100:
		return tracker{}, fmt.Errorf("audio: threshold %v dB isn't from %d to below zero: %w", req.Threshold, floorCB/100, ErrRequest)
	case req.Threshold != 0 && tuned:
		return tracker{}, fmt.Errorf("audio: a fixed threshold of %v dB and a rolling one's settings: %w", req.Threshold, ErrRequest)
	case req.Threshold != 0:
		tr.fixed, tr.threshold = true, centibels(req.Threshold)
		return tr, nil
	case !finite(req.Percentile) || req.Percentile < 0 || req.Percentile >= 100:
		return tracker{}, fmt.Errorf("audio: percentile %v isn't from 0 to under 100: %w", req.Percentile, ErrRequest)
	case req.FloorWindow != 0 && (req.FloorWindow < warmFrames*FrameLength || req.FloorWindow > maxFloorWindow):
		return tracker{}, fmt.Errorf("audio: rolling window %v isn't 0 or from %v to %v: %w",
			req.FloorWindow, warmFrames*FrameLength, maxFloorWindow, ErrRequest)
	case !finite(req.Margin) || req.Margin < 0:
		return tracker{}, fmt.Errorf("audio: margin %v dB isn't zero or more: %w", req.Margin, ErrRequest)
	}
	tr.percentile = cmp.Or(req.Percentile, DefaultPercentile)
	tr.window = int(cmp.Or(req.FloorWindow, DefaultFloorWindow) / FrameLength)
	tr.margin = centibels(cmp.Or(req.Margin, DefaultMargin))
	return tr, nil
}

// centibels returns db in whole centibels, rounded half away from zero.
func centibels(db float64) int { return int(math.Round(db * 100)) }

// warmFrames is how many frames, 1 s of them, the tracker measures before it decides the first:
// the frames before then are decided against the percentile of all of them, so the stream's first
// frames aren't judged against their own energy alone.
const warmFrames = 100

// Tracker finds the silences of a stream of PCM as it arrives: 16 kHz, SampleRate, mono, 16-bit
// little-endian samples, the windows' format. It measures each 10 ms frame's energy, its RMS in
// dB relative to full scale, and decides it silent when the energy is below the threshold in force:
// the request's fixed Threshold, or the Percentile of the energy of the frames in the rolling
// FloorWindow up to and including the frame, plus the Margin. A run of silent frames at least
// MinSilence long is a Silence. Because the threshold follows the stream's quietest frames, it
// follows a change of level or of noise floor, which a threshold fixed for the stream can't.
//
// The first warmFrames frames, 1 s, wait for one another: each is decided against the percentile
// of all of them, so the stream's start is decided as the rest is. After them, each frame is
// decided as it arrives.
//
// A Tracker's memory is bounded by its rolling window, whatever the stream's length: it keeps each
// frame of the window's energy and a fixed histogram of them, and less than a frame of samples. It
// reads nothing but its samples, and frames them by their place in the stream, so the same
// samples give the same silences however they are chunked. It isn't safe for concurrent use.
type Tracker struct {
	settings tracker
	// ring holds the energy of the window's frames, in centibels, the oldest at head once full;
	// tree counts them by value, as a Fenwick tree over bins.
	ring []int16
	head int
	tree []int32
	// pending is the bytes of the frame not yet whole, less than a frame's.
	pending []byte
	// frames is the frames measured, and decided those decided; warm frames wait between them.
	frames, decided int
	// samples is the samples the tracker has been given, its stream's length so far.
	samples int64
	// run is the first frame of the open run of silent frames, -1 when there is none, and
	// runThreshold the threshold at it, in centibels.
	run          int
	runThreshold int
	ended        bool
}

// NewTracker returns a Tracker that finds silences as req says: its MinSilence, the shortest
// silence; and either its fixed Threshold or its rolling threshold's Percentile, FloorWindow, and
// Margin. It reads no other field; Below, the mean volume's, isn't the tracker's, and must be zero.
//
// A MinSilence below zero; a Threshold above zero or below -120 dB; a Threshold with any of
// Percentile, FloorWindow, or Margin; a Percentile below zero or 100 or more; a FloorWindow not zero
// and under 1 s or over 10 minutes; a Margin below zero; or a Below not zero, is an error wrapping
// ErrRequest.
func NewTracker(req Request) (*Tracker, error) {
	if req.Below != 0 {
		return nil, fmt.Errorf("audio: %v dB below the mean volume, which the tracker doesn't measure: %w", req.Below, ErrRequest)
	}
	settings, err := req.checkTracker()
	if err != nil {
		return nil, err
	}
	return newTracker(settings), nil
}

// newTracker returns a Tracker over checked settings.
func newTracker(settings tracker) *Tracker {
	t := &Tracker{settings: settings, run: -1, pending: make([]byte, 0, 2*frameSamples)}
	if !settings.fixed {
		t.ring = make([]int16, 0, settings.window)
		t.tree = make([]int32, fenwickSize+1)
	}
	return t
}

// Feed gives the tracker the next bytes of the stream, any number of them, a sample's two bytes
// split across calls or not, and returns the silences they close, in order. A silence closes at
// the first frame after it that isn't silent; one still open when the stream ends is closed by
// End. Feed after End returns nothing.
func (t *Tracker) Feed(pcm []byte) []Silence {
	if t.ended {
		return nil
	}
	var out []Silence
	if len(t.pending) > 0 {
		n := min(len(pcm), cap(t.pending)-len(t.pending))
		t.pending = append(t.pending, pcm[:n]...)
		pcm = pcm[n:]
		if len(t.pending) < cap(t.pending) {
			return nil
		}
		out = t.frame(t.pending, out)
		t.pending = t.pending[:0]
	}
	for len(pcm) >= 2*frameSamples {
		out = t.frame(pcm[:2*frameSamples], out)
		pcm = pcm[2*frameSamples:]
	}
	t.pending = append(t.pending, pcm...)
	return out
}

// End ends the stream: it measures the samples of the last frame, if they are fewer than a
// frame's, decides any frames still warming, and returns the silences that closes, in order, a run
// of silent frames open at the end closed at the stream's end if it is MinSilence long. A trailing
// byte that isn't a whole sample is dropped. After End, the tracker takes nothing more, and its
// Position is the stream's Duration.
func (t *Tracker) End() []Silence {
	if t.ended {
		return nil
	}
	var out []Silence
	if len(t.pending) >= 2 {
		out = t.frame(t.pending[:len(t.pending)/2*2], out)
	}
	t.pending = nil
	out = t.decide(out)
	t.ended = true
	if t.run >= 0 {
		out = t.close(t.Duration(), out)
	}
	return out
}

// Duration returns the length of the stream the tracker has been given, its whole samples at
// SampleRate.
func (t *Tracker) Duration() time.Duration {
	return time.Duration(t.samples) * time.Second / SampleRate
}

// Position returns how far the stream is settled: every silence that begins before it has been
// returned. It is the start of the open run of silent frames, which may yet be a silence, or the
// end of the frames decided, whichever is earlier; after End, the stream's Duration. An
// incremental Planner's Advance takes it.
func (t *Tracker) Position() time.Duration {
	if t.ended {
		return t.Duration()
	}
	if t.run >= 0 {
		return frameTime(t.run)
	}
	return frameTime(t.decided)
}

// Threshold returns the threshold in force now, in dB relative to full scale: the request's fixed
// Threshold, or the rolling one, the Percentile of the energy of the frames in the FloorWindow up
// to the newest plus the Margin. Once the first second has passed, it is the threshold the newest
// whole frame was decided against, so a caller that feeds the stream a frame at a time reads each
// frame's; before then it is the threshold the warming frames would be decided against were the
// stream to end now, which End decides them against. After End, it is the threshold the last
// frame was decided against. Before any frame, the rolling threshold is -120 dB plus the Margin.
func (t *Tracker) Threshold() float64 {
	if t.settings.fixed {
		return float64(t.settings.threshold) / 100
	}
	return float64(t.threshold()) / 100
}

// frameTime is the stream time frame i starts at.
func frameTime(i int) time.Duration { return time.Duration(i) * FrameLength }

// frame measures one frame's samples, a frame's or, the last, fewer, and decides what frames it
// can, appending the silences that closes to out.
func (t *Tracker) frame(pcm []byte, out []Silence) []Silence {
	n := len(pcm) / 2
	var sum int64
	for i := range n {
		s := int64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		sum += s * s
	}
	t.samples += int64(n)
	e := floorCB
	if sum > 0 {
		db := 10 * math.Log10(float64(sum)/(float64(n)*32768*32768))
		e = min(max(centibels(db), floorCB), 0)
	}
	t.frames++
	if t.settings.fixed {
		// A fixed threshold has no window and no warming: the frame is decided now.
		return t.decideFrame(e < t.settings.threshold, t.settings.threshold, out)
	}
	if len(t.ring) < t.settings.window {
		t.ring = append(t.ring, int16(e))
	} else {
		t.add(int(t.ring[t.head]), -1)
		t.ring[t.head] = int16(e)
		t.head = (t.head + 1) % len(t.ring)
	}
	t.add(e, 1)
	if t.frames < warmFrames {
		return out
	}
	return t.decide(out)
}

// decide decides the frames measured but not decided: the warming frames, each against the
// threshold now in force, or the newest frame. It appends the silences that closes to out.
func (t *Tracker) decide(out []Silence) []Silence {
	if t.settings.fixed || t.decided == t.frames {
		return out
	}
	threshold := t.threshold()
	for t.decided < t.frames {
		// The frames not yet decided are the newest in the ring, at most warmFrames of them,
		// which a window of at least warmFrames holds.
		back := t.frames - t.decided
		e := int(t.ring[(t.head+len(t.ring)-back)%len(t.ring)])
		out = t.decideFrame(e < threshold, threshold, out)
	}
	return out
}

// decideFrame decides the next frame silent or not, at threshold, in centibels, appending to out
// the silence it closes, if any.
func (t *Tracker) decideFrame(silent bool, threshold int, out []Silence) []Silence {
	i := t.decided
	t.decided++
	switch {
	case silent && t.run < 0:
		t.run, t.runThreshold = i, threshold
	case !silent && t.run >= 0:
		out = t.close(frameTime(i), out)
	}
	return out
}

// close closes the open run of silent frames at end, appending it to out as a Silence if it is
// at least MinSilence long.
func (t *Tracker) close(end time.Duration, out []Silence) []Silence {
	start := frameTime(t.run)
	if end-start >= t.settings.minSilence {
		out = append(out, Silence{Span: Span{start, end}, Threshold: float64(t.runThreshold) / 100})
	}
	t.run = -1
	return out
}

// threshold returns the rolling threshold in centibels: the energy at the percentile of the
// ring's, the k-th smallest with k the frames times the percentile, rounded down, plus the margin.
func (t *Tracker) threshold() int {
	n := len(t.ring)
	k := min(int(float64(n)*t.settings.percentile/100), n-1)
	// Descend the Fenwick tree for the smallest bin whose count from the bottom passes k.
	pos, rest := 0, int32(k)
	for step := fenwickSize; step > 0; step >>= 1 {
		if next := pos + step; next <= fenwickSize && t.tree[next] <= rest {
			pos, rest = next, rest-t.tree[next]
		}
	}
	// pos is the count of bins below the k-th value's, so its bin, from 0, is pos.
	return pos + floorCB + t.settings.margin
}

// add counts delta more frames of energy e, in centibels, in the tree.
func (t *Tracker) add(e, delta int) {
	for i := e - floorCB + 1; i <= fenwickSize; i += i & -i {
		t.tree[i] += int32(delta)
	}
}
