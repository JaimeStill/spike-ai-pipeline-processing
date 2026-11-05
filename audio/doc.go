//go:build linux

// Package audio prepares speech for a model's audio input: it plans the windows a source is cut
// into, each short enough for the model to take whole, ending in a silence so no cut splits a
// word. It provides pre-inference preparation only; a layer above sends the windows to a model.
// The package builds on Linux only, on the standard library and this module's tool package;
// every program it runs is a tool.Tool the caller built: FFmpeg's ffmpeg and ffprobe, FFmpegSpec
// and FFprobeSpec declaring them, minimum 9.0, which the container runner runs in FFmpegImage.
//
// A Splitter, built over a Config of the two Tools, both required, probes a source through
// ffprobe for its duration and its first audio stream, and refuses a source with none, with
// ErrNoAudio, before ffmpeg runs. Detect then finds the stream's silences through ffmpeg's
// silencedetect: those at least DefaultMinSilence, 0.3 s, long, below a threshold DefaultBelow,
// 15 dB, under the stream's mean volume, which a run of volumedetect measures first. A Request
// may set the minimum and the dB below the mean, or give a fixed threshold in their place.
//
// PlanWindows, a pure function of a duration, its silences, and a Request's Overlap, plans the
// windows: they cover the source in order, each at most WindowLimit, 30 s, long, overlaps
// included, and each but the last ends at the middle of the latest silence its 30 s allow, or at
// the limit when they allow none; the next starts Overlap, DefaultOverlap, 2 s, by default,
// before it, and the range the two share is their seam. The same arguments give the same plan.
// Splitter.Plan is Detect and then PlanWindows.
//
// Splitter.Windows cuts a plan's windows, a lazy iter.Seq2 of WindowFile in plan order, as a
// WindowsRequest says: the source, the media directory the windows are written into and left, and
// how many ffmpeg runs are in flight at once, zero meaning one. Each window is 16 kHz, SampleRate,
// mono 16-bit PCM WAV, resampled and downmixed by ffmpeg from the audio stream Probe found, trimmed
// to the window's samples so it is its span long to within one, its header the canonical 44 bytes
// ffmpeg's -bitexact WAV carries, and named by the SHA-256 of its bytes; the same window is the
// same file through either runner. A loop that stops early, or a context that ends, stops the runs
// in flight, and the media directory keeps only the windows the stream yielded.
//
// Join merges the windows' transcripts, one string for each in plan order, into one Transcript,
// as a JoinRequest says: each seam with Hugging Face transformers' best-offset merge, the later
// window's head slid over the earlier window's tail, the best-scoring offset that matches at least
// 2 words, compared lowercased with their punctuation dropped, taken, and the two spliced at the
// middle of the overlap it aligns. The search is bounded to SeamWords words each side,
// DefaultSeamWords, 16, by default, the reach of a 2 s overlap, so a phrase repeated further away
// is never the splice. A seam that matches fewer than 2 words, as one beside a failed window's
// empty transcript does, is concatenated whole. Each seam is reported as a Seam, matched or not,
// with where in the transcript's words it is; the same transcripts give the same Transcript.
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
//   - [Splitter.Detect] finds a source's silences, a [Detection], as a [Request] says.
//   - [Splitter.Plan] plans a source's windows, as a Request says.
//   - [Splitter.Windows] cuts a plan's windows as a [WindowsRequest] says, each a [WindowFile].
//   - [SampleRate] is the windows' sample rate, 16 kHz.
//   - [PlanWindows] plans the windows of a duration and its silences, running nothing.
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
