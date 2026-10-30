# Results behind the documents findings

Each subdirectory holds the JSON one bake-off-tier run kept, copied from its `BAKEOFF_RESULTS`
directory. [`../documents.md`](../documents.md) cites them; every transcription is in them, beside
its scores, token counts, and status. All runs went from the laptop to gb10-2's llama.cpp router.

| Directory | Run | Date | Task |
|---|---|---|---|
| `renderers-reasoning-on/` | The renderer bake-off with the model's reasoning on, the server's default: one file per renderer and set | 2026-10-29 | `mise run bakeoff`, before reasoning went off |
| `renderers/` | The renderer bake-off with reasoning off, the ranking the page reports | 2026-10-29 | `mise run bakeoff` |
| `qat-512/renderers/` | The renderer bake-off on Google's QAT Q4_0 build at 512 image tokens, reasoning off: one file per renderer and set. The run that prompted ruling Q; its findings section comes with the 1,120-token runs | 2026-10-30 | `mise run bakeoff` |
| `corrections/` | Evidence 11's corrections matrix: one file per DPI, set, and cell | 2026-10-29 | `mise run bakeoff-corrections` |
| `placement/` | Instruction placement: one file per placement and set | 2026-10-30 | `mise run bakeoff-placement` |
| `flow/` | The staged two-pass flow and its `none` and `all-five` baselines: one file per variant, set, and run | 2026-10-30 | `mise run bakeoff-flow` |
| `tiles/` | Tiles and page 7's footer band: one file per DPI | 2026-10-30 | `mise run bakeoff-tiles` |

The scanned set's transcriptions are of pages from herald's
`_project/marked-documents/marked-documents.pdf`, scans already public in herald's repository
(`document/testdata/README.md`).
