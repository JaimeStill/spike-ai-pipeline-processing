//go:build linux

package onenote

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
)

// Schema is the version of onenote's JSON Lines the package decodes. A read whose notebook line
// gives any other schema is refused with ErrSchema, since a new schema version is a change to
// the lines' shape.
const Schema = "1"

// ErrSchema is the cause of the error a read returns for a notebook line whose schema isn't
// Schema; the error names the schema the line gave.
var ErrSchema = errors.New("unsupported schema")

// Record is one line of onenote's output, by its kind: *Notebook, *SectionGroup, *Section,
// *Page, or *Skipped, and *UnknownRecord for a kind schema "1" adds later. Line returns the
// line's "line" field.
//
// Each record type mirrors its line: a field the line leaves out is the zero value, which means
// none, as onenote leaves out a field with no value and an empty list. Each marshals back,
// through encoding/json/v2, to its line's fields less "line".
type Record interface {
	Line() string
	record()
}

// Notebook is the notebook line, the first of every read.
type Notebook struct {
	// Schema is the lines' version, Schema for every Notebook a read yields.
	Schema string `json:"schema,omitzero"`
	// Input is the input's kind, sniffed from its content: "section", "notebook" (a .onetoc2 or
	// a directory), or "package".
	Input string `json:"input,omitzero"`
	// Name is a section's file stem; a notebook's directory name, for a .onetoc2 input too; a
	// package's file stem.
	Name string `json:"name,omitzero"`
	// ID is a notebook's or package's: its .onetoc2's FileIdentity, a lowercase hyphenated GUID.
	// A section input has none.
	ID string `json:"id,omitzero"`
	// Color is a notebook's or package's colour, "#rrggbb".
	Color string `json:"color,omitzero"`
}

// SectionGroup is a section group line, which comes before the entries the group holds.
type SectionGroup struct {
	// ID is the FileIdentity of the group's own .onetoc2.
	ID string `json:"id,omitzero"`
	// Parent is the id of the group it is in; a group at the notebook's root has none.
	Parent string `json:"parent,omitzero"`
	// Path is the names from the notebook's root down to this group.
	Path []string `json:"path,omitzero"`
	// DisplayName is the group's directory name, which OneNote shows as its name.
	DisplayName string `json:"display_name,omitzero"`
}

// Section is a section line, which comes before its pages' lines.
type Section struct {
	// ID is the section's FileIdentity.
	ID string `json:"id,omitzero"`
	// Parent is the id of the section group it is in; a section at the notebook's root, or a
	// section input, has none.
	Parent string `json:"parent,omitzero"`
	// Path is the names from the notebook's root down to this section.
	Path []string `json:"path,omitzero"`
	// DisplayName is the section's display name.
	DisplayName string `json:"display_name,omitzero"`
	// Color is its colour, "#rrggbb"; none when it has none.
	Color string `json:"color,omitzero"`
}

// Page is a page line, one page at its latest revision, in its section's page series' order.
type Page struct {
	// ID is the page's GUID, lowercase and hyphenated.
	ID string `json:"id,omitzero"`
	// Section is the id of its section.
	Section string `json:"section,omitzero"`
	// Level is its depth in its section's page tree: 1 for a page, 2 for its subpage, and so on.
	Level int `json:"level,omitzero"`
	// Title is its title text.
	Title string `json:"title,omitzero"`
	// CreationTime and ModificationTime are UTC, to the second.
	CreationTime     time.Time `json:"creation_time,omitzero"`
	ModificationTime time.Time `json:"modification_time,omitzero"`
	// Author is the author, as stored.
	Author string `json:"author,omitzero"`
	// Content is its containers, the title area first, then the rest in stored order, as the
	// JSON array onenote writes, a copy the record owns: their outlines, paragraphs, runs,
	// tables, images, embedded files, and ink, as onenote's README gives schema "1".
	Content jsontext.Value `json:"content,omitzero"`
	// Ink is the page's own ink, every ink container's strokes in one drawing; nil for none.
	Ink *Ink `json:"ink,omitzero"`
	// InkText is the page's recognised ink text: lines, each a list of words.
	InkText [][]Word `json:"ink_text,omitzero"`
	// Markdown is the page as Markdown, rendered by onenote from the fields above; "" when
	// nothing on it renders.
	Markdown string `json:"markdown,omitzero"`
}

// Ink is an ink record: a drawing onenote writes as one SVG.
type Ink struct {
	// File is the SVG's name in the media directory: the lowercase hex SHA-256 of its bytes and
	// ".svg".
	File string `json:"file,omitzero"`
	// Strokes is its stroke count.
	Strokes int `json:"strokes,omitzero"`
	// Bounds is the box its strokes' points span before their pens' width is added; nil when
	// its strokes have no points.
	Bounds *Bounds `json:"bounds,omitzero"`
}

// Bounds is a box in ink units of 0.01 mm: a page's ink in the page's coordinates.
type Bounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Word is one recognised word of ink text.
type Word struct {
	// Text is the recogniser's best guess.
	Text string `json:"text,omitzero"`
	// Alternatives are its other guesses, best first.
	Alternatives []string `json:"alternatives,omitzero"`
	// Language is a Windows LCID, as stored: 1031 is German, Germany.
	Language int `json:"language,omitzero"`
}

// Skipped is a skipped line: a section inside a notebook that onenote couldn't read, such as a
// password-protected one, after the notebook's entries in the order the parser reported them.
type Skipped struct {
	// Kind is "section".
	Kind string `json:"kind,omitzero"`
	// Name is its name as the notebook's table of contents gives it, a section's file name.
	Name string `json:"name,omitzero"`
	// Reason is why, in the parser's words.
	Reason string `json:"reason,omitzero"`
}

// UnknownRecord is a line of a kind the package doesn't know, kept with its "line" and its JSON,
// never an error, since onenote's schema grows by adding kinds.
type UnknownRecord struct {
	// Kind is the line's "line" field.
	Kind string
	// Raw is the line's JSON, a copy the record owns.
	Raw jsontext.Value
}

// Line returns "notebook".
func (*Notebook) Line() string { return "notebook" }

// Line returns "section_group".
func (*SectionGroup) Line() string { return "section_group" }

// Line returns "section".
func (*Section) Line() string { return "section" }

// Line returns "page".
func (*Page) Line() string { return "page" }

// Line returns "skipped".
func (*Skipped) Line() string { return "skipped" }

// Line returns the line's kind, as it gave it.
func (r *UnknownRecord) Line() string { return r.Kind }

func (*Notebook) record()      {}
func (*SectionGroup) record()  {}
func (*Section) record()       {}
func (*Page) record()          {}
func (*Skipped) record()       {}
func (*UnknownRecord) record() {}

// lineHead is what decodeLine reads of a line before its kind's type.
type lineHead struct {
	Line   *string `json:"line"`
	Schema *string `json:"schema"`
}

// decodeLine decodes one line, raw, into its record. first says whether it is the read's first
// line, which must be the notebook line, whose schema must be Schema. raw is the decoder's
// buffer, valid only until its next read, so an UnknownRecord keeps a copy, and a Page's
// Content is one, as encoding/json/v2 copies a jsontext.Value it decodes into.
func decodeLine(raw jsontext.Value, first bool) (Record, error) {
	var head lineHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("onenote: decoding a line: %w", err)
	}
	if head.Line == nil {
		return nil, errors.New("onenote: a line with no line field")
	}
	kind := *head.Line
	if first {
		if kind != "notebook" {
			return nil, fmt.Errorf("onenote: the first line is a %s line, want the notebook line", kind)
		}
		switch {
		case head.Schema == nil:
			return nil, fmt.Errorf("onenote: %w: the notebook line has none, want %q", ErrSchema, Schema)
		case *head.Schema != Schema:
			return nil, fmt.Errorf("onenote: %w %q, want %q", ErrSchema, *head.Schema, Schema)
		}
	} else if kind == "notebook" {
		return nil, errors.New("onenote: a second notebook line")
	}
	var rec Record
	switch kind {
	case "notebook":
		rec = new(Notebook)
	case "section_group":
		rec = new(SectionGroup)
	case "section":
		rec = new(Section)
	case "page":
		rec = new(Page)
	case "skipped":
		rec = new(Skipped)
	default:
		return &UnknownRecord{Kind: kind, Raw: raw.Clone()}, nil
	}
	if err := json.Unmarshal(raw, rec); err != nil {
		return nil, fmt.Errorf("onenote: decoding a %s line: %w", kind, err)
	}
	return rec, nil
}
