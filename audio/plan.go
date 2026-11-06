//go:build linux

package audio

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// WindowLimit is the longest a window may be, its overlaps included: 30 s, the span a speech
// model's audio input takes whole.
const WindowLimit = 30 * time.Second

// DefaultOverlap is the overlap a request's zero Overlap asks for: 2 s, which the next window
// starts before the previous one's end, so a word a cut lands on is whole in one of the two.
const DefaultOverlap = 2 * time.Second

// Span is a range of the source, from Start to End, each an offset from the source's start.
type Span struct {
	Start time.Duration
	End   time.Duration
}

// Duration returns the span's length, End less Start.
func (s Span) Duration() time.Duration { return s.End - s.Start }

// String returns the span in seconds to the millisecond, as in "22.979s-49.820s".
func (s Span) String() string {
	return fmt.Sprintf("%.3fs-%.3fs", s.Start.Seconds(), s.End.Seconds())
}

// Cut is what ends a window.
type Cut int

const (
	// CutSilence is a window that ends at the middle of the latest silence its range allowed.
	CutSilence Cut = iota + 1
	// CutLimit is a window that ends at WindowLimit from its start, its range holding no silence
	// it could end at.
	CutLimit
	// CutEnd is the last window, which ends at the source's end.
	CutEnd
)

// String returns the cut's name: silence, limit, or end.
func (c Cut) String() string {
	switch c {
	case CutSilence:
		return "silence"
	case CutLimit:
		return "limit"
	case CutEnd:
		return "end"
	}
	return fmt.Sprintf("Cut(%d)", int(c))
}

// Window is one window of a plan: the span of the source it covers and what ended it.
type Window struct {
	Span
	Cut Cut
}

// Plan is a source's windows, in order, and the seams between them.
type Plan struct {
	// Duration is the source's duration, which the windows cover.
	Duration time.Duration
	// Overlap is the overlap the plan was made with: each window after the first starts Overlap
	// before the previous one's end.
	Overlap time.Duration
	// Windows cover the source from 0 to Duration in order, each at most WindowLimit long.
	Windows []Window
	// Seams are the ranges consecutive windows share: Seams[i] is the end of Windows[i] and the
	// start of Windows[i+1], Overlap long. One window has none. Seams never overlap one another,
	// so no part of the source is in more than two windows.
	Seams []Span
}

// CheckOverlap returns the overlap a request's Overlap asks for, DefaultOverlap when it is zero,
// or an error wrapping ErrRequest when it is below zero or WindowLimit/2 or more. Under half the
// limit, a window cut at the limit still has audio of its own between its two seams.
func CheckOverlap(overlap time.Duration) (time.Duration, error) {
	o := cmp.Or(overlap, DefaultOverlap)
	if o < 0 || o >= WindowLimit/2 {
		return 0, fmt.Errorf("audio: overlap %v isn't from 0 to under %v: %w", overlap, WindowLimit/2, ErrRequest)
	}
	return o, nil
}

// PlanWindows plans the windows of a source duration long whose silences are silences, with
// req's Overlap; it reads nothing else of req, and runs nothing, so the same arguments give the
// same plan. Silences may come in any order and may overlap or reach past the source; each is
// clipped to it.
//
// The first window starts at 0. A window whose start is less than WindowLimit from the source's
// end ends there, CutEnd. Any other window ends at the middle of the latest silence in its range,
// its start to WindowLimit after it, CutSilence; or, with no such silence, at the limit, CutLimit.
// The next window starts Overlap before that end. A silence that crosses the limit is taken up to
// the limit, its middle the middle of that part; one that began before the window's start is
// behind the window and never ends it, so a silence longer than a window doesn't cut the windows
// after the one it ends ever shorter. A silence is a window's end only when the window keeps audio
// of its own beyond its seams: its end is more than Overlap past its start, for the first window,
// and more than twice Overlap past it for a later one, whose start is in the seam before it. A
// silence at the source's very start thus never ends a window, and seams never overlap one
// another; a tail shorter than Overlap past the last cut is a last window of its own, the seam and
// the tail.
//
// A duration of zero or less, or an Overlap CheckOverlap refuses, is an error wrapping
// ErrRequest.
func PlanWindows(duration time.Duration, silences []Span, req Request) (Plan, error) {
	if duration <= 0 {
		return Plan{}, fmt.Errorf("audio: duration %v isn't positive: %w", duration, ErrRequest)
	}
	overlap, err := CheckOverlap(req.Overlap)
	if err != nil {
		return Plan{}, err
	}
	sorted := clipSilences(duration, silences)
	plan := Plan{Duration: duration, Overlap: overlap}
	start := time.Duration(0)
	for {
		limit := start + WindowLimit
		if limit >= duration {
			plan.Windows = append(plan.Windows, Window{Span: Span{start, duration}, Cut: CutEnd})
			return plan, nil
		}
		// The window's own audio begins after its leading seam, if it has one, and must reach
		// past its end less Overlap, the seam after it.
		own := start
		if len(plan.Windows) > 0 {
			own += overlap
		}
		w := Window{Span: Span{start, limit}, Cut: CutLimit}
		if end, ok := latestMiddle(sorted, start, limit, own+overlap); ok {
			w = Window{Span: Span{start, end}, Cut: CutSilence}
		}
		plan.Windows = append(plan.Windows, w)
		start = w.End - overlap
		plan.Seams = append(plan.Seams, Span{start, w.End})
	}
}

// clipSilences returns silences clipped to the source, 0 to duration, the empty ones dropped,
// sorted by start and then end.
func clipSilences(duration time.Duration, silences []Span) []Span {
	var out []Span
	for _, s := range silences {
		s = Span{max(s.Start, 0), min(s.End, duration)}
		if s.Start < s.End {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Span) int {
		return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End))
	})
	return out
}

// latestMiddle returns the latest middle past after of the sorted silences that begin at start or
// later, each taken up to limit; ok is false when no silence's is.
func latestMiddle(sorted []Span, start, limit, after time.Duration) (end time.Duration, ok bool) {
	for _, s := range sorted {
		if s.Start < start {
			continue
		}
		if s.Start >= limit {
			break
		}
		s.End = min(s.End, limit)
		if m := s.Start + s.Duration()/2; m > after && (!ok || m > end) {
			end, ok = m, true
		}
	}
	return end, ok
}
