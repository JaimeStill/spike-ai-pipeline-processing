//go:build linux

package office

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// ErrNoLibreOffice is the cause of the error a conversion returns for a legacy document or an RTF
// when the Converter has no LibreOffice converter. Nothing runs for it.
var ErrNoLibreOffice = errors.New("no LibreOffice converter")

// exportFilters are LibreOffice's export filters to the OOXML formats, by the format's
// extension; naming the filter, not only the extension, keeps a template's export from choosing
// a template format.
var exportFilters = map[string]string{
	"docx": "MS Word 2007 XML",
	"xlsx": "Calc MS Excel 2007 XML",
	"pptx": "Impress MS PowerPoint 2007 XML",
}

// legacyTargets are the OOXML formats the legacy extensions convert to. An RTF, found by its
// signature, not its extension, converts to docx.
var legacyTargets = map[string]string{
	".doc": "docx",
	".dot": "docx",
	".xls": "xlsx",
	".xlt": "xlsx",
	".ppt": "pptx",
	".pps": "pptx",
	".pot": "pptx",
}

// legacyHeadLen is how much of a document legacyFormat peeks at: an OLE2 compound file's
// signature, the legacy binary formats' and an encrypted OOXML package's, and an RTF's, which is
// shorter.
const legacyHeadLen = 8

// legacyFormat reports whether the document doc begins is one LibreOffice converts before office
// runs: an RTF, as the route package identifies it by its signature, whatever its file name,
// name, says, converted to docx as a .doc is; or an OLE2 compound file, as the route package
// identifies it, whose name has a legacy extension, in any case. It returns the format converted
// from, "rtf" or the legacy extension without its dot and lowercased, and the OOXML format it
// converts to; "" for both when the document goes to office as it is. It peeks at doc's first
// bytes without consuming them.
func legacyFormat(doc *bufio.Reader, name string) (from, to string) {
	head, _ := doc.Peek(legacyHeadLen)
	kind := route.Identify(head)
	if kind == route.RTF {
		return "rtf", "docx"
	}
	ext := strings.ToLower(filepath.Ext(name))
	to, ok := legacyTargets[ext]
	if !ok || kind != route.OLE2 {
		return "", ""
	}
	return strings.TrimPrefix(ext, "."), to
}

// sofficeOutputLimit is the most of soffice's output a conversion keeps, for its error.
const sofficeOutputLimit = 64 << 10

// toOOXML converts the legacy document or RTF spooled at path to the OOXML format to through the
// LibreOffice converter, in dir, the working directory the runners mount, as soffice says. It
// returns the converted document's path and a function that removes it with soffice's profile,
// which the caller runs when its call ends; a failed conversion removes them itself. soffice
// exits zero having written nothing when it can't export the document it loaded, so a converted
// document that isn't there is an error too.
func (c *Converter) toOOXML(ctx context.Context, dir, path, to string) (string, func() error, error) {
	outdir, remove, msg, err := c.soffice(ctx, dir, to+":"+exportFilters[to], []string{path})
	if err != nil {
		return "", nil, err
	}
	converted := filepath.Join(outdir, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))+"."+to)
	info, err := os.Stat(converted)
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s isn't a regular file", converted)
	} else if errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("soffice wrote no %s", to)
	}
	if err != nil {
		return "", nil, errors.Join(sofficeError(err, msg), remove())
	}
	return converted, remove, nil
}

// soffice runs the LibreOffice converter once in dir, the working directory the runners mount,
// converting each of inputs as convertTo says: the target extension and, after a colon, the
// export filter. It makes a directory of its own there, libreoffice-*, holding soffice's user
// profile, so no run shares or leaves a profile elsewhere, and its output directory, which it
// returns. soffice names each output for its input's stem. It returns a function that removes
// the directory, which the caller runs when it is done with the output, and soffice's message,
// for an error the caller finds in the output. A run that fails removes the directory itself,
// and its error carries soffice's message.
func (c *Converter) soffice(ctx context.Context, dir, convertTo string, inputs []string) (outdir string, remove func() error, msg string, err error) {
	work, err := os.MkdirTemp(dir, "libreoffice-*")
	if err != nil {
		return "", nil, "", fmt.Errorf("office: libreoffice directory: %w", err)
	}
	remove = func() error { return os.RemoveAll(work) }
	profile := url.URL{Scheme: "file", Path: filepath.Join(work, "profile")}
	outdir = filepath.Join(work, "out")
	out := &cli.LimitedBuffer{Limit: sofficeOutputLimit}
	args := []string{
		"-env:UserInstallation=" + profile.String(),
		"--headless", "--norestore",
		"--convert-to", convertTo,
		"--outdir", outdir,
	}
	err = c.libreoffice.Run(ctx, tool.Invocation{
		Args:   append(args, inputs...),
		Dir:    dir,
		Stdout: out,
		Stderr: out,
	})
	msg = sofficeMessage(out.String())
	if err != nil {
		return "", nil, "", errors.Join(sofficeError(err, msg), remove())
	}
	return outdir, remove, msg, nil
}

// sofficeError returns err as the LibreOffice converter's error, with soffice's message, msg,
// when it has one.
func sofficeError(err error, msg string) error {
	err = fmt.Errorf("office: libreoffice: %w", err)
	if msg != "" {
		err = fmt.Errorf("%w: %s", err, msg)
	}
	return err
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
