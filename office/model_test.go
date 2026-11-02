//go:build linux

package office_test

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
)

// convertJSON converts through the stand-in office, which prints model as its JSON model, and
// returns the result's document and the error.
func convertJSON(t *testing.T, c *office.Converter, model string) (*office.Document, error) {
	t.Helper()
	t.Setenv(stdoutEnv, model)
	res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: t.TempDir()})
	if err != nil {
		return nil, err
	}
	return res.Document, nil
}

// TestModelSchema: a model whose schema isn't "1", or that has none, is an error matching
// ErrSchema that names the schema it found, whatever the model's shape; a schema "1" model with
// a block or inline that isn't an object with a type is a decoding error, not ErrSchema.
func TestModelSchema(t *testing.T) {
	c, _ := standinConverter(t)
	for model, want := range map[string]string{
		`{"schema": "2", "format": "docx", "blocks": {"reshaped": true}}`: `"2"`,
		`{"format": "docx", "blocks": []}`:                                "none",
	} {
		_, err := convertJSON(t, c, model)
		if !errors.Is(err, office.ErrSchema) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want ErrSchema naming %s", model, err, want)
		}
	}
	for model, want := range map[string]string{
		`{"schema": "1", "format": "docx", "blocks": [{"level": 1}]}`:                                      "a block has no type",
		`{"schema": "1", "format": "docx", "blocks": [3]}`:                                                 "a block is number, not an object",
		`{"schema": "1", "format": "docx", "blocks": [{"type": "paragraph", "inlines": [{"text": "x"}]}]}`: "an inline has no type",
	} {
		_, err := convertJSON(t, c, model)
		if err == nil || errors.Is(err, office.ErrSchema) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want a decoding error saying %s", model, err, want)
		}
	}
}

// modelEveryKind is a schema "1" model with a block of every kind, an unknown block kind at the
// top and nested, an unknown inline kind, a chart series with nulls in place, a property a later
// office might add, and every optional property left out somewhere.
const modelEveryKind = `{
  "schema": "1",
  "format": "docx",
  "blocks": [
    {"type": "heading", "level": 2, "inlines": [
      {"type": "text", "text": "Title"},
      {"type": "footnote", "id": 2},
      {"type": "endnote", "id": 3},
      {"type": "comment", "id": 4},
      {"type": "equation", "tex": "x^2"}
    ]},
    {"type": "paragraph", "inlines": [{"type": "text", "text": "Body"}], "style": "added later"},
    {"type": "list", "ordered": true, "start": 0, "items": [
      {"blocks": [
        {"type": "paragraph", "inlines": [{"type": "text", "text": "one"}]},
        {"type": "list", "ordered": false, "items": [{"blocks": [{"type": "smartArt", "nodes": [1, 2]}]}]}
      ]}
    ]},
    {"type": "table", "rows": [{"cells": [
      {"skipBefore": 1, "colSpan": 2, "blocks": [{"type": "paragraph", "inlines": []}]},
      {"rowSpan": 3, "blocks": []}
    ]}]},
    {"type": "chart", "chartType": "bar", "series": [
      {"name": "S", "categories": ["a", null, "c"], "values": [1.5, 2, null]},
      {"categories": [], "values": []}
    ]},
    {"type": "media", "name": "ab.png", "contentType": "image/png", "alt": "A", "source": "/word/media/image1.png", "section": 0},
    {"type": "media", "target": "https://example.invalid/x.png", "title": "T"},
    {"type": "sheet", "name": "Sheet1", "hidden": true, "blocks": [
      {"type": "table", "rows": [{"row": 2, "cells": [
        {"ref": "A2", "valueType": "number", "value": 42.5},
        {"ref": "B2", "valueType": "boolean", "value": true},
        {"ref": "C2", "valueType": "date", "value": "2026-11-02", "formula": "TODAY()"},
        {"ref": "D2", "formula": "A2*2"},
        {"ref": "E2", "valueType": "text", "value": "t", "sharedFrom": "E1"}
      ]}]},
      {"type": "media", "name": "cd.emf", "contentType": "image/x-emf", "sheet": 0}
    ]},
    {"type": "slide", "number": 1, "hidden": false, "blocks": [
      {"type": "media", "name": "ef.jpg", "contentType": "image/jpeg", "slide": 7}
    ], "notes": [{"type": "paragraph", "inlines": [{"type": "text", "text": "note"}]}]},
    {"type": "slide", "number": 2, "hidden": true, "blocks": []},
    {"type": "diagram", "nodes": [{"id": 1}]}
  ],
  "sections": [{"headers": [{"kind": "default", "blocks": [{"type": "paragraph", "inlines": [{"type": "text", "text": "H"}]}]}]}],
  "footnotes": [{"id": 2, "blocks": [{"type": "paragraph", "inlines": [{"type": "text", "text": "fn"}]}]}],
  "endnotes": [{"id": 3, "blocks": []}],
  "comments": [{"id": 4, "author": "A", "anchor": "Title", "resolved": true, "blocks": [], "replies": [
    {"id": 5, "blocks": [{"type": "paragraph", "inlines": [{"type": "text", "text": "reply"}]}]}
  ]}]
}`

// TestModel: every schema "1" kind decodes into its own type, an unknown block or inline kind is
// kept with its type and JSON, a left-out property is none, and an array keeps its nulls in
// place.
func TestModel(t *testing.T) {
	c, _ := standinConverter(t)
	doc, err := convertJSON(t, c, modelEveryKind)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "1" || doc.Format != "docx" || len(doc.Blocks) != 11 {
		t.Fatalf("document: schema %q, format %q, %d blocks", doc.Schema, doc.Format, len(doc.Blocks))
	}
	var kinds []string
	for _, b := range doc.Blocks {
		kinds = append(kinds, b.Kind())
	}
	if got, want := strings.Join(kinds, ","), "heading,paragraph,list,table,chart,media,media,sheet,slide,slide,diagram"; got != want {
		t.Errorf("kinds: got %s, want %s", got, want)
	}

	h := doc.Blocks[0].(*office.Heading)
	if h.Level != 2 || len(h.Inlines) != 5 {
		t.Fatalf("heading: %+v", h)
	}
	if in, ok := h.Inlines[0].(*office.Text); !ok || in.Text != "Title" {
		t.Errorf("text inline: %#v", h.Inlines[0])
	}
	if in, ok := h.Inlines[1].(*office.FootnoteRef); !ok || in.ID != 2 {
		t.Errorf("footnote inline: %#v", h.Inlines[1])
	}
	if in, ok := h.Inlines[2].(*office.EndnoteRef); !ok || in.ID != 3 {
		t.Errorf("endnote inline: %#v", h.Inlines[2])
	}
	if in, ok := h.Inlines[3].(*office.CommentRef); !ok || in.ID != 4 {
		t.Errorf("comment inline: %#v", h.Inlines[3])
	}
	if in, ok := h.Inlines[4].(*office.UnknownInline); !ok || in.Kind() != "equation" || !sameJSON(in.Raw, `{"type": "equation", "tex": "x^2"}`) {
		t.Errorf("unknown inline: %#v", h.Inlines[4])
	}

	list := doc.Blocks[2].(*office.List)
	if !list.Ordered || list.Start == nil || *list.Start != 0 || len(list.Items) != 1 || len(list.Items[0].Blocks) != 2 {
		t.Fatalf("list: %+v", list)
	}
	nested := list.Items[0].Blocks[1].(*office.List)
	if nested.Ordered || nested.Start != nil {
		t.Errorf("nested list: ordered %t, start %v", nested.Ordered, nested.Start)
	}
	if u, ok := nested.Items[0].Blocks[0].(*office.UnknownBlock); !ok || u.Type != "smartArt" || !sameJSON(u.Raw, `{"type": "smartArt", "nodes": [1, 2]}`) {
		t.Errorf("nested unknown block: %#v", nested.Items[0].Blocks[0])
	}

	cells := doc.Blocks[3].(*office.Table).Rows[0].Cells
	if cells[0].SkipBefore != 1 || cells[0].ColSpan != 2 || cells[0].RowSpan != 0 || len(cells[0].Blocks) != 1 || cells[1].RowSpan != 3 || cells[0].Ref != "" {
		t.Errorf("docx table cells: %+v", cells)
	}

	chart := doc.Blocks[4].(*office.Chart)
	if chart.ChartType != "bar" || chart.Title != "" || len(chart.Series) != 2 {
		t.Fatalf("chart: %+v", chart)
	}
	s := chart.Series[0]
	if s.Name != "S" || len(s.Categories) != 3 || s.Categories[1] != nil || *s.Categories[2] != "c" ||
		len(s.Values) != 3 || *s.Values[0] != 1.5 || *s.Values[1] != 2 || s.Values[2] != nil {
		t.Errorf("series: %+v", s)
	}
	if chart.Series[1].Name != "" || chart.Series[1].Categories == nil || len(chart.Series[1].Values) != 0 {
		t.Errorf("empty series: %+v", chart.Series[1])
	}

	m := doc.Blocks[5].(*office.Media)
	if m.Name != "ab.png" || m.ContentType != "image/png" || m.Alt != "A" || m.Source != "/word/media/image1.png" ||
		m.Section == nil || *m.Section != 0 || m.Sheet != nil || m.Slide != nil || m.Target != "" || m.Title != "" {
		t.Errorf("embedded media: %+v", m)
	}
	linked := doc.Blocks[6].(*office.Media)
	if linked.Name != "" || linked.Target != "https://example.invalid/x.png" || linked.Title != "T" || linked.Section != nil {
		t.Errorf("linked media: %+v", linked)
	}

	sheet := doc.Blocks[7].(*office.Sheet)
	if sheet.Name != "Sheet1" || !sheet.Hidden || len(sheet.Blocks) != 2 {
		t.Fatalf("sheet: %+v", sheet)
	}
	row := sheet.Blocks[0].(*office.Table).Rows[0]
	if row.Row != 2 || len(row.Cells) != 5 {
		t.Fatalf("sheet row: %+v", row)
	}
	for i, want := range []struct {
		ref       string
		valueType office.ValueType
		value     any
		formula   string
		shared    string
	}{
		{"A2", office.ValueNumber, 42.5, "", ""},
		{"B2", office.ValueBoolean, true, "", ""},
		{"C2", office.ValueDate, "2026-11-02", "TODAY()", ""},
		{"D2", "", nil, "A2*2", ""},
		{"E2", office.ValueText, "t", "", "E1"},
	} {
		got := row.Cells[i]
		if got.Ref != want.ref || got.ValueType != want.valueType || got.Value != want.value || got.Formula != want.formula || got.SharedFrom != want.shared || got.Blocks != nil {
			t.Errorf("cell %s: got %+v", want.ref, got)
		}
	}
	if m := sheet.Blocks[1].(*office.Media); m.Sheet == nil || *m.Sheet != 0 || m.Section != nil {
		t.Errorf("sheet media: %+v", m)
	}

	slide := doc.Blocks[8].(*office.Slide)
	if slide.Number != 1 || slide.Hidden || len(slide.Notes) != 1 {
		t.Errorf("slide: %+v", slide)
	}
	if m := slide.Blocks[0].(*office.Media); m.Slide == nil || *m.Slide != 7 {
		t.Errorf("slide media: %+v", m)
	}
	if hidden := doc.Blocks[9].(*office.Slide); !hidden.Hidden || hidden.Notes != nil || hidden.Blocks == nil {
		t.Errorf("hidden slide: %+v", hidden)
	}
	if u, ok := doc.Blocks[10].(*office.UnknownBlock); !ok || u.Kind() != "diagram" || !sameJSON(u.Raw, `{"type": "diagram", "nodes": [{"id": 1}]}`) {
		t.Errorf("unknown block: %#v", doc.Blocks[10])
	}

	if len(doc.Sections) != 1 || doc.Sections[0].Footers != nil || doc.Sections[0].Headers[0].Kind != "default" || len(doc.Sections[0].Headers[0].Blocks) != 1 {
		t.Errorf("sections: %+v", doc.Sections)
	}
	if len(doc.Footnotes) != 1 || doc.Footnotes[0].ID != 2 || len(doc.Endnotes) != 1 || doc.Endnotes[0].ID != 3 {
		t.Errorf("notes: %+v, %+v", doc.Footnotes, doc.Endnotes)
	}
	if len(doc.Comments) != 1 {
		t.Fatalf("comments: %+v", doc.Comments)
	}
	cm := doc.Comments[0]
	if cm.ID != 4 || cm.Author != "A" || cm.Anchor != "Title" || !cm.Resolved || len(cm.Replies) != 1 || cm.Replies[0].ID != 5 || cm.Replies[0].Author != "" {
		t.Errorf("comment: %+v", cm)
	}
}

// TestModelLeftOut: an xlsx model with only its required properties has no sections, notes, or
// comments.
func TestModelLeftOut(t *testing.T) {
	c, _ := standinConverter(t)
	doc, err := convertJSON(t, c, `{"schema": "1", "format": "xlsx", "blocks": [{"type": "sheet", "name": "S", "hidden": false, "blocks": []}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Sections != nil || doc.Footnotes != nil || doc.Endnotes != nil || doc.Comments != nil {
		t.Errorf("document: %+v", doc)
	}
}

// sameJSON reports whether raw is the JSON want, whitespace aside.
func sameJSON(raw jsontext.Value, want string) bool {
	got, w := raw.Clone(), jsontext.Value(want)
	if got.Compact() != nil || w.Compact() != nil {
		return false
	}
	return string(got) == string(w)
}
