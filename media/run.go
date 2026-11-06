//go:build linux

package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// Events returns the stream of the events of the source req names: one ffmpeg run reads it,
// and each window of its audio and each frame of its video it takes is yielded as an Event as soon
// as it is complete. The stream is lazy: nothing runs, not even the request's validation, until it
// is ranged over.
//
// Ranging over it validates req, an invalid one an error wrapping ErrRequest. It probes the file
// through ffprobe for its first audio stream and first video stream, an attached picture not
// counted, and refuses one with neither, with ErrNoMedia, before ffmpeg runs. Then one ffmpeg run
// reads the file, at real time when req.Source.Paced says, and writes:
//
//   - its audio stream, resampled to 16 kHz, audio.SampleRate, downmixed to mono, as 16-bit PCM on
//     its stdout, as audio's tracker decodes it. A Tracker of req's settings is fed the PCM as it
//     comes and a Planner of req's Overlap its silences and position, so a window is cut as soon
//     as the stream passes its limit, WindowLimit from its start, or ends. Each window's samples
//     are the stream's from audio.Sample of its start up to but not including its end's, written
//     through audio.WAV; a window is the same bytes audio's Splitter.Windows cuts from a plan
//     made with DetectorTracker under the same fields. No silencedetect runs.
//   - the frames of its video stream that select takes: the first, each whose scene score is
//     past req.SceneThreshold, and each req.MaxGap or more after the last taken. Each is scaled to
//     fit req.MaxSide, when set, converted to RGB, and written as PNG into a hidden directory the
//     run makes in the file's directory, the run's working directory, through the image2 muxer's
//     atomic writing, so a PNG is whole once it has its name.
//   - each taken frame's time and scene score to its stderr, through the metadata filter, which
//     the stream reads as they come. A frame's reason is ReasonFirst for the first; ReasonScene
//     for one whose score is past the threshold; and ReasonInterval for the others, taken for the
//     gap. A frame both past the threshold and past the gap is a scene change.
//
// Windows are yielded in plan order and frames in source-time order, each as soon as it is
// complete: a window once fixed and written, a frame once its PNG is whole. The two interleave as
// they complete. A source with no video stream yields windows only, and one with no audio stream
// frames only. Each window and frame is written to the media directory as a hidden temporary file
// and renamed to its SHA-256 name just before it is yielded, so the media directory holds only
// whole files, and only the ones the stream yielded.
//
// A refused request or a failed probe yields one error with a zero Event, and the stream ends.
// When ctx ends, ffmpeg is stopped, through either runner, and the stream yields ctx's error once,
// with a zero Event, and ends. Breaking out of the range stops the run, and the loop returns once
// it has ended. A run that fails, or a window or frame the stream can't write, ends the stream
// with its error after the events already complete. In each case the windows and frames not
// yielded are removed, and so is the run's directory of frames.
func (s *Streamer) Events(ctx context.Context, req Request) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		c, err := req.check()
		if err != nil {
			yield(Event{}, err)
			return
		}
		if err := ctx.Err(); err != nil {
			yield(Event{}, err)
			return
		}
		st, err := s.probe(ctx, c.path)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				err = cerr
			}
			yield(Event{}, err)
			return
		}
		s.run(ctx, c, st, yield)
	}
}

// framePattern is the name the image2 muxer gives each frame in the run's directory, numbered from
// 0 in the order the frames are taken.
const framePattern = "frame-%08d.png"

// args returns ffmpeg's arguments for a run over the file of c with the streams st, writing its
// frames into scratch: one input, read at real time when paced, and an output for each stream
// st has, the audio's PCM to stdout and the video's frames to PNG files.
func args(c checked, st streams, scratch string) []string {
	a := []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "info"}
	// A decoder's frame threads hold back its output by a frame a thread: measured on the
	// fixture, at real time, 3.3 s of FLAC and about 1 s of video. The audio decodes in one
	// thread, which is cheap; a paced source's video decodes in slice threads, which add no
	// delay, and an unpaced one's keeps frame threads, for throughput. Threads change no output.
	a = append(a, "-threads:a", "1")
	if c.paced {
		a = append(a, "-readrate", "1", "-thread_type:v", "slice")
	}
	// Bit-exact decoding, so the same source gives the same samples and pixels whatever the
	// build's CPU paths, through either runner.
	a = append(a, "-flags:a", "+bitexact", "-flags:v", "+bitexact", "-i", c.path)
	if st.audio >= 0 {
		// audio's tracker decodes with these arguments, which audio's windows share.
		a = append(a,
			"-map", "0:"+strconv.Itoa(st.audio),
			"-af", fmt.Sprintf("aresample=%d", audio.SampleRate),
			"-ac", "1", "-ar", strconv.Itoa(audio.SampleRate), "-c:a", "pcm_s16le",
			"-flags:a", "+bitexact", "-fflags", "+bitexact", "-f", "s16le", "pipe:1",
		)
	}
	if st.video >= 0 {
		a = append(a,
			"-map", "0:"+strconv.Itoa(st.video),
			"-vf", videoFilter(c),
			// One encoder thread: with frame threads, the PNG encoder holds a frame until the
			// frames after it fill its threads, which a paced source's next scene may be minutes
			// away.
			"-fps_mode", "passthrough", "-c:v", "png", "-threads:v", "1", "-flags:v", "+bitexact", "-fflags", "+bitexact",
			"-f", "image2", "-start_number", "0", "-atomic_writing", "1",
			filepath.Join(scratch, framePattern),
		)
	}
	return a
}

// videoFilter returns the run's video filter chain. select takes the first frame, each whose scene
// score is past the threshold, and each the gap or more after the last it took; settb puts the
// frames' timestamps in microseconds, so the report's pts is the frame's time exactly; metadata
// prints each taken frame's pts and scene score to stderr; scale fits the frame to the maximum
// side, never enlarging it, bit-exact, and format makes it RGB for the PNG.
func videoFilter(c checked) string {
	sel := fmt.Sprintf("select='eq(n,0)+gt(scene,%s)+gte(t-prev_selected_t,%s)'",
		strconv.FormatFloat(c.scene, 'f', -1, 64), strconv.FormatFloat(c.gap.Seconds(), 'f', -1, 64))
	scale := "scale=flags=bicubic+accurate_rnd+full_chroma_int+bitexact"
	if c.maxSide > 0 {
		side := strconv.Itoa(c.maxSide)
		scale = "scale=w='min(iw," + side + ")':h='min(ih," + side + ")':force_original_aspect_ratio=decrease:" +
			"flags=bicubic+accurate_rnd+full_chroma_int+bitexact"
	}
	return sel + ",settb=AVTB,metadata=mode=print:key=lavfi.scene_score," + scale + ",format=rgb24"
}

// item is one event the run hands the stream: the event, its Path empty, and the temporary file in
// the media directory holding its window or frame.
type item struct {
	event Event
	tmp   string
}

// run reads the source of c, its streams st, through one ffmpeg run, yielding each event as it
// completes and then the run's error, if any. It returns once the run and its goroutines have
// ended, and every file it didn't yield is removed.
//
// The run's goroutine runs ffmpeg; the PCM on its stdout is cut into windows by an audioWriter
// as it is written, and the frames reported on its stderr are announced to a frameWatcher, whose
// goroutine waits for each frame's PNG and writes it into the media directory. Each hands its
// items to one channel, which the stream reads; a writer that waits on it holds ffmpeg back, so a
// slow consumer paces the run.
func (s *Streamer) run(ctx context.Context, c checked, st streams, yield func(Event, error) bool) {
	scratch, err := os.MkdirTemp(filepath.Dir(c.path), ".media-*")
	if err != nil {
		yield(Event{}, fmt.Errorf("media: the run's directory of frames: %w", err))
		return
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	work, cancel := context.WithCancel(ctx)
	items := make(chan item)
	r := &runner{work: work, cancel: cancel, items: items, dir: c.dir}
	var aw *audioWriter
	if st.audio >= 0 {
		// The request was checked: neither refuses it.
		planner, _ := audio.NewPlanner(c.audio)
		tracker, _ := audio.NewTracker(c.audio)
		aw = &audioWriter{r: r, tracker: tracker, planner: planner, overlap: planner.Overlap(), fixed: c.fixed, threshold: c.audio.Threshold}
	}
	fw := &frameWatcher{r: r, scratch: scratch, scene: c.scene, gap: c.gap, wake: make(chan struct{}, 1), ended: make(chan struct{})}
	var wg sync.WaitGroup
	if st.video >= 0 {
		wg.Go(fw.watch)
	} else {
		// No video, no frames to watch for.
		close(fw.ended)
	}
	go func() {
		defer close(items)
		err := s.ffmpeg.Run(work, tool.Invocation{
			Args:   args(c, st, scratch),
			Dir:    filepath.Dir(c.path),
			Stdout: aw.writer(),
			Stderr: fw.stderr(),
		})
		fw.lines.flush()
		if err == nil && fw.lines.err != nil {
			err = fmt.Errorf("its report of a frame: %w", fw.lines.err)
		}
		if err != nil {
			if last := fw.lines.last; last != "" {
				err = fmt.Errorf("%w: %s", err, last)
			}
			r.fail(fmt.Errorf("media: ffmpeg reading %s: %w", c.path, err))
		} else if aw != nil {
			aw.end()
		}
		if st.video >= 0 {
			fw.end(err == nil)
		}
		wg.Wait()
	}()
	defer func() {
		cancel()
		// Every goroutine sends its items or removes them once work has ended, and the channel
		// closes once they have all ended; an item still on its way is removed here.
		for it := range items {
			removeTemp(it.tmp)
		}
	}()
	for {
		var it item
		var ok bool
		select {
		case it, ok = <-items:
		case <-ctx.Done():
		}
		if err := ctx.Err(); err != nil {
			if ok {
				removeTemp(it.tmp)
			}
			yield(Event{}, err)
			return
		}
		if !ok {
			break
		}
		if err := r.publish(&it); err != nil {
			yield(Event{}, err)
			return
		}
		if !yield(it.event, nil) {
			return
		}
	}
	if err := r.err(); err != nil {
		yield(Event{}, err)
	}
}

// runner is what a run's writers share: the run's context, ended by a failure or the stream's end,
// the channel they hand items to, the media directory, and the run's first failure.
type runner struct {
	work   context.Context
	cancel context.CancelFunc
	items  chan<- item
	dir    string

	mu      sync.Mutex
	failure error
}

// send hands it to the stream, or removes its file and reports false once the run has ended.
func (r *runner) send(it item) bool {
	select {
	case r.items <- it:
		return true
	case <-r.work.Done():
		removeTemp(it.tmp)
		return false
	}
}

// fail records the run's first failure and ends the run, stopping ffmpeg.
func (r *runner) fail(err error) {
	r.mu.Lock()
	if r.failure == nil {
		r.failure = err
	}
	r.mu.Unlock()
	r.cancel()
}

// err returns the run's first failure.
func (r *runner) err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failure
}

// publish renames it's temporary file to its SHA-256 name in the media directory and sets its
// event's Path.
func (r *runner) publish(it *item) error {
	var path *string
	var name string
	switch {
	case it.event.Window != nil:
		path, name = &it.event.Window.Path, it.event.Window.SHA256+".wav"
	case it.event.Frame != nil:
		path, name = &it.event.Frame.Path, it.event.Frame.SHA256+".png"
	}
	full := filepath.Join(r.dir, name)
	if err := os.Rename(it.tmp, full); err != nil {
		removeTemp(it.tmp)
		return fmt.Errorf("media: %s: %w", name, err)
	}
	*path = full
	return nil
}

// writeTemp writes b to a new hidden temporary file in dir, named with ext, and returns its path;
// a failed write removes the file.
func writeTemp(dir, ext string, b []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".media-*"+ext)
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		removeTemp(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// removeTemp removes a temporary file, if there is one. The file is the package's own, so a
// failure to remove it leaves a hidden file and nothing else.
func removeTemp(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// arrival is when the stream's bytes up to end, counted from its start, arrived.
type arrival struct {
	end int64
	at  time.Time
}

// audioWriter cuts the PCM ffmpeg writes to its stdout into windows as it comes: it feeds a
// Tracker, gives the silences it finds and its position to a Planner, and writes each window the
// Planner fixes. It keeps the PCM from the start of the window not yet fixed on, and when each
// part of it arrived. Its writes never fail; a window it can't write fails the run.
type audioWriter struct {
	r         *runner
	tracker   *audio.Tracker
	planner   *audio.Planner
	overlap   time.Duration
	fixed     bool
	threshold float64

	// pcm holds the stream's bytes from base, a sample index, on; received is the bytes received,
	// and arrivals when the bytes from base on arrived.
	pcm      []byte
	base     int64
	received int64
	arrivals []arrival
	// silences are the silences found that begin at the start of the window not yet fixed or later.
	silences []audio.Silence
	index    int
	done     bool
}

// writer returns w as ffmpeg's stdout; nil, discarding it, when w is nil, as for a source with no
// audio.
func (w *audioWriter) writer() io.Writer {
	if w == nil {
		return nil
	}
	return w
}

func (w *audioWriter) Write(p []byte) (int, error) {
	if w.done {
		return len(p), nil
	}
	w.pcm = append(w.pcm, p...)
	w.received += int64(len(p))
	w.arrivals = append(w.arrivals, arrival{end: w.received, at: time.Now()})
	found := w.tracker.Feed(p)
	w.silences = append(w.silences, found...)
	if err := w.give(found); err != nil {
		w.stop(err)
		return len(p), nil
	}
	windows, err := w.planner.Advance(w.tracker.Position())
	if err != nil {
		w.stop(err)
		return len(p), nil
	}
	w.cut(windows)
	return len(p), nil
}

// give gives the planner silences.
func (w *audioWriter) give(silences []audio.Silence) error {
	for _, s := range silences {
		if err := w.planner.Silence(s.Span); err != nil {
			return err
		}
	}
	return nil
}

// stop fails the run with err and takes nothing more.
func (w *audioWriter) stop(err error) {
	w.done = true
	w.r.fail(fmt.Errorf("media: planning the windows: %w", err))
}

// end ends the stream once ffmpeg has exited: the tracker's last silences and the planner's last
// windows. A stream with no samples is an error, since its audio stream gave none.
func (w *audioWriter) end() {
	if w.done {
		return
	}
	if w.tracker.Duration() == 0 {
		w.stop(errors.New("ffmpeg wrote no samples of the audio stream"))
		return
	}
	last := w.tracker.End()
	w.silences = append(w.silences, last...)
	if err := w.give(last); err != nil {
		w.stop(err)
		return
	}
	windows, err := w.planner.End(w.tracker.Duration())
	if err != nil {
		w.stop(err)
		return
	}
	w.cut(windows)
	w.done = true
}

// cut writes windows, in order, and hands each to the stream, dropping the PCM and silences
// behind the next window's start.
func (w *audioWriter) cut(windows []audio.Window) {
	for _, win := range windows {
		if w.done {
			return
		}
		first, end := audio.Sample(win.Start), audio.Sample(win.End)
		data := w.pcm[2*(first-w.base) : 2*(end-w.base)]
		wav := audio.WAV(data)
		sum := sha256.Sum256(wav)
		ev := &Window{
			Index: w.index, Span: win.Span, Cut: win.Cut,
			Samples: len(data) / 2, Threshold: w.cutThreshold(win), SHA256: hex.EncodeToString(sum[:]),
		}
		if w.index > 0 {
			ev.SeamBefore = audio.Span{Start: win.Start, End: win.Start + w.overlap}
		}
		if win.Cut != audio.CutEnd {
			ev.SeamAfter = audio.Span{Start: win.End - w.overlap, End: win.End}
		}
		arrived := w.arrivedBy(2 * end)
		tmp, err := writeTemp(w.r.dir, ".wav", wav)
		if err != nil {
			w.stop(fmt.Errorf("window %d, %v: %w", w.index, win.Span, err))
			return
		}
		if !w.r.send(item{event: Event{Window: ev, Time: win.Start, Arrived: arrived}, tmp: tmp}) {
			w.done = true
			return
		}
		w.index++
		if win.Cut != audio.CutEnd {
			w.drop(win.End - w.overlap)
		}
	}
}

// cutThreshold returns the threshold win was cut under: the fixed threshold; or, with a rolling
// one, the threshold of the silence whose middle, taken up to the window's limit as the planner
// takes it, is the window's end; or zero for a window no silence ended.
func (w *audioWriter) cutThreshold(win audio.Window) float64 {
	if w.fixed {
		return w.threshold
	}
	if win.Cut != audio.CutSilence {
		return 0
	}
	limit := win.Start + audio.WindowLimit
	for _, s := range slices.Backward(w.silences) {
		if s.Start < win.Start || s.Start >= limit {
			continue
		}
		if e := min(s.End, limit); s.Start+(e-s.Start)/2 == win.End {
			return s.Threshold
		}
	}
	return 0
}

// arrivedBy returns when the stream's first n bytes had all arrived.
func (w *audioWriter) arrivedBy(n int64) time.Time {
	for _, a := range w.arrivals {
		if a.end >= n {
			return a.at
		}
	}
	return w.arrivals[len(w.arrivals)-1].at
}

// drop forgets the PCM, arrivals, and silences before start, the next window's.
func (w *audioWriter) drop(start time.Duration) {
	first := audio.Sample(start)
	w.pcm = append(w.pcm[:0], w.pcm[2*(first-w.base):]...)
	w.base = first
	i := 0
	for i < len(w.arrivals) && w.arrivals[i].end <= 2*first {
		i++
	}
	w.arrivals = append(w.arrivals[:0], w.arrivals[i:]...)
	j := 0
	for j < len(w.silences) && w.silences[j].Start < start {
		j++
	}
	w.silences = append(w.silences[:0], w.silences[j:]...)
}

// announced is a frame ffmpeg reported on its stderr, its PNG not yet seen whole.
type announced struct {
	index   int
	reason  Reason
	time    time.Duration
	arrived time.Time
}

// frameReport and sceneScore match the metadata filter's report of a frame: its line of the
// frame's count and pts, in microseconds after settb, and then its line of the scene score.
var (
	frameReport = regexp.MustCompile(`^\[Parsed_metadata_\d+ @ [^\]]*\] frame:\s*\d+\s+pts:\s*(-?\d+)\s`)
	sceneScore  = regexp.MustCompile(`^\[Parsed_metadata_\d+ @ [^\]]*\] lavfi\.scene_score=(\S+)$`)
)

// frameWatcher reads ffmpeg's stderr for the frames it takes, and waits for each one's PNG in the
// run's directory, scratch, writing it into the media directory and handing it to the stream in
// the order taken.
type frameWatcher struct {
	r       *runner
	scratch string
	scene   float64
	gap     time.Duration
	lines   lineWriter

	// pts is the pts of the frame whose score line is next, -1 when none; count the frames
	// announced, and previous the last one's time. They are the stderr goroutine's alone.
	pts      int64
	count    int
	previous time.Duration

	mu      sync.Mutex
	pending []announced
	ok      bool
	wake    chan struct{}
	ended   chan struct{}
}

// stderr returns the writer ffmpeg's stderr goes to, which reads the frames' reports.
func (f *frameWatcher) stderr() *lineWriter {
	f.pts = -1
	f.lines.line = f.line
	return &f.lines
}

// line reads one line of ffmpeg's stderr: a frame's pts, or its score, which announces it.
func (f *frameWatcher) line(s string) error {
	if m := frameReport.FindStringSubmatch(s); m != nil {
		pts, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return err
		}
		f.pts = pts
		return nil
	}
	m := sceneScore.FindStringSubmatch(s)
	if m == nil || f.pts < 0 {
		return nil
	}
	score, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return err
	}
	t := time.Duration(f.pts) * time.Microsecond
	f.pts = -1
	a := announced{index: f.count, time: t, arrived: time.Now()}
	switch {
	case f.count == 0:
		a.reason = ReasonFirst
	case score > f.scene:
		a.reason = ReasonScene
	case t-f.previous >= f.gap:
		a.reason = ReasonInterval
	default:
		// select took it past the threshold, which the score's six printed decimals round away.
		a.reason = ReasonScene
	}
	f.count++
	f.previous = t
	f.mu.Lock()
	f.pending = append(f.pending, a)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
	return nil
}

// end tells the watcher ffmpeg has exited, ok when it succeeded: every frame it announced has its
// PNG, and one that hasn't is an error. After a failed run, the frames whose PNG is whole are
// still yielded, and the rest dropped.
func (f *frameWatcher) end(ok bool) {
	f.mu.Lock()
	f.ok = ok
	f.mu.Unlock()
	close(f.ended)
}

// pollInterval is how often the watcher looks for a frame's PNG while ffmpeg writes it.
const pollInterval = 10 * time.Millisecond

// watch waits for each announced frame's PNG in turn and hands it to the stream, until ffmpeg has
// exited and every frame is handed over, or the run ends.
func (f *frameWatcher) watch() {
	for {
		f.mu.Lock()
		next, have := announced{}, len(f.pending) > 0
		if have {
			next = f.pending[0]
		}
		f.mu.Unlock()
		ended := false
		select {
		case <-f.ended:
			ended = true
		default:
		}
		if !have {
			if ended {
				return
			}
			select {
			case <-f.wake:
			case <-f.ended:
			case <-f.r.work.Done():
				return
			}
			continue
		}
		path := filepath.Join(f.scratch, fmt.Sprintf(framePattern, next.index))
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			_ = os.Remove(path)
			if !f.handOver(next, b) {
				return
			}
			f.mu.Lock()
			f.pending = f.pending[1:]
			f.mu.Unlock()
			continue
		case !errors.Is(err, os.ErrNotExist):
			f.r.fail(fmt.Errorf("media: frame %d: %w", next.index, err))
			return
		case ended:
			f.mu.Lock()
			ok := f.ok
			f.mu.Unlock()
			if ok {
				f.r.fail(fmt.Errorf("media: ffmpeg wrote no PNG for frame %d, at %v", next.index, next.time))
			}
			return
		}
		select {
		case <-time.After(pollInterval):
		case <-f.ended:
		case <-f.r.work.Done():
			return
		}
	}
}

// handOver writes a's PNG, b, into the media directory and hands it to the stream; false when the
// run has ended.
func (f *frameWatcher) handOver(a announced, b []byte) bool {
	width, height, err := pngSize(b)
	if err != nil {
		f.r.fail(fmt.Errorf("media: frame %d, at %v: %w", a.index, a.time, err))
		return false
	}
	sum := sha256.Sum256(b)
	tmp, err := writeTemp(f.r.dir, ".png", b)
	if err != nil {
		f.r.fail(fmt.Errorf("media: frame %d, at %v: %w", a.index, a.time, err))
		return false
	}
	fr := &Frame{Index: a.index, Reason: a.reason, Width: width, Height: height, SHA256: hex.EncodeToString(sum[:])}
	return f.r.send(item{event: Event{Frame: fr, Time: a.time, Arrived: a.arrived}, tmp: tmp})
}

// pngSignature begins every PNG.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// pngSize returns a PNG's width and height, from its IHDR chunk, which follows its signature.
func pngSize(b []byte) (width, height int, err error) {
	if len(b) < 24 || !bytes.Equal(b[:8], pngSignature) || string(b[12:16]) != "IHDR" {
		return 0, 0, errors.New("ffmpeg's frame isn't a PNG")
	}
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), nil
}

// maxLine is the longest stderr line a lineWriter passes whole; a longer one is cut there.
const maxLine = 4 << 10

// lineWriter splits what is written to it into lines, passing each to line, without its newline,
// and keeping the last that isn't empty. Once line returns an error, it keeps the error and passes
// nothing more. Its memory is one line, at most maxLine.
type lineWriter struct {
	line    func(string) error
	partial []byte
	last    string
	err     error
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexAny(p, "\r\n")
		if i < 0 {
			w.partial = append(w.partial, p[:min(len(p), maxLine-len(w.partial))]...)
			break
		}
		w.partial = append(w.partial, p[:min(i, maxLine-len(w.partial))]...)
		w.emit()
		p = p[i+1:]
	}
	return n, nil
}

// flush passes the line left without a newline, if any.
func (w *lineWriter) flush() {
	if len(w.partial) > 0 {
		w.emit()
	}
}

// emit passes the partial line to line and empties it.
func (w *lineWriter) emit() {
	s := string(bytes.TrimSpace(w.partial))
	w.partial = w.partial[:0]
	if s == "" {
		return
	}
	w.last = s
	if w.err == nil && w.line != nil {
		w.err = w.line(s)
	}
}
