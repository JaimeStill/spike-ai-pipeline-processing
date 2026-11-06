//go:build linux

package audio_test

import (
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// feedTracker feeds pcm to a tracker of req in chunks of chunk bytes, the last shorter, and
// ends it, returning the silences it found in order.
func feedTracker(t *testing.T, pcm []byte, req audio.Request, chunk int) []audio.Silence {
	t.Helper()
	tr, err := audio.NewTracker(req)
	if err != nil {
		t.Fatal(err)
	}
	var out []audio.Silence
	for len(pcm) > 0 {
		n := min(chunk, len(pcm))
		out = append(out, tr.Feed(pcm[:n])...)
		pcm = pcm[n:]
	}
	return append(out, tr.End()...)
}

// track is feedTracker's silences' spans.
func track(t *testing.T, pcm []byte, req audio.Request, chunk int) []audio.Span {
	t.Helper()
	var out []audio.Span
	for _, s := range feedTracker(t, pcm, req, chunk) {
		out = append(out, s.Span)
	}
	return out
}

// samplesOf returns the samples of the canonical 44-byte WAV name in testdata, as 16-bit
// little-endian PCM.
func samplesOf(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data[44:]
}

// trackFile is track over the samples of the WAV name in testdata.
func trackFile(t *testing.T, name string, req audio.Request, chunk int) []audio.Span {
	t.Helper()
	return track(t, samplesOf(t, name), req, chunk)
}

// gapsFound counts the inserted gaps of the levels fixture f that lie within one of silences,
// those before the change and those after it.
func gapsFound(t *testing.T, f levelsFixture, silences []audio.Span) (before, after int) {
	t.Helper()
	change := ms(f.ChangeMS)
	for _, g := range spans(f.Gaps) {
		switch {
		case !within(g, silences):
		case g.Start < change:
			before++
		default:
			after++
		}
	}
	return before, after
}

// TestTrackerLevels runs the tracker, at its defaults, over each level-change variant: it finds
// every inserted gap, before the change and after it, each within one silence, where silencedetect
// at the threshold fixed from the first 30 s finds none after it (TestLevelsFixedThreshold, in the
// integration tier). The tracker at that fixed threshold finds none after the noise's change
// either: the gaps there are above it.
func TestTrackerLevels(t *testing.T) {
	var f levelsFixture
	readTruth(t, levelsTruth, &f)
	nBefore, nAfter := len(f.Gaps)-len(f.GapsAfterChange), len(f.GapsAfterChange)
	for _, v := range f.Variants {
		t.Run(v.Change, func(t *testing.T) {
			got := feedTracker(t, samplesOf(t, v.File), audio.Request{}, 1<<16)
			var silences []audio.Span
			for _, s := range got {
				silences = append(silences, s.Span)
				t.Logf("recorded: %v at %.2f dB", s.Span, s.Threshold)
			}
			if before, after := gapsFound(t, f, silences); before != nBefore || after != nAfter {
				t.Errorf("%d of %d gaps found before the change and %d of %d after it", before, nBefore, after, nAfter)
			}
			fixed := trackFile(t, v.File, audio.Request{Threshold: f.FixedThresholdDB}, 1<<16)
			before, after := gapsFound(t, f, fixed)
			t.Logf("recorded: at a fixed %.1f dB, %d of %d gaps found before the change and %d of %d after",
				f.FixedThresholdDB, before, nBefore, after, nAfter)
			if v.Change == "noise" && after != 0 {
				t.Errorf("at a fixed %.1f dB, %d gaps found after the noise's change", f.FixedThresholdDB, after)
			}
		})
	}
}

// TestTrackerChunking feeds each variant in chunks of random sizes, odd ones among them so samples
// split across them, and in chunks of one byte: the silences are the same, each with the same
// threshold, however the stream is chunked. Each silence a Feed returns begins at or after the
// Position before it, so a Planner given them in order is never refused.
func TestTrackerChunking(t *testing.T) {
	var f levelsFixture
	readTruth(t, levelsTruth, &f)
	r := rand.New(rand.NewPCG(3, 4))
	for _, v := range f.Variants {
		pcm := samplesOf(t, v.File)
		want := feedTracker(t, pcm, audio.Request{}, len(pcm))
		if len(want) == 0 {
			t.Fatalf("%s: no silences", v.File)
		}
		for _, chunk := range []int{1, 2, 159, 320, 321, 4097} {
			if got := feedTracker(t, pcm, audio.Request{}, chunk); !reflect.DeepEqual(got, want) {
				t.Errorf("%s in chunks of %d: %v, want %v", v.File, chunk, got, want)
			}
		}
		for range 20 {
			tr, err := audio.NewTracker(audio.Request{})
			if err != nil {
				t.Fatal(err)
			}
			var got []audio.Silence
			rest := pcm
			for len(rest) > 0 {
				n := min(1+r.IntN(20000), len(rest))
				pos := tr.Position()
				for _, s := range tr.Feed(rest[:n]) {
					if s.Start < pos {
						t.Fatalf("%s: silence %v returned after the position reached %v", v.File, s.Span, pos)
					}
					got = append(got, s)
				}
				if tr.Position() < pos {
					t.Fatalf("%s: position %v went back from %v", v.File, tr.Position(), pos)
				}
				rest = rest[n:]
			}
			got = append(got, tr.End()...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s in random chunks: %v, want %v", v.File, got, want)
			}
		}
	}
}

// tone is a stretch of a test signal: d of a sine at 500 Hz and amplitude amp, 0 for digital
// silence. At 500 Hz a 10 ms frame holds 5 whole cycles, so every frame of a tone that starts on a
// frame has the same energy.
type tone struct {
	d   time.Duration
	amp float64
}

// pcmOf returns 16-bit little-endian PCM of tones in order.
func pcmOf(tones ...tone) []byte {
	var out []byte
	i := 0
	for _, s := range tones {
		for range int(s.d * audio.SampleRate / time.Second) {
			v := s.amp * 32767 * math.Sin(2*math.Pi*500*float64(i)/audio.SampleRate)
			out = binary.LittleEndian.AppendUint16(out, uint16(int16(math.Round(v))))
			i++
		}
	}
	return out
}

// The test signal's levels: speech's bursts at -23 dB RMS, and its floor at -63 dB.
const (
	loud  = 0.1
	floor = 0.001
)

// speechLike is 2.05 s of a speech-like signal: 100 ms bursts, loud, with 50 ms of floor between
// them, a third of its frames, none a silence's minimum.
func speechLike() []tone {
	var out []tone
	for range 13 {
		out = append(out, tone{100 * time.Millisecond, loud}, tone{50 * time.Millisecond, floor})
	}
	return append(out, tone{100 * time.Millisecond, loud})
}

// TestTrackerSilences runs the tracker over a speech-like signal broken by pauses at its floor, at
// its defaults and at a fixed threshold: a pause of the minimum is a silence, to the frame, and
// one a frame shorter isn't; one open at the stream's end ends at its last sample, a part frame's
// included; and each reports the threshold in force when it opened. The silence open at the end is
// reported open once it is the minimum long, to the frames decided, and not before.
func TestTrackerSilences(t *testing.T) {
	var tones []tone
	tones = append(tones, speechLike()...)
	tones = append(tones, tone{300 * time.Millisecond, floor}) // the minimum: a silence
	tones = append(tones, speechLike()...)
	tones = append(tones, tone{290 * time.Millisecond, floor}) // a frame short of it: no silence
	tones = append(tones, speechLike()...)
	tones = append(tones, tone{505 * time.Millisecond, floor}) // open at the end, half a frame past one
	pcm := append(pcmOf(tones...), 0)                          // and a byte short of a sample, dropped
	want := []audio.Span{
		{Start: 2050 * time.Millisecond, End: 2350 * time.Millisecond},
		{Start: 6740 * time.Millisecond, End: 7245 * time.Millisecond},
	}
	tests := []struct {
		name      string
		req       audio.Request
		threshold float64
	}{
		// The floor, a third of the frames, is the 10th percentile, -63.05 dB as its amplitude
		// rounds to 16 bits; the threshold is the default margin, 12 dB, above it.
		{"rolling", audio.Request{}, -51.05},
		{"fixed", audio.Request{Threshold: -40}, -40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := feedTracker(t, pcm, tt.req, 321)
			var spans []audio.Span
			for _, s := range got {
				spans = append(spans, s.Span)
			}
			if !reflect.DeepEqual(spans, want) {
				t.Fatalf("silences %v, want %v", spans, want)
			}
			for _, s := range got {
				if s.Threshold != tt.threshold {
					t.Errorf("silence %v's threshold %v dB, want %v", s.Span, s.Threshold, tt.threshold)
				}
			}
		})
	}
	tr, err := audio.NewTracker(audio.Request{})
	if err != nil {
		t.Fatal(err)
	}
	// The silence open at the end begins at 6.74 s, frame 674.
	open := 674 * frameBytes
	tr.Feed(pcm[:open+29*frameBytes])
	if s, ok := tr.Open(); ok {
		t.Errorf("a run of 290 ms open: %v, want none", s)
	}
	tr.Feed(pcm[open+29*frameBytes : open+30*frameBytes])
	if s, ok := tr.Open(); !ok || s.Span != (audio.Span{Start: 6740 * time.Millisecond, End: 7040 * time.Millisecond}) || s.Threshold != -51.05 {
		t.Errorf("a run of 300 ms open: %v at %v dB, %v; want 6.74 s to 7.04 s at -51.05 dB", s.Span, s.Threshold, ok)
	}
	tr.Feed(pcm[open+30*frameBytes:])
	// The open run's start: the stream is settled up to the silence it may yet be.
	if got, want := tr.Position(), 6740*time.Millisecond; got != want {
		t.Errorf("position before the end %v, want %v", got, want)
	}
	if s, ok := tr.Open(); !ok || s.Span != (audio.Span{Start: 6740 * time.Millisecond, End: 7240 * time.Millisecond}) {
		t.Errorf("open before the end: %v, %v; want 6.74 s to 7.24 s, the whole frames", s.Span, ok)
	}
	tr.End()
	if s, ok := tr.Open(); ok {
		t.Errorf("open after the end: %v", s)
	}
	if got, want := tr.Position(), 7245*time.Millisecond; got != want || tr.Duration() != want {
		t.Errorf("position %v and duration %v after the end, want %v", got, tr.Duration(), want)
	}
	if got := tr.Feed(pcm); got != nil || tr.End() != nil {
		t.Errorf("fed after the end: %v", got)
	}
}

// TestTrackerWarming: a stream shorter than the warming second is decided whole at its end, and a
// stream of nothing has no silences.
func TestTrackerWarming(t *testing.T) {
	pcm := pcmOf(tone{200 * time.Millisecond, loud}, tone{400 * time.Millisecond, 0}, tone{100 * time.Millisecond, loud})
	tr, err := audio.NewTracker(audio.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.Feed(pcm); got != nil || tr.Position() != 0 {
		t.Errorf("warming: %v returned, position %v; want none, 0", got, tr.Position())
	}
	got := tr.End()
	want := []audio.Span{{Start: 200 * time.Millisecond, End: 600 * time.Millisecond}}
	if len(got) != 1 || got[0].Span != want[0] {
		t.Errorf("silences %v, want %v", got, want)
	}
	if got := feedTracker(t, nil, audio.Request{}, 1); got != nil {
		t.Errorf("an empty stream's silences %v", got)
	}
}

// TestTrackerThreshold feeds the gain-step variant a frame at a time: after the first second, the
// threshold in force after each frame is the one it was decided against, so each silence's is the
// one read after its first frame; the threshold moves with the gain step; after End it is the one
// read after the last frame; and a fixed threshold is always the request's.
func TestTrackerThreshold(t *testing.T) {
	pcm := samplesOf(t, "speech-gain.wav")
	tr, err := audio.NewTracker(audio.Request{})
	if err != nil {
		t.Fatal(err)
	}
	const frameBytes = 2 * audio.SampleRate * int(audio.FrameLength) / int(time.Second)
	var thresholds []float64
	var silences []audio.Silence
	for at := 0; at < len(pcm); at += frameBytes {
		silences = append(silences, tr.Feed(pcm[at:min(at+frameBytes, len(pcm))])...)
		thresholds = append(thresholds, tr.Threshold())
	}
	silences = append(silences, tr.End()...)
	if got := tr.Threshold(); got != thresholds[len(thresholds)-1] {
		t.Errorf("after End: %v dB, want the last frame's %v dB", got, thresholds[len(thresholds)-1])
	}
	if len(silences) == 0 {
		t.Fatal("no silences")
	}
	for _, s := range silences {
		first := int(s.Start / audio.FrameLength)
		if first < 100 {
			continue
		}
		if s.Threshold != thresholds[first] {
			t.Errorf("silence %v: %v dB, but %v dB in force after its first frame", s.Span, s.Threshold, thresholds[first])
		}
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range thresholds[100:] {
		lo, hi = min(lo, v), max(hi, v)
	}
	if hi-lo < 6 {
		t.Errorf("the threshold ranged %v to %v dB over a 12 dB gain step", lo, hi)
	}
	fixed, err := audio.NewTracker(audio.Request{Threshold: -42.5})
	if err != nil {
		t.Fatal(err)
	}
	before := fixed.Threshold()
	fixed.Feed(pcm[:audio.SampleRate])
	if after := fixed.Threshold(); before != -42.5 || after != -42.5 {
		t.Errorf("fixed threshold %v and %v dB, want -42.5", before, after)
	}
}

func TestNewTrackerRefuses(t *testing.T) {
	for name, req := range map[string]audio.Request{
		"negative minimum":               {MinSilence: -time.Second},
		"below the mean":                 {Below: 15},
		"positive threshold":             {Threshold: 1},
		"threshold under -120 dB":        {Threshold: -121},
		"threshold and percentile":       {Threshold: -40, Percentile: 10},
		"threshold and window":           {Threshold: -40, FloorWindow: time.Minute},
		"threshold and margin":           {Threshold: -40, Margin: 3},
		"negative percentile":            {Percentile: -1},
		"the 100th percentile":           {Percentile: 100},
		"NaN percentile":                 {Percentile: math.NaN()},
		"window under a second":          {FloorWindow: 999 * time.Millisecond},
		"window over ten minutes":        {FloorWindow: 10*time.Minute + time.Millisecond},
		"negative window":                {FloorWindow: -time.Second},
		"negative margin":                {Margin: -1},
		"infinite margin":                {Margin: math.Inf(1)},
		"threshold of negative infinity": {Threshold: math.Inf(-1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := audio.NewTracker(req); !errors.Is(err, audio.ErrRequest) {
				t.Errorf("err %v, want ErrRequest", err)
			}
		})
	}
	for name, req := range map[string]audio.Request{
		"the shortest window": {FloorWindow: time.Second},
		"the longest window":  {FloorWindow: 10 * time.Minute},
		"-120 dB":             {Threshold: -120},
		"a request's others":  {Path: "a.flac", Overlap: time.Second, Detector: audio.DetectorTracker},
	} {
		if _, err := audio.NewTracker(req); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// frameBytes is the bytes of the tracker's frame of PCM.
const frameBytes = 2 * audio.SampleRate * int(audio.FrameLength) / int(time.Second)

// live is a stream planned as it arrives: the windows in the order the Planner returned them, the
// silences the tracker found, and, for each window, the stream's whole frames fed before the chunk
// it was returned after, and after it; for one End returned, all of them, and the stream's
// duration.
type live struct {
	windows  []audio.Window
	silences []audio.Span
	before   []time.Duration
	after    []time.Duration
}

// livePlan plans pcm as a stream, the incremental path: a Tracker of req fed it in chunks, each of
// next(rest) bytes with rest the bytes left, its silences given to a Planner of req as they close,
// and after each chunk the Planner given the tracker's open silence, once it has one, or advanced
// to its Position, each taken no further than duration, the source's, at which it is ended. Each
// open silence the tracker reports is checked against the silence it closes into: the same start,
// and an end as late or later.
func livePlan(t *testing.T, pcm []byte, req audio.Request, next func(rest int) int, duration time.Duration) live {
	t.Helper()
	tr, err := audio.NewTracker(audio.Request{
		MinSilence: req.MinSilence, Threshold: req.Threshold,
		Percentile: req.Percentile, FloorWindow: req.FloorWindow, Margin: req.Margin,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := audio.NewPlanner(req)
	if err != nil {
		t.Fatal(err)
	}
	var out live
	var open audio.Silence
	give := func(silences []audio.Silence) {
		for _, s := range silences {
			if open.End > 0 {
				if s.Start != open.Start || s.End < open.End || s.Threshold != open.Threshold {
					t.Fatalf("silence %v at %v dB closed from the one open, %v at %v dB", s.Span, s.Threshold, open.Span, open.Threshold)
				}
				open = audio.Silence{}
			}
			out.silences = append(out.silences, s.Span)
			if err := p.Silence(s.Span); err != nil {
				t.Fatal(err)
			}
		}
	}
	fed := 0
	for fed < len(pcm) {
		n := min(next(len(pcm)-fed), len(pcm)-fed)
		before := audio.FrameLength * time.Duration(fed/frameBytes)
		give(tr.Feed(pcm[fed : fed+n]))
		fed += n
		var ws []audio.Window
		if s, ok := tr.Open(); ok && min(s.End, duration) > s.Start {
			if open.End > 0 && s.Start != open.Start {
				t.Fatalf("open silence %v, the one open before %v and not closed", s.Span, open.Span)
			}
			open = s
			ws, err = p.Open(audio.Span{Start: s.Start, End: min(s.End, duration)})
		} else {
			ws, err = p.Advance(min(tr.Position(), duration))
		}
		if err != nil {
			t.Fatal(err)
		}
		for range ws {
			out.before = append(out.before, before)
			out.after = append(out.after, audio.FrameLength*time.Duration(fed/frameBytes))
		}
		out.windows = append(out.windows, ws...)
	}
	give(tr.End())
	ws, err := p.End(duration)
	if err != nil {
		t.Fatal(err)
	}
	for range ws {
		out.before = append(out.before, audio.FrameLength*time.Duration(fed/frameBytes))
		out.after = append(out.after, duration)
	}
	out.windows = append(out.windows, ws...)
	return out
}

// streamPlan plans pcm as a stream, livePlan's incremental path, in chunks of chunk bytes, and
// returns the windows in the order the Planner returned them.
func streamPlan(t *testing.T, pcm []byte, req audio.Request, chunk int, duration time.Duration) []audio.Window {
	t.Helper()
	return livePlan(t, pcm, req, func(int) int { return chunk }, duration).windows
}
