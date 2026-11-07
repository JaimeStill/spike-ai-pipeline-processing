package inspect_test

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// checks returns the checks of r's flags, in r's order.
func checks(r inspect.Report) []inspect.Check {
	out := make([]inspect.Check, 0, len(r.Flags))
	for _, f := range r.Flags {
		out = append(out, f.Check)
	}
	return out
}

// mustInspect inspects the file at path, named name, under opts, failing the test on an error.
func mustInspect(t *testing.T, path, name string, opts inspect.Options) inspect.Report {
	t.Helper()
	r, err := inspect.Inspect(t.Context(), path, name, opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// evidence returns the evidence of r's flag of check c, "" when it has none.
func evidence(r inspect.Report, c inspect.Check) string {
	for _, f := range r.Flags {
		if f.Check == c {
			return f.Evidence
		}
	}
	return ""
}

// synthesized are the committed hazard fixtures generate.go writes: each file's checks, sorted,
// a substring of each check's evidence, and its verdict under the default options.
var synthesized = []struct {
	file     string
	evidence map[inspect.Check]string
	verdict  inspect.Verdict
}{
	{"traversal.zip", map[inspect.Check]string{inspect.ZIPTraversal: `"docs/../../evil.txt"`}, inspect.Quarantine},
	{"absolute.zip", map[inspect.Check]string{inspect.ZIPAbsolutePath: `"/tmp/evil.txt", and 1 more`}, inspect.Quarantine},
	{"symlink.zip", map[inspect.Check]string{inspect.ZIPSymlink: `"passwd"`}, inspect.Quarantine},
	{"declared-bomb.zip", map[inspect.Check]string{inspect.ZIPBomb: `4294967296 bytes from 11`}, inspect.Quarantine},
	// 8 MiB is under the default 1 GiB, so the ratio alone doesn't make it a bomb.
	{"bomb.zip", nil, inspect.Proceed},
	{"encrypted.zip", map[inspect.Check]string{inspect.ZIPEncrypted: `"secret.txt"`}, inspect.Proceed},
	{"macro.docm", map[inspect.Check]string{
		inspect.ActiveX:              `"word/activeX/activeX1.xml"`,
		inspect.ExternalRelationship: `attachedTemplate https://example.invalid/template.dotm`,
		inspect.OLEObject:            `"word/embeddings/oleObject1.bin"`,
		inspect.VBAProject:           `"word/vbaProject.bin"`,
	}, inspect.Proceed},
	// Its one external relationship is a hyperlink, which isn't active content.
	{"hyperlink.docx", nil, inspect.Proceed},
	{"javascript.pdf", map[inspect.Check]string{
		inspect.PDFAA:         "/AA at offset 66",
		inspect.PDFJavaScript: "/JavaScript at offset 237, and 1 more",
		inspect.PDFJS:         "/JS at offset 249, and 1 more",
		inspect.PDFOpenAction: "/OpenAction at offset 48",
	}, inspect.Proceed},
	{"script.svg", map[inspect.Check]string{
		inspect.SVGEventHandler:  "onload on <svg>",
		inspect.SVGExternalHref:  `xlink:href="https://example.invalid/tracker.png" on <image> at line 7`,
		inspect.SVGForeignObject: "<foreignObject> at line 8",
		inspect.SVGScript:        "<script> at line 3",
	}, inspect.Proceed},
	{"invoice.pdf.exe", map[inspect.Check]string{
		inspect.DoubleExtension:     `"invoice.pdf.exe": .pdf, then .exe`,
		inspect.Executable:          "pe signature",
		inspect.ExecutableExtension: "extension .exe",
	}, inspect.Proceed},
	{"mismatch.pdf", map[inspect.Check]string{inspect.SignatureMismatch: "extension .pdf, png"}, inspect.Proceed},
	{"polyglot.jpg", map[inspect.Check]string{inspect.Polyglot: "ZIP end-of-central-directory record at offset 168 in a jpeg"}, inspect.Proceed},
	{"polyglot.png", map[inspect.Check]string{inspect.Polyglot: "PDF header at offset 33 after a png signature"}, inspect.Proceed},
	{"encrypted.xlsx", map[inspect.Check]string{inspect.OOXMLEncrypted: "EncryptedPackage"}, inspect.Proceed},
	// The tars, by their headers, as the ZIPs are by their central directories. A directory is
	// no hazard, and a gzip stream, which declares its expanded size only modulo 4 GiB, is
	// checked as a gzip no further: the tar it holds is inspected once the stage decompresses
	// it.
	{"clean.tar", nil, inspect.Proceed},
	{"clean.tar.gz", nil, inspect.Proceed},
	{"unnamed.tgz", nil, inspect.Proceed},
	{"traversal.tar.gz", nil, inspect.Proceed},
	{"bomb.tar.gz", nil, inspect.Proceed},
	{"traversal.tar", map[inspect.Check]string{inspect.TarTraversal: `entry "docs/../../evil.txt"`}, inspect.Quarantine},
	{"absolute.tar", map[inspect.Check]string{inspect.TarAbsolutePath: `entry "/tmp/evil.txt", and 1 more`}, inspect.Quarantine},
	{"symlink.tar", map[inspect.Check]string{inspect.TarSymlink: `entry "passwd" to "/etc/passwd"`}, inspect.Quarantine},
	{"hardlink.tar", map[inspect.Check]string{inspect.TarHardlink: `entry "shadow" to "/etc/shadow"`}, inspect.Quarantine},
	{"device.tar", map[inspect.Check]string{inspect.TarDevice: `character device entry "null", 1:3, and 2 more`}, inspect.Quarantine},
	{"sparse-bomb.tar", map[inspect.Check]string{inspect.TarBomb: `entry "zeros.bin": 4294967296 bytes declared in a 2048-byte tar`}, inspect.Quarantine},
}

// TestSynthesized inspects each synthesized hazard fixture: each yields exactly its checks,
// each check's evidence naming where it is, each flag of its check's class, and its verdict.
func TestSynthesized(t *testing.T) {
	for _, tc := range synthesized {
		t.Run(tc.file, func(t *testing.T) {
			r := mustInspect(t, filepath.Join("testdata", tc.file), tc.file, inspect.Options{})
			want := slices.Sorted(maps.Keys(tc.evidence))
			if got := checks(r); !slices.Equal(got, want) {
				t.Fatalf("checks %v, want %v (%+v)", got, want, r.Flags)
			}
			for _, f := range r.Flags {
				if !strings.Contains(f.Evidence, tc.evidence[f.Check]) {
					t.Errorf("%s: evidence %q, want it to hold %q", f.Check, f.Evidence, tc.evidence[f.Check])
				}
				if f.Class != inspect.ClassOf(f.Check) {
					t.Errorf("%s: class %s, want %s", f.Check, f.Class, inspect.ClassOf(f.Check))
				}
			}
			if r.Verdict != tc.verdict {
				t.Errorf("verdict %s, want %s", r.Verdict, tc.verdict)
			}
		})
	}
}

// TestBombThresholds: bomb.zip, 8 MiB at about 1,030:1, is a bomb once the size is lowered under
// it, and not once the ratio is raised over it, since an entry must be past both.
func TestBombThresholds(t *testing.T) {
	path := filepath.Join("testdata", "bomb.zip")
	r := mustInspect(t, path, "bomb.zip", inspect.Options{BombSize: 1 << 20})
	if !r.Has(inspect.ZIPBomb) || r.Verdict != inspect.Quarantine {
		t.Errorf("size lowered to 1 MiB: %+v, want a zip-bomb flag and quarantine", r)
	}
	if e := evidence(r, inspect.ZIPBomb); !strings.Contains(e, `entry "zeros.bin": 8388608 bytes`) {
		t.Errorf("evidence %q", e)
	}
	r = mustInspect(t, path, "bomb.zip", inspect.Options{BombSize: 1 << 20, BombRatio: 2000})
	if r.Has(inspect.ZIPBomb) {
		t.Errorf("ratio raised to 2,000:1: %+v, want no zip-bomb flag", r)
	}
}

// TestPolicy: a policy overrides a check's default action either way, quarantining an active
// finding and letting an unsafe one proceed, and leaves the checks it doesn't name to their
// class.
func TestPolicy(t *testing.T) {
	docm := filepath.Join("testdata", "macro.docm")
	r := mustInspect(t, docm, "macro.docm", inspect.Options{Policy: map[inspect.Check]inspect.Verdict{inspect.VBAProject: inspect.Quarantine}})
	if r.Verdict != inspect.Quarantine {
		t.Errorf("vba-project quarantined: verdict %s", r.Verdict)
	}
	traversal := filepath.Join("testdata", "traversal.zip")
	r = mustInspect(t, traversal, "traversal.zip", inspect.Options{Policy: map[inspect.Check]inspect.Verdict{inspect.ZIPTraversal: inspect.Proceed}})
	if r.Verdict != inspect.Proceed || !r.Has(inspect.ZIPTraversal) {
		t.Errorf("zip-traversal let proceed: %+v, want the flag kept and proceed", r)
	}
	r = mustInspect(t, traversal, "traversal.zip", inspect.Options{Policy: map[inspect.Check]inspect.Verdict{inspect.VBAProject: inspect.Proceed}})
	if r.Verdict != inspect.Quarantine {
		t.Errorf("a policy naming another check: verdict %s, want quarantine", r.Verdict)
	}
}

// TestOptionsRefused: a policy naming no check or an action other than proceed or quarantine,
// and a negative ratio, are refused with ErrOptions before the file is read.
func TestOptionsRefused(t *testing.T) {
	for name, opts := range map[string]inspect.Options{
		"unknown check":  {Policy: map[inspect.Check]inspect.Verdict{"no-such-check": inspect.Proceed}},
		"unknown action": {Policy: map[inspect.Check]inspect.Verdict{inspect.VBAProject: "delete"}},
		"negative ratio": {BombRatio: -1},
	} {
		if _, err := inspect.Inspect(t.Context(), "no-such-file", "x", opts); !errors.Is(err, inspect.ErrOptions) {
			t.Errorf("%s: %v, want ErrOptions", name, err)
		}
	}
	if _, err := inspect.Inspect(t.Context(), filepath.Join(t.TempDir(), "missing"), "x", inspect.Options{}); err == nil {
		t.Error("a missing file: no error")
	}
}

// write writes data to a file named name in a directory of its own and returns its path.
func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPDFNames: a hex-escaped name is read decoded, the whole file is scanned, past its head,
// and a name split across the stream's chunks is read whole; a longer name that begins with
// one, such as /JSX, isn't it.
func TestPDFNames(t *testing.T) {
	r := mustInspect(t, write(t, "a.pdf", []byte("%PDF-1.7\n<< /S /J#61vaScript /JSX 1 >>\n")), "a.pdf", inspect.Options{})
	if got := checks(r); !slices.Equal(got, []inspect.Check{inspect.PDFJavaScript}) {
		t.Fatalf("checks %v, want pdf-javascript alone", got)
	}
	if e := evidence(r, inspect.PDFJavaScript); e != "/J#61vaScript at offset 15" {
		t.Errorf("evidence %q", e)
	}
	// /Launch's "/" is the stream's last byte of its first 64 KiB chunk, /OpenAction lies across
	// the boundary of its second, and /Encrypt comes at the end of a file of 1 MiB.
	pdf := bytes.Repeat([]byte(" "), 1<<20)
	copy(pdf, "%PDF-1.7\n")
	copy(pdf[1<<16-1:], "/Launch ")
	copy(pdf[2<<16-4:], "/OpenAction ")
	pdf = append(pdf, "/Encrypt"...)
	r = mustInspect(t, write(t, "b.pdf", pdf), "b.pdf", inspect.Options{})
	want := []inspect.Check{inspect.PDFEncrypt, inspect.PDFLaunch, inspect.PDFOpenAction}
	if got := checks(r); !slices.Equal(got, want) {
		t.Fatalf("checks %v, want %v", got, want)
	}
	if e := evidence(r, inspect.PDFOpenAction); e != "/OpenAction at offset 131068" {
		t.Errorf("evidence %q", e)
	}
	if e := evidence(r, inspect.PDFEncrypt); e != "/Encrypt at offset 1048576" {
		t.Errorf("evidence %q", e)
	}
}

// TestPolyglotAcrossChunks: a ZIP's end-of-central-directory record that a chunk boundary splits
// is found, in content past the head.
func TestPolyglotAcrossChunks(t *testing.T) {
	jpg, err := os.ReadFile(filepath.Join("testdata", "polyglot.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	// polyglot.jpg's ZIP begins after its 22-byte JPEG; padding puts its record, at 168, across
	// the first chunk's end.
	pad := bytes.Repeat([]byte{0}, 1<<16-168-2)
	data := append(append(append([]byte{}, jpg[:22]...), pad...), jpg[22:]...)
	r := mustInspect(t, write(t, "p.jpg", data), "p.jpg", inspect.Options{})
	if !r.Has(inspect.Polyglot) {
		t.Errorf("%+v, want a polyglot flag", r)
	}
	// A JPEG that merely holds the record's four bytes, its fields making no central directory,
	// isn't one.
	data = append(append([]byte{}, jpg[:22]...), "PK\x05\x06"...)
	data = append(data, make([]byte, 64)...)
	if r := mustInspect(t, write(t, "q.jpg", data), "q.jpg", inspect.Options{}); r.Has(inspect.Polyglot) {
		t.Errorf("a bare signature: %+v, want no flag", r)
	}
}

// TestNames: executable extensions in any case and with trailing dots and spaces, a double
// extension only after a plausible extension, and names that are neither.
func TestNames(t *testing.T) {
	for name, want := range map[string][]inspect.Check{
		"run.js":             {inspect.ExecutableExtension},
		"SETUP.MSI":          {inspect.ExecutableExtension},
		"invoice.pdf.exe. ":  {inspect.DoubleExtension, inspect.ExecutableExtension},
		"photo.JPG.scr":      {inspect.DoubleExtension, inspect.ExecutableExtension},
		"my.report.bat":      {inspect.ExecutableExtension},
		"dir/archive.tar.gz": nil,
		"notes.txt":          nil,
		"js":                 nil,
	} {
		r := mustInspect(t, write(t, "f", []byte("plain text\n")), name, inspect.Options{})
		got := slices.DeleteFunc(checks(r), func(c inspect.Check) bool { return c == inspect.SignatureMismatch })
		if !slices.Equal(got, want) {
			t.Errorf("%q: checks %v, want %v", name, got, want)
		}
	}
}

// TestExecutableContent: route's executable kinds are flagged whatever the name.
func TestExecutableContent(t *testing.T) {
	for kind, head := range map[string][]byte{
		"elf":     []byte("\x7fELF\x02\x01\x01\x00"),
		"shebang": []byte("#!/bin/sh\necho hi\n"),
	} {
		r := mustInspect(t, write(t, "f", head), "notes", inspect.Options{})
		if got := checks(r); !slices.Equal(got, []inspect.Check{inspect.Executable}) || r.Verdict != inspect.Proceed {
			t.Errorf("%s: %+v, want an executable flag that proceeds", kind, r)
		}
	}
}

// TestUnreadableTar: a tar cut short inside an entry's content is quarantined, its headers
// unreadable past the cut, whatever the entries before it.
func TestUnreadableTar(t *testing.T) {
	whole, err := os.ReadFile(filepath.Join("testdata", "clean.tar"))
	if err != nil {
		t.Fatal(err)
	}
	// clean.tar's second entry, docs/notes.txt, has its header at 512 and its content at 1024.
	r := mustInspect(t, write(t, "cut.tar", whole[:1030]), "cut.tar", inspect.Options{})
	if !r.Has(inspect.TarUnreadable) || r.Verdict != inspect.Quarantine {
		t.Errorf("%+v, want tar-unreadable, quarantined", r)
	}
}

// TestTarBombThresholds: sparse-bomb.tar's entry declares 4 GiB in 2 KiB, a bomb at the
// defaults; it is none once the size is raised over it, nor once the ratio is raised over its
// 2,097,152:1, since an entry must be past both.
func TestTarBombThresholds(t *testing.T) {
	path := filepath.Join("testdata", "sparse-bomb.tar")
	if r := mustInspect(t, path, "sparse-bomb.tar", inspect.Options{BombSize: 8 << 30}); r.Has(inspect.TarBomb) {
		t.Errorf("size raised to 8 GiB: %+v, want no tar-bomb flag", r)
	}
	if r := mustInspect(t, path, "sparse-bomb.tar", inspect.Options{BombRatio: 4 << 20}); r.Has(inspect.TarBomb) {
		t.Errorf("ratio raised to 4,194,304:1: %+v, want no tar-bomb flag", r)
	}
}

// TestUnreadableZIP: content with a ZIP signature and no central directory is quarantined.
func TestUnreadableZIP(t *testing.T) {
	r := mustInspect(t, write(t, "a.zip", []byte("PK\x03\x04 and nothing after")), "a.zip", inspect.Options{})
	if !r.Has(inspect.ZIPUnreadable) || r.Verdict != inspect.Quarantine {
		t.Errorf("%+v, want zip-unreadable, quarantined", r)
	}
}

// TestDeterministic: the same bytes and name give the same report on every run, by value.
func TestDeterministic(t *testing.T) {
	for _, tc := range synthesized {
		path := filepath.Join("testdata", tc.file)
		a := mustInspect(t, path, tc.file, inspect.Options{})
		b := mustInspect(t, path, tc.file, inspect.Options{})
		if a.Kind != b.Kind || a.Verdict != b.Verdict || !slices.Equal(a.Flags, b.Flags) {
			t.Errorf("%s: %+v, then %+v", tc.file, a, b)
		}
	}
}

// TestCancelled: a context already ended ends a scan with its error.
func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join("testdata", "javascript.pdf")
	if _, err := inspect.Inspect(ctx, path, "javascript.pdf", inspect.Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("%v, want context.Canceled", err)
	}
}

// TestClasses: every check has a class, active or unsafe, and the unsafe ones are the ZIP, tar,
// gzip, 7z, cabinet, zstd, and PDF attachment bomb checks the safety standard names unsafe to read; 7z-encrypted, like
// zip-encrypted, is active.
func TestClasses(t *testing.T) {
	var unsafe []inspect.Check
	for _, c := range inspect.Checks() {
		switch inspect.ClassOf(c) {
		case inspect.Unsafe:
			unsafe = append(unsafe, c)
		case inspect.Active:
		default:
			t.Errorf("%s: class %q", c, inspect.ClassOf(c))
		}
	}
	want := []inspect.Check{
		inspect.SevenZipAbsolutePath, inspect.SevenZipBomb, inspect.SevenZipDevice, inspect.SevenZipHardlink,
		inspect.SevenZipSymlink, inspect.SevenZipTraversal,
		inspect.CabAbsolutePath, inspect.CabBomb, inspect.CabTraversal,
		inspect.GzipBomb, inspect.PDFAttachmentBomb,
		inspect.TarAbsolutePath, inspect.TarBomb, inspect.TarDevice, inspect.TarHardlink, inspect.TarSymlink,
		inspect.TarTraversal, inspect.TarUnreadable,
		inspect.ZIPAbsolutePath, inspect.ZIPBomb, inspect.ZIPSymlink, inspect.ZIPTraversal, inspect.ZIPUnreadable,
		inspect.ZstdBomb,
	}
	if !slices.Equal(unsafe, want) {
		t.Errorf("unsafe checks %v, want %v", unsafe, want)
	}
	if inspect.ClassOf("no-such-check") != "" {
		t.Error("a check the package doesn't have has a class")
	}
	if inspect.ClassOf(inspect.SevenZipEncrypted) != inspect.Active {
		t.Errorf("7z-encrypted: class %q, want active", inspect.ClassOf(inspect.SevenZipEncrypted))
	}
}
