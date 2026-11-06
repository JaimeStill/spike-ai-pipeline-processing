# Results behind the audio finding

[`../audio.md`](../audio.md) cites these files. They come from one run of `mise run
findings-audio`, named here **findings-audio 2026-11-05**, from 2026-11-05 23:42:31 to 2026-11-06
01:29:50 UTC, recorded by setting `AUDIO_FINDINGS_RESULTS`. The run used the working tree that
became processing commit `6e4e70f` on branch `audio`, which differs from it only in one comment's
figure in `internal/textscore/speech.go`.

The host is the laptop: Intel Core Ultra 9 185H, 22 logical CPUs, 30 GiB; Linux 7.2.15 x86_64; go
1.27.3; the host's FFmpeg 9.0.2 through the exec runner. The models are gb10-2's llama.cpp router,
build b12098 (`b12098-149f5641b`), serving set A: `ggml-org/gemma-4-E4B-it-GGUF:Q8_0` (4 slots,
32,768-token context) for transcription, and `ggml-org/gpt-oss-120b-GGUF:MXFP4` (4 slots) for the
seam pass.

No audio is committed, nor any truth: each source is in the test corpus at `~/docs/audio/long/`,
checked by its SHA-256 against `~/docs/MANIFEST.json`.

| File | Test | What it records |
|---|---|---|
| `findings.log` | all three | The run's `-v` output, verbatim: each truth read, each run's recorded lines, and the report's table lines |
| `ami.json` | `TestFindingsSource` | AMI ES2002a: the source, the excerpt's truth and its method, and each run: the excerpt unsplit and windowed, the whole source windowed, its seam pass. Transcripts in full (CC BY 4.0) |
| `earnings21.json` | `TestFindingsSource` | Earnings-21 4330115, as `ami.json`, **without transcripts**: its audio isn't redistributable. The seam pass keeps only the spans it changed or refused, 10 words each |
| `earnings22.json` | `TestFindingsSource` | Earnings-22 4479944, as `earnings21.json` |
| `librivox.json` | `TestFindingsSource` | LibriVox, *The Statement of Stella Maberly*, as `ami.json`, transcripts in full (public domain) |

Times are in nanoseconds in the JSON. A seam's `per_seam` count and a span's `seams` index the
joined transcript's seams in order; a span's `from` and `to` index its words.
