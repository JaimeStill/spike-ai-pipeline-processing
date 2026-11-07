# spike-ai-pipeline-processing

The processing layer of spike-ai-pipeline, released by tag and built on by the root. It requires
spike-ai-pipeline-client at v0.4.0 for its tests alone, the vision and speech harnesses the
integration, bake-off, and findings tiers run on. This context records only working knowledge the
code and the README do not express; the spike's question and evidence live in the root
repository's `context/README.md`.

## Capability map

- **tools**: the Tool contract, its exec runner and its locked-down container runner (the docker
  CLI, digest-pinned images), the minimum version check, and cancellation by process group and
  docker stop, and a run's CPU usage (no peak memory: the Go parent's shows through).
- **routes**: package route, content identified by its signature and the stage it goes to next
  named, pure functions on the standard library: document, office, vision, vision-converted, nested,
  onenote, archive, audio, video, text, or unrouted, each with a reason. A .pst and an mbox are
  nested, an .ost unrouted; WordPerfect and .xlsb are office's though office has no processor for
  them yet, for a dispatcher to report; an encrypted OOXML package, an OLE2 file holding an
  EncryptedPackage stream named as OOXML, is office's, which refuses it encrypted; HTML is unrouted,
  not text.
- **inspection**: package inspect, every part inspected before its stage on the standard library,
  pure functions of its bytes and name: active content flagged and let proceed, content unsafe to
  read (ZIP and tar traversal, absolute paths, links, a tar's devices, bombs by declared sizes)
  quarantined, a caller's policy over either; the dispatcher inspects every part before its stage.
  CheckListing holds a 7z's, a cabinet's, or a zstd stream's 7-Zip listing to the tar's rules, for
  the archive stage to call before it extracts anything, since inspect runs no program.
- **pictures**: package picture, TIFF, EMF, WMF, and SVG converted to PNG, one picture or a batch,
  named by SHA-256, EMF and WMF through soffice in the default gotenberg image and trimmed by
  magick; and the corrections stage, ImageMagick corrections in a fixed order, their catalog for a
  prompt, and Assess's measures.
- **cli contract**: package internal/cli, the exit codes 1 to 4 as one set of errors that office's,
  email's, and onenote's exit errors are bound to, the stderr warning reader, and the bounded buffer
  and spool the adapters share; the README's adapter convention describes the shape every format
  package follows.
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
  typed schema "1" records, a .pst's, a .msg's, an .eml's, or an mbox's, with mail's warnings and
  exit codes as errors; attachment routes by route.File, by content signature, never the stored
  type, from the attachments directory mail writes; and threads by RFC 5256's REFERENCES algorithm
  plus conversation-index parents, from keys a Threader keeps while the stream is read and saves and
  restores, so a read resumed through mail 0.3.0's --after, Request.After, threads whole. Its
  fixtures are mail's, copied at its tag under a SHA-256 manifest, and a built .msg of this
  repository's office fixtures and, by value, one of mail's .msg fixtures. The integration tier
  carries each route to its real stage; the findings tier, whose results `docs/findings/email.md`
  records, scores threads and routes against hand truth and times the adapter on plaso's store and
  mail's generated scale store, neither committed.
- **onenote**: the adapter over spike-ai-pipeline-onenote's CLI, its JSON Lines streamed from a pipe
  into typed schema "1" records, a section's, a notebook's, or a package's: notebook, section
  groups, sections, and pages, every element of their content typed, each record marshalling back to
  onenote's bytes, and skipped sections, with onenote's warnings and exit codes as errors bound to
  internal/cli's shared set; and a page's media, images, embedded files, and ink, wherever they sit,
  each placed and routed by route.File. Its fixtures are onenote's public Interop-TestSuites and
  onenote.rs sets and expectations, copied at its tag under a SHA-256 manifest; the real-binary
  tests hold every read to them.
- **dispatch**: an input and every part found in it walked to the caller's stages by route, hashed,
  routed, and inspected first, the format packages' adapters and the archive stage, which reads
  ZIP, tar, and gzip on the standard library and, through sevenzip, zstd, 7z, cabinets, and ZIP's
  other methods, quarantining an archive whose 7-Zip listing is unsafe, among them, under a content-addressed index, in memory or a directory, whose claims
  make one of concurrent dispatches prepare shared content; a pool over N workers sharing the index,
  its events merged onto the caller's goroutine, each worker held until the loop takes its event;
  and a store's read resumed after a message, Input.After, its entry not recorded, from a
  checkpoint, the cursor and the Threader saved together every N messages, since a save at every
  message is quadratic. The findings tier, whose results `docs/findings/onenote.md` records, holds
  the tier's routes to hand truth, records inspection over ~/docs and vision's reading of OneNote
  ink, scored by internal/textscore as document's bake-offs are, and times the onenote adapter and
  the dispatcher, resumed and pooled, over mail's scale store.
- **archives**: package sevenzip, the adapter over 7-Zip's 7zz, pinned by mise from its GitHub
  release: an archive's technical listing parsed into typed entries, and one entry extracted to the
  caller's writer through 7zz's standard output, never to a path; its fixtures are commons-compress's
  and libarchive's, under their licenses, and generated 7z, zstd, and cabinet hazards.
- **audio**: pre-inference preparation of speech. Package audio runs ffmpeg and ffprobe through
  either runner: it probes a source, plans windows of at most 30 s ending at the middle of
  silences, with 2 s seams, cuts them concurrently as 16 kHz mono WAV named by SHA-256, and joins
  the windows' transcripts at the seams by transformers' best-offset merge. Dispatch's audio stage
  yields a part's plan as a record and its windows as audio outputs. Transcription is test code:
  internal/audiotest's speech harness and the findings tier, whose results
  `docs/findings/audio.md` records, scored by internal/textscore's normalized word error rate and
  seam edits.
- **streams**: live and real-time-paced media, and chat streams, as events.
