//go:build integration && linux

package email_test

import (
	"bytes"
	"image/png"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// stagedRoutes are the routes that name a stage, each of which the fixtures must carry to its
// stage at least once: every route but RouteUnrouted.
var stagedRoutes = []email.Route{
	email.RouteDocument, email.RouteOffice, email.RouteVision, email.RouteVisionConverted, email.RouteNested,
}

// stageConverter builds an office Converter over the real office the mise pins, soffice in
// office's pinned image, office.LibreOfficeImage, through the container runner, which pulls the
// image when it isn't present, and the host's magick, as office's own integration tier builds
// its, and logs soffice's and magick's versions.
func stageConverter(t *testing.T) *office.Converter {
	t.Helper()
	o, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatalf("office, which mise pins, run the tier through mise run integration: %v", err)
	}
	lo, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: office.LibreOfficeImage}, office.LibreOfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	mg, err := tool.NewExec(t.Context(), office.MagickSpec())
	if err != nil {
		t.Fatalf("magick, a host prerequisite for TIFF: %v", err)
	}
	t.Logf("soffice %s in %s, magick %s", lo.Version(), office.LibreOfficeImage, mg.Version())
	c, err := office.NewConverter(office.Config{Office: o, LibreOffice: lo, Magick: mg})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// stagePreparer builds document's released preparation over the host's gs, through the exec
// runner, with no corrections stage, as document's own integration tier builds its.
func stagePreparer(t *testing.T) *document.Preparer {
	t.Helper()
	gs, err := tool.NewExec(t.Context(), document.GhostscriptSpec())
	if err != nil {
		t.Fatal(err)
	}
	g, err := document.NewGhostscript(gs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s", gs.Name(), gs.Version())
	p, err := document.NewPreparer(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// stages carries attachments to their stages: the real stages, the routes seen and run, and the
// pictures gathered for vision, which are sent once all the fixtures are read.
type stages struct {
	t        *testing.T
	reader   *email.Reader
	office   *office.Converter
	document *document.Preparer
	// seen are the routes the fixtures' attachments take; ran, those whose stage ran.
	seen, ran map[email.Route]int
	// dispatched counts the attachments dispatched; readAgain, the by-value .msg files read
	// through the CLI again, and readAgainDispatched, the attachments those reads dispatched.
	dispatched, readAgain, readAgainDispatched int
	// done are the contents already carried to their stage, by File, each content's stage run
	// once whichever attachments carry it; it holds the attachment that first did.
	done map[string]string
	// pictures are the distinct pictures for vision, by File, in the order first seen.
	pictures map[string]*picture
	order    []string
}

// TestRoutesToStages carries each routed attachment from the real CLI to its real stage. The real
// pinned mail reads every copied fixture, .pst and .msg, and the built .msg, each into an
// attachments directory of its own, and each attachment of each message read, those of the
// messages it nests included, is routed with Attachment.Route and dispatched on its route:
//
//   - RouteDocument: document's Images over the host's gs, each page's text layer read and the
//     page rendered;
//   - RouteOffice: office's Convert given the attachment's name, a legacy document through
//     soffice in office's pinned image first;
//   - RouteVision: the picture as stored, gathered for vision;
//   - RouteVisionConverted: office's ConvertPicture, and its PNG gathered for vision;
//   - RouteNested: a message nested under its record, which mail read with its parent, its own
//     attachments dispatched in turn; a by-value .msg file read through the CLI again, into an
//     attachments directory of its own, and its messages' attachments dispatched; a message
//     listed without its content, as a .pst lists an embedded one, mail's known limit, logged
//     and not run;
//   - RouteUnrouted: logged, with its reason.
//
// Each content's stage runs once, by File, so an attachment whose content was carried already is
// logged with the attachment that first carried it. Then each distinct picture is sent once to
// visionModel on the router. It fails unless every stage it runs succeeds, every staged route
// the fixtures hold had its stage run, the fixtures hold every staged route, and a by-value .msg
// was read through the CLI again, counted apart as "nested, read again", with attachments of its
// own dispatched. It logs a recorded line per stage run, with its wall time and result, and per
// picture.
func TestRoutesToStages(t *testing.T) {
	c := visiontest.Router(t)
	s := &stages{
		t:        t,
		reader:   realReader(t),
		office:   stageConverter(t),
		document: stagePreparer(t),
		seen:     map[email.Route]int{},
		ran:      map[email.Route]int{},
		done:     map[string]string{},
		pictures: map[string]*picture{},
	}
	inputs := []string{filepath.Join(builtDir, builtMsg)}
	for _, rel := range realInputs(t) {
		inputs = append(inputs, filepath.Join(mailCopy, "fixtures", rel))
	}
	for _, input := range inputs {
		s.read(input, filepath.Base(input))
	}

	for _, route := range slices.Sorted(maps.Keys(s.seen)) {
		t.Logf("recorded: route %s: %d attachments, %d stage runs", route, s.seen[route], s.ran[route])
	}
	t.Logf("recorded: route nested, read again: %d stage runs, %d attachments dispatched", s.readAgain, s.readAgainDispatched)
	for _, route := range stagedRoutes {
		switch {
		case s.seen[route] == 0:
			t.Errorf("no fixture holds an attachment routed %s", route)
		case s.ran[route] == 0:
			t.Errorf("route %s: %d attachments, no stage run", route, s.seen[route])
		}
	}
	switch {
	case s.readAgain == 0:
		t.Error("no by-value .msg was read through the CLI again")
	case s.readAgainDispatched == 0:
		t.Errorf("%d by-value .msg files read again, no attachment of theirs dispatched", s.readAgain)
	}

	pictures := make([]*picture, 0, len(s.order))
	for _, file := range s.order {
		pictures = append(pictures, s.pictures[file])
	}
	if len(pictures) == 0 {
		t.Fatal("no attachment routes a picture to vision")
	}
	describePictures(t, c, pictures)
}

// read reads input, a .pst or .msg, through the real mail into an attachments directory of its
// own, and dispatches each message's attachments; where names it in the recorded lines.
func (s *stages) read(input, where string) {
	t := s.t
	dir := t.TempDir()
	stream := mustRead(t)(s.reader.Read(t.Context(), email.Request{Input: input, Attachments: dir}))
	for rec, err := range stream.Records() {
		if err != nil {
			t.Errorf("%s: %v", where, err)
			return
		}
		if m, ok := rec.(*email.Message); ok {
			s.message(m, where)
		}
	}
}

// message dispatches each of m's attachments, where naming m's place.
func (s *stages) message(m *email.Message, where string) {
	for i := range m.Attachments {
		s.dispatch(&m.Attachments[i], where)
	}
}

// dispatch routes a and runs its route's stage.
func (s *stages) dispatch(a *email.Attachment, where string) {
	t := s.t
	route, why, err := a.Route()
	if err != nil {
		t.Errorf("%s: %s: %v", where, a.Filename, err)
		return
	}
	s.seen[route]++
	s.dispatched++
	name := a.Filename
	if name == "" {
		name = "(unnamed)"
	}
	source := where + " " + name
	if route != email.RouteNested && route != email.RouteUnrouted {
		if first, ok := s.done[a.File]; ok {
			if p, ok := s.pictures[a.File]; ok {
				p.sources = append(p.sources, source)
			}
			t.Logf("stage: %s: %s (%s), its content carried as %s", source, route, why, first)
			return
		}
		s.done[a.File] = source
	}
	switch route {
	case email.RouteDocument:
		s.pdf(a, source)
	case email.RouteOffice:
		s.convert(a, source)
	case email.RouteVision:
		s.stored(a, source)
	case email.RouteVisionConverted:
		s.converted(a, source)
	case email.RouteNested:
		s.nested(a, source, why)
	default:
		t.Logf("stage: %s: %s (%s), no stage", source, route, why)
	}
}

// pdf runs document's Images over a's PDF, reading each page's text layer and rendering the page,
// and records its pages, those with a text layer, and the wall time. Every page must be yielded,
// in order, as a PNG that decodes.
func (s *stages) pdf(a *email.Attachment, source string) {
	t := s.t
	f, err := os.Open(a.Path)
	if err != nil {
		t.Errorf("%s: %v", source, err)
		return
	}
	defer func() { _ = f.Close() }()
	start := time.Now()
	var pages, text, count int
	for img, err := range s.document.Images(t.Context(), f, document.ImagesRequest{Dir: t.TempDir(), Text: true, Concurrency: 4}) {
		if err != nil {
			t.Errorf("%s: page %d: %v", source, img.Number, err)
			return
		}
		pages++
		count = img.Count
		if img.Number != pages {
			t.Errorf("%s: yield %d is page %d", source, pages, img.Number)
		}
		if document.HasText(img.Text) {
			text++
		}
		if _, err := png.DecodeConfig(bytes.NewReader(img.PNG)); err != nil {
			t.Errorf("%s: page %d isn't a PNG: %v", source, img.Number, err)
		}
	}
	if pages == 0 || pages != count {
		t.Errorf("%s: %d pages yielded of %d", source, pages, count)
		return
	}
	s.ran[email.RouteDocument]++
	t.Logf("recorded: stage document, %s: %d of %d pages rendered at 150 dpi, %d with a text layer: %v wall",
		source, pages, count, text, time.Since(start).Round(time.Millisecond))
}

// convert runs office's Convert over a's document, given its name, as JSON, and records the
// model's format, the legacy format it was converted from, its media, its warnings, and the wall
// time. A legacy document must be converted from its extension's format.
func (s *stages) convert(a *email.Attachment, source string) {
	t := s.t
	f, err := os.Open(a.Path)
	if err != nil {
		t.Errorf("%s: %v", source, err)
		return
	}
	defer func() { _ = f.Close() }()
	start := time.Now()
	res, err := s.office.Convert(t.Context(), f, office.Request{Dir: t.TempDir(), Name: a.Filename})
	elapsed := time.Since(start)
	if err != nil {
		t.Errorf("%s: %v", source, err)
		return
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(a.Filename), "."))
	if res.Document == nil || res.Document.Format == "" {
		t.Errorf("%s: no model", source)
		return
	}
	if res.ConvertedFrom != "" && res.ConvertedFrom != ext {
		t.Errorf("%s: converted from %s, want %s", source, res.ConvertedFrom, ext)
	}
	from := "as it was"
	if res.ConvertedFrom != "" {
		from = "from " + res.ConvertedFrom + " through soffice"
	}
	for _, w := range res.Warnings {
		t.Logf("warning: %s: %s", source, w)
	}
	s.ran[email.RouteOffice]++
	t.Logf("recorded: stage office, %s: a %s model, %s, %d media, %d warnings: %v wall",
		source, res.Document.Format, from, len(res.Media), len(res.Warnings), elapsed.Round(time.Millisecond))
}

// stored gathers a's picture, as stored, for vision, its content type by its signature.
func (s *stages) stored(a *email.Attachment, source string) {
	t := s.t
	data, err := os.ReadFile(a.Path)
	if err != nil {
		t.Errorf("%s: %v", source, err)
		return
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		t.Errorf("%s: content type %s, want a picture's", source, ct)
		return
	}
	s.gather(a.File, &picture{data: data, contentType: ct, sources: []string{source}})
	s.ran[email.RouteVision]++
	t.Logf("recorded: stage vision, %s: %s %s, %d bytes, as stored", source, ct, visiontest.PixelSize(data), len(data))
}

// converted runs office's ConvertPicture over a's picture and gathers its PNG for vision,
// recording the PNG's size and the wall time. The PNG must be named by a's SHA-256 and decode.
func (s *stages) converted(a *email.Attachment, source string) {
	t := s.t
	dir := t.TempDir()
	start := time.Now()
	out, err := s.office.ConvertPicture(t.Context(), office.PictureRequest{Dir: dir, Path: a.Path})
	elapsed := time.Since(start)
	if err != nil {
		t.Errorf("%s: %v", source, err)
		return
	}
	if want := filepath.Join(dir, strings.TrimSuffix(a.File, filepath.Ext(a.File))+".png"); out != want {
		t.Errorf("%s: PNG %s, want %s", source, out, want)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Errorf("%s: %v", source, err)
		return
	}
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		t.Errorf("%s: its PNG doesn't decode: %v", source, err)
		return
	}
	s.gather(a.File, &picture{data: data, contentType: "image/png", sources: []string{source}})
	s.ran[email.RouteVisionConverted]++
	t.Logf("recorded: stage vision-converted, %s: %d bytes -> png %s, %d bytes: %v wall",
		source, sizeOf(a), visiontest.PixelSize(data), len(data), elapsed.Round(time.Millisecond))
}

// nested carries an embedded message on: one nested under its record, which mail read with its
// parent, has its own attachments dispatched; a by-value .msg file is read through the CLI again;
// one listed without its content can't be read, mail's known limit, and is logged, not run.
func (s *stages) nested(a *email.Attachment, source, why string) {
	t := s.t
	switch {
	case a.Message != nil:
		s.ran[email.RouteNested]++
		t.Logf("recorded: stage nested, %s: %s; message %q, %d attachments, dispatched in turn",
			source, why, a.Message.Subject, len(a.Message.Attachments))
		s.message(a.Message, source+" >")
	case a.Path != "":
		s.ran[email.RouteNested]++
		s.readAgain++
		before := s.dispatched
		s.read(a.Path, source+" >")
		n := s.dispatched - before
		s.readAgainDispatched += n
		t.Logf("recorded: stage nested, read again, %s: %s; read through the CLI again, %d attachments dispatched in turn",
			source, why, n)
	default:
		t.Logf("stage: %s: nested (%s), not read: mail lists it without its content", source, why)
	}
}

// gather adds a distinct picture for vision under file, its attachment's content's name.
func (s *stages) gather(file string, p *picture) {
	s.pictures[file] = p
	s.order = append(s.order, file)
}

// sizeOf returns a's stored size in bytes, or -1 when mail didn't read its content.
func sizeOf(a *email.Attachment) int64 {
	if a.Size == nil {
		return -1
	}
	return *a.Size
}
