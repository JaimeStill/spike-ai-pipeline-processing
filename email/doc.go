//go:build linux

// Package email reads mail stores, Outlook .pst stores and .msg messages, and MIME messages, .eml
// and mbox files, for a model through mail, spike-ai-pipeline-mail: an input in, its records
// streamed out as typed Go values while mail runs. It provides pre-inference preparation only; a
// layer above sends what it returns to a model. The package builds on Linux only, on the standard
// library and this module's tool package; the program it runs is a tool.Tool the caller built.
//
// A Reader is built at the composition root over a mail Tool, MailSpec declaring it. NewReader
// refuses one older than 0.2.0, and the Tool's own construction a missing one, so a missing or
// outdated mail fails at startup. mail's contract is its README: its JSON Lines, schema "1", its
// attachment naming, its warnings, and its exit codes.
//
// Reader.Read returns a Stream for one input, a path, refusing an invalid Request at once with an
// error matching ErrRequest, before anything runs; ranging over the Stream's Records runs mail once
// on it and yields each line mail writes as it writes it: the store first, then, for a .pst, each
// folder before its messages and they before its subfolders, with a skipped record wherever mail
// couldn't read one; for a .msg or an .eml, its message, with any embedded message nested under its
// attachment record, then its skipped attachments; for an mbox, each message in the file's order,
// with a skipped record for one mail couldn't read. A Request's After resumes a .pst's or an
// mbox's read after the message whose id it is, through mail's --after: the store, every folder,
// and every folder's skipped record, then only the messages after it, the whole read's tail byte
// for byte. It needs mail 0.3.0, MailAfterMinimum, which Read checks only when After is set, so a
// caller that never resumes may stay on 0.2.0. mail's stdout reaches the package through a
// pipe and is decoded one line at a time, so memory follows the record being read, never the
// store. Each line is decoded through encoding/json/v2 into the typed value of its kind, Store,
// Folder, Message, or Skipped, which mirror schema "1": addresses, times, the bodies stored and
// derived, and attachment records. A line of a kind the package doesn't know is kept as an
// UnknownRecord, never an error. A store line of any other schema is an error matching ErrSchema
// that names the schema. A loop that stops early stops mail.
//
// Each line mail writes to stderr as a warning is one of the Stream's Warnings, its prefix taken
// off, and a read with skipped records and warnings succeeds. mail's exit codes 1 to 4 are an
// *ExitError carrying mail's message, which matches its code's error through errors.Is:
// ErrNotWritten, ErrUsage (a fault in this package), ErrRefused (with mail's reason, an .ost's
// among them), or ErrFailed (a fault of mail's). Each is the exit error every CLI this module
// runs shares, so office.ErrRefused matches mail's refusal, and ErrRefused office's. A read whose
// context ends returns an error matching the context's, as every Tool run does.
//
// Given an attachments directory in its Request, mail writes each distinct attachment content
// there once, named by its SHA-256, and each Attachment's Path names its file. Attachment.Route
// names the stage the content goes to next, which the caller runs; the package never runs it. An
// embedded message is nested; any other attachment is routed by the route package's File over
// the file mail wrote, by the content's signature, never by the stored MIME type or the name
// alone, which real stores show to be unreliable: MsgKit stores an EMF as
// application/octet-stream, and Apache POI's alfresco.gif is a BMP. The route package's doc
// lists the routes: a PDF goes to the document package's stage; an office document to the office
// package's Convert; PNG, JPEG, GIF, and BMP to vision as stored; TIFF, EMF, and WMF to vision
// once converted to PNG; an OLE2 file named .msg, or content of no routed signature that is named
// .eml or stored as message/rfc822 or message/global and whose header has the shape mail reads a
// message by, is nested, for the caller to read, as are a .pst and an mbox; a CSV or a text file
// is text; and so on.
//
// Threads come from a store's messages: a Threader, fed each record while a Stream is read, keeps
// their threading keys alone, so its memory follows the number of messages, never their bodies.
// Threader.Threads builds them by RFC 5256's REFERENCES algorithm over Message-ID, In-Reply-To,
// and References, with one addition: a message whose headers name no parent takes the one its
// conversation index names, its own less the last child block. A Message-ID or index parent no
// message has is a placeholder, pruned as RFC 5256 says: one with no children is removed, and one
// with children gives them to its own parent, unless it is a root holding several, which it keeps
// together. Roots with the same base subject are then gathered, a reply under the message it
// replies to, so a store without threading fields threads by subject alone. Each set of children
// is ordered by sent date. Threader.MarshalBinary saves the keys, in a versioned, deterministic
// format, and UnmarshalBinary restores them, so a read resumed after message K threads whole: a
// Threader fed the records up to K, saved, restored, and fed the read resumed after K returns the
// threads of the whole read.
//
// Known limits: a by-value .eml whose header isn't a message's shape is unrouted, unless it
// begins with an mbox's "From " line, which makes it an mbox, and header-shaped content neither
// named .eml nor stored as a message isn't nested; an .ost is refused by mail, and unrouted; in a .pst, an embedded message is nested
// without its content, which mail can't read; a by-reference attachment has no content file, so
// it is unrouted; and the stores mail's generator writes, its fixtures and its scale store, carry
// no threading fields, so they thread by subject alone. mail's own limits reach MIME input: it
// reads every mbox as an mboxrd, so an mboxcl's Content-Length is ignored and an mboxo body line
// that was ">From " loses its ">"; and an .eml's or an mbox's message has no class, no creation
// or modification time, and SMTP addresses alone.
//
// Its exported identifiers:
//
//   - [MailSpec] declares mail's spike-ai-pipeline-mail for either runner's constructor.
//   - [MailMinimum] is the oldest mail the Reader accepts, 0.2.0.
//   - [MailAfterMinimum] is the oldest mail that resumes a read, 0.3.0.
//   - [Config] is the Tool a Reader is built over.
//   - [Reader] reads mail stores and MIME messages through mail.
//   - [NewReader] builds a Reader over a Config.
//   - [Reader.Read] returns the Stream of one input's read, or refuses its Request.
//   - [Request] is one input's read: its path, the attachments directory mail writes their
//     content into, if any, and the message its read resumes after, if any.
//   - [Stream] is one read: [Stream.Records] streams its records, and [Stream.Warnings]
//     returns mail's warnings once they have been read.
//   - [ErrRequest] is the cause of a Request Read refuses: no input, a path that can't be made
//     absolute, or an After past mail's range or given a mail older than MailAfterMinimum.
//   - [ExitError] reports mail's exit code 1, 2, 3, or 4, with its message.
//   - [ErrNotWritten], [ErrUsage], [ErrRefused], and [ErrFailed] are mail's exit codes 1 to 4.
//   - [Schema] is the version of mail's JSON Lines the package decodes, "1".
//   - [ErrSchema] is the cause of a store line of another schema.
//   - [Record] is one of mail's lines, its Line the line's kind: [Store], [Folder], [Message],
//     [Skipped], or [UnknownRecord].
//   - [Address], [Body], [Derived], and [Attachment] are a message's addresses, bodies, and
//     attachment records; an Attachment's Path names its content's file in the attachments
//     directory.
//   - [Attachment.Route] returns an attachment's route.Route and why: route.Nested for an
//     embedded message, route.Unrouted for an attachment without a written content file, and
//     route.File's route for any other.
//   - [Threader] builds a store's threads from the threading keys of the messages
//     [Threader.Add] is fed while a Stream is read; [Threader.Threads] returns them;
//     [Threader.MarshalBinary] and [Threader.UnmarshalBinary] save and restore its keys.
//   - [Threads] builds the threads of a read's Records whole.
//   - [Thread] is one message of a thread, or a placeholder, with its replies.
package email
