package inspect_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// docsFlags are the checks each of ~/docs' files yields, by its path under ~/docs, sorted; every
// file not listed yields none. Most are the corpus's hazard inputs, by the manifest's covers:
// security, javascript, macro, encrypted, and embedded-files. The rest are files whose content
// carries what a check reads: four PDFs that name an /OpenAction, as pdfid flags one whatever it
// does, generated docx files with a linked picture, real-world ones with an OLE object and a
// linked picture, and the generator script beside the scanned PDFs.
var docsFlags = map[string][]inspect.Check{
	"archive/password-protected.zip":                       {inspect.ZIPEncrypted},
	"image/vector/script-bearing.svg":                      {inspect.SVGEventHandler, inspect.SVGExternalHref, inspect.SVGScript},
	"office/macro-template/macro.openxmlsdk.xlsm":          {inspect.VBAProject},
	"office/macro-template/macro.openxmlsdk.xltm":          {inspect.VBAProject},
	"office/macro-template/simple-macro.poi.docm":          {inspect.VBAProject},
	"office/macro-template/simple-macro.poi.pptm":          {inspect.VBAProject},
	"office/macro-template/simple-macro.poi.xlsm":          {inspect.VBAProject},
	"office/macro-template/xlm-macro.poi.xlsm":             {inspect.MacroSheet},
	"office/macro-template/protected-passtika.poi.xlsb":    {inspect.OOXMLEncrypted},
	"pdf/edge/attachments/embedded-video-avi.pdf":          {inspect.PDFEmbeddedFile},
	"pdf/edge/attachments/file-attachment-annotation.pdf":  {inspect.PDFEmbeddedFile},
	"pdf/edge/attachments/file-attachment.pdf":             {inspect.PDFEmbeddedFile, inspect.PDFJavaScript, inspect.PDFJS},
	"pdf/edge/encrypted/encryption-nocopy.pdf":             {inspect.PDFEncrypt},
	"pdf/edge/encrypted/encryption-notextaccess.pdf":       {inspect.PDFEncrypt},
	"pdf/edge/encrypted/encryption-openpassword.pdf":       {inspect.PDFEncrypt},
	"pdf/edge/encrypted/simple-open-password.pdf":          {inspect.PDFEncrypt, inspect.PDFOpenAction},
	"pdf/edge/encrypted/unicode-password-corrigendum5.pdf": {inspect.PDFEncrypt},
	"pdf/edge/malformed/compacted-syntax.pdf":              {inspect.PDFAA, inspect.PDFJavaScript, inspect.PDFJS},
	"pdf/edge/malformed/header-no-version-info.pdf":        {inspect.SignatureMismatch},
	"pdf/edge/malformed/javascript.pdf":                    {inspect.PDFJavaScript, inspect.PDFJS, inspect.PDFOpenAction},
	"pdf/edge/pdfa/pdfa3b-embedded-files-fail.pdf":         {inspect.PDFEmbeddedFile},
	"pdf/edge/pdfa/pdfa3b-embedded-files-pass.pdf":         {inspect.PDFEmbeddedFile},

	"pdf/born-digital/mgrs-field-report.pdf":              {inspect.PDFOpenAction},
	"pdf/edge/forms/isartor-interactive-form.pdf":         {inspect.PDFOpenAction},
	"pdf/edge/pdfa/embedded-lucinda-sans-pdfa-1a.pdf":     {inspect.PDFOpenAction},
	"pdf/edge/tagged/pdfua1-text-pass.pdf":                {inspect.PDFOpenAction},
	"office/legacy/generated/docx/media.libreoffice.docx": {inspect.ExternalRelationship},
	"office/legacy/generated/docx/media.python-docx.docx": {inspect.ExternalRelationship},
	"office/realworld/docx/word-with-attachments.docx":    {inspect.OLEObject},
	"office/realworld/pptx/linked-pictures.pptx":          {inspect.ExternalRelationship},
	"pdf/scanned/generate.sh":                             {inspect.Executable},
}

// docsBombSize is the expanded size the bomb test lowers the rule to, under
// archive/high-ratio-200mb.zip's 200 MB, which the default 1 GiB lets through.
const docsBombSize = 100 << 20

// docsRoot returns ~/docs, skipping the test on a machine without it, and its manifest's paths.
func docsRoot(t *testing.T) (string, []string) {
	t.Helper()
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
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := jsonv2.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		paths = append(paths, f.Path)
	}
	return root, paths
}

// TestDocs inspects every file of ~/docs' corpus, read only, when this machine has it, by its
// manifest: each yields exactly the checks docsFlags lists for it, none for a file not listed,
// and every file listed is in the manifest. None is unsafe under the default options, so every
// verdict is proceed. A machine without ~/docs skips it.
func TestDocs(t *testing.T) {
	root, paths := docsRoot(t)
	for _, rel := range paths {
		r, err := inspect.Inspect(t.Context(), filepath.Join(root, rel), filepath.Base(rel), inspect.Options{})
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if got, want := checks(r), docsFlags[rel]; !slices.Equal(got, want) {
			t.Errorf("%s: checks %v, want %v (%+v)", rel, got, want, r.Flags)
		}
		if r.Verdict != inspect.Proceed {
			t.Errorf("%s: verdict %s", rel, r.Verdict)
		}
	}
	for rel := range docsFlags {
		if !slices.Contains(paths, rel) {
			t.Errorf("%s: listed, not in the manifest", rel)
		}
	}
}

// TestDocsBomb: archive/high-ratio-200mb.zip, 200 MB of zeros in one entry, is past the ratio,
// so a bomb once the rule's size is lowered under it, and quarantined.
func TestDocsBomb(t *testing.T) {
	root, _ := docsRoot(t)
	r, err := inspect.Inspect(t.Context(), filepath.Join(root, "archive", "high-ratio-200mb.zip"), "high-ratio-200mb.zip", inspect.Options{BombSize: docsBombSize})
	if err != nil {
		t.Fatal(err)
	}
	if got := checks(r); !slices.Equal(got, []inspect.Check{inspect.ZIPBomb}) || r.Verdict != inspect.Quarantine {
		t.Errorf("%+v, want zip-bomb alone, quarantined", r)
	}
}
