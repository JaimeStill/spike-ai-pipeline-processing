package route_test

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// zipEntry returns a ZIP's first local file header, its entry named name, stored by method,
// with content as its data and its sizes in the header.
func zipEntry(name string, method uint16, content string) string {
	b := make([]byte, 30)
	copy(b, "PK\x03\x04")
	binary.LittleEndian.PutUint16(b[4:], 20)
	binary.LittleEndian.PutUint16(b[8:], method)
	binary.LittleEndian.PutUint32(b[18:], uint32(len(content)))
	binary.LittleEndian.PutUint32(b[22:], uint32(len(content)))
	binary.LittleEndian.PutUint16(b[26:], uint16(len(name)))
	return string(b) + name + content + "PK\x03\x04"
}

// odfText is an ODF text document's first entry.
var odfText = zipEntry("mimetype", 0, "application/vnd.oasis.opendocument.text")

// ftyp returns an ISO-BMFF file's ftyp box, its major brand and compatible brands, then a moov
// box's start.
func ftyp(major string, compatible ...string) string {
	body := major + "\x00\x00\x02\x00" + strings.Join(compatible, "")
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	return string(b) + "ftyp" + body + "\x00\x00\x00\x08moov"
}

// ebml returns an EBML header holding docType as its DocType element.
func ebml(docType string) string {
	return "\x1a\x45\xdf\xa3\xa3\x42\x86\x81\x01\x42\xf7\x81\x01\x42\x82" + string([]byte{byte(0x80 | len(docType))}) + docType
}

// riffForm returns a RIFF file's header of form type form.
func riffForm(form string) string {
	return "RIFF\x24\x00\x00\x00" + form + "fmt "
}

// Further constructed content, each its format's signature followed by filler.
var (
	oneSection = "\xe4\x52\x5c\x7b\x8c\xd8\xa7\x4d\xae\xb1\x53\x78\xd0\x29\x96\xd3" + strings.Repeat("\x00", 16)
	oneTOC     = "\xa1\x2f\xff\x43\xd9\xef\x76\x4c\x9e\xe2\x10\xea\x57\x22\x76\x5f" + strings.Repeat("\x00", 16)
	mscf       = "MSCF\x00\x00\x00\x00\x2c\x01\x00\x00"
	svgPlain   = `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`
	svgProlog  = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- a comment -->\n" +
		"<!DOCTYPE svg PUBLIC \"-//W3C//DTD SVG 1.1//EN\" \"http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd\">\n" +
		"<svg xmlns=\"http://www.w3.org/2000/svg\">"
)

// TestIdentify: each kind is named by a minimal head of its signature, and each near-miss of a
// signature is another kind or none.
func TestIdentify(t *testing.T) {
	cases := []struct {
		name string
		head string
		want route.Kind
	}{
		{"pdf", pdfBytes, route.PDF},
		{"png", pngBytes, route.PNG},
		{"jpeg", jpegBytes, route.JPEG},
		{"gif87a", gif87, route.GIF},
		{"gif89a", gif89, route.GIF},
		{"bmp", bmp(40), route.BMP},
		{"tiff little-endian", tiffLE, route.TIFF},
		{"tiff big-endian", tiffBE, route.TIFF},
		{"emf", emf(1, " EMF"), route.EMF},
		{"wmf placeable", wmfPlace, route.WMF},
		{"wmf standard", wmf(1, 9, 0x0300), route.WMF},
		{"svg", svgPlain, route.SVG},
		{"svg after a prolog", svgProlog, route.SVG},
		{"svg after a byte order mark", "\xef\xbb\xbf" + svgPlain, route.SVG},
		{"svg after a doctype's internal subset", `<!DOCTYPE svg [ <!ENTITY a "b"> ]><svg>`, route.SVG},
		{"heic", ftyp("heic", "mif1", "heic"), route.HEIC},
		{"heif structural brand", ftyp("mif1", "mif1", "heic", "hevc"), route.HEIC},
		{"avif", ftyp("avif", "avif", "mif1", "miaf"), route.AVIF},
		{"avif under a structural brand", ftyp("mif1", "mif1", "avif", "miaf"), route.AVIF},
		{"webp", riffForm("WEBP"), route.WebP},
		{"zip", zipBytes, route.ZIP},
		{"ole2", ole2Bytes, route.OLE2},
		{"odf", odfText, route.ODF},
		{"odf spreadsheet", zipEntry("mimetype", 0, "application/vnd.oasis.opendocument.spreadsheet"), route.ODF},
		{"rtf", `{\rtf1\ansi`, route.RTF},
		{"onestore section", oneSection, route.OneStore},
		{"onestore table of contents", oneTOC, route.OneStore},
		{"cabinet", mscf, route.MSCF},
		{"wav", riffForm("WAVE"), route.WAV},
		{"flac", "fLaC\x00\x00\x00\x22", route.FLAC},
		{"mp3 id3", "ID3\x04\x00\x00\x00\x00\x00\x22", route.MP3},
		{"mp3 mpeg-1 frame", "\xff\xfb\x90\x64\x00", route.MP3},
		{"mp3 mpeg-2 frame", "\xff\xf3\x88\xc0\x00", route.MP3},
		{"ogg", "OggS\x00\x02\x00\x00", route.Ogg},
		{"m4a", ftyp("M4A ", "M4A ", "isom"), route.M4A},
		{"m4b with a nul-padded brand", ftyp("M4A\x00", "3gp5", "isom"), route.M4A},
		{"mp4", ftyp("isom", "isom", "iso2", "avc1", "mp41"), route.MP4},
		{"quicktime", ftyp("qt  ", "qt  "), route.MP4},
		{"matroska", ebml("matroska"), route.MKV},
		{"webm", ebml("webm"), route.WebM},
		{"ebml without a doctype in its head", "\x1a\x45\xdf\xa3\x9f", route.MKV},
		{"avi", riffForm("AVI "), route.AVI},
		{"gzip", "\x1f\x8b\x08\x00", route.Gzip},
		{"zstd", "\x28\xb5\x2f\xfd\x04", route.Zstd},
		{"7z", "7z\xbc\xaf\x27\x1c\x00\x04", route.SevenZip},
		{"pe", "MZ\x90\x00\x03\x00", route.PE},
		{"elf", "\x7fELF\x02\x01\x01", route.ELF},
		{"mach-o 32-bit", "\xfe\xed\xfa\xce\x00", route.MachO},
		{"mach-o 64-bit", "\xfe\xed\xfa\xcf\x00", route.MachO},
		{"mach-o 32-bit little-endian", "\xce\xfa\xed\xfe\x00", route.MachO},
		{"mach-o 64-bit little-endian", "\xcf\xfa\xed\xfe\x00", route.MachO},
		{"mach-o universal", "\xca\xfe\xba\xbe\x00", route.MachO},
		{"shebang", "#!/bin/sh\necho hi\n", route.Shebang},
		{"utf-8 bom", "\xef\xbb\xbfplain text", route.UTF8BOM},
		{"utf-16 little-endian bom", "\xff\xfea\x00", route.UTF16BOM},
		{"utf-16 big-endian bom", "\xfe\xff\x00a", route.UTF16BOM},
		{"utf-32 little-endian bom", "\xff\xfe\x00\x00a\x00\x00\x00", route.UTF32BOM},
		{"utf-32 big-endian bom", "\x00\x00\xfe\xff\x00\x00\x00a", route.UTF32BOM},

		// Near-misses.
		{"empty", "", route.Unknown},
		{"text", "plain text", route.Unknown},
		{"odf mimetype compressed", zipEntry("mimetype", 8, "application/vnd.oasis.opendocument.text"), route.ZIP},
		{"odf mimetype not first", zipEntry("content.xml", 0, "application/vnd.oasis.opendocument.text"), route.ZIP},
		{"epub mimetype", zipEntry("mimetype", 0, "application/epub+zip"), route.ZIP},
		{"odf mimetype cut short", odfText[:40], route.ZIP},
		{"riff of another form", riffForm("CDXA"), route.Unknown},
		{"riff cut short", "RIFF\x24\x00", route.Unknown},
		{"aac adts frame, layer 0", "\xff\xf1\x50\x80", route.Unknown},
		{"mpeg frame with bitrate 15", "\xff\xfb\xf0\x64", route.Unknown},
		{"mpeg frame with reserved sample rate", "\xff\xfb\x9c\x64", route.Unknown},
		{"mpeg frame of the reserved version", "\xff\xeb\x90\x64", route.Unknown},
		{"html with inline svg", "<html><svg></svg></html>", route.Unknown},
		{"svg-like element", "<svgfoo/>", route.Unknown},
		{"svg after an unterminated comment", "<!-- <svg>", route.Unknown},
		{"svg past the head", "<!--" + strings.Repeat("x", route.HeadLen) + "--><svg>", route.Unknown},
		{"ftyp cut short", "\x00\x00\x00\x18ftyp", route.Unknown},
		{"onestore guid cut short", oneSection[:8], route.Unknown},
		{"cabinet with reserved bytes set", "MSCF\x01\x00\x00\x00", route.Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := route.Identify([]byte(c.head)); got != c.want {
				t.Errorf("Identify = %q, want %q", got, c.want)
			}
		})
	}
}

// TestPNGSignature: the exported signature is the one Identify names a PNG by.
func TestPNGSignature(t *testing.T) {
	if got := route.Identify([]byte(route.PNGSignature)); got != route.PNG {
		t.Errorf("Identify(PNGSignature) = %q, want %q", got, route.PNG)
	}
	if got := route.Identify([]byte(route.PNGSignature[:7])); got == route.PNG {
		t.Errorf("Identify of seven bytes = %q, want no PNG", got)
	}
}
