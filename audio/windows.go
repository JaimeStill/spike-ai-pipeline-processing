//go:build linux

package audio

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// SampleRate is the windows' sample rate, 16 kHz, the rate a speech model's audio input takes.
// Every window is mono 16-bit PCM at this rate, whatever the source's rate, channels, and format.
const SampleRate = 16000

// wavHeader is the length of a window's WAV header, the canonical 44 bytes: a RIFF chunk holding
// a 16-byte fmt chunk and the data chunk, and nothing else.
const wavHeader = 44

// WindowsRequest is one source's windows, cut as a Plan of it says, through a Splitter's Windows.
type WindowsRequest struct {
	// Path is the source's file, the one the plan was made of, which is read and never changed.
	// It is required; ffmpeg reads it by its absolute path, in its own directory, as Detect does.
	Path string
	// Dir is the media directory the windows are written into and left, which the caller owns. It
	// is required, and must be a directory; an empty Dir is refused rather than taken as the
	// system's temporary directory.
	Dir string
	// Concurrency is how many windows are cut at once, each an ffmpeg run. Zero means 1; a
	// negative concurrency is refused.
	Concurrency int
}

// WindowFile is one window of a plan, written to the media directory as WAV: 16 kHz, SampleRate,
// mono 16-bit PCM, its header the canonical 44 bytes, carrying no encoder's name, so the same
// window of the same source is the same bytes through either runner.
type WindowFile struct {
	// Index is the window's index in the plan's Windows, from 0; the plan's Seams[Index-1] is the
	// seam it starts in, and Seams[Index] the one it ends in.
	Index int
	// Count is the number of windows in the plan. The zero WindowFile, which a stream yields with
	// an error that ends it, has a Count of zero.
	Count int
	// Window is the plan's window: its span of the source and what ended it.
	Window Window
	// Path is the WAV's path in the media directory, named by SHA256 and ".wav"; empty for a
	// window that failed.
	Path string
	// SHA256 is the lowercase hex SHA-256 of the WAV's bytes, header included.
	SHA256 string
	// Samples is the number of samples the WAV holds, at SampleRate.
	Samples int
}

// Duration returns the WAV's duration: its Samples at SampleRate.
func (w WindowFile) Duration() time.Duration {
	return time.Duration(w.Samples) * time.Second / SampleRate
}

// Windows returns the stream of the windows plan gives the source req.Path names, each cut by
// ffmpeg and written into req.Dir as a WindowFile. The stream is lazy: nothing runs, not even the
// request's validation, until it is ranged over.
//
// Ranging over it validates req and plan, an invalid one an error wrapping ErrRequest: a plan with
// no windows, or a window outside its Duration. It probes the source, as Probe does, and refuses
// one whose duration isn't plan.Duration, with ErrRequest, as a plan of another source. Then, for
// each window in order, one ffmpeg run reads the audio stream Probe found, resamples it to
// SampleRate, downmixes it to mono, and trims it to the window's samples: from its Start, to the
// nearest sample at SampleRate, up to but not including its End's, so a window is its span long to
// within a sample. ffmpeg writes the samples as raw 16-bit PCM to its stdout, read in memory, at
// most a window's, 960,000 bytes for one of WindowLimit; the package writes the WAV header and
// names the file by the SHA-256 of the whole. A WAV ffmpeg muxed itself would need a seekable
// output to set its sizes, and the container runner mounts only the source's directory, so ffmpeg
// writes nothing to the media directory. At most req.Concurrency windows are being cut at once.
// Windows are yielded in plan order, a window that finishes early held until the windows before it
// are yielded.
//
// A window is written to a hidden temporary file in req.Dir and renamed to its SHA-256 name just
// before it is yielded, so the media directory holds only whole windows, and only the ones the
// stream yielded: a window cut but never yielded, because the loop stopped or ctx ended first, is
// removed, as is a failed window's partial file.
//
// A window that fails, in its ffmpeg run or in writing its file, yields the WindowFile's Index,
// Count, and Window with the error, which names the window and wraps its cause, and the stream
// carries on. A refused request or plan, or a failed probe, yields one error with a zero
// WindowFile, and the stream ends. When ctx ends, the runs in flight are stopped, and the stream
// yields ctx's error once, with a zero WindowFile, and ends.
//
// Breaking out of the range cancels the runs in flight, and the loop returns once they have
// ended and their files are removed.
func (s *Splitter) Windows(ctx context.Context, req WindowsRequest, plan Plan) iter.Seq2[WindowFile, error] {
	return func(yield func(WindowFile, error) bool) {
		if err := req.check(plan); err != nil {
			yield(WindowFile{}, err)
			return
		}
		if err := ctx.Err(); err != nil {
			yield(WindowFile{}, err)
			return
		}
		src, err := s.probe(ctx, req.Path)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				err = cerr
			}
			yield(WindowFile{}, err)
			return
		}
		if src.Duration != plan.Duration {
			yield(WindowFile{}, fmt.Errorf("audio: %s is %v long, and the plan is of a source %v long: %w",
				req.Path, src.Duration, plan.Duration, ErrRequest))
			return
		}
		s.windows(ctx, req, plan, src, yield)
	}
}

// check validates req and plan and sets req's defaults: its Path absolute, and a concurrency of 1
// for zero. An invalid one is an error wrapping ErrRequest.
func (req *WindowsRequest) check(plan Plan) error {
	switch {
	case req.Path == "":
		return fmt.Errorf("audio: no source path: %w", ErrRequest)
	case req.Dir == "":
		return fmt.Errorf("audio: request has no media directory: %w", ErrRequest)
	case req.Concurrency < 0:
		return fmt.Errorf("audio: request's concurrency %d is negative: %w", req.Concurrency, ErrRequest)
	case len(plan.Windows) == 0:
		return fmt.Errorf("audio: the plan has no windows: %w", ErrRequest)
	}
	for i, w := range plan.Windows {
		if w.Start < 0 || w.Start >= w.End || w.End > plan.Duration {
			return fmt.Errorf("audio: the plan's window %d, %v, isn't within 0 to %v: %w", i, w.Span, plan.Duration, ErrRequest)
		}
	}
	path, err := filepath.Abs(req.Path)
	if err != nil {
		return fmt.Errorf("audio: source path %s: %w: %w", req.Path, ErrRequest, err)
	}
	req.Path = path
	info, err := os.Stat(req.Dir)
	if err != nil {
		return fmt.Errorf("audio: media directory: %w: %w", ErrRequest, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("audio: media directory %s isn't a directory: %w", req.Dir, ErrRequest)
	}
	if req.Concurrency == 0 {
		req.Concurrency = 1
	}
	return nil
}

// cut is one window's outcome, as its worker hands it to the stream: the WindowFile, its Path
// empty, the temporary file holding the WAV, and the window's error.
type cut struct {
	file WindowFile
	tmp  string
	err  error
}

// windows cuts the plan's windows of the source src, req already checked, yielding each in plan
// order. It returns once every window's run has ended and every window it didn't yield is
// removed.
//
// A dispatcher starts a worker per window, in plan order, each holding one of req.Concurrency
// slots for its ffmpeg run and the writing of its temporary file; each worker hands its outcome
// to a channel of its window's own, buffered so it never waits on the consumer. The consumer reads
// the channels in plan order, so a later window that finishes first waits in its channel, its slot
// already freed, as document's pages do.
func (s *Splitter) windows(ctx context.Context, req WindowsRequest, plan Plan, src Source, yield func(WindowFile, error) bool) {
	n := len(plan.Windows)
	work, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	done := make([]chan cut, n)
	for i := range done {
		done[i] = make(chan cut, 1)
	}
	next := 0
	defer func() {
		cancel()
		wg.Wait()
		// Every worker started has handed its outcome over by now; the ones the stream didn't
		// take are removed.
		for _, ch := range done[next:] {
			select {
			case c := <-ch:
				removeTemp(c.tmp)
			default:
			}
		}
	}()
	slots := make(chan struct{}, req.Concurrency)
	wg.Go(func() {
		for i := range n {
			select {
			case slots <- struct{}{}:
			case <-work.Done():
				return
			}
			wg.Go(func() {
				defer func() { <-slots }()
				file := WindowFile{Index: i, Count: n, Window: plan.Windows[i]}
				tmp, err := s.window(work, req, src, &file)
				done[i] <- cut{file: file, tmp: tmp, err: err}
			})
		}
	})
	for next < n {
		var c cut
		select {
		case c = <-done[next]:
			// Taken, so the deferred drain doesn't take it again.
			next++
		case <-ctx.Done():
		}
		// A window that failed because ctx ended is ctx's error, not the window's.
		if err := ctx.Err(); err != nil {
			removeTemp(c.tmp)
			yield(WindowFile{}, err)
			return
		}
		if c.err == nil {
			path := filepath.Join(req.Dir, c.file.SHA256+".wav")
			if err := os.Rename(c.tmp, path); err != nil {
				removeTemp(c.tmp)
				c.err = fmt.Errorf("audio: window %d of %d, %v: %w", c.file.Index, n, c.file.Window.Span, err)
			} else {
				c.file.Path = path
			}
		}
		if c.err != nil {
			c.file.SHA256, c.file.Samples = "", 0
		}
		if !yield(c.file, c.err) {
			return
		}
	}
}

// removeTemp removes a window's temporary file, if it has one. The file is the package's own, in
// the caller's directory, so a failure to remove it leaves a hidden file and nothing else.
func removeTemp(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// window cuts file's window of the source src through ffmpeg and writes it as WAV to a new
// temporary file in req.Dir, setting file's SHA256 and Samples; it returns the temporary file's
// path. Its error names the window; a failed window leaves no file.
func (s *Splitter) window(ctx context.Context, req WindowsRequest, src Source, file *WindowFile) (string, error) {
	w := file.Window
	first, end := Sample(w.Start), Sample(w.End)
	want := int(end - first)
	fail := func(err error) (string, error) {
		return "", fmt.Errorf("audio: window %d of %d, %v: %w", file.Index, file.Count, w.Span, err)
	}
	// One byte past the window's samples is room enough to see that ffmpeg wrote too many.
	pcm := &capped{limit: 2*want + 1}
	stderr := &lineWriter{line: func(string) error { return nil }}
	err := s.ffmpeg.Run(ctx, tool.Invocation{
		Args: []string{
			"-nostdin", "-hide_banner", "-nostats", "-loglevel", "error",
			"-flags:a", "+bitexact", "-i", req.Path, "-map", "0:" + strconv.Itoa(src.Stream),
			"-af", fmt.Sprintf("aresample=%d,atrim=start_sample=%d:end_sample=%d", SampleRate, first, end),
			"-ac", "1", "-ar", strconv.Itoa(SampleRate), "-c:a", "pcm_s16le",
			"-flags:a", "+bitexact", "-fflags", "+bitexact", "-f", "s16le", "-",
		},
		Dir:    filepath.Dir(req.Path),
		Stdout: pcm,
		Stderr: stderr,
	})
	stderr.flush()
	if err != nil {
		if stderr.last != "" {
			return fail(fmt.Errorf("ffmpeg: %w: %s", err, stderr.last))
		}
		return fail(fmt.Errorf("ffmpeg: %w", err))
	}
	data := pcm.b
	switch {
	case len(data) == 0:
		return fail(errors.New("ffmpeg wrote no samples"))
	case len(data)%2 != 0:
		return fail(fmt.Errorf("ffmpeg wrote %d bytes, not whole 16-bit samples", len(data)))
	case len(data) > 2*want:
		return fail(fmt.Errorf("ffmpeg wrote more than the window's %d samples", want))
	}
	wav := WAV(data)
	sum := sha256.Sum256(wav)
	tmp, err := writeTemp(req.Dir, wav)
	if err != nil {
		return fail(err)
	}
	file.SHA256, file.Samples = hex.EncodeToString(sum[:]), len(data)/2
	return tmp, nil
}

// capped is a writer that keeps the first limit bytes written to it, allocated as they come, and
// discards the rest; its writes never fail.
type capped struct {
	b     []byte
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

// Sample returns the index of the sample at SampleRate nearest d, without overflow for any
// Duration: a window of span s holds the samples from Sample(s.Start) up to but not including
// Sample(s.End).
func Sample(d time.Duration) int64 {
	return int64(d/time.Second)*SampleRate + (int64(d%time.Second)*SampleRate+int64(time.Second)/2)/int64(time.Second)
}

// WAV returns pcm, mono 16-bit little-endian samples at SampleRate, as a WAV file: the canonical
// 44-byte header, a RIFF chunk holding a 16-byte PCM fmt chunk and the data chunk, then the
// samples. It is the header ffmpeg's own WAV muxer writes with -bitexact, which leaves out its
// encoder tag, and every window is written through it, so the same samples are the same bytes
// whichever package cut them.
func WAV(pcm []byte) []byte {
	const (
		channels   = 1
		bits       = 16
		blockAlign = channels * bits / 8
	)
	b := make([]byte, 0, wavHeader+len(pcm))
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(wavHeader-8+len(pcm)))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, channels)
	b = binary.LittleEndian.AppendUint32(b, SampleRate)
	b = binary.LittleEndian.AppendUint32(b, SampleRate*blockAlign)
	b = binary.LittleEndian.AppendUint16(b, blockAlign)
	b = binary.LittleEndian.AppendUint16(b, bits)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(pcm)))
	return append(b, pcm...)
}

// writeTemp writes b to a new hidden temporary file in dir and returns its path; a failed write
// removes the file.
func writeTemp(dir string, b []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".window-*.wav")
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
