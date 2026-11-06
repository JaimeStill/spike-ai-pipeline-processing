# testdata

The audio package's speech fixture and its reference transcript. The audio package's tests and
dispatch's audio stage tests probe, plan, and cut the fixture, through both runners in the
integration tier, which also transcribes it against the transcript. Both files are committed and
never generated in the check; `mise run fixtures:audio` (`scripts/fixtures-audio.sh`) rebuilds
them from the test corpus at `~/docs`, checking each source against the SHA-256 the corpus's
`MANIFEST.json` pins and each output against the SHA-256 below.

| File | Source | License | Bytes | SHA-256 |
| --- | --- | --- | --- | --- |
| `speech.flac` | LibriSpeech dev-clean 2277-149896-0000 to 0003, through `~/docs` | CC BY 4.0 | 765404 | `d5b77b3875e42a1907834fb241a24bf85cf7c922bfd4ab59b86de6f05c4d084a` |
| `speech.truth.txt` | the same utterances' LibriSpeech transcripts, through `~/docs` | CC BY 4.0 | 730 | `2902ea6452b2f29c53bbc2b5541fb036e449c5bdb17b5dc52b6d479c3e7457ea` |

- `speech.flac`: 49.82 s of 16 kHz mono 16-bit FLAC. The four utterances, 6.59, 7.145, 4.835, and
  2.84 s long, are read in order twice, eight in all, with one second of digital silence after each
  but the last, so the seven inserted gaps begin at 6.59, 14.735, 20.57, 24.41, 32.0, 40.145, and
  45.98 s. silencedetect at its default threshold, 15 dB under the mean volume of -25.1 dB, finds
  each gap within a longer silence, the utterances' own pauses beside it, and more besides; the
  plan cuts the one window boundary in the gap at 24.41 s. The host's ffmpeg n9.0.2 and
  `audio.FFmpegImage`'s 9.0.2 build the same bytes.
- `speech.truth.txt`: the eight utterances' transcripts, one a line in the fixture's order, as
  LibriSpeech gives them, upper case without punctuation, from the corpus's
  `librispeech-dev-clean.truth.json`.

The sources are the corpus's `audio/short/librispeech/librispeech-dev-clean-2277-149896-000N.flac`
and `librispeech-dev-clean.truth.json`, which its manifest marks CC BY 4.0, verified, and
redistributable, from the Hugging Face dataset `openslr/librispeech_asr` (validation.clean).

Attribution: LibriSpeech ASR corpus, Vassil Panayotov, Guoguo Chen, Daniel Povey, and Sanjeev
Khudanpur, <https://www.openslr.org/12>, licensed under CC BY 4.0,
<https://creativecommons.org/licenses/by/4.0/>; its audio is read from LibriVox recordings, in the
public domain. The fixture concatenates four of its utterances twice with inserted silences, and
the transcript file lists their transcripts in that order.
