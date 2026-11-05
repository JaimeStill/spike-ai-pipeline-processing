// Package dispatch walks an input and every part found in it to the stages that prepare them,
// on the standard library and this module's route and inspect packages, and, for the format
// packages' adapters, which build on Linux only, on document, office, picture, email, onenote,
// and tool. It never calls a model: it reports what each part is and what its stage prepared,
// for a layer above to send on. The stages are the caller's, registered by route; the format
// packages' adapters are stages.
//
// [Dispatcher.Dispatch] takes an [Input], a file the caller owns and the dispatcher never
// removes, and yields an [Event] for each part, depth-first: a stage's parts are dispatched as
// it finds them, each with every part found in it, before the stage's next item, and a part's
// event follows its descendants', once its outcome is known, so the input's event is last. A
// part is a file: its SHA-256, streamed, and size are taken before any stage runs, its route by
// route.File, its flags by inspect.Inspect, and it names its parent's SHA-256, its depth, and
// where its parent's stage found it.
//
// # Outcomes
//
// A part comes to one [Outcome]:
//
//   - [Prepared]: its stage prepared it; the event carries the stage's outputs and references to
//     the parts it found.
//   - [NoProcessor]: its route has no stage registered, or its stage ended with
//     [ErrNoProcessor], as office's does for a kind it has no processor for yet.
//   - [Seen]: the index already holds its content's entry under its stage's identity, recorded
//     by this dispatch, an earlier one, or a concurrent one this dispatch waited on. The event
//     carries the entry, its outputs, its children, and its outcome in Recorded, and the flags
//     the index holds; the part is neither inspected, prepared, nor recursed into again.
//   - [Cycle]: its SHA-256 is an ancestor's; it is neither inspected, prepared, nor recursed
//     into.
//   - [Quarantined]: its inspection's verdict quarantines it, its flags holding the evidence: no
//     stage runs, and its file is kept.
//   - [Encrypted]: its stage ended with [ErrEncrypted], or failed on content whose flags mark it
//     encrypted, a /Encrypt PDF, an encrypted ZIP entry, or an encrypted OOXML package; Err holds
//     the stage's error.
//   - [Failed]: the part couldn't be read, routed, inspected, or prepared; Err says why. A stage
//     that fails after finding parts has had those parts dispatched.
//   - [Limited]: the part is past a cap the caller set in [Limits], on the parts, the bytes, or
//     the depth one dispatch admits; it is neither inspected nor prepared, and its siblings go
//     on. No cap applies by default.
//
// An error that stops the whole dispatch, the input unreadable, the index failing, a file the
// dispatcher can't remove, or the context ending, is yielded alone, last.
//
// # Stages
//
// A [Stage] names its [Identity], its name, its tool's version, and its configuration's hash,
// and prepares one part: Prepare yields [Item]s, each an [Output] it prepared or a [Found] part,
// a file it wrote into the directory the dispatcher made for the part. An Output is text,
// Markdown, an image, or a record, held in a file or inline, with the stage's metadata.
//
// # Adapters
//
// Each format package has an adapter here, a Stage built over the reader or converter the
// caller already built, its Tools the caller's, and the version of those Tools, which [Versions]
// names: no adapter constructs a tool. An adapter's identity is its name, that version, and a
// hash of the options that change what it prepares, with the adapters' own version, so a change
// to an adapter prepares content again.
//
//   - [DocumentStage], for route.Document: a PDF's pages through document's Preparer, each
//     page's text layer a text output and its image a PNG, under the request the caller gives.
//   - [OfficeStage], for route.Office: office's Markdown, and each medium office wrote, a
//     converted picture as its PNG, found as a part. ODF, RTF, WordPerfect, an Excel binary
//     workbook, and a legacy document without LibreOffice have no processor yet.
//   - [PictureStage], for route.VisionConverted: a TIFF, EMF, WMF, or SVG to a PNG through
//     picture's Converter. HEIC, WebP, and AVIF, and a picture whose tool the Converter lacks,
//     have no processor yet.
//   - [VisionStage], for route.Vision: a PNG, JPEG, GIF, or BMP passed through as an image
//     output, so every picture reaches a layer above as one.
//   - [EmailStage], for route.Nested: mail's records, each a record output, a message's record
//     followed by its Markdown body, and each attachment's file found as a part, where its
//     message and place say; an embedded message mail gives no file for is prepared inline,
//     its own attachments found below it, at any depth.
//   - [OneNoteStage], for route.OneNote: onenote's records, a page's record followed by its
//     Markdown, and each of its media found as a part, "page <id>, <where>"; a medium onenote
//     wrote no content for is a record.
//
// Where a stage yields a record and its Markdown, per message or page, the record comes first,
// then the Markdown of the same name.
//
// [ZipStage], for route.Archive, is no format package's adapter: it reads a ZIP through
// archive/zip, on any platform, one entry at a time, each file entry written into the part's
// directory under its content's SHA-256 and found as a part, named by its stored name, "zip
// entry <name>"; the stored name is never a path. A directory's entry is no part. An encrypted
// entry is a record, and the ZIP, once its other entries are dispatched, [Encrypted]. The stage
// enforces inspect's zip bomb rule again on the bytes it writes, under the thresholds of the
// inspect.Options its [ZipConfig] is given, which should be the dispatcher's: an entry past both
// is stopped, its partial file removed, and recorded quarantined, with its evidence, whatever
// its central directory declared. A tar, gzip, zstd, or 7z archive, or a cabinet, has no
// processor yet. Its identity is "zip", this package's version of it, and a hash of its
// thresholds. A ZIP with a traversing, absolute, or drive letter name, or a symbolic link, is
// quarantined by inspect before the stage runs; under a policy that lets it proceed, the stage
// still writes each entry by its hash, inside the part's directory.
//
// A skipped record, a medium or attachment with no content, a ZIP entry the zip stage can't
// write, and a CLI's warnings are each a record output, never dropped. The email and onenote
// stages place each file they find in a directory of its own, a hard link to the file the CLI
// wrote, since the CLI writes each distinct content once and the dispatcher removes a found
// part's file once its event is done.
//
// # The index
//
// An [Index] is content-addressed. It records a stage's [Entry] for content by a [Key], the
// content's SHA-256 and the stage's identity, so a new tool version or configuration prepares
// it again; and the content's inspection flags by its SHA-256 alone. A dispatch looks the key
// up, and on a miss claims it: one claimant prepares the content, and a concurrent dispatch that
// meets the claim waits until the claimant records the entry, then reports the part [Seen], or
// releases it, then claims it in turn. A part that fails releases its claim, so another dispatch
// may try again.
//
// An entry is recorded only for a part whose stage ran and whose whole subtree came to an
// outcome the content decides: a part with a [Failed] or [Limited] descendant, or that failed
// itself, is not recorded, so a later dispatch prepares it again and retries what failed. A
// part whose route has no stage records only its flags, so a stage registered later prepares
// it. A [Seen] part's entry references its children, by their SHA-256, name, where they were
// found, size, route, and outcome, as [Child]ren, rather than yielding their events again: their
// own entries and flags are in the index, and their outputs where the caller put them.
//
// [MemoryIndex] holds the index in memory, its waiters woken the moment a claim ends.
// [FileIndex] holds it in a directory the caller names, on the standard library alone: a file
// per record, written by atomic rename, and a claim file per claim, created with O_EXCL, which
// a claimant takes over once it is older than [FileOptions.Stale], as a dead claimant would
// leave it; its waiters poll. A claim is never renewed, so the stale timeout must outlast the
// slowest stage. Two dispatches each holding a part the other contains wait on each other, as
// only content that contains itself can arrange; their contexts end the wait.
//
// # Outputs and the caller's loop
//
// The caller's loop owns an event until its body returns. The dispatcher records the part's
// entry in the index only then, from the event's Outputs as the caller left them, so a caller
// that persists an output sets its Ref, through the slice, as ev.Outputs[i].Ref = key, and a
// later [Seen] event carries the caller's Refs. Then the dispatcher removes the part's file,
// but for the input's and a quarantined part's, and the part's working directory, the outputs'
// files and anything else its stage wrote, so the disk holds the files of the parts in flight,
// a record's media, never an input's. [Event.Keep] keeps the part's file and its working
// directory, the caller then owning them; a recorded output keeps its Path only for a kept
// event, the path otherwise removed. A file the caller moved away is not missed.
//
// # The pool
//
// [Dispatcher.Pool] dispatches an iterator of inputs over a number of workers, each a Dispatch of
// its own, all sharing the dispatcher's index, so content the inputs share is prepared once and
// seen by the rest, and yields every input's events as one sequence, each a [PoolEvent], its
// input and its event. An input's events keep their order; the inputs' interleave. The caller's
// loop body runs on its own goroutine, and each worker waits for it to return before its dispatch
// goes on, so the caller's loop owns each event as it owns one dispatch's: the entry is recorded
// and the files removed once the body returns. A worker holds one event at most, so a slow loop
// holds the workers back, and the disk holds what that many dispatches have in flight. An error
// that stops one input's dispatch is yielded with that input, and the others go on; ctx ending
// stops them all, its error yielded alone, last.
//
// # Resuming a store
//
// [Input.After] resumes the input's read after a message, the id email.Request's After takes, a
// .pst's node id or an mbox's ordinal: [EmailStage] passes it to mail as --after, mail 0.3.0 or
// later, which writes the whole read's store and folders, then only the messages after it. Only
// the input's own part carries it. A resumed input's entry is never recorded, its outputs being
// the read's tail, so a later whole dispatch prepares it whole; content the index already holds
// is seen, resumed or not, and the parts found below are recorded as ever.
//
// A caller resuming a large store after a crash combines three things it kept as it went: a
// [FileIndex] in a directory that outlives the process, which holds the entries and flags of the
// parts recorded before the crash, so the resumed dispatch sees each attachment already prepared;
// the cursor, the id of the last message whose outputs it persisted; and an email.Threader saved
// by its MarshalBinary with that cursor, fed every message up to it. It restores the Threader by
// UnmarshalBinary, dispatches the store with After set to the cursor, persists each message's
// outputs and feeds the Threader each message record, advancing the cursor and saving the
// Threader together, and once the store's event is done, the Threader's threads are the whole
// store's. The crashed dispatch's claim on the store stays in the index's directory until it is
// [FileOptions.Stale], so the resumed dispatch waits that out before it claims the store.
//
// A store's records reach the caller in the store's own event, its last, after every part found
// in it, so the cursor, a message record's Meta "id", advances as the caller persists that
// event's outputs: a crash while it does resumes after the last message persisted, every
// attachment then seen in the index. A crash before the store's event, while the parts found in
// it are dispatched, leaves no cursor, and the store is dispatched whole again, the parts
// recorded before the crash seen. A caller that needs a cursor while the parts are dispatched
// reads the store's records through email.Reader, which streams them.
//
// Its exported identifiers:
//
//   - [New] builds a [Dispatcher] over a [Config]: its [Stages], its [Index], its working
//     directory, its [Limits], and its inspect.Options. [Dispatcher.Dispatch] dispatches an
//     [Input], its After resuming a store, yielding [Event]s, each a [Part] and its [Outcome];
//     [Event.Keep] keeps its files. [Dispatcher.Pool] dispatches many over workers, yielding
//     [PoolEvent]s.
//   - [Stage], its [Identity], and [HashConfig] for an identity's configuration hash; a stage
//     yields [Item]s, made by [OutputItem] and [PartItem], each an [Output] of an [OutputKind],
//     [TextOutput], [MarkdownOutput], [ImageOutput], or [RecordOutput], or a [Found] part, and
//     ends with [ErrNoProcessor] or [ErrEncrypted] to name its outcome.
//   - The adapters, on Linux: [DocumentStage], [OfficeStage], [PictureStage], [EmailStage], and
//     [OneNoteStage], made by [NewDocumentStage], [NewOfficeStage], [NewPictureStage],
//     [NewEmailStage], and [NewOneNoteStage] over a [DocumentConfig], [OfficeConfig],
//     [PictureConfig], [EmailConfig], and [OneNoteConfig]; [VisionStage], made by
//     [NewVisionStage]; and [Versions], a stage's tools' version.
//   - [ZipStage], made by [NewZipStage] over a [ZipConfig], on any platform.
//   - [Index], keyed by a [Key], holding [Entry]s, each referencing its [Child]ren; the shipped
//     ones are [MemoryIndex], made by [NewMemoryIndex], and [FileIndex], made by
//     [NewFileIndex] under [FileOptions], [DefaultStale] and [DefaultPoll] by default.
package dispatch
