# spike-ai-pipeline-processing

The processing layer of spike-ai-pipeline, released by tag and built on by the root; it requires
spike-ai-pipeline-client at v0.4.0 for its tests alone, the vision harness the integration and
bake-off tiers run on. This context records only working
knowledge the code and the README do not express; the spike's question and evidence live in the
root repository's `context/README.md`.

## Capability map

- **tools**: the Tool contract, its exec runner and its locked-down container runner (the docker
  CLI, digest-pinned images), the minimum version check, and cancellation by process group and
  docker stop, and a run's CPU usage (no peak memory: the Go parent's shows through).
- **documents**: pre-inference preparation of a PDF's pages: Ghostscript, the renderer bake-off's
  winner, for page counts, pages and regions as PNG, and text layers; ImageMagick corrections in a
  fixed order, their catalog for a prompt, and Assess's measures; and Preparer.Images, the lazy,
  ordered, concurrent page stream over them. Inference is test code: the vision harness and the
  bake-off tier, whose results are `docs/findings/documents.md`.
- **office**: the adapter over spike-ai-pipeline-office's CLI, and legacy conversion through
  LibreOffice in a container.
- **audio**: windows cut at silences, concurrent transcription, and the seam merge.
- **streams**: live and real-time-paced media, and chat streams, as events.
