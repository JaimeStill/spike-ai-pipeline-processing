//go:build linux

// Package onenote reads OneNote sections, .one files from desktop OneNote 2016 and later or
// downloaded from OneDrive, .onetoc2 notebooks and notebook directories, and .onepkg packages,
// for a model through onenote, spike-ai-pipeline-onenote: an input in, its records streamed out
// as typed Go values while onenote runs. It provides pre-inference preparation only; a layer
// above sends what it returns to a model. The package builds on Linux only, on the standard
// library and this module's tool package; the program it runs is a tool.Tool the caller built.
//
// A Reader is built at the composition root over a onenote Tool, OneNoteSpec declaring it.
// NewReader refuses one older than 0.1.0, and the Tool's own construction a missing one, so a
// missing or outdated onenote fails at startup. onenote's contract is its README: its JSON
// Lines, schema "1", its media naming, its warnings, and its exit codes.
//
// Reader.Read returns a Stream for one input, a path, refusing an invalid Request at once with
// an error matching ErrRequest, before anything runs; ranging over the Stream's Records runs
// onenote once on it and yields each line onenote writes as it writes it: the notebook first,
// then the notebook's entries depth-first in its table of contents' order, each section group
// before the entries it holds and each section before its pages, then a skipped record for each
// section onenote couldn't read. A section input is a notebook, its one section, and its pages.
// onenote's stdout reaches the package through a pipe and is decoded one line at a time, so
// memory follows the record being read, never the notebook. Each line is decoded through
// encoding/json/v2 into the typed value of its kind, Notebook, SectionGroup, Section, Page, or
// Skipped, which mirror schema "1". A Page is typed through and through: its ids, level,
// title, times, author, ink, recognised ink text, Markdown, and Content, its containers, each
// by its kind (title, outline, image, embedded file, ink), holding items (elements and groups)
// that hold blocks (paragraphs with their runs, equations among them, note tags, list markers,
// and handwriting; tables of cells; images; embedded files; ink). Each field is left out, or
// nil, where onenote leaves it out, and kept where onenote writes a zero, so a decoded line
// marshals back through encoding/json/v2 to onenote's own bytes once its "line" is put back in
// front; the numbers onenote writes as Rust's f32 are a Float, which marshals as onenote writes
// them. A Page's Markdown is onenote's, its links as onenote wrote them. A line of a kind the
// package doesn't know is kept as an UnknownRecord, and a container, item, or block of a kind it
// doesn't know as an Unknown holding its JSON, never an error, and either marshals back as it
// came. A notebook line of any other schema is an error matching ErrSchema that names the
// schema. A loop that stops early stops onenote.
//
// Each line onenote writes to stderr as a warning is one of the Stream's Warnings, its prefix
// taken off, and a read with skipped sections and warnings succeeds. onenote's exit codes 1 to
// 4 are an *ExitError carrying onenote's message, which matches its code's error through
// errors.Is: ErrNotWritten, ErrUsage (a fault in this package), ErrRefused (with onenote's
// reason, a password-protected section's among them), or ErrFailed (a fault of onenote's). Each
// is the exit error every CLI this module runs shares, so email.ErrRefused matches onenote's
// refusal, and ErrRefused mail's and office's. A read whose context ends returns an error
// matching the context's, as every Tool run does.
//
// Given a media directory in its Request, onenote writes each distinct image, embedded file, and
// ink drawing there once, named by its SHA-256, before the line that names it; without one it
// writes nothing to disk, and the records name the files all the same.
//
// Known limits: they are onenote's, as its README states. A password-protected section is
// refused as an input, and skipped inside a notebook; a page is read at its latest revision
// only; merged table cells are read as their own; a section that fails inside a section group is
// lost without a skipped record or a warning; and skipped records come after the notebook's
// entries, not in their place.
//
// Its exported identifiers:
//
//   - [OneNoteSpec] declares onenote's spike-ai-pipeline-onenote for either runner's
//     constructor.
//   - [OneNoteMinimum] is the oldest onenote the Reader accepts, 0.1.0.
//   - [Config] is the Tool a Reader is built over.
//   - [Reader] reads OneNote sections, notebooks, and packages through onenote.
//   - [NewReader] builds a Reader over a Config.
//   - [Reader.Read] returns the Stream of one input's read, or refuses its Request.
//   - [Request] is one input's read: its path, and the media directory onenote writes images,
//     embedded files, and ink into, if any.
//   - [Stream] is one read: [Stream.Records] streams its records, and [Stream.Warnings]
//     returns onenote's warnings once they have been read.
//   - [ErrRequest] is the cause of a Request Read refuses: no input, or a path that can't be
//     made absolute.
//   - [ExitError] reports onenote's exit code 1, 2, 3, or 4, with its message.
//   - [ErrNotWritten], [ErrUsage], [ErrRefused], and [ErrFailed] are onenote's exit codes 1 to
//     4.
//   - [Schema] is the version of onenote's JSON Lines the package decodes, "1".
//   - [ErrSchema] is the cause of a notebook line of another schema.
//   - [Record] is one of onenote's lines, its Line the line's kind: [Notebook],
//     [SectionGroup], [Section], [Page], [Skipped], or [UnknownRecord].
//   - [Ink], [Bounds], and [Word] are an ink record, its strokes' box, and a recognised word
//     of a page's ink text.
//   - [Containers], [Items], and [Blocks] are a page's content, an outline's items, and an
//     element's blocks, each element of [Container], [Item], or [Block], by its Kind.
//   - [Title], [Outline], [ImageContainer], [EmbeddedFileContainer], and [InkContainer] are
//     the containers, each with its [Offsets].
//   - [Element] and [Group] are the items.
//   - [Paragraph], [Table], [Image], [EmbeddedFile], and [InkBlock] are the blocks; a
//     paragraph holds its [List], its [Tag]s, and its [Run]s, and a table its [Cell]s.
//   - [Unknown] is a container, item, or block of a kind the package doesn't know.
//   - [Float] is one of onenote's f32 numbers, an offset or a bound.
package onenote
