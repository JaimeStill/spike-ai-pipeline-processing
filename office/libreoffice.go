//go:build linux

package office

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// LibreOfficeMinimum is the oldest LibreOffice the Converter accepts: 24.2, the release Ubuntu
// 24.04 LTS ships, the oldest in a current long-term distribution. The legacy import and the
// OOXML export filters a conversion names are far older; the integration tier runs the default
// image's release.
const LibreOfficeMinimum = "24.2"

// LibreOfficeImage is the default image the LibreOffice converter runs in through the container
// runner: Gotenberg's 8.39.0 LibreOffice image, which carries LibreOffice 26.8.1.1, pinned by its
// multi-arch index digest. soffice runs in it under the runner's full lockdown, with no
// relaxation, so tool.Image{Ref: LibreOfficeImage} is the whole of it. A caller may run another
// image, or soffice on the host through the exec runner.
const LibreOfficeImage = "gotenberg/gotenberg:8.39.0-libreoffice@sha256:" +
	"4c0eb92ef22e5d5fb0a49eac924b63d85d0b0334ea3365a1f5ca428f367b3054"

// LibreOfficeSpec declares LibreOffice's soffice, minimum 24.2, for either runner's constructor;
// the container runner makes it the entrypoint in place of the image's own. soffice --version
// prints "LibreOffice 26.8.1.1 680(Build:1)", after any warnings of its desktop libraries on
// stderr, and the pattern takes the dotted numbers after the name. A caller that needs another
// program path or a KillAfter period sets them on the Spec returned.
func LibreOfficeSpec() tool.Spec {
	return tool.Spec{
		Program:        "soffice",
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`LibreOffice (\d+(?:\.\d+)*)`),
		Minimum:        LibreOfficeMinimum,
	}
}

// ErrNoLibreOffice is the cause of the error a conversion returns for a legacy document when the
// Converter has no LibreOffice converter. Nothing runs for it.
var ErrNoLibreOffice = errors.New("no LibreOffice converter")

// ole2Signature begins every OLE2 compound file: the legacy binary formats, and an encrypted
// OOXML package.
var ole2Signature = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}

// exportFilters are LibreOffice's export filters to the OOXML formats, by the format's
// extension; naming the filter, not only the extension, keeps a template's export from choosing
// a template format.
var exportFilters = map[string]string{
	"docx": "MS Word 2007 XML",
	"xlsx": "Calc MS Excel 2007 XML",
	"pptx": "Impress MS PowerPoint 2007 XML",
}

// legacyTargets are the OOXML formats the legacy extensions convert to.
var legacyTargets = map[string]string{
	".doc": "docx",
	".dot": "docx",
	".xls": "xlsx",
	".xlt": "xlsx",
	".ppt": "pptx",
	".pps": "pptx",
	".pot": "pptx",
}

// legacyFormat reports whether the document doc begins is a legacy one: an OLE2 compound file
// whose file name, name, has a legacy extension, in any case. It returns the legacy format, the
// extension without its dot and lowercased, and the OOXML format it converts to; "" for both
// when the document isn't legacy. It peeks at doc's first bytes without consuming them.
func legacyFormat(doc *bufio.Reader, name string) (from, to string) {
	ext := strings.ToLower(filepath.Ext(name))
	to, ok := legacyTargets[ext]
	if !ok {
		return "", ""
	}
	head, _ := doc.Peek(len(ole2Signature))
	if !bytes.Equal(head, ole2Signature) {
		return "", ""
	}
	return strings.TrimPrefix(ext, "."), to
}

// sofficeOutputLimit is the most of soffice's output a conversion keeps, for its error.
const sofficeOutputLimit = 64 << 10

// toOOXML converts the legacy document spooled at path to the OOXML format to through the
// LibreOffice converter, in dir, the working directory the runners mount. It makes a directory
// of its own there, libreoffice-*, holding soffice's user profile, so no run shares or leaves a
// profile elsewhere, and the converted document. It returns the converted document's path and a
// function that removes the directory, which the caller runs when its call ends; a failed
// conversion removes it itself.
//
// soffice exits zero having written nothing when it can't export the document it loaded, so a
// converted document that isn't there is an error too. Either error carries soffice's own
// message: its first line beginning "Error", or else its last line.
func (c *Converter) toOOXML(ctx context.Context, dir, path, to string) (string, func() error, error) {
	work, err := os.MkdirTemp(dir, "libreoffice-*")
	if err != nil {
		return "", nil, fmt.Errorf("office: libreoffice directory: %w", err)
	}
	remove := func() error { return os.RemoveAll(work) }
	profile := url.URL{Scheme: "file", Path: filepath.Join(work, "profile")}
	outdir := filepath.Join(work, "out")
	out := &limitedBuffer{limit: sofficeOutputLimit}
	err = c.libreoffice.Run(ctx, tool.Invocation{
		Args: []string{
			"-env:UserInstallation=" + profile.String(),
			"--headless", "--norestore",
			"--convert-to", to + ":" + exportFilters[to],
			"--outdir", outdir,
			path,
		},
		Dir:    dir,
		Stdout: out,
		Stderr: out,
	})
	converted := filepath.Join(outdir, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))+"."+to)
	if err == nil {
		var info os.FileInfo
		if info, err = os.Stat(converted); err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("%s isn't a regular file", converted)
		} else if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("soffice wrote no %s", to)
		}
	}
	if err != nil {
		err = fmt.Errorf("office: libreoffice: %w", err)
		if msg := sofficeMessage(out.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return "", nil, errors.Join(err, remove())
	}
	return converted, remove, nil
}

// sofficeMessage returns soffice's message in its output: the first line beginning "Error", or
// else the last line that isn't blank.
func sofficeMessage(out string) string {
	last := ""
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Error") {
			return line
		}
		if line != "" {
			last = line
		}
	}
	return last
}
