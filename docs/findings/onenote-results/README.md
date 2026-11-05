# Results behind the onenote finding

[`../onenote.md`](../onenote.md) cites these files. They are one run, named here **findings-onenote
2026-11-05**: one run of `mise run findings-onenote` on 2026-11-05 from 18:08:01 to 18:13:08 UTC,
recorded by setting `ONENOTE_FINDINGS_RESULTS=docs/findings/onenote-results` and
`ONENOTE_SCALE_STORE` to mail's checkout's `.state/scale/scale.pst`, at processing commit
`96797c6` (`96797c6004fb6f29989eff5dc70547ffca3cdcf7`) on branch `onenote`.

The host is the laptop: Intel Core Ultra 9 185H, 22 logical CPUs, 30 GiB; Linux 7.2.15 x86_64;
go 1.27.3; docker server 29.9.2. The tools:

| Tool | Version | Source |
|---|---|---|
| spike-ai-pipeline-onenote | 0.1.0 | mise pin, release binary, SHA-256 `5c1e8a25577fec4fbc982902c5b99c12bfe2e34f5d19a2264864c00fabc8c81b` |
| spike-ai-pipeline-mail | 0.3.0 | mise pin, release binary, SHA-256 `7715ba5ee00ca0979416352558f7ca5db9e057aff7c3db4682115b7c596e2da9` |
| spike-ai-pipeline-office | 0.1.0 | mise pin, release binary, SHA-256 `23c88e2724d291e2dd3d4ea37d0e8ec1f67b54c55397ae284b587ea89ddc8ee6` |
| Ghostscript | 10.08.0 | host |
| ImageMagick | 7.1.2-34 Q16-HDRI | host |
| LibreOffice | `picture.LibreOfficeImage`, pinned by digest | docker, image already present |
| llama.cpp router | b11669 (`b11669-620d2225c`) | gb10-2, serving set A; the vision model `google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0` |

No store the run reads is committed: plaso's store is the one `mise run findings-email` fetched at
its pin into `.state/findings/plaso/`; the scale store is mail's generator's, in mail's checkout
(mail `v0.3.0`), content SHA-256 `d40792a45510dc0d4b7a3abc527848095d4f255bae26d853ee7f9a87119362b4`,
the pin mail's and the email finding's runs record; its byte copy was made by the run into
`.state/findings/onenote/scale-copy.pst`; the three large sections are read from
`~/docs/onenote/large/`. Working directories and file indexes were on tmpfs (`/tmp`), each removed
after its run.

| File | Test | What it records |
|---|---|---|
| `findings.log` | all | The run's `-v` output, verbatim, each step under a `# findings-onenote:` line |
| `tier-routes.log` | `TestTierRoutes` (integration tier) | Every part's event of the 119 fixture inputs, its counts by route × outcome, and the vision sample's rows |
| `docs-inspect.json` | `TestFindingsDocs` | Each ~/docs file whose manifest names a hazard or that yields a flag: covers, kind, flags with evidence, verdict; and the bomb under the lowered size |
| `ink.json`, `ink.png` | `TestFindingsInk` | The ink's PNG as the picture stage wrote it, the truth, OneNote's recognition and its score, and the three transcriptions with their scores, tokens, and times |
| `onenote-large.json` | `TestFindingsOneNoteLarge` | Each large section, the launcher's floor, and every run each way: rusage, wall time, and for the adapter its own and its children's rusage and its survey |
| `scale-dispatch.json` | `TestFindingsScaleDispatch` | The generator's record and every run each way; a dispatch's survey holds its counts, peak disk, and index size |
| `resume.json` | `TestFindingsResume` | The cursor, the Threader's size and save time, each dispatch's time and commits, and the union's checks |
| `pool.json` | `TestFindingsPool` | The 41 inputs, and each worker count's run and survey, each input's own event with when it came |

Times are in nanoseconds in the JSON; `max_rss`, `peak_disk`, and `index_bytes` are in bytes.
