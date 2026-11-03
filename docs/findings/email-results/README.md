# Results behind the email finding

[`../email.md`](../email.md) cites these files. The findings tier's run is one run of
`mise run findings-email` on 2026-11-03, 18:47:40 to 18:50:43 UTC, from the laptop (Intel Core
Ultra 9 185H, 22 logical CPUs, 32 GB; Linux 7.2.15 x86_64, go 1.27.3), with mail 0.1.0 from the
mise pin, the release binary (SHA-256 `b75a19203199b87f5d4f5e21eacca82f1b97f0bf8ac5ddc4d8e22e3bc1a0b0b9`),
recorded by setting `EMAIL_FINDINGS_RESULTS`; the integration tier's run is a `-v` run of the email
package's integration-tagged tests the same day, ending 18:44:22 UTC, after a full
`mise run integration` that passed. Both are at processing commit 464fa86. Neither store the findings tier reads is committed: plaso's store is fetched at its pin,
and the scale store is rebuilt from mail's tag (`../email.md`, "Rerunning").

| File | Test | What it records |
|---|---|---|
| `findings.log` | `TestFindingsFixtures`, `TestFindingsPlaso`, `TestFindingsScale` | The findings run's `-v` output, verbatim, under a header: each fixture's counts, threads, and scores, and each timed run of the large stores, with each way's median and peak |
| `fixtures.json` | `TestFindingsFixtures` | Each of the 23 fixtures: its records, warnings, attachments by route, thread counts, its threads rendered, and its scores against the tests' hand truth |
| `plaso.json` | `TestFindingsPlaso` | plaso's store, its pin, the launcher's floor, and every run: the launcher's rusage and wall time, and, for the adapter's ways, the adapter's own rusage, mail's, and the survey (records, routes, thread counts, the read's time) |
| `scale.json` | `TestFindingsScale` | The scale store, its generator record (`scale.json` beside it: parameters, counts, content SHA-256), and every run, as `plaso.json`'s |
| `routes-to-stages.log` | `TestRoutesToStages` (integration tier) | An excerpt of the run's `-v` output, verbatim: each attachment carried to its stage, the built .msg's by-value `forwarded.msg` read through the CLI again, and each of the 15 distinct pictures sent once to Gemma 4 26B-A4B on gb10-2's router |

Each run's times are in nanoseconds in the JSON; `max_rss` is in bytes.
