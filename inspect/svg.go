package inspect

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// svg reads an SVG's elements and attributes, a token at a time, for a script element, a
// foreignObject, an event handler attribute, and an href, xlink's or SVG 2's, that leaves the
// document: one that doesn't begin "#", a fragment of its own, or "data:", inline content. It
// reads leniently, as a browser would, undefined entities kept as text and never expanded, and
// checks markup that doesn't decode as far as it decodes. Its memory follows the token being
// read.
func (in *inspection) svg(ctx context.Context) error {
	if _, err := in.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	d := xml.NewDecoder(in.file)
	d.Strict = false
	for n := 0; ; n++ {
		if n%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		tok, err := d.Token()
		if err != nil {
			return nil
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		at := fmt.Sprintf("<%s> at line %d", start.Name.Local, line(d))
		switch strings.ToLower(start.Name.Local) {
		case "script":
			in.flag(SVGScript, at)
		case "foreignobject":
			in.flag(SVGForeignObject, at)
		}
		for _, a := range start.Attr {
			local := strings.ToLower(a.Name.Local)
			if strings.HasPrefix(local, "on") {
				in.flag(SVGEventHandler, fmt.Sprintf("%s on %s", a.Name.Local, at))
			}
			if local == "href" {
				v := strings.TrimSpace(a.Value)
				if !strings.HasPrefix(v, "#") && !strings.HasPrefix(strings.ToLower(v), "data:") {
					in.flag(SVGExternalHref, fmt.Sprintf("%s=%q on %s", attrName(a.Name), clip(v), at))
				}
			}
		}
	}
}

// xlinkNS is XLink's namespace, which SVG 1.1's xlink:href is in.
const xlinkNS = "http://www.w3.org/1999/xlink"

// attrName returns an attribute's name as an SVG writes it: xlink:href for XLink's href.
func attrName(n xml.Name) string {
	if n.Space == xlinkNS || n.Space == "xlink" {
		return "xlink:" + n.Local
	}
	return n.Local
}

// line returns the line d has read to.
func line(d *xml.Decoder) int {
	l, _ := d.InputPos()
	return l
}

// maxEvidence is the most of a value a flag's evidence quotes.
const maxEvidence = 120

// clip returns s cut to maxEvidence bytes, at a rune's start, and an ellipsis when cut.
func clip(s string) string {
	if len(s) <= maxEvidence {
		return s
	}
	cut := maxEvidence
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
