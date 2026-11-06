//go:build linux

package audio_test

import (
	"cmp"
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
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

// planIncrementally plans a source duration long whose silences are silences through a Planner
// of req, as a stream: it advances through positions, in order, giving before each the silences
// that begin before it, and then ends at duration. It checks that each Advance returns exactly
// the windows of want, PlanWindows' plan, whose limit the position has passed, and returns every
// window the planner returned, in order.
func planIncrementally(t *testing.T, duration time.Duration, silences []audio.Span, positions []time.Duration, req audio.Request, want audio.Plan) []audio.Window {
	t.Helper()
	p, err := audio.NewPlanner(req)
	if err != nil {
		t.Fatal(err)
	}
	sorted := slices.Clone(silences)
	slices.SortFunc(sorted, func(a, b audio.Span) int { return cmp.Compare(max(a.Start, 0), max(b.Start, 0)) })
	var got []audio.Window
	for _, pos := range positions {
		for len(sorted) > 0 && max(sorted[0].Start, 0) < pos {
			if err := p.Silence(sorted[0]); err != nil {
				t.Fatal(err)
			}
			sorted = sorted[1:]
		}
		ws, err := p.Advance(pos)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ws...)
		fixed := 0
		for _, w := range want.Windows {
			if w.Start+audio.WindowLimit < pos && w.Cut != audio.CutEnd {
				fixed++
			}
		}
		if len(got) != fixed {
			t.Fatalf("at %v, %d windows returned, want the %d fixed", pos, len(got), fixed)
		}
	}
	for _, s := range sorted {
		if err := p.Silence(s); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := p.End(duration)
	if err != nil {
		t.Fatal(err)
	}
	return append(got, ws...)
}

// randomPositions returns up to n positions from 0 to duration, in order, from r.
func randomPositions(r *rand.Rand, duration time.Duration, n int) []time.Duration {
	var out []time.Duration
	for range r.IntN(n + 1) {
		out = append(out, time.Duration(r.Int64N(int64(duration)+1)))
	}
	slices.Sort(out)
	return out
}

// TestPlannerMatchesPlanWindows plans sources of random durations, silences, and overlaps, from
// a fixed seed, as streams advanced in random steps, or to each window's limit and just past it:
// the windows a Planner returns, as each is
// fixed, are PlanWindows' windows exactly, and each is returned as soon as the stream passes its
// limit.
func TestPlannerMatchesPlanWindows(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for range 2000 {
		duration := time.Duration(1 + r.Int64N(int64(5*time.Minute)))
		var silences []audio.Span
		for range r.IntN(40) {
			start := time.Duration(r.Int64N(int64(duration))) - time.Second
			silences = append(silences, audio.Span{Start: start, End: start + time.Duration(r.Int64N(int64(3*time.Second)))})
		}
		req := audio.Request{Overlap: time.Duration(r.Int64N(int64(audio.WindowLimit / 2)))}
		want, err := audio.PlanWindows(duration, silences, req)
		if err != nil {
			t.Fatal(err)
		}
		positions := randomPositions(r, duration, 30)
		if r.IntN(2) == 0 {
			// The positions at each window's limit and just past it, where it is fixed.
			positions = nil
			for _, w := range want.Windows {
				if limit := w.Start + audio.WindowLimit; limit < duration {
					positions = append(positions, limit, limit+1)
				}
			}
		}
		if got := planIncrementally(t, duration, silences, positions, req, want); !reflect.DeepEqual(got, want.Windows) {
			t.Fatalf("duration %v, overlap %v, silences %v, positions %v: windows %v, want %v",
				duration, req.Overlap, silences, positions, got, want.Windows)
		}
	}
}

// TestPlannerFixtures plans each level-change variant from the tracker's silences, as a stream fed
// in chunks: the Planner advanced to the tracker's Position after each chunk returns PlanWindows'
// windows of the same silences, each as soon as it is fixed.
func TestPlannerFixtures(t *testing.T) {
	var f levelsFixture
	readTruth(t, levelsTruth, &f)
	r := rand.New(rand.NewPCG(7, 8))
	for _, v := range f.Variants {
		pcm := samplesOf(t, v.File)
		for _, chunk := range []int{1 << 10, 1 << 16, 3 * 1 << 18} {
			tr, err := audio.NewTracker(audio.Request{})
			if err != nil {
				t.Fatal(err)
			}
			p, err := audio.NewPlanner(audio.Request{})
			if err != nil {
				t.Fatal(err)
			}
			var silences []audio.Span
			var got []audio.Window
			var fixedAt []time.Duration
			for rest := pcm; len(rest) > 0; {
				n := min(chunk+r.IntN(chunk), len(rest))
				for _, s := range tr.Feed(rest[:n]) {
					silences = append(silences, s.Span)
					if err := p.Silence(s.Span); err != nil {
						t.Fatal(err)
					}
				}
				rest = rest[n:]
				ws, err := p.Advance(tr.Position())
				if err != nil {
					t.Fatal(err)
				}
				for range ws {
					fixedAt = append(fixedAt, tr.Position())
				}
				got = append(got, ws...)
			}
			for _, s := range tr.End() {
				silences = append(silences, s.Span)
				if err := p.Silence(s.Span); err != nil {
					t.Fatal(err)
				}
			}
			ws, err := p.End(tr.Duration())
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, ws...)
			want, err := audio.PlanWindows(tr.Duration(), silences, audio.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want.Windows) {
				t.Errorf("%s in chunks of %d: windows %v, want %v", v.File, chunk, got, want.Windows)
			}
			for i, at := range fixedAt {
				if limit := got[i].Start + audio.WindowLimit; at <= limit {
					t.Errorf("%s: window %d returned at %v, before its limit, %v", v.File, i, at, limit)
				}
			}
		}
	}
}

func TestPlannerRefuses(t *testing.T) {
	if _, err := audio.NewPlanner(audio.Request{Overlap: audio.WindowLimit / 2}); !errors.Is(err, audio.ErrRequest) {
		t.Errorf("an overlap of half a window: %v, want ErrRequest", err)
	}
	p, err := audio.NewPlanner(audio.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Overlap() != audio.DefaultOverlap {
		t.Errorf("overlap %v, want the default, %v", p.Overlap(), audio.DefaultOverlap)
	}
	first, err := p.Advance(40 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if want := []audio.Window{win(0, 30, audio.CutLimit)}; !reflect.DeepEqual(first, want) {
		t.Errorf("advanced past the first limit: windows %v, want %v", first, want)
	}
	// Clipped to 0 and empty, a silence before the position is dropped, not refused.
	if err := p.Silence(span(-2, -1)); err != nil {
		t.Errorf("an empty silence: %v", err)
	}
	for name, call := range map[string]func() error{
		"a silence before the position": func() error { return p.Silence(span(39, 41)) },
		"a position before it":          func() error { _, err := p.Advance(39 * time.Second); return err },
		"an end before it":              func() error { _, err := p.End(39 * time.Second); return err },
		"an end at zero":                func() error { _, err := p.End(0); return err },
	} {
		if err := call(); !errors.Is(err, audio.ErrRequest) {
			t.Errorf("%s: %v, want ErrRequest", name, err)
		}
	}
	if err := p.Silence(span(40, 41)); err != nil {
		t.Errorf("a silence at the position: %v", err)
	}
	ws, err := p.End(40 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if want := []audio.Window{win(28, 40, audio.CutEnd)}; !reflect.DeepEqual(ws, want) {
		t.Errorf("windows %v, want %v", ws, want)
	}
	for name, call := range map[string]func() error{
		"a silence after the end": func() error { return p.Silence(span(41, 42)) },
		"an advance after it":     func() error { _, err := p.Advance(50 * time.Second); return err },
		"an end after it":         func() error { _, err := p.End(50 * time.Second); return err },
	} {
		if err := call(); !errors.Is(err, audio.ErrRequest) {
			t.Errorf("%s: %v, want ErrRequest", name, err)
		}
	}
}
