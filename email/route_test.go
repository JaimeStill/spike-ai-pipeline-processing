//go:build linux

package email_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
)

// Constructed content, each its format's signature followed by filler.
var (
	pdfBytes  = "%PDF-1.7\n%filler"
	pngBytes  = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	jpegBytes = "\xff\xd8\xff\xe0\x00\x10JFIF\x00"
	gif87     = "GIF87a\x01\x00\x01\x00"
	gif89     = "GIF89a\x01\x00\x01\x00"
	tiffLE    = "II*\x00\x08\x00\x00\x00"
	tiffBE    = "MM\x00*\x00\x00\x00\x08"
	zipBytes  = "PK\x03\x04\x14\x00\x06\x00[Content_Types].xml"
	ole2Bytes = "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1\x00\x00\x00\x00"
	wmfPlace  = "\xd7\xcd\xc6\x9a\x00\x00\x00\x00"
)

// bmp returns a BMP's first bytes, its DIB header size at offset 14 being dib.
func bmp(dib uint32) string {
	b := make([]byte, 54)
	copy(b, "BM")
	binary.LittleEndian.PutUint32(b[14:], dib)
	return string(b)
}

// emf returns an EMF's first bytes: a record of type typ, with sig at offset 40.
func emf(typ uint32, sig string) string {
	b := make([]byte, 88)
	binary.LittleEndian.PutUint32(b, typ)
	binary.LittleEndian.PutUint32(b[4:], 108)
	copy(b[40:], sig)
	return string(b)
}

// wmf returns a standard WMF META_HEADER's first bytes: its type, its size in words, and its
// version.
func wmf(typ, size, version uint16) string {
	b := make([]byte, 18)
	binary.LittleEndian.PutUint16(b, typ)
	binary.LittleEndian.PutUint16(b[2:], size)
	binary.LittleEndian.PutUint16(b[4:], version)
	return string(b)
}

// writeContent writes content into dir under its SHA-256 and ext, as mail names it, and returns
// the name.
func writeContent(t *testing.T, dir, content, ext string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	name := hex.EncodeToString(sum[:]) + ext
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

// TestRouteSignatures: a by-value attachment written into the directory is routed by its
// content's signature, whatever its stored MIME type says; the extension, in any case, decides
// only within a ZIP or OLE2 container, and a container without a routed extension, a legacy name
// without the signature, and a near-miss of a signature are unrouted. A header-shaped .eml is
// nested, as TestRouteMessages tells apart.
func TestRouteSignatures(t *testing.T) {
	type tc struct {
		name     string
		content  string
		filename string
		mime     string
		want     email.Route
	}
	cases := []tc{
		{"pdf", pdfBytes, "report.pdf", "application/pdf", email.RouteDocument},
		{"png", pngBytes, "a.png", "image/png", email.RouteVision},
		{"jpeg", jpegBytes, "a.jpg", "image/jpeg", email.RouteVision},
		{"gif87a", gif87, "a.gif", "image/gif", email.RouteVision},
		{"gif89a", gif89, "a.gif", "image/gif", email.RouteVision},
		{"bmp", bmp(40), "a.bmp", "image/bmp", email.RouteVision},
		{"bmp core header", bmp(12), "a.bmp", "", email.RouteVision},
		{"bmp v5 header", bmp(124), "a.bmp", "", email.RouteVision},
		{"tiff little-endian", tiffLE, "a.tif", "image/tiff", email.RouteVisionConverted},
		{"tiff big-endian", tiffBE, "a.tiff", "image/tiff", email.RouteVisionConverted},
		{"emf", emf(1, " EMF"), "a.emf", "image/x-emf", email.RouteVisionConverted},
		{"wmf placeable", wmfPlace, "a.wmf", "image/x-wmf", email.RouteVisionConverted},
		{"wmf memory 3.0", wmf(1, 9, 0x0300), "a.wmf", "", email.RouteVisionConverted},
		{"wmf disk 1.0", wmf(2, 9, 0x0100), "a.wmf", "", email.RouteVisionConverted},
		{"ole2 msg", ole2Bytes, "forwarded.msg", "application/vnd.ms-outlook", email.RouteNested},
		{"ole2 MSG upper case", ole2Bytes, "FORWARDED.MSG", "", email.RouteNested},

		// Near-misses.
		{"bmp with no dib header", "BM" + strings.Repeat("x", 30), "a.bmp", "image/bmp", email.RouteUnrouted},
		{"emf type without signature", emf(1, "NOPE"), "a.emf", "image/x-emf", email.RouteUnrouted},
		{"emf signature on another record", emf(2, " EMF"), "a.emf", "", email.RouteUnrouted},
		{"wmf version 2", wmf(1, 9, 0x0200), "a.wmf", "", email.RouteUnrouted},
		{"wmf type 3", wmf(3, 9, 0x0300), "a.wmf", "", email.RouteUnrouted},
		{"wmf header size 8", wmf(1, 8, 0x0300), "a.wmf", "", email.RouteUnrouted},
		{"pdf signature cut short", "%PDF", "a.pdf", "application/pdf", email.RouteUnrouted},
		{"zip without an ooxml extension", zipBytes, "archive.zip", "application/zip", email.RouteUnrouted},
		{"zip named for a macro slide show office doesn't read", zipBytes, "a.ppsm", "", email.RouteUnrouted},
		{"zip named for a legacy format", zipBytes, "a.doc", "application/msword", email.RouteUnrouted},
		{"ole2 without an extension", ole2Bytes, "attachment", "", email.RouteUnrouted},
		{"ole2 named for ooxml, an encrypted package", ole2Bytes, "secret.docx", "", email.RouteUnrouted},
		{"ole2 named .bin", ole2Bytes, "a.bin", "application/msword", email.RouteUnrouted},
		{"legacy name without the signature", "plain text", "notes.doc", "application/msword", email.RouteUnrouted},
		{"ooxml name without the signature", "plain text", "notes.docx", "", email.RouteUnrouted},
		{"msg name without the signature", "plain text", "note.msg", "application/vnd.ms-outlook", email.RouteUnrouted},
		{"eml", "From: a@example.org\r\nSubject: x\r\n\r\nbody", "fwd.eml", "message/rfc822", email.RouteNested},
		{"text", "plain text", "a.txt", "text/plain", email.RouteUnrouted},
		{"empty", "", "a.pdf", "application/pdf", email.RouteUnrouted},

		// A stored MIME type that lies loses to the signature.
		{"pdf stored as png", pdfBytes, "a.png", "image/png", email.RouteDocument},
		{"png stored as pdf", pngBytes, "report.pdf", "application/pdf", email.RouteVision},
		{"jpeg stored as docx", jpegBytes, "a.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", email.RouteVision},
		{"text stored as pdf", "plain text", "a.pdf", "application/pdf", email.RouteUnrouted},
	}
	for _, ext := range []string{".docx", ".docm", ".dotx", ".dotm", ".xlsx", ".xlsm", ".xltx", ".xltm", ".pptx", ".pptm", ".potx", ".potm", ".ppsx", ".DOCX", ".XlSm"} {
		cases = append(cases, tc{"zip " + ext, zipBytes, "doc" + ext, "application/octet-stream", email.RouteOffice})
	}
	for _, ext := range []string{".doc", ".dot", ".xls", ".xlt", ".ppt", ".pps", ".pot", ".DOC", ".Xls", ".PPS"} {
		cases = append(cases, tc{"ole2 " + ext, ole2Bytes, "doc" + ext, "application/octet-stream", email.RouteOffice})
	}
	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := writeContent(t, dir, c.content, filepath.Ext(c.filename))
			a := email.Attachment{File: file, Filename: c.filename, MimeType: c.mime, Method: "by_value", Path: filepath.Join(dir, file)}
			got, why, err := a.Route()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want || why == "" {
				t.Errorf("route %q (%s), want %q", got, why, c.want)
			}
		})
	}
}

// headerShaped is content with a message's header: a From field first.
const headerShaped = "From: ann@example.org\r\nSubject: x\r\n\r\nbody"

// TestRouteMessages: content of no routed signature is nested when its name has the .eml
// extension, in any case, or its stored type is message/rfc822 or message/global, in any case
// and whatever its parameters, and its header has mail's shape: its first line a field, and a
// From or Date field, its name in any case, before the block ends at a blank line, a line that is
// neither a field nor a folded continuation, the end of the input, or a line past 64 KiB. One so
// named that isn't header-shaped is unrouted and says why, an mbox's "From " line first among
// them; header-shaped content neither so named nor so stored is unrouted; and content with a
// signature is routed by it, whatever its name or type.
func TestRouteMessages(t *testing.T) {
	const notShaped = "but not header-shaped"
	long := strings.Repeat("x", 64*1024)
	cases := []struct {
		name, content, filename, mime string
		want                          email.Route
		why                           string
	}{
		// Each condition on its own names a message.
		{"eml extension alone", headerShaped, "fwd.eml", "application/octet-stream", email.RouteNested, ".eml extension, header-shaped"},
		{"eml extension in upper case", headerShaped, "FWD.EML", "", email.RouteNested, ".eml extension, header-shaped"},
		{"rfc822 type alone", headerShaped, "Fwd", "message/rfc822", email.RouteNested, "message/rfc822 type, header-shaped"},
		{"global type alone", headerShaped, "fwd.bin", "message/global", email.RouteNested, "message/global type, header-shaped"},
		{"rfc822 type in odd case with parameters", headerShaped, "", "Message/RFC822; name=\"fwd\"", email.RouteNested, "message/rfc822 type, header-shaped"},
		{"global type with a malformed parameter", headerShaped, "fwd", "MESSAGE/Global; name", email.RouteNested, "message/global type, header-shaped"},

		// The header's shape.
		{"date field after a folded one", "Received: from a\r\n by b; Mon, 12 Jan 2026\r\nDate: x\r\n", "a.eml", "", email.RouteNested, "header-shaped"},
		{"space before the colon, lower-case from, bare newlines", "Subject : hi\nfrom: ann@example.org\n", "a.eml", "", email.RouteNested, "header-shaped"},
		{"date field without a newline", "date:x", "a.eml", "", email.RouteNested, "header-shaped"},
		{"from field past 64 KiB on the first line", "From: " + long + "\r\n", "a.eml", "", email.RouteNested, "header-shaped"},
		{"mbox separator first", "From ann@example.org Sat Jan  3 01:05:34 1996\nFrom: ann@example.org\n", "a.eml", "message/rfc822", email.RouteUnrouted, notShaped},
		{"plain text", "plain text", "a.eml", "message/rfc822", email.RouteUnrouted, notShaped},
		{"empty", "", "a.eml", "", email.RouteUnrouted, notShaped},
		{"blank line first", "\r\nFrom: ann@example.org\r\n", "a.eml", "", email.RouteUnrouted, notShaped},
		{"folded line first", " folded\r\nFrom: ann@example.org\r\n", "a.eml", "", email.RouteUnrouted, notShaped},
		{"from field after the blank line", "Subject: hi\r\n\r\nFrom: ann@example.org\r\n", "a.eml", "", email.RouteUnrouted, notShaped},
		{"from field after a line not a field", "Subject: hi\r\nnot a field\r\nFrom: ann@example.org\r\n", "a.eml", "", email.RouteUnrouted, notShaped},
		{"from field after a line past 64 KiB", "Subject: " + long + "\r\nFrom: ann@example.org\r\n", "a.eml", "", email.RouteUnrouted, notShaped},
		{"neither from nor date", "Subject: hi\r\nTo: bob@example.org\r\n", "a.eml", "", email.RouteUnrouted, notShaped},
		{"field without a name", ": no name\r\nFrom: ann@example.org\r\n", "a.eml", "", email.RouteUnrouted, notShaped},

		// Neither names a message.
		{"header-shaped text", headerShaped, "a.txt", "text/plain", email.RouteUnrouted, "no routed signature"},
		{"header-shaped, no name or type", headerShaped, "", "", email.RouteUnrouted, "no routed signature"},
		{"another message type", headerShaped, "a.txt", "message/partial", email.RouteUnrouted, "no routed signature"},
		{"rfc822 as a parameter's value", headerShaped, "a", "text/plain; x=message/rfc822", email.RouteUnrouted, "no routed signature"},

		// A signature decides first.
		{"pdf named and stored as a message", pdfBytes, "a.eml", "message/rfc822", email.RouteDocument, "PDF signature"},
		{"zip named as a message", zipBytes, "a.eml", "message/rfc822", email.RouteUnrouted, "ZIP signature without an OOXML extension"},
		{"ole2 stored as a message", ole2Bytes, "a.msg", "message/rfc822", email.RouteNested, "OLE2 signature, .msg extension"},
	}
	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := writeContent(t, dir, c.content, filepath.Ext(c.filename))
			a := email.Attachment{File: file, Filename: c.filename, MimeType: c.mime, Method: "by_value", Path: filepath.Join(dir, file)}
			got, why, err := a.Route()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want || !strings.HasSuffix(why, c.why) {
				t.Errorf("route %q (%s), want %q (...%s)", got, why, c.want, c.why)
			}
		})
	}
}

// TestRouteName: within a container the extension is Filename's, else File's when the record has
// no Filename; a File's extension never overrides a Filename's.
func TestRouteName(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, filename, fileExt string
		want                    email.Route
	}{
		{"file's extension, no filename", "", ".xls", email.RouteOffice},
		{"filename's extension over file's", "budget.xls", ".bin", email.RouteOffice},
		{"filename without one, file's ignored", "budget", ".xls", email.RouteUnrouted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := writeContent(t, dir, ole2Bytes+c.name, c.fileExt)
			a := email.Attachment{File: file, Filename: c.filename, Method: "by_value", Path: filepath.Join(dir, file)}
			if got, why, err := a.Route(); err != nil || got != c.want {
				t.Errorf("route %q (%s), %v; want %q", got, why, err, c.want)
			}
		})
	}
}

// TestRouteWithoutContent: an embedded message is nested, with or without its content; an
// attachment with no content file, one read without a directory, and one whose file wasn't
// written are unrouted; a file that is there but can't be read is an error.
func TestRouteWithoutContent(t *testing.T) {
	dir := t.TempDir()
	pdf := writeContent(t, dir, pdfBytes, ".pdf")
	cases := []struct {
		name string
		a    email.Attachment
		want email.Route
	}{
		{"embedded message in a .msg", email.Attachment{Method: "embedded_message", Message: &email.Message{Subject: "Inner"}}, email.RouteNested},
		{"embedded message in a .pst", email.Attachment{Method: "embedded_message"}, email.RouteNested},
		{"by reference", email.Attachment{Filename: "share.pdf", Method: "by_reference"}, email.RouteUnrouted},
		{"no directory", email.Attachment{File: pdf, Filename: "a.pdf", Method: "by_value"}, email.RouteUnrouted},
		{"not written", email.Attachment{File: "missing.pdf", Filename: "a.pdf", Method: "by_value", Path: filepath.Join(dir, "missing.pdf")}, email.RouteUnrouted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why, err := c.a.Route()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want || why == "" {
				t.Errorf("route %q (%s), want %q", got, why, c.want)
			}
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		sub := filepath.Join(dir, "a-directory.pdf")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		a := email.Attachment{File: "a-directory.pdf", Method: "by_value", Path: sub}
		if got, _, err := a.Route(); err == nil {
			t.Errorf("route %q, want an error", got)
		}
	})
}

// attachmentLines are a .msg's lines whose attachments have files, one nested in an embedded
// message, and one that names no bare file.
const attachmentLines = `{"line":"store","schema":"1","format":"msg"}
{"line":"message","class":"IPM.Note","attachments":[{"file":"aa.pdf","filename":"a.pdf","method":"by_value","size":1},{"filename":"Fwd","method":"embedded_message","message":{"class":"IPM.Note","attachments":[{"file":"bb.png","filename":"b.png","method":"by_value","size":1}]}},{"filename":"share","method":"by_reference"},{"file":"../escape.pdf","method":"by_value","size":1}]}
`

// TestReadAttachments: a Request's attachments directory, made absolute, is passed to mail
// before the input, and each attachment with a bare File, nested ones included, is given its
// Path there; without a directory none is passed and no attachment has a Path.
func TestReadAttachments(t *testing.T) {
	r, runs := standinReader(t)
	t.Setenv(stdoutEnv, attachmentLines)
	cwd := t.TempDir()
	t.Chdir(cwd)
	dir, input := filepath.Join(cwd, "att"), filepath.Join(cwd, "in.msg")

	recs, err := collect(r.Read(t.Context(), email.Request{Input: "in.msg", Attachments: "att"}))
	if err != nil {
		t.Fatal(err)
	}
	m := recs[1].(*email.Message)
	got := []string{m.Attachments[0].Path, m.Attachments[1].Message.Attachments[0].Path, m.Attachments[2].Path, m.Attachments[3].Path}
	want := []string{filepath.Join(dir, "aa.pdf"), filepath.Join(dir, "bb.png"), "", ""}
	if !slices.Equal(got, want) {
		t.Errorf("paths %q, want %q", got, want)
	}

	recs, err = collect(r.Read(t.Context(), email.Request{Input: "in.msg"}))
	if err != nil {
		t.Fatal(err)
	}
	m = recs[1].(*email.Message)
	if p := m.Attachments[0].Path + m.Attachments[1].Message.Attachments[0].Path; p != "" {
		t.Errorf("paths %q without a directory, want none", p)
	}

	wantRuns := [][]string{{"--attachments", dir, input}, {input}}
	if all := runs(); !reflect.DeepEqual(all, wantRuns) {
		t.Errorf("runs %q, want %q", all, wantRuns)
	}
}

// routed is one attachment's expected route: its filename and route.
type routed struct {
	filename string
	route    email.Route
}

// fixtureRoutes is the route of every attachment the copied fixtures hold, written by hand from
// the goldens, per fixture in the order a read gives them, each attachment followed by those of
// the message it nests. A fixture not listed holds none.
var fixtureRoutes = map[string][]routed{
	"generated/msg/03-html-inline-image.msg":   {{"moth.png", email.RouteVision}},
	"generated/msg/04-attachment-cyrillic.msg": {{"отчёт.csv", email.RouteUnrouted}},
	"generated/msg/08-forward-attached-message.msg": {
		{"note-g.txt", email.RouteUnrouted},
		// MsgKit's forward attaches the message by value, as an .eml, header-shaped and stored
		// as message/rfc822.
		{"Notes on the engine (draft).eml", email.RouteNested},
	},
	"generated/pst/mailbox.pst": {
		{"moth.png", email.RouteVision},
		{"отчёт.csv", email.RouteUnrouted},
		{"note-g.txt", email.RouteUnrouted},
	},
	// alfresco.gif is a BMP, named .gif and stored as image/gif: its signature routes it.
	"msg/poi/example_sent_regular.msg": {{"alfresco.gif", email.RouteVision}},
	"msg/tika/testMSG_att_msg.msg": {
		{"Test Attachment", email.RouteNested},
		{"smbprn.00009008.KdcPjl.pdf", email.RouteDocument},
	},
	"msg/tika/test-outlook2003.msg": outlookJPEGs,
	// The .pst stores' embedded messages, attach method 5, are listed without their content.
	"pst/java-libpst/dist-list.pst": {{"", email.RouteNested}, {"", email.RouteNested}},
	"pst/libyal/outlook.pst":        slices.Concat(outlookJPEGs, outlookJPEGs),
	"pst/tika/testPST.pst":          {{"", email.RouteNested}},
}

// mimeRoutes is the route of every attachment the MIME inputs hold, written by hand from the
// inputs, keyed by the input's path under testdata/ as fixtureRoutes is under fixtures/. Generated
// 03's picture goes to vision; 04's CSV and 08's text are unrouted; 08's forwarded message, a
// message/rfc822 part, is an embedded message, without a filename, nested under its record, and
// holds no attachment of its own. The mboxrd holds 03, 04, and 08 among its nine. The built
// .msg's .eml source attaches builtSources, each routed as the .msg's is. An input not listed
// holds none.
var mimeRoutes = map[string][]routed{
	"mail/fixtures/generated/eml/03-html-inline-image.eml":   {{"moth.png", email.RouteVision}},
	"mail/fixtures/generated/eml/04-attachment-cyrillic.eml": {{"отчёт.csv", email.RouteUnrouted}},
	"mail/fixtures/generated/eml/08-forward-attached-message.eml": {
		{"note-g.txt", email.RouteUnrouted}, {"", email.RouteNested},
	},
	"mail/fixtures/mime/nine.mboxrd": {
		{"moth.png", email.RouteVision},
		{"отчёт.csv", email.RouteUnrouted},
		{"note-g.txt", email.RouteUnrouted}, {"", email.RouteNested},
	},
	"built/" + builtEML: builtRoutes(),
}

// builtRoutes returns builtSources' filenames and routes, in order.
func builtRoutes() []routed {
	var all []routed
	for _, s := range builtSources {
		all = append(all, routed{s.filename, s.route})
	}
	return all
}

// outlookJPEGs are the JPEGs of Outlook 2003's welcome message, 11 of them, no 11.jpg among them.
var outlookJPEGs = []routed{
	{"1.jpg", email.RouteVision}, {"2.jpg", email.RouteVision}, {"3.jpg", email.RouteVision},
	{"4.jpg", email.RouteVision}, {"5.jpg", email.RouteVision}, {"6.jpg", email.RouteVision},
	{"7.jpg", email.RouteVision}, {"8.jpg", email.RouteVision}, {"9.jpg", email.RouteVision},
	{"10.jpg", email.RouteVision}, {"12.jpg", email.RouteVision},
}

// attachmentsOf returns every attachment of recs' messages, each followed by those of the message
// it nests.
func attachmentsOf(recs []email.Record) []email.Attachment {
	var all []email.Attachment
	var walk func(m *email.Message)
	walk = func(m *email.Message) {
		for _, a := range m.Attachments {
			all = append(all, a)
			if a.Message != nil {
				walk(a.Message)
			}
		}
	}
	for _, rec := range recs {
		if m, ok := rec.(*email.Message); ok {
			walk(m)
		}
	}
	return all
}

// TestRealRoutes runs the real pinned mail over every copied fixture and MIME input with an
// attachments directory, and routes each attachment as it is read: each is routed as
// fixtureRoutes or mimeRoutes says, and
// each with a file has its content at Path, under the SHA-256 name mail gives it. Read again
// without a directory, nothing is written, every by-value attachment is unrouted, and embedded
// messages are still nested.
func TestRealRoutes(t *testing.T) {
	r := realReader(t)
	root, err := filepath.Abs(filepath.Join(mailCopy, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	type input struct {
		rel, path string
		want      []routed
	}
	var inputs []input
	stores, mime := realInputs(t)
	for _, rel := range stores {
		inputs = append(inputs, input{rel, filepath.Join(root, rel), fixtureRoutes[rel]})
	}
	for _, rel := range mime {
		inputs = append(inputs, input{rel, filepath.Join(testdata, rel), mimeRoutes[rel]})
	}
	for _, in := range inputs {
		rel := in.rel
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			var got []routed
			s := mustRead(t)(r.Read(t.Context(), email.Request{Input: in.path, Attachments: dir}))
			for rec, err := range s.Records() {
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range attachmentsOf([]email.Record{rec}) {
					route, why, err := a.Route()
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, routed{a.Filename, route})
					if a.File != "" {
						checkWritten(t, dir, a)
					}
					t.Logf("%s: %s (%s)", a.Filename, route, why)
				}
			}
			if want := in.want; !slices.Equal(got, want) {
				t.Errorf("routes %v, want %v", got, want)
			}
		})
		t.Run(rel+"/without a directory", func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)
			recs, err := collect(r.Read(t.Context(), email.Request{Input: in.path}))
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range attachmentsOf(recs) {
				want := email.RouteUnrouted
				if a.Method == "embedded_message" {
					want = email.RouteNested
				}
				if got, why, err := a.Route(); err != nil || got != want || a.Path != "" {
					t.Errorf("%s: route %q (%s), %v, path %q; want %q and no path", a.Filename, got, why, err, a.Path, want)
				}
			}
			if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
				t.Errorf("working directory holds %v, %v; want nothing written", entries, err)
			}
		})
	}
}

// TestRealNestedEML runs the real pinned mail over generated 08's .msg with an attachments
// directory: its forward, by value, "Notes on the engine (draft).eml", stored as message/rfc822,
// 324 bytes, is routed nested, and a second read of the file mail wrote gives the forwarded
// message: an .eml store and one message, Ada Lovelace's draft, without attachments.
func TestRealNestedEML(t *testing.T) {
	r := realReader(t)
	dir := t.TempDir()
	input := filepath.Join(mailCopy, "fixtures", "generated", "msg", "08-forward-attached-message.msg")
	recs, err := collect(r.Read(t.Context(), email.Request{Input: input, Attachments: dir}))
	if err != nil {
		t.Fatal(err)
	}
	all := attachmentsOf(recs)
	i := slices.IndexFunc(all, func(a email.Attachment) bool { return a.Filename == "Notes on the engine (draft).eml" })
	if i < 0 {
		t.Fatalf("no forwarded .eml among %d attachments", len(all))
	}
	a := all[i]
	if a.Method != "by_value" || a.MimeType != "message/rfc822" || a.Size == nil || *a.Size != 324 {
		t.Errorf("forward: method %q, type %q, size %v; want by_value message/rfc822, 324 bytes", a.Method, a.MimeType, a.Size)
	}
	if route, why, err := a.Route(); err != nil || route != email.RouteNested {
		t.Fatalf("forward: route %q (%s), %v; want %q", route, why, err, email.RouteNested)
	}

	again, err := collect(r.Read(t.Context(), email.Request{Input: a.Path, Attachments: t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(again))
	for i, rec := range again {
		got[i] = describe(rec)
	}
	if want := []string{"store eml", "message"}; !slices.Equal(got, want) {
		t.Fatalf("read again: %q, want %q", got, want)
	}
	m := again[1].(*email.Message)
	if m.Subject != "Notes on the engine (draft)" || m.Sender.Address != "ada@example.org" {
		t.Errorf("read again: subject %q, sender %+v; want Ada Lovelace's draft", m.Subject, m.Sender)
	}
}

// checkWritten checks that a's content was written at its Path in dir, under the lowercase hex
// SHA-256 of its bytes and its File's extension, its size the record's.
func checkWritten(t *testing.T, dir string, a email.Attachment) {
	t.Helper()
	if a.Path != filepath.Join(dir, a.File) {
		t.Errorf("%s: path %q, want %s in %s", a.Filename, a.Path, a.File, dir)
		return
	}
	content, err := os.ReadFile(a.Path)
	if err != nil {
		t.Errorf("%s: %v", a.Filename, err)
		return
	}
	sum := sha256.Sum256(content)
	if want := hex.EncodeToString(sum[:]) + filepath.Ext(a.File); a.File != want {
		t.Errorf("%s: file %s, its content's name %s", a.Filename, a.File, want)
	}
	if a.Size == nil || *a.Size != int64(len(content)) {
		t.Errorf("%s: size %v, the file's %d", a.Filename, a.Size, len(content))
	}
}
