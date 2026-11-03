//go:build linux

package email

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Route names the stage an attachment goes to next, for the caller to run: the package names it
// and never runs it. Routes are decided by the content's signature, read from the file mail
// wrote, never by the stored MIME type; a file name's extension decides only within a ZIP or OLE2
// container, where the signature alone can't tell its formats apart.
type Route string

const (
	// RouteDocument is a PDF, by its %PDF- signature: the document package's stage.
	RouteDocument Route = "document"
	// RouteOffice is an office document, for the office package's Convert, which is given the
	// attachment's name so it reads the same extension: a ZIP whose name has an OOXML extension
	// office reads, .docx, .docm, .dotx, .dotm, .xlsx, .xlsm, .xltx, .xltm, .pptx, .pptm, .potx,
	// .potm, or .ppsx; or an OLE2 compound file whose name has a legacy extension, .doc or .dot,
	// .xls or .xlt, .ppt, .pps, or .pot. Extensions are matched in any case.
	RouteOffice Route = "office"
	// RouteVision is a picture a vision model decodes as stored: PNG, JPEG, GIF, or BMP.
	RouteVision Route = "vision"
	// RouteVisionConverted is a picture a vision model decodes only once office's picture
	// conversion has made it a PNG: TIFF, EMF, or WMF. The caller converts it first.
	RouteVisionConverted Route = "vision-converted"
	// RouteNested is a message: an embedded message, nested under its record in a .msg and
	// listed without its content in a .pst, or a by-value .msg file, an OLE2 compound file whose
	// name has the .msg extension, for the caller to read with a Reader.
	RouteNested Route = "nested"
	// RouteUnrouted is anything else: content of no routed signature, a by-value .eml among
	// them; a by-reference attachment, which has no content file; and content that wasn't
	// written, read without an attachments directory or not written by mail.
	RouteUnrouted Route = "unrouted"
)

// The signatures Route reads. A BMP's two-byte "BM" is checked further by its DIB header's size,
// and a WMF's standard header by its size as well as its type and version.
var (
	pdfSignature  = []byte("%PDF-")
	pngSignature  = []byte("\x89PNG\r\n\x1a\n")
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
)

// sniffLen is the most of a file Route reads: enough for an EMF's signature at offset 40.
const sniffLen = 64

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

// bmpHeaderSizes are the sizes of the DIB headers a BMP begins its header with, at offset 14.
var bmpHeaderSizes = map[uint32]bool{12: true, 40: true, 52: true, 56: true, 64: true, 108: true, 124: true}

// Route returns the attachment's route and why, in a few words, such as "PDF signature". An
// embedded message is RouteNested. Any other attachment is routed by its content's file at Path,
// of which Route reads at most the first 64 bytes: an attachment with no Path, read without an
// attachments directory or with no content file, and one whose file mail didn't write, are
// RouteUnrouted. Within a ZIP or OLE2 container, the extension is Filename's, else File's, the
// name to give office's Convert. It returns an error only when the file is there but can't be
// read.
func (a *Attachment) Route() (Route, string, error) {
	if a.Method == "embedded_message" {
		if a.Message == nil {
			return RouteNested, "embedded message, listed without its content", nil
		}
		return RouteNested, "embedded message, nested under its record", nil
	}
	switch {
	case a.File == "":
		return RouteUnrouted, "no content file", nil
	case a.Path == "":
		return RouteUnrouted, "not written: no attachments directory", nil
	}
	head, err := readHead(a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return RouteUnrouted, "not written", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("email: routing %s: %w", a.File, err)
	}
	name := a.Filename
	if name == "" {
		name = a.File
	}
	route, why := sniff(head, strings.ToLower(filepath.Ext(name)))
	return route, why, nil
}

// readHead returns the first sniffLen bytes of the file at path, fewer for a shorter file.
func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return head[:n], nil
}

// sniff routes content by head, its first bytes, and ext, its name's extension lowercased, which
// decides only within a ZIP or OLE2 container.
func sniff(head []byte, ext string) (Route, string) {
	switch {
	case bytes.HasPrefix(head, pdfSignature):
		return RouteDocument, "PDF signature"
	case bytes.HasPrefix(head, pngSignature):
		return RouteVision, "PNG signature"
	case bytes.HasPrefix(head, jpegSignature):
		return RouteVision, "JPEG signature"
	case bytes.HasPrefix(head, gif87), bytes.HasPrefix(head, gif89):
		return RouteVision, "GIF signature"
	case isBMP(head):
		return RouteVision, "BMP signature"
	case bytes.HasPrefix(head, tiffLE), bytes.HasPrefix(head, tiffBE):
		return RouteVisionConverted, "TIFF signature"
	case isEMF(head):
		return RouteVisionConverted, "EMF signature"
	case isWMF(head):
		return RouteVisionConverted, "WMF signature"
	case bytes.HasPrefix(head, zipSignature):
		if ooxmlExtensions[ext] {
			return RouteOffice, "ZIP signature, " + ext + " extension"
		}
		return RouteUnrouted, "ZIP signature without an OOXML extension"
	case bytes.HasPrefix(head, ole2Signature):
		switch {
		case legacyExtensions[ext]:
			return RouteOffice, "OLE2 signature, " + ext + " extension"
		case ext == ".msg":
			return RouteNested, "OLE2 signature, .msg extension"
		}
		return RouteUnrouted, "OLE2 signature without a legacy or .msg extension"
	}
	return RouteUnrouted, "no routed signature"
}

// isBMP reports whether head begins a BMP: "BM", then a DIB header of a known size at offset 14.
func isBMP(head []byte) bool {
	return len(head) >= 18 && bytes.HasPrefix(head, bmpSignature) &&
		bmpHeaderSizes[binary.LittleEndian.Uint32(head[14:18])]
}

// isEMF reports whether head begins an EMF: an EMR_HEADER record, type 1, with the " EMF"
// signature at offset 40.
func isEMF(head []byte) bool {
	return len(head) >= 44 && binary.LittleEndian.Uint32(head) == 1 && bytes.Equal(head[40:44], emfSignature)
}

// isWMF reports whether head begins a WMF: a placeable header's key, or a standard META_HEADER,
// its type 1 (memory) or 2 (disk), its size 9 words, and its version 0x0300 or 0x0100.
func isWMF(head []byte) bool {
	if bytes.HasPrefix(head, wmfPlaceable) {
		return true
	}
	if len(head) < 6 {
		return false
	}
	typ := binary.LittleEndian.Uint16(head)
	size := binary.LittleEndian.Uint16(head[2:])
	version := binary.LittleEndian.Uint16(head[4:])
	return (typ == 1 || typ == 2) && size == 9 && (version == 0x0300 || version == 0x0100)
}
