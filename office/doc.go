//go:build linux

// Package office prepares office documents, docx, xlsx, and pptx, for a model through office,
// spike-ai-pipeline-office: a document in, office's JSON model or its Markdown out, with the
// pictures the document embeds. It provides pre-inference preparation only; a layer above sends
// what it returns to a model. The package builds on Linux only, on the standard library and this
// module's tool package; every program it runs is a tool.Tool the caller built.
//
// A Converter is built at the composition root over an office Tool, OfficeSpec declaring it.
// NewConverter refuses one older than 0.1.0, and the Tool's own construction a missing one, so a
// missing or outdated office fails at startup. office's contract is its README: the JSON model,
// schema "1", its Markdown rendering, its media naming, and its exit codes.
//
// Convert reads a document from an io.Reader and spools it into the working directory the
// Request names, which the caller owns, makes a media directory of its own there, and runs office
// once on the spooled document, in that directory, with the request's Format and the media
// directory. The spooled document is removed when Convert returns; the media directory stays,
// the caller's, unless the conversion failed.
//
// A legacy document, .doc, .xls, or .ppt and their templates and shows, is one with the OLE2
// compound-file signature whose Request.Name has a legacy extension. Given a LibreOffice Tool in
// the Config, Convert converts it first, in the request's working directory, to the OOXML format
// its extension names, and runs office on that; the Result's ConvertedFrom names the legacy
// format. Without one, a legacy document is refused with ErrNoLibreOffice before anything runs.
// Any other OLE2 file, such as an encrypted OOXML package, goes to office, which refuses it.
// LibreOfficeSpec declares soffice, minimum 24.2, and LibreOfficeImage is the default image the
// container runner runs it in, under its full lockdown: no network, the caller's uid:gid, every
// capability dropped. Each conversion gives soffice a user profile of its own inside the working
// directory, which the runner mounts, and removes it with the converted document when it ends.
//
// A JSON result, the default, is the model decoded through encoding/json/v2 into a Document of
// typed values that mirror schema "1": each Block by its kind, each Inline by its kind. A model
// of any other schema is an error matching ErrSchema that names the schema. A property the model
// leaves out is the zero value, which means none; an optional number whose zero is meaningful,
// such as a picture's section index, is a pointer, nil for none. An array keeps its nulls in
// place, as a chart series' points do. A block or inline of a kind the package doesn't know is
// kept as an UnknownBlock or UnknownInline with its type and its JSON, never an error, since
// office's model grows by adding kinds. A Markdown result is office's Markdown text, unchanged.
//
// Both results carry the media list, one MediaFile per file in the media directory: office's
// name for it, the lowercase hex SHA-256 of its bytes and its stored extension, its content type
// by that extension, and the file handed onward, which is the stored file itself. A linked
// picture is no file, never fetched, and isn't listed. Each line office writes to stderr as a
// warning is one of the result's Warnings, and a conversion with warnings succeeds.
//
// office's exit codes 1 to 4 are an *ExitError carrying office's message, which matches its
// code's error through errors.Is: ErrMediaNotWritten, ErrUsage (a fault in this package),
// ErrRefused (with office's reason), or ErrFailed (a fault of office's). A conversion whose
// context ends returns an error matching the context's, as every Tool run does.
//
// Its exported identifiers:
//
//   - [OfficeSpec] declares office's spike-ai-pipeline-office for either runner's constructor.
//   - [OfficeMinimum] is the oldest office the Converter accepts, 0.1.0.
//   - [LibreOfficeSpec] declares LibreOffice's soffice for either runner's constructor.
//   - [LibreOfficeMinimum] is the oldest LibreOffice the Converter accepts, 24.2.
//   - [LibreOfficeImage] is the default image soffice runs in through the container runner.
//   - [ErrNoLibreOffice] is the cause of a legacy document's refusal without a LibreOffice Tool.
//   - [Config] is the Tools a Converter is built over: office, and LibreOffice for legacy
//     documents.
//   - [Converter] converts office documents through office.
//   - [NewConverter] builds a Converter over a Config.
//   - [Converter.Convert] converts one document as a Request says.
//   - [Request] is one document's conversion: its working directory, its format, and its file
//     name.
//   - [Format] is a conversion's rendering: [JSON] or [Markdown].
//   - [Format.String] returns a format's name, as office's --format takes it.
//   - [Result] is one document's conversion: the model or the Markdown, the media directory, the
//     media list, office's warnings, and the legacy format it was converted from.
//   - [MediaFile] is one file of the media list: its name, content type, the file handed onward,
//     and whether it was converted.
//   - [ContentType] returns a media file's content type by its extension.
//   - [ExitError] reports office's exit code 1, 2, 3, or 4, with its message.
//   - [ErrMediaNotWritten], [ErrUsage], [ErrRefused], and [ErrFailed] are office's exit codes 1
//     to 4.
//   - [Schema] is the version of office's JSON model the package decodes, "1".
//   - [ErrSchema] is the cause of a model of another schema.
//   - [Document] is office's JSON model of a document.
//   - [Section], [HeaderFooter], [Note], [Comment], and [CommentReply] are a docx document's
//     sections, notes, and comments.
//   - [Block] is one of a document's blocks: [Heading], [Paragraph], [List] with its
//     [ListItem]s, [Table] with its [TableRow]s and [TableCell]s, [Chart] with its
//     [ChartSeries], [Media], [Sheet], [Slide], or [UnknownBlock].
//   - [ValueType] is a sheet cell's value type: [ValueNumber], [ValueText], [ValueBoolean],
//     [ValueError], or [ValueDate].
//   - [Inline] is a piece of a block's text: [Text], [FootnoteRef], [EndnoteRef], [CommentRef],
//     or [UnknownInline].
package office
