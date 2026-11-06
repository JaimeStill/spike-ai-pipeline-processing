# Results behind the streams finding

[`../streams.md`](../streams.md) cites these files. They come from two runs of `mise run
findings-streams`, each recorded by setting `STREAMS_FINDINGS_RESULTS`, on branch `streams`, and
one run of `mise run findings-floor`:

- **findings-streams 2026-11-06**, all three sources, from 2026-11-06 16:25:59 to 17:50:25 UTC, at
  processing commit `c3a7f72`: `findings.log`, `tears.json`, and `ami.json`. Its NASA section of
  `findings.log` is the measurement before the open-silence fix, superseded.
- **findings-streams nasa 2026-11-06**, NASA alone (`STREAMS_FINDINGS_SOURCES=nasa`), from
  2026-11-06 18:07:48 to 18:48:02 UTC, at processing commit `5a8f16c`, which fixes a window inside
  a silence still open once the stream passes its limit: `findings-nasa.log` and `nasa.json`. Its
  windows are the first run's, by SHA-256.
- `mise run findings-floor` at commit `c3a7f72`, from 2026-11-06 16:24:51 to 16:25:54 UTC:
  `floor.log`.

The host is the laptop: Intel Core Ultra 9 185H, 22 logical CPUs, 30 GiB; Linux 7.2.15 x86_64; go
1.27.3; the host's FFmpeg 9.0.2 through the exec runner. The models are gb10-2's llama.cpp router,
build b12098 (`b12098-149f5641b`), serving set A: `ggml-org/gemma-4-E4B-it-GGUF:Q8_0` (4 slots,
32,768-token context) for the windows, and `google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0` (4
slots) for the frames.

No media is committed, nor any truth: each source is in the test corpus at `~/docs/video/` and
`~/docs/audio/long/`, checked by its SHA-256 against `~/docs/MANIFEST.json`, and copied, or muxed,
into a temporary directory for the run.

| File | Test | What it records |
|---|---|---|
| `findings.log` | `TestFindingsStreams`, `TestFindingsStreamsReport` | The first run's `-v` output, verbatim: each event as it was yielded and answered, each settled step's new words, a `lag` line per event, each step's recorded line, and the report's table lines; its NASA section is the run before the fix |
| `findings-nasa.log` | `TestFindingsStreams`, `TestFindingsStreamsReport` | The NASA run's `-v` output, verbatim, as `findings.log`, its report NASA's lines alone |
| `tears.json` | `TestFindingsStreams` | Tears of Steel's 180 s clip: the source and settings, the unpaced read, the file path under each detector, the 3 paced runs with each event's lags, their distributions, the transcript and every frame's description, and the cancellation |
| `ami.json` | `TestFindingsStreams` | AMI ES2002a's overhead camera muxed with its Mix-Headset WAV, as `tears.json`, 1 paced run |
| `nasa.json` | `TestFindingsStreams` | NASA's ISS Expedition 60 in-flight event, as `tears.json`, 1 paced run, scored to 18:36 where its captions end; from the NASA run, after the fix |
| `floor.log` | `TestFindingsFloor` | `mise run findings-floor`'s output, verbatim: the tracker's tuning grid over the level-change fixtures and three long sources |

Times are in nanoseconds in the JSON, and in milliseconds in a `lags` record and a `lag` line. A
`lag` line's and record's fields: `arrival_to_event_ms`, from the event's last sample's or frame's
arrival from ffmpeg to the stream yielding it; `event_to_answer_ms`, from the yield to the model's
answer, `queued_ms` of it waiting for one of the model's 4 slots and `request_ms` the request.
