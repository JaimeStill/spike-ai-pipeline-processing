# spike-ai-pipeline-processing

The processing layer of spike-ai-pipeline. The repository is managed with the marathon
workflow; start from `context/README.md`.

- **Standards:** `STANDARDS.md`, with pointers into the architecture repository.
- **Check:** `mise run check` (hermetic). `mise run integration` runs the integration-tagged
  tests against the router `SPIKE_ROUTER_URL` names, gb10-2's by default. The bake-off tier,
  the bakeoff-tagged tests, runs against the same router, never in CI: `mise run bakeoff`,
  `bakeoff-corrections`, `bakeoff-placement`, `bakeoff-flow`, and `bakeoff-tiles` (`README.md`).
- **Currency:** `mise run currency`.
- **Goals:** the spike's tasks live in the coordinator's roadmap,
  `standards-lab/context/roadmap.toml`, under `experiment.ai.spike-ai-pipeline`. Its goal record
  is in the root repository, `spike-ai-pipeline`, at
  `context/goals/experiment.ai.spike-ai-pipeline.md`.
- **Dependencies:** published versions only, never a `replace` directive; spike-ai-pipeline-client
  by tag, v0.4.0, a test-only requirement. System tools are found at run time, never linked.
- **References:** the repositories this spike reads are keys in the coordinator's
  `references.toml` and `references.local.toml`: tau-protocol, tau-format, tau-provider,
  tau-agent, herald, spike-harness-driver, spike-model-hosting, spike-model-engines, go-core,
  go-database, sqlate, go-web-sdk, and go-observability. The spike reads them and never writes to
  them.
