//go:build linux

package audio_test

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// sec is n seconds.
func sec(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }

// span is the span from a to b, in seconds.
func span(a, b float64) audio.Span { return audio.Span{Start: sec(a), End: sec(b)} }

// win is a window from a to b, in seconds, ended as cut says.
func win(a, b float64, cut audio.Cut) audio.Window {
	return audio.Window{Span: span(a, b), Cut: cut}
}

func TestPlanWindows(t *testing.T) {
	const (
		silence = audio.CutSilence
		limit   = audio.CutLimit
		end     = audio.CutEnd
	)
	tests := []struct {
		name     string
		duration float64
		silences []audio.Span
		overlap  time.Duration
		windows  []audio.Window
		seams    []audio.Span
	}{
		{
			name:     "shorter than a window is one window, without seams",
			duration: 20,
			silences: []audio.Span{span(5, 6), span(12, 13)},
			windows:  []audio.Window{win(0, 20, end)},
		},
		{
			name:     "exactly a window is one window",
			duration: 30,
			silences: []audio.Span{span(14, 15)},
			windows:  []audio.Window{win(0, 30, end)},
		},
		{
			name:     "no silence cuts at the limit",
			duration: 70,
			windows:  []audio.Window{win(0, 30, limit), win(28, 58, limit), win(56, 70, end)},
			seams:    []audio.Span{span(28, 30), span(56, 58)},
		},
		{
			name:     "a window ends at the middle of the latest silence",
			duration: 50,
			silences: []audio.Span{span(6, 7), span(14, 15), span(24, 26)},
			windows:  []audio.Window{win(0, 25, silence), win(23, 50, end)},
			seams:    []audio.Span{span(23, 25)},
		},
		{
			name:     "a silence at the very start never ends a window",
			duration: 40,
			silences: []audio.Span{span(0, 1)},
			windows:  []audio.Window{win(0, 30, limit), win(28, 40, end)},
			seams:    []audio.Span{span(28, 30)},
		},
		{
			name:     "a silence at the very end is in the last window",
			duration: 40,
			silences: []audio.Span{span(39, 40)},
			windows:  []audio.Window{win(0, 30, limit), win(28, 40, end)},
			seams:    []audio.Span{span(28, 30)},
		},
		{
			name:     "a silence across the limit is taken within it",
			duration: 50,
			silences: []audio.Span{span(29, 31)},
			windows:  []audio.Window{win(0, 29.5, silence), win(27.5, 50, end)},
			seams:    []audio.Span{span(27.5, 29.5)},
		},
		{
			name:     "a tiny tail is a last window of the seam and the tail",
			duration: 30.1,
			silences: []audio.Span{span(29, 29.8)},
			windows:  []audio.Window{win(0, 29.4, silence), win(27.4, 30.1, end)},
			seams:    []audio.Span{span(27.4, 29.4)},
		},
		{
			// The silence's part past the first window's limit has its middle at 30.25, in the
			// second window's leading seam's reach: ending there would leave the window no audio
			// of its own, and its two seams would overlap.
			name:     "a silence in a window's first two overlaps doesn't end it",
			duration: 80,
			silences: []audio.Span{span(29.5, 31)},
			windows:  []audio.Window{win(0, 29.75, silence), win(27.75, 57.75, limit), win(55.75, 80, end)},
			seams:    []audio.Span{span(27.75, 29.75), span(55.75, 57.75)},
		},
		{
			name:     "a silence within the overlap of the first window's start doesn't end it",
			duration: 40,
			silences: []audio.Span{span(1, 2.5)},
			windows:  []audio.Window{win(0, 30, limit), win(28, 40, end)},
			seams:    []audio.Span{span(28, 30)},
		},
		{
			name:     "the request's overlap",
			duration: 60,
			overlap:  5 * time.Second,
			windows:  []audio.Window{win(0, 30, limit), win(25, 55, limit), win(50, 60, end)},
			seams:    []audio.Span{span(25, 30), span(50, 55)},
		},
		{
			name:     "silences out of order, overlapping, and past the source",
			duration: 50,
			silences: []audio.Span{span(45, 60), span(10, 12), span(24, 26), span(11, 13), span(-1, 0.5)},
			windows:  []audio.Window{win(0, 25, silence), win(23, 50, end)},
			seams:    []audio.Span{span(23, 25)},
		},
		{
			// The silence ends the first window at the middle of its part within the limit; the
			// windows after start within it, and it is behind them.
			name:     "a silence longer than a window ends one window",
			duration: 100,
			silences: []audio.Span{span(20, 70)},
			windows:  []audio.Window{win(0, 25, silence), win(23, 53, limit), win(51, 81, limit), win(79, 100, end)},
			seams:    []audio.Span{span(23, 25), span(51, 53), span(79, 81)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			duration := sec(tt.duration)
			req := audio.Request{Overlap: tt.overlap}
			plan, err := audio.PlanWindows(duration, tt.silences, req)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan.Windows, tt.windows) {
				t.Errorf("windows %v, want %v", plan.Windows, tt.windows)
			}
			if !reflect.DeepEqual(plan.Seams, tt.seams) {
				t.Errorf("seams %v, want %v", plan.Seams, tt.seams)
			}
			checkPlan(t, plan, duration)
			again, err := audio.PlanWindows(duration, tt.silences, req)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan, again) {
				t.Errorf("planned twice: %+v, then %+v", plan, again)
			}
		})
	}
}

// TestPlanWindowsInvariants plans sources of random durations and silences, from a fixed seed,
// and checks each plan's invariants: the windows cover the source in order, each at most the
// limit, seams the overlap long and never overlapping, and the same plan for the silences in any
// order.
func TestPlanWindowsInvariants(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		duration := time.Duration(1 + r.Int64N(int64(5*time.Minute)))
		var silences []audio.Span
		for range r.IntN(40) {
			start := time.Duration(r.Int64N(int64(duration)))
			silences = append(silences, audio.Span{Start: start, End: start + time.Duration(r.Int64N(int64(3*time.Second)))})
		}
		overlap := time.Duration(r.Int64N(int64(audio.WindowLimit / 2)))
		req := audio.Request{Overlap: overlap}
		plan, err := audio.PlanWindows(duration, silences, req)
		if err != nil {
			t.Fatalf("duration %v, overlap %v: %v", duration, overlap, err)
		}
		checkPlan(t, plan, duration)
		shuffled := append([]audio.Span(nil), silences...)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		again, err := audio.PlanWindows(duration, shuffled, req)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(plan, again) {
			t.Fatalf("the silences shuffled planned %+v, in order %+v", again, plan)
		}
		if t.Failed() {
			t.Fatalf("duration %v, overlap %v, silences %v", duration, overlap, silences)
		}
	}
}

// checkPlan checks plan's invariants for a source duration long.
func checkPlan(t *testing.T, plan audio.Plan, duration time.Duration) {
	t.Helper()
	if plan.Duration != duration {
		t.Errorf("plan's duration %v, want %v", plan.Duration, duration)
	}
	ws := plan.Windows
	if len(ws) == 0 {
		t.Fatal("no windows")
	}
	if len(plan.Seams) != len(ws)-1 {
		t.Errorf("%d seams for %d windows", len(plan.Seams), len(ws))
	}
	if ws[0].Start != 0 {
		t.Errorf("the first window starts at %v", ws[0].Start)
	}
	if last := ws[len(ws)-1]; last.End != duration || last.Cut != audio.CutEnd {
		t.Errorf("the last window is %v, cut %v; want it to end at %v, cut end", last.Span, last.Cut, duration)
	}
	for i, w := range ws {
		if w.Duration() <= 0 || w.Duration() > audio.WindowLimit {
			t.Errorf("window %d, %v, is %v long", i, w.Span, w.Duration())
		}
		if i == len(ws)-1 {
			break
		}
		if w.Cut == audio.CutEnd {
			t.Errorf("window %d, %v, isn't the last but is cut at the end", i, w.Span)
		}
		next := ws[i+1]
		seam := plan.Seams[i]
		if want := (audio.Span{Start: next.Start, End: w.End}); seam != want {
			t.Errorf("seam %d is %v, want %v", i, seam, want)
		}
		if seam.Duration() != plan.Overlap {
			t.Errorf("seam %d, %v, is %v long, want the overlap, %v", i, seam, seam.Duration(), plan.Overlap)
		}
		if i > 0 && plan.Seams[i-1].End > seam.Start && plan.Overlap > 0 {
			t.Errorf("seams %d, %v, and %d, %v, overlap", i-1, plan.Seams[i-1], i, seam)
		}
		if next.Start <= w.Start {
			t.Errorf("window %d starts at %v, not after window %d's start, %v", i+1, next.Start, i, w.Start)
		}
	}
}

func TestPlanWindowsRefuses(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		overlap  time.Duration
	}{
		{"zero duration", 0, 0},
		{"negative duration", -time.Second, 0},
		{"negative overlap", time.Minute, -time.Second},
		{"half the window's overlap", time.Minute, audio.WindowLimit / 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := audio.PlanWindows(tt.duration, nil, audio.Request{Overlap: tt.overlap})
			if !errors.Is(err, audio.ErrRequest) {
				t.Errorf("err %v, want ErrRequest", err)
			}
		})
	}
	if _, err := audio.PlanWindows(time.Minute, nil, audio.Request{Overlap: audio.WindowLimit/2 - 1}); err != nil {
		t.Errorf("an overlap just under half the window: %v", err)
	}
}

func TestCheckOverlap(t *testing.T) {
	if got, err := audio.CheckOverlap(0); err != nil || got != audio.DefaultOverlap {
		t.Errorf("CheckOverlap(0) = %v, %v; want the default, %v", got, err, audio.DefaultOverlap)
	}
	if got, err := audio.CheckOverlap(3 * time.Second); err != nil || got != 3*time.Second {
		t.Errorf("CheckOverlap(3s) = %v, %v; want 3s", got, err)
	}
}

func TestSpanString(t *testing.T) {
	if got, want := span(22.9791875, 49.82).String(), "22.979s-49.820s"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
