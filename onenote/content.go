//go:build linux

package onenote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Containers is a page's content: its containers, the title area first, then the rest in stored
// order. Each is a *Title, *Outline, *ImageContainer, *EmbeddedFileContainer, or *InkContainer,
// and an *Unknown for a kind schema "1" adds later.
type Containers []Container

// Items is an outline's, a title area's, a group's, or a table cell's items, nested as stored:
// each an *Element or a *Group, and an *Unknown for a kind schema "1" adds later.
type Items []Item

// Blocks is an outline element's blocks: each a *Paragraph, *Table, *Image, *EmbeddedFile, or
// *InkBlock, and an *Unknown for a kind schema "1" adds later.
type Blocks []Block

// Container is one of a page's containers, by its kind. Kind returns its "kind" field.
type Container interface {
	Kind() string
	container()
}

// Item is one of an outline's items, by its kind. Kind returns its "kind" field.
type Item interface {
	Kind() string
	item()
}

// Block is one of an outline element's blocks, by its kind. Kind returns its "kind" field.
type Block interface {
	Kind() string
	block()
}

// Offsets is a container's place: from the page's origin, in half-inch units, as stored. Each is
// nil when the container has none, since zero is a place.
type Offsets struct {
	OffsetHorizontal *Float `json:"offset_horizontal,omitzero"`
	OffsetVertical   *Float `json:"offset_vertical,omitzero"`
}

// Title is the page's title area: its outlines' items, in order.
type Title struct {
	Offsets
	Items Items `json:"items,omitzero"`
}

// Outline is a text container.
type Outline struct {
	Offsets
	Items Items `json:"items,omitzero"`
}

// ImageContainer is an image placed on the page as a container of its own, rather than in an
// outline: its offsets, then the image's fields.
type ImageContainer struct {
	Offsets
	Image
}

// EmbeddedFileContainer is an embedded file placed on the page as a container of its own: its
// offsets, then the file's fields.
type EmbeddedFileContainer struct {
	Offsets
	EmbeddedFile
}

// InkContainer is one stored ink container. Its strokes are drawn in the page's Ink, not on
// their own.
type InkContainer struct {
	Offsets
	// Strokes is its stroke count, written even when zero.
	Strokes int `json:"strokes"`
	// Bounds is the box its strokes span on the page, in ink units; nil when its strokes have
	// no points.
	Bounds *Bounds `json:"bounds,omitzero"`
}

// Element is an outline element: its blocks, then the items indented below it.
type Element struct {
	Blocks   Blocks `json:"blocks,omitzero"`
	Children Items  `json:"children,omitzero"`
}

// Group is an outline group: items OneNote keeps together at their own indentation.
type Group struct {
	Items Items `json:"items,omitzero"`
}

// Paragraph is a paragraph block.
type Paragraph struct {
	// Style is the paragraph style's id as stored: "p", "h1" to "h6", "PageTitle",
	// "PageDateTime", "Quote", "code", and so on.
	Style string `json:"style,omitzero"`
	// List is the element's list marker, on its first paragraph; nil for none.
	List *List `json:"list,omitzero"`
	// Tags are its note tags.
	Tags []Tag `json:"tags,omitzero"`
	// Runs are its visible runs, in order; hidden runs are left out.
	Runs []Run `json:"runs,omitzero"`
	// Ink is its handwriting, its inks laid out as they read in one drawing; nil for none. Such
	// a paragraph has no runs.
	Ink *Ink `json:"ink,omitzero"`
}

// List is a list marker: a Bullet, the bullet character, or a Number, its format ("decimal",
// "lower-letter", "upper-letter", "lower-roman", "upper-roman", "ordinal", "cardinal-text",
// "ordinal-text", "hex", "decimal-zero", else "nfc-<code>"), with the Prefix and Suffix around
// it.
type List struct {
	Bullet string `json:"bullet,omitzero"`
	Number string `json:"number,omitzero"`
	Prefix string `json:"prefix,omitzero"`
	Suffix string `json:"suffix,omitzero"`
	// Restart is the number the list restarts at; nil when it doesn't.
	Restart *int `json:"restart,omitzero"`
}

// Tag is a note tag.
type Tag struct {
	Label string `json:"label,omitzero"`
	// Shape is the tag's icon, by onenote_parser's NoteTagShape name: "BlueCheckBox",
	// "YellowStar", and so on.
	Shape string `json:"shape,omitzero"`
	// Status is "completed" or "open", always written.
	Status string `json:"status"`
	// CompletedTime is when a completed tag was completed, UTC, to the second; zero for none.
	CompletedTime time.Time `json:"completed_time,omitzero"`
}

// Run is text of one formatting, or an equation. A flag that isn't set, and a colour that is
// automatic, are left out.
//
// An equation is a run of its own, with no text, flags, colours, or link: its Math, "display"
// when it is its paragraph's only content, else "inline", and its LaTeX, bare, without $ or \(
// around it. onenote writes an equation's latex whenever it writes its math, so a Run with Math
// marshals its LaTeX even when it is "".
type Run struct {
	Text          string `json:"text,omitzero"`
	Math          string `json:"math,omitzero"`
	LaTeX         string `json:"latex,omitzero"`
	Bold          bool   `json:"bold,omitzero"`
	Italic        bool   `json:"italic,omitzero"`
	Underline     bool   `json:"underline,omitzero"`
	Strikethrough bool   `json:"strikethrough,omitzero"`
	Superscript   bool   `json:"superscript,omitzero"`
	Subscript     bool   `json:"subscript,omitzero"`
	// Highlight and FontColor are "#rrggbb".
	Highlight string `json:"highlight,omitzero"`
	FontColor string `json:"font_color,omitzero"`
	// Link is the link's target. A link OneNote recognised in typed text targets its own text.
	Link string `json:"link,omitzero"`
}

// runJSON is a Run as its line gives it: its latex there whenever its math is.
type runJSON struct {
	Text          string  `json:"text,omitzero"`
	Math          string  `json:"math,omitzero"`
	LaTeX         *string `json:"latex,omitzero"`
	Bold          bool    `json:"bold,omitzero"`
	Italic        bool    `json:"italic,omitzero"`
	Underline     bool    `json:"underline,omitzero"`
	Strikethrough bool    `json:"strikethrough,omitzero"`
	Superscript   bool    `json:"superscript,omitzero"`
	Subscript     bool    `json:"subscript,omitzero"`
	Highlight     string  `json:"highlight,omitzero"`
	FontColor     string  `json:"font_color,omitzero"`
	Link          string  `json:"link,omitzero"`
}

// MarshalJSONTo writes r as onenote does, its latex whenever it has math.
func (r Run) MarshalJSONTo(enc *jsontext.Encoder) error {
	j := runJSON{
		Text: r.Text, Math: r.Math, Bold: r.Bold, Italic: r.Italic, Underline: r.Underline,
		Strikethrough: r.Strikethrough, Superscript: r.Superscript, Subscript: r.Subscript,
		Highlight: r.Highlight, FontColor: r.FontColor, Link: r.Link,
	}
	if r.Math != "" || r.LaTeX != "" {
		j.LaTeX = &r.LaTeX
	}
	return json.MarshalEncode(enc, j)
}

// UnmarshalJSONFrom reads a run's line.
func (r *Run) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var j runJSON
	if err := json.UnmarshalDecode(dec, &j); err != nil {
		return err
	}
	*r = Run{
		Text: j.Text, Math: j.Math, Bold: j.Bold, Italic: j.Italic, Underline: j.Underline,
		Strikethrough: j.Strikethrough, Superscript: j.Superscript, Subscript: j.Subscript,
		Highlight: j.Highlight, FontColor: j.FontColor, Link: j.Link,
	}
	if j.LaTeX != nil {
		r.LaTeX = *j.LaTeX
	}
	return nil
}

// Table is a table block: its tags and its rows, each a list of cells. The parser doesn't
// expose merged cells, so each cell is its own.
type Table struct {
	Tags []Tag `json:"tags,omitzero"`
	// Rows is written even when empty.
	Rows [][]Cell `json:"rows"`
}

// Cell is a table cell: its items, as an outline's, so lists and tables inside it survive. An
// empty cell is {}, since it holds its place in its row.
type Cell struct {
	Items Items `json:"items,omitzero"`
}

// Image is an image's fields, an image block's, and an ImageContainer's after its offsets.
type Image struct {
	// File is its content's name in the media directory; "" when its data is missing or
	// invalid, its other fields kept.
	File string `json:"file,omitzero"`
	// Size is its content's size in bytes, when its data is there; nil otherwise.
	Size *int64 `json:"size,omitzero"`
	// Extension is its own stored extension, without the dot.
	Extension string `json:"extension,omitzero"`
	// Filename is the name it was inserted from, as stored; never a path to use.
	Filename string `json:"filename,omitzero"`
	AltText  string `json:"alt_text,omitzero"`
	// OCRText is its text as OneNote recognised it in the picture, which the Markdown leaves
	// out.
	OCRText   string `json:"ocr_text,omitzero"`
	Hyperlink string `json:"hyperlink,omitzero"`
	// PrintoutPage is, for an image of a printout, the printed page it shows; nil otherwise.
	PrintoutPage *int `json:"printout_page,omitzero"`
}

// EmbeddedFile is an embedded file's fields, an embedded file block's, and an
// EmbeddedFileContainer's after its offsets.
type EmbeddedFile struct {
	// File is its content's name in the media directory; "" when its data is missing or
	// invalid.
	File string `json:"file,omitzero"`
	// Filename is the name it was attached under, as stored; never a path to use.
	Filename string `json:"filename,omitzero"`
	// Type is "audio" or "video" for a recording; "" for other files.
	Type string `json:"type,omitzero"`
	// Size is its content's size in bytes, when its data is there; nil otherwise.
	Size *int64 `json:"size,omitzero"`
}

// InkBlock is ink in an outline, drawn alone: its ink record, nil for ink with no strokes,
// which onenote writes as a block of kind "ink" with no other field.
type InkBlock struct {
	Ink *Ink `json:",embed"`
}

// Unknown is a container, item, or block of a kind the package doesn't know, kept as its JSON, a
// copy it owns, so a kind schema "1" adds later is neither an error nor lost: it marshals back
// as it came.
type Unknown struct {
	Raw jsontext.Value
}

// Kind returns Raw's "kind" field, "" if it has none.
func (u *Unknown) Kind() string {
	kind, _ := kindOf(u.Raw)
	return kind
}

// Kind returns "title".
func (*Title) Kind() string { return "title" }

// Kind returns "outline".
func (*Outline) Kind() string { return "outline" }

// Kind returns "image".
func (*ImageContainer) Kind() string { return "image" }

// Kind returns "embedded_file".
func (*EmbeddedFileContainer) Kind() string { return "embedded_file" }

// Kind returns "ink".
func (*InkContainer) Kind() string { return "ink" }

// Kind returns "element".
func (*Element) Kind() string { return "element" }

// Kind returns "group".
func (*Group) Kind() string { return "group" }

// Kind returns "paragraph".
func (*Paragraph) Kind() string { return "paragraph" }

// Kind returns "table".
func (*Table) Kind() string { return "table" }

// Kind returns "image".
func (*Image) Kind() string { return "image" }

// Kind returns "embedded_file".
func (*EmbeddedFile) Kind() string { return "embedded_file" }

// Kind returns "ink".
func (*InkBlock) Kind() string { return "ink" }

func (*Title) container()                 {}
func (*Outline) container()               {}
func (*ImageContainer) container()        {}
func (*EmbeddedFileContainer) container() {}
func (*InkContainer) container()          {}
func (*Unknown) container()               {}
func (*Element) item()                    {}
func (*Group) item()                      {}
func (*Unknown) item()                    {}
func (*Paragraph) block()                 {}
func (*Table) block()                     {}
func (*Image) block()                     {}
func (*EmbeddedFile) block()              {}
func (*InkBlock) block()                  {}
func (*Unknown) block()                   {}

// MarshalJSONTo writes c as onenote does: an array of objects, each its "kind" first.
func (c Containers) MarshalJSONTo(enc *jsontext.Encoder) error { return marshalUnion(enc, c) }

// UnmarshalJSONFrom reads an array of containers, each by its "kind".
func (c *Containers) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return unmarshalUnion(dec, (*[]Container)(c), func(kind string, raw jsontext.Value) (Container, error) {
		switch kind {
		case "title":
			return decodeAs[Title](raw)
		case "outline":
			return decodeAs[Outline](raw)
		case "image":
			return decodeAs[ImageContainer](raw)
		case "embedded_file":
			return decodeAs[EmbeddedFileContainer](raw)
		case "ink":
			return decodeAs[InkContainer](raw)
		}
		return &Unknown{Raw: raw.Clone()}, nil
	})
}

// MarshalJSONTo writes i as onenote does: an array of objects, each its "kind" first.
func (i Items) MarshalJSONTo(enc *jsontext.Encoder) error { return marshalUnion(enc, i) }

// UnmarshalJSONFrom reads an array of items, each by its "kind".
func (i *Items) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return unmarshalUnion(dec, (*[]Item)(i), func(kind string, raw jsontext.Value) (Item, error) {
		switch kind {
		case "element":
			return decodeAs[Element](raw)
		case "group":
			return decodeAs[Group](raw)
		}
		return &Unknown{Raw: raw.Clone()}, nil
	})
}

// MarshalJSONTo writes b as onenote does: an array of objects, each its "kind" first.
func (b Blocks) MarshalJSONTo(enc *jsontext.Encoder) error { return marshalUnion(enc, b) }

// UnmarshalJSONFrom reads an array of blocks, each by its "kind".
func (b *Blocks) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return unmarshalUnion(dec, (*[]Block)(b), func(kind string, raw jsontext.Value) (Block, error) {
		switch kind {
		case "paragraph":
			return decodeAs[Paragraph](raw)
		case "table":
			return decodeAs[Table](raw)
		case "image":
			return decodeAs[Image](raw)
		case "embedded_file":
			return decodeAs[EmbeddedFile](raw)
		case "ink":
			return decodeAs[InkBlock](raw)
		}
		return &Unknown{Raw: raw.Clone()}, nil
	})
}

// tagged is a variant as its line gives it: its "kind" first, then its fields, as serde writes
// an internally tagged enum.
type tagged[T any] struct {
	Kind  string `json:"kind"`
	Value *T     `json:",embed"`
}

// decodeAs decodes raw, a variant's object, into a new T, its "kind" among its known members.
func decodeAs[T any](raw jsontext.Value) (*T, error) {
	v := new(T)
	if err := json.Unmarshal(raw, &tagged[T]{Value: v}); err != nil {
		return nil, err
	}
	return v, nil
}

// encodeAs writes v as a variant of kind.
func encodeAs[T any](enc *jsontext.Encoder, kind string, v *T) error {
	return json.MarshalEncode(enc, tagged[T]{Kind: kind, Value: v})
}

// kindOf returns raw's "kind" field, reporting whether raw is an object that has one.
func kindOf(raw jsontext.Value) (string, bool) {
	var head struct {
		Kind *string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.Kind == nil {
		return "", false
	}
	return *head.Kind, true
}

// marshalUnion writes list, each element a variant of its kind, an Unknown as its JSON.
func marshalUnion[T interface{ Kind() string }](enc *jsontext.Encoder, list []T) error {
	if err := enc.WriteToken(jsontext.BeginArray); err != nil {
		return err
	}
	for _, v := range list {
		var err error
		switch v := any(v).(type) {
		case *Title:
			err = encodeAs(enc, v.Kind(), v)
		case *Outline:
			err = encodeAs(enc, v.Kind(), v)
		case *ImageContainer:
			err = encodeAs(enc, v.Kind(), v)
		case *EmbeddedFileContainer:
			err = encodeAs(enc, v.Kind(), v)
		case *InkContainer:
			err = encodeAs(enc, v.Kind(), v)
		case *Element:
			err = encodeAs(enc, v.Kind(), v)
		case *Group:
			err = encodeAs(enc, v.Kind(), v)
		case *Paragraph:
			err = encodeAs(enc, v.Kind(), v)
		case *Table:
			err = encodeAs(enc, v.Kind(), v)
		case *Image:
			err = encodeAs(enc, v.Kind(), v)
		case *EmbeddedFile:
			err = encodeAs(enc, v.Kind(), v)
		case *InkBlock:
			err = encodeAs(enc, v.Kind(), v)
		case *Unknown:
			err = enc.WriteValue(v.Raw)
		default:
			err = fmt.Errorf("onenote: marshalling content: %T", v)
		}
		if err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndArray)
}

// unmarshalUnion reads an array into list, each element by its "kind", through decode; null
// leaves list nil. An element that isn't an object with a "kind" is an error.
func unmarshalUnion[T any](dec *jsontext.Decoder, list *[]T, decode func(kind string, raw jsontext.Value) (T, error)) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	switch tok.Kind() {
	case 'n':
		*list = nil
		return nil
	case '[':
	default:
		return fmt.Errorf("onenote: content: a %v where a list belongs", tok.Kind())
	}
	out := []T{}
	for dec.PeekKind() != ']' {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		kind, ok := kindOf(raw)
		if !ok {
			return errors.New("onenote: content: an element with no kind")
		}
		v, err := decode(kind, raw)
		if err != nil {
			return fmt.Errorf("onenote: content: a %s: %w", kind, err)
		}
		out = append(out, v)
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	*list = out
	return nil
}

// Float is one of onenote's numbers with a fraction, a Rust f32: an offset, or a bound in ink
// units. It marshals as serde_json writes an f32, the shortest digits that read back to the same
// float32, always with a fraction or an exponent, as 1.0 for one and 1e+13 past twelve digits'
// worth, so a decoded line marshals back to onenote's bytes.
type Float float32

// MarshalJSONTo writes f as serde_json writes an f32.
func (f Float) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteValue(jsontext.Value(formatF32(float32(f))))
}

// UnmarshalJSONFrom reads a JSON number into f, rounded to a float32; null leaves f unchanged.
func (f *Float) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	if raw.Kind() == 'n' {
		return nil
	}
	if raw.Kind() != '0' {
		return fmt.Errorf("onenote: a %v where a number belongs", raw.Kind())
	}
	v, err := strconv.ParseFloat(string(raw), 32)
	if err != nil {
		return err
	}
	*f = Float(v)
	return nil
}

// formatF32 formats v as serde_json 1.0.154 does through zmij 1.0.23, the CLI's locked
// versions: the shortest digits that round-trip, in fixed notation when the leading digit's
// decimal exponent is from -6 to 12, with ".0" after a whole number, else as d.ddde±x.
func formatF32(v float32) string {
	s := strconv.FormatFloat(float64(v), 'e', -1, 32)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	mantissa, exponent, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(exponent)
	digits := strings.Replace(mantissa, ".", "", 1)
	n := len(digits)
	switch {
	case exp < -6 || exp > 12:
		out := digits[:1]
		if n > 1 {
			out += "." + digits[1:]
		}
		if exp >= 0 {
			return sign + out + "e+" + strconv.Itoa(exp)
		}
		return sign + out + "e-" + strconv.Itoa(-exp)
	case n-1 <= exp:
		return sign + digits + strings.Repeat("0", exp-(n-1)) + ".0"
	case exp >= 0:
		return sign + digits[:exp+1] + "." + digits[exp+1:]
	default:
		return sign + "0." + strings.Repeat("0", -exp-1) + digits
	}
}
