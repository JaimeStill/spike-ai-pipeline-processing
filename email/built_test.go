//go:build linux

package email_test

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
)

// builtDir is the built .msg's directory: its .eml source, the generator's layout and manifest,
// the .msg, and its MANIFEST, which scripts/fixtures-built.sh writes. It isn't part of the copy of
// mail's fixtures.
var builtDir = filepath.Join("testdata", "built")

// builtMsg is the built .msg, under builtDir.
const builtMsg = "msg/office-attachments.msg"

// builtSource is one of the built .msg's attachments as the source attaches it: its filename,
// the route its content takes, and where its bytes came from, a committed fixture, by its path
// from the repository's root, and the member of that fixture's package, or "-" for the fixture
// itself.
type builtSource struct {
	filename string
	route    email.Route
	fixture  string
	member   string
}

// builtSources are the built .msg's attachments, in the order its source attaches them, written
// by hand from the brief: processing's own committed office fixtures, two whole and two pictures
// taken from packages.
var builtSources = []builtSource{
	{"minutes.docx", email.RouteOffice, "office/testdata/ooxml/tables.libreoffice.docx", "-"},
	{"budget.xls", email.RouteOffice, "office/testdata/legacy/generated/formulas.libreoffice.xls", "-"},
	{"scan.tif", email.RouteVisionConverted, "office/testdata/pictures/tiff.docx", "word/media/image1.tiff"},
	{"diagram.emf", email.RouteVisionConverted, "office/testdata/ooxml/word-with-attachments.docx", "word/media/image4.emf"},
}

// builtManifest is the built directory's MANIFEST: its header, each key and its value; its
// attachments, each line's fields after the key; and its files, each path and its SHA-256.
type builtManifest struct {
	header      map[string]string
	attachments [][]string
	files       map[string]string
}

// readBuiltManifest reads the built directory's MANIFEST.
func readBuiltManifest(t *testing.T) builtManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(builtDir, "MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	m := builtManifest{header: map[string]string{}, files: map[string]string{}}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\n")
		if f := manifestLine.FindStringSubmatch(line); f != nil {
			m.files[f[2]] = f[1]
		} else if key, value, ok := strings.Cut(line, " "); ok && key == "attachment" {
			m.attachments = append(m.attachments, strings.Fields(value))
		} else if ok && key != "#" {
			m.header[key] = value
		}
	}
	return m
}

// sourceContent returns the bytes of an attachment's source: its fixture's, read from the
// repository's root, or its member's within the fixture's package.
func sourceContent(t *testing.T, s builtSource) []byte {
	t.Helper()
	path := filepath.Join("..", filepath.FromSlash(s.fixture))
	if s.member == "-" {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	f, err := z.Open(s.member)
	if err != nil {
		t.Fatalf("%s: %v", s.fixture, err)
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// sha256Hex returns the lowercase hex SHA-256 of content.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// TestBuiltManifest: the built directory is exactly the files its MANIFEST lists, each with the
// SHA-256 listed. The MANIFEST names the mail tag the copy of mail's fixtures is taken from, a
// full commit, and the generator's tool versions, the ones the generator's own manifest.json
// records, which also records the .msg's SHA-256 and its source's as listed. Its attachments are
// builtSources, each with its source's SHA-256, read from the fixture now.
func TestBuiltManifest(t *testing.T) {
	m := readBuiltManifest(t)
	mailHeader, _ := readManifest(t)
	if m.header["tag"] != mailHeader["tag"] || !commitHash.MatchString(m.header["commit"]) {
		t.Errorf("manifest pins tag %q at commit %q, want mail's copy's tag %q and a full commit hash",
			m.header["tag"], m.header["commit"], mailHeader["tag"])
	}

	found := map[string]string{}
	err := filepath.WalkDir(builtDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "MANIFEST" {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(builtDir, path)
		if err != nil {
			return err
		}
		found[filepath.ToSlash(rel)] = sha256Hex(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(found, m.files) {
		for _, path := range slices.Sorted(maps.Keys(m.files)) {
			if found[path] != m.files[path] {
				t.Errorf("%s: SHA-256 %q, the manifest's %s", path, found[path], m.files[path])
			}
		}
		for _, path := range slices.Sorted(maps.Keys(found)) {
			if _, ok := m.files[path]; !ok {
				t.Errorf("%s: in the directory, not listed", path)
			}
		}
	}

	data, err := os.ReadFile(filepath.Join(builtDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		File   string `json:"file"`
		Sha256 string `json:"sha256"`
	}
	var generated struct {
		Tools   map[string]string `json:"tools"`
		Sources []entry           `json:"sources"`
		Outputs []entry           `json:"outputs"`
	}
	if err := jsonv2.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"dotnet-sdk", "MsgKit", "MimeKit"} {
		if got := m.header[tool]; got == "" || got != generated.Tools[tool] {
			t.Errorf("%s %q, the generator's manifest's %q", tool, got, generated.Tools[tool])
		}
	}
	for _, e := range slices.Concat(generated.Sources, generated.Outputs) {
		if e.Sha256 != m.files[e.File] {
			t.Errorf("%s: the generator's manifest has SHA-256 %s, MANIFEST %s", e.File, e.Sha256, m.files[e.File])
		}
	}
	if !slices.ContainsFunc(generated.Outputs, func(e entry) bool { return e.File == builtMsg }) {
		t.Errorf("the generator's manifest doesn't list %s", builtMsg)
	}

	if len(m.attachments) != len(builtSources) {
		t.Fatalf("%d attachments listed, want %d", len(m.attachments), len(builtSources))
	}
	for i, s := range builtSources {
		want := []string{s.filename, sha256Hex(sourceContent(t, s)), s.fixture, s.member}
		if got := m.attachments[i]; !slices.Equal(got, want) {
			t.Errorf("attachment %d: %q, want %q", i, got, want)
		}
	}
}

// TestBuiltRoutes runs the real pinned mail over the built .msg with an attachments directory.
// Its one message holds builtSources' attachments, in order, each routed as they say and written
// under the SHA-256 name mail gives it, its content byte for byte its source's: the docx and the
// legacy xls go to office, the TIFF and the EMF to vision through office's conversion.
func TestBuiltRoutes(t *testing.T) {
	r := realReader(t)
	dir := t.TempDir()
	recs, err := collect(r.Read(t.Context(), email.Request{Input: filepath.Join(builtDir, builtMsg), Attachments: dir}))
	if err != nil {
		t.Fatal(err)
	}
	all := attachmentsOf(recs)
	if len(all) != len(builtSources) {
		t.Fatalf("%d attachments, want %d", len(all), len(builtSources))
	}
	for i, s := range builtSources {
		a := all[i]
		t.Run(s.filename, func(t *testing.T) {
			if a.Filename != s.filename || a.Method != "by_value" {
				t.Fatalf("attachment %d: %q, method %q; want %q by value", i, a.Filename, a.Method, s.filename)
			}
			checkWritten(t, dir, a)
			content, err := os.ReadFile(a.Path)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := sha256Hex(content), sha256Hex(sourceContent(t, s)); got != want {
				t.Errorf("content SHA-256 %s, its source's %s", got, want)
			}
			route, why, err := a.Route()
			if err != nil {
				t.Fatal(err)
			}
			if route != s.route {
				t.Errorf("route %q (%s), want %q", route, why, s.route)
			}
			t.Logf("%s: %s (%s), stored as %s", a.Filename, route, why, a.MimeType)
		})
	}
}
