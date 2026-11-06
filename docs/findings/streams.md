# Finding: audio and video read at real time, prepared as they arrive

This page records what the measurements behind the `media` package found on three real videos read
at real time. The package reads a source through one ffmpeg run and yields, as they arrive, windows
of its audio, cut at silences a noise-floor tracker finds as the samples come, and frames of its
video, taken at its scene changes and at least every 10 s. The test harness sends each window to
Gemma 4 E4B and each frame to Gemma 4 26B-A4B as it is yielded, and re-joins the transcripts as
each lands. The page reports how long each window and frame took from its last sample's or frame's
arrival to its event, and from its event to the model's answer; whether the models kept up; how
accurately the live path transcribes beside audio's file path over the same audio; and what a
cancellation midway leaves. It also gathers what the task measured on the way: the live protocols'
matrix, the tracker's tuning, and the stream's headroom. The measurements answer evidence 13 of the
spike (the root's `context/README.md`) for task `streams` (the root's goal record,
`context/goals/experiment.ai.spike-ai-pipeline.md`).

Every number on the paced runs comes from three runs of the findings tier, from the laptop,
against gb10-2's llama.cpp router, build b12098, serving set A. AMI's come from **findings-streams
2026-11-06**, `mise run findings-streams` from 2026-11-06 16:25:59 to 17:50:25 UTC at processing
commit `c3a7f72`. Commit `5a8f16c` then fixed a window inside a silence still open once the stream
passes its limit (below, under the lags), and the other two sources were run again alone:
NASA's come from **findings-streams nasa 2026-11-06**, from 18:07:48 to 18:48:02 UTC at `5a8f16c`,
and Tears of Steel's from **findings-streams tears 2026-11-06**, from 18:52:32 to 19:03:25 UTC at
`c820411`, whose code is `5a8f16c`'s. Each re-run's windows and frames are the first run's, by
SHA-256. The tracker's tuning comes from `mise run findings-floor` at `c3a7f72`, 16:24:51
to 16:25:54 UTC. Their results are committed in [`streams-results/`](streams-results/README.md):
the logs verbatim and each source's JSON, with every transcript and frame description.

The `media` package releases pre-inference preparation only: the windows, the frames, and their
events. Every request here is test code, in `media/harness_test.go` and `media/findings_test.go`.

## At a glance

| Question | Answer |
|---|---|
| Is a paced source processed as it arrives? | **Yes, on all three**: Tears of Steel's 180 s clip (3 runs), AMI ES2002a's 21.2 min meeting (1 run), and NASA's 25.5 min in-flight event (1 run), read at real time through one ffmpeg run each, 7, 49, and 62 windows and 35, 125, and 156 frames, the same files byte for byte as an unpaced read. No window, frame, or request failed; every stream ended cleanly. |
| How long from a window's last sample to its event? | **The plan's wait**: a window is yielded once the stream passes its 30 s limit, inside a silence or not. AMI: median 1.2 s, at most 6.3 s. Tears: median 1.9 s, at most 3.7 s. NASA: median 2.0 s, 7.9 s at the 90th percentile, and **15.0 s at most**, a window that ends 15 s before its limit. The stream itself adds 1 to 8 ms. |
| And from the event to E4B's transcript? | **0.8 to 4.6 s, medians 1.7 to 2.6 s, never queued**: at most 2 windows in flight of E4B's 4 slots. |
| Frames? | **Yielded 20 to 170 ms after ffmpeg reports them, described 0.5 to 3.9 s later, medians 1.4 to 1.7 s**. 26B-A4B kept up: no frame waited for a slot, at most 2 in flight, Tears' 30 scene cuts in 180 s included. |
| Is the live transcript as good as the file path's? | **Yes, under the same detector**: AMI 0.322 live against 0.323 through audio's file path with the tracker, Tears 0.361 against 0.361 (approximate truth), NASA 0.476 against 0.478 (approximate truth). Against the file path's default, silencedetect, live is **0.015 worse on AMI** (0.307, the audio finding's 0.308), 0.021 on Tears, 0.009 on NASA. |
| Does a cancellation midway stop cleanly? | **Yes, all three**: the stream yielded `context.Canceled` once and ended 0.26 to 0.44 s after the cancel; the request on the wire returned the context's error in under a millisecond; the media directory held exactly the 23, 89, and 110 events yielded, nothing was left beside the source, and no ffmpeg process remained. |
| What headroom does the stream have? | **44 to 507 times real time unpaced**, end to end through media (Tears' 1150x480 slowest); the tracker alone about 48,000 times. |

## The sources

| Source | Media | Length | License | Truth | Paced runs |
|---|---|---|---|---|---|
| `tears` | Tears of Steel, the corpus' 180 s clip: H.264 1150x480 at 24 fps, AAC 44.1 kHz stereo, MP4 | 180.0 s | CC BY 3.0 (film), CC BY-ND 3.0 (soundtrack) | **Approximate**: the film's English subtitles (`tos-en.srt`), the 14 cues whose middle is before 180 s, the first at 23 s, 97 normalized words | 3 |
| `ami` | AMI ES2002a: the overhead camera, MPEG-4 Part 2 352x288 at 25 fps, AVI (1,248.1 s), muxed by stream copy with the Mix-Headset WAV, 16 kHz mono (1,272.6 s), into Matroska | 1,272.6 s | CC BY 4.0 | AMI's manual word-level transcription, 2,455 normalized words, read as the [audio finding](audio.md) reads it | 1 |
| `nasa` | NASA, ISS Expedition 60 in-flight event with Pittsburgh stations: H.264 640x360 at 30 fps, AAC 22.05 kHz stereo, MP4 | 1,531.2 s | NASA media, not subject to copyright in the US | **Approximate**: NASA's live captions, upper case, `>>` at each turn, which end at 18:36; only the windows that end by then are scored, against the cues whose middle is before the last of them ends | 1 |

Each file is in the test corpus at `~/docs/video/` or `~/docs/audio/long/`, and the run refuses it
unless its SHA-256 and size are the corpus manifest's (`~/docs/MANIFEST.json`). None is committed.
A file's run writes its frames' scratch directory beside the source, so each source is copied, or
muxed, into a temporary directory first: the corpus is only read.

The brief's two sources are Tears of Steel (3 runs) and AMI (1 run). It allowed the NASA in-flight
event; the corpus task had placed it, with its manifest entries, before the run, so it was run
once. AMI's camera ends 24.5 s before its audio: the last 24.5 s yield windows and no frames.

## What was measured

### Setup

- **Host**: the laptop, Intel Core Ultra 9 185H, 22 logical CPUs, 30 GiB, Linux 7.2.15; go 1.27.3,
  no race detector. ffmpeg and ffprobe are the host's FFmpeg 9.0.2 through the exec runner. The
  container runner's image, `linuxserver/ffmpeg:9.0.2-cli-ls89` by digest, isn't used here; the
  integration tier holds the two runners' windows and frames byte-identical (slices 1, 4, and 5).
- **Models**: gb10-2's router, llama.cpp b12098, set A. `ggml-org/gemma-4-E4B-it-GGUF:Q8_0` (4
  slots) transcribes each window through the transcription endpoint, greedy, in English, with the
  endpoint's own instruction, `audiotest.Transcriber`'s request. `google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0`
  (4 slots) describes each frame, its PNG beside office's describe prompt, "Describe this picture in
  two or three sentences.", temperature 0, reasoning off, output capped at 512 tokens.
- **Slots**: the harness keeps at most 4 requests in flight to each model, the router's slots,
  taking a slot in the request's goroutine so the stream's loop never waits on a model.
- **Settings**: every request takes media's defaults. Windows: the tracker's 10th percentile of
  10 ms frame energy over a rolling 4 s, plus 12 dB; silences of at least 0.3 s; windows at most
  30 s, overlapping by 2 s, each cut at the middle of the latest silence before its limit, or at
  the limit. Frames: the first frame, each whose select scene score is past 0.2, and each 10 s or
  more after the last taken; PNG at the source's resolution (no largest side).

### A source's steps

`TestFindingsStreams` runs one source a go test run, in this order:

1. **Unpaced**: the source read once as fast as ffmpeg decodes it, with no model, for the stream's
   own speed and its windows and frames, by SHA-256.
2. **The file path**: the source's audio stream through audio's file path, `audiotest.Run`, the
   audio finding's harness: planned whole, its windows cut and transcribed 4 at once, and joined.
   Twice: under silencedetect, the file path's default and the audio finding's detector, and under
   the tracker, the live path's detector. The prepared file holds the corpus' audio stream
   unchanged (AMI's WAV is copied into the Matroska), so the file path and the live path read the
   same samples.
3. **Paced runs**: the source read at real time, each window and frame sent to its model as the
   stream yields it, `Join` re-run as each transcript lands and its settled words, those before the
   latest seam, logged. Each event's lags are recorded, a `lag` line each.
4. **A cancellation**: one more paced run, cancelled midway, below.

### The lags

For each window and frame, from wall-clock times on the laptop:

- **Arrival to event**: from the arrival from ffmpeg of the event's last sample (a window's end) or
  its frame (the PNG's frame report) to the stream yielding the event. A frame is yielded once its
  PNG is whole. A window is yielded once it is fixed: its plan can't change, which is once the
  stream passes the window's limit, 30 s after its start, or the source ends, since a later
  silence before the limit would move its end. So a window that ends in a silence well before its
  limit waits for the stream to reach the limit.
- **Event to answer**: from the yield to the model's answer, split into **queued**, waiting for a
  slot, and **request**, the request itself.

Each distribution is given as its median, 90th percentile, and maximum, by nearest rank. A run also
counts the events yielded while all 4 of their model's slots were busy, and the most requests in
flight at once.

### Scoring

The word error rate is the audio finding's, `textscore.NormalizedWordErrors` over the truth's
normalized words, with the same fillers dropped and no numbers verbalized; AMI's truth is read by
the same reader, moved for this task to `internal/corpustest`, so the live path's WER and the file
path's read beside the audio finding's 0.308. Tears' and NASA's truths are subtitles, condensed
speech, and score approximately: an upper bound inflated by the truth, good for comparing the paths
with each other on the same source, not as an absolute rate.

### The cancellation

A paced run of each source is cancelled at the first event yielded half the source's length into
the run, 250 ms after that event's request is sent, so a request is on the wire. Recorded: how
long after the cancel the stream ended and the last request returned; how the requests in flight
and those waiting for a slot ended; and what was left: the media directory's files against the
events yielded, any entry beside the source (the frames' scratch directory), and any ffmpeg process
still reading the source (`pgrep`). The measured runs above complete; the cancellation is a run of
its own, so its lags don't cut the measured ones short.

## Results

### The events

| Source | Windows (cut) | Frames (reason) | Paced runs equal to the unpaced read | Stream ended | Last answer | Failed |
|---|---|---|---|---|---|---|
| `tears` | 7 (6 silence, 1 end) | 35 (1 first, 30 scene, 4 interval) | yes, every run, by SHA-256 | 179.4 to 179.5 s | 181.1 to 181.4 s | 0 |
| `ami` | 49 (48 silence, 1 end) | 125 (1 first, 124 interval) | yes | 1,247.6 s | 1,250.4 s | 0 |
| `nasa` | 62 (59 silence, 2 limit, 1 end) | 156 (1 first, 4 scene, 151 interval) | yes | 1,530.8 s | 1,532.4 s | 0 |

Paced or unpaced, a source gives the same windows and frames, byte for byte: reading at real time
changes when an event comes, not what it is. Tears of Steel's 30 scene frames are its cuts (slice
4 found select's 0.15 to 0.3 take the same 30 frames at the same times as scdet's default); AMI's
fixed overhead camera has no cut past 0.2, so every frame after the first is the 10 s interval's.
No window, frame, or request failed in any run, and every stream ended cleanly.

**AMI's paced read ended at 1,247.6 s, not 1,272.6 s.** Its camera ends 24.5 s before its audio,
and once the video stream ended, ffmpeg's `-re` delivered the rest of the audio at once: windows 47
and 48, 1,234.8 to 1,272.6 s, were yielded at 1,247.6 s, 1 and 4 ms after their last samples
arrived. Up to then the pacing held: each window's last sample arrived about 0.5 s before its
source time, the read's initial burst, and each frame 0.45 s before. A source whose streams end
apart is paced only while its video lasts.

### Lags: windows

The median, 90th percentile, and maximum over a run's windows, in seconds. Tears' three runs gave
the same arrival lags within 4 ms, and request times within 0.3 s; its row is run 1's, the others
in the JSON.

| Source | Windows | Arrival to event | Event to answer | Queued | Request | Last transcript after its window's last sample |
|---|---|---|---|---|---|---|
| `tears` | 7 | 1.88 / 3.67 / 3.67 (mean 1.67) | 1.71 / 2.58 / 2.58 | 0 | 1.71 / 2.58 / 2.58 | 1.68 to 1.93 s |
| `ami` | 49 | 1.20 / 4.44 / 6.28 (mean 1.77) | 2.60 / 3.43 / 4.52 | 0 | 2.60 / 3.43 / 4.52 | 1.18 s |
| `nasa` | 62 | 2.03 / 7.90 / 15.03 (mean 3.22) | 2.49 / 3.57 / 4.56 | 0 | 2.49 / 3.57 / 4.56 | 1.62 s |

**Arrival to event is the plan's wait, not the stream's.** A window is fixed once nothing later can
move its end: once the stream passes its 30 s limit, 30 s after its start, or the source ends. A
window that ends in a silence well before its limit waits for the stream to reach the limit.

**A silence open across a limit no longer holds a window.** At commit `c3a7f72`, the first run's,
the tracker reported a silence only once it had ended, and the planner advanced only to the start
of one still open, so a window whose limit fell inside a silence waited for that silence to end.
On NASA's event that held window 6 (135.5 to 159.4 s) 65.3 s after its last sample, until a
silence ended at 224.2 s. PlanWindows takes a silence that crosses a window's limit only up to the
limit, so the window's end never depends on how long the silence lasts past it: commit `5a8f16c`
has the tracker report a silence still open once it is MinSilence long, and the planner fix each
window as the stream passes its limit inside it. On the NASA run after the fix, the same 62
windows, byte for byte, each came at its limit: window 6 6.1 s after its last sample, and window 3
(68.6 to 97.1 s) 1.6 s where it had waited 35.9 s. The longest waits are now windows that end well
before their limits, in the event's first minutes of music, which E4B transcribed as "[Music]" and
"No speech detected": window 0 (0 to 15 s) waited 15.0 s and window 4 (95.1 to 110.3 s) 14.8 s.
After the first 4 minutes no window waited more than 9.6 s.

On AMI, whose talk leaves a silence near every limit, each window was yielded at its limit, 0.2 to
6.3 s after its last sample; no open silence held one. On Tears of Steel after the fix, the same 7
windows, byte for byte, each came at its limit in all three runs: windows 1, 2, 4, and 5 2.3, 3.7,
0.9, and 2.8 s after their last samples, and no window waited more than 3.7 s; before it, open
silences had held them 5.4, 14.8, 3.1, and 21.2 s, the last until the source ended at 180 s. A
window cut at the source's end, whose wait is no plan's, came 1 to 8 ms after its last sample: the
ffmpeg run, the tracker, and the WAV writing add nothing visible.

**Event to answer is E4B alone.** No window ever waited for a slot: at most 2 transcriptions were
in flight at once, of E4B's 4, since a window comes every 25 to 30 s and takes 0.8 to 4.6 s. So
the end-to-end lag from a window's last sample to its transcript is the plan's wait plus about 2 to
3 s: on AMI a median of 3.8 s, 7.3 s at the 90th percentile, and 7.6 s at most; on Tears of Steel a
median of 3.7 to 3.8 s and at most 4.8 s (22.7 s before the fix); on NASA a median of 4.5 s, 10.2 s at the 90th
percentile, and 17.9 s at most.

### Lags: frames

| Source | Frames | Arrival to event | Event to answer | Queued | Request | Waited for a slot | Most in flight |
|---|---|---|---|---|---|---|---|
| `tears` | 35 | 0.11 / 0.14 / 0.16 to 0.17 | 1.42 to 1.74 / 1.58 to 1.98 / 1.80 to 3.76 | 0 | as event to answer | 0 | 2 |
| `ami` | 125 | 0.02 / 0.02 / 0.04 | 1.46 / 1.75 / 3.90 | 0 | as event to answer | 0 | 1 |
| `nasa` | 156 | 0.04 / 0.06 / 0.07 | 1.43 / 1.82 / 3.63 | 0 | as event to answer | 0 | 2 |

A frame is yielded once ffmpeg's image2 muxer has renamed its PNG whole and the metadata filter has
reported it: 20 ms on AMI's 352x288 frames, 40 to 70 ms on NASA's 640x360, 110 to 170 ms on Tears'
1150x480: the PNG's encoding, which grows with the frame. **26B-A4B kept up with every frame**:
Tears of Steel's 35 frames in 180 s, a cut every 5 s on average and 2 in flight at most, and AMI's
and NASA's one every 10 s, never waited for a slot; each description took 1.5 to 2 s, under 4 s at
worst. From a frame's arrival to its description: a median of 1.5 to 1.9 s on Tears of Steel,
1.5 s on AMI, and 1.5 s on NASA, at most 3.9 s.

Every description describes its frame: Tears' first frame, black, is "a solid, uniform black
rectangle"; AMI's are "an overhead view shows a group of people gathered around a large,
oval-shaped wooden table in a meeting room" and the like; NASA's, "two astronauts in blue shirts and
khaki pants are floating in a space station, smiling at the camera".

### Word error rate: live against the file path

| Source | Truth words | File path, silencedetect | File path, tracker | Live, tracker (paced) |
|---|---|---|---|---|
| `tears` (approximate) | 97 | 0.340 (33), 7 windows | 0.361 (35), 7 windows | **0.361 (35) in all 3 runs** |
| `ami` | 2,455 | 0.307 (754), 50 windows | 0.323 (793), 49 windows | **0.322 (791)** |
| `nasa` (approximate, to 18:36) | 2,580 to 2,583 | 0.467 (1,207), 67 windows, 47 scored | 0.478 (1,234), 62 windows, 45 scored | **0.476 (1,229)**, 45 scored |

**The live path transcribes as well as audio's file path under the same detector**: on AMI 0.322
against 0.323, on Tears of Steel 0.361 against 0.361. Its windows are the file path's under the
tracker, plan for plan (behavior 8's property, here on real sources), so the transcripts differ
only by E4B's own run-to-run variation. **The tracker costs about 0.015 against silencedetect on
AMI**, 0.322 against 0.307, the audio finding's detector (whose own runs gave 0.308): 49 windows
against 50, cut at other silences, with 19 of 48 seams unmatched. On Tears of Steel the difference,
0.361 against 0.340, is 2 edits of 97.

Tears' truth is the subtitles from 23 s on: the opening countdown ("We have main engine start.
Four, three, two, one. Go.") is in every transcript and in no cue, and E4B wrote "[No speech
detected]" and "[Music]" for stretches of score, which count as errors too. Its WER bounds the
error from above; the paths' equal rates are the result.

NASA's live transcript, 0.476, is the tracker's file path's, 0.478, over the same 62 windows. The
first run gave the same live rate, 1,229 edits, but 0.505 through the file path, E4B's variation,
not the plan's: an 83-word passage ("to say hi to everybody there in Western Pennsylvania ...")
appeared twice in that file path's join. Both file path runs answered one window "Please provide
the audio you would like me to transcribe.", where the live runs have "[Music] No speech
detected.". Against silencedetect's 67 windows, 0.467, the live path is 0.009 worse. The captions are condensed and
upper case, so the rates are high; they compare the paths, not E4B.

The settled transcript only grew in every run: each step's words before its latest seam led every
later step's join (7 steps a Tears run, 48 on AMI, 62 on NASA).

### The cancellation

| Source | Cancelled after | At, into the run | Events by then | Stream ended after the cancel | In flight | Waiting | Files left | Beside the source | ffmpeg left |
|---|---|---|---|---|---|---|---|---|---|
| `tears` | frame 19's request | 92.3 s | 23 | 264 ms | 1, cancelled, returned in 0.13 ms | 0 | 23, the 23 yielded | nothing | 0 |
| `ami` | frame 64's request | 639.8 s | 89 | 437 ms | 1, cancelled, returned in 0.11 ms | 0 | 89, the 89 yielded | nothing | 0 |
| `nasa` | frame 78's request | 771.4 s | 110 | 345 ms | 1, cancelled, returned in 0.10 ms | 0 | 110, the 110 yielded | nothing | 0 |

**Each cancellation stopped cleanly.** The stream yielded `context.Canceled` once and ended 0.26 to
0.44 s after the cancel, the time to stop ffmpeg and drop the window it was filling; the request on
the wire returned the context's error in under a millisecond; every request sent before had been
answered. The media directory held exactly the windows and frames yielded, the frames' scratch
directory beside the source was gone, and no ffmpeg process was left reading it. Midway, the
models were idle but for the request the cancel waited for: nothing was queued.

### The live protocols

Slice 5's integration tier (`media/sources_integration_test.go`, commit `acad3a0`) carries a 36 s
excerpt of the committed audio-and-video fixture from a host `ffmpeg -re` sender over loopback,
through both runners, and holds each live source's windows and frames to the file source's, window
for window and frame for frame, SHA-256s included:

| Source | Carried as | Windows and frames against the file's |
|---|---|---|
| reader | the excerpt's open file on ffmpeg's stdin | equal |
| `srt://` | Matroska live, FLAC audio and MPEG-4 Part 2 video copied | equal but the end: **the last 1.024 s is lost**, so the last window is the file's cut short, its samples a prefix |
| `rtmp://` | enhanced FLV, FLAC copied, lossless H.264 without B-frames | equal |
| `http://` | Matroska live, as srt | equal |
| live HLS over `http://` | fragmented MP4 segments from a Go server | equal |
| `udp://` | Matroska live, as srt | equal |
| `udp://` MPEG-TS | the video alone (TS can't carry FLAC or 16 kHz PCM, and a lossy codec's priming shifts the audio) | frames equal |

The srt loss is SRT's live mode: it drops what it hasn't delivered when the sender closes, and
ffmpeg's libsrt can't linger in live mode, so the last Matroska cluster never arrives. A live srt
source loses its tail at the sender's close, not mid-stream. Each run allows only its scheme's
protocols, and an http playlist that names an https segment is refused (`TestPlaylistConfined`); a
URL through the container runner joins the docker network the caller names (`TestNamedNetwork`).
These are pass-or-fail results of the integration tier, not timings.

### The tracker's tuning

`mise run findings-floor` (`floor.log`) plans three long sources, each unchanged, stepped up 12 dB
from 600 s, and with pink noise added from 600 s, and counts the windows cut at the 30 s limit,
where a window found no silence to end in. A window cut at the limit splits speech. Windows cut at
the limit, AMI / Earnings-21 / Earnings-22:

| Detector | Unchanged | +12 dB step | Pink noise |
|---|---|---|---|
| **The tracker's defaults, p10 over 4 s, +12 dB** | **0 / 0 / 2** | **0 / 0 / 2** | **0 / 0 / 1** |
| silencedetect, the file path's default (the mean volume less 15 dB) | 0 / 0 / 2 | 0 / 0 / 3 | 24 / 0 / 2 |
| A threshold fixed from the first 30 s | 2 / 0 / 10 | 25 / 9 / 87 | 26 / 0 / 11 |
| The tracker, p10 over 30 s, +12 dB | 0 / 0 / 6 | 0 / 0 / 6 | 0 / 0 / 4 |
| The tracker, p10 over 30 s, +6 dB (the plan's untuned setting) | 4 / 1 / 10 | 4 / 1 / 10 | 2 / 0 / 7 |

Of the windows, AMI has 49 to 54, Earnings-21 96 to 109, and Earnings-22 153 to 167, by setting.
On the two committed level-change fixtures, whose 7 gaps after the change are known, the defaults
find all 7 after a gain step and all 7 after added noise; a threshold fixed from the first 30 s
finds none of them; a 30 s floor window finds 4 and 3 at +12 dB, and none at +6 dB, since it lags
the change by about its own length. The planning round's simulation reported a fixed threshold
cutting 66 of 92 Earnings-21 windows at the limit under added noise; measured here it cut 0 of
101, and only measured numbers are cited.

### Headroom

The stream's own work, with no model, from the unpaced reads (step 1), each the whole source
through one ffmpeg run, decoding the audio and video, finding the silences, planning and writing
the windows, and scoring, selecting, and writing the frames as PNG:

| Source | Length | Picture | Windows | Frames | Wall, unpaced | Times real time |
|---|---|---|---|---|---|---|
| `tears` | 180.0 s | 1150x480 H.264, 24 fps | 7 | 35 | 4.11 s | 44 |
| `ami` | 1,272.6 s | 352x288 MPEG-4 Part 2, 25 fps | 49 | 125 | 2.51 s | 507 |
| `nasa` | 1,531.2 s | 640x360 H.264, 30 fps | 62 | 156 | 7.56 s | 202 |

The stream keeps up with a live source by 44 to 507 times: decoding and scoring every video frame
for its scene change dominates, and grows with the picture, so Tears of Steel's 1150x480 is the
slowest. The task's earlier standalone measurements, ffmpeg's decode and scene detection alone,
2,100 times real time on AMI's camera and about 240 times on Tears of Steel, were taken without the
PNG writing, the windows, and the pacing machinery; through media, end to end, the figures are
these. A paced run's own cost is in the lags above: a frame is yielded 20 to 170 ms after ffmpeg
reports it, and a window cut at the end 1 to 8 ms after its last sample.

The tracker alone, fed AMI's 1,272.6 s of PCM in 32 KiB pieces, took 26 to 27 ms in 3 runs, about
48,000 times real time, and found the same 526 silences as `floor.log`. That was an ad hoc
measurement in this session's working tree, not a committed test.

## Known limits

- **Paced files, not live sources.** Every measured run reads a file at real time through ffmpeg's
  `-re`. The live protocols are held equal to the file in the integration tier, over loopback,
  with a 36 s excerpt; none was timed here, and none crossed a real network.
- **A muxed source is paced only while its video lasts.** ffmpeg's `-re` stops pacing an input
  once its video stream ends, so AMI's last 24.5 s of audio, past the camera's end, arrived at
  once. It is a limit of the source and of ffmpeg's pacing, not of the package, and isn't fixed.
- **The window's wait is the plan's policy.** A window waits for the stream to pass its 30 s limit,
  inside a silence or not: up to 30 s for a window that ends early in a long silence, 15 s on
  NASA's event. A run of silent frames open across the limit, shorter than MinSilence so far,
  holds the window until it is a silence or ends, at most 0.3 s more. A shorter limit would cut the
  wait; it isn't in the package, and wasn't measured. The first window can't be yielded before 30 s
  of audio.
- **One or three runs, one router build, one laptop.** Tears was run 3 times, AMI and NASA once;
  greedy decoding still varies by a few words between runs, from batching across the slots.
- **The models were never pressed.** At these sources' rates no request waited for a slot; a
  source with cuts more often than one a second, or several sources at once, would queue, and no
  run measured where 26B-A4B's 4 slots run out.
- **Truths**: Tears' and NASA's are subtitles and captions, approximate upper bounds; AMI's
  speaker order at overlaps, as in the audio finding.
- **The cancellations came when the models were idle** but for the one request they waited for;
  none cancelled with requests queued.
- **The tracker's throughput** is an ad hoc measurement, not a committed test.
- **Tears of Steel and the NASA event were run before the fix and after it**; AMI, which no open
  silence held, only before. The NASA event's truth is its captions to 18:36.

## What it answers for evidence 13

Answered:

- A real-time-paced audio and video source is processed as it arrives: one ffmpeg run per source
  yields its windows and frames as one lazy event stream, the same windows and frames, byte for
  byte, as an unpaced read, on three real videos up to 25.5 minutes.
- Per-window latency: from a window's last sample to its event, the plan's wait for the window's
  limit, 0 to 6.3 s on a meeting, up to 3.7 s on a film, and up to 15.0 s on NASA's event; from
  the event to E4B's transcript, 0.8 to 4.6 s, with no queueing. Per frame: 20 to 170 ms to the
  event and 0.5 to 3.9 s, a median of 1.4 to 1.7 s, to
  26B-A4B's description, with no queueing either.
- The live transcript is as accurate as audio's file path under the same detector: AMI 0.322
  against 0.323. Against the file path's default detector, silencedetect, it is 0.015 worse on
  AMI (0.307).
- Cancellation: the stream ends within 0.26 to 0.44 s of a cancel with the context's error, the
  request on the wire returns it at once, and nothing is left but the windows and frames yielded.

Not contradicted. Not answered here: a live network source's latency, and the browser's consumption
of the event stream (evidence 15).

## Rerunning

From this repository's root, on a laptop (never in CI, never in the check), with set A loaded on
gb10-2 (`mise run host-update` in the root):

```sh
mise run findings-streams
```

`mise run findings-streams` (`scripts/findings-streams.sh`) runs the findings-tagged tests in
package media, without the race detector: `TestFindingsStreams` runs each source as a go test run
of its own, capped at 2 hours, and `TestFindingsStreamsReport` logs the tables from their results.
The paced runs take real time: about 11 minutes for Tears of Steel, 33 for AMI, and 40 for NASA.
`STREAMS_FINDINGS_SOURCES` (`tears ami nasa`) and `STREAMS_FINDINGS_RUNS` (each source's own: 3, 1,
1) choose the sources and runs, and `DOCS` (`~/docs`) the corpus. A plain run keeps nothing; to keep
the results, record them:

```sh
STREAMS_FINDINGS_RESULTS=docs/findings/streams-results mise run findings-streams
```

which writes each source's JSON, after each step, and the run's `-v` log, `findings.log`, there.
The tracker's tuning is `mise run findings-floor`, whose output is `floor.log` here.
