//go:build integration && linux

package media_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// liveLength is the excerpt the live sources carry: long enough for a window cut at a silence
// before the one cut at the end, and for frames of every reason.
const liveLength = 36 * time.Second

// liveRunners build ffmpeg and ffprobe through each runner for the live sources. The container
// runner's ffmpeg joins the host's network, so a sender on the host's loopback is reachable from
// inside its container, as from the exec runner's ffmpeg.
var liveRunners = map[string]func(t *testing.T) media.Config{
	"exec": runners["exec"],
	"container": func(t *testing.T) media.Config {
		return tools(t, func(spec tool.Spec) (*tool.Tool, error) {
			return tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage, Network: "host"}, spec)
		})
	},
}

// summary is what a stream yields, its arrival times aside, for comparing two streams: each
// window's index, span, cut, samples, threshold, and SHA-256, and each frame's index, reason,
// time, size, and SHA-256, in the order yielded.
type summary struct {
	windows, frames []string
}

// summarize summarizes events.
func summarize(events []media.Event) summary {
	var s summary
	for _, e := range events {
		switch {
		case e.Window != nil:
			w := e.Window
			s.windows = append(s.windows, fmt.Sprintf("window %d: %v %s, %d samples, %.2f dB, %s", w.Index, w.Span, w.Cut, w.Samples, w.Threshold, w.SHA256))
		case e.Frame != nil:
			f := e.Frame
			s.frames = append(s.frames, fmt.Sprintf("frame %d: %s at %v, %dx%d, %s", f.Index, f.Reason, e.Time, f.Width, f.Height, f.SHA256))
		}
	}
	return s
}

// compare checks got's windows and frames against want's, one for one.
func compare(t *testing.T, got, want []string) {
	t.Helper()
	for i := range max(len(got), len(want)) {
		switch {
		case i >= len(got):
			t.Errorf("missing: %s", want[i])
		case i >= len(want):
			t.Errorf("extra: %s", got[i])
		case got[i] != want[i]:
			t.Errorf("%s; the file gives %s", got[i], want[i])
		default:
			t.Logf("recorded: %s", got[i])
		}
	}
}

// checkTruth checks the frames of events against the truth's frames within the excerpt: reasons
// and times exactly.
func checkTruth(t *testing.T, events []media.Event) {
	t.Helper()
	truth := readTruth(t)
	_, fs := split(events)
	var want []string
	for _, f := range truth.Video.Frames {
		if at := time.Duration(f.TimeMS) * time.Millisecond; at < liveLength {
			want = append(want, fmt.Sprintf("%s at %v", f.Reason, at))
		}
	}
	var got []string
	for _, e := range fs {
		got = append(got, fmt.Sprintf("%s at %v", e.Frame.Reason, e.Time))
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("frames %q, want the truth's %q", got, want)
	}
}

// checkCutShort checks that got, a stream's last window, is want, the file's, cut short at its end
// by at most tail: the same start and cut, and samples that are a prefix of want's.
func checkCutShort(t *testing.T, got, want *media.Window, tail time.Duration) {
	t.Helper()
	t.Logf("recorded: the last window %v, %d samples, the file's %v, %d samples; %v short",
		got.Span, got.Samples, want.Span, want.Samples, want.Span.End-got.Span.End)
	if got.Index != want.Index || got.Span.Start != want.Span.Start || got.Cut != want.Cut ||
		got.Span.End > want.Span.End || got.Span.End < want.Span.End-tail {
		t.Errorf("the last window %d, %v %s; want the file's %d, %v %s, cut short by at most %v",
			got.Index, got.Span, got.Cut, want.Index, want.Span, want.Cut, tail)
	}
	g, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := os.ReadFile(want.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(w[44:], g[44:]) {
		t.Errorf("the last window's samples aren't a prefix of the file's")
	}
}

// freePort returns a port of network, tcp or udp, free on the loopback when asked.
func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// awaitSocket waits until a socket of network, tcp or udp, is bound to port in this network
// namespace, which a container on the host's network shares: for tcp, listening. It reads the
// kernel's socket tables, so it never connects, which would take a one-client server's client.
func awaitSocket(t *testing.T, network string, port int) {
	t.Helper()
	want := fmt.Sprintf(":%04X", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, table := range []string{network, network + "6"} {
			b, err := os.ReadFile(filepath.Join("/proc/net", table))
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(bytes.NewReader(b))
			for sc.Scan() {
				fields := strings.Fields(sc.Text())
				if len(fields) > 3 && strings.HasSuffix(fields[1], want) && (network == "udp" || fields[3] == "0A") {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %s socket bound to port %d", network, port)
}

// sender is an ffmpeg on the host sending the excerpt at real time, -re.
type sender struct {
	cmd    *exec.Cmd
	stderr bytes.Buffer
}

// startSender starts ffmpeg on the host with args after the excerpt's input, read at real time;
// it is killed, if still running, when the test ends.
func startSender(t *testing.T, excerpt string, args ...string) *sender {
	t.Helper()
	s := &sender{}
	full := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-re", "-i", excerpt}, args...)
	s.cmd = exec.Command("ffmpeg", full...)
	s.cmd.Stderr = &s.stderr
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	})
	return s
}

// wait waits for the sender to exit and logs how; a sender serving one client may exit with an
// error once its client has gone, which fails nothing.
func (s *sender) wait(t *testing.T) {
	t.Helper()
	err := s.cmd.Wait()
	t.Logf("recorded: the sender exited: %v %s", err, strings.TrimSpace(s.stderr.String()))
}

// receive ranges over s's stream of req in a goroutine of its own, returning a function that waits
// for it and returns its events, failing the test on an error.
func receive(t *testing.T, s *media.Streamer, req media.Request) func() []media.Event {
	t.Helper()
	var events []media.Event
	var errs []error
	var wg sync.WaitGroup
	wg.Go(func() {
		for e, err := range s.Events(t.Context(), req) {
			if err != nil {
				errs = append(errs, err)
				continue
			}
			events = append(events, e)
		}
	})
	return func() []media.Event {
		wg.Wait()
		for _, err := range errs {
			t.Error(err)
		}
		return events
	}
}

// liveCase is one live source: how its sender sends the excerpt, and what the stream takes and
// compares. tracks is the request's: with TracksVideo, only the frames compare with the file's.
// tail, when not zero, is how much of the excerpt's end the protocol may lose when the sender
// closes: the last window is then the file's cut short by at most tail, its samples a prefix of
// the file's.
type liveCase struct {
	name   string
	tracks media.Tracks
	tail   time.Duration
	// run starts the sender and the receiver in whichever order the protocol needs, and returns
	// the stream's events.
	run func(t *testing.T, s *media.Streamer, excerpt, dir string) []media.Event
}

// listening is a live case whose sender listens on a TCP or UDP port before the receiver connects
// to url, ffmpeg sending the excerpt with args, %d in url and args its port.
func listening(network, url string, args ...string) func(t *testing.T, s *media.Streamer, excerpt, dir string) []media.Event {
	return func(t *testing.T, s *media.Streamer, excerpt, dir string) []media.Event {
		port := freePort(t, network)
		snd := startSender(t, excerpt, withPort(args, port)...)
		awaitSocket(t, network, port)
		events := receive(t, s, media.Request{Source: media.Source{URL: fmt.Sprintf(url, port)}, Dir: dir})()
		snd.wait(t)
		return events
	}
}

// withPort returns args with %d in each replaced by port.
func withPort(args []string, port int) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "%d", strconv.Itoa(port))
	}
	return out
}

// udpCase is a live case whose receiver binds its UDP port, the stream ending a timeout after the
// sender's last datagram, before ffmpeg sends the excerpt to it with args.
func udpCase(tracks media.Tracks, args ...string) func(t *testing.T, s *media.Streamer, excerpt, dir string) []media.Event {
	return func(t *testing.T, s *media.Streamer, excerpt, dir string) []media.Event {
		port := freePort(t, "udp")
		wait := receive(t, s, media.Request{
			Source: media.Source{URL: fmt.Sprintf("udp://127.0.0.1:%d?timeout=3000000", port), Tracks: tracks},
			Dir:    dir,
		})
		awaitSocket(t, "udp", port)
		snd := startSender(t, excerpt, append(withPort(args, port), fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", port))...)
		snd.wait(t)
		return wait()
	}
}

// hlsCase is live HLS: ffmpeg writes the excerpt as a live playlist of fragmented MP4 segments,
// FLAC and MPEG-4 Part 2 copied, into a directory a Go server serves over HTTP, and the receiver
// opens the playlist as soon as it lists its first segment, so it starts at the first.
func hlsCase(t *testing.T, s *media.Streamer, excerpt, dir string) []media.Event {
	served := t.TempDir()
	srv := httptest.NewServer(http.FileServer(http.Dir(served)))
	defer srv.Close()
	playlist := filepath.Join(served, "live.m3u8")
	snd := startSender(t, excerpt, "-c", "copy", "-f", "hls", "-hls_time", "2", "-hls_list_size", "0",
		"-hls_segment_type", "fmp4", "-hls_flags", "temp_file", playlist)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(playlist); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sender wrote no playlist")
		}
		time.Sleep(20 * time.Millisecond)
	}
	events := receive(t, s, media.Request{Source: media.Source{URL: srv.URL + "/live.m3u8"}, Dir: dir})()
	snd.wait(t)
	return events
}

// readerCase is a reader: the excerpt's open file, fed to ffmpeg's stdin.
func readerCase(t *testing.T, s *media.Streamer, excerpt, dir string) []media.Event {
	f, err := os.Open(excerpt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	return receive(t, s, media.Request{Source: media.Source{Reader: f}, Dir: dir})()
}

// liveCases are the live sources: srt, rtmp, http, live HLS, and udp, each carrying the excerpt
// from ffmpeg as sender over the loopback, and a reader. Every container but RTMP's carries the
// excerpt's FLAC and MPEG-4 Part 2 copied, so the stream is the file's byte for byte: Matroska,
// streamed live, over srt, http, and udp, which carry any container, and fragmented MP4 in HLS.
// FLV can't carry MPEG-4 Part 2, so RTMP's sender encodes the video as lossless H.264, without
// B-frames, whose decoded pixels are the source's, and carries the FLAC copied in enhanced FLV.
// MPEG-TS, udp's and srt's usual container, can't carry FLAC or 16 kHz PCM, and a lossy codec's
// priming shifts the audio, so udp's MPEG-TS case carries the video alone, copied, its decoder's
// headers repeated at each keyframe, and the stream takes the video alone.
//
// SRT's live mode delivers each packet a latency after it is sent and drops what it hasn't
// delivered when the sender closes, and ffmpeg's libsrt can't make a sender wait: lingering, its
// option, applies to file mode alone, in which ffmpeg's receiver got nothing. Matroska's last
// cluster, about a second, is written as the sender closes, so the srt case loses it: about 1 s,
// measured, of the excerpt's 36 s, and its last window is the file's cut short.
var liveCases = []liveCase{
	{name: "srt", tail: 1500 * time.Millisecond, run: listening("udp", "srt://127.0.0.1:%d",
		"-c", "copy", "-f", "matroska", "-live", "1", "srt://127.0.0.1:%d?mode=listener")},
	{name: "rtmp", run: listening("tcp", "rtmp://127.0.0.1:%d/live/stream",
		"-c:v", "libx264", "-qp", "0", "-bf", "0", "-g", "20", "-c:a", "copy", "-f", "flv", "-listen", "1", "rtmp://127.0.0.1:%d/live/stream")},
	{name: "http", run: listening("tcp", "http://127.0.0.1:%d/live.mkv",
		"-c", "copy", "-f", "matroska", "-live", "1", "-listen", "1", "http://127.0.0.1:%d/live.mkv")},
	{name: "hls", run: hlsCase},
	{name: "udp", run: udpCase(media.TracksAuto, "-c", "copy", "-f", "matroska", "-live", "1")},
	{name: "udp-mpegts", tracks: media.TracksVideo, run: udpCase(media.TracksVideo,
		"-map", "0:v", "-c", "copy", "-bsf:v", "dump_extra", "-f", "mpegts")},
	{name: "reader", run: readerCase},
}

// TestLiveSources reads a 36 s excerpt of the fixture from each live source, through both
// runners, and checks that the stream yields what the excerpt read as a file yields on the same
// runner, window for window and frame for frame, their SHA-256s included, and frames whose reasons
// and times are the truth's; udp's MPEG-TS case compares its frames alone. Each case's media
// directory holds only its events' files at the end. The cases run in parallel: each takes about
// the excerpt's length, its sender pacing it, udp's a few seconds more for its timeout.
func TestLiveSources(t *testing.T) {
	t.Parallel()
	excerpt := excerpt(t, liveLength)
	for name, runner := range liveRunners {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := streamer(t, runner(t))
			fileEvents := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{File: excerpt}, Dir: t.TempDir()}))
			want := summarize(fileEvents)
			for _, c := range liveCases {
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()
					dir := t.TempDir()
					start := time.Now()
					events := c.run(t, s, excerpt, dir)
					t.Logf("recorded: %d events in %v", len(events), time.Since(start))
					got := summarize(events)
					compare(t, got.frames, want.frames)
					switch {
					case c.tracks == media.TracksVideo:
						if len(got.windows) != 0 {
							t.Errorf("%d windows of a stream that takes the video alone", len(got.windows))
						}
					case c.tail != 0 && len(got.windows) > 0 && len(got.windows) == len(want.windows):
						n := len(got.windows) - 1
						compare(t, got.windows[:n], want.windows[:n])
						gotWindows, _ := split(events)
						wantWindows, _ := split(fileEvents)
						checkCutShort(t, gotWindows[n].Window, wantWindows[n].Window, c.tail)
					default:
						compare(t, got.windows, want.windows)
					}
					checkTruth(t, events)
					checkDir(t, dir, events)
				})
			}
		})
	}
}

// TestNamedNetwork reads the excerpt over http through the container runner from a sender in a
// container of its own, on a docker network the test creates, which the receiver's image names:
// the receiver reaches the sender by its container's name, and the stream is the file's.
func TestNamedNetwork(t *testing.T) {
	t.Parallel()
	excerpt := excerpt(t, liveLength)
	suffix := strings.ToLower(rand.Text())[:12]
	network, name := "media-test-"+suffix, "media-sender-"+suffix
	docker := func(args ...string) error {
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	if err := docker("network", "create", network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = docker("rm", "--force", name)
		_ = docker("network", "rm", network)
	})
	cfg := tools(t, func(spec tool.Spec) (*tool.Tool, error) {
		return tool.NewContainer(t.Context(), "", tool.Image{Ref: audio.FFmpegImage, Network: network}, spec)
	})
	s := streamer(t, cfg)
	want := summarize(collect(t, s.Events(t.Context(), media.Request{Source: media.Source{File: excerpt}, Dir: t.TempDir()})))
	dir := filepath.Dir(excerpt)
	err := docker("run", "--rm", "--detach", "--name="+name, "--network="+network,
		"--user="+strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()), "--cap-drop=ALL",
		"--volume="+dir+":"+dir+":ro", "--entrypoint=ffmpeg", audio.FFmpegImage,
		"-nostdin", "-loglevel", "error", "-re", "-i", excerpt, "-c", "copy", "-f", "matroska", "-live", "1",
		"-listen", "1", "http://0.0.0.0:8080/live.mkv")
	if err != nil {
		t.Fatal(err)
	}
	// The sender listens inside its own network namespace, where the test can't see its socket,
	// and a connection before it listens is refused, which takes nothing from it: retry those.
	var events []media.Event
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	for attempt := 1; ; attempt++ {
		events = nil
		var failed error
		for e, err := range s.Events(ctx, media.Request{Source: media.Source{URL: "http://" + name + ":8080/live.mkv"}, Dir: t.TempDir()}) {
			if err != nil {
				failed = err
				continue
			}
			events = append(events, e)
		}
		if failed == nil {
			t.Logf("recorded: attempt %d reached the sender", attempt)
			break
		}
		if len(events) > 0 || attempt == 20 || !strings.Contains(failed.Error(), "Connection refused") {
			t.Fatalf("attempt %d: %v", attempt, failed)
		}
		time.Sleep(500 * time.Millisecond)
	}
	got := summarize(events)
	compare(t, got.frames, want.frames)
	compare(t, got.windows, want.windows)
	checkTruth(t, events)
}

// TestPlaylistConfined serves an HLS playlist over http whose one segment, an MPEG-TS cut of the
// excerpt, is served over https. ffmpeg, unconfined, opens the segment's https, as the control
// shows: the protocols its HLS demuxer allows an http playlist's segments by default include https,
// and data, rtp, tcp, and udp besides, though not file. Its TLS then refuses the test server's
// certificate, which ffmpeg 9 verifies and which no option of the playlist's reaches. The stream's
// run, allowed only http's own protocols, refuses https itself, and ends with that refusal and no
// event.
func TestPlaylistConfined(t *testing.T) {
	t.Parallel()
	excerpt := excerpt(t, 4*time.Second)
	ts, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-i", excerpt,
		"-map", "0:v", "-c", "copy", "-bsf:v", "dump_extra", "-f", "mpegts", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	segment := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(ts)
	}))
	defer segment.Close()
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\n" + segment.URL + "/segment.ts\n#EXT-X-ENDLIST\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(playlist))
	}))
	defer srv.Close()
	url := srv.URL + "/live.m3u8"

	control, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-i", url, "-map", "0:V:0", "-f", "null", "-").CombinedOutput()
	t.Logf("recorded: the control: %v: %s", err, control)
	if !strings.Contains(string(control), "[tls @") || strings.Contains(string(control), "whitelist") {
		t.Fatalf("the control, unconfined, didn't open the https segment: %s", control)
	}

	s := streamer(t, runners["exec"](t))
	dir := t.TempDir()
	var events []media.Event
	var errs []error
	for e, err := range s.Events(t.Context(), media.Request{Source: media.Source{URL: url, Tracks: media.TracksVideo}, Dir: dir}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		events = append(events, e)
	}
	t.Logf("recorded: %v", errs)
	if len(events) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "Protocol 'https' not on whitelist 'http,tcp,crypto'") {
		t.Errorf("%d events and errors %v, want ffmpeg's refusal of the https protocol alone", len(events), errs)
	}
	checkDir(t, dir, nil)
}
