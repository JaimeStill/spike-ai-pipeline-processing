// Package dispatch walks an input and every part found in it to the stages that prepare them,
// on the standard library and this module's route and inspect packages, and, for the format
// packages' adapters, which build on Linux only, on document, office, picture, email, onenote,
// audio, and tool. It never calls a model: it reports what each part is and what its stage prepared,
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
// many outputs it yielded, in Outputs, and how many parts it found, in Children; never the outputs
// or the parts themselves, whose events the caller has had. Once the body returns for an output
// event, the dispatcher holds nothing of the output but what it appended to the index, and once
// a found part's event is done, nothing of the part but the [Child] reference it appended, so a
// stage that yields millions of outputs and parts, a store's messages and attachments, holds the
// memory of one at a time, the caller's loop owning each as it comes.
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
//     Seen and the Ref the caller set when it was prepared; then its own event carries the entry's
//     outcome, in Recorded, and its counts, and the flags the index holds; Index.Children reads its
//     children back, streamed, as the caller wants them. The part is neither inspected, prepared,
//     nor recursed into again.
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
// Markdown, an image, a record, or audio, held in a file or inline, with the stage's metadata.
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
//     workbook, and a legacy document without LibreOffice have no processor yet; an encrypted
//     OOXML package, which office refuses, ends with [ErrEncrypted].
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
//   - [AudioStage], for route.Audio: a WAV, FLAC, MP3, Ogg, or M4A through audio's Splitter, its
//     plan a record, an [AudioPlan], then each window cut in a silence, a 16 kHz mono WAV, an
//     audio output in plan order, its Meta its index, span, cut, and seams, so a layer above
//     transcribes each and joins their transcripts by audio.Join. It prepares; it transcribes
//     nothing. A source with no audio stream fails, its error wrapping audio.ErrNoAudio, and a
//     failed window fails the part once the other windows are yielded. A caller with no ffmpeg
//     and ffprobe has no Splitter and registers no audio stage, so its audio is no processor yet.
//
// No adapter takes route.Video yet: a video part is no processor yet, as is every part whose
// route has no stage registered.
//
// Where a stage yields a record and its Markdown, per message or page, the record comes first,
// then the Markdown of the same name.
//
// [ZipStage], for route.Archive, is no format package's adapter: it reads a ZIP through
// archive/zip, on any platform, one entry at a time, each file entry written into the part's
// directory under its content's SHA-256 and found as a part, named by its stored name, "zip
// entry <name>"; the stored name is never a path. A directory's entry is no part. An encrypted
// entry is a record, recorded encrypted, and the ZIP, once its other entries are dispatched,
// [Encrypted]. The stage enforces inspect's zip bomb rule again on the bytes it writes, under the
// thresholds of the inspect.Options its [ZipConfig] is given, which should be the dispatcher's:
// an entry past both is stopped, its partial file removed, and recorded quarantined, with its
// evidence, whatever its central directory declared. A tar, gzip, zstd, or 7z archive, or a cabinet, has no
// processor yet. Its identity is "zip", this package's version of it, and a hash of its
// thresholds. A ZIP with a traversing, absolute, or drive letter name, or a symbolic link, is
// quarantined by inspect before the stage runs; under a policy that lets it proceed, the stage
// still writes each entry by its hash, inside the part's directory.
//
// A skipped record, a medium or attachment with no content, a ZIP entry the zip stage can't
// write, and a CLI's warnings are each a record output, never dropped. The email and onenote
// stages place each file they find in a directory of its own, a hard link to the file the CLI
// wrote, since the CLI writes each distinct content once, named by its SHA-256, for every record
// that names it, and the dispatcher removes a found part's file once its event is done. Each such
// part carries that SHA-256 and a release, as [Found] says, which removes the CLI's original once
// the dispatcher has settled its content; a later record naming it is then found with no file,
// and seen from the index.
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
// appended, with the outcome and reason, or releases them by Index.Release when the part fails,
// is limited, or the loop stops. Each part the stage finds is appended as a [Child], its
// reference, by Index.AppendChild, once the found part's own event is done, and committed or
// released with the outputs. Index.Outputs and Index.Children read a recorded entry's outputs and
// children back, streamed, never all at once, and Lookup's Entry counts them.
//
// An entry is recorded only for a part whose stage ran and whose whole subtree came to an outcome
// the content decides: a part with a [Failed] or [Limited] descendant, or that failed itself, is
// not recorded, so a later dispatch prepares it again and retries what failed. A part whose route
// has no stage records only its flags, never an entry under any stage, so a stage registered later
// prepares it in a later dispatch. A [Seen] part's entry references its children, by their SHA-256,
// name, where they were found, size, route, and outcome, as [Child]ren, read by Index.Children,
// rather than yielding their events again: their own entries and flags are in the index, and their
// outputs where the caller put them.
//
// [MemoryIndex] holds the index in memory, every output among it, its waiters woken the moment a
// claim ends. [FileIndex] holds it in a directory the caller names, on the standard library alone:
// a file per record, written by atomic rename, a claim file per claim, created with O_EXCL, which a
// claimant takes over once it is older than [FileOptions.Stale], as a dead claimant would leave it,
// and two pending files per claim, its outputs and its children as JSON lines, appended as they
// come and renamed beside the entry when it is recorded, so its memory holds none of them; its
// waiters poll, and a takeover removes the dead claim's pending files. A claim is never renewed, so
// the stale timeout must outlast the slowest stage. Two dispatches each holding a part the other
// contains wait on each other, as only content that contains itself can arrange; their contexts end
// the wait.
//
// # Outputs and the caller's loop
//
// The caller's loop owns an event until its body returns. The dispatcher appends an output to
// the part's pending entry only then, as the caller left it, so a caller that persists an output
// sets its Ref, as ev.Output.Ref = key, and a later [Seen] part's output events carry the
// caller's Refs; it records the part's entry only once the body returns for the part's own event.
// Then the dispatcher removes the part's file, but for the input's and a quarantined part's, and
// the part's working directory, the outputs' files and anything else its stage wrote. An output's
// file is there until its part's own event is done.
//
// The disk, then, holds the files of the parts in flight, never an input's: each part being
// dispatched, its ancestors' among them, has its file and its working directory, and each found
// part's file goes once its event is done. A store's stage holds more: the content its CLI wrote
// for records still to come. The email and onenote stages release each original as soon as the
// dispatcher settles its content, the found part's event done, its entry in the index, the
// caller keeping nothing of it. Content whose route has no stage is settled too, once the
// caller's loop has moved past its [NoProcessor] event without keeping it: the caller had its
// chance, and a later record naming it in the same dispatch is no processor yet again, with no
// file, its flags those the index holds under its SHA-256 and its route and reason the first's.
// So a store's working directory holds the attachments of the messages in flight, those mail has
// written ahead of the dispatch among them, rather than every attachment until the store's own
// event, whether or not a stage is registered for them. What the dispatcher doesn't settle stays
// until then, so no record loses its content: an original whose part failed, was limited,
// quarantined, or a cycle, or that the caller kept, is there for the next record naming it, which
// is dispatched from it again. [Event.Keep], in an output event, keeps the
// output's file, and the index records its Path, the path otherwise cleared; in a part's own
// event, it keeps the part's file and its working directory, the caller then owning them. A file
// the caller moved away is not missed.
//
// # The pool
//
// [Dispatcher.Pool] dispatches an iterator of inputs over a number of workers, each a Dispatch of
// its own, all sharing the dispatcher's index, so content the inputs share is prepared once and
// seen by the rest, and yields every input's events as one sequence, each a [PoolEvent], its input
// and its event. An input's events keep their order; the inputs' interleave. The caller's loop body
// runs on its own goroutine, and each worker waits for it to return before its dispatch goes on, so
// the caller's loop owns each event as it owns one dispatch's, output events among them: an output
// is appended, the entry recorded, and the files removed once the body returns. A worker holds one
// event at most, so a slow loop holds the workers back, and the disk holds what that many
// dispatches have in flight. An error that stops one input's dispatch is yielded with that input,
// and the others go on; ctx ending stops them all, its error yielded alone, last.
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
// the cursor, the id of the last message whose outputs it has persisted; and an email.Threader,
// fed every message up to the cursor, saved by its MarshalBinary together with it: a checkpoint.
//
// The store's outputs stream as the email stage reads them, so the cursor advances while the
// store is dispatched. For each message, in mail's order, the caller has, as events: each of its
// attachments, a found part's outputs, every part found in it, and its own event, or an inline
// message's own attachments, record, and Markdown, or an attachment's record, in the attachment
// table's order; then the message's record, its Meta "id" the message's store id; then its
// Markdown, when the record's Meta "markdown" says "follows". The message's last output, its
// Markdown or, with none, its record, is the last of everything the message holds, so the caller
// persists the message's outputs as they come and feeds the Threader the message's record,
// decoded into an email.Message; at the message's last output the message is done, and its id is
// the cursor. A message record whose Meta has "embedded_in" is an inline message's, part of its
// parent's, and is fed to no Threader.
//
// The caller saves the cursor and the Threader together every N messages, or on a timer, not at
// every message: each save marshals every message's keys so far, so saving at every message grows
// with the square of the store. Task onenote's finding, docs/findings/onenote.md, measured one
// save at 3.5 ms at 25,000 messages, about 3 minutes of saving over a 50,000-message store whose
// dispatch took 16 s. The cursor rule stays exact: a checkpoint is saved only at a message's last
// output, its cursor that message's id and its Threader fed exactly the messages up to it. Every
// message past the checkpoint's cursor comes again on resuming, those persisted after the
// checkpoint and the one a crash interrupted alike, so the caller discards or overwrites what it
// had of them and feeds the restored Threader each of their records once.
//
// To resume, the caller restores the Threader by UnmarshalBinary and dispatches the store, over
// the same index directory, with After set to the checkpoint's cursor: every part prepared before
// the crash is seen, the parts of the messages past the cursor included, and once the store's
// event is done, the Threader's threads are the whole store's. A process that died leaves its
// claims and pending outputs in the index's directory until they are [FileOptions.Stale], so the
// resumed dispatch waits that out before it claims the store, or a part in flight at the crash,
// and the takeover removes the dead claim's pending outputs; a dispatch whose loop stopped, rather
// than died, released them.
//
// Its exported identifiers:
//
//   - [New] builds a [Dispatcher] over a [Config]: its [Stages], its [Index], its working
//     directory, its [Limits], and its inspect.Options. [Dispatcher.Dispatch] dispatches an
//     [Input], its After resuming a store, yielding [Event]s, each a [Part] and an [Output] or the
//     part's [Outcome]; [Event.Keep] keeps its files. [Dispatcher.Pool] dispatches many over
//     workers, yielding [PoolEvent]s.
//   - [Stage], its [Identity], and [HashConfig] for an identity's configuration hash; a stage
//     yields [Item]s, made by [OutputItem] and [PartItem], each an [Output] of an [OutputKind],
//     [TextOutput], [MarkdownOutput], [ImageOutput], [RecordOutput], or [AudioOutput], or a
//     [Found] part, and
//     ends with [ErrNoProcessor] or [ErrEncrypted] to name its outcome.
//   - The adapters, on Linux: [DocumentStage], [OfficeStage], [PictureStage], [EmailStage],
//     [OneNoteStage], and [AudioStage], made by [NewDocumentStage], [NewOfficeStage],
//     [NewPictureStage], [NewEmailStage], [NewOneNoteStage], and [NewAudioStage] over a
//     [DocumentConfig], [OfficeConfig], [PictureConfig], [EmailConfig], [OneNoteConfig], and
//     [AudioConfig]; the audio stage's plan record an [AudioPlan] of [AudioWindow]s and
//     [AudioSpan]s; [VisionStage], made by [NewVisionStage]; and [Versions], a stage's tools'
//     version.
//   - [ZipStage], made by [NewZipStage] over a [ZipConfig], on any platform.
//   - [Index], keyed by a [Key], holding [Entry]s, each counting its outputs and its [Child]ren,
//     both appended under a claim and read back streamed; the shipped
//     ones are [MemoryIndex], made by [NewMemoryIndex], and [FileIndex], made by
//     [NewFileIndex] under [FileOptions], [DefaultStale] and [DefaultPoll] by default. A
//     [Found] part may carry its SHA-256 and a release, for content a stage holds for later
//     records.
package dispatch
