package inspect

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// maxRels is the most of a relationships part, expanded, the OOXML checks read; a larger one is
// read that far.
const maxRels = 16 << 20

// relHyperlink is the last element of a hyperlink relationship's type URI: an external
// hyperlink is a link a reader follows by choice, not content fetched when the document opens,
// so it isn't active content.
const relHyperlink = "hyperlink"

// zip reads a ZIP's central directory, extracting no entry, and checks each entry's name, mode,
// declared sizes, and encryption; an OOXML package, one with a [Content_Types].xml entry, has
// its parts and relationships checked too. A central directory archive/zip can't read is
// [ZIPUnreadable].
func (in *inspection) zip(ctx context.Context) error {
	r, err := zip.NewReader(in.file, in.size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		in.flag(ZIPUnreadable, err.Error())
		return nil
	}
	ooxml := false
	for _, f := range r.File {
		if f.Name == "[Content_Types].xml" {
			ooxml = true
		}
	}
	for _, f := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		in.zipEntry(f)
		if ooxml {
			in.ooxmlPart(f)
		}
	}
	return nil
}

// zipEntry checks one entry's name, mode, sizes, and encryption. The bomb rule reads the sizes
// the central directory declares: the stage that extracts the entry enforces the same limits
// on the bytes it writes, since a declared size may lie.
func (in *inspection) zipEntry(f *zip.File) {
	quoted := fmt.Sprintf("entry %q", f.Name)
	elements := strings.FieldsFunc(f.Name, func(r rune) bool { return r == '/' || r == '\\' })
	if slices.Contains(elements, "..") {
		in.flag(ZIPTraversal, quoted)
	}
	if strings.HasPrefix(f.Name, "/") || strings.HasPrefix(f.Name, `\`) || driveLetter(f.Name) {
		in.flag(ZIPAbsolutePath, quoted)
	}
	if f.Mode()&fs.ModeSymlink != 0 {
		in.flag(ZIPSymlink, quoted)
	}
	expanded, compressed := f.UncompressedSize64, max(f.CompressedSize64, 1)
	if ratio := float64(expanded) / float64(compressed); expanded > in.opts.BombSize && ratio > in.opts.BombRatio {
		in.flag(ZIPBomb, fmt.Sprintf("%s: %d bytes from %d, %.0f:1", quoted, expanded, f.CompressedSize64, ratio))
	}
	if f.Flags&0x1 != 0 {
		in.flag(ZIPEncrypted, quoted)
	}
}

// driveLetter reports whether name begins with a drive letter and a colon, as "C:" does.
func driveLetter(name string) bool {
	return len(name) >= 2 && name[1] == ':' &&
		(name[0] >= 'a' && name[0] <= 'z' || name[0] >= 'A' && name[0] <= 'Z')
}

// ooxmlPart checks one part of an OOXML package, by its name: a VBA project, a macro sheet, an
// ActiveX control, or an OLE object; and a relationships part's external relationships.
func (in *inspection) ooxmlPart(f *zip.File) {
	lower := strings.ToLower(f.Name)
	base := path.Base(lower)
	quoted := fmt.Sprintf("part %q", f.Name)
	switch {
	case base == "vbaproject.bin":
		in.flag(VBAProject, quoted)
	case strings.HasPrefix(lower, "xl/macrosheets/"):
		in.flag(MacroSheet, quoted)
	case strings.Contains(lower, "/activex/") || strings.HasPrefix(lower, "activex/"):
		in.flag(ActiveX, quoted)
	case strings.Contains(lower, "embeddings/") && strings.HasPrefix(base, "oleobject"):
		in.flag(OLEObject, quoted)
	}
	if strings.HasSuffix(lower, ".rels") && f.Flags&0x1 == 0 {
		in.relationships(f)
	}
}

// relationship is one Relationship element of a relationships part.
type relationship struct {
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

// relationships checks a relationships part for an external relationship, other than a
// hyperlink: a remote template, a linked picture, an OLE link. A part that doesn't open is
// checked no further, and one that doesn't decode as far as it decodes.
func (in *inspection) relationships(f *zip.File) {
	rc, err := f.Open()
	if err != nil {
		return
	}
	defer func() { _ = rc.Close() }()
	d := xml.NewDecoder(io.LimitReader(rc, maxRels))
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var rel relationship
		if err := d.DecodeElement(&rel, &start); err != nil {
			return
		}
		if typ := path.Base(rel.Type); strings.EqualFold(rel.TargetMode, "External") && typ != relHyperlink {
			in.flag(ExternalRelationship, fmt.Sprintf("%s: %s %s", f.Name, typ, rel.Target))
		}
	}
}
