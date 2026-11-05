package inspect

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// chunkSize is how much of a file the stream reads at a time, each chunk beginning at a multiple
// of it.
const chunkSize = 1 << 16

// pdfNames are the PDF names the scan flags, decoded, each with its check: pdfid's, and two more.
var pdfNames = map[string]Check{
	"JavaScript": PDFJavaScript, "JS": PDFJS, "OpenAction": PDFOpenAction, "AA": PDFAA,
	"Launch": PDFLaunch, "EmbeddedFile": PDFEmbeddedFile, "RichMedia": PDFRichMedia,
	"XFA": PDFXFA, "Encrypt": PDFEncrypt,
	// Beyond pdfid's list, which counts /EmbeddedFile, an embedded file stream's optional type: the
	// catalog's name tree of embedded files and a file specification's embedded file key, which
	// mark one whether its stream has the type or not.
	"EmbeddedFiles": PDFEmbeddedFile, "EF": PDFEmbeddedFile,
}

// maxPDFName is the longest name, raw, the PDF scan reads; a longer one is none of pdfNames,
// whose longest, hex-escaped throughout, is 36 bytes.
const maxPDFName = 64

// containerKinds are the kinds whose content holds other files' bytes as stored, so another
// format's signature inside one is a file it holds, not a polyglot: archives, compound files,
// mail stores, and OneNote's stores. Text, of no signature or marked by a byte order mark or an
// mbox's "From " line, isn't checked either: it may quote a signature.
var containerKinds = map[route.Kind]bool{
	route.ZIP: true, route.ODF: true, route.OLE2: true, route.PST: true, route.OST: true,
	route.Mbox: true, route.Tar: true, route.MSCF: true, route.OneStore: true,
	route.SevenZip: true, route.Gzip: true, route.Zstd: true,
	route.Unknown: true, route.UTF8BOM: true, route.UTF16BOM: true, route.UTF32BOM: true,
	route.Shebang: true,
}

// The signatures the stream looks for: a ZIP's end-of-central-directory record, a PDF's header,
// and the name of an encrypted OOXML package's stream in an OLE2 directory, in UTF-16LE.
var (
	eocdSignature    = []byte("PK\x05\x06")
	pdfHeader        = []byte("%PDF-")
	encryptedPackage = utf16le("EncryptedPackage")
)

// eocdLen is the length of an end-of-central-directory record without its comment.
const eocdLen = 22

// maxEOCDCandidates is how many end-of-central-directory signatures the stream keeps to check,
// the last ones, where a ZIP appended to a file has its record.
const maxEOCDCandidates = 64

// utf16le returns s, ASCII, in UTF-16LE.
func utf16le(s string) []byte {
	b := make([]byte, 0, 2*len(s))
	for i := range len(s) {
		b = append(b, s[i], 0)
	}
	return b
}

// pattern is a byte string the stream looks for, and what it does at each offset it is found.
type pattern struct {
	sig   []byte
	found func(off int64)
}

// stream reads the whole file once, from its start, for what the kind and the extension call
// for: the PDF names in a PDF, the polyglot signatures in content that isn't a container, and an
// EncryptedPackage stream in an OLE2 compound file named as an OOXML package. Its memory is a
// chunk and a few bytes carried between chunks.
func (in *inspection) stream(ctx context.Context) error {
	var patterns []pattern
	var names *pdfScanner
	if in.kind == route.PDF || in.ext == ".pdf" {
		names = &pdfScanner{in: in}
	}
	var eocd []int64
	if !containerKinds[in.kind] {
		patterns = append(patterns, pattern{eocdSignature, func(off int64) {
			if len(eocd) == maxEOCDCandidates {
				eocd = eocd[1:]
			}
			eocd = append(eocd, off)
		}})
		if in.kind != route.PDF {
			patterns = append(patterns, pattern{pdfHeader, func(off int64) {
				in.flag(Polyglot, fmt.Sprintf("PDF header at offset %d after a %s signature", off, in.kind))
			}})
		}
	}
	if in.kind == route.OLE2 && ooxmlExtensions[in.ext] {
		patterns = append(patterns, pattern{encryptedPackage, func(int64) { in.encryptedPackage = true }})
	}
	if names == nil && len(patterns) == 0 {
		return nil
	}

	carryMax := 0
	for _, p := range patterns {
		carryMax = max(carryMax, len(p.sig)-1)
	}
	if _, err := in.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	buf := make([]byte, carryMax+chunkSize)
	carry := 0
	var base int64 // the file offset of buf[0]
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := in.file.Read(buf[carry : carry+chunkSize])
		if n > 0 {
			data := buf[:carry+n]
			for _, p := range patterns {
				// A match ending within the carried bytes was found in the chunk before.
				for i := 0; ; {
					j := bytes.Index(data[i:], p.sig)
					if j < 0 {
						break
					}
					at := i + j
					if at+len(p.sig) > carry {
						p.found(base + int64(at))
					}
					i = at + 1
				}
			}
			if names != nil {
				names.scan(data[carry:], base+int64(carry))
			}
			keep := min(carryMax, len(data))
			copy(buf, data[len(data)-keep:])
			base += int64(len(data) - keep)
			carry = keep
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("inspect: %w", err)
		}
	}
	if names != nil {
		names.end()
	}
	return in.checkEOCD(eocd)
}

// checkEOCD flags a polyglot for the first candidate that reads as a real end-of-central-
// directory record: its central directory ends at or before the record, and its comment ends
// within the file. Content of no container kind holds a ZIP then, appended or embedded.
func (in *inspection) checkEOCD(candidates []int64) error {
	rec := make([]byte, eocdLen)
	for _, off := range candidates {
		if off+eocdLen > in.size {
			continue
		}
		if _, err := in.file.ReadAt(rec, off); err != nil {
			return fmt.Errorf("inspect: %w", err)
		}
		cdSize := int64(binary.LittleEndian.Uint32(rec[12:]))
		cdOffset := int64(binary.LittleEndian.Uint32(rec[16:]))
		comment := int64(binary.LittleEndian.Uint16(rec[20:]))
		if cdSize > 0 && cdOffset+cdSize <= off && off+eocdLen+comment <= in.size {
			in.flag(Polyglot, fmt.Sprintf("ZIP end-of-central-directory record at offset %d in a %s", off, in.kind))
			return nil
		}
	}
	return nil
}

// pdfScanner reads a PDF's names, a chunk at a time, as pdfid does: a name begins with "/" and
// runs to the next delimiter or whitespace, and "#" and two hex digits stand for a byte, so
// /J#61vaScript is /JavaScript.
type pdfScanner struct {
	in *inspection
	// raw is the name being read, its "/" left out; inName is set while one is.
	raw    []byte
	inName bool
	// start is the file offset of the name's "/"; long is set once it outgrows maxPDFName.
	start int64
	long  bool
}

// pdfDelimiter reports whether b ends a PDF name: whitespace or a delimiter.
func pdfDelimiter(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ', '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// scan reads chunk, whose first byte is at file offset base.
func (s *pdfScanner) scan(chunk []byte, base int64) {
	for i := 0; i < len(chunk); {
		if !s.inName {
			j := bytes.IndexByte(chunk[i:], '/')
			if j < 0 {
				return
			}
			i += j
			s.begin(base + int64(i))
			i++
			continue
		}
		b := chunk[i]
		if pdfDelimiter(b) {
			s.end()
			if b == '/' {
				s.begin(base + int64(i))
			}
			i++
			continue
		}
		if len(s.raw) < maxPDFName {
			s.raw = append(s.raw, b)
		} else {
			s.long = true
		}
		i++
	}
}

// begin starts a name whose "/" is at file offset off.
func (s *pdfScanner) begin(off int64) {
	s.inName, s.start, s.long, s.raw = true, off, false, s.raw[:0]
}

// end ends the name being read, flagging it when it is one of pdfNames.
func (s *pdfScanner) end() {
	if !s.inName {
		return
	}
	s.inName = false
	if s.long {
		return
	}
	if c, ok := pdfNames[decodeName(s.raw)]; ok {
		s.in.flag(c, fmt.Sprintf("/%s at offset %d", s.raw, s.start))
	}
}

// decodeName decodes a PDF name's "#" escapes; a "#" not followed by two hex digits is kept.
func decodeName(raw []byte) string {
	if bytes.IndexByte(raw, '#') < 0 {
		return string(raw)
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' && i+2 < len(raw) {
			hi, ok1 := unhex(raw[i+1])
			lo, ok2 := unhex(raw[i+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, raw[i])
	}
	return string(out)
}

// unhex returns the value of the hex digit c.
func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
