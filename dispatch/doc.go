// Package dispatch walks an input and every part found in it to the stages that prepare them,
// on the standard library and this module's route and inspect packages, and, for the format
// packages' adapters, which build on Linux only, on document, office, picture, email, onenote,
// and tool. It never calls a model: it reports what each part is and what its stage prepared,
// for a layer above to send on. The stages are the caller's, registered by route; the format
// packages' adapters are stages.
//
// [Dispatcher.Dispatch] takes an [Input], a file the caller owns and the dispatcher never
// removes, and yields [Event]s, depth-first, in each stage's own order: each output a stage
// prepares is an output event, its Output set, yielded as the stage yields it, and each part a
// stage finds is dispatched as it finds it, its outputs' events and every part found in it, then
// its own event, before the stage's next item. A part's own event, its Output nil, follows all of
// that, once its outcome is known, so the input's event is last. A part is a file: its SHA-256,
// streamed, and size are taken before any stage runs, its route by route.File, its flags by
// inspect.Inspect, and it names its parent's SHA-256, its depth, and where its parent's stage
// found it.
//
// # Events
//
// An output event carries the part it belongs to, the identity of its stage, and the [Output],
// which the caller's loop body may persist, setting its Ref, as ev.Output.Ref = key. A part's own
// event carries its outcome and why, the error a failed part came to, its stage's identity, how
// many outputs it yielded, in Outputs, and the parts it found, as [Child]ren; never the outputs
// themselves, which the caller has had. Once the body returns for an output event, the dispatcher
// holds nothing of the output but what it appended to the index, so a stage that yields millions
// of outputs, a store's messages, holds the memory of one at a time, the caller's loop owning
// each as it comes.
//
// # Outcomes
//
// A part comes to one [Outcome]:
//
//   - [Prepared]: its stage prepared it; its outputs' events came before its own, which counts
//     them and references the parts it found.
//   - [NoProcessor]: its route has no stage registered, or its stage ended with
//     [ErrNoProcessor], as office's does for a kind it has no processor for yet.
//   - [Seen]: the index already holds its content's entry under its stage's identity, recorded
//     by this dispatch, an earlier one, or a concurrent one this dispatch waited on. Its recorded
//     outputs are output events, streamed from the index one at a time, each with the Outcome
//     Seen and the Ref the caller set when it was prepared; then its own event carries the entry,
//     its children, and its outcome in Recorded, and the flags the index holds. The part is
//     neither inspected, prepared, nor recursed into again.
//   - [Cycle]: its SHA-256 is an ancestor's; it is neither inspected, prepared, nor recursed
//     into.
//   - [Quarantined]: its inspection's verdict quarantines it, its flags holding the evidence: no
//     stage runs, and its file is kept.
//   - [Encrypted]: its stage ended with [ErrEncrypted], or failed on content whose flags mark it
//     encrypted, a /Encrypt PDF, an encrypted ZIP entry, or an encrypted OOXML package; Err holds
//     the stage's error.
//   - [Failed]: the part couldn't be read, routed, inspected, or prepared; Err says why. A stage
//     that fails after yielding outputs and finding parts has had those outputs yielded and those
//     parts dispatched.
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
//   - [EmailStage], for route.Nested: mail's records, each a record output, and each
//     attachment's file found as a part, where its message and place say. A message's
//     attachments come first, each found part dispatched whole, then its record, then its
//     Markdown body, which the record's Meta "markdown" says follows; an embedded message mail
//     gives no file for is prepared inline, in its place among the attachments, its own
//     attachments found below it, at any depth.
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
// The claimant appends each output to the claim's pending entry by Index.Append, once the
// caller's loop body returns for its event, as the body left it, its Ref set, its Path cleared
// unless the body kept it; the part's own event commits them by Index.Record, in the order
// appended, with the outcome, reason, and children, or releases them by Index.Release when the
// part fails, is limited, or the loop stops. Index.Outputs reads a recorded entry's outputs
// back, streamed, never all at once, and Lookup's Entry counts them.
//
// An entry is recorded only for a part whose stage ran and whose whole subtree came to an
// outcome the content decides: a part with a [Failed] or [Limited] descendant, or that failed
// itself, is not recorded, so a later dispatch prepares it again and retries what failed. A
// part whose route has no stage records only its flags, so a stage registered later prepares
// it. A [Seen] part's entry references its children, by their SHA-256, name, where they were
// found, size, route, and outcome, as [Child]ren, rather than yielding their events again: their
// own entries and flags are in the index, and their outputs where the caller put them.
//
// [MemoryIndex] holds the index in memory, every output among it, its waiters woken the moment a
// claim ends. [FileIndex] holds it in a directory the caller names, on the standard library
// alone: a file per record, written by atomic rename, a claim file per claim, created with
// O_EXCL, which a claimant takes over once it is older than [FileOptions.Stale], as a dead
// claimant would leave it, and a pending file per claim, its outputs as JSON lines, appended as
// they come and renamed beside the entry when it is recorded, so its memory holds none of them;
// its waiters poll, and a takeover removes the dead claim's pending file. A claim is never renewed, so the stale timeout must outlast the
// slowest stage. Two dispatches each holding a part the other contains wait on each other, as
// only content that contains itself can arrange; their contexts end the wait.
//
// # Outputs and the caller's loop
//
// The caller's loop owns an event until its body returns. The dispatcher appends an output to
// the part's pending entry only then, as the caller left it, so a caller that persists an output
// sets its Ref, as ev.Output.Ref = key, and a later [Seen] part's output events carry the
// caller's Refs; it records the part's entry only once the body returns for the part's own event.
// Then the dispatcher removes the part's file, but for the input's and a quarantined part's, and
// the part's working directory, the outputs' files and anything else its stage wrote, so the disk
// holds the files of the parts in flight, a record's media, never an input's. An output's file
// is there until its part's own event is done. [Event.Keep], in an output event, keeps the
// output's file, and the index records its Path, the path otherwise cleared; in a part's own
// event, it keeps the part's file and its working directory, the caller then owning them. A file
// the caller moved away is not missed.
//
// # The pool
//
// [Dispatcher.Pool] dispatches an iterator of inputs over a number of workers, each a Dispatch of
// its own, all sharing the dispatcher's index, so content the inputs share is prepared once and
// seen by the rest, and yields every input's events as one sequence, each a [PoolEvent], its
// input and its event. An input's events keep their order; the inputs' interleave. The caller's
// loop body runs on its own goroutine, and each worker waits for it to return before its dispatch
// goes on, so the caller's loop owns each event as it owns one dispatch's, output events among
// them: an output is appended, the entry recorded, and the files removed once the body returns. A worker holds one event at most, so a slow loop
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
// A caller resuming a large store after a crash combines three things it keeps as it goes: a
// [FileIndex] in a directory that outlives the process, which holds the entries and flags of the
// parts recorded before the crash, so the resumed dispatch sees each attachment already prepared;
// the cursor, the id of the last message whose outputs it has persisted; and an email.Threader
// saved by its MarshalBinary with that cursor, fed every message up to it.
//
// The store's outputs stream as the email stage reads them, so the cursor advances while the
// store is dispatched. For each message, in mail's order, the caller has, as events: each of its
// attachments, a found part's outputs, every part found in it, and its own event, or an inline
// message's own attachments, record, and Markdown, or an attachment's record, in the attachment
// table's order; then the message's record, its Meta "id" the message's store id; then its
// Markdown, when the record's Meta "markdown" says "follows". The message's last output, its
// Markdown or, with none, its record, is the last of everything the message holds, so the caller
// persists the message's outputs as they come and, at its last, sets the cursor to its id and
// saves the Threader, fed the message's record, decoded into an email.Message, with it. A
// message record whose Meta has "embedded_in" is an inline message's, part of its parent's, and
// is fed to no Threader. Outputs of the message past the cursor that a crash interrupts come
// again on resuming, so the caller discards or overwrites what it had of that message.
//
// To resume, the caller restores the Threader by UnmarshalBinary and dispatches the store, over
// the same index directory, with After set to the cursor: every part prepared before the crash
// is seen, the message past the cursor's included, and once the store's event is done, the
// Threader's threads are the whole store's. A process that died leaves its claims and pending
// outputs in the index's directory until they are [FileOptions.Stale], so the resumed dispatch
// waits that out before it claims the store, or a part in flight at the crash, and the takeover
// removes the dead claim's pending outputs; a dispatch whose loop stopped, rather than died,
// released them.
//
// Its exported identifiers:
//
//   - [New] builds a [Dispatcher] over a [Config]: its [Stages], its [Index], its working
//     directory, its [Limits], and its inspect.Options. [Dispatcher.Dispatch] dispatches an
//     [Input], its After resuming a store, yielding [Event]s, each a [Part] and an [Output] or the
//     part's [Outcome]; [Event.Keep] keeps its files. [Dispatcher.Pool] dispatches many over workers, yielding
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
//   - [Index], keyed by a [Key], holding [Entry]s, each counting its outputs, appended under a
//     claim and read back streamed, and referencing its [Child]ren; the shipped
//     ones are [MemoryIndex], made by [NewMemoryIndex], and [FileIndex], made by
//     [NewFileIndex] under [FileOptions], [DefaultStale] and [DefaultPoll] by default.
package dispatch
