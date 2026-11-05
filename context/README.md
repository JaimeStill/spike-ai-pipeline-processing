# spike-ai-pipeline-processing

The processing layer of spike-ai-pipeline, released by tag and built on by the root. It requires
spike-ai-pipeline-client at v0.4.0 for its tests alone, the vision harness the integration and
bake-off tiers run on. This context records only working knowledge the code and the README do not
express; the spike's question and evidence live in the root repository's `context/README.md`.

## Capability map

- **tools**: the Tool contract, its exec runner and its locked-down container runner (the docker
  CLI, digest-pinned images), the minimum version check, and cancellation by process group and
  docker stop, and a run's CPU usage (no peak memory: the Go parent's shows through).
- **routes**: package route, content identified by its signature and the stage it goes to next
  named, pure functions on the standard library: a .pst and an mbox nested, an .ost unrouted,
  WordPerfect and .xlsb office's though office has no processor for them yet, for a dispatcher to
  report.
- **inspection**: package inspect, every part inspected before its stage on the standard library,
  pure functions of its bytes and name: active content flagged and let proceed, content unsafe to
  read (ZIP traversal, absolute paths, symlinks, bombs by declared sizes) quarantined, a caller's
  policy over either; no stage calls it yet, which the dispatcher will.
- **pictures**: package picture, TIFF, EMF, WMF, and SVG converted to PNG, one picture or a batch,
  named by SHA-256, EMF and WMF through soffice in the default gotenberg image and trimmed by
  magick; and the corrections stage, ImageMagick corrections in a fixed order, their catalog for a
  prompt, and Assess's measures.
- **cli contract**: package internal/cli, the exit codes 1 to 4 as one set of errors that office's,
  email's, and onenote's exit errors are bound to, the stderr warning reader, and the bounded
  buffer and spool the adapters share; the README's adapter convention describes the shape every
  format package follows.
- **documents**: pre-inference preparation of a PDF's pages: Ghostscript, the renderer bake-off's
  winner, for page counts, pages and regions as PNG, and text layers; picture's corrections; and
  Preparer.Images, the lazy, ordered, concurrent page stream over them. Inference is test code: the
  vision harness and the bake-off tier, whose results `docs/findings/documents.md` records.
- **office**: the adapter over spike-ai-pipeline-office's CLI, its JSON model decoded into typed
  schema "1" values or its Markdown, with the media list and office's exit codes as errors; legacy
  doc, xls, and ppt converted to OOXML through soffice in the default gotenberg image, locked down;
  and the picture standard, a document's pictures converted in one batch by picture, PNG named by
  the original's SHA-256, the Markdown's links rewritten. Vision over the pictures is test code in
  the integration tier, whose results `docs/findings/office.md` records with the legacy round trip's
  loss and the container's footprint. One picture outside a document, for route.VisionConverted, is
  picture's Converter.Convert.
- **email**: the adapter over spike-ai-pipeline-mail's CLI, its JSON Lines streamed from a pipe into
  typed schema "1" records, a .pst's or a .msg's, with mail's warnings and exit codes as errors;
  attachment routes by route.File, by content signature, never the stored type, from the attachments
  directory mail writes; and threads by RFC 5256's REFERENCES algorithm plus conversation-index
  parents, from keys a Threader keeps while the stream is read. Its fixtures are mail's, copied at
  its tag under a SHA-256 manifest, and a built .msg of this repository's office fixtures and, by
  value, one of mail's .msg fixtures. The integration tier carries each route to its real stage; the
  findings tier, whose results `docs/findings/email.md` records, scores threads and routes against
  hand truth and times the adapter on plaso's store and mail's generated scale store, neither
  committed.
- **onenote**: the adapter over spike-ai-pipeline-onenote's CLI, its JSON Lines streamed from a
  pipe into typed schema "1" records, a section's, a notebook's, or a package's: notebook, section
  groups, sections, pages with their content kept as onenote's JSON, and skipped sections, with
  onenote's warnings and exit codes as errors bound to internal/cli's shared set. Its fixtures are
  onenote's public Interop-TestSuites and onenote.rs sets and expectations, copied at its tag under
  a SHA-256 manifest; the real-binary tests hold every read to them. Its media are named, not yet
  routed.
- **audio**: windows cut at silences, concurrent transcription, and the seam merge.
- **streams**: live and real-time-paced media, and chat streams, as events.
