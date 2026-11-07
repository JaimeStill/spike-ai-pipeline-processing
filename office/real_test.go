//go:build linux

package office_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// fixtures are the OOXML fixtures in testdata/ooxml, copied from office v0.1.0, as
// testdata/README.md lists them.
var fixtures = []string{
	"media.libreoffice.docx",
	"notes.libreoffice.docx",
	"word-with-attachments.docx",
	"drawings.libreoffice.xlsx",
	"with-drawing.xlsx",
	"slide-objects.libreoffice.pptx",
}

// mediaLink matches a picture's name, the lowercase hex SHA-256 and an extension, as office's
// Markdown links it.
var mediaLink = regexp.MustCompile(`\]\(([0-9a-f]{64}\.[A-Za-z0-9]+)\)`)

// realConverter builds a Converter over the real office the check's mise pins, found on PATH
// through the exec runner.
func realConverter(t *testing.T) *office.Converter {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatalf("office, which mise pins, run the check through mise run check: %v", err)
	}
	c, err := office.NewConverter(t.Context(), office.Config{Office: tl})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestReal runs the real pinned office over the fixtures, as JSON and as Markdown. Each model is
// schema "1" in the fixture's format, with no kind the package doesn't know; together they hold
// every block and inline kind of schema "1", and pictures of every media kind. Each media list
// is the media directory's files, each named by its SHA-256, a picture vision decodes handed on
// as stored and, without converters, an EMF or WMF with no file handed onward, the same
// list for both results, and exactly the names the model's Media and the Markdown's links give,
// a linked picture in neither. The spooled document is gone.
func TestReal(t *testing.T) {
	c := realConverter(t)
	blockKinds := map[string]bool{}
	inlineKinds := map[string]bool{}
	extensions := map[string]bool{}
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			res := convertFixture(t, c, name, office.JSON)
			doc := res.Document
			if doc.Schema != "1" || doc.Format != strings.TrimPrefix(filepath.Ext(name), ".") {
				t.Errorf("schema %q, format %q", doc.Schema, doc.Format)
			}
			modelNames := map[string]string{}
			walkDocument(doc, func(b office.Block) {
				blockKinds[b.Kind()] = true
				switch b := b.(type) {
				case *office.UnknownBlock:
					t.Errorf("unknown block kind %s", b.Type)
				case *office.Media:
					if b.Name != "" {
						modelNames[b.Name] = b.ContentType
					} else if b.Target == "" {
						t.Errorf("media with neither name nor target: %+v", b)
					}
				}
			}, func(in office.Inline) {
				inlineKinds[in.Kind()] = true
				if u, ok := in.(*office.UnknownInline); ok {
					t.Errorf("unknown inline kind %s", u.Type)
				}
			})

			var listed []string
			for _, m := range res.Media {
				listed = append(listed, m.Name)
				extensions[strings.ToLower(filepath.Ext(m.Name))] = true
				stored := filepath.Join(res.MediaDir, m.Name)
				want := stored
				if ext := filepath.Ext(m.Name); ext == ".emf" || ext == ".wmf" {
					want = ""
				}
				if m.Converted || m.File != want {
					t.Errorf("%s: handed on as %q, converted %t; want %q", m.Name, m.File, m.Converted, want)
				}
				data, err := os.ReadFile(stored)
				if err != nil {
					t.Fatal(err)
				}
				if sum := sha256.Sum256(data); hex.EncodeToString(sum[:])+filepath.Ext(m.Name) != m.Name {
					t.Errorf("%s: its SHA-256 is %x", m.Name, sum)
				}
				if want, ok := modelNames[m.Name]; !ok || m.ContentType != want {
					t.Errorf("%s: content type %s, the model's %q (named %t)", m.Name, m.ContentType, want, ok)
				}
			}
			if want := slices.Sorted(maps.Keys(modelNames)); !slices.Equal(listed, want) {
				t.Errorf("media list %q, the model's names %q", listed, want)
			}

			md := convertFixture(t, c, name, office.Markdown)
			if md.Document != nil || md.Markdown == "" {
				t.Fatalf("Markdown result: document %v, %d bytes of Markdown", md.Document, len(md.Markdown))
			}
			var mdListed []string
			for _, m := range md.Media {
				mdListed = append(mdListed, m.Name)
			}
			if !slices.Equal(mdListed, listed) {
				t.Errorf("Markdown result's media list %q, the JSON result's %q", mdListed, listed)
			}
			linked := map[string]bool{}
			for _, m := range mediaLink.FindAllStringSubmatch(md.Markdown, -1) {
				linked[m[1]] = true
			}
			if got := slices.Sorted(maps.Keys(linked)); !slices.Equal(got, listed) {
				t.Errorf("Markdown links %q, media list %q", got, listed)
			}
		})
	}
	for _, kind := range []string{"heading", "paragraph", "list", "table", "chart", "media", "sheet", "slide"} {
		if !blockKinds[kind] {
			t.Errorf("no fixture holds a %s block", kind)
		}
	}
	for _, kind := range []string{"text", "footnote", "endnote", "comment"} {
		if !inlineKinds[kind] {
			t.Errorf("no fixture holds a %s inline", kind)
		}
	}
	for _, ext := range []string{".png", ".jpeg", ".emf", ".wmf"} {
		if !extensions[ext] {
			t.Errorf("no fixture holds a %s picture", ext)
		}
	}
}

// convertFixture converts testdata/ooxml/name as format in a working directory of its own, and
// checks that the conversion left the media directory alone in it.
func convertFixture(t *testing.T, c *office.Converter, name string, format office.Format) *office.Result {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "ooxml", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	dir := t.TempDir()
	res, err := c.Convert(t.Context(), f, office.Request{Dir: dir, Format: format})
	if err != nil {
		t.Fatalf("%v: %v", format, err)
	}
	if res.Warnings != nil {
		t.Errorf("%v: warnings %q", format, res.Warnings)
	}
	if got := entries(t, dir); !slices.Equal(got, []string{filepath.Base(res.MediaDir)}) {
		t.Errorf("%v: working directory holds %q, want the media directory alone", format, got)
	}
	return res
}

// walkDocument calls block for every block of doc, nested ones and those of its sections,
// notes, and comments included, and inline for every inline.
func walkDocument(doc *office.Document, block func(office.Block), inline func(office.Inline)) {
	var walk func([]office.Block)
	inlines := func(ins []office.Inline) {
		for _, in := range ins {
			inline(in)
		}
	}
	walk = func(blocks []office.Block) {
		for _, b := range blocks {
			block(b)
			switch b := b.(type) {
			case *office.Heading:
				inlines(b.Inlines)
			case *office.Paragraph:
				inlines(b.Inlines)
			case *office.List:
				for _, item := range b.Items {
					walk(item.Blocks)
				}
			case *office.Table:
				for _, row := range b.Rows {
					for _, cell := range row.Cells {
						walk(cell.Blocks)
					}
				}
			case *office.Sheet:
				walk(b.Blocks)
			case *office.Slide:
				walk(b.Blocks)
				walk(b.Notes)
			}
		}
	}
	walk(doc.Blocks)
	for _, s := range doc.Sections {
		for _, hf := range slices.Concat(s.Headers, s.Footers) {
			walk(hf.Blocks)
		}
	}
	for _, n := range slices.Concat(doc.Footnotes, doc.Endnotes) {
		walk(n.Blocks)
	}
	for _, cm := range doc.Comments {
		walk(cm.Blocks)
		for _, r := range cm.Replies {
			walk(r.Blocks)
		}
	}
}

// TestRealRefused: the real office refuses a document that isn't an OOXML package with exit code
// 3, and the error carries its reason without the spooled document's path.
func TestRealRefused(t *testing.T) {
	c := realConverter(t)
	dir := t.TempDir()
	_, err := c.Convert(t.Context(), strings.NewReader("plain text, not a package"), office.Request{Dir: dir})
	var exit *office.ExitError
	if !errors.Is(err, office.ErrRefused) || !errors.As(err, &exit) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if exit.Message == "" || strings.Contains(exit.Message, dir) {
		t.Errorf("message %q: want office's reason alone", exit.Message)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("working directory holds %q, want nothing", got)
	}
}
