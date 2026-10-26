# spike-ai-pipeline-processing

The processing layer of [spike-ai-pipeline](https://github.com/JaimeStill/spike-ai-pipeline): it
prepares messages, documents, and streams for native model capabilities, such as a PDF's pages
through vision, whether the payload is a whole blob or a stream.

Go builds only what the layer must own. Mature system tools do the specialized work behind a tool
contract this layer defines, as herald's processing layer did: an exec runner for binaries and
scripts, and a container runner for pinned images. The tools in play are a PDF renderer (chosen
by bake-off), ImageMagick for images and image correction, ffmpeg and ffprobe for audio and video,
spike-ai-pipeline-office's Open XML CLI for office documents, and LibreOffice in a container for
legacy office formats only.

It builds on spike-ai-pipeline-client by tag, and is released by tag, `vX.Y.Z`, with a CHANGELOG.
The spike's question, evidence, and answer are in the root repository's README.

Apache-2.0.
