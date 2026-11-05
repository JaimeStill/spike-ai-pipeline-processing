//go:build linux

package audio_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// flacPlan is the plan TestPlanStandin makes of flacProbe's source: two windows, cut in the
// silence at 24.2 s.
func flacPlan() audio.Plan {
	return audio.Plan{
		Duration: 49820 * time.Millisecond,
		Overlap:  audio.DefaultOverlap,
		Windows: []audio.Window{
			{Start: 0, End: 24979187500 * time.Nanosecond, Cut: audio.CutSilence},
			{Start: 22979187500 * time.Nanosecond, End: 49820 * time.Millisecond, Cut: audio.CutEnd},
		},
		Seams: []audio.Span{{Start: 22979187500 * time.Nanosecond, End: 24979187500 * time.Nanosecond}},
	}
}

// limitPlan plans a source of n windows cut at the limit, and sets the stand-in ffprobe to report
// a source that long, its audio the first stream.
func limitPlan(t *testing.T, n int) audio.Plan {
	t.Helper()
	duration := time.Duration(n-1)*(audio.WindowLimit-audio.DefaultOverlap) + 10*time.Second
	plan, err := audio.PlanWindows(duration, nil, audio.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Windows) != n {
		t.Fatalf("%d windows planned, want %d", len(plan.Windows), n)
	}
	t.Setenv(probeEnv, fmt.Sprintf(`{"streams":[{"index":0,"codec_name":"flac","codec_type":"audio","sample_rate":"16000","channels":1}],"format":{"duration":"%.6f"}}`,
		duration.Seconds()))
	return plan
}

// first returns the first sample of w at 16 kHz, rounded, as the stand-in reads it.
func first(w audio.Window) int {
	return int((w.Start*audio.SampleRate + time.Second/2) / time.Second)
}

// end returns the sample w ends before at 16 kHz, rounded.
func end(w audio.Window) int {
	return int((w.End*audio.SampleRate + time.Second/2) / time.Second)
}

// collect ranges over the windows stream, failing the test on an error.
func collect(t *testing.T, s *audio.Splitter, ctx context.Context, req audio.WindowsRequest, plan audio.Plan) []audio.WindowFile {
	t.Helper()
	var out []audio.WindowFile
	for w, err := range s.Windows(ctx, req, plan) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, w)
	}
	return out
}

// checkWindow checks that w is window i of plan and that its file is the stand-in's samples of
// it, as WAV, named by its SHA-256 in dir.
func checkWindow(t *testing.T, w audio.WindowFile, i int, plan audio.Plan, dir string) {
	t.Helper()
	if w.Index != i || w.Count != len(plan.Windows) || w.Window != plan.Windows[i] {
		t.Errorf("window %d of %d, %v; want %d of %d, %v", w.Index, w.Count, w.Window, i, len(plan.Windows), plan.Windows[i])
	}
	b, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != w.SHA256 || w.Path != filepath.Join(dir, got+".wav") {
		t.Errorf("window %d at %s, SHA-256 %s; its bytes' SHA-256 is %s, in %s", i, w.Path, w.SHA256, got, dir)
	}
	a, z := first(plan.Windows[i]), end(plan.Windows[i])
	if w.Samples != z-a {
		t.Errorf("window %d holds %d samples, want %d", i, w.Samples, z-a)
	}
	var want []byte
	for j := a; j < z; j++ {
		want = binary.LittleEndian.AppendUint16(want, uint16(windowSample(j)))
	}
	checkWAV(t, b, want)
}

// checkWAV checks that b is pcm, 16 kHz mono 16-bit, under the canonical 44-byte WAV header.
func checkWAV(t *testing.T, b, pcm []byte) {
	t.Helper()
	header := []byte("RIFF")
	header = binary.LittleEndian.AppendUint32(header, uint32(36+len(pcm)))
	header = append(header, "WAVEfmt "...)
	for _, v := range []uint32{16, 1 | 1<<16, 16000, 32000, 2 | 16<<16} {
		header = binary.LittleEndian.AppendUint32(header, v)
	}
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(pcm)))
	if len(b) < 44 || string(b[:44]) != string(header) {
		t.Errorf("header % x, want % x", b[:min(len(b), 44)], header)
	}
	if len(b) != 44+len(pcm) || string(b[44:]) != string(pcm) {
		t.Errorf("%d bytes of samples, not the window's %d", len(b)-44, len(pcm))
	}
}

// files returns the names in dir, sorted.
func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// names returns the base names of ws' files, sorted.
func names(ws []audio.WindowFile) []string {
	var out []string
	for _, w := range ws {
		out = append(out, filepath.Base(w.Path))
	}
	slices.Sort(out)
	return out
}

// inFlight makes a directory for the stand-ins to mark themselves in, and returns it.
func inFlight(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(flightEnv, dir)
	return dir
}

// maxInFlight returns the most window runs the stand-ins counted marked at once.
func maxInFlight(t *testing.T, flight string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(flight, "counts"))
	if err != nil {
		t.Fatal(err)
	}
	most := 0
	for line := range strings.Lines(string(b)) {
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatal(err)
		}
		most = max(most, n)
	}
	return most
}

// checkEnded checks that every stand-in marked in flight has ended: those that finished removed
// their marks, and the process of each mark left, killed, is gone.
func checkEnded(t *testing.T, flight string) {
	t.Helper()
	marks, err := filepath.Glob(filepath.Join(flight, "[0-9]*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range marks {
		pid, err := strconv.Atoi(filepath.Base(m))
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("the window run of process %d is still running (signal 0: %v)", pid, err)
		}
	}
}

func TestWindows(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	dir := t.TempDir()
	plan := flacPlan()
	ws := collect(t, s, t.Context(), audio.WindowsRequest{Path: "speech.flac", Dir: dir}, plan)
	if len(ws) != 2 {
		t.Fatalf("%d windows, want 2", len(ws))
	}
	for i, w := range ws {
		checkWindow(t, w, i, plan, dir)
	}
	if got, want := ws[0].Samples, 399667; got != want {
		t.Errorf("the first window holds %d samples, want 24.9791875 s at 16 kHz rounded, %d", got, want)
	}
	if got, want := ws[0].Duration(), 24979187500*time.Nanosecond; got-want > time.Second/audio.SampleRate || want-got > time.Second/audio.SampleRate {
		t.Errorf("the first window is %v long, want %v to within a sample", got, want)
	}
	if got := files(t, dir); !slices.Equal(got, names(ws)) {
		t.Errorf("the media directory holds %q, want the windows' %q", got, names(ws))
	}
	rs := runs(t)
	if got := programs(rs); !slices.Equal(got, []string{"ffprobe", "ffmpeg", "ffmpeg"}) {
		t.Fatalf("runs %q, want ffprobe, then ffmpeg for each window", got)
	}
	abs, err := filepath.Abs("speech.flac")
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rs[1:] {
		args := strings.Join(r.Args, " ")
		for _, want := range []string{
			"-i " + abs + " -map 0:1 ",
			fmt.Sprintf("-af aresample=16000,atrim=start_sample=%d:end_sample=%d ", first(plan.Windows[i]), end(plan.Windows[i])),
			"-ac 1 -ar 16000 -c:a pcm_s16le -flags:a +bitexact -fflags +bitexact -f s16le -",
		} {
			if !strings.Contains(args, want) {
				t.Errorf("window %d's ffmpeg arguments %q lack %q", i, args, want)
			}
		}
	}
}

// TestWindowsOrderAndConcurrency cuts six windows, the first the slowest, and checks that they
// come in plan order and that the runs in flight never pass the request's concurrency, zero
// meaning one.
func TestWindowsOrderAndConcurrency(t *testing.T) {
	for _, tt := range []struct {
		concurrency, want int
	}{{0, 1}, {3, 3}} {
		t.Run("concurrency "+strconv.Itoa(tt.concurrency), func(t *testing.T) {
			s := standinSplitter(t)
			plan := limitPlan(t, 6)
			flight := inFlight(t)
			t.Setenv(delaysEnv, `{"0": 900, "*": 300}`)
			dir := t.TempDir()
			ws := collect(t, s, t.Context(), audio.WindowsRequest{Path: "long.flac", Dir: dir, Concurrency: tt.concurrency}, plan)
			if len(ws) != 6 {
				t.Fatalf("%d windows, want 6", len(ws))
			}
			for i, w := range ws {
				checkWindow(t, w, i, plan, dir)
			}
			most := maxInFlight(t, flight)
			t.Logf("at most %d window runs at once", most)
			if most > tt.want {
				t.Errorf("%d window runs at once, more than %d", most, tt.want)
			}
			// Six runs, each 300 ms or more, from a dispatcher that starts them as slots free:
			// a concurrency of three overlaps some.
			if tt.want > 1 && most < 2 {
				t.Errorf("at most %d window run at once, at a concurrency of %d", most, tt.want)
			}
			checkEnded(t, flight)
		})
	}
}

// TestWindowsEarlyStop breaks out of the stream after the first window, once with the later
// windows still running and once with them cut and waiting, and checks that the loop returns
// promptly, every run has ended, and the media directory holds the first window alone.
func TestWindowsEarlyStop(t *testing.T) {
	for name, delays := range map[string]string{
		"runs in flight":   `{"0": 100, "*": 20000}`,
		"windows finished": `{"0": 1000, "*": 50}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := standinSplitter(t)
			plan := limitPlan(t, 6)
			flight := inFlight(t)
			t.Setenv(delaysEnv, delays)
			dir := t.TempDir()
			start := time.Now()
			var got []audio.WindowFile
			for w, err := range s.Windows(t.Context(), audio.WindowsRequest{Path: "long.flac", Dir: dir, Concurrency: 3}, plan) {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, w)
				break
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("the loop returned after %v", elapsed)
			}
			if len(got) != 1 {
				t.Fatalf("%d windows, want 1", len(got))
			}
			checkWindow(t, got[0], 0, plan, dir)
			checkEnded(t, flight)
			if files := files(t, dir); !slices.Equal(files, names(got)) {
				t.Errorf("the media directory holds %q, want the first window's %q alone", files, names(got))
			}
		})
	}
}

// TestWindowsCancel cancels the context after the first window, with the later ones running, and
// checks that the stream yields the context's error once and ends, every run ended and the media
// directory holding the first window alone.
func TestWindowsCancel(t *testing.T) {
	s := standinSplitter(t)
	plan := limitPlan(t, 6)
	flight := inFlight(t)
	t.Setenv(delaysEnv, `{"0": 100, "*": 20000}`)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Now()
	var got []audio.WindowFile
	var errs []error
	for w, err := range s.Windows(ctx, audio.WindowsRequest{Path: "long.flac", Dir: dir, Concurrency: 3}, plan) {
		if err != nil {
			errs = append(errs, err)
			if w.Count != 0 {
				t.Errorf("the error came with window %d of %d, want a zero WindowFile", w.Index, w.Count)
			}
			continue
		}
		got = append(got, w)
		cancel()
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the stream ended after %v", elapsed)
	}
	if len(got) != 1 || len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("%d windows and errors %v; want 1 window, then context.Canceled once", len(got), errs)
	}
	checkEnded(t, flight)
	if files := files(t, dir); !slices.Equal(files, names(got)) {
		t.Errorf("the media directory holds %q, want the first window's %q alone", files, names(got))
	}
}

func TestWindowsCancelledBeforeRuns(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	n := 0
	for w, err := range s.Windows(ctx, audio.WindowsRequest{Path: "speech.flac", Dir: t.TempDir()}, flacPlan()) {
		n++
		if !errors.Is(err, context.Canceled) || w.Count != 0 {
			t.Errorf("window %+v, err %v; want a zero WindowFile and context.Canceled", w, err)
		}
	}
	if n != 1 {
		t.Errorf("%d values, want 1", n)
	}
	if rs := runs(t); len(rs) != 0 {
		t.Errorf("runs %q, want none", programs(rs))
	}
}

func TestWindowsRefused(t *testing.T) {
	s := standinSplitter(t)
	t.Setenv(probeEnv, flacProbe)
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	past := flacPlan()
	past.Windows = slices.Clone(past.Windows)
	past.Windows[1].End = past.Duration + time.Second
	empty := flacPlan()
	empty.Windows = nil
	for name, tt := range map[string]struct {
		req  audio.WindowsRequest
		plan audio.Plan
	}{
		"no path":               {audio.WindowsRequest{Dir: dir}, flacPlan()},
		"no media directory":    {audio.WindowsRequest{Path: "speech.flac"}, flacPlan()},
		"missing directory":     {audio.WindowsRequest{Path: "speech.flac", Dir: filepath.Join(dir, "none")}, flacPlan()},
		"directory is a file":   {audio.WindowsRequest{Path: "speech.flac", Dir: file}, flacPlan()},
		"negative concurrency":  {audio.WindowsRequest{Path: "speech.flac", Dir: dir, Concurrency: -1}, flacPlan()},
		"no windows":            {audio.WindowsRequest{Path: "speech.flac", Dir: dir}, empty},
		"a window past the end": {audio.WindowsRequest{Path: "speech.flac", Dir: dir}, past},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			n := 0
			for w, err := range s.Windows(t.Context(), tt.req, tt.plan) {
				n++
				if !errors.Is(err, audio.ErrRequest) || w.Count != 0 {
					t.Errorf("window %+v, err %v; want a zero WindowFile and ErrRequest", w, err)
				}
			}
			if n != 1 {
				t.Errorf("%d values, want 1", n)
			}
			if rs := runs(t); len(rs) != 0 {
				t.Errorf("runs %q, want none", programs(rs))
			}
		})
	}
	t.Run("a plan of another source", func(t *testing.T) {
		t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
		other := flacPlan()
		other.Duration += time.Millisecond
		for _, err := range s.Windows(t.Context(), audio.WindowsRequest{Path: "speech.flac", Dir: dir}, other) {
			if !errors.Is(err, audio.ErrRequest) {
				t.Errorf("err %v, want ErrRequest", err)
			}
		}
		if got := programs(runs(t)); !slices.Equal(got, []string{"ffprobe"}) {
			t.Errorf("runs %q, want ffprobe alone", got)
		}
	})
	t.Run("no audio", func(t *testing.T) {
		t.Setenv(probeEnv, `{"streams":[],"format":{}}`)
		for _, err := range s.Windows(t.Context(), audio.WindowsRequest{Path: "speech.flac", Dir: dir}, flacPlan()) {
			if !errors.Is(err, audio.ErrNoAudio) {
				t.Errorf("err %v, want ErrNoAudio", err)
			}
		}
	})
	if got := files(t, dir); !slices.Equal(got, []string{"file"}) {
		t.Errorf("the media directory holds %q", got)
	}
}

// TestWindowsFailedWindow fails the second window's run and checks that it yields its index and
// count with ffmpeg's error and last line, the first window written, and no file of its own.
func TestWindowsFailedWindow(t *testing.T) {
	s := standinSplitter(t)
	plan := limitPlan(t, 3)
	t.Setenv(failWindowEnv, strconv.Itoa(first(plan.Windows[1])))
	dir := t.TempDir()
	var got []audio.WindowFile
	var errs []error
	for w, err := range s.Windows(t.Context(), audio.WindowsRequest{Path: "long.flac", Dir: dir, Concurrency: 2}, plan) {
		got = append(got, w)
		errs = append(errs, err)
	}
	if len(got) != 3 {
		t.Fatalf("%d values, want 3, the stream carrying on past the failed window", len(got))
	}
	for _, i := range []int{0, 2} {
		if errs[i] != nil {
			t.Fatalf("window %d: %v", i, errs[i])
		}
		checkWindow(t, got[i], i, plan, dir)
	}
	var exit *tool.ExitError
	if !errors.As(errs[1], &exit) || !strings.Contains(errs[1].Error(), "Cannot allocate memory") {
		t.Errorf("window 1's error %v, want a *tool.ExitError carrying ffmpeg's last line", errs[1])
	}
	if w := got[1]; w.Index != 1 || w.Count != 3 || w.Window != plan.Windows[1] || w.Path != "" || w.SHA256 != "" {
		t.Errorf("the failed window %+v, want its index, count, and window, with no file", w)
	}
	if files := files(t, dir); !slices.Equal(files, names([]audio.WindowFile{got[0], got[2]})) {
		t.Errorf("the media directory holds %q, want the two windows' files alone", files)
	}
}
