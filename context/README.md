# spike-ai-pipeline-processing

The processing layer of spike-ai-pipeline, released by tag on spike-ai-pipeline-client and built
on by the root. This context records only working knowledge the code and the README do not
express; the spike's question and evidence live in the root repository's `context/README.md`.

## Capability map

- **tools**: the tool contract, its exec and container runners, and the version checks.
- **documents**: PDF pages to images through the chosen renderer, and image correction.
- **office**: the adapter over spike-ai-pipeline-office's CLI, and legacy conversion through
  LibreOffice in a container.
- **audio**: windows cut at silences, concurrent transcription, and the seam merge.
- **streams**: live and real-time-paced media, and chat streams, as events.
