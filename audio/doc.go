//go:build linux

// Package audio prepares speech for a model's audio input: it cuts a source into windows short
// enough for the model to take whole, each ending in a silence so no cut splits a word, and joins
// the windows' transcripts into one. It provides pre-inference preparation only; a layer above
// sends the windows to a model. The package builds on Linux only, on the standard library and
// this module's tool package. The programs it runs are FFmpeg's ffmpeg and ffprobe, each a
// tool.Tool the caller built through either runner: FFmpegSpec and FFprobeSpec declare them,
// minimum 9.0, and the container runner runs them in FFmpegImage.
//
// A Splitter, built over a Config of the two Tools, both required, probes a source through
// ffprobe for its duration and its first audio stream, and refuses a source with none, with
// ErrNoAudio, before ffmpeg runs. Detect then finds the stream's silences: a volumedetect run
// measures the stream's mean volume, and a silencedetect run finds the silences at least
// DefaultMinSilence, 0.3 s, long and quieter than the mean less DefaultBelow, 15 dB. A Request
// may set the minimum and the dB below the mean, or give a fixed threshold in their place.
//
// A Request's Detector chooses how the silences are found: DetectorSilencedetect, the default,
// through volumedetect and silencedetect as above; or DetectorTracker, through a Tracker fed the
// stream ffmpeg decodes to 16 kHz mono 16-bit PCM, its threshold following the stream's noise
// floor. A Tracker frames the PCM in 10 ms frames, FrameLength, and takes each frame's RMS energy
// in dB; its threshold is a low percentile of the frames' energy over a rolling window, plus a
// margin, DefaultPercentile, the 10th, over DefaultFloorWindow, 4 s, plus DefaultMargin, 12 dB, by
// default, or a fixed threshold in their place; a run of frames under it at least the minimum
// silence long is a Silence, which reports the threshold in force when it opened. It is fed PCM in
// chunks as the stream arrives, returns each silence as it closes, keeps memory bounded by its
// window, and finds the same silences however the stream is chunked.
//
// The tracker is Go, not silencedetect, though the module runs a mature tool through an adapter
// rather than reimplement it, because silencedetect can't do what the tracker is for: its
// threshold is fixed for a run, so it can't follow a change of level or of noise floor partway
// through a stream, and a stream has no whole-source mean to fix it from before it ends. The
// streams task's round-2 simulation measured the cost: a threshold fixed from the first 30 s, with
// noise added partway, cut 66 of 92 Earnings-21 windows and 25 of 51 AMI windows at the 30 s limit,
// where a floor tracker, untuned, cut 0 and 3; ffmpeg's dynaudnorm, normalizing the level ahead of
// silencedetect, followed a gain step but not added noise, and added about 15 s of lag.
// TestFindingsFloor measured the tracker here at its defaults on the corpus's long sources, with
// a 12 dB gain step or a pink noise floor from 10 minutes: it cut at the limit 0 of 49 AMI windows
// unchanged, 0 with the gain step, and 0 with the noise, where silencedetect at a threshold fixed
// from the first 30 s cut 2, 25, and 26, and at its default 0, 0, and 24; and on the speech
// fixture's level-change variants it finds every inserted gap after the change, of which
// silencedetect at the first 30 s's threshold finds none.
//
// PlanWindows, a pure function of a duration, its silences, and a Request's Overlap, plans the
// windows. They cover the source in order, each at most WindowLimit, 30 s, long, overlap
// included. Each window but the last ends at the middle of the latest silence its 30 s allow, or
// at the limit when they allow none. The next window starts Overlap before it, DefaultOverlap,
// 2 s, by default, and the range the two share is their seam. The same arguments give the same
// plan. Splitter.Plan is Detect and then PlanWindows. A Planner plans a stream's windows by the
// same policy as it arrives: it is given the silences as they are found and the stream's position
// as it advances, a Tracker's Position, and returns each window as soon as the stream passes the
// window's limit, or ends; PlanWindows is a Planner given everything at once, so the two give the
// same windows. With DetectorTracker, Splitter.Plan's plan is the one a Tracker and a Planner give
// fed the same PCM as a stream.
//
// Splitter.Windows cuts a plan's windows as a WindowsRequest says, a lazy iter.Seq2 of WindowFile
// in plan order. The request names the source, the media directory the windows are written into
// and left, and how many ffmpeg runs are in flight at once, zero meaning one. ffmpeg resamples and
// downmixes the audio stream Probe found, and each window is written as 16 kHz, SampleRate, mono
// 16-bit PCM WAV, trimmed to its span's samples to within one, with the canonical 44-byte header
// of ffmpeg's -bitexact WAV, and named by the SHA-256 of its bytes. The same window is the same
// file through either runner. A loop that stops early, or a context that ends, stops the runs in
// flight, and the media directory keeps only the windows the stream yielded.
//
// Join merges the windows' transcripts, one string for each in plan order, into one Transcript,
// as a JoinRequest says. It merges each seam as Hugging Face transformers' best-offset merge does:
// it slides the later window's first words over the earlier window's last ones, compares them
// lowercased with their punctuation dropped, takes the best-scoring offset that matches at least
// 2 words, and splices the two at the middle of the overlap that offset aligns. The search reaches
// SeamWords words into each window, DefaultSeamWords, 16, by default, the reach of a 2 s overlap,
// so a phrase repeated further away is never the splice. A seam that matches fewer than 2
// words, as one beside a failed window's empty transcript does, is concatenated whole. Each seam
// is reported as a Seam, matched or not, with its position in the transcript's words. The same
// transcripts give the same Transcript. Each seam's merge reads only the windows up to it, so
// joining the transcripts received so far again as more arrive leaves the words before the
// latest seam unchanged: they are settled.
//
// Its exported identifiers:
//
//   - [FFmpegSpec] and [FFprobeSpec] declare ffmpeg and ffprobe for either runner's constructor.
//   - [FFmpegMinimum] is the oldest ffmpeg and ffprobe the package accepts, 9.0.
//   - [FFmpegImage] is the default image ffmpeg and ffprobe run in through the container runner.
//   - [Config] is the Tools a Splitter is built over: ffmpeg and ffprobe.
//   - [Splitter] probes a source, detects its silences, and plans its windows.
//   - [NewSplitter] builds a Splitter over a Config.
//   - [Splitter.Probe] reads a source's duration and audio stream, a [Source].
//   - [Splitter.Detect] finds a source's silences, a [Detection], as a [Request] says, through
//     the [Detector] it names: [DetectorSilencedetect] or [DetectorTracker].
//   - [Splitter.Plan] plans a source's windows, as a Request says.
//   - [Splitter.Windows] cuts a plan's windows as a [WindowsRequest] says, each a [WindowFile].
//   - [SampleRate] is the windows' sample rate, 16 kHz.
//   - [Sample] is the sample at SampleRate nearest a time, where a window's samples begin and end.
//   - [WAV] writes 16 kHz mono 16-bit PCM as a window's WAV, with the canonical 44-byte header.
//   - [PlanWindows] plans the windows of a duration and its silences, running nothing.
//   - [Planner] plans a stream's windows as it arrives, by PlanWindows' policy; [NewPlanner] builds
//     one.
//   - [Planner.Silence] gives it a silence found; [Planner.Open] gives it one still open,
//     [Planner.Advance] the stream's position, and [Planner.End] the stream's end, each returning
//     the windows that fixes; [Planner.Overlap] returns the overlap it plans with.
//   - [Tracker] finds a stream's silences as its PCM arrives, each a [Silence] with its
//     threshold; [NewTracker] builds one.
//   - [Tracker.Feed] gives it the stream's next bytes and [Tracker.End] ends the stream, each
//     returning the silences that closes; [Tracker.Open] reports the silence still open,
//     [Tracker.Position] how far the stream is settled, [Tracker.Duration] its length, and
//     [Tracker.Threshold] the threshold in force.
//   - [FrameLength] is the tracker's frame, 10 ms.
//   - [DefaultPercentile], [DefaultFloorWindow], and [DefaultMargin] are the tracker's defaults.
//   - [Plan] is a source's windows, each a [Window], ended as its [Cut] says, and their seams.
//   - [Cut] is what ends a window: [CutSilence], [CutLimit], or [CutEnd].
//   - [Span] is a range of the source: a silence, a window's, or a seam.
//   - [WindowLimit] is the longest a window may be, 30 s.
//   - [DefaultOverlap], [DefaultMinSilence], and [DefaultBelow] are a Request's defaults.
//   - [CheckOverlap] returns the overlap a request asks for, or refuses it.
//   - [Join] merges window transcripts into a [Transcript], as a [JoinRequest] says, each seam
//     reported as a [Seam].
//   - [DefaultSeamWords] is a JoinRequest's default seam word bound, 16.
//   - [ErrRequest] is a refused request's cause; [ErrNoAudio] a source's with no audio stream.
package audio
