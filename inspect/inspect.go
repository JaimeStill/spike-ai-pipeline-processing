package inspect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// Class names what a flag's finding means for the part, as the package doc explains.
type Class string

const (
	// Active is content that could act, or that a reader can't see into: flagged, and the part
	// proceeds by default, since no stage executes it.
	Active Class = "active"
	// Unsafe is content unsafe to read: flagged, and the part is quarantined by default, so no
	// stage runs on it and its content is kept.
	Unsafe Class = "unsafe"
)

// Verdict names what becomes of a part once its flags are weighed under a policy.
type Verdict string

const (
	// Proceed lets the part go to its stage.
	Proceed Verdict = "proceed"
	// Quarantine keeps the part from every stage, its content kept and reported with its
	// evidence.
	Quarantine Verdict = "quarantine"
)

// Check names one inspection; each flag names the check that raised it.
type Check string

// The checks of class [Active].
const (
	// SignatureMismatch is a signature that disagrees with the name's extension, such as a
	// PNG named .pdf.
	SignatureMismatch Check = "signature-mismatch"
	// Polyglot is content of one format carrying another's: a ZIP end-of-central-directory
	// record inside content that isn't a container, or a PDF header after another signature.
	Polyglot Check = "polyglot"
	// Executable is executable content, by route's PE, ELF, Mach-O, or "#!" kinds.
	Executable Check = "executable"
	// ExecutableExtension is a name whose extension is an executable's, such as .exe or .js.
	ExecutableExtension Check = "executable-extension"
	// DoubleExtension is an executable extension after another, such as "invoice.pdf.exe".
	DoubleExtension Check = "double-extension"
	// VBAProject is an OOXML package's VBA project, vbaProject.bin: macros.
	VBAProject Check = "vba-project"
	// MacroSheet is an OOXML workbook's Excel 4.0 macro sheet, under xl/macrosheets/.
	MacroSheet Check = "macro-sheet"
	// ActiveX is an OOXML package's ActiveX control part.
	ActiveX Check = "activex"
	// OLEObject is an OOXML package's OLE object, an embedding oleObject*.bin or a relationship
	// of the oleObject type.
	OLEObject Check = "ole-object"
	// ExternalRelationship is an OOXML relationship whose TargetMode is External, other than a
	// hyperlink: a remote template, a linked picture, an OLE link.
	ExternalRelationship Check = "external-relationship"
	// OOXMLEncrypted is an encrypted OOXML package: an OLE2 compound file holding an
	// EncryptedPackage stream, named with an OOXML extension.
	OOXMLEncrypted Check = "ooxml-encrypted"
	// ZIPEncrypted is a ZIP entry whose general purpose flags mark it encrypted.
	ZIPEncrypted Check = "zip-encrypted"

	// The PDF names pdfid counts, each a check of its own; [PDFEncrypt] marks encryption.
	PDFJavaScript   Check = "pdf-javascript"
	PDFJS           Check = "pdf-js"
	PDFOpenAction   Check = "pdf-openaction"
	PDFAA           Check = "pdf-aa"
	PDFLaunch       Check = "pdf-launch"
	PDFEmbeddedFile Check = "pdf-embeddedfile"
	PDFRichMedia    Check = "pdf-richmedia"
	PDFXFA          Check = "pdf-xfa"
	PDFEncrypt      Check = "pdf-encrypt"

	// SVGScript is an SVG's script element.
	SVGScript Check = "svg-script"
	// SVGEventHandler is an SVG attribute naming an event handler, one whose name begins "on".
	SVGEventHandler Check = "svg-event-handler"
	// SVGExternalHref is an SVG href or xlink:href that doesn't begin "#" or "data:".
	SVGExternalHref Check = "svg-external-href"
	// SVGForeignObject is an SVG's foreignObject element, which embeds other markup.
	SVGForeignObject Check = "svg-foreign-object"
)

// The checks of class [Unsafe].
const (
	// ZIPTraversal is a ZIP entry whose name has a ".." element.
	ZIPTraversal Check = "zip-traversal"
	// ZIPAbsolutePath is a ZIP entry whose name is absolute or begins with a drive letter.
	ZIPAbsolutePath Check = "zip-absolute-path"
	// ZIPSymlink is a ZIP entry whose external attributes' Unix mode makes it a symbolic link.
	ZIPSymlink Check = "zip-symlink"
	// ZIPBomb is a ZIP entry past both the expansion ratio and the expanded size its
	// [Options] allow, by the sizes its central directory declares.
	ZIPBomb Check = "zip-bomb"
	// ZIPUnreadable is content with a ZIP signature whose central directory can't be read, so
	// its entries can't be checked.
	ZIPUnreadable Check = "zip-unreadable"
	// TarTraversal is a tar entry whose name has a ".." element.
	TarTraversal Check = "tar-traversal"
	// TarAbsolutePath is a tar entry whose name is absolute or begins with a drive letter.
	TarAbsolutePath Check = "tar-absolute-path"
	// TarSymlink is a tar entry that is a symbolic link.
	TarSymlink Check = "tar-symlink"
	// TarHardlink is a tar entry that is a hard link.
	TarHardlink Check = "tar-hardlink"
	// TarDevice is a tar entry that is a character or block device, or a FIFO: a node an
	// extraction would make rather than a file.
	TarDevice Check = "tar-device"
	// TarBomb is a tar entry past both the expansion ratio, against the whole tar's size, and
	// the expanded size its [Options] allow, by the size its header declares. Only a sparse
	// entry, whose holes the tar doesn't store, or a header that lies, can declare one.
	TarBomb Check = "tar-bomb"
	// TarUnreadable is content with a tar signature whose headers can't all be read, so its
	// entries can't all be checked: a truncated tar among them.
	TarUnreadable Check = "tar-unreadable"
	// GzipBomb is a gzip stream past both the expansion ratio and the expanded size its
	// [Options] allow, by the bytes it decompresses to. [Inspect] raises it for no part: a gzip
	// declares its expanded size only modulo 4 GiB, in its trailer, so the stage that
	// decompresses the stream raises it on the bytes it writes.
	GzipBomb Check = "gzip-bomb"
)

// classes are every check's class.
var classes = map[Check]Class{
	SignatureMismatch: Active, Polyglot: Active, Executable: Active,
	ExecutableExtension: Active, DoubleExtension: Active,
	VBAProject: Active, MacroSheet: Active, ActiveX: Active, OLEObject: Active,
	ExternalRelationship: Active, OOXMLEncrypted: Active, ZIPEncrypted: Active,
	PDFJavaScript: Active, PDFJS: Active, PDFOpenAction: Active, PDFAA: Active,
	PDFLaunch: Active, PDFEmbeddedFile: Active, PDFRichMedia: Active, PDFXFA: Active,
	PDFEncrypt: Active,
	SVGScript:  Active, SVGEventHandler: Active, SVGExternalHref: Active, SVGForeignObject: Active,
	ZIPTraversal: Unsafe, ZIPAbsolutePath: Unsafe, ZIPSymlink: Unsafe, ZIPBomb: Unsafe,
	ZIPUnreadable: Unsafe,
	TarTraversal:  Unsafe, TarAbsolutePath: Unsafe, TarSymlink: Unsafe, TarHardlink: Unsafe,
	TarDevice: Unsafe, TarBomb: Unsafe, TarUnreadable: Unsafe, GzipBomb: Unsafe,
}

// Checks returns every check, sorted.
func Checks() []Check {
	all := make([]Check, 0, len(classes))
	for c := range classes {
		all = append(all, c)
	}
	slices.Sort(all)
	return all
}

// ClassOf returns c's class, "" for a check the package doesn't have.
func ClassOf(c Check) Class { return classes[c] }

// Flag is one finding: the check that raised it, its class, and its evidence, in a few words
// naming where it is, such as `/JavaScript at offset 1146` or `entry "../evil.txt"`. A check
// raises one flag at most, its evidence the first finding and a count of any others.
type Flag struct {
	Check    Check
	Class    Class
	Evidence string
}

// Report is what [Inspect] found in a part: the kind its signature names, its flags, sorted by
// check, and the verdict they come to under the policy.
type Report struct {
	Kind    route.Kind
	Flags   []Flag
	Verdict Verdict
}

// Has reports whether r holds a flag of check c.
func (r Report) Has(c Check) bool {
	return slices.ContainsFunc(r.Flags, func(f Flag) bool { return f.Check == c })
}

// The bomb rule's defaults, a ZIP's, a tar's, and a gzip stream's: an entry is a bomb past both.
const (
	// DefaultBombRatio is the expansion ratio, expanded to compressed size, past which an
	// entry may be a bomb.
	DefaultBombRatio = 1000
	// DefaultBombSize is the expanded size, 1 GiB, past which an entry may be a bomb.
	DefaultBombSize = 1 << 30
)

// Options tune an inspection. The zero Options is the default: each flag's class decides, and
// the bomb rule's thresholds are the defaults.
type Options struct {
	// Policy overrides the default action of the checks it names, Proceed or Quarantine, so a
	// caller may quarantine an active finding, such as a macro, or let an unsafe one proceed.
	Policy map[Check]Verdict
	// BombRatio is the expansion ratio past which a ZIP or tar entry, or a gzip stream, may be a
	// bomb; [DefaultBombRatio] when zero.
	BombRatio float64
	// BombSize is the expanded size in bytes past which a ZIP or tar entry, or a gzip stream,
	// may be a bomb; [DefaultBombSize] when zero.
	BombSize uint64
}

// ErrOptions is the error an invalid [Options] matches: a policy naming a check the package
// doesn't have or an action other than Proceed or Quarantine, or a negative ratio.
var ErrOptions = errors.New("inspect: invalid options")

// check refuses invalid options and fills in the defaults.
func (o Options) check() (Options, error) {
	for c, v := range o.Policy {
		if classes[c] == "" {
			return o, fmt.Errorf("%w: policy names no check %q", ErrOptions, c)
		}
		if v != Proceed && v != Quarantine {
			return o, fmt.Errorf("%w: policy for %s is %q, not proceed or quarantine", ErrOptions, c, v)
		}
	}
	if o.BombRatio < 0 {
		return o, fmt.Errorf("%w: bomb ratio %g", ErrOptions, o.BombRatio)
	}
	if o.BombRatio == 0 {
		o.BombRatio = DefaultBombRatio
	}
	if o.BombSize == 0 {
		o.BombSize = DefaultBombSize
	}
	return o, nil
}

// verdict is the verdict flags come to under the policy: Quarantine when any flag's action is,
// each flag's action the policy's for its check, or its class's default.
func (o Options) verdict(flags []Flag) Verdict {
	for _, f := range flags {
		action, ok := o.Policy[f.Check]
		if !ok {
			action = Proceed
			if f.Class == Unsafe {
				action = Quarantine
			}
		}
		if action == Quarantine {
			return Quarantine
		}
	}
	return Proceed
}

// Inspect inspects the part at path, named name, before its stage, and returns its report. The
// name's extension is name's, which may differ from path's, as a file named by its SHA-256
// does. It runs no stage and no program: it reads the file, streaming, its memory bounded
// whatever the file's size, and a ZIP's central directory or a tar's headers without extracting
// an entry. The
// report is a function of the file's bytes and name alone, and opts, so the same content and
// name give the same report on every run. Inspect returns an error for opts it refuses,
// matching [ErrOptions], for a file it can't read, and for ctx's end.
func Inspect(ctx context.Context, path, name string, opts Options) (Report, error) {
	opts, err := opts.check()
	if err != nil {
		return Report{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Report{}, fmt.Errorf("inspect: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Report{}, fmt.Errorf("inspect: %w", err)
	}
	head := make([]byte, route.HeadLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Report{}, fmt.Errorf("inspect: %s: %w", path, err)
	}
	head = head[:n]

	in := &inspection{file: f, size: info.Size(), name: name, ext: extension(name), kind: route.Identify(head), opts: opts, found: map[Check]*finding{}}
	in.names()
	if err := in.stream(ctx); err != nil {
		return Report{}, err
	}
	in.mismatch()
	if in.kind == route.ZIP || in.kind == route.ODF {
		if err := in.zip(ctx); err != nil {
			return Report{}, err
		}
	}
	if in.kind == route.Tar {
		if err := in.tar(ctx); err != nil {
			return Report{}, err
		}
	}
	if in.kind == route.SVG || (in.kind == route.Unknown && in.ext == ".svg") {
		if err := in.svg(ctx); err != nil {
			return Report{}, err
		}
	}
	flags := in.flags()
	return Report{Kind: in.kind, Flags: flags, Verdict: opts.verdict(flags)}, nil
}

// inspection is one Inspect's state: the file, what its head and name say, and the findings so
// far, by check.
type inspection struct {
	file  *os.File
	size  int64
	name  string
	ext   string
	kind  route.Kind
	opts  Options
	found map[Check]*finding
	// encryptedPackage is set when the stream holds an OLE2 EncryptedPackage stream's name.
	encryptedPackage bool
}

// finding is a check's first evidence and how many more findings it had.
type finding struct {
	evidence string
	more     int
}

// flag records a finding of check c.
func (in *inspection) flag(c Check, evidence string) {
	if f, ok := in.found[c]; ok {
		f.more++
		return
	}
	in.found[c] = &finding{evidence: evidence}
}

// flags returns the findings as flags, sorted by check, each evidence counting the findings
// after its first.
func (in *inspection) flags() []Flag {
	var flags []Flag
	for c, f := range in.found {
		evidence := f.evidence
		if f.more > 0 {
			evidence = fmt.Sprintf("%s, and %d more", evidence, f.more)
		}
		flags = append(flags, Flag{Check: c, Class: classes[c], Evidence: evidence})
	}
	slices.SortFunc(flags, func(a, b Flag) int { return strings.Compare(string(a.Check), string(b.Check)) })
	return flags
}

// executableExtensions are the extensions of programs and scripts a desktop runs when opened.
var executableExtensions = map[string]bool{
	".exe": true, ".dll": true, ".js": true, ".vbs": true, ".scr": true, ".bat": true,
	".cmd": true, ".ps1": true, ".lnk": true, ".hta": true, ".jar": true, ".msi": true,
}

// executableKinds are route's kinds of executable content.
var executableKinds = map[route.Kind]bool{
	route.PE: true, route.ELF: true, route.MachO: true, route.Shebang: true,
}

// extension returns name's extension, lowercased, after trailing dots and spaces are trimmed,
// as Windows trims them.
func extension(name string) string {
	return strings.ToLower(filepath.Ext(strings.TrimRight(name, ". ")))
}

// names checks the kind and the name for executables: executable content, an executable
// extension, and one after another extension.
func (in *inspection) names() {
	if executableKinds[in.kind] {
		in.flag(Executable, fmt.Sprintf("%s signature", in.kind))
	}
	if !executableExtensions[in.ext] {
		return
	}
	in.flag(ExecutableExtension, fmt.Sprintf("extension %s", in.ext))
	trimmed := strings.TrimRight(baseName(in.name), ". ")
	stem := trimmed[:len(trimmed)-len(in.ext)]
	if inner := filepath.Ext(stem); plausibleExtension(inner) {
		in.flag(DoubleExtension, fmt.Sprintf("%q: %s, then %s", in.name, strings.ToLower(inner), in.ext))
	}
}

// baseName returns the last element of a name, which may be a path.
func baseName(n string) string { return filepath.Base(filepath.ToSlash(n)) }

// plausibleExtension reports whether ext, with its dot, looks like a file extension: one to
// five letters or digits, as ".pdf" or ".docx" does and ".report" doesn't.
func plausibleExtension(ext string) bool {
	if len(ext) < 2 || len(ext) > 6 {
		return false
	}
	return !strings.ContainsFunc(ext[1:], func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
}

// extensionKinds are the kinds a signature may be for an extension that names a format; an
// extension not listed, such as .txt, which no signature marks, isn't checked.
var extensionKinds = map[string][]route.Kind{
	".pdf": {route.PDF},
	".png": {route.PNG}, ".jpg": {route.JPEG}, ".jpeg": {route.JPEG}, ".gif": {route.GIF},
	".bmp": {route.BMP}, ".tif": {route.TIFF}, ".tiff": {route.TIFF}, ".emf": {route.EMF},
	".wmf": {route.WMF}, ".svg": {route.SVG}, ".heic": {route.HEIC}, ".webp": {route.WebP},
	".avif": {route.AVIF},
	".docx": {route.ZIP}, ".docm": {route.ZIP}, ".dotx": {route.ZIP}, ".dotm": {route.ZIP},
	".xlsx": {route.ZIP}, ".xlsm": {route.ZIP}, ".xltx": {route.ZIP}, ".xltm": {route.ZIP},
	".xlsb": {route.ZIP},
	".pptx": {route.ZIP}, ".pptm": {route.ZIP}, ".potx": {route.ZIP}, ".potm": {route.ZIP},
	".ppsx": {route.ZIP},
	".doc":  {route.OLE2}, ".dot": {route.OLE2}, ".xls": {route.OLE2}, ".xlt": {route.OLE2},
	".ppt": {route.OLE2}, ".pps": {route.OLE2}, ".pot": {route.OLE2}, ".msg": {route.OLE2},
	".odt": {route.ODF, route.ZIP}, ".ods": {route.ODF, route.ZIP}, ".odp": {route.ODF, route.ZIP},
	".odg": {route.ODF, route.ZIP},
	".rtf": {route.RTF}, ".wpd": {route.WordPerfect},
	".pst": {route.PST}, ".ost": {route.OST},
	".one": {route.OneStore}, ".onetoc2": {route.OneStore}, ".onepkg": {route.MSCF},
	".cab": {route.MSCF},
	".zip": {route.ZIP}, ".jar": {route.ZIP}, ".epub": {route.ZIP, route.ODF},
	".7z": {route.SevenZip}, ".gz": {route.Gzip}, ".tgz": {route.Gzip}, ".zst": {route.Zstd},
	".tar": {route.Tar},
	".wav": {route.WAV}, ".flac": {route.FLAC}, ".mp3": {route.MP3}, ".ogg": {route.Ogg},
	".m4a": {route.M4A}, ".mp4": {route.MP4}, ".mkv": {route.MKV}, ".webm": {route.WebM},
	".avi": {route.AVI},
	".exe": {route.PE}, ".dll": {route.PE}, ".scr": {route.PE},
}

// ooxmlExtensions are the extensions of OOXML packages, which an encrypted one keeps on an OLE2
// compound file.
var ooxmlExtensions = map[string]bool{
	".docx": true, ".docm": true, ".dotx": true, ".dotm": true,
	".xlsx": true, ".xlsm": true, ".xltx": true, ".xltm": true, ".xlsb": true,
	".pptx": true, ".pptm": true, ".potx": true, ".potm": true, ".ppsx": true,
}

// mismatch checks the signature against the name's extension. An OOXML extension on an OLE2
// compound file holding an EncryptedPackage is an encrypted package, as Office writes one, not a
// mismatch.
func (in *inspection) mismatch() {
	if in.kind == route.OLE2 && ooxmlExtensions[in.ext] && in.encryptedPackage {
		in.flag(OOXMLEncrypted, "OLE2 compound file with an EncryptedPackage stream")
		return
	}
	want, ok := extensionKinds[in.ext]
	if !ok || slices.Contains(want, in.kind) {
		return
	}
	got := string(in.kind)
	if in.kind == route.Unknown {
		got = "no signature"
	}
	in.flag(SignatureMismatch, fmt.Sprintf("extension %s, %s", in.ext, got))
}
