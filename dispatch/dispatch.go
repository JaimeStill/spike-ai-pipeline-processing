package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// Outcome names what became of a part, as the package doc explains.
type Outcome string

const (
	// Prepared is a part its stage prepared.
	Prepared Outcome = "prepared"
	// NoProcessor is a part whose route has no stage registered, or whose stage has no
	// processor for its kind yet.
	NoProcessor Outcome = "no processor yet"
	// Seen is content the index already holds: the event carries its recorded entry and flags.
	Seen Outcome = "seen"
	// Cycle is a part whose SHA-256 is one of its ancestors': it is neither prepared nor
	// recursed into.
	Cycle Outcome = "cycle"
	// Quarantined is a part its inspection's verdict quarantines: no stage runs, its content is
	// kept, and its flags hold the evidence.
	Quarantined Outcome = "quarantined"
	// Encrypted is a part its stage can't read because it is encrypted.
	Encrypted Outcome = "encrypted"
	// Failed is a part that couldn't be read, routed, inspected, or prepared: the event's Err
	// says why.
	Failed Outcome = "failed"
	// Limited is a part past a cap the caller set: it is neither inspected nor prepared, and
	// its siblings go on.
	Limited Outcome = "limited"
)

// Event is what the dispatcher reports of one part: an output its stage prepared, as the stage
// yields it, or, once its outcome is known, the part's own event.
//
// An output event has Output set: the part it belongs to, as far as it is known, the identity of
// its stage, and the output. The dispatcher yields each as its stage yields it, among the events
// of the parts the stage finds, in the stage's order, and holds nothing of it once the caller's
// loop body returns, but for what it appends to the part's pending entry in the index: the output
// as the body left it, so a caller that persists it sets its Ref, as ev.Output.Ref = key, and the
// index records the Ref. A [Seen] part's recorded outputs are output events too, streamed from
// the index before its own event, each with its Outcome [Seen] and the entry's in Recorded, the
// Ref the caller set when it was prepared on it; a prepared output's event has no Outcome.
//
// A part's own event has Output nil, and follows its outputs' events and every part found in it:
// the part, its outcome and why, the error a [Failed] part came to, the identity of the stage its
// route has, if any, how many outputs it yielded, and how many parts it found, each of whose own
// events came before it; the dispatcher holds none of them, appending each, as a [Child], to the
// part's pending entry in the index as its event is done. A [Seen] event carries the entry the
// index recorded, its outcome in Recorded and its counts; the index's Children reads its
// children back, streamed.
//
// The caller's loop owns the event until its body returns. It may call [Event.Keep] to keep the
// event's files, which the dispatcher otherwise removes once the part's own event is done.
type Event struct {
	Part     Part
	Output   *Output
	Outcome  Outcome
	Reason   string
	Err      error
	Stage    Identity
	Recorded Outcome
	Outputs  int
	Children int
	keep     *bool
}

// Keep keeps the event's files. Called in an output event, it keeps the output's file, and the
// index records its path; called in a part's own event, it keeps the part's file and its working
// directory, the files of all its outputs among them, though the index records the path only of
// an output kept in its own event. The caller then owns them. A file the caller moved away is not
// missed: the dispatcher's removal ignores it.
func (e Event) Keep() {
	if e.keep != nil {
		*e.keep = true
	}
}

// Limits are the caps a caller may set on one dispatch, each off when zero: none applies by
// default. A part past one is [Limited], and its siblings go on.
type Limits struct {
	// Parts is the most parts one dispatch admits, the input among them.
	Parts int
	// Bytes is the most bytes the parts one dispatch admits may come to, their sizes summed.
	Bytes int64
	// Depth is the deepest part one dispatch admits, the input at depth 0.
	Depth int
}

// Config configures a [Dispatcher].
type Config struct {
	// Stages are the stages, by route.
	Stages Stages
	// Index records what is prepared; it is required, and dispatchers that share one share
	// their work.
	Index Index
	// Dir is the working directory the dispatcher makes each part's own directory in, which it
	// gives the part's stage and removes once the caller is done with the part; it must exist.
	Dir string
	// Limits are the caller's caps; none by default.
	Limits Limits
	// Inspect tunes every part's inspection.
	Inspect inspect.Options
}

// Dispatcher dispatches inputs to their stages. It is safe for concurrent use, each Dispatch
// independent but for the index they share.
type Dispatcher struct {
	cfg Config
}

// New returns a Dispatcher over cfg, refusing one with no index or working directory, or a
// stage that is nil.
func New(cfg Config) (*Dispatcher, error) {
	if cfg.Index == nil {
		return nil, errors.New("dispatch: no index")
	}
	if cfg.Dir == "" {
		return nil, errors.New("dispatch: no working directory")
	}
	if info, err := os.Stat(cfg.Dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("dispatch: working directory %q isn't a directory", cfg.Dir)
	}
	for r, s := range cfg.Stages {
		if s == nil {
			return nil, fmt.Errorf("dispatch: route %s: nil stage", r)
		}
	}
	if cfg.Limits.Parts < 0 || cfg.Limits.Bytes < 0 || cfg.Limits.Depth < 0 {
		return nil, errors.New("dispatch: negative limit")
	}
	// A copy, so the caller's later change to its map changes nothing.
	cfg.Stages = maps.Clone(cfg.Stages)
	return &Dispatcher{cfg: cfg}, nil
}

// Input is the file a dispatch begins at: its path, the name it was given, which routes it,
// filepath.Base(Path) when empty, and its stored MIME type, if it has one. The dispatcher never
// removes it.
//
// After, when not zero, resumes the input's read after the message whose store id it is, as
// email.Request's After does: the input's [Part] carries it to its stage, and [EmailStage] reads
// a .pst or an mbox from the message after it; every other stage, and every part found below the
// input, reads whole. A resumed input's own entry is never recorded, its outputs being the
// read's tail alone, so a later dispatch of the same content prepares it whole; the parts found
// in it are recorded as any part is. The package doc's "Resuming a store" says how a caller
// resumes after a crash.
type Input struct {
	Path  string
	Name  string
	MIME  string
	After uint64
}

// Dispatch dispatches in and every part its stages find in it, depth-first, yielding each output
// a stage prepares as its own event, as the stage yields it, and each part's event once its
// outcome is known: a part's event follows its outputs' and the events of every part found in it,
// so the input's is last. An error that stops the dispatch, as the input unreadable, the
// index failing, a file the dispatcher can't remove, or ctx ending, is yielded alone, last; a
// part that fails is a [Failed] event, and the dispatch goes on. A loop that stops early stops
// the stages, releases every claim, and removes the files the dispatch made.
func (d *Dispatcher) Dispatch(ctx context.Context, in Input) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		name := in.Name
		if name == "" {
			name = filepath.Base(in.Path)
		}
		r := &run{d: d, ctx: ctx, yield: yield, kept: map[string]bool{}, after: in.After, released: map[string]released{}}
		r.part(Found{Path: in.Path, Name: name, MIME: in.MIME}, "", 0)
	}
}

// run is one dispatch's state.
type run struct {
	d     *Dispatcher
	ctx   context.Context
	yield func(Event, error) bool
	// ancestors are the SHA-256s of the parts being prepared, the input first.
	ancestors []string
	// parts and bytes are what the dispatch has admitted, for its caps.
	parts int
	bytes int64
	// kept are the paths the caller kept, or that a quarantine keeps, which no cleanup removes.
	kept map[string]bool
	// stopped is set once the caller's loop stopped or a fatal error was yielded.
	stopped bool
	// after is the input's After, which its part alone carries.
	after uint64
	// released is the content whose found part's Release the dispatch called, by its SHA-256:
	// what a later part of the same content, found without a file, is dispatched as.
	released map[string]released
}

// released is what a released content's part was: its size, kind, and route, and why.
type released struct {
	size   int64
	kind   route.Kind
	route  route.Route
	reason string
}

// result is what a part came to, for its parent's entry: a reference to it, whether its whole
// subtree is complete, so its parent may be recorded, and whether it is settled: the index holds
// its content's entry under its stage, and the caller kept nothing of it, so a later part of the
// same content needs no file.
type result struct {
	child    Child
	complete bool
	settled  bool
}

// fatal yields err alone and stops the dispatch.
func (r *run) fatal(err error) result {
	if !r.stopped {
		r.stopped = true
		r.yield(Event{}, err)
	}
	return result{}
}

// emit yields ev, then removes the part's file and, given dir, the part's working directory,
// unless the caller kept them, and returns the part's result. record, when not nil, runs once the
// caller's loop body returns, before the removal, to record the entry, and reports whether the
// index holds the content's entry; its failure stops the dispatch once the removal is done.
func (r *run) emit(ev Event, dir string, complete bool, record func(Event) (bool, error)) result {
	var keep bool
	ev.keep = &keep
	if !r.yield(ev, nil) {
		r.stopped = true
	}
	res := result{child: Child{
		SHA256: ev.Part.SHA256, Name: ev.Part.Name, Where: ev.Part.Where, Size: ev.Part.Size,
		Route: ev.Part.Route, Outcome: ev.Outcome,
	}, complete: complete}
	var err error
	if record != nil {
		var held bool
		held, err = record(ev)
		res.settled = held && err == nil && !keep && ev.Outcome != Quarantined
	}
	if keep || ev.Outcome == Quarantined {
		r.kept[ev.Part.Path] = true
		if keep && dir != "" {
			r.kept[dir] = true
		}
	}
	// A failed record stops the dispatch, but the part's files are removed all the same.
	if rerr := r.remove(ev, dir); err == nil {
		err = rerr
	}
	if err != nil {
		return r.fatal(err)
	}
	return res
}

// remove removes ev's part's file, but for the input's and a kept one, and dir, but for the
// kept paths under it.
func (r *run) remove(ev Event, dir string) error {
	if ev.Part.Depth > 0 && ev.Part.Path != "" && !r.kept[ev.Part.Path] {
		if err := os.Remove(ev.Part.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("dispatch: remove %s: %w", ev.Part.Path, err)
		}
	}
	if dir == "" || r.kept[dir] {
		return nil
	}
	return r.clean(dir)
}

// clean removes dir and everything under it but the kept paths, and the directories that hold
// them.
func (r *run) clean(dir string) error {
	under := false
	for p := range r.kept {
		if strings.HasPrefix(p, dir+string(filepath.Separator)) {
			under = true
			break
		}
	}
	if !under {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("dispatch: remove %s: %w", dir, err)
		}
		return nil
	}
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case r.kept[p]:
			if e.IsDir() {
				return fs.SkipDir
			}
		case e.IsDir():
			dirs = append(dirs, p)
		default:
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("dispatch: clean %s: %w", dir, err)
	}
	// Deepest first; a directory still holding a kept path stays.
	for _, p := range slices.Backward(dirs) {
		_ = os.Remove(p)
	}
	return nil
}

// part dispatches one part, f, found in the part whose SHA-256 is parent, at depth, then, when
// it is settled and f's SHA-256 is its content's, calls f's Release, as [Found] says.
func (r *run) part(f Found, parent string, depth int) result {
	p := Part{Path: f.Path, Name: f.Name, MIME: f.MIME, Parent: parent, Depth: depth, Where: f.Where}
	if depth == 0 {
		p.After = r.after
	}
	res := r.dispatchPart(&p, f.SHA256)
	if res.settled && f.Release != nil && f.Path != "" && f.SHA256 == p.SHA256 && !r.stopped {
		r.released[p.SHA256] = released{size: p.Size, kind: p.Kind, route: p.Route, reason: p.Reason}
		f.Release()
	}
	return res
}

// dispatchPart dispatches p, as part says; sum is the SHA-256 its stage gave, which a part with
// no file is dispatched by.
func (r *run) dispatchPart(p *Part, sum string) result {
	if err := r.ctx.Err(); err != nil {
		return r.fatal(err)
	}
	failed := func(err error) result {
		return r.emit(Event{Part: *p, Outcome: Failed, Reason: err.Error(), Err: err}, "", false, nil)
	}

	if p.Path == "" {
		// A part found without a file: content whose earlier part's Release this dispatch called,
		// dispatched as that part was routed.
		c, ok := r.released[sum]
		if !ok {
			return failed(fmt.Errorf("dispatch: %s has no file, and its content is none this dispatch released", p.Name))
		}
		p.SHA256, p.Size, p.Kind, p.Route, p.Reason = sum, c.size, c.kind, c.route, c.reason
	} else {
		sum, size, head, err := hashFile(p.Path)
		if err != nil {
			if p.Depth == 0 {
				return r.fatal(err)
			}
			return failed(err)
		}
		p.SHA256, p.Size, p.Kind = sum, size, route.Identify(head)
		if p.Route, p.Reason, err = route.File(p.Path, p.Name, p.MIME); err != nil {
			return failed(fmt.Errorf("dispatch: route %s: %w", p.Name, err))
		}
	}

	if i := slices.Index(r.ancestors, p.SHA256); i >= 0 {
		why := fmt.Sprintf("same content as its ancestor at depth %d", i)
		return r.emit(Event{Part: *p, Outcome: Cycle, Reason: why}, "", true, nil)
	}
	if why := r.limit(*p); why != "" {
		return r.emit(Event{Part: *p, Outcome: Limited, Reason: why}, "", false, nil)
	}
	r.parts++
	r.bytes += p.Size

	stage := r.d.cfg.Stages[p.Route]
	switch {
	case p.Path == "":
		return r.releasedPart(*p, stage, failed)
	case stage == nil:
		return r.unstaged(*p, failed)
	}
	return r.staged(*p, stage, failed)
}

// limit returns the cap p is past, in a few words, or "" when it is under every cap.
func (r *run) limit(p Part) string {
	l := r.d.cfg.Limits
	switch {
	case l.Depth > 0 && p.Depth > l.Depth:
		return fmt.Sprintf("past the depth cap, %d", l.Depth)
	case l.Parts > 0 && r.parts >= l.Parts:
		return fmt.Sprintf("past the parts cap, %d", l.Parts)
	case l.Bytes > 0 && r.bytes+p.Size > l.Bytes:
		return fmt.Sprintf("past the bytes cap, %d", l.Bytes)
	}
	return ""
}

// inspect inspects p and records its flags in the index.
func (r *run) inspect(p *Part) (inspect.Report, error) {
	rep, err := inspect.Inspect(r.ctx, p.Path, p.Name, r.d.cfg.Inspect)
	if err != nil {
		return rep, err
	}
	p.Flags = rep.Flags
	if err := r.d.cfg.Index.RecordFlags(r.ctx, p.SHA256, rep.Flags); err != nil {
		return rep, &indexError{err}
	}
	return rep, nil
}

// indexError marks an index's failure, which stops the dispatch, from a part's own.
type indexError struct{ err error }

func (e *indexError) Error() string { return e.err.Error() }
func (e *indexError) Unwrap() error { return e.err }

// quarantined returns the event of a part its inspection quarantined.
func quarantined(p Part) Event {
	var unsafe []string
	for _, f := range p.Flags {
		if f.Class == inspect.Unsafe {
			unsafe = append(unsafe, string(f.Check)+": "+f.Evidence)
		}
	}
	why := "quarantined by policy"
	if len(unsafe) > 0 {
		why = strings.Join(unsafe, "; ")
	}
	return Event{Part: p, Outcome: Quarantined, Reason: why}
}

// unstaged dispatches p, whose route has no stage: it is inspected, and is quarantined or no
// processor yet. Nothing but its flags is recorded, so a stage registered later prepares it.
func (r *run) unstaged(p Part, failed func(error) result) result {
	rep, err := r.inspect(&p)
	if ie := (*indexError)(nil); errors.As(err, &ie) {
		return r.fatal(err)
	} else if err != nil {
		return failed(err)
	}
	if rep.Verdict == inspect.Quarantine {
		return r.emit(quarantined(p), "", true, nil)
	}
	why := fmt.Sprintf("no stage for route %s", p.Route)
	return r.emit(Event{Part: p, Outcome: NoProcessor, Reason: why}, "", true, nil)
}

// releasedPart dispatches p, a part with no file of content this dispatch released, to stage:
// it is seen, the index holding the content's entry under stage since the release.
func (r *run) releasedPart(p Part, stage Stage, failed func(error) result) result {
	if stage == nil {
		return failed(fmt.Errorf("dispatch: %s has no file, and its route %s no stage", p.Name, p.Route))
	}
	id := stage.Identity()
	key := Key{SHA256: p.SHA256, Stage: id}
	e, ok, err := r.d.cfg.Index.Lookup(r.ctx, key)
	if err != nil {
		return r.fatal(err)
	}
	if !ok {
		return failed(fmt.Errorf("dispatch: %s has no file, and the index no longer holds its content's entry", p.Name))
	}
	return r.seen(p, id, key, e)
}

// staged dispatches p to stage: it is seen when the index holds its entry, waits on another
// dispatch's claim, or claims it, inspects it, and prepares it.
func (r *run) staged(p Part, stage Stage, failed func(error) result) result {
	idx := r.d.cfg.Index
	id := stage.Identity()
	key := Key{SHA256: p.SHA256, Stage: id}
	var token string
	for {
		e, ok, err := idx.Lookup(r.ctx, key)
		if err != nil {
			return r.fatal(err)
		}
		if ok {
			return r.seen(p, id, key, e)
		}
		t, claimed, err := idx.Claim(r.ctx, key)
		if err != nil {
			return r.fatal(err)
		}
		if claimed {
			token = t
			break
		}
		if e, ok, err = idx.Wait(r.ctx, key); err != nil {
			return r.fatal(err)
		} else if ok {
			return r.seen(p, id, key, e)
		}
	}
	// Until the entry is recorded, any way out releases the claim, under a context of its own so
	// that a dispatch whose context ended still releases.
	recorded := false
	defer func() {
		if !recorded {
			_ = idx.Release(context.WithoutCancel(r.ctx), key, token)
		}
	}()

	rep, err := r.inspect(&p)
	if ie := (*indexError)(nil); errors.As(err, &ie) {
		return r.fatal(err)
	} else if err != nil {
		return failed(err)
	}
	if rep.Verdict == inspect.Quarantine {
		ev := quarantined(p)
		ev.Stage = id
		return r.emit(ev, "", true, nil)
	}

	dir, err := os.MkdirTemp(r.d.cfg.Dir, "part-")
	if err != nil {
		return r.fatal(fmt.Errorf("dispatch: %w", err))
	}
	// A resumed read's outputs are its tail alone, no entry of the content's: nothing is
	// appended.
	var add func(Output) error
	var addChild func(Child) error
	if p.After == 0 {
		add = func(o Output) error { return idx.Append(r.ctx, key, token, o) }
		addChild = func(c Child) error { return idx.AppendChild(r.ctx, key, token, c) }
	}
	ev := Event{Part: p, Stage: id}
	complete := true
	var stageErr error
	r.ancestors = append(r.ancestors, p.SHA256)
	for item, err := range stage.Prepare(r.ctx, p, dir) {
		if err != nil {
			stageErr = err
			break
		}
		switch {
		case item.Output != nil:
			r.output(p, id, *item.Output, add)
			ev.Outputs++
		case item.Part != nil:
			// Once the dispatch is stopped there is no parent event to report the child in.
			if res := r.part(*item.Part, p.SHA256, p.Depth+1); !r.stopped {
				ev.Children++
				complete = complete && res.complete
				if addChild != nil {
					if err := addChild(res.child); err != nil {
						r.fatal(err)
					}
				}
			}
		}
		if r.stopped {
			break
		}
	}
	r.ancestors = r.ancestors[:len(r.ancestors)-1]
	if r.stopped {
		_ = r.clean(dir)
		return result{}
	}
	if err := r.ctx.Err(); err != nil {
		// The stage failed, if it did, because the dispatch's context ended: that is no failure
		// of the part's.
		_ = r.clean(dir)
		return r.fatal(err)
	}

	switch {
	case stageErr == nil:
		ev.Outcome = Prepared
	case errors.Is(stageErr, ErrNoProcessor):
		ev.Outcome, ev.Reason = NoProcessor, stageErr.Error()
	case errors.Is(stageErr, ErrEncrypted), encrypted(p.Flags):
		ev.Outcome, ev.Reason, ev.Err = Encrypted, stageErr.Error(), stageErr
	default:
		ev.Outcome, ev.Reason, ev.Err, complete = Failed, stageErr.Error(), stageErr, false
	}
	record := func(ev Event) (bool, error) {
		if !complete || p.After != 0 {
			return false, nil
		}
		e := Entry{Outcome: ev.Outcome, Reason: ev.Reason, Outputs: ev.Outputs, Children: ev.Children}
		if err := idx.Record(context.WithoutCancel(r.ctx), key, token, e); err != nil {
			return false, err
		}
		recorded = true
		return true, nil
	}
	return r.emit(ev, dir, complete, record)
}

// encrypted reports whether flags hold a check that marks content encrypted.
func encrypted(flags []inspect.Flag) bool {
	return slices.ContainsFunc(flags, func(f inspect.Flag) bool {
		return f.Check == inspect.PDFEncrypt || f.Check == inspect.ZIPEncrypted || f.Check == inspect.OOXMLEncrypted
	})
}

// output yields o as an output event of p's, prepared by the stage id names, then, once the
// caller's loop body returns, appends it to p's pending entry by add, when not nil, as the body
// left it, its Path cleared unless the body kept it. An append that fails stops the dispatch.
func (r *run) output(p Part, id Identity, o Output, add func(Output) error) {
	r.yieldOutput(Event{Part: p, Stage: id, Output: &o}, add)
}

// yieldOutput yields ev, an output event, as output says.
func (r *run) yieldOutput(ev Event, add func(Output) error) {
	var keep bool
	ev.keep = &keep
	if !r.yield(ev, nil) {
		r.stopped = true
		return
	}
	rec := *ev.Output
	if keep && rec.Path != "" {
		r.kept[rec.Path] = true
	} else {
		rec.Path = ""
	}
	if add == nil {
		return
	}
	if err := add(rec); err != nil {
		r.fatal(err)
	}
}

// seen emits p's event as content the index holds under key, e its entry under id, its flags the
// index's: each recorded output an output event, streamed from the index, then p's own, which
// counts the entry's children, never reading them.
func (r *run) seen(p Part, id Identity, key Key, e Entry) result {
	idx := r.d.cfg.Index
	flags, _, err := idx.Flags(r.ctx, p.SHA256)
	if err != nil {
		return r.fatal(err)
	}
	p.Flags = flags
	n := 0
	for o, err := range idx.Outputs(r.ctx, key) {
		if err != nil {
			return r.fatal(err)
		}
		if r.yieldOutput(Event{Part: p, Stage: id, Output: &o, Outcome: Seen, Recorded: e.Outcome}, nil); r.stopped {
			return result{}
		}
		n++
	}
	ev := Event{
		Part: p, Outcome: Seen, Reason: "recorded by " + id.String(), Err: nil, Stage: id,
		Recorded: e.Outcome, Outputs: n, Children: e.Children,
	}
	return r.emit(ev, "", true, func(Event) (bool, error) { return true, nil })
}

// hashFile returns the lowercase hex SHA-256 of the file at path, streamed, its size, and its
// first route.HeadLen bytes.
func hashFile(path string) (string, int64, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, nil, fmt.Errorf("dispatch: %w", err)
	}
	defer func() { _ = f.Close() }() // read only: a close error loses nothing
	h := sha256.New()
	head := &headWriter{max: route.HeadLen}
	n, err := io.Copy(io.MultiWriter(h, head), f)
	if err != nil {
		return "", 0, nil, fmt.Errorf("dispatch: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, head.b, nil
}

// headWriter keeps the first max bytes written to it.
type headWriter struct {
	b   []byte
	max int
}

func (w *headWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.b); room > 0 {
		w.b = append(w.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
