# Results behind the formats finding

[`../formats.md`](../formats.md) cites these files. They are one run, named here **findings-formats
2026-11-07**: one run of `mise run findings-formats` on 2026-11-07 from 02:55:03 to 02:57:41 UTC,
recorded by setting `FORMATS_FINDINGS_RESULTS=docs/findings/formats-results`, at processing commit
`d50f5a4` (`d50f5a4ad9fa7d049e55d608915dc5f8d4f60db5`) on branch `formats`, the commit that adds
the finding's tests, script, and task. No model was called.

The host is the laptop: Intel Core Ultra 9 185H, 22 logical CPUs, 30 GiB; Linux 7.2.15 x86_64;
go 1.27.3; docker server 29.9.2. The tools:

| Tool | Version | Source |
|---|---|---|
| 7-Zip's 7zz | 26.05 | mise pin, ip7z/7zip's GitHub release, its digest checked |
| spike-ai-pipeline-mail | 0.3.1 | mise pin, release binary |
| spike-ai-pipeline-office | 0.1.0 | mise pin, release binary |
| qpdf | 12.4.2 | host |
| poppler's pdfdetach | 26.10.0 | host |
| Ghostscript | 10.08.0 | host |
| ImageMagick | 7.1.2-34 Q16-HDRI | host |
| LibreOffice | `picture.LibreOfficeImage`, gotenberg/gotenberg:9.1.0-libreoffice by digest | docker, image already present |

The ~/docs inputs were read in place, each checked by its SHA-256 against the corpus manifest
(`internal/corpustest`); none is committed. Working directories and file indexes were on tmpfs
(`/tmp`), each removed after its run.

| File | Test | What it records |
|---|---|---|
| `findings.log` | all | The run's `-v` output, verbatim, under a header, each step under a `# findings-formats:` line: the unit and integration tests that hold the new routes to their truth, then `TestFindingsFormats`, one line per part and per check, and `TestFindingsFormatsCost`, each timed run and each input's medians |
| `formats.json` | `TestFindingsFormats` | Every input each way, `fixtures`, `docs`, `fixtures, no 7-Zip`, `docs, no 7-Zip`, and `fixtures, lowered thresholds`: its group, size, dispatch time, and every part with its place, route, stage, outcome, reason, flags, and outputs (Meta, a record's value, a text or Markdown output's text to 8 KiB, an image's pixel size), and the input's checks against the truth, each its want and got |
| `formats-cost.json` | `TestFindingsFormatsCost` | Each ~/docs input and the conversation store, the launcher's floor, and its three runs: the launcher's rusage and wall time, the dispatching process's own and its children's rusage, and its dispatch survey (parts by route × outcome, dispatch time, peak disk, index size) |

Times are in nanoseconds in the JSON; `max_rss`, `peak_disk`, and `index_bytes` are in bytes.
