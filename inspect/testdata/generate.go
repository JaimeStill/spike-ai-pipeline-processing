//go:build ignore

// generate writes the inspect package's synthesized hazard fixtures, ZIPs, tars, and gzip
// streams among them, into the directory it runs in, every byte fixed, so a rerun writes the
// same files. Run it from inspect/testdata with
// `go run generate.go`; the check never runs it, and reads the committed files.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"slices"
	"time"
)

// modified is every entry's time, so the archives are the same bytes on every run.
var modified = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)

// entry is one ZIP entry: its name, content, and, when set, its Unix mode and general purpose
// flags; declared, when set, replaces the sizes its header declares.
type entry struct {
	name     string
	body     string
	mode     fs.FileMode
	flags    uint16
	declared uint64
}

func main() {
	files := map[string][]byte{
		"traversal.zip": zipOf(
			entry{name: "notes.txt", body: "an ordinary entry\n"},
			entry{name: "docs/../../evil.txt", body: "written outside the extraction directory\n"},
		),
		"absolute.zip": zipOf(
			entry{name: "/tmp/evil.txt", body: "an absolute path\n"},
			entry{name: `C:\evil.txt`, body: "a drive letter\n"},
		),
		"symlink.zip": zipOf(
			entry{name: "notes.txt", body: "an ordinary entry\n"},
			entry{name: "passwd", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777},
		),
		// bomb.zip holds 8 MiB of zeros, about 1,030:1: past the ratio, under the default size,
		// so a test lowers the size to see the rule fire.
		"bomb.zip": zipOf(entry{name: "zeros.bin", body: string(make([]byte, 8<<20))}),
		// declared-bomb.zip's one entry declares 4 GiB expanded from its few compressed bytes,
		// past both default thresholds by its central directory alone.
		"declared-bomb.zip": zipOf(entry{name: "zeros.bin", body: string(make([]byte, 1024)), declared: 4 << 30}),
		"encrypted.zip": zipOf(
			entry{name: "notes.txt", body: "an ordinary entry\n"},
			entry{name: "secret.txt", body: "ciphertext stands in here\n", flags: 0x1},
		),
		"macro.docm": zipOf(
			entry{name: "[Content_Types].xml", body: contentTypes},
			entry{name: "_rels/.rels", body: packageRels},
			entry{name: "word/document.xml", body: document},
			entry{name: "word/_rels/document.xml.rels", body: documentRels},
			entry{name: "word/_rels/settings.xml.rels", body: settingsRels},
			entry{name: "word/settings.xml", body: settings},
			entry{name: "word/vbaProject.bin", body: "stands in for a VBA project"},
			entry{name: "word/activeX/activeX1.xml", body: `<ax:ocx xmlns:ax="http://schemas.microsoft.com/office/2006/activeX"/>`},
			entry{name: "word/embeddings/oleObject1.bin", body: "stands in for an OLE object"},
		),
		"hyperlink.docx": zipOf(
			entry{name: "[Content_Types].xml", body: contentTypes},
			entry{name: "_rels/.rels", body: packageRels},
			entry{name: "word/document.xml", body: document},
			entry{name: "word/_rels/document.xml.rels", body: documentRels},
		),
		"javascript.pdf":  []byte(javascriptPDF),
		"script.svg":      []byte(scriptSVG),
		"invoice.pdf.exe": pe(),
		"mismatch.pdf":    png(),
		"polyglot.jpg":    append(jpeg(), zipOf(entry{name: "hidden.txt", body: "appended\n"})...),
		"polyglot.png":    append(png(), []byte("%PDF-1.7\n%%EOF\n")...),
		"encrypted.xlsx":  encryptedOOXML(),

		"clean.tar": tarOf(cleanTar...),
		// clean.tar.gz's header names its tar; unnamed.tgz's names nothing, so its tar is named
		// by the stream's stem.
		"clean.tar.gz": gzipOf("clean.tar", tarOf(cleanTar...)),
		"unnamed.tgz":  gzipOf("", tarOf(cleanTar...)),
		"traversal.tar": tarOf(
			tent{name: "notes.txt", body: "an ordinary entry\n"},
			tent{name: "docs/../../evil.txt", body: "written outside the extraction directory\n"},
		),
		// traversal.tar.gz holds traversal.tar: the gzip is safe, the tar it holds isn't.
		"traversal.tar.gz": gzipOf("traversal.tar", tarOf(
			tent{name: "notes.txt", body: "an ordinary entry\n"},
			tent{name: "docs/../../evil.txt", body: "written outside the extraction directory\n"},
		)),
		"absolute.tar": tarOf(
			tent{name: "/tmp/evil.txt", body: "an absolute path\n"},
			tent{name: `C:\evil.txt`, body: "a drive letter\n"},
		),
		"symlink.tar": tarOf(
			tent{name: "notes.txt", body: "an ordinary entry\n"},
			tent{name: "passwd", typ: tar.TypeSymlink, link: "/etc/passwd"},
		),
		"hardlink.tar": tarOf(
			tent{name: "notes.txt", body: "an ordinary entry\n"},
			tent{name: "shadow", typ: tar.TypeLink, link: "/etc/shadow"},
		),
		"device.tar": tarOf(
			tent{name: "notes.txt", body: "an ordinary entry\n"},
			tent{name: "null", typ: tar.TypeChar, major: 1, minor: 3},
			tent{name: "sda", typ: tar.TypeBlock, major: 8},
			tent{name: "pipe", typ: tar.TypeFifo},
		),
		// sparse-bomb.tar's one entry is a GNU sparse file declaring 4 GiB, all a hole but its
		// last 512 bytes, in a 2 KiB tar: past both default thresholds by its header alone, and
		// as written once its holes are read as zeros.
		"sparse-bomb.tar": sparseBomb(),
		// bomb.tar.gz holds a tar of 8 MiB of zeros, about 1,000:1 as the gzip expands: past
		// the ratio, under the default size, as bomb.zip is. The tar itself is no bomb.
		"bomb.tar.gz": gzipOf("bomb.tar", tarOf(tent{name: "zeros.bin", body: string(make([]byte, 8<<20))})),
	}
	for name, b := range files {
		if err := os.WriteFile(name, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

// zipOf returns a ZIP of entries, each deflated, or written raw with the sizes it declares.
func zipOf(entries ...entry) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: modified}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		h.Flags |= e.flags
		if err := add(w, h, e); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// add writes e under h: through CreateHeader, or raw when e has flags or declared sizes, which
// archive/zip's writer would otherwise set itself.
func add(w *zip.Writer, h *zip.FileHeader, e entry) error {
	if e.flags == 0 && e.declared == 0 {
		f, err := w.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = f.Write([]byte(e.body))
		return err
	}
	var packed bytes.Buffer
	fw, err := flate.NewWriter(&packed, flate.BestCompression)
	if err != nil {
		return err
	}
	if _, err := fw.Write([]byte(e.body)); err != nil {
		return err
	}
	if err := fw.Close(); err != nil {
		return err
	}
	h.CRC32 = crc32.ChecksumIEEE([]byte(e.body))
	h.CompressedSize64 = uint64(packed.Len())
	h.UncompressedSize64 = uint64(len(e.body))
	if e.declared != 0 {
		h.UncompressedSize64 = e.declared
	}
	f, err := w.CreateRaw(h)
	if err != nil {
		return err
	}
	_, err = f.Write(packed.Bytes())
	return err
}

// tent is one tar entry: its name and content, and, when set, its type, its link's target, and
// its device numbers. Its type is a regular file's when unset.
type tent struct {
	name         string
	body         string
	typ          byte
	link         string
	major, minor int64
}

// cleanTar are clean.tar's entries: a directory, and a text and a picture under it. The picture
// is 2x1, so its bytes are no other fixture's.
var cleanTar = []tent{
	{name: "docs/", typ: tar.TypeDir},
	{name: "docs/notes.txt", body: "an ordinary entry\n"},
	{name: "docs/pixel.png", body: string(pngSized(2, 1))},
}

// tarOf returns a POSIX tar of entries, in order.
func tarOf(entries ...tent) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{
			Typeflag: e.typ, Name: e.name, Linkname: e.link, Mode: 0o644, ModTime: modified,
			Devmajor: e.major, Devminor: e.minor, Format: tar.FormatUSTAR,
		}
		switch e.typ {
		case 0:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		case tar.TypeDir:
			h.Mode = 0o755
		}
		if err := w.WriteHeader(h); err != nil {
			panic(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// gzipOf returns b as a gzip stream, its header naming name, or nothing when name is empty, at
// best compression.
func gzipOf(name string, b []byte) []byte {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		panic(err)
	}
	w.Name, w.ModTime = name, modified
	if _, err := w.Write(b); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// sparseBomb returns a tar of one old GNU sparse entry, written by hand, since archive/tar
// writes no sparse file: its header declares a real size of 4 GiB, its sparse map one region,
// the last 512 bytes, stored after the header, and the rest a hole. Then the two zero blocks
// that end a tar.
func sparseBomb() []byte {
	const real, stored = 4 << 30, 512
	h := make([]byte, 512)
	octal := func(off, n int, v int64) { copy(h[off:off+n], fmt.Sprintf("%0*o\x00", n-1, v)) }
	copy(h, "zeros.bin")
	octal(100, 8, 0o644)            // mode
	octal(108, 8, 0)                // uid
	octal(116, 8, 0)                // gid
	octal(124, 12, stored)          // size: the bytes stored
	octal(136, 12, modified.Unix()) // mtime
	h[156] = tar.TypeGNUSparse      // typeflag
	copy(h[257:], "ustar  \x00")    // GNU's magic and version
	octal(386, 12, real-stored)     // the sparse map's one region: its offset,
	octal(398, 12, stored)          // and its length
	octal(483, 12, real)            // the real size
	copy(h[148:156], "        ")    // the checksum, summed as spaces
	var sum int64
	for _, c := range h {
		sum += int64(c)
	}
	copy(h[148:156], fmt.Sprintf("%06o\x00 ", sum))
	data := bytes.Repeat([]byte("x"), stored)
	return slices.Concat(h, data, make([]byte, 1024))
}

// pe returns the start of a PE executable: an MZ header whose e_lfanew points at a PE
// signature.
func pe() []byte {
	b := make([]byte, 128)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 64)
	copy(b[64:], "PE\x00\x00")
	return b
}

// png returns a 1x1 PNG's signature and IHDR chunk, enough for its signature.
func png() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde")
}

// pngSized returns a PNG's signature and an IHDR chunk for a width by height RGB picture, its
// CRC-32 computed, enough for its signature.
func pngSized(width, height uint32) []byte {
	chunk := []byte("IHDR")
	chunk = binary.BigEndian.AppendUint32(chunk, width)
	chunk = binary.BigEndian.AppendUint32(chunk, height)
	chunk = append(chunk, 8, 2, 0, 0, 0)
	b := append([]byte("\x89PNG\r\n\x1a\n"), 0, 0, 0, 13)
	b = append(b, chunk...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(chunk))
}

// jpeg returns a JPEG's start of image and an APP0 marker, enough for its signature.
func jpeg() []byte {
	return []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00\xff\xd9")
}

// encryptedOOXML returns the shape of an encrypted OOXML package as the checks read it: an OLE2
// compound file's signature, and its directory's EncryptedPackage and EncryptionInfo stream
// names in UTF-16LE. It is no readable compound file.
func encryptedOOXML() []byte {
	b := make([]byte, 1024)
	copy(b, "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")
	off := 512
	for _, name := range []string{"EncryptionInfo", "EncryptedPackage"} {
		for i := range len(name) {
			b[off+2*i] = name[i]
		}
		off += 128
	}
	return b
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.ms-word.document.macroEnabled.main+xml"/></Types>`

const packageRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`

const document = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>A synthesized hazard fixture.</w:t></w:r></w:p></w:body></w:document>`

// documentRels holds a hyperlink, external, which isn't active content.
const documentRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.invalid/page" TargetMode="External"/></Relationships>`

// settingsRels holds a remote template, external, which is.
const settingsRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/attachedTemplate" Target="https://example.invalid/template.dotm" TargetMode="External"/></Relationships>`

const settings = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:attachedTemplate r:id="rId1"/></w:settings>`

// javascriptPDF is a one-page PDF whose catalog runs JavaScript when it opens, through
// /OpenAction, and again through /AA, named /J#61vaScript, hex-escaped, so a scan that matches
// bytes alone misses it. Its cross-reference table isn't kept: no reader opens it here.
const javascriptPDF = `%PDF-1.7
1 0 obj
<< /Type /Catalog /Pages 2 0 R /OpenAction 4 0 R /AA << /O 5 0 R >> >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>
endobj
4 0 obj
<< /S /JavaScript /JS (app.alert\('opened'\);) >>
endobj
5 0 obj
<< /S /J#61vaScript /JS (app.alert\('again'\);) >>
endobj
trailer
<< /Root 1 0 R >>
%%EOF
`

// scriptSVG holds each of the SVG checks once, and an internal href and a data: href, which
// aren't external.
const scriptSVG = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="100" height="100" onload="alert(1)">
  <script>alert(2)</script>
  <defs><rect id="box" width="10" height="10"/></defs>
  <use href="#box"/>
  <image href="data:image/png;base64,iVBORw0KGgo=" width="1" height="1"/>
  <image xlink:href="https://example.invalid/tracker.png" width="1" height="1"/>
  <foreignObject width="10" height="10"><div xmlns="http://www.w3.org/1999/xhtml">markup</div></foreignObject>
</svg>
`
