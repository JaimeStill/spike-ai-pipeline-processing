//go:build linux

package onenote_test

import (
	jsonv2 "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
)

// contentKinds counts what content holds, walking it all: each container, item, and block by
// its place and kind, such as "container ink", "item group", and "block table"; "unknown "
// before any of an unknown kind; and "paragraph ink", "run math", "run text", "tag", "list",
// and "cell empty" for what a block holds.
func contentKinds(content onenote.Containers) map[string]int {
	kinds := map[string]int{}
	var items func(onenote.Items)
	blocks := func(list onenote.Blocks) {
		for _, b := range list {
			switch b := b.(type) {
			case *onenote.Unknown:
				kinds["unknown block "+b.Kind()]++
				continue
			case *onenote.Paragraph:
				if b.Ink != nil {
					kinds["paragraph ink"]++
				}
				if b.List != nil {
					kinds["list"]++
				}
				kinds["tag"] += len(b.Tags)
				for _, r := range b.Runs {
					if r.Math != "" {
						kinds["run math"]++
					} else {
						kinds["run text"]++
					}
				}
			case *onenote.Table:
				kinds["tag"] += len(b.Tags)
				for _, row := range b.Rows {
					for _, cell := range row {
						if len(cell.Items) == 0 {
							kinds["cell empty"]++
						}
						items(cell.Items)
					}
				}
			}
			kinds["block "+b.Kind()]++
		}
	}
	items = func(list onenote.Items) {
		for _, it := range list {
			switch it := it.(type) {
			case *onenote.Unknown:
				kinds["unknown item "+it.Kind()]++
				continue
			case *onenote.Element:
				blocks(it.Blocks)
				items(it.Children)
			case *onenote.Group:
				items(it.Items)
			}
			kinds["item "+it.Kind()]++
		}
	}
	for _, c := range content {
		switch c := c.(type) {
		case *onenote.Unknown:
			kinds["unknown container "+c.Kind()]++
			continue
		case *onenote.Title:
			items(c.Items)
		case *onenote.Outline:
			items(c.Items)
		}
		kinds["container "+c.Kind()]++
	}
	return kinds
}

// fullContent is a page's content with every variant of schema "1"'s containers, items, and
// blocks, every field of each set, an empty table cell, an equation with empty LaTeX, an ink
// block without strokes, a paragraph's ink, and a container, an item, and a block of kinds the
// package doesn't know, each in the order onenote writes its fields.
const fullContent = `[` +
	`{"kind":"title","offset_horizontal":0.0,"offset_vertical":-0.5,"items":[{"kind":"element","blocks":[{"kind":"paragraph","style":"PageTitle","runs":[{"text":"T"}]}]}]},` +
	`{"kind":"outline","offset_horizontal":1.0,"offset_vertical":2.4,"items":[` +
	`{"kind":"element","blocks":[` +
	`{"kind":"paragraph","style":"p","list":{"bullet":"•"},"tags":[{"label":"To Do","shape":"BlueCheckBox","status":"completed","completed_time":"2020-10-27T10:50:13Z"},{"status":"open"}],` +
	`"runs":[{"text":"a","bold":true,"italic":true,"underline":true,"strikethrough":true,"superscript":true,"subscript":true,"highlight":"#ffc000","font_color":"#7f7f7f","link":"https://example.com"},{"math":"inline","latex":""},{"math":"display","latex":"a=b"}]},` +
	`{"kind":"paragraph","list":{"number":"decimal","prefix":"(","suffix":")","restart":0}},` +
	`{"kind":"paragraph","ink":{"file":"aa.svg","strokes":3,"bounds":{"x":0.0,"y":1.5,"width":1e+13,"height":1e-7}}},` +
	`{"kind":"table","tags":[{"status":"open"}],"rows":[[{},{"items":[{"kind":"element","blocks":[{"kind":"paragraph","runs":[{"text":"<&>"}]}]}]}]]},` +
	`{"kind":"table","rows":[]},` +
	`{"kind":"image","file":"bb.png","size":0,"extension":"png","filename":"x.png","alt_text":"alt","ocr_text":"ocr","hyperlink":"https://h","printout_page":0},` +
	`{"kind":"embedded_file","file":"cc.mp3","filename":"x.mp3","type":"audio","size":12},` +
	`{"kind":"ink","file":"dd.svg","strokes":1,"bounds":{"x":1363.0,"y":38643.0,"width":16563.0,"height":2017.0}},` +
	`{"kind":"ink"},` +
	`{"kind":"chart","series":[1,2]}` +
	`],"children":[{"kind":"group","items":[{"kind":"element"}]},{"kind":"bookmark","at":"x"}]}]},` +
	`{"kind":"image","offset_horizontal":0.9166665,"offset_vertical":2.0408335,"file":"ee.png","size":27146,"printout_page":1},` +
	`{"kind":"embedded_file","file":"ff.docx","filename":"testing.docx","size":14660},` +
	`{"kind":"ink","offset_horizontal":3.0,"strokes":0},` +
	`{"kind":"ink","strokes":2,"bounds":{"x":-1.0,"y":0.001,"width":123456790000.0,"height":1.25}},` +
	`{"kind":"sticker","name":"cat"}` +
	`]`

// TestContent: content of every kind decodes into its typed value, its fields set, its omitted
// fields none and its zeros kept, an unknown container, item, or block an Unknown holding its
// JSON; and it marshals back to the same bytes.
func TestContent(t *testing.T) {
	var content onenote.Containers
	if err := jsonv2.Unmarshal([]byte(fullContent), &content); err != nil {
		t.Fatal(err)
	}
	got, err := jsonv2.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fullContent {
		t.Errorf("content marshals to\n%s\nwant\n%s", got, fullContent)
	}
	kinds := contentKinds(content)
	for _, kind := range []string{
		"container title", "container outline", "container image", "container embedded_file", "container ink",
		"unknown container sticker", "item element", "item group", "unknown item bookmark",
		"block paragraph", "block table", "block image", "block embedded_file", "block ink", "unknown block chart",
		"paragraph ink", "run math", "run text", "tag", "list", "cell empty",
	} {
		if kinds[kind] == 0 {
			t.Errorf("no %s in %v", kind, kinds)
		}
	}

	f := func(v onenote.Float) *onenote.Float { return &v }
	n := func(v int) *int { return &v }
	n64 := func(v int64) *int64 { return &v }
	if got, want := content[0], (&onenote.Title{
		OffsetHorizontal: f(0), OffsetVertical: f(-0.5),
		Items: onenote.Items{&onenote.Element{Blocks: onenote.Blocks{
			&onenote.Paragraph{Style: "PageTitle", Runs: []onenote.Run{{Text: "T"}}},
		}}},
	}); !reflect.DeepEqual(got, want) {
		t.Errorf("title %#v, want %#v", got, want)
	}
	outline := content[1].(*onenote.Outline)
	element := outline.Items[0].(*onenote.Element)
	completed, err := time.Parse(time.RFC3339, "2020-10-27T10:50:13Z")
	if err != nil {
		t.Fatal(err)
	}
	wantBlocks := onenote.Blocks{
		&onenote.Paragraph{
			Style: "p",
			List:  &onenote.List{Bullet: "•"},
			Tags:  []onenote.Tag{{Label: "To Do", Shape: "BlueCheckBox", Status: "completed", CompletedTime: completed}, {Status: "open"}},
			Runs: []onenote.Run{
				{Text: "a", Bold: true, Italic: true, Underline: true, Strikethrough: true, Superscript: true, Subscript: true, Highlight: "#ffc000", FontColor: "#7f7f7f", Link: "https://example.com"},
				{Math: "inline"},
				{Math: "display", LaTeX: "a=b"},
			},
		},
		&onenote.Paragraph{List: &onenote.List{Number: "decimal", Prefix: "(", Suffix: ")", Restart: n(0)}},
		&onenote.Paragraph{Ink: &onenote.Ink{File: "aa.svg", Strokes: 3, Bounds: &onenote.Bounds{Y: 1.5, Width: 1e13, Height: 1e-7}}},
		&onenote.Table{Tags: []onenote.Tag{{Status: "open"}}, Rows: [][]onenote.Cell{{
			{},
			{Items: onenote.Items{&onenote.Element{Blocks: onenote.Blocks{&onenote.Paragraph{Runs: []onenote.Run{{Text: "<&>"}}}}}}},
		}}},
		&onenote.Table{Rows: [][]onenote.Cell{}},
		&onenote.Image{File: "bb.png", Size: n64(0), Extension: "png", Filename: "x.png", AltText: "alt", OCRText: "ocr", Hyperlink: "https://h", PrintoutPage: n(0)},
		&onenote.EmbeddedFile{File: "cc.mp3", Filename: "x.mp3", Type: "audio", Size: n64(12)},
		&onenote.InkBlock{Ink: &onenote.Ink{File: "dd.svg", Strokes: 1, Bounds: &onenote.Bounds{X: 1363, Y: 38643, Width: 16563, Height: 2017}}},
		&onenote.InkBlock{},
		&onenote.Unknown{Raw: []byte(`{"kind":"chart","series":[1,2]}`)},
	}
	if !reflect.DeepEqual(element.Blocks, wantBlocks) {
		t.Errorf("blocks\n%#v\nwant\n%#v", element.Blocks, wantBlocks)
	}
	wantChildren := onenote.Items{
		&onenote.Group{Items: onenote.Items{&onenote.Element{}}},
		&onenote.Unknown{Raw: []byte(`{"kind":"bookmark","at":"x"}`)},
	}
	if !reflect.DeepEqual(element.Children, wantChildren) {
		t.Errorf("children %#v, want %#v", element.Children, wantChildren)
	}
	wantRest := onenote.Containers{
		&onenote.ImageContainer{
			OffsetHorizontal: f(0.9166665), OffsetVertical: f(2.0408335),
			File: "ee.png", Size: n64(27146), PrintoutPage: n(1),
		},
		&onenote.EmbeddedFileContainer{File: "ff.docx", Filename: "testing.docx", Size: n64(14660)},
		&onenote.InkContainer{OffsetHorizontal: f(3)},
		&onenote.InkContainer{Strokes: 2, Bounds: &onenote.Bounds{X: -1, Y: 0.001, Width: 123456790000, Height: 1.25}},
		&onenote.Unknown{Raw: []byte(`{"kind":"sticker","name":"cat"}`)},
	}
	if !reflect.DeepEqual(content[2:], wantRest) {
		t.Errorf("containers\n%#v\nwant\n%#v", content[2:], wantRest)
	}
}

// TestContentMalformed: a content element that isn't an object with a kind, and content that
// isn't a list, are errors, never an Unknown.
func TestContentMalformed(t *testing.T) {
	for _, bad := range []string{
		`[{"offset_horizontal":1.0}]`,
		`[1]`,
		`{"kind":"title"}`,
		`[{"kind":"outline","items":[{"blocks":[]}]}]`,
		`[{"kind":"outline","items":[{"kind":"element","blocks":["p"]}]}]`,
		`[{"kind":"ink","strokes":1,"bounds":{"x":"1"}}]`,
	} {
		var content onenote.Containers
		if err := jsonv2.Unmarshal([]byte(bad), &content); err == nil {
			t.Errorf("%s: decoded to %#v, want an error", bad, content)
		}
	}
}

// TestFloat: a Float marshals as serde_json writes an f32, fixed from a leading digit's
// exponent of -6 to 12, whole numbers with ".0", and exponents past them with their sign; and
// reads back to the same float32.
func TestFloat(t *testing.T) {
	for _, want := range []string{
		"0.0", "-0.0", "1.0", "-1.0", "12.34", "100.0", "16563.0", "0.9902231", "32.098755",
		"0.001", "0.000001", "1e-7", "1.5e-7", "-2.5e-20", "123456790000.0", "1234568000000.0",
		"1e+13", "1.2345e+15", "3.4028235e+38", "1e-45",
	} {
		var f onenote.Float
		if err := jsonv2.Unmarshal([]byte(want), &f); err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		got, err := jsonv2.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s marshals back to %s", want, got)
		}
	}
	var f onenote.Float
	if err := jsonv2.Unmarshal([]byte(`"1.0"`), &f); err == nil || !strings.Contains(err.Error(), "number") {
		t.Errorf("a string: got %v, want an error", err)
	}
}
