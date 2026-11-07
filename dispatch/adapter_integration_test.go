//go:build linux && integration

package dispatch_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/media"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// builtMSG is email's built .msg, attaching a docx, a legacy xls, a TIFF, an EMF, and a .msg.
var builtMSG = filepath.Join("..", "email", "testdata", "built", "msg", "office-attachments.msg")

// allStages returns every adapter, over this machine's gs, magick, ffmpeg, and ffprobe,
// LibreOffice through the container runner in its pinned image, and the office, mail, and onenote
// mise pins (the last two through emailStage and onenoteStage), as a composition root wires them:
// the document stage reading each page's text layer, and the text stage, which runs no tool.
func allStages(t *testing.T) dispatch.Stages {
	t.Helper()
	gs := pinned(t, document.GhostscriptSpec())
	mg := pinned(t, picture.MagickSpec())
	lo, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: picture.LibreOfficeImage}, picture.LibreOfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	of := pinned(t, office.OfficeSpec())

	render, err := document.NewGhostscript(gs)
	if err != nil {
		t.Fatal(err)
	}
	prep, err := document.NewPreparer(render, nil)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{
		Preparer: prep, Version: dispatch.Versions(gs), Request: document.ImagesRequest{Text: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	oc, err := office.NewConverter(t.Context(), office.Config{Office: of, LibreOffice: lo, Magick: mg})
	if err != nil {
		t.Fatal(err)
	}
	offices, err := dispatch.NewOfficeStage(dispatch.OfficeConfig{Converter: oc, Version: dispatch.Versions(of, lo, mg)})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := picture.NewConverter(t.Context(), picture.Config{Magick: mg, LibreOffice: lo})
	if err != nil {
		t.Fatal(err)
	}
	pictures, err := dispatch.NewPictureStage(dispatch.PictureConfig{Converter: pc, Version: dispatch.Versions(mg, lo)})
	if err != nil {
		t.Fatal(err)
	}
	ff, fp := pinned(t, audio.FFmpegSpec()), pinned(t, audio.FFprobeSpec())
	splitter, err := audio.NewSplitter(audio.Config{FFmpeg: ff, FFprobe: fp})
	if err != nil {
		t.Fatal(err)
	}
	audios, err := dispatch.NewAudioStage(dispatch.AudioConfig{Splitter: splitter, Version: dispatch.Versions(ff, fp)})
	if err != nil {
		t.Fatal(err)
	}
	streamer, err := media.NewStreamer(media.Config{FFmpeg: ff, FFprobe: fp})
	if err != nil {
		t.Fatal(err)
	}
	videos, err := dispatch.NewVideoStage(dispatch.VideoConfig{Streamer: streamer, Version: dispatch.Versions(ff, fp)})
	if err != nil {
		t.Fatal(err)
	}
	return dispatch.Stages{
		route.Document:        docs,
		route.Office:          offices,
		route.VisionConverted: pictures,
		route.Vision:          dispatch.NewVisionStage(),
		route.Nested:          emailStage(t),
		route.OneNote:         onenoteStage(t),
		route.Audio:           audios,
		route.Video:           videos,
		route.Text:            dispatch.NewTextStage(),
	}
}

// wantPNG fails the test unless the image output o is a PNG file.
func wantPNG(t *testing.T, o dispatch.Output) {
	t.Helper()
	b, err := os.ReadFile(o.Path)
	if err != nil || o.Kind != dispatch.ImageOutput || o.MediaType != "image/png" || !bytes.HasPrefix(b, []byte(route.PNGSignature)) {
		t.Errorf("%s: a %s %s of %d bytes, %v; want a PNG", o.Name, o.Kind, o.MediaType, len(b), err)
	}
}

// TestDocumentStageReal: the clean PDF's six pages, through gs, are each its text layer and a
// PNG, in page order, the text the page's own.
func TestDocumentStageReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, filepath.Join("..", "document", "testdata", "clean.pdf"), func(ev *dispatch.Event) {
		if o := ev.Output; o != nil && o.Kind == dispatch.ImageOutput {
			wantPNG(t, *o)
		}
	})
	wantSteps(t, events, []step{{"clean.pdf", 0, dispatch.Prepared}})
	var got []string
	for _, o := range events[0].Outputs {
		got = append(got, string(o.Kind)+" "+o.Name+" of "+o.Meta["count"])
	}
	var want []string
	for _, n := range []string{"1", "2", "3", "4", "5", "6"} {
		want = append(want, "text page "+n+" of 6", "image page "+n+" of 6")
	}
	if !slices.Equal(got, want) {
		t.Fatalf("outputs:\n got %q\nwant %q", got, want)
	}
	if text := string(events[0].Outputs[0].Value); !strings.Contains(text, "The Harbor Survey") {
		t.Errorf("page 1's text %.80q", text)
	}
}

// TestOfficeStageReal: a document's EMF, with LibreOffice and magick, is found as its PNG and
// prepared by the vision stage, as its PNGs are.
func TestOfficeStageReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, filepath.Join(officeFixtures, "media.libreoffice.docx"), nil)
	const emf = "670be6349ed84b27d6ca43d66ba6e9454c632f68de8d9b8fea78a480e90afb31"
	for _, ev := range events[:len(events)-1] {
		if ev.Part.Route != route.Vision || ev.Outcome != dispatch.Prepared {
			t.Errorf("%s at %q: %s %s", ev.Part.Name, ev.Part.Where, ev.Part.Route, ev.Outcome)
		}
	}
	if ev := byName(t, events, emf+".png"); ev.Part.Where != "media "+emf+".emf, as PNG" {
		t.Errorf("the EMF's PNG found at %q", ev.Part.Where)
	}
	if last := events[len(events)-1]; last.Part.Depth != 0 || last.Children != 4 {
		t.Errorf("the document: %s@%d, %d children", last.Part.Name, last.Part.Depth, last.Children)
	}
}

// TestOfficeStageRTFReal: an RTF, through LibreOffice in its pinned image to docx and then
// office, is one Markdown document converted from "rtf", holding the RTF's heading and table.
func TestOfficeStageRTFReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, filepath.Join("..", "office", "testdata", "rtf", "memo.rtf"), nil)
	wantSteps(t, events, []step{{"memo.rtf", 0, dispatch.Prepared}})
	if ev := events[0]; ev.Part.Route != route.Office || ev.Part.Kind != route.RTF {
		t.Errorf("memo.rtf: %s %s", ev.Part.Route, ev.Part.Kind)
	}
	md := outputNamed(t, events[0], dispatch.MarkdownOutput, "document")
	if md.Meta["converted_from"] != "rtf" {
		t.Errorf("converted from %q, want rtf", md.Meta["converted_from"])
	}
	for _, want := range []string{"# Harbor memo", "| North | 12 m |", "Next survey: Thursday."} {
		if !bytes.Contains(md.Value, []byte(want)) {
			t.Errorf("no %q in the Markdown:\n%s", want, md.Value)
		}
	}
}

// TestPictureStageReal: an SVG, a HEIC, a WebP, and an AVIF each convert to one PNG, and a TIFF
// of three pages to three, in page order, each naming its page and the count.
func TestPictureStageReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	for name, kind := range map[string]route.Kind{
		"shapes.svg": route.SVG, "two-images.heic": route.HEIC, "animated.webp": route.WebP, "animated.avif": route.AVIF,
	} {
		events := dispatchAll(t, d, filepath.Join("..", "picture", "testdata", name), func(ev *dispatch.Event) {
			if ev.Output != nil {
				wantPNG(t, *ev.Output)
			}
		})
		wantSteps(t, events, []step{{name, 0, dispatch.Prepared}})
		if o := events[0].Outputs; len(o) != 1 || o[0].Name != "picture" || o[0].Meta["from"] != string(kind) {
			t.Errorf("%s: outputs %+v", name, o)
		}
	}
	events := dispatchAll(t, d, filepath.Join("..", "picture", "testdata", "pages.tiff"), func(ev *dispatch.Event) {
		if ev.Output != nil {
			wantPNG(t, *ev.Output)
		}
	})
	wantSteps(t, events, []step{{"pages.tiff", 0, dispatch.Prepared}})
	var got []string
	for _, o := range events[0].Outputs {
		got = append(got, o.Name+" "+o.Meta["page"]+"/"+o.Meta["count"]+" "+o.Meta["from"])
	}
	if want := []string{"page 1 1/3 tiff", "page 2 2/3 tiff", "page 3 3/3 tiff"}; !slices.Equal(got, want) {
		t.Errorf("pages.tiff: outputs %q, want %q", got, want)
	}
}

// TestEmailStageReal: a .msg's attachments each reach their route's stage, recursively: the
// docx office's, its pictures vision's; the legacy xls office's through LibreOffice; the TIFF and
// the EMF the picture stage's; and the forwarded .msg email's again. A PDF attachment reaches the
// document stage.
func TestEmailStageReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, builtMSG, func(ev *dispatch.Event) {
		if ev.Part.Route == route.VisionConverted && ev.Output != nil {
			wantPNG(t, *ev.Output)
		}
	})
	top := map[string]route.Route{
		"minutes.docx": route.Office, "budget.xls": route.Office, "scan.tif": route.VisionConverted,
		"diagram.emf": route.VisionConverted, "forwarded.msg": route.Nested,
	}
	for i, ev := range events {
		if ev.Outcome != dispatch.Prepared {
			t.Errorf("%s@%d at %q: %s %s, %s", ev.Part.Name, ev.Part.Depth, ev.Part.Where, ev.Part.Route, ev.Outcome, ev.Reason)
		}
		if want, ok := top[ev.Part.Name]; ok && ev.Part.Depth == 1 {
			if ev.Part.Route != want || !strings.HasPrefix(ev.Part.Where, "message, attachment ") {
				t.Errorf("%s at %q: route %s, want %s", ev.Part.Name, ev.Part.Where, ev.Part.Route, want)
			}
			delete(top, ev.Part.Name)
		}
		// Post-order: a part's parent's event comes after it.
		if ev.Part.Depth > 0 && !slices.ContainsFunc(events[i+1:], func(p dispatched) bool { return p.Part.SHA256 == ev.Part.Parent }) {
			t.Errorf("event %d, %s@%d, comes after its parent's", i, ev.Part.Name, ev.Part.Depth)
		}
	}
	if len(top) != 0 {
		t.Errorf("attachments never dispatched: %v", top)
	}
	if xls := byName(t, events, "budget.xls"); outputNamed(t, xls, dispatch.MarkdownOutput, "document").Meta["converted_from"] != "xls" {
		t.Error("budget.xls wasn't converted from xls")
	}

	events = dispatchAll(t, d, filepath.Join(mailFixtures, "msg", "tika", "testMSG_att_msg.msg"), nil)
	wantSteps(t, events, []step{
		{"smbprn.00009008.KdcPjl.pdf", 1, dispatch.Prepared},
		{"testMSG_att_msg.msg", 0, dispatch.Prepared},
	})
	if ev := events[0]; ev.Stage.Name != "document" || len(ev.Outputs) == 0 {
		t.Errorf("the PDF attachment: %v, %d outputs", ev.Stage, len(ev.Outputs))
	}
}

// TestOneNoteStageReal: a page's ink, an SVG, reaches the picture stage and becomes a PNG.
func TestOneNoteStageReal(t *testing.T) {
	d := newDispatcher(t, allStages(t), dispatch.NewMemoryIndex())
	events := dispatchAll(t, d, filepath.Join(onenoteFixtures, "handwriting_recognition.one"), func(ev *dispatch.Event) {
		if ev.Part.Route == route.VisionConverted && ev.Output != nil {
			wantPNG(t, *ev.Output)
		}
	})
	var ink *dispatched
	for i := range events {
		if strings.HasSuffix(events[i].Part.Where, ", page ink") {
			ink = &events[i]
		}
	}
	if ink == nil || ink.Outcome != dispatch.Prepared || ink.Stage.Name != "picture" || len(ink.Outputs) != 1 {
		t.Fatalf("the ink: %+v", ink)
	}
}
