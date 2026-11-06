//go:build linux

package audio_test

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// cycle is one cycle of the test signal's 500 Hz sine at full scale, 32 samples at SampleRate:
// pcmOf's samples, by a table, for streams minutes long.
var cycle = func() [32]float64 {
	var c [32]float64
	for i := range c {
		c[i] = math.Sin(2 * math.Pi * float64(i) / 32)
	}
	return c
}()

// signal builds a test signal of tones, each a whole number of frames, so every frame of a tone
// has the same energy.
type signal struct {
	pcm []byte
}

// add appends d of the sine at amplitude amp.
func (s *signal) add(d time.Duration, amp float64) {
	for range int(d * audio.SampleRate / time.Second) {
		i := len(s.pcm) / 2
		v := amp * 32767 * cycle[i%32]
		s.pcm = binary.LittleEndian.AppendUint16(s.pcm, uint16(int16(math.Round(v))))
	}
}

// speech appends n of speechLike's 2.05 s blocks: loud bursts with floor between them, none a
// silence's minimum.
func (s *signal) speech(n int) {
	for range n {
		for _, tn := range speechLike() {
			s.add(tn.d, tn.amp)
		}
	}
}

// at returns the signal's length.
func (s *signal) at() time.Duration {
	return time.Duration(len(s.pcm)/2) * time.Second / audio.SampleRate
}

// speechTo appends speech up to at, whole blocks and then a loud burst for the rest, so a pause
// that follows begins at at, after a loud frame.
func (s *signal) speechTo(at time.Duration) {
	block := 2050 * time.Millisecond
	if n := int((at - s.at()) / block); n > 0 {
		s.speech(n)
	}
	if rest := at - s.at(); rest > 0 {
		s.add(rest, loud)
	}
}

// randomSignal returns a test signal from r about a minute to five long: speech broken by pauses
// at the floor, short ones near MinSilence, some a few seconds, and some longer than a window,
// 31 to 75 s, which cross one or two limits, each followed by speech.
func randomSignal(r *rand.Rand) []byte {
	var s signal
	end := time.Minute + time.Duration(r.Int64N(int64(4*time.Minute)))
	ms := func(lo, hi int) time.Duration { return time.Duration(lo+r.IntN(hi-lo+1)) * 10 * time.Millisecond }
	for s.at() < end {
		s.speech(1 + r.IntN(6))
		s.add(ms(0, 50), loud)
		switch r.IntN(8) {
		case 0, 1, 2:
			s.add(ms(25, 35), floor)
		case 3, 4, 5:
			s.add(ms(30, 300), floor)
		case 6:
			s.add(ms(3100, 7500), floor)
		}
	}
	return s.pcm
}

// randomChunks returns chunk sizes from r: a few bytes, odd ones among them so samples split, a
// few frames, or many.
func randomChunks(r *rand.Rand) func(int) int {
	return func(int) int {
		switch r.IntN(3) {
		case 0:
			return 1 + r.IntN(2*frameBytes)
		case 1:
			return 1 + r.IntN(8192)
		}
		return 1 + r.IntN(1<<17)
	}
}

// checkLatency checks that each window but the last of l was returned once the stream had passed
// its limit, and by the chunk that carried the stream to bound past it.
func checkLatency(t *testing.T, l live, bound time.Duration) {
	t.Helper()
	for i, w := range l.windows[:len(l.windows)-1] {
		limit := w.Start + audio.WindowLimit
		if l.after[i] <= limit {
			t.Errorf("window %d, %v, returned at %v, before its limit %v", i, w.Span, l.after[i], limit)
		}
		if l.before[i] >= limit+bound {
			t.Errorf("window %d, %v, returned at %v, not by %v past its limit %v (%v)", i, w.Span, l.after[i], bound, limit, limit+bound)
		}
	}
}

// TestLivePlanMatchesPlanWindows plans random test signals, from a fixed seed, as streams fed in
// random chunks: a Tracker and a Planner given its open silence or its Position after each chunk.
// The signals' pauses include ones longer than a window, which cross one or two limits, and ones
// near MinSilence, some of which open at a limit. The windows are PlanWindows' over the silences
// the tracker found, exactly; and each is returned once the stream passes its limit, and by the
// chunk that carries it MinSilence past: a run of silent frames open across the limit, shorter so
// far, may yet be a silence that ends the window, and is known either way by then.
func TestLivePlanMatchesPlanWindows(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	long, crossings := 0, 0
	for c := range 24 {
		pcm := randomSignal(r)
		req := audio.Request{Overlap: time.Duration(r.Int64N(int64(audio.WindowLimit / 2)))}
		if c%4 == 3 {
			req.Threshold = -40
		}
		duration := time.Duration(len(pcm)/2) * time.Second / audio.SampleRate
		got := livePlan(t, pcm, req, randomChunks(r), duration)
		want, err := audio.PlanWindows(duration, got.silences, req)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.windows, want.Windows) {
			t.Fatalf("case %d, overlap %v, threshold %v: windows %v, want %v; silences %v",
				c, req.Overlap, req.Threshold, got.windows, want.Windows, got.silences)
		}
		checkLatency(t, got, audio.DefaultMinSilence)
		for _, s := range got.silences {
			if s.Duration() > audio.WindowLimit {
				long++
			}
			for _, w := range want.Windows {
				if limit := w.Start + audio.WindowLimit; s.Start < limit && s.End > limit {
					crossings++
				}
			}
		}
	}
	// The generator's cases are the ones the test is for.
	if long < 10 || crossings < 20 {
		t.Errorf("%d silences longer than a window, %d crossing a limit; want 10 and 20 at least", long, crossings)
	}
}

// TestLivePlanLongSilence plans 25 s of speech, 70 s of floor across two limits, and 20 s of
// speech, fed a frame at a time and in half seconds: each window whose limit is inside the
// silence is returned with the frame or chunk that carries the stream past the limit, not when
// the silence ends; the first ends at the silence's middle up to its limit, and the next two,
// whose start is inside the silence, at their limits. The windows are PlanWindows'.
func TestLivePlanLongSilence(t *testing.T) {
	var s signal
	s.speechTo(25 * time.Second)
	s.add(70*time.Second, floor)
	s.speech(10)
	duration := s.at()
	want := []audio.Window{
		win(0, 27.5, audio.CutSilence),
		win(25.5, 55.5, audio.CutLimit),
		win(53.5, 83.5, audio.CutLimit),
	}
	for _, chunk := range []int{frameBytes, audio.SampleRate} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			got := livePlan(t, s.pcm, audio.Request{}, func(int) int { return chunk }, duration)
			plan, err := audio.PlanWindows(duration, got.silences, audio.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.windows, plan.Windows) || !reflect.DeepEqual(got.windows[:3], want) {
				t.Fatalf("windows %v, want PlanWindows' %v, starting %v", got.windows, plan.Windows, want)
			}
			step := audio.FrameLength * time.Duration(chunk/frameBytes)
			for i, w := range want {
				limit := w.Start + audio.WindowLimit
				if got.after[i] <= limit || got.after[i] > limit+step {
					t.Errorf("window %d, %v, returned at %v, want with the %v that passes its limit, %v", i, w.Span, got.after[i], step, limit)
				}
			}
		})
	}
}

// TestLivePlanMinSilenceAtLimit plans speech with one pause near MinSilence that opens at or just
// before the first window's limit, 30 s, fed a frame at a time. The pause is a silence when it
// lasts MinSilence, though most of it is past the limit: the window ends at its middle up to the
// limit, as PlanWindows says. The window is returned as soon as that is known, with the frame
// that passes the limit, or, while the run of silent frames open across it is shorter than
// MinSilence, once it reaches MinSilence or ends.
func TestLivePlanMinSilenceAtLimit(t *testing.T) {
	const limit = audio.WindowLimit
	for _, early := range []time.Duration{0, 10, 100, 200, 290, 300, 310, 500} {
		for _, length := range []time.Duration{290, 300, 310, 600} {
			early, length := early*time.Millisecond, length*time.Millisecond
			var s signal
			start := limit - early
			s.speechTo(start)
			s.add(length, floor)
			s.speech(6)
			duration := s.at()
			got := livePlan(t, s.pcm, audio.Request{}, func(int) int { return frameBytes }, duration)
			plan, err := audio.PlanWindows(duration, got.silences, audio.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.windows, plan.Windows) {
				t.Fatalf("pause of %v from %v: windows %v, want PlanWindows' %v", length, start, got.windows, plan.Windows)
			}
			first := win(0, 30, audio.CutLimit)
			if length >= audio.DefaultMinSilence && early > 0 {
				first = audio.Window{End: start + min(length, early)/2, Cut: audio.CutSilence}
			}
			if got.windows[0] != first {
				t.Errorf("pause of %v from %v: first window %v, want %v", length, start, got.windows[0], first)
			}
			// The window is known with the frame past the limit, or once the run open there
			// reaches MinSilence or its first frame of speech is decided.
			known := max(limit+audio.FrameLength, min(start+length+audio.FrameLength, start+audio.DefaultMinSilence))
			if got.after[0] != known {
				t.Errorf("pause of %v from %v: first window returned at %v, want %v", length, start, got.after[0], known)
			}
		}
	}
}
