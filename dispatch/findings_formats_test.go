//go:build findings && linux

package dispatch_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/corpustest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The formats finding, in the findings tier, never in CI or the check: every route task formats
// added, the archive kinds, a TIFF's pages, HEIC, WebP, and AVIF, text, RTF, a PDF's password,
// forms, and attachments, and the conversation history records, dispatched over the committed
// fixtures and ~/docs and held to the truth where there is one; and what the dispatcher costs
// on each ~/docs input and the conversation store, in time and peak memory. `mise run
// findings-formats` runs it; what it found is in docs/findings/formats.md. Its settings, from the
// environment:
//
//   - DOCS names the corpus root, ~/docs when unset; each ~/docs input is checked by its SHA-256
//     against the corpus manifest before it is read.
//   - FORMATS_FINDINGS_RUNS is the timed runs of each input, 3 when unset.
//   - FORMATS_FINDINGS_RESULTS names a directory each test writes its results to as JSON; unset
//     writes nothing, so a run keeps its results only when asked to.
const (
	formatsRunsEnv    = "FORMATS_FINDINGS_RUNS"
	formatsResultsEnv = "FORMATS_FINDINGS_RESULTS"
)

// formatStages returns findingStages' adapters with the archive stage under opts, reading
// through the 7zz mise pins when sevenZip is set and without 7-Zip otherwise, and the document
// stage over this machine's qpdf and pdfdetach, its embedded files held to opts's thresholds.
func formatStages(ctx context.Context, sevenZip bool, opts inspect.Options) (dispatch.Stages, error) {
	stages, err := findingStages(ctx)
	if err != nil {
		return nil, err
	}
	ac := dispatch.ArchiveConfig{Inspect: opts}
	if sevenZip {
		sz, err := tool.NewExec(ctx, sevenzip.SevenZipSpec())
		if err != nil {
			return nil, fmt.Errorf("7zz: %w", err)
		}
		r, err := sevenzip.NewReader(sevenzip.Config{SevenZip: sz})
		if err != nil {
			return nil, err
		}
		ac.SevenZip, ac.Version = r, dispatch.Versions(sz)
	}
	archives, err := dispatch.NewArchiveStage(ac)
	if err != nil {
		return nil, err
	}
	stages[route.Archive] = archives

	execs := map[string]*tool.Tool{}
	for name, spec := range map[string]tool.Spec{
		"gs": document.GhostscriptSpec(), "qpdf": document.QPDFSpec(), "pdfdetach": document.PDFDetachSpec(),
	} {
		tl, err := tool.NewExec(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		execs[name] = tl
	}
	gs, qt, dt := execs["gs"], execs["qpdf"], execs["pdfdetach"]
	render, err := document.NewGhostscript(gs)
	if err != nil {
		return nil, err
	}
	prep, err := document.NewPreparer(render, nil)
	if err != nil {
		return nil, err
	}
	q, err := document.NewQPDF(qt)
	if err != nil {
		return nil, err
	}
	detach, err := document.NewDetacher(dt)
	if err != nil {
		return nil, err
	}
	docs, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{
		Preparer: prep, Version: dispatch.Versions(gs, qt, dt), Request: document.ImagesRequest{Text: true},
		QPDF: q, PDFDetach: detach, Inspect: opts,
	})
	if err != nil {
		return nil, err
	}
	stages[route.Document] = docs
	return stages, nil
}

// formatGroups are the committed fixtures each route's finding reads, as globs under the
// repository's root, by group, each glob's matches sorted, but for a README.md or a Go file.
var formatGroups = []struct {
	group string
	globs []string
}{
	{"archives", []string{
		"inspect/testdata/*.tar", "inspect/testdata/*.tar.gz", "inspect/testdata/*.tgz",
		"sevenzip/testdata/commons-compress/*.zip", "sevenzip/testdata/commons-compress/*.7z",
		"sevenzip/testdata/libarchive/*.zipx", "sevenzip/testdata/generated/*",
	}},
	{"pictures", []string{
		"picture/testdata/pages.tiff", "picture/testdata/two-images.heic", "picture/testdata/oriented.webp",
		"picture/testdata/animated.webp", "picture/testdata/animated.avif",
	}},
	{"rtf", []string{"office/testdata/rtf/memo.rtf"}},
	{"pdfs", []string{"document/testdata/forms/acroform.pdf", "document/testdata/clean.pdf"}},
	{"conversations", []string{"email/testdata/mail/fixtures/generated/pst/conversations.pst"}},
}

// docsGroups are the ~/docs inputs each route's finding reads, by group, under the corpus root.
var docsGroups = []struct {
	group string
	paths []string
}{
	{"archives", []string{
		"archive/mixed-bundle.zip", "archive/mixed-bundle.tar.gz", "archive/mixed-bundle.tar.zst", "archive/mixed-bundle.7z",
	}},
	{"pictures", []string{
		"image/formats/tiff/scanned-3page-g4.tif", "image/formats/tiff/scanned-3page-lzw-gray.tif",
		"image/formats/tiff/photo-deflate.tif", "image/formats/tiff/old-style-jpeg-compression.tif",
		"image/formats/heic/example.heic",
		"image/formats/webp/photo.webp", "image/formats/webp/text-lossless.webp",
		"image/formats/avif/example.avif", "image/formats/avif/bbb-alpha-inverted.avif",
		"image/formats/avif/hdr-cosmos.avif", "image/formats/avif/monochrome.avif",
	}},
	{"text", []string{
		"text/encodings/utf8-nobom.txt", "text/encodings/utf8-bom.txt", "text/encodings/utf16le-bom.txt",
		"text/encodings/windows-1252.txt", "text/encodings/crlf-windows-1252.txt", "text/encodings/iso-8859-1.txt",
		"text/encodings/koi8-r.txt", "text/encodings/euc-jp.txt", "text/encodings/shift-jis.txt",
		"text/encodings/windows-1256-arabic.txt", "text/encodings/utf16be-nobom.txt",
	}},
	{"rtf", []string{
		"office/rtf/test-rtf.rtf", "office/rtf/lists.libreoffice.rtf", "office/rtf/tables.libreoffice.rtf",
		"office/rtf/lorem-ipsum-calibre.rtf",
	}},
	{"pdfs", []string{
		"pdf/edge/encrypted/encryption-openpassword.pdf", "pdf/edge/encrypted/simple-open-password.pdf",
		"pdf/edge/encrypted/unicode-password-corrigendum5.pdf", "pdf/edge/encrypted/encryption-nocopy.pdf",
		"pdf/edge/encrypted/encryption-notextaccess.pdf", "pdf/edge/encrypted/unicode-password-baseline.pdf",
		"pdf/edge/forms/isartor-interactive-form.pdf", "pdf/edge/forms/pdfa2b-interactive-form-pass.pdf",
		"pdf/edge/attachments/file-attachment.pdf", "pdf/edge/attachments/file-attachment-annotation.pdf",
		"pdf/edge/attachments/embedded-video-avi.pdf",
	}},
}

// bombFixtures are the archive fixtures whose zeros pass a lowered bomb rule, dispatched again
// under bombOptions, as the unit tier lowers it to see the rule fire.
var bombFixtures = []string{
	"inspect/testdata/bomb.tar.gz", "inspect/testdata/sparse-bomb.tar",
	"sevenzip/testdata/generated/bomb.7z", "sevenzip/testdata/generated/bomb.cab", "sevenzip/testdata/generated/bomb.zst",
}

// bombOptions are the lowered thresholds: 1 MiB and 100:1.
var bombOptions = inspect.Options{BombSize: 1 << 20, BombRatio: 100}

// formatTextCap is the most of a text or Markdown output a part's record keeps.
const formatTextCap = 8 << 10

// formatOutput is one output as the finding records it: its kind, name, media type, and Meta; a
// record's value; a text or Markdown output's text, to formatTextCap, and whether it was cut; and
// an image's pixel size.
type formatOutput struct {
	Kind      dispatch.OutputKind `json:"kind"`
	Name      string              `json:"name,omitzero"`
	MediaType string              `json:"media_type,omitzero"`
	Meta      map[string]string   `json:"meta,omitzero"`
	Value     json.RawMessage     `json:"value,omitzero"`
	Text      string              `json:"text,omitzero"`
	Cut       bool                `json:"cut,omitzero"`
	Pixels    string              `json:"pixels,omitzero"`
}

// formatPart is one part's own event as the finding records it, its outputs among it.
type formatPart struct {
	Place    string           `json:"place"`
	Name     string           `json:"name"`
	Depth    int              `json:"depth"`
	Size     int64            `json:"size"`
	SHA256   string           `json:"sha256"`
	Kind     route.Kind       `json:"kind"`
	Route    route.Route      `json:"route"`
	Stage    string           `json:"stage,omitzero"`
	Outcome  dispatch.Outcome `json:"outcome"`
	Reason   string           `json:"reason,omitzero"`
	Flags    []string         `json:"flags,omitzero"`
	Children int              `json:"children"`
	Outputs  []formatOutput   `json:"outputs,omitzero"`
}

// formatCheck is one comparison against truth: what it compares, the truth's answer, and the
// run's.
type formatCheck struct {
	What string `json:"what"`
	Want string `json:"want"`
	Got  string `json:"got"`
}

// formatInput is one input's dispatch, one way: its group and way, its path, under the
// repository's root or "~/docs/", its size, the dispatch's time, its parts, and its checks.
type formatInput struct {
	Group   string        `json:"group"`
	Way     string        `json:"way"`
	Input   string        `json:"input"`
	Size    int64         `json:"size"`
	Elapsed time.Duration `json:"elapsed"`
	Parts   []formatPart  `json:"parts"`
	Checks  []formatCheck `json:"checks,omitzero"`
}

// formatInputs returns the committed fixtures, by group, as paths from the package's directory.
func formatInputs(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, g := range formatGroups {
		for _, pattern := range g.globs {
			matches, err := filepath.Glob(filepath.Join("..", pattern))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) == 0 {
				t.Fatalf("%s matches no fixture", pattern)
			}
			for _, m := range matches {
				if filepath.Base(m) != "README.md" && filepath.Ext(m) != ".go" {
					out[g.group] = append(out[g.group], m)
				}
			}
		}
	}
	return out
}

// docsInputs returns the ~/docs inputs, by group, as absolute paths, each checked by its SHA-256
// against the corpus manifest; a corpus that isn't here skips t.
func docsInputs(t *testing.T) (string, map[string][]string) {
	t.Helper()
	docs := corpustest.Root(t)
	if _, err := os.Stat(filepath.Join(docs, "MANIFEST.json")); err != nil {
		t.Skipf("no corpus at %s", docs)
	}
	out := map[string][]string{}
	for _, g := range docsGroups {
		if _, err := corpustest.Check(docs, g.paths); err != nil {
			t.Fatal(err)
		}
		for _, p := range g.paths {
			out[g.group] = append(out[g.group], filepath.Join(docs, p))
		}
	}
	return docs, out
}

// shown returns path as the finding names it: under the repository's root, or under ~/docs/.
func shown(docs, path string) string {
	if rel, ok := strings.CutPrefix(path, docs+string(filepath.Separator)); ok {
		return "~/docs/" + rel
	}
	return strings.TrimPrefix(filepath.ToSlash(path), "../")
}

// dispatchFormat dispatches path over stages, through a dispatcher of its own over a fresh memory
// index, so no part is seen from another input, and records every part's event with its outputs.
func dispatchFormat(t *testing.T, stages dispatch.Stages, group, way, docs, path string) formatInput {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	d := newDispatcher(t, stages, dispatch.NewMemoryIndex())
	input := shown(docs, path)
	// Each output's text or pixels, read in the loop's body while its file is there, in the
	// order of its part's outputs.
	extra := map[string][]formatOutput{}
	began := time.Now()
	events := dispatchAll(t, d, path, func(ev *dispatch.Event) {
		o := ev.Output
		if o == nil {
			return
		}
		fo := formatOutput{Kind: o.Kind, Name: o.Name, MediaType: o.MediaType, Meta: o.Meta}
		if o.Kind == dispatch.RecordOutput {
			fo.Value = json.RawMessage(o.Value)
		}
		data := o.Value
		if o.Path != "" && o.Kind != dispatch.RecordOutput {
			if data, err = os.ReadFile(o.Path); err != nil {
				t.Errorf("%s: %s's %s: %v", input, ev.Part.Name, o.Name, err)
			}
		}
		switch o.Kind {
		case dispatch.TextOutput, dispatch.MarkdownOutput:
			fo.Text, fo.Cut = string(data), len(data) > formatTextCap
			if fo.Cut {
				fo.Text = string(data[:formatTextCap])
			}
		case dispatch.ImageOutput:
			fo.Pixels = visiontest.PixelSize(data)
		}
		extra[ev.Part.Path] = append(extra[ev.Part.Path], fo)
	})
	elapsed := time.Since(began)
	res := formatInput{Group: group, Way: way, Input: input, Size: info.Size(), Elapsed: elapsed}
	for _, e := range places(input, events) {
		ev := e.ev
		p := formatPart{Place: e.place, Name: ev.Part.Name, Depth: ev.Part.Depth, Size: ev.Part.Size,
			SHA256: ev.Part.SHA256, Kind: ev.Part.Kind, Route: ev.Part.Route, Stage: ev.Stage.Name,
			Outcome: ev.Outcome, Children: ev.Children, Outputs: extra[ev.Part.Path]}
		if ev.Outcome != dispatch.Prepared {
			p.Reason = ev.Reason
		}
		for _, f := range ev.Part.Flags {
			p.Flags = append(p.Flags, fmt.Sprintf("%s (%s): %s", f.Check, f.Class, f.Evidence))
		}
		res.Parts = append(res.Parts, p)
		delete(extra, ev.Part.Path)
		for _, o := range p.Outputs {
			if o.Kind == dispatch.RecordOutput && o.Meta["check"] != "" {
				t.Logf("recorded: %s %s %s\t%s\trecord %s: %s %s: %s", group, way, input, e.place, o.Name,
					o.Meta["check"], o.Meta["outcome"], o.Meta["evidence"])
			}
		}
		t.Logf("recorded: %s %s %s\t%s\t%s\t%d\t%s\t%s\t%s\t%d outputs\t%d children\t%s", group, way, input, e.place,
			ev.Part.Name, ev.Part.Depth, ev.Part.Route, cmp.Or(ev.Stage.Name, "-"), ev.Outcome, ev.Event.Outputs, ev.Children, p.Reason)
	}
	t.Logf("recorded: %s %s %s: %d parts, %v", group, way, input, len(events), elapsed.Round(time.Millisecond))
	return res
}

// top returns in's input part, the last event.
func (in *formatInput) top() formatPart {
	return in.Parts[len(in.Parts)-1]
}

// check records a comparison against truth on in, failing t when the run's answer isn't the
// truth's.
func (in *formatInput) check(t *testing.T, what, want, got string) {
	t.Helper()
	in.Checks = append(in.Checks, formatCheck{what, want, got})
	if got != want {
		t.Errorf("%s: %s: got %q, want %q", in.Input, what, got, want)
	} else {
		t.Logf("recorded: check %s: %s: %s", in.Input, what, got)
	}
}

// output returns p's first output of kind named name.
func (p formatPart) output(kind dispatch.OutputKind, name string) (formatOutput, bool) {
	for _, o := range p.Outputs {
		if o.Kind == kind && o.Name == name {
			return o, true
		}
	}
	return formatOutput{}, false
}

// TestFindingsFormats dispatches every input of formatGroups and docsGroups through formatStages,
// each over a dispatcher of its own, and records every part's event, route, outcome, and outputs;
// the archive inputs again without 7-Zip; and bombFixtures again under bombOptions. It holds them
// to the truth where there is one: ~/docs' mixed bundle routes the same parts the same way in all
// four forms; its TIFFs give their page counts; its encodings decode to encodings.truth.json's
// text, or don't, the stage's known limit; the test RTF's hidden \tc title isn't in its Markdown;
// its encrypted PDFs need a password or not, as their manifest covers say; its forms and the
// generated AcroForm give their fields; its attachments are found, the Screen video not; and the
// conversation store's records equal mail's hand truth. It writes formats.json.
func TestFindingsFormats(t *testing.T) {
	ctx := t.Context()
	docs, fromDocs := docsInputs(t)
	fixtures := formatInputs(t)
	withSevenZip, err := formatStages(ctx, true, inspect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	withoutSevenZip, err := formatStages(ctx, false, inspect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lowered, err := formatStages(ctx, true, bombOptions)
	if err != nil {
		t.Fatal(err)
	}

	var results []formatInput
	byInput := map[string]*formatInput{}
	run := func(stages dispatch.Stages, group, way string, paths []string) {
		for _, p := range paths {
			results = append(results, dispatchFormat(t, stages, group, way, docs, p))
		}
	}
	for _, g := range formatGroups {
		run(withSevenZip, g.group, "fixtures", fixtures[g.group])
	}
	for _, g := range docsGroups {
		run(withSevenZip, g.group, "docs", fromDocs[g.group])
	}
	run(withoutSevenZip, "archives", "fixtures, no 7-Zip", fixtures["archives"])
	run(withoutSevenZip, "archives", "docs, no 7-Zip", fromDocs["archives"])
	var bombs []string
	for _, b := range bombFixtures {
		bombs = append(bombs, filepath.Join("..", b))
	}
	run(lowered, "archives", "fixtures, lowered thresholds", bombs)
	for i := range results {
		if results[i].Way == "fixtures" || results[i].Way == "docs" {
			byInput[results[i].Input] = &results[i]
		}
	}

	checkBundles(t, byInput)
	checkPictures(t, byInput)
	checkEncodings(t, docs, byInput)
	checkRTF(t, byInput)
	checkPDFs(t, byInput)
	checkConversations(t, byInput)

	counts := map[string]int{}
	for _, in := range results {
		for _, p := range in.Parts {
			counts[in.Way+": "+string(p.Route)+" × "+string(p.Outcome)]++
		}
	}
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		t.Logf("recorded: count %s\t%d", k, counts[k])
	}
	saveResults(t, formatsResultsEnv, "formats.json", results)
}

// entryPlace matches a place's first archive entry, a ZIP's, a tar's, or a 7z's, and its name.
var entryPlace = regexp.MustCompile(`^(?:zip|tar|7z) entry (?:\./)?(.*)$`)

// bundleParts returns in's parts below its archive's entries, each keyed by its place from the
// entry down, the entry's name without its "./", as route, outcome, and SHA-256.
func bundleParts(in *formatInput) map[string]string {
	out := map[string]string{}
	for _, p := range in.Parts {
		segs := strings.Split(p.Place, " > ")
		for i, s := range segs {
			m := entryPlace.FindStringSubmatch(s)
			if m == nil {
				continue
			}
			key := strings.Join(append([]string{m[1]}, segs[i+1:]...), " > ")
			out[key] = fmt.Sprintf("%s %s %s", p.Route, p.Outcome, p.SHA256)
			break
		}
	}
	return out
}

// checkBundles holds ~/docs' mixed bundle's .tar.gz, .tar.zst, and .7z to its .zip: the same
// parts, each by its place below its entry, routed the same way to the same outcome.
func checkBundles(t *testing.T, byInput map[string]*formatInput) {
	t.Helper()
	zip := byInput["~/docs/archive/mixed-bundle.zip"]
	want := bundleParts(zip)
	t.Logf("recorded: mixed-bundle.zip's parts below its entries: %d", len(want))
	for _, k := range slices.Sorted(maps.Keys(want)) {
		t.Logf("recorded: mixed-bundle.zip\t%s\t%s", k, want[k])
	}
	for _, form := range []string{"mixed-bundle.tar.gz", "mixed-bundle.tar.zst", "mixed-bundle.7z"} {
		in := byInput["~/docs/archive/"+form]
		got := bundleParts(in)
		var diff []string
		for _, k := range slices.Sorted(maps.Keys(want)) {
			if got[k] != want[k] {
				diff = append(diff, fmt.Sprintf("%s: %q, the .zip's %q", k, got[k], want[k]))
			}
		}
		for _, k := range slices.Sorted(maps.Keys(got)) {
			if _, ok := want[k]; !ok {
				diff = append(diff, fmt.Sprintf("%s: %q, not in the .zip", k, got[k]))
			}
		}
		g := "the same parts, routes, and outcomes as mixed-bundle.zip"
		if len(diff) > 0 {
			g = strings.Join(diff, "; ")
		}
		in.check(t, fmt.Sprintf("%d parts below its entries", len(want)),
			"the same parts, routes, and outcomes as mixed-bundle.zip", g)
	}
}

// docsPages are ~/docs' TIFFs' page counts, which their names and the manifest's covers give, as
// picture's integration tier holds them.
var docsPages = map[string]int{
	"~/docs/image/formats/tiff/scanned-3page-g4.tif":           3,
	"~/docs/image/formats/tiff/scanned-3page-lzw-gray.tif":     3,
	"~/docs/image/formats/tiff/photo-deflate.tif":              1,
	"~/docs/image/formats/tiff/old-style-jpeg-compression.tif": 1,
	"picture/testdata/pages.tiff":                              3,
}

// checkPictures holds each TIFF to its page count, its images "page N" in order, each naming the
// count, and each other picture to one image.
func checkPictures(t *testing.T, byInput map[string]*formatInput) {
	t.Helper()
	for _, in := range byInput {
		if in.Group != "pictures" {
			continue
		}
		top := in.top()
		var names []string
		for _, o := range top.Outputs {
			if o.Kind == dispatch.ImageOutput {
				names = append(names, fmt.Sprintf("%s %s of %s", o.Name, o.Pixels, cmp.Or(o.Meta["count"], "-")))
			}
		}
		pages, ok := docsPages[in.Input]
		if !ok {
			in.check(t, "images", "1", fmt.Sprint(len(names)))
			continue
		}
		in.check(t, "pages", fmt.Sprint(pages), fmt.Sprint(len(names)))
		for i, n := range names {
			if want := fmt.Sprintf("page %d ", i+1); !strings.HasPrefix(n, want) || !strings.HasSuffix(n, fmt.Sprintf(" of %d", pages)) {
				t.Errorf("%s: image %d is %q, want %q, of %d", in.Input, i+1, n, want, pages)
			}
		}
	}
}

// checkEncodings holds each of ~/docs' encodings to encodings.truth.json: the stage's decided
// encoding, and whether its text is the truth's. The stage detects no encoding, so text with no
// byte order mark that isn't UTF-8 is windows-1252, which is right for windows-1252 and
// ISO-8859-1's printable text and wrong for the rest, the known limit; the want is the stage's
// rule, not the truth's encoding.
func checkEncodings(t *testing.T, docs string, byInput map[string]*formatInput) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(docs, "text", "encodings", "encodings.truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var truth map[string]struct {
		Encoding string `json:"encoding"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	correct := map[string]bool{
		"utf8-nobom.txt": true, "utf8-bom.txt": true, "utf16le-bom.txt": true,
		"windows-1252.txt": true, "crlf-windows-1252.txt": true, "iso-8859-1.txt": true,
	}
	for _, name := range slices.Sorted(maps.Keys(truth)) {
		in := byInput["~/docs/text/encodings/"+name]
		if in == nil {
			t.Errorf("%s: not dispatched", name)
			continue
		}
		top := in.top()
		o, ok := top.output(dispatch.TextOutput, "text")
		if !ok {
			in.check(t, "text output, truth "+truth[name].Encoding, "a text output", fmt.Sprintf("%s by %s, %s", top.Kind, top.Route, top.Outcome))
			continue
		}
		want := "garbled"
		if correct[name] {
			want = "the truth's text"
		}
		got := "garbled"
		if o.Text == truth[name].Text {
			got = "the truth's text"
		}
		t.Logf("recorded: encoding %s: truth %s, decided %q, warnings %q, %s", name, truth[name].Encoding,
			o.Meta["encoding"], o.Meta["warnings"], got)
		in.check(t, fmt.Sprintf("text, truth %s, decided %s", truth[name].Encoding, o.Meta["encoding"]), want, got)
	}
}

// checkRTF holds each RTF to its Markdown, converted from "rtf": memo.rtf's heading and table,
// and test-rtf.rtf's paragraph, one sentence eleven times, without its hidden \tc title, the same
// sentence alone on a line.
func checkRTF(t *testing.T, byInput map[string]*formatInput) {
	t.Helper()
	sentence := "Sluwe Sjaantje sloeg de slome slager"
	for input, want := range map[string][]string{
		"office/testdata/rtf/memo.rtf":   {"# Harbor memo", "| North | 12 m |"},
		"~/docs/office/rtf/test-rtf.rtf": {strings.TrimSpace(strings.Repeat(sentence+". ", 11))},
	} {
		in := byInput[input]
		top := in.top()
		var md formatOutput
		for _, o := range top.Outputs {
			if o.Kind == dispatch.MarkdownOutput {
				md = o
			}
		}
		lines := map[string]bool{}
		for line := range strings.Lines(md.Text) {
			lines[strings.TrimSpace(line)] = true
		}
		in.check(t, "converted from", "rtf", md.Meta["converted_from"])
		for _, w := range want {
			in.check(t, "a line "+w, "present", present(lines[w]))
		}
		if input == "~/docs/office/rtf/test-rtf.rtf" {
			in.check(t, `the \tc title alone on a line`, "absent", present(lines[sentence] || lines["**"+sentence+"**"]))
		}
	}
}

// present names whether a line is there.
func present(ok bool) string {
	if ok {
		return "present"
	}
	return "absent"
}

// checkPDFs holds ~/docs' encrypted PDFs to their manifest's covers, a password= or a unicode
// password needing one and so encrypted with nothing rendered, permissions alone or none rendered;
// the forms to their fields, the generated AcroForm's seven, ~/docs' one each, the clean PDF's
// none; and the attachments to the files found, each by its stored name, the Screen video's PDF
// to none.
func checkPDFs(t *testing.T, byInput map[string]*formatInput) {
	t.Helper()
	for name, want := range map[string]dispatch.Outcome{
		"encryption-openpassword.pdf": dispatch.Encrypted, "simple-open-password.pdf": dispatch.Encrypted,
		"unicode-password-corrigendum5.pdf": dispatch.Encrypted, "encryption-nocopy.pdf": dispatch.Prepared,
		"encryption-notextaccess.pdf": dispatch.Prepared, "unicode-password-baseline.pdf": dispatch.Prepared,
	} {
		in := byInput["~/docs/pdf/edge/encrypted/"+name]
		top := in.top()
		in.check(t, "outcome", string(want), string(top.Outcome))
		images := 0
		for _, o := range top.Outputs {
			if o.Kind == dispatch.ImageOutput {
				images++
			}
		}
		rendered := "nothing rendered"
		if images > 0 {
			rendered = "rendered"
		}
		wantRendered := "rendered"
		if want == dispatch.Encrypted {
			wantRendered = "nothing rendered"
		}
		in.check(t, "pages", wantRendered, rendered)
	}
	for input, want := range map[string]string{
		"document/testdata/forms/acroform.pdf":                   "7",
		"~/docs/pdf/edge/forms/isartor-interactive-form.pdf":     "1",
		"~/docs/pdf/edge/forms/pdfa2b-interactive-form-pass.pdf": "1",
		"document/testdata/clean.pdf":                            "no forms record",
	} {
		in := byInput[input]
		got := "no forms record"
		if o, ok := in.top().output(dispatch.RecordOutput, "forms"); ok {
			got = o.Meta["fields"]
		}
		in.check(t, "forms record's fields", want, got)
	}
	for input, want := range map[string]string{
		"~/docs/pdf/edge/attachments/file-attachment.pdf":            "KSBASE.WQ2",
		"~/docs/pdf/edge/attachments/file-attachment-annotation.pdf": "PF.WK1",
		"~/docs/pdf/edge/attachments/embedded-video-avi.pdf":         "",
	} {
		in := byInput[input]
		var found []string
		for _, p := range in.Parts {
			if p.Depth == 1 {
				found = append(found, p.Name+" at "+p.Place+", "+string(p.Route)+" "+string(p.Outcome))
			}
		}
		got := strings.Join(found, "; ")
		if want != "" {
			want = want + " at attachment " + want + ", "
			if len(found) == 1 && strings.HasPrefix(found[0], want) {
				got = want
			}
		}
		in.check(t, "attachments found", want, got)
	}
}

// itemTruth is one item source's hand truth in mail's truth.json "items", as email's tests read
// it.
type itemTruth struct {
	Class        string          `json:"class"`
	Subject      string          `json:"subject"`
	Sender       email.Address   `json:"sender"`
	To           []email.Address `json:"to"`
	CC           []email.Address `json:"cc"`
	SubmitTime   time.Time       `json:"submit_time"`
	DeliveryTime time.Time       `json:"delivery_time"`
	HTML         string          `json:"html"`
	Markdown     string          `json:"markdown"`
}

// checkConversations holds the conversation store's records to mail's hand truth: each item of a
// conversation class a conversation record whose class, topic, participants, start and end,
// missed, HTML, and Markdown, its record's and its Markdown output's, are the truth's; the
// IPM.Note beside them a message record, unchanged, its subject and Markdown the truth's.
func checkConversations(t *testing.T, byInput map[string]*formatInput) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "email", "testdata", "mail", "fixtures", "generated", "truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var truth struct {
		Items map[string]itemTruth `json:"items"`
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	in := byInput["email/testdata/mail/fixtures/generated/pst/conversations.pst"]
	top := in.top()
	bySubject := map[string]string{}
	for name, it := range truth.Items {
		bySubject[it.Subject] = name
	}
	matched := map[string]bool{}
	for i, o := range top.Outputs {
		if o.Kind != dispatch.RecordOutput {
			continue
		}
		var md string
		if i+1 < len(top.Outputs) && top.Outputs[i+1].Kind == dispatch.MarkdownOutput && top.Outputs[i+1].Name == o.Name {
			md = top.Outputs[i+1].Text
		}
		switch {
		case strings.HasPrefix(o.Name, "conversation"):
			var c email.Conversation
			if err := json.Unmarshal(o.Value, &c); err != nil {
				t.Fatal(err)
			}
			name, ok := bySubject[c.Topic]
			if !ok {
				in.check(t, o.Name+"'s topic", "a truth item's subject", c.Topic)
				continue
			}
			matched[name] = true
			want := truth.Items[name]
			parts := append([]email.Address{want.Sender}, append(want.To, want.CC...)...)
			in.check(t, name+": class", want.Class, c.Class)
			in.check(t, name+": participants", addresses(parts), addresses(c.Participants))
			in.check(t, name+": start, end", want.SubmitTime.String()+", "+want.DeliveryTime.String(), c.Start.String()+", "+c.End.String())
			in.check(t, name+": missed", fmt.Sprint(want.Class == email.MissedClass), fmt.Sprint(c.Missed))
			in.check(t, name+": HTML as stored", "the truth's", same(c.HTML, want.HTML))
			in.check(t, name+": record's Markdown", "the truth's", same(c.Markdown, want.Markdown))
			in.check(t, name+": Markdown output", "the truth's", same(md, want.Markdown))
		case strings.HasPrefix(o.Name, "message"):
			var m struct {
				Class   string `json:"class"`
				Subject string `json:"subject"`
			}
			if err := json.Unmarshal(o.Value, &m); err != nil {
				t.Fatal(err)
			}
			name, ok := bySubject[m.Subject]
			if !ok {
				in.check(t, o.Name+"'s subject", "a truth item's subject", m.Subject)
				continue
			}
			matched[name] = true
			want := truth.Items[name]
			in.check(t, name+": a message, class", want.Class, m.Class)
			in.check(t, name+": Markdown output", "the truth's", same(md, want.Markdown))
		}
	}
	in.check(t, "items matched", fmt.Sprint(len(truth.Items)), fmt.Sprint(len(matched)))
}

// addresses writes addrs as "name <address>", comma-separated.
func addresses(addrs []email.Address) string {
	var out []string
	for _, a := range addrs {
		out = append(out, a.Name+" <"+a.Address+">")
	}
	return strings.Join(out, ", ")
}

// same names whether got is want.
func same(got, want string) string {
	if got == want {
		return "the truth's"
	}
	return fmt.Sprintf("%.120q", got)
}

// TestFindingsFormatsCost times the dispatcher over formatStages on each ~/docs input and the
// conversation store, each a Dispatch alone over a fresh file index in a process of its own, with
// its peak memory, its own and its children's, the tools it ran.
func TestFindingsFormatsCost(t *testing.T) {
	ctx := t.Context()
	docs, fromDocs := docsInputs(t)
	runs := runsFrom(t, formatsRunsEnv)
	paths := []string{filepath.Join("..", "email", "testdata", "mail", "fixtures", "generated", "pst", "conversations.pst")}
	for _, g := range docsGroups {
		paths = append(paths, fromDocs[g.group]...)
	}
	var results []measuredInput
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatal(err)
		}
		name := shown(docs, p)
		res := measureWays(t, name, abs, runs, []way{
			jobWay(ctx, "dispatch", dispatchJob{Inputs: []string{abs}, Formats: true}),
		})
		var took []time.Duration
		for _, rn := range res.Runs {
			s := rn.Survey.(*dispatchSurvey)
			took = append(took, s.Elapsed)
			if len(s.Failed) > 0 {
				t.Errorf("%s: failed parts %q", name, s.Failed)
			}
		}
		t.Logf("recorded: %s dispatch, %d runs: median dispatch %v (%s)", name, runs, median(took).Round(time.Millisecond),
			surveyLine(res.Runs[0].Survey.(*dispatchSurvey)))
		results = append(results, res)
	}
	saveResults(t, formatsResultsEnv, "formats-cost.json", results)
}
