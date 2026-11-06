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
// PlanWindows is a Planner given the silences, clipped and in order, and then the source's end,
// so a stream planned as it arrives gets the same windows.
//
// A duration of zero or less, or an Overlap CheckOverlap refuses, is an error wrapping
// ErrRequest.
func PlanWindows(duration time.Duration, silences []Span, req Request) (Plan, error) {
	if duration <= 0 {
		return Plan{}, fmt.Errorf("audio: duration %v isn't positive: %w", duration, ErrRequest)
	}
	p, err := NewPlanner(req)
	if err != nil {
		return Plan{}, err
	}
	for _, s := range clipSilences(duration, silences) {
		// Clipped to the source and in order, from a position of 0: never refused.
		_ = p.Silence(s)
	}
	windows, err := p.End(duration)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Duration: duration, Overlap: p.overlap, Windows: windows}
	for i, w := range windows[1:] {
		plan.Seams = append(plan.Seams, Span{w.Start, windows[i].End})
	}
	return plan, nil
}

// Planner plans a stream's windows as it arrives, as PlanWindows plans a source's: it is given the
// silences as they are found and the stream's position as it advances, and returns each window as
// soon as it is fixed, once the stream has passed the window's limit, WindowLimit from its start,
// or the stream has ended. Given the same silences and the same duration, in any chunking, a
// Planner returns PlanWindows' windows exactly: they are one policy, and PlanWindows is a Planner
// given everything at once.
//
// A silence still open, one the stream is in, is given by Open: its start and how far it has
// lasted. Since a window takes a silence that crosses its limit only up to the limit, a silence
// open past a window's limit ends the window where the closed silence would, however long it
// lasts, so the window is fixed once the stream passes its limit, inside the silence or not.
//
// A window's seams are the next window's start to its end; a Planner returns windows alone, and
// window i's seam with window i+1 is Span{Start: windows[i+1].Start, End: windows[i].End}.
//
// A Planner keeps only the silences that can still end a window: those that begin at the current
// window's start or later. It isn't safe for concurrent use.
type Planner struct {
	overlap time.Duration
	// start is the start of the window not yet fixed, and windows the windows fixed before it.
	start   time.Duration
	windows int
	// pos is the stream's settled position: every silence that begins before it has been given.
	// reach is the stream's position, pos or, while a silence is open from pos, how far it has
	// lasted; open is whether one is.
	pos   time.Duration
	reach time.Duration
	open  bool
	// silences are the silences given that begin at start or later, sorted by start and then end.
	silences []Span
	ended    bool
}

// NewPlanner returns a Planner that plans with req's Overlap, as PlanWindows does; it reads no
// other field. An Overlap CheckOverlap refuses is an error wrapping ErrRequest.
func NewPlanner(req Request) (*Planner, error) {
	overlap, err := CheckOverlap(req.Overlap)
	if err != nil {
		return nil, err
	}
	return &Planner{overlap: overlap}, nil
}

// Overlap returns the overlap the planner plans with, its request's Overlap or DefaultOverlap.
func (p *Planner) Overlap() time.Duration { return p.overlap }

// Silence gives the planner a silence of the stream. A silence before 0 is clipped to it, and an
// empty one is dropped; silences may overlap, and come in any order but one: each must begin at the
// stream's position or after, since the windows before it are planned from the silences already
// given. While a silence is open, the only one that may begin before the stream's position is the
// open one, closed: it begins at the open one's start and lasts at least as far, and giving it
// closes the open one. A silence that begins before the position, one that begins inside the open
// silence and isn't it, or one given after End, is an error wrapping ErrRequest.
func (p *Planner) Silence(s Span) error {
	if p.ended {
		return fmt.Errorf("audio: a silence after the stream's end: %w", ErrRequest)
	}
	s.Start = max(s.Start, 0)
	if s.Start >= s.End {
		return nil
	}
	if s.Start < p.pos {
		return fmt.Errorf("audio: silence %v begins before the stream's position, %v: %w", s, p.pos, ErrRequest)
	}
	if p.open && s.Start < p.reach {
		if s.Start != p.pos || s.End < p.reach {
			return fmt.Errorf("audio: silence %v isn't the one open from %v to %v: %w", s, p.pos, p.reach, ErrRequest)
		}
		p.open = false
	}
	i, _ := slices.BinarySearchFunc(p.silences, s, compareSpans)
	p.silences = slices.Insert(p.silences, i, s)
	return nil
}

// Open moves the stream's position into a silence still open: s.Start is where it began, and s.End
// how far it has lasted, the stream's position. It promises that every silence that begins before
// s.Start has been given, that no other begins from s.Start to s.End, and that the open one is a
// silence, as long as a silence must be, which Silence will give once it closes, lasting at least
// to s.End. It returns the windows that fixes, in order: each window whose limit is before s.End,
// the silence taken up to the limit, as PlanWindows takes the closed one.
//
// Open may be called again as the silence lasts, with the same start. Before the next Advance or
// End, Silence must give the open one, closed. A silence that begins before 0 is clipped to it. An
// empty one, a start before the stream's position or a start other than the open one's, an end
// before the stream's position, or an Open after End, is an error wrapping ErrRequest.
func (p *Planner) Open(s Span) ([]Window, error) {
	s.Start = max(s.Start, 0)
	switch {
	case p.ended:
		return nil, fmt.Errorf("audio: an open silence after the stream's end: %w", ErrRequest)
	case s.Start >= s.End:
		return nil, fmt.Errorf("audio: open silence %v is empty: %w", s, ErrRequest)
	case s.Start < p.pos:
		return nil, fmt.Errorf("audio: open silence %v begins before the stream's position, %v: %w", s, p.pos, ErrRequest)
	case p.open && s.Start != p.pos:
		return nil, fmt.Errorf("audio: open silence %v while the one from %v is open: %w", s, p.pos, ErrRequest)
	case s.End < p.reach:
		return nil, fmt.Errorf("audio: open silence %v ends before the stream's position, %v: %w", s, p.reach, ErrRequest)
	}
	p.pos, p.reach, p.open = s.Start, s.End, true
	return p.fix(0, false), nil
}

// Advance moves the stream's position to pos, which promises that every silence that begins
// before pos has been given, and returns the windows that fixes, in order: each window whose
// limit, WindowLimit from its start, is before pos. A pos before the position, an Advance while a
// silence is open and not yet given closed, or an Advance after End, is an error wrapping
// ErrRequest.
func (p *Planner) Advance(pos time.Duration) ([]Window, error) {
	switch {
	case p.ended:
		return nil, fmt.Errorf("audio: an advance after the stream's end: %w", ErrRequest)
	case p.open:
		return nil, fmt.Errorf("audio: an advance with the silence from %v open: %w", p.pos, ErrRequest)
	case pos < p.reach:
		return nil, fmt.Errorf("audio: position %v is before the stream's, %v: %w", pos, p.reach, ErrRequest)
	}
	p.pos, p.reach = pos, pos
	return p.fix(0, false), nil
}

// End ends the stream at duration, its length, which promises that every silence of the stream
// has been given, and returns the windows left, in order, the last cut at the end, CutEnd. After
// End, the planner takes nothing more. A duration of zero or less, or before the position, a
// silence open and not yet given closed, or an End after End, is an error wrapping ErrRequest.
func (p *Planner) End(duration time.Duration) ([]Window, error) {
	switch {
	case p.ended:
		return nil, fmt.Errorf("audio: the stream's end after its end: %w", ErrRequest)
	case duration <= 0:
		return nil, fmt.Errorf("audio: duration %v isn't positive: %w", duration, ErrRequest)
	case p.open:
		return nil, fmt.Errorf("audio: the stream's end with the silence from %v open: %w", p.pos, ErrRequest)
	case duration < p.reach:
		return nil, fmt.Errorf("audio: duration %v is before the stream's position, %v: %w", duration, p.reach, ErrRequest)
	}
	p.pos, p.reach = duration, duration
	return p.fix(duration, true), nil
}

// fix returns the windows the position fixes, from the window not yet fixed on: while the stream
// has passed the window's limit, or, once ended at duration, until the window that reaches it.
// A window that doesn't reach the end ends where PlanWindows says; a silence past duration, which
// the planner doesn't clip, begins past every limit fix reads, so it ends no window. An open
// silence is a silence to the stream's position, past every limit fix reads: taken up to a limit,
// it is the closed silence taken up to it.
func (p *Planner) fix(duration time.Duration, ended bool) []Window {
	var out []Window
	for !p.ended {
		limit := p.start + WindowLimit
		if ended && limit >= duration {
			out = append(out, Window{Span: Span{p.start, duration}, Cut: CutEnd})
			p.ended, p.silences = true, nil
			break
		}
		if !ended && p.reach <= limit {
			break
		}
		// The window's own audio begins after its leading seam, if it has one, and must reach
		// past its end less Overlap, the seam after it.
		own := p.start
		if p.windows > 0 {
			own += p.overlap
		}
		w := Window{Span: Span{p.start, limit}, Cut: CutLimit}
		end, ok := latestMiddle(p.silences, p.start, limit, own+p.overlap)
		if p.open {
			if e, o := latestMiddle([]Span{{p.pos, p.reach}}, p.start, limit, own+p.overlap); o && (!ok || e > end) {
				end, ok = e, true
			}
		}
		if ok {
			w = Window{Span: Span{p.start, end}, Cut: CutSilence}
		}
		out = append(out, w)
		p.windows++
		p.start = w.End - p.overlap
		// A silence that begins before the next window's start is behind it and ends no window.
		i := 0
		for i < len(p.silences) && p.silences[i].Start < p.start {
			i++
		}
		p.silences = p.silences[i:]
	}
	return out
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
	slices.SortFunc(out, compareSpans)
	return out
}

// compareSpans orders spans by start and then end.
func compareSpans(a, b Span) int {
	return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End))
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
