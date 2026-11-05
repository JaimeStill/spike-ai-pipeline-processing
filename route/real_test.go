package route_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// testdataRoutes are the routes of the repository's committed testdata files, by extension; a
// file of an extension not listed isn't checked.
var testdataRoutes = map[string]route.Route{
	".pdf":  route.Document,
	".docx": route.Office, ".xlsx": route.Office, ".pptx": route.Office,
	".doc": route.Office, ".xls": route.Office, ".ppt": route.Office,
	".wav":  route.Audio,
	".svg":  route.VisionConverted,
	".json": route.Text, ".md": route.Text, ".txt": route.Text,
	".sh": route.Unrouted, ".typ": route.Unrouted,
}

// TestFileTestdata routes the repository's own testdata, the office, document, picture, and tool
// packages' fixtures: each file is routed as its extension says it should be, a script by its
// "#!" line.
func TestFileTestdata(t *testing.T) {
	n := 0
	for _, dir := range []string{"../office/testdata", "../document/testdata", "../picture/testdata", "../tool/testdata"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			want, ok := testdataRoutes[filepath.Ext(path)]
			if !ok {
				return nil
			}
			n++
			got, why, err := route.File(path, d.Name(), "")
			if err != nil {
				return err
			}
			if got != want || (filepath.Ext(path) == ".sh" && !strings.Contains(why, "executable")) {
				t.Errorf("%s: route %q (%s), want %q", path, got, why, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n == 0 {
		t.Error("no testdata routed")
	}
}

// docsRoutes are the routes of ~/docs' files by the format its manifest records; a file of a
// format not listed, such as text/plain, which names licences, transcripts, and messages alike,
// isn't checked.
var docsRoutes = map[string]route.Route{
	"application/pdf": route.Document,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   route.Office,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": route.Office,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         route.Office,
	"message/rfc822":                                  route.Nested,
	"application/msword":                              route.Office,
	"application/vnd.ms-excel":                        route.Office,
	"application/vnd.ms-powerpoint":                   route.Office,
	"application/vnd.oasis.opendocument.text":         route.Office,
	"application/vnd.oasis.opendocument.spreadsheet":  route.Office,
	"application/vnd.oasis.opendocument.presentation": route.Office,
	"application/vnd.oasis.opendocument.graphics":     route.Office,
	"text/rtf":                    route.Office,
	"image/png":                   route.Vision,
	"image/jpeg":                  route.Vision,
	"image/gif":                   route.Vision,
	"image/bmp":                   route.Vision,
	"image/tiff":                  route.VisionConverted,
	"image/wmf":                   route.VisionConverted,
	"image/svg+xml":               route.VisionConverted,
	"image/heif":                  route.VisionConverted,
	"image/webp":                  route.VisionConverted,
	"image/avif":                  route.VisionConverted,
	"application/vnd.ms-outlook":  route.Nested,
	"application/zip":             route.Archive,
	"application/epub+zip":        route.Archive,
	"application/gzip":            route.Archive,
	"application/zstd":            route.Archive,
	"application/x-7z-compressed": route.Archive,
	"audio/x-wav":                 route.Audio,
	"audio/flac":                  route.Audio,
	"audio/mpeg":                  route.Audio,
	"audio/ogg":                   route.Audio,
	"audio/x-m4a":                 route.Audio,
	"video/mp4":                   route.Video,
	"video/webm":                  route.Video,
	"video/x-matroska":            route.Video,
	"video/x-msvideo":             route.Video,
	"application/json":            route.Text,
	"application/x-ndjson":        route.Text,
	"application/x-subrip":        route.Text,
	"application/xhtml+xml":       route.Unrouted,
	"text/csv":                    route.Text,
	"text/xml":                    route.Text,
	"text/x-shellscript":          route.Unrouted,
	"application/vnd.wordperfect": route.Unrouted,
}

// docsFile is one of ~/docs' manifest's files: its path under ~/docs and its format.
type docsFile struct {
	Path   string `json:"path"`
	Format string `json:"format"`
}

// TestFileDocs routes ~/docs' corpus, read only, when this machine has it: each file whose
// format docsRoutes lists is routed so, but for a .pst, which is no OLE2 file, and an .xlsb, a
// ZIP office doesn't read, which is an archive. A machine without ~/docs skips it.
func TestFileDocs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	root := filepath.Join(home, "docs")
	data, err := os.ReadFile(filepath.Join(root, "MANIFEST.json"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("no ~/docs on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files []docsFile `json:"files"`
	}
	if err := jsonv2.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range manifest.Files {
		want, ok := docsRoutes[f.Format]
		if !ok {
			continue
		}
		switch filepath.Ext(f.Path) {
		case ".pst":
			want = route.Unrouted
		case ".xlsb":
			want = route.Archive
		}
		n++
		got, why, err := route.File(filepath.Join(root, f.Path), filepath.Base(f.Path), f.Format)
		if err != nil {
			t.Errorf("%s: %v", f.Path, err)
			continue
		}
		if got != want {
			t.Errorf("%s (%s): route %q (%s), want %q", f.Path, f.Format, got, why, want)
		}
	}
	t.Logf("routed %d of ~/docs' %d files", n, len(manifest.Files))
}
