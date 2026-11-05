//go:build linux

package onenote_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// onenoteCopy is the copy of onenote's fixtures and expectations, pinned by its MANIFEST.
var onenoteCopy = filepath.Join("testdata", "onenote")

// fixtures is the copy's fixtures directory: onenote's tests/fixtures, the public sets in
// interop/ and onenote-rs/, each with its LICENSE and sources.json, and the expectation files.
var fixtures = filepath.Join(onenoteCopy, "fixtures")

// manifestLine matches a file's line in the MANIFEST: its SHA-256, two spaces, and its path.
var manifestLine = regexp.MustCompile(`^([0-9a-f]{64})  (\S.*)$`)

// commitHash matches a full git commit hash.
var commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

// readManifest reads the copy's MANIFEST: its header, each key and its value, and the files it
// lists, each path and its SHA-256.
func readManifest(t *testing.T) (header, listed map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(onenoteCopy, "MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	header, listed = map[string]string{}, map[string]string{}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\n")
		if m := manifestLine.FindStringSubmatch(line); m != nil {
			listed[m[2]] = m[1]
		} else if key, value, ok := strings.Cut(line, " "); ok && key != "#" {
			header[key] = value
		}
	}
	return header, listed
}

// TestManifest: the copy of onenote's fixtures is exactly the files its MANIFEST lists, each with
// the SHA-256 listed, and the MANIFEST names the tag and the commit it was copied from. Each
// public set keeps its LICENSE and its sources.json.
func TestManifest(t *testing.T) {
	header, listed := readManifest(t)
	if header["tag"] == "" || !commitHash.MatchString(header["commit"]) {
		t.Errorf("manifest pins tag %q at commit %q, want a tag and a full commit hash", header["tag"], header["commit"])
	}
	found := map[string]string{}
	err := filepath.WalkDir(fixtures, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(onenoteCopy, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		found[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) == 0 {
		t.Fatal("the manifest lists no files")
	}
	for _, path := range slices.Sorted(maps.Keys(listed)) {
		if got, ok := found[path]; !ok {
			t.Errorf("%s: listed, not in the copy", path)
		} else if got != listed[path] {
			t.Errorf("%s: SHA-256 %s, the manifest's %s", path, got, listed[path])
		}
	}
	for _, path := range slices.Sorted(maps.Keys(found)) {
		if _, ok := listed[path]; !ok {
			t.Errorf("%s: in the copy, not listed", path)
		}
	}
	for _, set := range []string{"interop", "onenote-rs"} {
		for _, file := range []string{"LICENSE", "sources.json"} {
			if _, ok := listed["fixtures/"+set+"/"+file]; !ok {
				t.Errorf("%s has no %s", set, file)
			}
		}
	}
}

// TestRealVersion: the onenote the check's mise pins is the version whose tag the copy of its
// fixtures was taken from, so the expectations TestReal compares against are that onenote's.
func TestRealVersion(t *testing.T) {
	tl := realTool(t)
	header, _ := readManifest(t)
	if got, want := tl.Version().String(), strings.TrimPrefix(header["tag"], "v"); got != want {
		t.Errorf("onenote %s, the fixtures' tag %s: refresh them with mise run fixtures:onenote", got, header["tag"])
	}
}

// realTool builds the real onenote the check's mise pins, found on PATH through the exec runner.
func realTool(t *testing.T) *tool.Tool {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), onenote.OneNoteSpec())
	if err != nil {
		t.Fatalf("onenote, which mise pins, run the check through mise run check: %v", err)
	}
	return tl
}

// realReader builds a Reader over tl, the real onenote.
func realReader(t *testing.T, tl *tool.Tool) *onenote.Reader {
	t.Helper()
	r, err := onenote.NewReader(onenote.Config{OneNote: tl})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sectionTruth is one entry of sections.json: a section input's exit code and, for a refusal,
// its reason; else its section and its pages.
type sectionTruth struct {
	File    string `json:"file"`
	Code    int    `json:"code"`
	Reason  string `json:"reason"`
	Section struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Color       string `json:"color"`
	} `json:"section"`
	Pages []struct {
		ID               string         `json:"id"`
		Level            int            `json:"level"`
		Title            string         `json:"title"`
		CreationTime     time.Time      `json:"creation_time"`
		ModificationTime time.Time      `json:"modification_time"`
		Author           string         `json:"author"`
		Content          map[string]int `json:"content"`
	} `json:"pages"`
}

// notebookTruth is one entry of notebooks.json: a notebook input's exit code, its notebook, its
// entries in order, each section with its pages, and its warnings.
type notebookTruth struct {
	File     string `json:"file"`
	Code     int    `json:"code"`
	Notebook struct {
		Name  string `json:"name"`
		ID    string `json:"id"`
		Color string `json:"color"`
	} `json:"notebook"`
	Entries  []entryTruth `json:"entries"`
	Warnings []string     `json:"warnings"`
}

// entryTruth is a notebook's entry, a section line with its pages or a skipped line, with the
// fields notebooks.json gives, or a section group line, which none of its notebooks holds.
type entryTruth struct {
	Line        string      `json:"line"`
	ID          string      `json:"id,omitzero"`
	Path        []string    `json:"path,omitzero"`
	DisplayName string      `json:"display_name,omitzero"`
	Color       string      `json:"color,omitzero"`
	Kind        string      `json:"kind,omitzero"`
	Name        string      `json:"name,omitzero"`
	Reason      string      `json:"reason,omitzero"`
	Pages       []pageTruth `json:"pages,omitzero"`
}

// pageTruth is a notebook's page, as notebooks.json gives it.
type pageTruth struct {
	ID    string `json:"id"`
	Title string `json:"title,omitzero"`
}

// readTruth decodes the expectation file name, in the copy's fixtures, into v.
func readTruth(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// realInputs returns every section and notebook among the copied fixtures, each path relative to
// the fixtures directory, slash-separated, in order.
func realInputs(t *testing.T) []string {
	t.Helper()
	var inputs []string
	err := filepath.WalkDir(fixtures, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if ext := filepath.Ext(path); ext == ".one" || ext == ".onetoc2" {
			rel, err := filepath.Rel(fixtures, path)
			if err != nil {
				return err
			}
			inputs = append(inputs, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}

// TestReal runs the real pinned onenote over every section and notebook among the copied
// fixtures, each of which sections.json or notebooks.json expects. Each read yields one record
// per line onenote writes for the same input, of the line's kind, none unknown, each marshalling
// back to its line's fields; its exit code is the expectation's, a refusal an ExitError matching
// ErrRefused with the expected reason. A section's read is a section input's notebook, its
// section, and its pages, each with the expected id, level, title, times, author, and container
// counts; a notebook's is the expected notebook and entries, sections with their pages and
// skipped sections in order, with the expected warnings, their prefix taken off. Between them
// the fixtures give notebook, section, page, and skipped records and warnings.
func TestReal(t *testing.T) {
	tl := realTool(t)
	r := realReader(t, tl)
	var sections []sectionTruth
	var notebooks []notebookTruth
	readTruth(t, "sections.json", &sections)
	readTruth(t, "notebooks.json", &notebooks)
	sectionsBy := map[string]sectionTruth{}
	for _, s := range sections {
		sectionsBy[s.File] = s
	}
	notebooksBy := map[string]notebookTruth{}
	for _, n := range notebooks {
		notebooksBy[n.File] = n
	}

	inputs := realInputs(t)
	if want := len(sections) + len(notebooks); len(inputs) != want {
		t.Errorf("%d inputs, the expectations %d", len(inputs), want)
	}
	kinds := map[string]bool{}
	warned := false
	for _, rel := range inputs {
		t.Run(rel, func(t *testing.T) {
			input := filepath.Join(fixtures, rel)
			s := mustRead(t)(r.Read(t.Context(), onenote.Request{Input: input}))
			recs, err := collect(s, nil)
			code := 0
			if exit, ok := errors.AsType[*onenote.ExitError](err); ok {
				code = exit.Code
			} else if err != nil {
				t.Fatal(err)
			}
			lines, cliCode := cliLines(t, tl, input)
			if code != cliCode {
				t.Errorf("exit code %d, onenote's own %d", code, cliCode)
			}
			checkLines(t, recs, lines)
			for _, rec := range recs {
				kinds[rec.Line()] = true
			}
			if len(s.Warnings()) > 0 {
				warned = true
			}
			if want, ok := sectionsBy[rel]; ok {
				checkSection(t, want, recs, err)
			} else if want, ok := notebooksBy[rel]; ok {
				checkNotebook(t, want, recs, err, s.Warnings())
			} else {
				t.Errorf("no expectation for %s", rel)
			}
		})
	}
	for _, kind := range []string{"notebook", "section", "page", "skipped"} {
		if !kinds[kind] {
			t.Errorf("no fixture gives a %s record", kind)
		}
	}
	if !warned {
		t.Error("no fixture gives a warning")
	}
}

// cliLines runs tl, onenote, on input itself, outside the package, and returns each line it
// writes to stdout, decoded, and its exit code.
func cliLines(t *testing.T, tl *tool.Tool, input string) ([]map[string]any, int) {
	t.Helper()
	var stdout bytes.Buffer
	code := 0
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{input}, Stdout: &stdout})
	if exit, ok := errors.AsType[*tool.ExitError](err); ok {
		code = exit.Code
	} else if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	for line := range strings.Lines(stdout.String()) {
		var m map[string]any
		if err := jsonv2.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("onenote's line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines, code
}

// checkLines checks recs, a read's records, against want, onenote's own lines for the same
// input: there is one record per line, in order and of the line's kind, none of an unknown kind,
// and each marshals back to its line's fields, less "line".
func checkLines(t *testing.T, recs []onenote.Record, want []map[string]any) {
	t.Helper()
	if len(recs) != len(want) {
		t.Fatalf("%d records, onenote's %d lines", len(recs), len(want))
	}
	for i, rec := range recs {
		if _, ok := rec.(*onenote.UnknownRecord); ok {
			t.Errorf("record %d: unknown kind %s", i, rec.Line())
		}
		line := maps.Clone(want[i])
		if kind := line["line"]; kind != rec.Line() {
			t.Errorf("record %d: kind %s, onenote's %v", i, rec.Line(), kind)
		}
		delete(line, "line")
		if got := marshalled(t, rec); !reflect.DeepEqual(got, line) {
			t.Errorf("record %d marshals to\n%v\nonenote's line is\n%v", i, got, line)
		}
	}
}

// marshalled returns rec marshalled through encoding/json/v2 and decoded back as a JSON object.
func marshalled(t *testing.T, rec onenote.Record) map[string]any {
	t.Helper()
	data, err := jsonv2.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := jsonv2.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// checkSection checks a section input's read, its records and its error, against want.
func checkSection(t *testing.T, want sectionTruth, recs []onenote.Record, err error) {
	t.Helper()
	if want.Code != 0 {
		exit, ok := errors.AsType[*onenote.ExitError](err)
		if !ok || exit.Code != want.Code || !errors.Is(err, onenote.ErrRefused) || exit.Message != want.Reason {
			t.Errorf("got %v, want exit code %d refusing with %q", err, want.Code, want.Reason)
		}
		return
	}
	if len(recs) != 2+len(want.Pages) {
		t.Fatalf("%d records, want a notebook, a section, and %d pages", len(recs), len(want.Pages))
	}
	nb, ok := recs[0].(*onenote.Notebook)
	if stem := strings.TrimSuffix(filepath.Base(want.File), ".one"); !ok || nb.Input != "section" || nb.Name != stem {
		t.Errorf("notebook %+v, want a section input named %q", recs[0], stem)
	}
	sec, ok := recs[1].(*onenote.Section)
	if !ok || sec.ID != want.Section.ID || sec.DisplayName != want.Section.DisplayName || sec.Color != want.Section.Color {
		t.Errorf("section %+v, want %+v", recs[1], want.Section)
	}
	for i, wp := range want.Pages {
		p, ok := recs[2+i].(*onenote.Page)
		if !ok {
			t.Errorf("record %d %+v, want a page", 2+i, recs[2+i])
			continue
		}
		if p.ID != wp.ID || p.Section != want.Section.ID || p.Level != wp.Level || p.Title != wp.Title ||
			!p.CreationTime.Equal(wp.CreationTime) || !p.ModificationTime.Equal(wp.ModificationTime) || p.Author != wp.Author {
			t.Errorf("page %d %+v, want %+v", i, p, wp)
		}
		if got := containerCounts(t, p); !maps.Equal(got, wp.Content) {
			t.Errorf("page %d containers %v, want %v", i, got, wp.Content)
		}
	}
}

// containerCounts counts p's containers by kind, from its Content.
func containerCounts(t *testing.T, p *onenote.Page) map[string]int {
	t.Helper()
	var containers []struct {
		Kind string `json:"kind"`
	}
	if err := jsonv2.Unmarshal(p.Content, &containers); err != nil {
		t.Fatalf("page %s content: %v", p.ID, err)
	}
	counts := map[string]int{}
	for _, c := range containers {
		counts[c.Kind]++
	}
	return counts
}

// checkNotebook checks a notebook input's read, its records, its error, and its warnings,
// against want.
func checkNotebook(t *testing.T, want notebookTruth, recs []onenote.Record, err error, warnings []string) {
	t.Helper()
	if err != nil || want.Code != 0 {
		t.Fatalf("got %v, want exit code %d", err, want.Code)
	}
	nb, ok := recs[0].(*onenote.Notebook)
	if !ok || nb.Input != "notebook" || nb.Name != want.Notebook.Name || nb.ID != want.Notebook.ID || nb.Color != want.Notebook.Color {
		t.Errorf("notebook %+v, want %+v", recs[0], want.Notebook)
	}
	var entries []entryTruth
	for _, rec := range recs[1:] {
		switch rec := rec.(type) {
		case *onenote.Section:
			// notebooks.json gives a section's id, path, and colour, not its display name.
			entries = append(entries, entryTruth{Line: "section", ID: rec.ID, Path: rec.Path, Color: rec.Color})
		case *onenote.SectionGroup:
			entries = append(entries, entryTruth{Line: "section_group", ID: rec.ID, Path: rec.Path, DisplayName: rec.DisplayName})
		case *onenote.Page:
			if len(entries) == 0 || entries[len(entries)-1].Line != "section" || entries[len(entries)-1].ID != rec.Section {
				t.Errorf("page %s doesn't follow its section %s", rec.ID, rec.Section)
				continue
			}
			last := &entries[len(entries)-1]
			last.Pages = append(last.Pages, pageTruth{ID: rec.ID, Title: rec.Title})
		case *onenote.Skipped:
			entries = append(entries, entryTruth{Line: "skipped", Kind: rec.Kind, Name: rec.Name, Reason: rec.Reason})
		default:
			t.Errorf("record %+v in a notebook's entries", rec)
		}
	}
	// An empty list in notebooks.json decodes to an empty slice, where entries is nil.
	if (len(entries) > 0 || len(want.Entries) > 0) && !reflect.DeepEqual(entries, want.Entries) {
		t.Errorf("entries\n%+v\nwant\n%+v", entries, want.Entries)
	}
	if !slices.Equal(warnings, want.Warnings) {
		t.Errorf("warnings %q, want %q", warnings, want.Warnings)
	}
}

// contentTruth is content.json: hand-written content fields, each a JSON pointer into a page
// line and its value, null for a field the page must not have.
type contentTruth struct {
	Pages []struct {
		Files  []string `json:"files"`
		Page   string   `json:"page"`
		Fields []struct {
			Kind    string `json:"kind"`
			Pointer string `json:"pointer"`
			Raw     any    `json:"value"`
		} `json:"fields"`
	} `json:"pages"`
}

// TestRealContent: every content field content.json expects is in the page the real onenote's
// read yields for each of its files, at its pointer into the page marshalled back, with its
// value, and every field it expects absent is absent: the content a Page keeps as JSON is
// onenote's whole.
func TestRealContent(t *testing.T) {
	r := realReader(t, realTool(t))
	var truth contentTruth
	readTruth(t, "content.json", &truth)
	if len(truth.Pages) == 0 {
		t.Fatal("content.json expects no pages")
	}
	fields := 0
	for _, want := range truth.Pages {
		for _, file := range want.Files {
			recs, err := collect(r.Read(t.Context(), onenote.Request{Input: filepath.Join(fixtures, file)}))
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			var page map[string]any
			for _, rec := range recs {
				if p, ok := rec.(*onenote.Page); ok && p.ID == want.Page {
					page = marshalled(t, p)
				}
			}
			if page == nil {
				t.Errorf("%s: no page %s", file, want.Page)
				continue
			}
			for _, f := range want.Fields {
				fields++
				got, found := pointer(page, f.Pointer)
				switch {
				case f.Raw == nil && found:
					t.Errorf("%s, page %s, %s: %v, want none", file, want.Page, f.Pointer, got)
				case f.Raw != nil && (!found || !reflect.DeepEqual(got, f.Raw)):
					t.Errorf("%s, page %s, %s: %v (found %t), want %v", file, want.Page, f.Pointer, got, found, f.Raw)
				}
			}
		}
	}
	if fields == 0 {
		t.Error("no content field checked")
	}
}

// pointer resolves the JSON pointer p, RFC 6901's, in v, a decoded JSON value, reporting whether
// it names a value.
func pointer(v any, p string) (any, bool) {
	if p == "" {
		return v, true
	}
	for token := range strings.SplitSeq(strings.TrimPrefix(p, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			v = node[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// TestRealMedia: with a media directory, the real onenote writes a page's ink drawing there,
// under the name its Ink record gives.
func TestRealMedia(t *testing.T) {
	r := realReader(t, realTool(t))
	media := t.TempDir()
	recs, err := collect(r.Read(t.Context(), onenote.Request{
		Input: filepath.Join(fixtures, "onenote-rs", "handwriting_recognition.one"),
		Media: media,
	}))
	if err != nil {
		t.Fatal(err)
	}
	inked := 0
	for _, rec := range recs {
		p, ok := rec.(*onenote.Page)
		if !ok || p.Ink == nil {
			continue
		}
		inked++
		if p.Ink.Strokes == 0 || len(p.InkText) == 0 {
			t.Errorf("page %s ink %+v, ink text %v; want strokes and recognised text", p.ID, p.Ink, p.InkText)
		}
		data, err := os.ReadFile(filepath.Join(media, p.Ink.File))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if want := hex.EncodeToString(sum[:]) + ".svg"; p.Ink.File != want {
			t.Errorf("ink file %s, its content's name %s", p.Ink.File, want)
		}
	}
	if inked == 0 {
		t.Error("no page with ink")
	}
}
