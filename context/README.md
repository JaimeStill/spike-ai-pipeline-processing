# spike-ai-pipeline-processing

The processing layer of spike-ai-pipeline, released by tag and built on by the root; it will take
spike-ai-pipeline-client by tag once a stage first needs it. This context records only working
knowledge the code and the README do not express; the spike's question and evidence live in the
root repository's `context/README.md`.

## Capability map

- **tools**: the Tool contract, its exec runner and its locked-down container runner (the docker
  CLI, digest-pinned images), the minimum version check, and cancellation by process group and
  docker stop.
- **documents**: PDF pages to images through the chosen renderer, and image correction.
- **office**: the adapter over spike-ai-pipeline-office's CLI, and legacy conversion through
  LibreOffice in a container.
- **audio**: windows cut at silences, concurrent transcription, and the seam merge.
- **streams**: live and real-time-paced media, and chat streams, as events.
