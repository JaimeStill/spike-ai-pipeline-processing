//go:build linux

package dispatch_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The format packages' fixtures the adapters' tests dispatch.
var (
	mailFixtures    = filepath.Join("..", "email", "testdata", "mail", "fixtures")
	onenoteFixtures = filepath.Join("..", "onenote", "testdata", "onenote", "fixtures", "onenote-rs")
	officeFixtures  = filepath.Join("..", "office", "testdata", "ooxml")
)

// pinned builds the tool spec declares, found on PATH through the exec runner: office, mail,
// and onenote, which the check's mise pins.
func pinned(t *testing.T, spec tool.Spec) *tool.Tool {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatalf("%s, which mise pins, run the check through mise run check: %v", spec.Program, err)
	}
	return tl
}

// officeStage returns an office stage over the pinned office alone, with no LibreOffice or
// magick.
func officeStage(t *testing.T) *dispatch.OfficeStage {
	t.Helper()
	tl := pinned(t, office.OfficeSpec())
	c, err := office.NewConverter(office.Config{Office: tl})
	if err != nil {
		t.Fatal(err)
	}
	s, err := dispatch.NewOfficeStage(dispatch.OfficeConfig{Converter: c, Version: dispatch.Versions(tl)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// emailStage returns an email stage over the pinned mail.
func emailStage(t *testing.T) *dispatch.EmailStage {
	t.Helper()
	tl := pinned(t, email.MailSpec())
	r, err := email.NewReader(email.Config{Mail: tl})
	if err != nil {
		t.Fatal(err)
	}
	s, err := dispatch.NewEmailStage(dispatch.EmailConfig{Reader: r, Version: dispatch.Versions(tl)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// onenoteStage returns a onenote stage over the pinned onenote.
func onenoteStage(t *testing.T) *dispatch.OneNoteStage {
	t.Helper()
	tl := pinned(t, onenote.OneNoteSpec())
	r, err := onenote.NewReader(onenote.Config{OneNote: tl})
	if err != nil {
		t.Fatal(err)
	}
	s, err := dispatch.NewOneNoteStage(dispatch.OneNoteConfig{Reader: r, Version: dispatch.Versions(tl)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// toollessPicture returns a picture stage over a Converter given no tool, which has no processor
// for any picture.
func toollessPicture(t *testing.T) *dispatch.PictureStage {
	t.Helper()
	c, err := picture.NewConverter(picture.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := dispatch.NewPictureStage(dispatch.PictureConfig{Converter: c, Version: "none"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newDispatcher returns a dispatcher over stages and index, in a working directory of its own.
func newDispatcher(t *testing.T, stages dispatch.Stages, index dispatch.Index) *dispatch.Dispatcher {
	t.Helper()
	d, err := dispatch.New(dispatch.Config{Stages: stages, Index: index, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// recordThenMarkdown fails the test unless ev's record named name comes right before its
// Markdown of the same name, the order the email and onenote stages yield a message or a page in.
func recordThenMarkdown(t *testing.T, ev dispatch.Event, name string) {
	t.Helper()
	i := slices.IndexFunc(ev.Outputs, func(o dispatch.Output) bool { return o.Name == name })
	if i < 0 || i+1 >= len(ev.Outputs) || ev.Outputs[i].Kind != dispatch.RecordOutput ||
		ev.Outputs[i+1].Kind != dispatch.MarkdownOutput || ev.Outputs[i+1].Name != name {
		t.Errorf("%s: %q's outputs aren't its record, then its Markdown", ev.Part.Name, name)
	}
}

// TestAdapterIdentities: each adapter names itself and its tools' version, refuses a missing
// reader or converter and an empty version, and hashes exactly the options that change what it
// prepares; the vision stage is versioned by this package.
func TestAdapterIdentities(t *testing.T) {
	prep, err := document.NewPreparer(&pages{n: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := func(req document.ImagesRequest) dispatch.Identity {
		t.Helper()
		s, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{Preparer: prep, Version: "gs 10.5", Request: req})
		if err != nil {
			t.Fatal(err)
		}
		return s.Identity()
	}
	base := doc(document.ImagesRequest{})
	if base.Name != "document" || base.Version != "gs 10.5" || len(base.Config) != 64 {
		t.Errorf("document identity %v", base)
	}
	if doc(document.ImagesRequest{DPI: 150, Concurrency: 4, Dir: "elsewhere"}) != base {
		t.Error("the default DPI, a concurrency, or a Dir changed the document identity")
	}
	for name, req := range map[string]document.ImagesRequest{
		"dpi":         {DPI: 300},
		"corrections": {Corrections: []picture.Correction{picture.Deskew}},
		"text":        {Text: true},
		"skip text":   {SkipText: true},
		"region":      {Region: &document.Region{Top: 0.9, Bottom: 1, Right: 1}},
	} {
		if doc(req).Config == base.Config {
			t.Errorf("%s left the document identity's config unchanged", name)
		}
	}

	if _, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{Version: "gs 10.5"}); err == nil {
		t.Error("a document stage with no preparer was built")
	}
	if _, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{Preparer: prep}); err == nil {
		t.Error("a document stage with no version was built")
	}
	if _, err := dispatch.NewOfficeStage(dispatch.OfficeConfig{Version: "x"}); err == nil {
		t.Error("an office stage with no converter was built")
	}
	if _, err := dispatch.NewPictureStage(dispatch.PictureConfig{Version: "x"}); err == nil {
		t.Error("a picture stage with no converter was built")
	}
	if _, err := dispatch.NewEmailStage(dispatch.EmailConfig{Version: "x"}); err == nil {
		t.Error("an email stage with no reader was built")
	}
	if _, err := dispatch.NewOneNoteStage(dispatch.OneNoteConfig{Version: "x"}); err == nil {
		t.Error("a onenote stage with no reader was built")
	}

	officeTool := pinned(t, office.OfficeSpec())
	if got, want := dispatch.Versions(officeTool, nil), "spike-ai-pipeline-office "+officeTool.Version().String(); got != want {
		t.Errorf("Versions %q, want %q", got, want)
	}
	o := officeStage(t).Identity()
	if o.Name != "office" || o.Version != dispatch.Versions(officeTool) {
		t.Errorf("office identity %v", o)
	}
	c, err := office.NewConverter(office.Config{Office: officeTool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch.NewOfficeStage(dispatch.OfficeConfig{Converter: c}); err == nil {
		t.Error("an office stage with no version was built")
	}
	scaled, err := dispatch.NewOfficeStage(dispatch.OfficeConfig{Converter: c, Version: o.Version, MetafileScale: 5})
	if err != nil {
		t.Fatal(err)
	}
	if scaled.Identity().Config == o.Config {
		t.Error("a metafile scale left the office identity's config unchanged")
	}

	p := toollessPicture(t).Identity()
	if p.Name != "picture" || p.Version != "none" {
		t.Errorf("picture identity %v", p)
	}
	for _, id := range []dispatch.Identity{emailStage(t).Identity(), onenoteStage(t).Identity()} {
		if id.Config == "" || !strings.HasPrefix(id.Version, "spike-ai-pipeline-") {
			t.Errorf("%s identity %v: want its tool's name and version", id.Name, id)
		}
	}
	v := dispatch.NewVisionStage().Identity()
	if v.Name != "vision" || v.Version != "1" || v != dispatch.NewVisionStage().Identity() {
		t.Errorf("vision identity %v", v)
	}
}

// pages is a document.Renderer and TextExtractor over a PDF of n pages: page N renders as a
// 1x1 PNG and reads "page N" as its text, but for the page empty, which has no text layer, and
// the page fail, which fails to render.
type pages struct {
	n, empty, fail int
}

func (r *pages) Count(context.Context, string) (int, error) { return r.n, nil }

func (r *pages) Render(_ context.Context, _ string, page, dpi int, w io.Writer) error {
	if err := document.CheckRender(page, dpi); err != nil {
		return err
	}
	if page == r.fail {
		return errors.New("told to fail")
	}
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.Gray{Y: uint8(page)})
	return png.Encode(w, img)
}

func (r *pages) Text(_ context.Context, _ string, page int) (string, error) {
	if page == r.empty {
		return "  \n", nil
	}
	return fmt.Sprintf("page %d", page), nil
}

// documentStage returns a document stage over render, reading the pages' text.
func documentStage(t *testing.T, render document.Renderer) *dispatch.DocumentStage {
	t.Helper()
	prep, err := document.NewPreparer(render, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{
		Preparer: prep, Version: "pages 1", Request: document.ImagesRequest{Text: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestDocumentStage: a PDF's pages, over a renderer of its own, are a text output and an image
// output each, in page order, named by the page and carrying its number and the count, the PNG
// a file in the part's directory; a page with no text layer has no text output; and a page that
// fails fails the part once the others are prepared.
func TestDocumentStage(t *testing.T) {
	pdf := input(t, "three.pdf", "%PDF-1.7\n%%EOF\n")
	d := newDispatcher(t, dispatch.Stages{route.Document: documentStage(t, &pages{n: 3, empty: 2})}, dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, pdf, func(ev *dispatch.Event) {
		for _, o := range ev.Outputs {
			if o.Kind != dispatch.ImageOutput {
				continue
			}
			b, err := os.ReadFile(o.Path)
			if err != nil || !bytes.HasPrefix(b, []byte(route.PNGSignature)) {
				t.Errorf("%s: %v, %d bytes", o.Name, err, len(b))
			}
		}
	})
	wantSteps(t, events, []step{{"three.pdf", 0, dispatch.Prepared}})
	var got []string
	for _, o := range events[0].Outputs {
		got = append(got, fmt.Sprintf("%s %s %s/%s %q", o.Kind, o.Name, o.Meta["page"], o.Meta["count"], o.Value))
	}
	want := []string{
		`text page 1 1/3 "page 1"`, `image page 1 1/3 ""`,
		`image page 2 2/3 ""`,
		`text page 3 3/3 "page 3"`, `image page 3 3/3 ""`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("outputs:\n got %q\nwant %q", got, want)
	}

	d = newDispatcher(t, dispatch.Stages{route.Document: documentStage(t, &pages{n: 3, fail: 2})}, dispatch.NewMemoryIndex())
	ev := dispatchAll(t, d, pdf, nil)[0]
	if ev.Outcome != dispatch.Failed || !strings.Contains(ev.Reason, "1 of 3 pages failed") || len(ev.Outputs) != 4 {
		t.Errorf("a failing page: %s, %q, %d outputs", ev.Outcome, ev.Reason, len(ev.Outputs))
	}
}

// TestPictureNoProcessor: a WebP, a HEIC, and an AVIF have no processor yet, nor a TIFF or an EMF
// for a Converter given no tool; each reason says why.
func TestPictureNoProcessor(t *testing.T) {
	d := newDispatcher(t, dispatch.Stages{route.VisionConverted: toollessPicture(t)}, dispatch.NewMemoryIndex())
	for name, c := range map[string]struct{ content, reason string }{
		"x.webp": {"RIFF\x10\x00\x00\x00WEBPVP8 ", "picture stage: no processor yet for a WebP picture"},
		"x.heic": {"\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic", "picture stage: no processor yet for a HEIC picture"},
		"x.avif": {"\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avif", "picture stage: no processor yet for an AVIF picture"},
		"x.tif":  {"II*\x00\x08\x00\x00\x00", "no magick converter"},
		"x.emf":  {"\x01\x00\x00\x00" + strings.Repeat("\x00", 36) + " EMF" + strings.Repeat("\x00", 40), "no LibreOffice converter"},
	} {
		ev := dispatchAll(t, d, input(t, name, c.content), nil)[0]
		if ev.Part.Route != route.VisionConverted || ev.Outcome != dispatch.NoProcessor || !strings.Contains(ev.Reason, c.reason) {
			t.Errorf("%s: %s %s, %q; want no processor, %q", name, ev.Part.Route, ev.Outcome, ev.Reason, c.reason)
		}
	}
}

// zipOf returns a ZIP of entries, each name and content in order, stored uncompressed with its
// sizes in its local header, as an ODF package's mimetype is.
func zipOf(t *testing.T, entries ...string) string {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for i := 0; i < len(entries); i += 2 {
		content := entries[i+1]
		w, err := zw.CreateRaw(&zip.FileHeader{
			Name: entries[i], Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte(content)),
			CompressedSize64: uint64(len(content)), UncompressedSize64: uint64(len(content)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestOfficeNoProcessor: an RTF, a WordPerfect document, an ODF package, and an Excel binary
// workbook have no processor yet, nor a legacy document for a Converter with no LibreOffice;
// each reason names the kind.
func TestOfficeNoProcessor(t *testing.T) {
	d := newDispatcher(t, dispatch.Stages{route.Office: officeStage(t)}, dispatch.NewMemoryIndex())
	for name, c := range map[string]struct{ content, reason string }{
		"x.rtf":  {`{\rtf1\ansi hello}`, "office stage: no processor yet for an RTF document"},
		"x.wpd":  {"\xffWPC\x10\x00\x00\x00\x01\x0a", "office stage: no processor yet for a WordPerfect document"},
		"x.odt":  {zipOf(t, "mimetype", "application/vnd.oasis.opendocument.text", "content.xml", "<x/>"), "office stage: no processor yet for an ODF package"},
		"x.xlsb": {zipOf(t, "[Content_Types].xml", "<x/>"), "office stage: no processor yet for an Excel binary workbook"},
		"x.doc":  {"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1" + strings.Repeat("\x00", 504), "no LibreOffice converter"},
	} {
		ev := dispatchAll(t, d, input(t, name, c.content), nil)[0]
		if ev.Part.Route != route.Office || ev.Outcome != dispatch.NoProcessor || !strings.Contains(ev.Reason, c.reason) {
			t.Errorf("%s: %s %s, %q; want no processor, %q", name, ev.Part.Route, ev.Outcome, ev.Reason, c.reason)
		}
	}
}

// TestOfficeStage runs the pinned office alone over a document holding PNGs and an EMF: its
// Markdown is the document's output, and each medium office wrote is a part of its own, found
// at its office name, the PNGs prepared by the vision stage, the EMF, with no converter, routed
// to the picture stage, which has none either. Every part's event comes before the document's.
func TestOfficeStage(t *testing.T) {
	d := newDispatcher(t, dispatch.Stages{
		route.Office: officeStage(t), route.Vision: dispatch.NewVisionStage(), route.VisionConverted: toollessPicture(t),
	}, dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, filepath.Join(officeFixtures, "media.libreoffice.docx"), nil)
	const (
		red   = "0fcca33bb0baf1a0669bd2fcdcaeb34e33d27196f7ffb5294b27aea8a9e46446"
		blue  = "10a86bd779a80e60e5d3314ec1edeab643cb454a4c7dfee671132090878d7aea"
		emf   = "670be6349ed84b27d6ca43d66ba6e9454c632f68de8d9b8fea78a480e90afb31"
		green = "df026100fd6fe38e22923c5152a33ea6f6879e38073fb47f022ed9d90dec65a8"
	)
	wantSteps(t, events, []step{
		{red + ".png", 1, dispatch.Prepared},
		{blue + ".png", 1, dispatch.Prepared},
		{emf + ".emf", 1, dispatch.NoProcessor},
		{green + ".png", 1, dispatch.Prepared},
		{"media.libreoffice.docx", 0, dispatch.Prepared},
	})
	if got, want := wheres(events), []string{"media " + red + ".png", "media " + blue + ".png", "media " + emf + ".emf", "media " + green + ".png"}; !slices.Equal(got, want) {
		t.Errorf("wheres:\n got %q\nwant %q", got, want)
	}
	if ev := byName(t, events, red+".png"); ev.Part.Route != route.Vision || ev.Outputs[0].Kind != dispatch.ImageOutput || ev.Outputs[0].MediaType != "image/png" {
		t.Errorf("red: %s, %+v", ev.Part.Route, ev.Outputs)
	}
	if ev := byName(t, events, emf+".emf"); ev.Part.Route != route.VisionConverted || ev.Part.MIME != "image/x-emf" {
		t.Errorf("emf: %s, %q", ev.Part.Route, ev.Part.MIME)
	}
	md := outputNamed(t, events[len(events)-1], dispatch.MarkdownOutput, "document")
	if !bytes.HasPrefix(md.Value, []byte("# Media")) || !bytes.Contains(md.Value, []byte(red+".png")) {
		t.Errorf("markdown: %.80q", md.Value)
	}
}

// TestEmailStage dispatches a .msg whose picture a .pst holds too, then the .pst, sharing one
// index. The .msg's message is a record and Markdown, its picture a part prepared by the vision
// stage; the .pst's store, folders, and messages are records, a message's Meta naming its folder's
// path, and its picture, the same content, is seen; its text and CSV attachments, which no stage
// takes, are no processor yet. Every part is found where its message says.
func TestEmailStage(t *testing.T) {
	index := dispatch.NewMemoryIndex()
	d := newDispatcher(t, dispatch.Stages{route.Nested: emailStage(t), route.Vision: dispatch.NewVisionStage()}, index)
	const (
		moth = "moth.png"
		csv  = "отчёт.csv"
		note = "note-g.txt"
	)
	events := dispatchAll(t, d, filepath.Join(mailFixtures, "generated", "msg", "03-html-inline-image.msg"), nil)
	wantSteps(t, events, []step{{moth, 1, dispatch.Prepared}, {"03-html-inline-image.msg", 0, dispatch.Prepared}})
	if got := wheres(events); !slices.Equal(got, []string{"message, attachment 1"}) {
		t.Errorf("wheres %q", got)
	}
	msg := events[1]
	rec := outputNamed(t, msg, dispatch.RecordOutput, "message")
	body := outputNamed(t, msg, dispatch.MarkdownOutput, "message")
	if rec.Meta["subject"] == "" || body.Meta["subject"] != rec.Meta["subject"] || len(body.Value) == 0 {
		t.Errorf("message: %v, %v, %d bytes of Markdown", rec.Meta, body.Meta, len(body.Value))
	}
	recordThenMarkdown(t, msg, "message")
	outputNamed(t, msg, dispatch.RecordOutput, "store")

	events = dispatchAll(t, d, filepath.Join(mailFixtures, "generated", "pst", "mailbox.pst"), nil)
	wantSteps(t, events, []step{
		{moth, 1, dispatch.Seen},
		{csv, 1, dispatch.NoProcessor},
		{note, 1, dispatch.NoProcessor},
		{"mailbox.pst", 0, dispatch.Prepared},
	})
	if got, want := wheres(events), []string{
		"message 2097220, attachment 1", "message 2097252, attachment 1", "message 2097380, attachment 1",
	}; !slices.Equal(got, want) {
		t.Errorf("wheres:\n got %q\nwant %q", got, want)
	}
	if ev := byName(t, events, moth); ev.Recorded != dispatch.Prepared || ev.Stage.Name != "vision" {
		t.Errorf("moth.png: recorded %s by %v", ev.Recorded, ev.Stage)
	}
	pst := events[len(events)-1]
	if m := outputNamed(t, pst, dispatch.RecordOutput, "message 2097220").Meta; m["folder"] != "Top of Personal Folders/Inbox/Projects/2026" || m["id"] != "2097220" {
		t.Errorf("message 2097220's meta %v", m)
	}
	if m := outputNamed(t, pst, dispatch.RecordOutput, "folder 32962").Meta; m["path"] != "Top of Personal Folders/Inbox/Projects/2026" {
		t.Errorf("folder 32962's meta %v", m)
	}
	recordThenMarkdown(t, pst, "message 2097156")
	if len(pst.Children) != 3 {
		t.Errorf("the .pst's children %+v", pst.Children)
	}
}

// TestEmailResume: an input's After reaches the email stage, which reads the .pst from the
// message after it: the resumed store's event holds the whole read's store and folder records and
// the records of the messages after it, and finds the attachments of those messages alone. The
// resumed store's own entry isn't recorded, while the parts found in it are; a whole dispatch of
// it on the same index then prepares it, and a resumed one after that sees it. A .msg given an
// After fails, mail refusing --after with a single message.
func TestEmailResume(t *testing.T) {
	stage := emailStage(t)
	stages := dispatch.Stages{route.Nested: stage, route.Vision: dispatch.NewVisionStage()}
	path := filepath.Join(mailFixtures, "generated", "pst", "mailbox.pst")
	whole := dispatchAll(t, newDispatcher(t, stages, dispatch.NewMemoryIndex()), path, nil)
	wholeStore := whole[len(whole)-1]
	var ids []string
	for _, o := range wholeStore.Outputs {
		if o.Kind == dispatch.RecordOutput && strings.HasPrefix(o.Name, "message ") {
			ids = append(ids, o.Meta["id"])
		}
	}
	k := ids[len(ids)/2]
	after, err := strconv.ParseUint(k, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// want are the whole read's outputs less the messages up to k's, and wantWheres the whole
	// read's parts found in the messages after k's.
	var want []string
	past := false
	for _, o := range wholeStore.Outputs {
		switch {
		case o.Name == "message "+k || strings.HasPrefix(o.Name, "message "+k+", "):
			past = true
		case !strings.HasPrefix(o.Name, "message ") || past:
			want = append(want, string(o.Kind)+" "+o.Name)
		}
	}
	var wantWheres []string
	for _, w := range wheres(whole) {
		id, _, _ := strings.Cut(strings.TrimPrefix(w, "message "), ",")
		if slices.Index(ids, id) > slices.Index(ids, k) {
			wantWheres = append(wantWheres, w)
		}
	}

	if len(want) >= len(wholeStore.Outputs) || len(wantWheres) == 0 || len(wantWheres) == len(wheres(whole)) {
		t.Fatalf("after %s: %d of %d outputs and %d of %d parts left; want some of each passed over, and a part left", k,
			len(want), len(wholeStore.Outputs), len(wantWheres), len(wheres(whole)))
	}

	index := dispatch.NewMemoryIndex()
	d := newDispatcher(t, stages, index)
	var resumed []dispatch.Event
	for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: path, After: after}) {
		if err != nil {
			t.Fatal(err)
		}
		resumed = append(resumed, ev)
	}
	store := resumed[len(resumed)-1]
	if store.Outcome != dispatch.Prepared || store.Part.After != after {
		t.Fatalf("the resumed store: %s, after %d", store.Outcome, store.Part.After)
	}
	var got []string
	for _, o := range store.Outputs {
		got = append(got, string(o.Kind)+" "+o.Name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("after %s, outputs:\n got %q\nwant %q", k, got, want)
	}
	if got := wheres(resumed); !slices.Equal(got, wantWheres) {
		t.Errorf("after %s, parts found %q, want %q", k, got, wantWheres)
	}
	for _, ev := range resumed[:len(resumed)-1] {
		if ev.Part.After != 0 {
			t.Errorf("%s, found below the input, carries after %d", ev.Part.Name, ev.Part.After)
		}
		if _, ok, err := index.Flags(t.Context(), ev.Part.SHA256); err != nil || !ok {
			t.Errorf("%s's flags not recorded: %v", ev.Part.Name, err)
		}
	}
	key := dispatch.Key{SHA256: store.Part.SHA256, Stage: stage.Identity()}
	if _, ok, err := index.Lookup(t.Context(), key); err != nil || ok {
		t.Errorf("the resumed store recorded: %t, %v", ok, err)
	}
	if again := dispatchAll(t, d, path, nil); again[len(again)-1].Outcome != dispatch.Prepared {
		t.Errorf("a whole dispatch after the resumed one: %s, want prepared", again[len(again)-1].Outcome)
	}
	for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: path, After: after}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Part.Depth == 0 && ev.Outcome != dispatch.Seen {
			t.Errorf("a resumed dispatch of a recorded store: %s, want seen", ev.Outcome)
		}
	}

	msg := filepath.Join(mailFixtures, "generated", "msg", "01-plain.msg")
	for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: msg, After: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Outcome != dispatch.Failed || !errors.Is(ev.Err, email.ErrUsage) {
			t.Errorf("a .msg after 1: %s, %v; want failed, mail's usage error", ev.Outcome, ev.Err)
		}
	}
}

// TestEmailEmbedded: an embedded message whose content is no file is prepared inline, its record
// and Markdown named by its place and naming the message it is embedded in, while a file beside
// it, a PDF here, which no stage takes in this test, is found.
func TestEmailEmbedded(t *testing.T) {
	d := newDispatcher(t, dispatch.Stages{route.Nested: emailStage(t)}, dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, filepath.Join(mailFixtures, "msg", "tika", "testMSG_att_msg.msg"), nil)
	wantSteps(t, events, []step{
		{"smbprn.00009008.KdcPjl.pdf", 1, dispatch.NoProcessor},
		{"testMSG_att_msg.msg", 0, dispatch.Prepared},
	})
	if got := wheres(events); !slices.Equal(got, []string{"message, attachment 2"}) {
		t.Errorf("wheres %q", got)
	}
	if ev := events[0]; ev.Part.Route != route.Document {
		t.Errorf("the PDF's route %s", ev.Part.Route)
	}
	inline := outputNamed(t, events[1], dispatch.RecordOutput, "message, attachment 1")
	if inline.Meta["embedded_in"] != "message" || inline.Meta["subject"] != "Test Attachment" {
		t.Errorf("the embedded message's meta %v", inline.Meta)
	}
	outputNamed(t, events[1], dispatch.MarkdownOutput, "message, attachment 1")

	events = dispatchAll(t, d, filepath.Join(mailFixtures, "generated", "eml", "08-forward-attached-message.eml"), nil)
	wantSteps(t, events, []step{{"note-g.txt", 1, dispatch.NoProcessor}, {"08-forward-attached-message.eml", 0, dispatch.Prepared}})
	if m := outputNamed(t, events[1], dispatch.RecordOutput, "message, attachment 2").Meta; m["subject"] != "Notes on the engine (draft)" {
		t.Errorf("the forwarded message's meta %v", m)
	}
}

// TestOneNoteStage dispatches a section with an image and ink, then a section holding the same
// image, sharing one index. Each page is a record, then its Markdown, beside the section and
// notebook records; the image, found on its page, is prepared by the vision stage, then seen; the
// ink, an SVG, goes to the picture stage, which has no processor without magick.
func TestOneNoteStage(t *testing.T) {
	index := dispatch.NewMemoryIndex()
	d := newDispatcher(t, dispatch.Stages{
		route.OneNote: onenoteStage(t), route.Vision: dispatch.NewVisionStage(), route.VisionConverted: toollessPicture(t),
	}, index)
	const (
		page  = "page 5eaf76b1-8dde-8745-a8a7-1a9865cc478a"
		jpg   = "d6d4898c203cbff35fe92e844bbf404064293314a71d1e6b24e907334e5bdff9.jpg"
		inked = "95c93a747acbbbeeb6d345faf0d17e86669bc7cdd0854155b588c58a7556d194.svg"
	)
	events := dispatchAll(t, d, filepath.Join(onenoteFixtures, "handwriting_recognition.one"), nil)
	var names []string
	for _, ev := range events {
		names = append(names, fmt.Sprintf("%s@%d:%s %s", filepath.Ext(ev.Part.Name), ev.Part.Depth, ev.Outcome, ev.Part.Route))
	}
	want := []string{".jpg@1:prepared vision", ".svg@1:no processor yet vision-converted", ".one@0:prepared onenote"}
	if !slices.Equal(names, want) {
		t.Fatalf("events:\n got %q\nwant %q", names, want)
	}
	for _, w := range wheres(events) {
		if !strings.HasPrefix(w, "page ") || !strings.Contains(w, ", ") {
			t.Errorf("where %q: want the page and the medium's place", w)
		}
	}
	if ink := events[1]; ink.Part.Name != inked || !strings.HasSuffix(ink.Part.Where, ", page ink") || ink.Part.Kind != route.SVG {
		t.Errorf("ink: %s at %q, %s", ink.Part.Name, ink.Part.Where, ink.Part.Kind)
	}
	section := events[2]
	outputNamed(t, section, dispatch.RecordOutput, "notebook")
	outputNamed(t, section, dispatch.RecordOutput, "section 3ef70c2a-1787-854a-a0b4-ef192956c142")
	md := outputNamed(t, section, dispatch.MarkdownOutput, page)
	if md.Meta["title"] != "Test Page" || !bytes.HasPrefix(md.Value, []byte("# Test Page")) {
		t.Errorf("page markdown %v, %.40q", md.Meta, md.Value)
	}
	recordThenMarkdown(t, section, page)

	events = dispatchAll(t, d, filepath.Join(onenoteFixtures, "New Section 1.one"), nil)
	img := events[0]
	if img.Part.SHA256+".jpg" != jpg || img.Outcome != dispatch.Seen || img.Recorded != dispatch.Prepared {
		t.Errorf("the shared image: %s %s, recorded %s", img.Part.Name, img.Outcome, img.Recorded)
	}
	if last := events[len(events)-1]; last.Part.Depth != 0 || last.Outcome != dispatch.Prepared {
		t.Errorf("the section's event: %s@%d %s", last.Part.Name, last.Part.Depth, last.Outcome)
	}
}
