package route

import (
	"bytes"
	"encoding/binary"
)

// Kind names the format a signature identifies; the zero Kind, [Unknown], is content of no
// signature [Identify] knows.
type Kind string

// The kinds [Identify] names.
const (
	Unknown Kind = ""

	PDF Kind = "pdf"

	PNG  Kind = "png"
	JPEG Kind = "jpeg"
	GIF  Kind = "gif"
	BMP  Kind = "bmp"

	TIFF Kind = "tiff"
	EMF  Kind = "emf"
	WMF  Kind = "wmf"
	SVG  Kind = "svg"
	HEIC Kind = "heic"
	WebP Kind = "webp"
	AVIF Kind = "avif"

	// ZIP and OLE2 are containers whose formats their signature can't tell apart; [File] reads
	// the name's extension for them.
	ZIP  Kind = "zip"
	OLE2 Kind = "ole2"
	// ODF is a ZIP whose first entry, mimetype, stored uncompressed, names an OpenDocument type.
	ODF Kind = "odf"
	RTF Kind = "rtf"
	// WordPerfect is a WordPerfect document, by its "\xFFWPC" prefix.
	WordPerfect Kind = "wordperfect"

	// PST is a .pst, by its "!BDN" magic and a .pst's client magic, "SM"; OST is the same magic
	// with an .ost's, "SO". mail tells them apart the same way, and reads the first and refuses
	// the second.
	PST Kind = "pst"
	OST Kind = "ost"
	// Mbox is content whose first line begins "From ", an mbox's separator line, as mail sniffs
	// one.
	Mbox Kind = "mbox"

	// OneStore is a OneNote section (.one) or table of contents (.onetoc2), by the file type
	// GUID it begins with.
	OneStore Kind = "onestore"
	// MSCF is a cabinet, the container a OneNote package (.onepkg) is.
	MSCF Kind = "mscf"

	WAV  Kind = "wav"
	FLAC Kind = "flac"
	MP3  Kind = "mp3"
	Ogg  Kind = "ogg"
	M4A  Kind = "m4a"

	MP4  Kind = "mp4"
	MKV  Kind = "mkv"
	WebM Kind = "webm"
	AVI  Kind = "avi"

	Gzip     Kind = "gzip"
	Zstd     Kind = "zstd"
	SevenZip Kind = "7z"
	// Tar is a POSIX or GNU tar archive, by the "ustar" magic at offset 257 of its first header.
	Tar Kind = "tar"

	PE      Kind = "pe"
	ELF     Kind = "elf"
	MachO   Kind = "mach-o"
	Shebang Kind = "shebang"

	// The byte order marks: text, but no signature of a format, so [File] reads a message's
	// name and a text extension before them.
	UTF8BOM  Kind = "utf-8 bom"
	UTF16BOM Kind = "utf-16 bom"
	UTF32BOM Kind = "utf-32 bom"
)

// HeadLen is the most of a file's first bytes [Identify] reads: enough for an ODF package's
// mimetype entry, a Matroska DocType, an ISO-BMFF file's brands, and an SVG's root element after
// a short prolog.
const HeadLen = 4096

// PNGSignature is the eight bytes every PNG begins with, for a caller that must match a stream's
// prefix before eight bytes of it have arrived.
const PNGSignature = "\x89PNG\r\n\x1a\n"

// The signatures Identify reads. A BMP's two-byte "BM" is checked further by its DIB header's
// size, and a WMF's standard header by its size as well as its type and version.
var (
	pdfSignature  = []byte("%PDF-")
	pngSignature  = []byte(PNGSignature)
	jpegSignature = []byte{0xff, 0xd8, 0xff}
	gif87         = []byte("GIF87a")
	gif89         = []byte("GIF89a")
	bmpSignature  = []byte("BM")
	tiffLE        = []byte("II*\x00")
	tiffBE        = []byte("MM\x00*")
	emfSignature  = []byte(" EMF")
	wmfPlaceable  = []byte{0xd7, 0xcd, 0xc6, 0x9a}
	zipSignature  = []byte("PK\x03\x04")
	ole2Signature = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
	rtfSignature  = []byte(`{\rtf`)
	wpSignature   = []byte("\xffWPC")
	pstMagic      = []byte("!BDN")
	// The OneStore file type GUIDs, as stored: a section's {7B5C52E4-D88C-4DA7-AEB1-5378D02996D3}
	// and a table of contents' {43FF2FA1-EFD9-4C76-9EE2-10EA5722765F} ([MS-ONESTORE] 2.3.1).
	oneSection    = []byte{0xe4, 0x52, 0x5c, 0x7b, 0x8c, 0xd8, 0xa7, 0x4d, 0xae, 0xb1, 0x53, 0x78, 0xd0, 0x29, 0x96, 0xd3}
	oneTOC        = []byte{0xa1, 0x2f, 0xff, 0x43, 0xd9, 0xef, 0x76, 0x4c, 0x9e, 0xe2, 0x10, 0xea, 0x57, 0x22, 0x76, 0x5f}
	mscfSignature = []byte("MSCF\x00\x00\x00\x00")
	riff          = []byte("RIFF")
	flacSignature = []byte("fLaC")
	id3Signature  = []byte("ID3")
	oggSignature  = []byte("OggS")
	ebmlSignature = []byte{0x1a, 0x45, 0xdf, 0xa3}
	gzipSignature = []byte{0x1f, 0x8b}
	zstdSignature = []byte{0x28, 0xb5, 0x2f, 0xfd}
	sevenZip      = []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}
	peSignature   = []byte("MZ")
	elfSignature  = []byte("\x7fELF")
	shebang       = []byte("#!")
	mboxSeparator = []byte("From ")
	tarMagic      = []byte("ustar")
	utf8BOM       = []byte{0xef, 0xbb, 0xbf}
	utf32LEBOM    = []byte{0xff, 0xfe, 0x00, 0x00}
	utf32BEBOM    = []byte{0x00, 0x00, 0xfe, 0xff}
	utf16LEBOM    = []byte{0xff, 0xfe}
	utf16BEBOM    = []byte{0xfe, 0xff}
)

// machOMagics are a Mach-O file's magics, 32- and 64-bit in either byte order, and a universal
// binary's.
var machOMagics = [][]byte{
	{0xfe, 0xed, 0xfa, 0xce}, {0xfe, 0xed, 0xfa, 0xcf},
	{0xce, 0xfa, 0xed, 0xfe}, {0xcf, 0xfa, 0xed, 0xfe},
	{0xca, 0xfe, 0xba, 0xbe},
}

// bmpHeaderSizes are the sizes of the DIB headers a BMP begins its header with, at offset 14.
var bmpHeaderSizes = map[uint32]bool{12: true, 40: true, 52: true, 56: true, 64: true, 108: true, 124: true}

// The ISO-BMFF brands Identify tells an ftyp box's file by, as their four bytes with trailing
// spaces and NULs trimmed. HEIF's structural brands, mif1, msf1, and miaf, are AVIF's too, so a
// file whose major brand is one of them is AVIF when its compatible brands name AVIF.
var (
	avifBrands       = map[string]bool{"avif": true, "avis": true}
	heicBrands       = map[string]bool{"heic": true, "heix": true, "heim": true, "heis": true, "hevc": true, "hevx": true, "heif": true}
	structuralBrands = map[string]bool{"mif1": true, "msf1": true, "miaf": true}
	m4aBrands        = map[string]bool{"M4A": true, "M4B": true, "M4P": true, "F4A": true, "F4B": true}
)

// odfType is the prefix of the OpenDocument media types an ODF package's mimetype entry holds.
const odfType = "application/vnd.oasis.opendocument."

// Identify names the kind head, a file's first bytes, begins, by its signature; [Unknown] for
// content of no signature it knows. It reads at most [HeadLen] bytes, and a head shorter than a
// signature's check needs is not that kind. The strong signatures are checked before the weak
// ones, a WMF's standard header and an MP3's frame sync, and the byte order marks last.
func Identify(head []byte) Kind {
	head = head[:min(len(head), HeadLen)]
	for _, check := range signatures {
		if kind := check(head); kind != Unknown {
			return kind
		}
	}
	return Unknown
}

// signatures are Identify's checks, in order: email's first, as v0.5.0 ordered them, then the
// strong ones added since; the weak ones after them, an mbox's "From " line last but the byte
// order marks.
var signatures = []func([]byte) Kind{
	prefix(pdfSignature, PDF),
	prefix(pngSignature, PNG),
	prefix(jpegSignature, JPEG),
	prefix(gif87, GIF),
	prefix(gif89, GIF),
	isBMP,
	prefix(tiffLE, TIFF),
	prefix(tiffBE, TIFF),
	isEMF,
	isWMF,
	isZIP,
	prefix(ole2Signature, OLE2),
	prefix(rtfSignature, RTF),
	prefix(wpSignature, WordPerfect),
	isPST,
	prefix(oneSection, OneStore),
	prefix(oneTOC, OneStore),
	prefix(mscfSignature, MSCF),
	isRIFF,
	prefix(flacSignature, FLAC),
	prefix(id3Signature, MP3),
	prefix(oggSignature, Ogg),
	isISOBMFF,
	isEBML,
	prefix(gzipSignature, Gzip),
	prefix(zstdSignature, Zstd),
	prefix(sevenZip, SevenZip),
	isTar,
	prefix(elfSignature, ELF),
	isMachO,
	prefix(peSignature, PE),
	prefix(shebang, Shebang),
	isMPEGFrame,
	isSVG,
	prefix(mboxSeparator, Mbox),
	prefix(utf8BOM, UTF8BOM),
	prefix(utf32LEBOM, UTF32BOM),
	prefix(utf32BEBOM, UTF32BOM),
	prefix(utf16LEBOM, UTF16BOM),
	prefix(utf16BEBOM, UTF16BOM),
}

// prefix returns a check naming kind for a head that begins with sig.
func prefix(sig []byte, kind Kind) func([]byte) Kind {
	return func(head []byte) Kind {
		if bytes.HasPrefix(head, sig) {
			return kind
		}
		return Unknown
	}
}

// isBMP names a BMP: "BM", then a DIB header of a known size at offset 14.
func isBMP(head []byte) Kind {
	if len(head) >= 18 && bytes.HasPrefix(head, bmpSignature) &&
		bmpHeaderSizes[binary.LittleEndian.Uint32(head[14:18])] {
		return BMP
	}
	return Unknown
}

// isEMF names an EMF: an EMR_HEADER record, type 1, with the " EMF" signature at offset 40.
func isEMF(head []byte) Kind {
	if len(head) >= 44 && binary.LittleEndian.Uint32(head) == 1 && bytes.Equal(head[40:44], emfSignature) {
		return EMF
	}
	return Unknown
}

// isWMF names a WMF: a placeable header's key, or a standard META_HEADER, its type 1 (memory) or
// 2 (disk), its size 9 words, and its version 0x0300 or 0x0100.
func isWMF(head []byte) Kind {
	if bytes.HasPrefix(head, wmfPlaceable) {
		return WMF
	}
	if len(head) < 6 {
		return Unknown
	}
	typ := binary.LittleEndian.Uint16(head)
	size := binary.LittleEndian.Uint16(head[2:])
	version := binary.LittleEndian.Uint16(head[4:])
	if (typ == 1 || typ == 2) && size == 9 && (version == 0x0300 || version == 0x0100) {
		return WMF
	}
	return Unknown
}

// isZIP names a ZIP's local file header, ODF when its first entry is an ODF package's: named
// mimetype, stored uncompressed, its content an OpenDocument media type.
func isZIP(head []byte) Kind {
	if !bytes.HasPrefix(head, zipSignature) {
		return Unknown
	}
	if odfMimetype(head) != "" {
		return ODF
	}
	return ZIP
}

// odfMimetype returns the media type of the ODF package head begins, the content of its first
// entry, mimetype, stored uncompressed; "" when head begins no ODF package or is too short to
// hold the entry.
func odfMimetype(head []byte) string {
	const fixed = 30
	if len(head) < fixed || binary.LittleEndian.Uint16(head[8:]) != 0 {
		return ""
	}
	size := int(binary.LittleEndian.Uint32(head[18:]))
	nameLen := int(binary.LittleEndian.Uint16(head[26:]))
	extraLen := int(binary.LittleEndian.Uint16(head[28:]))
	start := fixed + nameLen + extraLen
	if string(head[fixed:min(len(head), fixed+nameLen)]) != "mimetype" || start+size > len(head) {
		return ""
	}
	mediaType := string(head[start : start+size])
	if len(mediaType) <= len(odfType) || mediaType[:len(odfType)] != odfType {
		return ""
	}
	return mediaType
}

// isPST names a PST header by its client magic at offset 8, after the "!BDN" magic: PST for a
// .pst's, "SM", and OST for an .ost's, "SO". A header of another client, or one too short to hold
// it, is neither, as mail reads it.
func isPST(head []byte) Kind {
	if len(head) < 10 || !bytes.HasPrefix(head, pstMagic) {
		return Unknown
	}
	switch string(head[8:10]) {
	case "SM":
		return PST
	case "SO":
		return OST
	}
	return Unknown
}

// isTar names a tar archive by its first header's magic, "ustar" at offset 257: POSIX's
// "ustar\x00" and GNU's "ustar " alike. A pre-POSIX tar, which has no magic, isn't named.
func isTar(head []byte) Kind {
	if len(head) >= 262 && bytes.Equal(head[257:262], tarMagic) {
		return Tar
	}
	return Unknown
}

// isRIFF names a RIFF file by its form type at offset 8: WAVE, AVI, or WEBP.
func isRIFF(head []byte) Kind {
	if len(head) < 12 || !bytes.HasPrefix(head, riff) {
		return Unknown
	}
	switch string(head[8:12]) {
	case "WAVE":
		return WAV
	case "AVI ":
		return AVI
	case "WEBP":
		return WebP
	}
	return Unknown
}

// isISOBMFF names an ISO base media file by its ftyp box's brands: AVIF or HEIC for an image's,
// M4A for an audio file's, and MP4 for any other.
func isISOBMFF(head []byte) Kind {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return Unknown
	}
	major := brand(head[8:12])
	boxLen := min(int(binary.BigEndian.Uint32(head)), len(head))
	var compatible []string
	for i := 16; i+4 <= boxLen; i += 4 {
		compatible = append(compatible, brand(head[i:i+4]))
	}
	switch {
	case avifBrands[major]:
		return AVIF
	case heicBrands[major]:
		return HEIC
	case structuralBrands[major]:
		for _, b := range compatible {
			if avifBrands[b] {
				return AVIF
			}
		}
		return HEIC
	case m4aBrands[major]:
		return M4A
	}
	return MP4
}

// brand returns an ISO-BMFF brand's four bytes with trailing spaces and NULs trimmed.
func brand(b []byte) string {
	return string(bytes.TrimRight(b, " \x00"))
}

// isEBML names an EBML file by its DocType, WebM for "webm" and Matroska otherwise, read from
// the DocType element (ID 0x4282) in head when its size is one byte long.
func isEBML(head []byte) Kind {
	if !bytes.HasPrefix(head, ebmlSignature) {
		return Unknown
	}
	if i := bytes.Index(head, []byte{0x42, 0x82}); i >= 0 && i+3 <= len(head) && head[i+2]&0x80 != 0 {
		size := int(head[i+2] & 0x7f)
		if start := i + 3; start+size <= len(head) && string(head[start:start+size]) == "webm" {
			return WebM
		}
	}
	return MKV
}

// isMachO names a Mach-O file by its magic.
func isMachO(head []byte) Kind {
	for _, magic := range machOMagics {
		if bytes.HasPrefix(head, magic) {
			return MachO
		}
	}
	return Unknown
}

// isMPEGFrame names an MP3 that begins with an MPEG audio frame and no ID3 tag: an 11-bit frame
// sync, a version other than the reserved one, layer III, a bitrate index other than the invalid
// 15, and a sample rate index other than the reserved 3. Layer III alone keeps a UTF-16 byte order
// mark, FF FE, which is a layer I sync, from reading as one.
func isMPEGFrame(head []byte) Kind {
	if len(head) < 3 || head[0] != 0xff || head[1]&0xe0 != 0xe0 {
		return Unknown
	}
	version, layer := head[1]>>3&3, head[1]>>1&3
	bitrate, rate := head[2]>>4, head[2]>>2&3
	if version != 1 && layer == 1 && bitrate != 15 && rate != 3 {
		return MP3
	}
	return Unknown
}

// isSVG names an SVG: after an optional UTF-8 byte order mark, white space, an XML declaration,
// comments, processing instructions, and a DOCTYPE, the root element is svg. The prolog must end
// within head.
func isSVG(head []byte) Kind {
	rest := bytes.TrimPrefix(head, utf8BOM)
	for {
		rest = bytes.TrimLeft(rest, " \t\r\n")
		var end []byte
		switch {
		case bytes.HasPrefix(rest, []byte("<?")):
			end = []byte("?>")
		case bytes.HasPrefix(rest, []byte("<!--")):
			end = []byte("-->")
		case bytes.HasPrefix(rest, []byte("<!DOCTYPE")):
			end = []byte(">")
			if open := bytes.IndexByte(rest, '['); open >= 0 && open < bytes.IndexByte(rest, '>') {
				end = []byte("]>")
			}
		case bytes.HasPrefix(rest, []byte("<svg")) && len(rest) > 4 && bytes.IndexByte([]byte(" \t\r\n>/"), rest[4]) >= 0:
			return SVG
		default:
			return Unknown
		}
		i := bytes.Index(rest, end)
		if i < 0 {
			return Unknown
		}
		rest = rest[i+len(end):]
	}
}
