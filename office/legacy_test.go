//go:build linux

package office_test

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// ole2 begins an OLE2 compound file, as a legacy document and an encrypted OOXML package do.
const ole2 = "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"

// rtf begins an RTF document.
const rtf = `{\rtf1\ansi `

// TestConvertLegacy: a document with the OLE2 signature whose name has a legacy extension, in
// any case, is spooled with that extension, lowercased, and converted by soffice to its OOXML
// format, in the working directory, with a fresh profile and its output in a directory of its
// own there; office then runs once on the converted document. The result records the legacy format,
// and only the media directory is left. An RTF, by its signature, whatever its name says, is
// spooled as .rtf and converted to docx by Writer's filter, as a .doc is, and recorded as "rtf".
func TestConvertLegacy(t *testing.T) {
	c, runs := legacyConverter(t)
	for _, tc := range []struct {
		desc, head, name, from, to, filter string
	}{
		{"report.doc", ole2, "report.doc", "doc", "docx", "MS Word 2007 XML"},
		{"letter.DOT", ole2, "letter.DOT", "dot", "docx", "MS Word 2007 XML"},
		{"budget.xls", ole2, "/somewhere/else/budget.xls", "xls", "xlsx", "Calc MS Excel 2007 XML"},
		{"budget.xlt", ole2, "budget.xlt", "xlt", "xlsx", "Calc MS Excel 2007 XML"},
		{"deck.ppt", ole2, "deck.ppt", "ppt", "pptx", "Impress MS PowerPoint 2007 XML"},
		{"deck.Pps", ole2, "deck.Pps", "pps", "pptx", "Impress MS PowerPoint 2007 XML"},
		{"deck.pot", ole2, "deck.pot", "pot", "pptx", "Impress MS PowerPoint 2007 XML"},
		{"memo.rtf", rtf, "memo.rtf", "rtf", "docx", "MS Word 2007 XML"},
		{"MEMO.RTF", rtf, "MEMO.RTF", "rtf", "docx", "MS Word 2007 XML"},
		{"rtf named .doc", rtf, "memo.doc", "rtf", "docx", "MS Word 2007 XML"},
		{"rtf named .xls", rtf, "memo.xls", "rtf", "docx", "MS Word 2007 XML"},
		{"rtf with no name", rtf, "", "rtf", "docx", "MS Word 2007 XML"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			before := len(runs())
			doc := tc.head + "the " + tc.from + " document"
			res, err := c.Convert(t.Context(), strings.NewReader(doc), office.Request{Dir: dir, Name: tc.name})
			if err != nil {
				t.Fatal(err)
			}
			if res.ConvertedFrom != tc.from {
				t.Errorf("converted from: got %q, want %q", res.ConvertedFrom, tc.from)
			}
			all := runs()[before:]
			if len(all) != 2 || all[0].Program != "soffice" || all[1].Program != "spike-ai-pipeline-office" {
				t.Fatalf("runs: got %+v, want soffice, then office", all)
			}
			so, of := all[0], all[1]
			if so.Dir != dir || of.Dir != dir {
				t.Errorf("working directories: soffice %s, office %s, want %s", so.Dir, of.Dir, dir)
			}
			if so.Input != doc {
				t.Errorf("soffice's input: got %q, want %q", so.Input, doc)
			}
			if so.Registry != "" {
				t.Errorf("soffice's profile started with %q, want a fresh one", so.Registry)
			}
			work := regexp.QuoteMeta(dir) + `/libreoffice-[^/]+`
			spooled := regexp.QuoteMeta(dir) + `/(spool-[^/]+)\.` + tc.from
			want := []string{
				`-env:UserInstallation=file://` + work + `/profile`,
				`--headless`, `--norestore`,
				`--convert-to`, regexp.QuoteMeta(tc.to + ":" + tc.filter),
				`--outdir`, work + `/out`,
				spooled,
			}
			if !matchArgs(so.Args, want) {
				t.Errorf("soffice's arguments: got %q, want %q", so.Args, want)
			}
			base := regexp.MustCompile(spooled).FindStringSubmatch(so.Args[len(so.Args)-1])
			if base == nil {
				t.Fatalf("soffice's input %s isn't the spooled document", so.Args[len(so.Args)-1])
			}
			outdir := so.Args[slices.Index(so.Args, "--outdir")+1]
			if of.Args[0] != filepath.Join(outdir, base[1]+"."+tc.to) {
				t.Errorf("office's input: got %s, want soffice's output %s", of.Args[0], filepath.Join(outdir, base[1]+"."+tc.to))
			}
			if of.Input != convertedPrefix+doc {
				t.Errorf("office's input: got %q, want soffice's output", of.Input)
			}
			if got := entries(t, dir); !slices.Equal(got, []string{filepath.Base(res.MediaDir)}) {
				t.Errorf("working directory holds %q, want the media directory alone", got)
			}
		})
	}
}

// matchArgs reports whether each of args matches the anchored pattern of want at its index.
func matchArgs(args, want []string) bool {
	if len(args) != len(want) {
		return false
	}
	for i, p := range want {
		if !regexp.MustCompile(`^` + p + `$`).MatchString(args[i]) {
			return false
		}
	}
	return true
}

// TestConvertLegacyNoConverter: without a LibreOffice converter, a legacy document or an RTF is
// refused with ErrNoLibreOffice, naming the file and its format, before anything is spooled or
// run.
func TestConvertLegacyNoConverter(t *testing.T) {
	c, runs := standinConverter(t)
	for _, tc := range []struct{ doc, name, want string }{
		{ole2 + "a doc", "/in/report.doc", "office: report.doc is a legacy doc document: no LibreOffice converter"},
		{rtf + "a memo}", "/in/memo.rtf", "office: memo.rtf is an RTF document: no LibreOffice converter"},
		{rtf + "a memo}", "", "office: the document is an RTF document: no LibreOffice converter"},
	} {
		dir := t.TempDir()
		_, err := c.Convert(t.Context(), strings.NewReader(tc.doc), office.Request{Dir: dir, Name: tc.name})
		if !errors.Is(err, office.ErrNoLibreOffice) {
			t.Fatalf("%q: got %v, want ErrNoLibreOffice", tc.name, err)
		}
		if err.Error() != tc.want {
			t.Errorf("error: got %q, want %q", err.Error(), tc.want)
		}
		if n := len(runs()); n != 0 {
			t.Errorf("%q: %d runs, want none", tc.name, n)
		}
		if got := entries(t, dir); len(got) != 0 {
			t.Errorf("%q: working directory holds %q, want nothing", tc.name, got)
		}
	}
}

// TestConvertNotLegacy: an OLE2 file whose name has no legacy extension, such as an encrypted
// OOXML package, a legacy name on a file without the signature, and an .rtf without RTF's
// signature, go to office as they are, and soffice never runs; office refusing the OLE2 file is
// office's refusal.
func TestConvertNotLegacy(t *testing.T) {
	c, runs := legacyConverter(t)
	for _, tc := range []struct{ desc, doc, name string }{
		{"encrypted docx", ole2 + "an encrypted package", "secret.docx"},
		{"no name", ole2 + "a doc", ""},
		{"no extension", ole2 + "a doc", "report"},
		{"no signature", "PK\x03\x04 a docx", "renamed.doc"},
		{"no rtf signature", "PK\x03\x04 a docx", "renamed.rtf"},
		{"short", "\xd0\xcf", "short.doc"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			before := len(runs())
			res, err := c.Convert(t.Context(), strings.NewReader(tc.doc), office.Request{Dir: t.TempDir(), Name: tc.name})
			if err != nil {
				t.Fatal(err)
			}
			if res.ConvertedFrom != "" {
				t.Errorf("converted from %q", res.ConvertedFrom)
			}
			all := runs()[before:]
			if len(all) != 1 || all[0].Program != "spike-ai-pipeline-office" || all[0].Input != tc.doc {
				t.Fatalf("runs: got %+v, want office alone on the document", all)
			}
			if ext := filepath.Ext(all[0].Args[0]); ext != "" {
				t.Errorf("spooled with extension %s, want none", ext)
			}
		})
	}

	t.Setenv(exitEnv, "3")
	t.Setenv(stderrEnv, "spike-ai-pipeline-office: refused: {path}: an OLE compound file\n")
	_, err := c.Convert(t.Context(), strings.NewReader(ole2+"an encrypted package"), office.Request{Dir: t.TempDir(), Name: "secret.docx"})
	if !errors.Is(err, office.ErrRefused) {
		t.Errorf("office's refusal: got %v, want ErrRefused", err)
	}
}

// TestConvertLegacyFails: soffice exiting nonzero, or exiting zero having written nothing, fails
// the conversion with soffice's own error line, and office never runs; office failing on the
// converted document is office's error. Each leaves nothing in the working directory.
func TestConvertLegacyFails(t *testing.T) {
	c, runs := legacyConverter(t)
	noise := "(process:22): dconf-CRITICAL **: unable to create directory '/.cache/dconf'\nFontconfig error: No writable cache directories\n"
	for _, tc := range []struct {
		desc     string
		env      map[string]string
		programs []string
		check    func(error) bool
		want     string
	}{
		{"soffice exits 1",
			map[string]string{sofficeExitEnv: "1", sofficeOutputEnv: noise + "Error: Please verify input parameters... (SfxBaseModel::impl_store failed)\n"},
			[]string{"soffice"},
			func(err error) bool { _, ok := errors.AsType[*tool.ExitError](err); return ok },
			"Error: Please verify input parameters... (SfxBaseModel::impl_store failed)"},
		{"soffice writes nothing",
			map[string]string{sofficeNothingEnv: "1", sofficeOutputEnv: noise},
			[]string{"soffice"},
			func(err error) bool { return err != nil },
			"office: libreoffice: soffice wrote no docx: Fontconfig error: No writable cache directories"},
		{"office refuses",
			map[string]string{exitEnv: "3", stderrEnv: "spike-ai-pipeline-office: refused: {path}: not a package\n", mediaEnv: "a.png"},
			[]string{"soffice", "spike-ai-pipeline-office"},
			func(err error) bool { return errors.Is(err, office.ErrRefused) },
			"office: refused: not a package"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := t.TempDir()
			before := len(runs())
			res, err := c.Convert(t.Context(), strings.NewReader(ole2+"a doc"), office.Request{Dir: dir, Name: "a.doc"})
			if res != nil || !tc.check(err) {
				t.Fatalf("got %+v, %v", res, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q doesn't say %q", err, tc.want)
			}
			var programs []string
			for _, r := range runs()[before:] {
				programs = append(programs, r.Program)
			}
			if !slices.Equal(programs, tc.programs) {
				t.Errorf("runs: got %q, want %q", programs, tc.programs)
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("working directory holds %q, want nothing", got)
			}
		})
	}
}

// TestNewConverterLibreOffice: LibreOfficeSpec finds soffice's version after the noise on its
// stderr, and refuses an older soffice at the Tool's construction; the constructor refuses one a
// laxer Spec built, whatever minimum the Tool's own Spec declared.
func TestNewConverterLibreOffice(t *testing.T) {
	installStandin(t, "0.1.0")
	o, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	lo, err := tool.NewExec(t.Context(), picture.LibreOfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	if got := lo.Version().String(); got != "26.8.1.1" {
		t.Errorf("version: got %s, want 26.8.1.1", got)
	}

	t.Setenv(sofficeVersionEnv, "7.6.4.1")
	if _, err := tool.NewExec(t.Context(), picture.LibreOfficeSpec()); !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewExec over 7.6.4.1: got %v, want ErrTooOld", err)
	}
	lax := picture.LibreOfficeSpec()
	lax.Minimum = "7.0"
	old, err := tool.NewExec(t.Context(), lax)
	if err != nil {
		t.Fatal(err)
	}
	_, err = office.NewConverter(t.Context(), office.Config{Office: o, LibreOffice: old})
	var verr *tool.VersionError
	if !errors.As(err, &verr) || !errors.Is(err, tool.ErrTooOld) {
		t.Fatalf("NewConverter over 7.6.4.1: got %v, want a VersionError wrapping ErrTooOld", err)
	}
	if verr.Program != "soffice" || verr.Found != "7.6.4.1" || verr.Minimum != picture.LibreOfficeMinimum {
		t.Errorf("VersionError: %+v", verr)
	}
}
