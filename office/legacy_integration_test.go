//go:build integration && linux

package office_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// legacyTargets are the OOXML formats the legacy fixtures' extensions convert to.
var legacyTargets = map[string]string{".doc": "docx", ".xls": "xlsx", ".ppt": "pptx"}

// containerConverter builds a Converter over the real office the mise pins, through the exec
// runner, and soffice in the default image, LibreOfficeImage, through the container runner,
// which pulls the image when it isn't present.
func containerConverter(t *testing.T) *office.Converter {
	t.Helper()
	o, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatalf("office, which mise pins, run the tier through mise run integration: %v", err)
	}
	lo, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: picture.LibreOfficeImage}, picture.LibreOfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("soffice %s in %s", lo.Version(), picture.LibreOfficeImage)
	c, err := office.NewConverter(office.Config{Office: o, LibreOffice: lo})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestLegacyRoundTrip converts every legacy fixture through soffice in the default image, then
// office, twice each, and asserts only that each conversion succeeds as a JSON model of the
// OOXML format the legacy one converts to, which ConvertedFrom names. It records, in its log,
// the image's footprint, each conversion's wall time, first run and repeat, and what the round
// trip loses per element kind: for a generated fixture against office's model of its OOXML
// original in testdata/ooxml, for a real one against its hand-written truth.
func TestLegacyRoundTrip(t *testing.T) {
	c := containerConverter(t)
	logFootprint(t)

	generated, err := filepath.Glob(filepath.Join("testdata", "legacy", "generated", "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	generated = slices.DeleteFunc(generated, func(p string) bool { return legacyTargets[filepath.Ext(p)] == "" })
	byFormat := map[string]map[string]*tally{}
	for _, path := range generated {
		t.Run(filepath.Base(path), func(t *testing.T) {
			res := convertLegacy(t, c, path)
			original := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + "." + res.Document.Format
			want := elements(convertFixture(t, c, original, office.JSON).Document)
			got := elements(res.Document)
			format := strings.TrimPrefix(filepath.Ext(path), ".")
			if byFormat[format] == nil {
				byFormat[format] = map[string]*tally{}
			}
			logLoss(t, filepath.Base(path), "original", want, got, byFormat[format])
		})
	}
	for _, format := range slices.Sorted(maps.Keys(byFormat)) {
		for _, kind := range slices.Sorted(maps.Keys(byFormat[format])) {
			t.Logf("loss: %-5s %-12s %s", format, kind, byFormat[format][kind])
		}
	}

	truths, err := filepath.Glob(filepath.Join("testdata", "legacy", "real", "*.truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range truths {
		var truth struct {
			File     string              `json:"file"`
			Elements map[string][]string `json:"elements"`
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &truth); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		t.Run(truth.File, func(t *testing.T) {
			res := convertLegacy(t, c, filepath.Join("testdata", "legacy", "real", truth.File))
			want := map[string][]string{}
			for kind, keys := range truth.Elements {
				for _, k := range keys {
					want[kind] = append(want[kind], normalize(k))
				}
			}
			logLoss(t, truth.File, "truth", want, elements(res.Document), nil)
		})
	}
}

// convertLegacy converts the legacy fixture at path as JSON, in a working directory of its own,
// twice, logging each run's wall time, and fails unless both succeed with a model of the OOXML
// format its extension converts to. It returns the first result.
func convertLegacy(t *testing.T, c *office.Converter, path string) *office.Result {
	t.Helper()
	ext := filepath.Ext(path)
	var first *office.Result
	var times []time.Duration
	for range 2 {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		res, err := c.Convert(t.Context(), f, office.Request{Dir: t.TempDir(), Name: filepath.Base(path)})
		times = append(times, time.Since(start))
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.Document == nil || res.Document.Format != legacyTargets[ext] || res.ConvertedFrom != strings.TrimPrefix(ext, ".") {
			t.Fatalf("result: document %v, converted from %q; want a %s model from %s", res.Document, res.ConvertedFrom, legacyTargets[ext], ext)
		}
		for _, w := range res.Warnings {
			t.Logf("warning: %s", w)
		}
		if first == nil {
			first = res
		}
	}
	t.Logf("footprint: %-34s first %5d ms  repeat %5d ms", filepath.Base(path), times[0].Milliseconds(), times[1].Milliseconds())
	return first
}

// logFootprint logs the default image's size: compressed, the sum of its layers for this
// machine's architecture as the registry's manifest gives them, and on disk, as docker image
// inspect gives it. Neither is asserted; a size docker can't give is logged as unknown.
func logFootprint(t *testing.T) {
	t.Helper()
	ref := picture.LibreOfficeImage
	disk, err := exec.CommandContext(t.Context(), "docker", "image", "inspect", "--format={{.Size}}", ref).Output()
	if err != nil {
		t.Logf("footprint: on disk unknown: %v", err)
	} else {
		t.Logf("footprint: image on disk %s bytes", strings.TrimSpace(string(disk)))
	}
	out, err := exec.CommandContext(t.Context(), "docker", "manifest", "inspect", "--verbose", ref).Output()
	if err != nil {
		t.Logf("footprint: compressed unknown: %v", err)
		return
	}
	var manifests []struct {
		Descriptor struct {
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"Descriptor"`
		Docker *layered `json:"SchemaV2Manifest"`
		OCI    *layered `json:"OCIManifest"`
	}
	if err := json.Unmarshal(out, &manifests); err != nil {
		t.Logf("footprint: compressed unknown: %v", err)
		return
	}
	for _, m := range manifests {
		p := m.Descriptor.Platform
		if p.OS != "linux" || p.Architecture != runtime.GOARCH {
			continue
		}
		layers := m.Docker
		if layers == nil {
			layers = m.OCI
		}
		if layers == nil {
			break
		}
		var size int64
		for _, l := range layers.Layers {
			size += l.Size
		}
		t.Logf("footprint: image compressed %d bytes (linux/%s, %d layers)", size, p.Architecture, len(layers.Layers))
		return
	}
	t.Logf("footprint: compressed unknown: no linux/%s manifest", runtime.GOARCH)
}

// layered is an image manifest's layers, as docker manifest inspect --verbose gives them.
type layered struct {
	Layers []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
}

// tally is one element kind's loss: the reference's elements, the model's, and those they share,
// counted with multiplicity.
type tally struct{ want, got, kept int }

func (t *tally) String() string {
	return fmt.Sprintf("reference %4d  model %4d  kept %4d  lost %4d  added %4d", t.want, t.got, t.kept, t.want-t.kept, t.got-t.kept)
}

// logLoss logs, for each element kind in want or got, how many of want's elements got keeps, by
// their keys counted with multiplicity, then each element lost and each added, and adds each
// kind's counts to sum when it isn't nil. ref names the reference in the log.
func logLoss(t *testing.T, file, ref string, want, got map[string][]string, sum map[string]*tally) {
	t.Helper()
	kinds := map[string]bool{}
	for k := range want {
		kinds[k] = true
	}
	for k := range got {
		kinds[k] = true
	}
	for _, kind := range slices.Sorted(maps.Keys(kinds)) {
		n := tally{want: len(want[kind]), got: len(got[kind])}
		left := map[string]int{}
		for _, k := range got[kind] {
			left[k]++
		}
		var lost []string
		for _, k := range want[kind] {
			if left[k] > 0 {
				left[k]--
				n.kept++
			} else {
				lost = append(lost, k)
			}
		}
		t.Logf("loss: %-34s %-12s %s against the %s", file, kind, &n, ref)
		for _, k := range lost {
			t.Logf("loss:   lost %s %q", kind, k)
		}
		for _, k := range got[kind] {
			if left[k] > 0 {
				left[k]--
				t.Logf("loss:   added %s %q", kind, k)
			}
		}
		if sum != nil {
			if sum[kind] == nil {
				sum[kind] = &tally{}
			}
			sum[kind].want += n.want
			sum[kind].got += n.got
			sum[kind].kept += n.kept
		}
	}
}

// elements returns doc's elements by kind, each as a key that a hand-written truth can give,
// its whitespace collapsed: a heading as its level and text, "1:Title"; a paragraph as its text,
// empty ones left out; a list as ordered or unordered and its item count, "unordered:3"; a table
// as its rows and widest row's cells, "2x3", or "sheet" for a sheet's; a sheet cell as its sheet,
// reference, and value, "Sheet1!A1=text", and its formula as a "formula" too; a chart as its type,
// title, and series count, and each series as its name and point count; a picture as its content
// type, or "linked"; a sheet as its name; a slide as its number; a header, footer, footnote,
// endnote, or comment as its text; and each footnote, endnote, and comment reference.
func elements(doc *office.Document) map[string][]string {
	out := map[string][]string{}
	add := func(kind, key string) { out[kind] = append(out[kind], normalize(key)) }
	var sheet string
	walkDocument(doc, func(b office.Block) {
		switch b := b.(type) {
		case *office.Heading:
			add("heading", strconv.Itoa(b.Level)+":"+inlineText(b.Inlines))
		case *office.Paragraph:
			if text := inlineText(b.Inlines); strings.TrimSpace(text) != "" {
				add("paragraph", text)
			}
		case *office.List:
			kind := "unordered"
			if b.Ordered {
				kind = "ordered"
			}
			add("list", kind+":"+strconv.Itoa(len(b.Items)))
		case *office.Table:
			if sheet != "" && len(b.Rows) > 0 && b.Rows[0].Row > 0 {
				add("table", "sheet")
				for _, row := range b.Rows {
					for _, cell := range row.Cells {
						if cell.Value != nil {
							add("cell", sheet+"!"+cell.Ref+"="+cellValue(cell.Value))
						}
						if cell.Formula != "" {
							add("formula", sheet+"!"+cell.Ref+"="+cell.Formula)
						}
					}
				}
				return
			}
			width := 0
			for _, row := range b.Rows {
				width = max(width, len(row.Cells))
			}
			add("table", fmt.Sprintf("%dx%d", len(b.Rows), width))
		case *office.Chart:
			add("chart", fmt.Sprintf("%s|%s|%d", b.ChartType, b.Title, len(b.Series)))
			for _, s := range b.Series {
				add("series", fmt.Sprintf("%s|%d", s.Name, len(s.Values)))
			}
		case *office.Media:
			if b.Name == "" {
				add("media", "linked")
			} else {
				add("media", b.ContentType)
			}
		case *office.Sheet:
			sheet = b.Name
			add("sheet", b.Name)
		case *office.Slide:
			add("slide", strconv.Itoa(b.Number))
		}
	}, func(in office.Inline) {
		switch in.(type) {
		case *office.FootnoteRef:
			add("footnote-ref", "ref")
		case *office.EndnoteRef:
			add("endnote-ref", "ref")
		case *office.CommentRef:
			add("comment-ref", "ref")
		}
	})
	for _, s := range doc.Sections {
		for _, h := range s.Headers {
			add("header", h.Kind+":"+blocksText(h.Blocks))
		}
		for _, f := range s.Footers {
			add("footer", f.Kind+":"+blocksText(f.Blocks))
		}
	}
	for _, n := range doc.Footnotes {
		add("footnote", blocksText(n.Blocks))
	}
	for _, n := range doc.Endnotes {
		add("endnote", blocksText(n.Blocks))
	}
	for _, cm := range doc.Comments {
		add("comment", cm.Author+":"+blocksText(cm.Blocks))
	}
	return out
}

// inlineText returns the text of ins, its Text inlines' joined.
func inlineText(ins []office.Inline) string {
	var b strings.Builder
	for _, in := range ins {
		if tx, ok := in.(*office.Text); ok {
			b.WriteString(tx.Text)
		}
	}
	return b.String()
}

// blocksText returns the text of blocks' headings and paragraphs, nested ones included, joined
// by spaces.
func blocksText(blocks []office.Block) string {
	var parts []string
	walkDocument(&office.Document{Blocks: blocks}, func(b office.Block) {
		switch b := b.(type) {
		case *office.Heading:
			parts = append(parts, inlineText(b.Inlines))
		case *office.Paragraph:
			parts = append(parts, inlineText(b.Inlines))
		}
	}, func(office.Inline) {})
	return strings.Join(parts, " ")
}

// cellValue returns a sheet cell's value as text: a number in its shortest form, a boolean as
// TRUE or FALSE, and text, an error, or a date as given.
func cellValue(v any) string {
	switch v := v.(type) {
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return strings.ToUpper(strconv.FormatBool(v))
	case string:
		return v
	}
	return fmt.Sprint(v)
}

// normalize collapses a key's runs of whitespace to single spaces and trims it.
func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
