//go:build linux

package office

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
)

// Schema is the version of office's JSON model the package decodes. A document of any other
// schema is refused with ErrSchema, since a new schema version is a change to the model's shape.
const Schema = "1"

// ErrSchema is the cause of the error a JSON result returns for a document whose schema isn't
// Schema; the error names the schema the document gave.
var ErrSchema = errors.New("unsupported schema")

// Document is office's JSON model of a document, schema "1": one tree for docx, xlsx, and pptx.
// A property the JSON leaves out is the zero value here, which means none, as office's model
// means it. A docx document's sections, notes, and comments are nil for an xlsx or pptx one.
type Document struct {
	// Schema is the model's version, Schema for every Document a conversion returns.
	Schema string `json:"schema"`
	// Format is the source format: "docx", "xlsx", or "pptx".
	Format string `json:"format"`
	// Blocks are the document's blocks in document order: a docx document's body; an xlsx
	// document's sheets; a pptx document's slides.
	Blocks []Block `json:"blocks"`
	// Sections are a docx document's sections in body order, each with the headers and footers
	// its pages show; nil when no section has any. A Media's Section indexes them, and counts
	// them even when this is nil.
	Sections []Section `json:"sections"`
	// Footnotes are the footnotes a docx document's text references, in the order it first
	// references them.
	Footnotes []Note `json:"footnotes"`
	// Endnotes are the endnotes, in the same order.
	Endnotes []Note `json:"endnotes"`
	// Comments are a docx document's comment threads, in the order the document stores them.
	Comments []Comment `json:"comments"`
}

// Section is a docx section's headers and footers, as its pages show them.
type Section struct {
	Headers []HeaderFooter `json:"headers"`
	Footers []HeaderFooter `json:"footers"`
}

// HeaderFooter is a header or footer: the pages it shows on, "default", "first", or "even", and
// its blocks.
type HeaderFooter struct {
	Kind   string  `json:"kind"`
	Blocks []Block `json:"blocks"`
}

// Note is a footnote or endnote: its id, which its references in the text give, and its blocks.
type Note struct {
	ID     int     `json:"id"`
	Blocks []Block `json:"blocks"`
}

// Comment is a docx comment thread: its id, which its reference in the text gives, its author
// ("" for none), the text it is anchored to, whether it is resolved, its blocks, and its replies
// in order.
type Comment struct {
	ID       int            `json:"id"`
	Author   string         `json:"author"`
	Anchor   string         `json:"anchor"`
	Resolved bool           `json:"resolved"`
	Blocks   []Block        `json:"blocks"`
	Replies  []CommentReply `json:"replies"`
}

// CommentReply is a reply in a comment's thread.
type CommentReply struct {
	ID     int     `json:"id"`
	Author string  `json:"author"`
	Blocks []Block `json:"blocks"`
}

// Block is one of a document's blocks, by its kind: *Heading, *Paragraph, *List, *Table, *Chart,
// *Media, *Sheet, or *Slide, and *UnknownBlock for a kind schema "1" adds later, since office's
// model grows by adding kinds. Kind returns the block's "type" in the JSON.
type Block interface {
	Kind() string
	block()
}

// Heading is a heading, level 1 the outermost.
type Heading struct {
	Level   int      `json:"level"`
	Inlines []Inline `json:"inlines"`
}

// Paragraph is a paragraph of text.
type Paragraph struct {
	Inlines []Inline `json:"inlines"`
}

// List is a list, ordered or not. A nested list is a block of the item it belongs to.
type List struct {
	Ordered bool `json:"ordered"`
	// Start is an ordered list's first number, nil when it is 1 or the list is unordered.
	Start *int       `json:"start"`
	Items []ListItem `json:"items"`
}

// ListItem is a list item: its own blocks, usually a paragraph, then any nested list.
type ListItem struct {
	Blocks []Block `json:"blocks"`
}

// Table is a table, a docx or pptx table or a sheet's cells: rows of cells, the first row first.
type Table struct {
	Rows []TableRow `json:"rows"`
}

// TableRow is a table row. A position a merged cell covers has no cell of its own.
type TableRow struct {
	// Row is a sheet row's number, from 1; zero in a docx or pptx table.
	Row   int         `json:"row"`
	Cells []TableCell `json:"cells"`
}

// TableCell is a docx or pptx table's cell and its blocks, or a sheet's cell and its value.
type TableCell struct {
	// Ref is a sheet cell's reference, such as B2; "" in a docx or pptx table.
	Ref string `json:"ref"`
	// ValueType is the type of a sheet cell's value, "" when it has none.
	ValueType ValueType `json:"valueType"`
	// Value is a sheet cell's value, as its ValueType says: a float64 for ValueNumber, a bool for
	// ValueBoolean, and a string for the rest (a date in ISO 8601). nil when the cell has none,
	// as a formula the file caches no result for has none.
	Value any `json:"value"`
	// Formula is a sheet cell's formula as stored, without the leading =.
	Formula string `json:"formula"`
	// SharedFrom is, for a cell of a shared formula other than its anchor, the anchor's reference.
	SharedFrom string `json:"sharedFrom"`
	// SkipBefore is the grid columns a docx row that starts late skips before its first cell.
	SkipBefore int `json:"skipBefore"`
	// ColSpan and RowSpan are the columns and rows a merged cell spans; zero for one.
	ColSpan int `json:"colSpan"`
	RowSpan int `json:"rowSpan"`
	// Blocks are a docx or pptx cell's blocks; nil for a sheet cell.
	Blocks []Block `json:"blocks"`
}

// ValueType is the type of a sheet cell's value.
type ValueType string

// A sheet cell's value types.
const (
	// ValueNumber is a number, as stored, in no number format.
	ValueNumber ValueType = "number"
	// ValueText is text, rich text flattened.
	ValueText ValueType = "text"
	// ValueBoolean is TRUE or FALSE.
	ValueBoolean ValueType = "boolean"
	// ValueError is an error, by its code, such as #DIV/0!.
	ValueError ValueType = "error"
	// ValueDate is a number in a date or time format, in ISO 8601.
	ValueDate ValueType = "date"
)

// Chart is a chart, the data its chart part caches: its type (the first plot's kind, such as
// bar, line, or pie, or an Office 2016 chart's layout, such as boxWhisker), its title ("" for
// none), and its series.
type Chart struct {
	ChartType string        `json:"chartType"`
	Title     string        `json:"title"`
	Series    []ChartSeries `json:"series"`
}

// ChartSeries is a chart series: its name, its categories, and its values. A point the chart
// caches no category or value for is nil in place, so the positions line up.
type ChartSeries struct {
	Name       string     `json:"name"`
	Categories []*string  `json:"categories"`
	Values     []*float64 `json:"values"`
}

// Media is a picture. An embedded one has its file's Name in the media directory (the lowercase
// hex SHA-256 of its bytes and its stored extension), its ContentType, and the package part it
// is stored in; a linked one has its Target instead, which is never fetched, and no file.
type Media struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Alt         string `json:"alt"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	// Section is the index of the docx section that shows the picture; nil outside a docx
	// document and in a comment.
	Section *int `json:"section"`
	// Sheet is the index of the xlsx sheet the picture is on, in the document's blocks.
	Sheet *int `json:"sheet"`
	// Slide is the index of the pptx slide the picture is on, in the document's blocks.
	Slide *int `json:"slide"`
}

// Sheet is an xlsx sheet, in workbook order: its name, whether it is hidden, and its blocks, a
// table of its cells when it has any, then its charts and pictures.
type Sheet struct {
	Name   string  `json:"name"`
	Hidden bool    `json:"hidden"`
	Blocks []Block `json:"blocks"`
}

// Slide is a pptx slide, in presentation order: its number from 1, whether it is hidden, its
// blocks in its shape tree's order, and its notes' blocks, nil when it has no notes.
type Slide struct {
	Number int     `json:"number"`
	Hidden bool    `json:"hidden"`
	Blocks []Block `json:"blocks"`
	Notes  []Block `json:"notes"`
}

// UnknownBlock is a block of a kind the package doesn't know, kept with its type and its JSON
// as office wrote it, so a later addition to the model is never an error.
type UnknownBlock struct {
	Type string
	Raw  jsontext.Value
}

// Inline is a piece of a block's text, by its kind: *Text, *FootnoteRef, *EndnoteRef, or
// *CommentRef, and *UnknownInline for a kind the package doesn't know. Kind returns the inline's
// "type" in the JSON.
type Inline interface {
	Kind() string
	inline()
}

// Text is a run of text, tabs as \t and line breaks as \n.
type Text struct {
	Text string `json:"text"`
}

// FootnoteRef is a footnote's reference, by the note's id.
type FootnoteRef struct {
	ID int `json:"id"`
}

// EndnoteRef is an endnote's reference, by the note's id.
type EndnoteRef struct {
	ID int `json:"id"`
}

// CommentRef is the end of a comment's anchored range, by the comment's id.
type CommentRef struct {
	ID int `json:"id"`
}

// UnknownInline is an inline of a kind the package doesn't know, kept with its type and its
// JSON as office wrote it.
type UnknownInline struct {
	Type string
	Raw  jsontext.Value
}

func (*Heading) Kind() string        { return "heading" }
func (*Paragraph) Kind() string      { return "paragraph" }
func (*List) Kind() string           { return "list" }
func (*Table) Kind() string          { return "table" }
func (*Chart) Kind() string          { return "chart" }
func (*Media) Kind() string          { return "media" }
func (*Sheet) Kind() string          { return "sheet" }
func (*Slide) Kind() string          { return "slide" }
func (b *UnknownBlock) Kind() string { return b.Type }

func (*Heading) block()      {}
func (*Paragraph) block()    {}
func (*List) block()         {}
func (*Table) block()        {}
func (*Chart) block()        {}
func (*Media) block()        {}
func (*Sheet) block()        {}
func (*Slide) block()        {}
func (*UnknownBlock) block() {}

func (*Text) Kind() string            { return "text" }
func (*FootnoteRef) Kind() string     { return "footnote" }
func (*EndnoteRef) Kind() string      { return "endnote" }
func (*CommentRef) Kind() string      { return "comment" }
func (i *UnknownInline) Kind() string { return i.Type }

func (*Text) inline()          {}
func (*FootnoteRef) inline()   {}
func (*EndnoteRef) inline()    {}
func (*CommentRef) inline()    {}
func (*UnknownInline) inline() {}

// newBlock returns a new block of the kind kind names, nil for a kind the package doesn't know.
func newBlock(kind string) Block {
	switch kind {
	case "heading":
		return new(Heading)
	case "paragraph":
		return new(Paragraph)
	case "list":
		return new(List)
	case "table":
		return new(Table)
	case "chart":
		return new(Chart)
	case "media":
		return new(Media)
	case "sheet":
		return new(Sheet)
	case "slide":
		return new(Slide)
	}
	return nil
}

// newInline returns a new inline of the kind kind names, nil for a kind the package doesn't know.
func newInline(kind string) Inline {
	switch kind {
	case "text":
		return new(Text)
	case "footnote":
		return new(FootnoteRef)
	case "endnote":
		return new(EndnoteRef)
	case "comment":
		return new(CommentRef)
	}
	return nil
}

// typed is a block's or inline's discriminator, read alone from its JSON.
type typed struct {
	Type string `json:"type"`
}

// unmarshalers decode a Block and an Inline by their "type": a known kind into its own type, its
// nested blocks and inlines decoded the same way through the options the decoder carries, and
// any other kind into an UnknownBlock or UnknownInline holding its JSON.
var unmarshalers = json.JoinUnmarshalers(
	json.UnmarshalFromFunc(func(dec *jsontext.Decoder, b *Block) error {
		raw, kind, err := readTyped(dec, "a block")
		if err != nil {
			return err
		}
		known := newBlock(kind)
		if known == nil {
			*b = &UnknownBlock{Type: kind, Raw: raw}
			return nil
		}
		if err := json.Unmarshal(raw, known, dec.Options()); err != nil {
			return err
		}
		*b = known
		return nil
	}),
	json.UnmarshalFromFunc(func(dec *jsontext.Decoder, in *Inline) error {
		raw, kind, err := readTyped(dec, "an inline")
		if err != nil {
			return err
		}
		known := newInline(kind)
		if known == nil {
			*in = &UnknownInline{Type: kind, Raw: raw}
			return nil
		}
		if err := json.Unmarshal(raw, known, dec.Options()); err != nil {
			return err
		}
		*in = known
		return nil
	}),
)

// readTyped reads the next value, "a block" or "an inline" as what says, and its "type". A value
// that isn't an object, or has no type, is an error; it carries no prefix of its own, since
// decodeDocument's error wraps it.
func readTyped(dec *jsontext.Decoder, what string) (jsontext.Value, string, error) {
	raw, err := dec.ReadValue()
	if err != nil {
		return nil, "", err
	}
	if raw.Kind() != '{' {
		return nil, "", fmt.Errorf("%s is %s, not an object", what, raw.Kind())
	}
	var t typed
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, "", err
	}
	if t.Type == "" {
		return nil, "", fmt.Errorf("%s has no type", what)
	}
	return raw.Clone(), t.Type, nil
}

// decodeDocument decodes office's JSON model. It reads the schema first, so a document of
// another schema is ErrSchema naming the schema, whatever its shape.
func decodeDocument(data []byte) (*Document, error) {
	var head struct {
		Schema *string `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("office: decoding the JSON model: %w", err)
	}
	switch {
	case head.Schema == nil:
		return nil, fmt.Errorf("office: %w: the JSON model has none, want %q", ErrSchema, Schema)
	case *head.Schema != Schema:
		return nil, fmt.Errorf("office: %w %q, want %q", ErrSchema, *head.Schema, Schema)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc, json.WithUnmarshalers(unmarshalers)); err != nil {
		return nil, fmt.Errorf("office: decoding the JSON model: %w", err)
	}
	return &doc, nil
}
