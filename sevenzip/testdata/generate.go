//go:build ignore

// generate writes the sevenzip package's generated fixtures into generated/, beside this file: the
// zstd streams and cabinets written by hand, every byte fixed, and the 7z archives written by
// 7zz, which mise pins, from files whose content, mode, and order are fixed and with no times
// stored, so a rerun writes the same bytes for every fixture but the two encrypted 7z archives,
// whose salt and IV 7zz draws at random. Run it from sevenzip/testdata with
// `mise exec -- go run generate.go`; the check never runs it, and reads the committed files.
package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// out is the directory the fixtures are written into.
const out = "generated"

// password is the encrypted 7z archives' password, which no test gives.
const password = "infected"

// The onenote package's copy of onenote.rs's section group, two of whose files the cabinet
// packages as a .onepkg would.
const sectionGroup = "../../onenote/testdata/onenote/fixtures/onenote-rs/New Section Group"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	cleanTar, err := os.ReadFile("../../inspect/testdata/clean.tar")
	if err != nil {
		return err
	}
	section, err := os.ReadFile(filepath.Join(sectionGroup, "New Section 1.one"))
	if err != nil {
		return err
	}
	toc, err := os.ReadFile(filepath.Join(sectionGroup, "Open Notebook.onetoc2"))
	if err != nil {
		return err
	}
	zeros := make([]byte, 8<<20)
	files := map[string][]byte{
		// clean.tar.zst is inspect's clean.tar as a zstd stream of raw blocks, declaring no size.
		"clean.tar.zst": zstdOf(cleanTar, -1),
		// bomb.zst is 8 MiB of zeros in RLE blocks, about 32,000:1, declaring no size: past the
		// ratio, under the default size, so a test lowers the size to see the rule fire.
		"bomb.zst": zstdOf(zeros, -1),
		// declared-bomb.zst's frame header declares 4 GiB; its content is 1 KiB of zeros.
		"declared-bomb.zst": zstdOf(make([]byte, 1024), 4<<30),
		// notebook.cab packages a OneNote section and its notebook as a .onepkg does, an MSZIP
		// cabinet, named .cab.
		"notebook.cab": cabOf(cabFile{"Open Notebook.onetoc2", toc}, cabFile{"New Section 1.one", section}),
		"traversal.cab": cabOf(
			cabFile{"notes.txt", []byte("a cabinet's ordinary entry\n")},
			cabFile{`docs\..\..\evil.txt`, []byte("written outside the extraction directory\n")},
		),
		"absolute.cab": cabOf(
			cabFile{`\tmp\evil.txt`, []byte("an absolute path\n")},
			cabFile{`C:\evil.txt`, []byte("a drive letter\n")},
		),
		// bomb.cab holds 8 MiB of zeros, about 570:1: under the default ratio and size, so a test
		// lowers both to see the rule fire.
		"bomb.cab": cabOf(cabFile{"zeros.bin", zeros}),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
			return err
		}
	}
	return sevenZips(zeros)
}

// zstdOf returns content as one zstd frame: RLE blocks for runs of one byte, raw blocks for the
// rest, each at most 128 KiB, with no checksum, and the content size declare in its header,
// when declare isn't negative.
func zstdOf(content []byte, declare int64) []byte {
	const blockMax = 128 << 10
	b := []byte{0x28, 0xb5, 0x2f, 0xfd}
	// The frame header descriptor: no single segment, so a window descriptor follows, 128 KiB
	// (exponent 7); an 8-byte content size, flag 3, when one is declared.
	if declare >= 0 {
		b = append(b, 0xc0, 0x38)
		b = binary.LittleEndian.AppendUint64(b, uint64(declare))
	} else {
		b = append(b, 0x00, 0x38)
	}
	for start := 0; start < len(content); start += blockMax {
		block := content[start:min(start+blockMax, len(content))]
		last := uint32(0)
		if start+blockMax >= len(content) {
			last = 1
		}
		if bytes.Count(block, block[:1]) == len(block) {
			header := last | 1<<1 | uint32(len(block))<<3
			b = append(b, byte(header), byte(header>>8), byte(header>>16), block[0])
			continue
		}
		header := last | uint32(len(block))<<3
		b = append(b, byte(header), byte(header>>8), byte(header>>16))
		b = append(b, block...)
	}
	return b
}

// cabFile is one file of a cabinet: its stored name, Windows separators and all, and content.
type cabFile struct {
	name string
	body []byte
}

// cabOf returns a cabinet of files, in one MSZIP folder: each 32 KiB of the folder's
// concatenated content is a data block, "CK" and a whole deflate stream, with no checksum. Every
// file is dated 2026-11-05 00:00 and marked archive.
func cabOf(files ...cabFile) []byte {
	const (
		headerLen = 36
		folderLen = 8
		blockMax  = 32 << 10
		dosDate   = (2026-1980)<<9 | 11<<5 | 5
		attrArch  = 0x20
	)
	var folder []byte
	var entries []byte
	for _, f := range files {
		entries = binary.LittleEndian.AppendUint32(entries, uint32(len(f.body)))
		entries = binary.LittleEndian.AppendUint32(entries, uint32(len(folder)))
		entries = binary.LittleEndian.AppendUint16(entries, 0) // folder 0
		entries = binary.LittleEndian.AppendUint16(entries, dosDate)
		entries = binary.LittleEndian.AppendUint16(entries, 0) // 00:00
		entries = binary.LittleEndian.AppendUint16(entries, attrArch)
		entries = append(entries, f.name...)
		entries = append(entries, 0)
		folder = append(folder, f.body...)
	}
	var data []byte
	blocks := 0
	for start := 0; start < len(folder); start += blockMax {
		chunk := folder[start:min(start+blockMax, len(folder))]
		var packed bytes.Buffer
		packed.WriteString("CK")
		fw, err := flate.NewWriter(&packed, flate.BestCompression)
		if err != nil {
			panic(err)
		}
		fw.Write(chunk)
		fw.Close()
		data = binary.LittleEndian.AppendUint32(data, 0) // no checksum
		data = binary.LittleEndian.AppendUint16(data, uint16(packed.Len()))
		data = binary.LittleEndian.AppendUint16(data, uint16(len(chunk)))
		data = append(data, packed.Bytes()...)
		blocks++
	}
	filesAt := headerLen + folderLen
	dataAt := filesAt + len(entries)
	total := dataAt + len(data)
	var b []byte
	b = append(b, "MSCF"...)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(total))
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(filesAt))
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = append(b, 3, 1) // version 1.3
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(files)))
	b = binary.LittleEndian.AppendUint16(b, 0) // no flags
	b = binary.LittleEndian.AppendUint16(b, 0x5a7a)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(dataAt))
	b = binary.LittleEndian.AppendUint16(b, uint16(blocks))
	b = binary.LittleEndian.AppendUint16(b, 1) // MSZIP
	b = append(b, entries...)
	return append(b, data...)
}

// sevenZips writes the 7z fixtures through 7zz, from files in a temporary directory.
func sevenZips(zeros []byte) error {
	src, err := os.MkdirTemp("", "sevenzip-fixtures-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(src)
	outAbs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	write := func(name string, body []byte) error {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
		return os.Chmod(path, 0o644)
	}
	for name, body := range map[string][]byte{
		"docs/notes.txt": []byte("a 7z's ordinary entry\n"),
		"docs/pixel.png": []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x03\x00\x00\x00\x01\x08\x02\x00\x00\x00\x94\x82\x83\xe3"),
		"notes.txt":      []byte("an ordinary entry\n"),
		"evil.txt":       []byte("written outside the extraction directory\n"),
		"absolute.txt":   []byte("an absolute path\n"),
		"drive.txt":      []byte("a drive letter\n"),
		"secret.txt":     []byte("encrypted with the fixtures' password\n"),
		"zeros.bin":      zeros,
	} {
		if err := write(name, body); err != nil {
			return err
		}
	}
	if err := os.Chmod(filepath.Join(src, "docs"), 0o755); err != nil {
		return err
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "passwd")); err != nil {
		return err
	}
	if err := mkfifo(filepath.Join(src, "pipe")); err != nil {
		return err
	}
	// Every archive is single-threaded, with no times stored, so it is the same bytes on a rerun.
	add := []string{"a", "-mtm-", "-mtc-", "-mta-", "-mmt=1", "-bd"}
	sz := func(args ...string) error {
		cmd := exec.Command("7zz", args...)
		cmd.Dir = src
		cmd.Stderr = os.Stderr
		if b, err := cmd.Output(); err != nil {
			return fmt.Errorf("7zz %v: %w\n%s", args, err, b)
		}
		return nil
	}
	archive := func(name string) string {
		path := filepath.Join(outAbs, name)
		_ = os.Remove(path)
		return path
	}
	steps := [][]string{
		// clean.7z: a directory, docs, and a text and a 3x1 PNG's signature under it.
		append(append([]string{}, add...), archive("clean.7z"), "docs"),
		// traversal.7z: an ordinary entry, and evil.txt renamed to a name with ".." elements.
		append(append([]string{}, add...), archive("traversal.7z"), "notes.txt", "evil.txt"),
		{"rn", filepath.Join(outAbs, "traversal.7z"), "evil.txt", "docs/../../evil.txt"},
		// absolute.7z: an absolute path and a drive letter, each a file renamed.
		append(append([]string{}, add...), archive("absolute.7z"), "absolute.txt", "drive.txt"),
		{"rn", filepath.Join(outAbs, "absolute.7z"), "absolute.txt", "/tmp/evil.txt"},
		{"rn", filepath.Join(outAbs, "absolute.7z"), "drive.txt", `C:\evil.txt`},
		// symlink.7z: an ordinary entry and passwd, a symbolic link to /etc/passwd, stored as a
		// link (-snl).
		append(append([]string{}, add...), "-snl", archive("symlink.7z"), "notes.txt", "passwd"),
		// device.7z: an ordinary entry and pipe, a FIFO, stored as one (-snl); a character or
		// block device needs root to make, so none is stored.
		append(append([]string{}, add...), "-snl", archive("device.7z"), "notes.txt", "pipe"),
		// bomb.7z: 8 MiB of zeros, about 6,000:1 against the archive.
		append(append([]string{}, add...), archive("bomb.7z"), "zeros.bin"),
		// encrypted.7z: an ordinary entry, then secret.txt added encrypted, its headers clear.
		append(append([]string{}, add...), archive("encrypted.7z"), "notes.txt"),
		append(append([]string{}, add...), "-p"+password, filepath.Join(outAbs, "encrypted.7z"), "secret.txt"),
		// encrypted-headers.7z: secret.txt with its headers encrypted too, so nothing lists.
		append(append([]string{}, add...), "-p"+password, "-mhe=on", archive("encrypted-headers.7z"), "secret.txt"),
	}
	for _, args := range steps {
		if err := sz(args...); err != nil {
			return err
		}
	}
	return nil
}

// mkfifo makes a FIFO at path, through mkfifo(1), so the generator needs no syscall package.
func mkfifo(path string) error {
	if b, err := exec.Command("mkfifo", "-m", "0644", path).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfifo: %w: %s", err, b)
	}
	return nil
}
