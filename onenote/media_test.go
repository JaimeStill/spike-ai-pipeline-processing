//go:build linux

package onenote_test

import (
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// mediaPage is a page with a medium in every place one sits: a title's paragraph ink; an
// outline's image, ink, and embedded file blocks, one without a file; a table's cells, one table
// inside another's; an element's children and groups nested in groups; image and embedded file
// containers; and the page's own ink. Beside them are what yields none: an empty cell, an ink
// block without strokes, an ink container, and a block and a container of kinds the package
// doesn't know, whose JSON names a file Media must not look for. Its recordings are an audio
// file and a video file of no known signature, an audio recording whose bytes are an MP4's, and
// one whose bytes are an executable's.
const mediaPage = `{"id":"p","content":[` +
	`{"kind":"title","items":[{"kind":"element","blocks":[{"kind":"paragraph","ink":{"file":"t.svg","strokes":1}}]}]},` +
	`{"kind":"outline","items":[` +
	`{"kind":"element","blocks":[` +
	`{"kind":"paragraph","runs":[{"text":"x"}]},` +
	`{"kind":"image","file":"a.png","size":4,"extension":"png","filename":"a.png"},` +
	`{"kind":"table","rows":[` +
	`[{},{"items":[{"kind":"element","blocks":[{"kind":"image","file":"b.jpg","filename":"b.jpg"}]}]}],` +
	`[{"items":[{"kind":"element","blocks":[{"kind":"table","rows":[[{"items":[{"kind":"element","blocks":[{"kind":"embedded_file","file":"d.docx","filename":"d.docx","size":9}]}]}]]}]}]}]` +
	`]},` +
	`{"kind":"ink","file":"i.svg","strokes":2},` +
	`{"kind":"ink"},` +
	`{"kind":"embedded_file","filename":"lost.bin"},` +
	`{"kind":"chart","file":"chart.png"}` +
	`],"children":[` +
	`{"kind":"group","items":[{"kind":"element","blocks":[{"kind":"embedded_file","file":"rec.wma","filename":"rec.wma","type":"audio"}]}]},` +
	`{"kind":"element","blocks":[{"kind":"paragraph","ink":{"file":"p.svg","strokes":3}}]}` +
	`]},` +
	`{"kind":"group","items":[{"kind":"group","items":[{"kind":"element","blocks":[{"kind":"embedded_file","file":"clip.mp4","type":"audio"}]}]}]}` +
	`]},` +
	`{"kind":"image","file":"c.png","filename":"c.png"},` +
	`{"kind":"embedded_file","file":"v.wmv","filename":"v.wmv","type":"video"},` +
	`{"kind":"ink","strokes":5},` +
	`{"kind":"sticker","file":"s.png"},` +
	`{"kind":"embedded_file","file":"x.wma","filename":"x.wma","type":"audio"},` +
	`{"kind":"image","file":"gone.png"}` +
	`],"ink":{"file":"pg.svg","strokes":4}}`

// asf is an ASF header object's GUID, a .wma's or .wmv's first bytes, which route knows no
// signature for.
const asf = "\x30\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6c"

// mediaFiles are the files mediaPage's media name, as onenote would write them, but gone.png.
var mediaFiles = map[string]string{
	"t.svg":    `<svg xmlns="http://www.w3.org/2000/svg"/>`,
	"p.svg":    `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
	"i.svg":    `<svg xmlns="http://www.w3.org/2000/svg"></svg>`,
	"pg.svg":   `<svg xmlns="http://www.w3.org/2000/svg" width="1"/>`,
	"a.png":    string(route.PNGSignature) + "data",
	"c.png":    string(route.PNGSignature) + "more",
	"b.jpg":    "\xff\xd8\xff\xe0jfif",
	"d.docx":   "PK\x03\x04[Content_Types].xml",
	"rec.wma":  asf + "audio",
	"v.wmv":    asf + "video",
	"clip.mp4": "\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2",
	"x.wma":    "\x7fELF\x02\x01\x01",
	// What the unknown kinds name, so a walk that looked into them would find them.
	"chart.png": string(route.PNGSignature),
	"s.png":     string(route.PNGSignature),
}

// decodePage decodes a page line's fields.
func decodePage(t *testing.T, line string) *onenote.Page {
	t.Helper()
	p := new(onenote.Page)
	if err := jsonv2.Unmarshal([]byte(line), p); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeMedia writes files into a new media directory and returns it.
func writeMedia(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// mediumTruth is what a test expects of a Medium.
type mediumTruth struct {
	kind     onenote.MediumKind
	file     string
	filename string
	typ      string
	where    string
	route    route.Route
	reason   string
}

// mediaTruth is mediaPage's media, in Media's order, routed over mediaFiles.
var mediaTruth = []mediumTruth{
	{onenote.InkMedium, "t.svg", "", "", "title 1, item 1, block 1, paragraph ink", route.VisionConverted, "OneNote ink, an SVG: SVG root element"},
	{onenote.ImageMedium, "a.png", "a.png", "", "outline 2, item 1, block 2", route.Vision, "PNG signature"},
	{onenote.ImageMedium, "b.jpg", "b.jpg", "", "outline 2, item 1, block 3, table cell r1c2, item 1, block 1", route.Vision, "JPEG signature"},
	{onenote.EmbeddedFileMedium, "d.docx", "d.docx", "", "outline 2, item 1, block 3, table cell r2c1, item 1, block 1, table cell r1c1, item 1, block 1", route.Office, "ZIP signature, .docx extension"},
	{onenote.InkMedium, "i.svg", "", "", "outline 2, item 1, block 4", route.VisionConverted, "OneNote ink, an SVG: SVG root element"},
	{onenote.EmbeddedFileMedium, "", "lost.bin", "", "outline 2, item 1, block 6", route.Unrouted, "no content written: its data is missing or invalid"},
	{onenote.EmbeddedFileMedium, "rec.wma", "rec.wma", "audio", "outline 2, item 1, child 1, item 1, block 1", route.Audio, "OneNote audio recording, of no known signature"},
	{onenote.InkMedium, "p.svg", "", "", "outline 2, item 1, child 2, block 1, paragraph ink", route.VisionConverted, "OneNote ink, an SVG: SVG root element"},
	{onenote.EmbeddedFileMedium, "clip.mp4", "", "audio", "outline 2, item 2, item 1, item 1, block 1", route.Video, "MP4 brand"},
	{onenote.ImageMedium, "c.png", "c.png", "", "image container 3", route.Vision, "PNG signature"},
	{onenote.EmbeddedFileMedium, "v.wmv", "v.wmv", "video", "embedded file container 4", route.Video, "OneNote video recording, of no known signature"},
	{onenote.EmbeddedFileMedium, "x.wma", "x.wma", "audio", "embedded file container 7", route.Unrouted, "ELF signature, an executable"},
	{onenote.ImageMedium, "gone.png", "", "", "image container 8", route.Unrouted, "not written: its file isn't in the media directory"},
	{onenote.InkMedium, "pg.svg", "", "", "page ink", route.VisionConverted, "OneNote ink, an SVG: SVG root element"},
}

// collectMedia ranges over page's Media in dir, failing the test on an error.
func collectMedia(t *testing.T, page *onenote.Page, dir string) []onenote.Medium {
	t.Helper()
	var media []onenote.Medium
	for m, err := range page.Media(dir) {
		if err != nil {
			t.Fatalf("%s: %v", m.Where, err)
		}
		media = append(media, m)
	}
	return media
}

// TestMedia: a page's Media yields every image, embedded file, and ink record wherever it sits,
// in the page's order and the page's ink last, each where it was found, with its record's
// fields and its record, its Path in the media directory when its file is there, routed by its
// signature: ink vision-converted as OneNote ink, a recording audio or video by its type only
// when no signature decides, an MP4 typed audio a video and an executable typed audio unrouted,
// and a medium with no file, or a file not written, unrouted with no Path. An ink container, an
// ink block without strokes, an empty cell, and unknown kinds yield none.
func TestMedia(t *testing.T) {
	page := decodePage(t, mediaPage)
	dir := writeMedia(t, mediaFiles)
	media := collectMedia(t, page, dir)
	if len(media) != len(mediaTruth) {
		t.Errorf("%d media, want %d", len(media), len(mediaTruth))
	}
	for i, want := range mediaTruth[:min(len(media), len(mediaTruth))] {
		m := media[i]
		got := mediumTruth{m.Kind, m.File, m.Filename, m.Type, m.Where, m.Route, m.Reason}
		if got != want {
			t.Errorf("medium %d:\n got %+v\nwant %+v", i+1, got, want)
		}
		wantPath := ""
		if _, written := mediaFiles[m.File]; written {
			wantPath = filepath.Join(dir, m.File)
		}
		if m.Path != wantPath {
			t.Errorf("%s: path %q, want %q", m.Where, m.Path, wantPath)
		}
		records := map[onenote.MediumKind]bool{
			onenote.ImageMedium:        m.Image != nil,
			onenote.EmbeddedFileMedium: m.EmbeddedFile != nil,
			onenote.InkMedium:          m.Ink != nil,
		}
		for kind, set := range records {
			if set != (kind == m.Kind) {
				t.Errorf("%s: its %s record set %t, its kind %s", m.Where, kind, set, m.Kind)
			}
		}
	}
	if len(media) < len(mediaTruth) {
		return
	}
	// The records are the page's own: a container's its own fields, a block's the block.
	if c := page.Content[2].(*onenote.ImageContainer); media[9].Image != &c.Image {
		t.Error("an image container's medium doesn't hold the container's image")
	}
	if media[13].Ink != page.Ink {
		t.Error("the page ink's medium doesn't hold the page's ink")
	}
	if m := media[1]; *m.Size != 4 || m.Image.Extension != "png" {
		t.Errorf("a.png: size %d, extension %q, want 4 and png", *m.Size, m.Image.Extension)
	}
	if m := media[3]; *m.Size != 9 {
		t.Errorf("d.docx: size %d, want 9", *m.Size)
	}
	if media[0].Size != nil {
		t.Errorf("ink has a size, %d", *media[0].Size)
	}
}

// TestMediaNoDirectory: without a media directory, every medium is still yielded where it sits,
// with no Path, unrouted: not written for one that names a file, no content for one that
// doesn't.
func TestMediaNoDirectory(t *testing.T) {
	media := collectMedia(t, decodePage(t, mediaPage), "")
	if len(media) != len(mediaTruth) {
		t.Fatalf("%d media, want %d", len(media), len(mediaTruth))
	}
	for i, m := range media {
		want := "not written: no media directory"
		if m.File == "" {
			want = "no content written: its data is missing or invalid"
		}
		if m.Where != mediaTruth[i].where || m.Path != "" || m.Route != route.Unrouted || m.Reason != want {
			t.Errorf("%s: path %q, %s, %q; want %s, no path, unrouted, %q", m.Where, m.Path, m.Route, m.Reason, mediaTruth[i].where, want)
		}
	}
}

// TestMediaNotPlain: a file name that isn't a plain name, which onenote never writes, is never
// joined to the media directory: the medium is unrouted, with no Path.
func TestMediaNotPlain(t *testing.T) {
	dir := writeMedia(t, nil)
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "up.png"), []byte(route.PNGSignature), 0o600); err != nil {
		t.Fatal(err)
	}
	page := decodePage(t, `{"content":[{"kind":"image","file":"../up.png"},{"kind":"image","file":".."}]}`)
	for _, m := range collectMedia(t, page, dir) {
		if m.Path != "" || m.Route != route.Unrouted || m.Reason != "its file name isn't a plain file name" {
			t.Errorf("%s: path %q, %s, %q; want no path, unrouted", m.File, m.Path, m.Route, m.Reason)
		}
	}
}

// TestMediaUnreadable: a medium whose file is there but can't be read is yielded with the
// error, which names its file and where it sits, and the loop may go on to the next medium.
func TestMediaUnreadable(t *testing.T) {
	dir := writeMedia(t, map[string]string{"b.png": string(route.PNGSignature)})
	if err := os.Mkdir(filepath.Join(dir, "a.png"), 0o700); err != nil {
		t.Fatal(err)
	}
	page := decodePage(t, `{"content":[{"kind":"image","file":"a.png"},{"kind":"image","file":"b.png"}]}`)
	var errs []error
	var routes []route.Route
	for m, err := range page.Media(dir) {
		errs, routes = append(errs, err), append(routes, m.Route)
	}
	if len(errs) != 2 {
		t.Fatalf("%d media, want 2", len(errs))
	}
	if errs[0] == nil || !strings.Contains(errs[0].Error(), "a.png at image container 1") || routes[0] != "" {
		t.Errorf("a.png: error %v, route %q; want an error naming it, no route", errs[0], routes[0])
	}
	if errs[1] != nil || routes[1] != route.Vision {
		t.Errorf("b.png: error %v, route %q; want vision", errs[1], routes[1])
	}
}

// TestMediaStop: a loop that stops early stops the walk, wherever it stops, page ink included.
func TestMediaStop(t *testing.T) {
	page := decodePage(t, mediaPage)
	dir := writeMedia(t, mediaFiles)
	for stop := 1; stop <= len(mediaTruth); stop++ {
		n := 0
		for _, err := range page.Media(dir) {
			if err != nil {
				t.Fatal(err)
			}
			n++
			if n == stop {
				break
			}
		}
		if n != stop {
			t.Errorf("stopped at %d, saw %d", stop, n)
		}
	}
}
