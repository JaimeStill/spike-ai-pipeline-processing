//go:build linux

// Package media reads a source's audio and video through one ffmpeg run and streams them, as they
// arrive, as the inputs a model takes: windows of its audio, cut as audio cuts them, and frames of
// its video, taken at its scene changes and at least every so often. It provides pre-inference
// preparation only; a layer above sends the windows and frames to a model. The package builds on
// Linux only, on the standard library and this module's audio and tool packages. The programs it
// runs are FFmpeg's ffmpeg and ffprobe, each a tool.Tool the caller built through either runner,
// typically from audio.FFmpegSpec and audio.FFprobeSpec, minimum 9.0, the container runner running
// them in audio.FFmpegImage.
//
// A Streamer, built over a Config of the two Tools, both required, takes a Request: one Source,
// the media directory its windows and frames are written into and left, and the settings of its
// windows and frames. A Source names exactly one of:
//
//   - a file, read unpaced, as fast as ffmpeg decodes it, or paced, at real time, as a live source
//     arrives;
//   - a reader the caller holds, fed to ffmpeg's stdin as it is read; or
//   - an input URL whose scheme is srt, rtmp, http, https, an HLS playlist's included, or udp.
//
// A request that names no source or more than one, paces anything but a file, or names a URL of
// any other scheme, rtsp among them, is refused, as is any field out of range, with ErrRequest,
// before anything runs. Each run allows ffmpeg only its source's protocols, through
// -protocol_whitelist: a file's run files, a reader's its pipe, and a URL's its scheme's, so a
// playlist can't open another protocol or a local file. Through the container runner, whose runs
// join no network, a URL run joins the docker network the ffmpeg Tool's image names, and one whose
// image names none is refused with ErrRequest; http(s) reconnection isn't attempted.
//
// Events probes a file through ffprobe for its first audio stream and its first video stream, an
// attached picture not counted, refusing a file with neither, with ErrNoMedia, before ffmpeg runs.
// A reader or a URL can't be probed ahead, since a probe would read what only the run may read: its
// run takes the streams the Source's Tracks names, both by default, by type, and one the source
// lacks fails the run with ErrNoMedia before any event. Tracks narrows a file's streams too. Then
// one ffmpeg run reads the source, and the stream yields an Event for each window and each frame
// as soon as it is complete, a lazy iter.Seq2 of Event:
//
//   - The audio stream comes to the package as 16 kHz mono 16-bit PCM on ffmpeg's stdout. An
//     audio.Tracker finds its silences as it arrives, under the request's fixed threshold or a
//     rolling one that follows the stream's noise floor, with audio's tracker defaults, and an
//     audio.Planner plans the windows from them by PlanWindows' policy: each at most 30 s, its
//     overlap included, cut at the middle of the latest silence before its limit, or at the limit.
//     A window is cut once the stream passes its limit or ends, written as WAV through audio.WAV,
//     so a window is the same bytes audio's Splitter.Windows cuts under the tracker detector, and
//     its event carries its index, span, cut, seams, samples, and the threshold it was cut under:
//     the silence's it ends in, or, cut at the limit or the end, the one in force at its last
//     sample. No silencedetect runs.
//   - The video stream's frames are taken by ffmpeg's select filter: the first frame, each whose
//     scene score is past the request's SceneThreshold, DefaultSceneThreshold, 0.2, by default,
//     and each MaxGap, DefaultMaxGap, 10 s, by default, or more after the last taken. Each is
//     written as PNG, at the source's resolution or scaled down to fit the request's MaxSide,
//     into a hidden directory of the run's own. A file's is made in the file's directory, the
//     run's working directory, which the container runner mounts, since ffmpeg reads the file by
//     its path and the runner mounts one directory; a reader's or a URL's is made in the media
//     directory and is the run's working directory, the only one mounted. The metadata filter
//     reports each frame's time and scene score on ffmpeg's stderr, from which its reason comes:
//     first, scene, or interval.
//
// Each event carries its source time and the wall-clock time its last sample or frame arrived from
// ffmpeg. Windows come in plan order and frames in source-time order, interleaved as they
// complete. Each window and frame is named by its SHA-256 in the media directory, renamed there
// just before it is yielded, so the media directory holds only the windows and frames the stream
// yielded. The same source and request give the same files through either runner.
//
// A context that ends stops ffmpeg through either runner, and the stream yields its error once and
// ends; a loop that stops early stops the run. A run that fails ends the stream with its error,
// after the events already complete, the frames whose PNG is whole included. Either way, nothing
// the stream didn't yield is left behind, nor the run's directory of frames.
//
// Its exported identifiers:
//
//   - [Config] is the Tools a Streamer is built over: ffmpeg and ffprobe.
//   - [Streamer] reads a source through one ffmpeg run; [NewStreamer] builds one over a Config.
//   - [Streamer.Events] streams a [Request]'s windows and frames, each an [Event].
//   - [Source] is a request's one source: a file, read unpaced or paced; a reader; or a URL.
//   - [Tracks] is which of a source's streams a run takes: [TracksAuto], [TracksBoth],
//     [TracksAudio], or [TracksVideo].
//   - [Window] is a window event's window of the audio; [Frame] a frame event's frame of the
//     video, taken for its [Reason]: [ReasonFirst], [ReasonScene], or [ReasonInterval].
//   - [DefaultSceneThreshold] and [DefaultMaxGap] are a Request's frame defaults.
//   - [ErrRequest] is a refused request's cause; [ErrNoMedia] a source's without the audio or video
//     stream it takes.
package media
