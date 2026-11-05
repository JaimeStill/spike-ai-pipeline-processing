package route

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// Route names the stage content goes to next, for the caller to run, as the package doc lists
// them.
type Route string

const (
	// Document is a PDF, by its %PDF- signature: the document package's stage.
	Document Route = "document"
	// Office is an office document: a ZIP whose name has an OOXML extension office reads,
	// .docx, .docm, .dotx, .dotm, .xlsx, .xlsm, .xltx, .xltm, .pptx, .pptm, .potx, .potm, or
	// .ppsx; an OLE2 compound file whose name has a legacy extension, .doc or .dot, .xls or .xlt,
	// .ppt, .pps, or .pot; an ODF package; an RTF; a WordPerfect document; or a ZIP whose name
	// has the .xlsb extension, an Excel binary workbook. Extensions are matched in any case, and
	// the caller gives office's Convert the same name so it reads the same extension. The last
	// four go through LibreOffice; office's stage has no processor for the last two yet, which a
	// dispatcher reports.
	Office Route = "office"
	// Vision is a picture a vision model decodes as stored: PNG, JPEG, GIF, or BMP.
	Vision Route = "vision"
	// VisionConverted is a picture a vision model decodes only once converted to PNG: TIFF,
	// EMF, WMF, SVG, HEIC, WebP, or AVIF.
	VisionConverted Route = "vision-converted"
	// Nested is a message: an OLE2 compound file whose name has the .msg extension, or content of
	// no routed signature whose name has the .eml extension, in any case, or whose stored MIME
	// type is message/rfc822 or message/global, its parameters ignored, and whose header has the
	// shape mail reads a message by: its first line a header field, and a From or Date field
	// before its header block ends. It is a mail store too: a .pst, by its signature, and an
	// mbox, by its first line's "From ", unless its name has a text extension, since prose may
	// begin so too. The caller reads it with the email package's Reader.
	Nested Route = "nested"
	// OneNote is a OneNote section or table of contents, by its OneStore signature, or a cabinet
	// whose name has the .onepkg extension, a OneNote package.
	OneNote Route = "onenote"
	// Archive is a ZIP that is neither an office document nor an ODF package, a tar, gzip, zstd,
	// or 7z archive, or a cabinet not named .onepkg.
	Archive Route = "archive"
	// Audio is WAV, FLAC, MP3, Ogg, or M4A.
	Audio Route = "audio"
	// Video is MP4, Matroska, WebM, or AVI.
	Video Route = "video"
	// Text is content of no routed signature whose name has a text extension, in any case, or
	// that begins with a UTF-8, UTF-16, or UTF-32 byte order mark.
	Text Route = "text"
	// Unrouted is anything else: an executable, by a PE, ELF, or Mach-O signature or a "#!"
	// line; an OLE2 compound file without a legacy or .msg extension; an .ost, an offline store
	// mail refuses; content named .eml or stored as a message whose header isn't a message's
	// shape; HTML, by its .html, .htm, or .xhtml extension, web content no stage takes yet; and
	// other content of no routed signature.
	Unrouted Route = "unrouted"
)

// headerLineMax is the longest line headerShaped reads, mail's LINE_MAX; a longer one ends the
// header block.
const headerLineMax = 64 * 1024

// messageTypes are the stored MIME types, as mime.ParseMediaType lowercases them, that name a
// message.
var messageTypes = map[string]bool{"message/rfc822": true, "message/global": true}

// ooxmlExtensions are the extensions of the ZIP packages office reads, each as its base format:
// the documents, their macro-enabled forms, templates, and slide show.
var ooxmlExtensions = map[string]bool{
	".docx": true, ".docm": true, ".dotx": true, ".dotm": true,
	".xlsx": true, ".xlsm": true, ".xltx": true, ".xltm": true,
	".pptx": true, ".pptm": true, ".potx": true, ".potm": true, ".ppsx": true,
}

// legacyExtensions are the legacy extensions office converts from an OLE2 compound file, its
// own rule.
var legacyExtensions = map[string]bool{
	".doc": true, ".dot": true,
	".xls": true, ".xlt": true,
	".ppt": true, ".pps": true, ".pot": true,
}

// textExtensions are the extensions that name content of no signature text: plain text, tables,
// structured data, markup, subtitles, and logs. HTML, .html, .htm, and .xhtml, is none of them:
// it is web content, which no stage takes yet, so it is unrouted, but for one whose root element
// is an SVG's, which is an SVG.
var textExtensions = map[string]bool{
	".txt": true, ".text": true, ".csv": true, ".tsv": true, ".tab": true,
	".json": true, ".ndjson": true, ".jsonl": true, ".xml": true, ".yaml": true, ".yml": true,
	".toml": true, ".ini": true, ".md": true, ".markdown": true, ".srt": true, ".vtt": true,
	".log": true,
}

// kindRoutes are the routes of the kinds whose signature decides alone, with their reasons.
var kindRoutes = map[Kind]struct {
	route Route
	why   string
}{
	PDF:  {Document, "PDF signature"},
	PNG:  {Vision, "PNG signature"},
	JPEG: {Vision, "JPEG signature"},
	GIF:  {Vision, "GIF signature"},
	BMP:  {Vision, "BMP signature"},
	TIFF: {VisionConverted, "TIFF signature"},
	EMF:  {VisionConverted, "EMF signature"},
	WMF:  {VisionConverted, "WMF signature"},
	SVG:  {VisionConverted, "SVG root element"},
	HEIC: {VisionConverted, "HEIC brand"},
	WebP: {VisionConverted, "WebP signature"},
	AVIF: {VisionConverted, "AVIF brand"},
	ODF:  {Office, "ODF mimetype entry"},
	RTF:  {Office, "RTF signature"},
	// WordPerfect goes through LibreOffice, which office's stage has no processor for yet.
	WordPerfect: {Office, "WordPerfect signature, a WordPerfect document"},
	PST:         {Nested, "PST signature, a .pst"},
	OST:         {Unrouted, "PST signature with an .ost's client magic, an offline store mail refuses"},
	Tar:         {Archive, "tar signature"},
	OneStore:    {OneNote, "OneStore signature"},
	WAV:         {Audio, "WAV signature"},
	FLAC:        {Audio, "FLAC signature"},
	MP3:         {Audio, "MP3 signature"},
	Ogg:         {Audio, "Ogg signature"},
	M4A:         {Audio, "M4A brand"},
	MP4:         {Video, "MP4 brand"},
	MKV:         {Video, "Matroska signature"},
	WebM:        {Video, "WebM signature"},
	AVI:         {Video, "AVI signature"},
	Gzip:        {Archive, "gzip signature"},
	Zstd:        {Archive, "zstd signature"},
	SevenZip:    {Archive, "7z signature"},
	PE:          {Unrouted, "PE signature, an executable"},
	ELF:         {Unrouted, "ELF signature, an executable"},
	MachO:       {Unrouted, "Mach-O signature, an executable"},
	Shebang:     {Unrouted, `"#!" line, an executable script`},
}

// boms are the byte order marks' names, for Text's reason.
var boms = map[Kind]string{UTF8BOM: "UTF-8", UTF16BOM: "UTF-16", UTF32BOM: "UTF-32"}

// File returns the route of the file at path and why, in a few words, such as "PDF signature".
// It reads the file's first [HeadLen] bytes for a signature, which decides whatever mimeType,
// its stored MIME type, says. The extension is name's, in any case, which may differ from
// path's, as a file named by its SHA-256 does; it decides within a ZIP or OLE2 container, for a
// cabinet, whether an mbox's first line is text's instead, and, with mimeType, whether content of no signature is a message, whose header File
// then reads further, line by line, a line at most 64 KiB, and else whether it is text. It
// returns an error, as os reports it, when the file can't be opened or read.
func File(path, name, mimeType string) (Route, string, error) {
	head, err := readHead(path)
	if err != nil {
		return "", "", err
	}
	ext := strings.ToLower(filepath.Ext(name))
	kind := Identify(head)
	if route, why := byKind(kind, ext); route != "" {
		return route, why, nil
	}
	if as := messageNamed(ext, mimeType); as != "" {
		shaped, err := headerShapedFile(path)
		if err != nil {
			return "", "", err
		}
		if !shaped {
			return Unrouted, "a message by its " + as + ", but not header-shaped", nil
		}
		return Nested, "a message by its " + as + ", header-shaped", nil
	}
	if textExtensions[ext] {
		return Text, "text by its " + ext + " extension", nil
	}
	if bom, ok := boms[kind]; ok {
		return Text, "text by its " + bom + " byte order mark", nil
	}
	return Unrouted, "no routed signature", nil
}

// byKind routes content of kind, named with ext, its name's extension lowercased, which decides
// only within a ZIP or OLE2 container, for a cabinet, and for an mbox. It returns no route for
// content of no routed signature, a byte order mark's included, and for an mbox named with a
// text extension, which File decides.
func byKind(kind Kind, ext string) (Route, string) {
	if r, ok := kindRoutes[kind]; ok {
		return r.route, r.why
	}
	switch kind {
	case ZIP:
		switch {
		case ooxmlExtensions[ext]:
			return Office, "ZIP signature, " + ext + " extension"
		case ext == ".xlsb":
			// An Excel binary workbook, which office doesn't read: it goes through LibreOffice.
			return Office, "ZIP signature, .xlsb extension, an Excel binary workbook"
		}
		return Archive, "ZIP signature without an OOXML extension or ODF mimetype"
	case OLE2:
		switch {
		case legacyExtensions[ext]:
			return Office, "OLE2 signature, " + ext + " extension"
		case ext == ".msg":
			return Nested, "OLE2 signature, .msg extension"
		}
		return Unrouted, "OLE2 signature without a legacy or .msg extension"
	case Mbox:
		// "From " is a weak signature, which prose begins too: a text extension decides first.
		if textExtensions[ext] {
			return "", ""
		}
		return Nested, `mbox "From " line`
	case MSCF:
		if ext == ".onepkg" {
			return OneNote, "cabinet signature, .onepkg extension"
		}
		return Archive, "cabinet signature without a .onepkg extension"
	}
	return "", ""
}

// readHead returns the first HeadLen bytes of the file at path, fewer for a shorter file.
func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, HeadLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return head[:n], nil
}

// messageNamed returns what names content a message, its ext, the .eml extension, else its
// stored type, mimeType, message/rfc822 or message/global; or "" when neither does. The type is
// parsed as a media type, in any case and its parameters ignored, malformed ones included.
func messageNamed(ext, mimeType string) string {
	if ext == ".eml" {
		return ".eml extension"
	}
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return ""
	}
	if messageTypes[mediaType] {
		return mediaType + " type"
	}
	return ""
}

// headerShapedFile reports whether the file at path is headerShaped.
func headerShapedFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	return headerShaped(f)
}

// headerShaped reports whether r has the shape of an RFC 5322 message, mail's header_shape: its
// first line a header field, and a From or Date field in its header block. The block runs to its
// blank line, to the end of the input, or to the first line that is neither a field nor a
// field's folded continuation; a line past headerLineMax ends it once its first headerLineMax
// bytes are read. An mbox's "From " separator is no field, so an input that begins with one isn't
// header-shaped. The field's name is checked by fieldName, not net/textproto's, which refuses the
// space before the colon mail allows.
func headerShaped(r io.Reader) (bool, error) {
	// A line is read through its newline or to the buffer's size, whichever comes first.
	br := bufio.NewReaderSize(r, headerLineMax)
	for first := true; ; first = false {
		line, err := br.ReadSlice('\n')
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
			return false, err
		}
		content := bytes.TrimSuffix(line, []byte("\n"))
		content = bytes.TrimSuffix(content, []byte("\r"))
		if len(content) == 0 {
			return false, nil
		}
		folded := content[0] == ' ' || content[0] == '\t'
		name := fieldName(content)
		switch {
		case name != nil && (bytes.EqualFold(name, []byte("From")) || bytes.EqualFold(name, []byte("Date"))):
			return true, nil
		case name != nil, folded && !first:
		default:
			return false, nil
		}
		if !bytes.HasSuffix(line, []byte("\n")) {
			// The end of the input, or a line past headerLineMax.
			return false, nil
		}
	}
}

// fieldName returns the name of the header field line starts, or nil if it starts none: printable
// ASCII but the colon (RFC 5322 3.6.8's ftext), then the colon, after any spaces or tabs, as RFC
// 5322 4.5.8's obsolete syntax allows.
func fieldName(line []byte) []byte {
	end := 0
	for end < len(line) && (line[end] >= 33 && line[end] <= 126 && line[end] != ':') {
		end++
	}
	rest := bytes.TrimLeft(line[end:], " \t")
	if end == 0 || len(rest) == 0 || rest[0] != ':' {
		return nil
	}
	return line[:end]
}
