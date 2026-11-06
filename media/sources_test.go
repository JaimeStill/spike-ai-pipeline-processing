//go:build linux

package media_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
)

// ffmpegRuns returns the log's runs of ffmpeg, docker's included, and fails the test on any probe.
func ffmpegRuns(t *testing.T) []run {
	t.Helper()
	var out []run
	for _, r := range runs(t) {
		if r.Program == "ffprobe" {
			t.Errorf("a probe, %q, of a source that can't be probed", r.Args)
			continue
		}
		out = append(out, r)
	}
	return out
}

// checkStream checks a stream of the stand-in's sceneFrames and the gain fixture's windows: every
// frame and window, in dir and nowhere else, and dir holding nothing else, the run's own
// directory gone.
func checkStream(t *testing.T, dir string, events []media.Event) {
	t.Helper()
	wantWindows, _ := expectedWindows(t, audio.Request{})
	ws, fs := split(events)
	if len(ws) != len(wantWindows) || len(fs) != len(sceneFrames) {
		t.Errorf("%d windows and %d frames, want %d and %d", len(ws), len(fs), len(wantWindows), len(sceneFrames))
	}
	for i, e := range ws {
		if i < len(wantWindows) && e.Window.Span != wantWindows[i].Span {
			t.Errorf("window %d: %v, want %v", i, e.Window.Span, wantWindows[i].Span)
		}
	}
	for i, e := range fs {
		if i < len(sceneReasons) && e.Frame.Reason != sceneReasons[i] {
			t.Errorf("frame %d: %s, want %s", i, e.Frame.Reason, sceneReasons[i])
		}
	}
	checkDir(t, dir, events)
}

// TestEventsURL streams each scheme's URL through the stand-in: the run allows only its scheme's
// protocols, takes the first audio stream and the first video stream by type, with no probe,
// decodes its video in slice threads and reads at the source's own pace, and works in a hidden
// directory of its own in the media directory, which holds only the events' files at the end.
func TestEventsURL(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probePicture, sceneFrames)
	for _, c := range []struct{ url, protocols string }{
		{"srt://127.0.0.1:9000?latency=200000", "srt"},
		{"rtmp://127.0.0.1:1935/live/stream", "rtmp,tcp"},
		{"http://127.0.0.1:8080/live.m3u8", "http,tcp,crypto"},
		{"https://example.test/live/index.m3u8", "https,tls,tcp,crypto"},
		{"HTTPS://example.test/s.mkv", "https,tls,tcp,crypto"},
		{"udp://127.0.0.1:9000?timeout=2000000", "udp"},
		{"udp://@:9000", "udp"},
	} {
		t.Run(c.url, func(t *testing.T) {
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			dir := t.TempDir()
			events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{URL: c.url}, Dir: dir}))
			checkStream(t, dir, events)
			r := ffmpegRuns(t)
			if len(r) != 1 || r[0].Program != "ffmpeg" {
				t.Fatalf("runs %v, want one ffmpeg run", r)
			}
			args := strings.Join(r[0].Args, " ")
			for _, want := range []string{
				"-thread_type:v slice -flags:a +bitexact -flags:v +bitexact -protocol_whitelist " + c.protocols + " -i " + c.url + " ",
				"-map 0:a:0 -af aresample=16000",
				"-map 0:V:0 -vf select=",
				"-f image2 -start_number 0 -atomic_writing 1 " + dir + "/.media-",
			} {
				if !strings.Contains(args, want) {
					t.Errorf("ffmpeg's arguments %q lack %q", args, want)
				}
			}
			if strings.Contains(args, "-readrate") {
				t.Errorf("ffmpeg's arguments %q pace a URL", args)
			}
		})
	}
}

// TestEventsReader streams a reader through the stand-in: ffmpeg reads it whole from its stdin,
// allowed only its pipe, with no probe, and the stream is the same as a URL's.
func TestEventsReader(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, probePicture, sceneFrames)
	data, err := os.ReadFile(gainWAV)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{Reader: strings.NewReader(string(data))}, Dir: dir}))
	checkStream(t, dir, events)
	r := ffmpegRuns(t)
	if len(r) != 1 {
		t.Fatalf("runs %v, want one ffmpeg run", r)
	}
	if r[0].Stdin != sum(data) {
		t.Errorf("ffmpeg read %q from its stdin, want the reader's %d bytes, %s", r[0].Stdin, len(data), sum(data))
	}
	if args := strings.Join(r[0].Args, " "); !strings.Contains(args, "-thread_type:v slice -flags:a +bitexact -flags:v +bitexact -protocol_whitelist pipe -i pipe:0 ") {
		t.Errorf("ffmpeg's arguments %q", args)
	}
}

// TestEventsTracks takes one stream of a source: a URL's by type, with no probe, and a file's by
// the index its probe found; a file without a stream its tracks require is refused with
// ErrNoMedia after its probe, before ffmpeg runs.
func TestEventsTracks(t *testing.T) {
	s := standinStreamer(t)
	for _, c := range []struct {
		name   string
		source media.Source
		probe  string
		maps   []string
	}{
		{"URL audio", media.Source{URL: "udp://127.0.0.1:9000", Tracks: media.TracksAudio}, "", []string{"0:a:0"}},
		{"URL video", media.Source{URL: "udp://127.0.0.1:9000", Tracks: media.TracksVideo}, "", []string{"0:V:0"}},
		{"URL both", media.Source{URL: "udp://127.0.0.1:9000", Tracks: media.TracksBoth}, "", []string{"0:a:0", "0:V:0"}},
		{"file audio", media.Source{Tracks: media.TracksAudio}, probeBoth, []string{"0:1"}},
		{"file video", media.Source{Tracks: media.TracksVideo}, probeBoth, []string{"0:0"}},
		{"file both", media.Source{Tracks: media.TracksBoth}, probeBoth, []string{"0:1", "0:0"}},
		{"file auto", media.Source{}, probeAudio, []string{"0:0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			setStandin(t, c.probe, sceneFrames)
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			src := c.source
			if src.URL == "" {
				src.File = source(t)
			}
			collect(t, s.Events(t.Context(), media.Request{Source: src, Dir: t.TempDir()}))
			var maps []string
			for _, r := range runs(t) {
				for i, a := range r.Args {
					if a == "-map" && r.Program == "ffmpeg" {
						maps = append(maps, r.Args[i+1])
					}
				}
			}
			if !slices.Equal(maps, c.maps) {
				t.Errorf("maps %q, want %q", maps, c.maps)
			}
		})
	}
	for _, c := range []struct {
		name   string
		tracks media.Tracks
		probe  string
	}{
		{"both of audio", media.TracksBoth, probeAudio},
		{"video of audio", media.TracksVideo, probeAudio},
		{"audio of video", media.TracksAudio, probeVideo},
		{"auto of a picture", media.TracksAuto, probePicture},
	} {
		t.Run(c.name, func(t *testing.T) {
			setStandin(t, c.probe, sceneFrames)
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			var errs []error
			for _, err := range s.Events(t.Context(), media.Request{Source: media.Source{File: source(t), Tracks: c.tracks}, Dir: t.TempDir()}) {
				errs = append(errs, err)
			}
			if len(errs) != 1 || !errors.Is(errs[0], media.ErrNoMedia) {
				t.Errorf("errors %v, want ErrNoMedia once", errs)
			}
			if r := runs(t); len(r) != 1 || r[0].Program != "ffprobe" {
				t.Errorf("runs %v, want the probe alone", r)
			}
		})
	}
}

// TestEventsMissingStream streams a URL and a reader whose source lacks a stream the request
// takes: ffmpeg refuses its map, and the stream yields ErrNoMedia once, naming the stream, and no
// event.
func TestEventsMissingStream(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, "", sceneFrames)
	for kind, name := range map[string]string{"a": "audio", "V": "video"} {
		for _, src := range []media.Source{{URL: "srt://127.0.0.1:9000"}, {Reader: strings.NewReader("x")}} {
			t.Run(name, func(t *testing.T) {
				t.Setenv(missingEnv, kind)
				dir := t.TempDir()
				var errs []error
				for e, err := range s.Events(t.Context(), media.Request{Source: src, Dir: dir}) {
					if err == nil {
						t.Errorf("event %+v", e)
						continue
					}
					errs = append(errs, err)
				}
				if len(errs) != 1 || !errors.Is(errs[0], media.ErrNoMedia) || !strings.Contains(errs[0].Error(), "no "+name+" stream") {
					t.Errorf("errors %v, want ErrNoMedia once, naming the %s", errs, name)
				}
				checkDir(t, dir, nil)
			})
		}
	}
}

// TestEventsNetwork streams through the container runner, the stand-in docker running the
// stand-ins: a URL through an image that names no network, or names none, is refused with
// ErrRequest before ffmpeg runs; one through an image naming a network joins it, works in the
// run's own directory, the one mounted, and allows only its scheme's protocols; and a file and a
// reader need no network.
func TestEventsNetwork(t *testing.T) {
	installStandins(t, "9.0.2")
	setStandin(t, probeBoth, sceneFrames)
	url := "rtmp://sender:1935/live/stream"
	for _, network := range []string{"", "none"} {
		t.Run("refused "+network, func(t *testing.T) {
			s := containerStreamer(t, network)
			t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
			var errs []error
			for _, err := range s.Events(t.Context(), media.Request{Source: media.Source{URL: url}, Dir: t.TempDir()}) {
				errs = append(errs, err)
			}
			if len(errs) != 1 || !errors.Is(errs[0], media.ErrRequest) || !strings.Contains(errs[0].Error(), "docker network") {
				t.Errorf("errors %v, want the refusal once", errs)
			}
			if r := runs(t); len(r) != 0 {
				t.Errorf("runs %v, want none", r)
			}
		})
	}
	t.Run("joined", func(t *testing.T) {
		s := containerStreamer(t, "media-test")
		t.Setenv(logEnv, filepath.Join(t.TempDir(), "runs.log"))
		dir := t.TempDir()
		events := collect(t, s.Events(t.Context(), media.Request{Source: media.Source{URL: url}, Dir: dir}))
		checkStream(t, dir, events)
		r := ffmpegRuns(t)
		if len(r) != 2 || r[0].Program != "docker" || r[1].Program != "ffmpeg" {
			t.Fatalf("runs %v, want docker's run of ffmpeg", r)
		}
		docker := strings.Join(r[0].Args, " ")
		if !strings.Contains(docker, " --network=media-test ") || !strings.Contains(docker, " --entrypoint=ffmpeg ") {
			t.Errorf("docker's arguments %q don't join media-test", docker)
		}
		mount := flagValue(r[0].Args, "--volume")
		host, _, _ := strings.Cut(mount, ":")
		if !strings.HasPrefix(host, filepath.Join(dir, ".media-")) || flagValue(r[0].Args, "--workdir") != host {
			t.Errorf("docker mounts %q and works in %q, want the run's own directory in %s", mount, flagValue(r[0].Args, "--workdir"), dir)
		}
		if args := strings.Join(r[1].Args, " "); !strings.Contains(args, "-protocol_whitelist rtmp,tcp -i "+url+" ") {
			t.Errorf("ffmpeg's arguments %q", args)
		}
	})
	t.Run("file and reader", func(t *testing.T) {
		s := containerStreamer(t, "")
		for _, src := range []media.Source{{File: source(t)}, {Reader: strings.NewReader("x")}} {
			dir := t.TempDir()
			checkStream(t, dir, collect(t, s.Events(t.Context(), media.Request{Source: src, Dir: dir})))
		}
	})
}

// flagValue returns the value of args' flag, joined to it with =, or empty.
func flagValue(args []string, flag string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
	}
	return ""
}

// TestEventsRefusedProtocol streams a URL whose run ffmpeg fails on a protocol its whitelist
// doesn't hold, as a playlist's segment's: the stream's error names the refusal, lines before
// ffmpeg's last, and the last.
func TestEventsRefusedProtocol(t *testing.T) {
	s := standinStreamer(t)
	setStandin(t, "", nil)
	t.Setenv(failEnv, "[https @ 0x55d0c0de] Protocol 'https' not on whitelist 'http,tcp,crypto'!\n"+
		"[in#0 @ 0x55d0c0de] Error when loading first segment 'https://example.test/0.ts'\nError opening input files: Invalid data found when processing input")
	var errs []error
	for _, err := range s.Events(t.Context(), media.Request{Source: media.Source{URL: "http://127.0.0.1:8080/live.m3u8", Tracks: media.TracksAudio}, Dir: t.TempDir()}) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "Protocol 'https' not on whitelist 'http,tcp,crypto': Error opening input files") {
		t.Errorf("errors %v, want the refusal and ffmpeg's last line once", errs)
	}
}
