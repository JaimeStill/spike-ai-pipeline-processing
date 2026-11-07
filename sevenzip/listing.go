package sevenzip

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// Format names the archive format 7-Zip is told to read, its -t switch, so it never guesses one
// from the content or the name.
type Format string

// The formats the package reads.
const (
	// SevenZ is a 7z archive.
	SevenZ Format = "7z"
	// Zstd is a zstd stream: one item, its decompressed content, unnamed.
	Zstd Format = "zstd"
	// Cabinet is a Microsoft cabinet, the container a OneNote package is.
	Cabinet Format = "cab"
	// ZIP is a ZIP, read through 7-Zip for an entry whose method archive/zip doesn't read.
	ZIP Format = "zip"
)

// formats are the formats a request may name.
var formats = map[Format]bool{SevenZ: true, Zstd: true, Cabinet: true, ZIP: true}

// Entry is one item of an archive as 7-Zip's technical listing, -slt, gives it: each field its
// property's value, and a property the format doesn't carry left zero.
type Entry struct {
	// Path is the item's stored name, as 7-Zip lists it. It is only ever a name: nothing is
	// written to it. A zstd stream's one item has none.
	Path string
	// Folder is a directory: its Folder property is "+", or its attributes mark a directory.
	Folder bool
	// Size is the expanded size the archive declares, when Sized; a zstd stream declares none
	// unless its frame header carries its content size.
	Size  uint64
	Sized bool
	// Packed is the item's packed size, when the archive declares one for it; a 7z declares one
	// only for the first item of each solid block.
	Packed uint64
	// CRC is the CRC-32 the archive declares for the item's content, when HasCRC.
	CRC    uint32
	HasCRC bool
	// Encrypted is an item 7-Zip reports encrypted, its Encrypted property "+".
	Encrypted bool
	// Method is the item's compression method, as 7-Zip names it, such as "LZMA2:24" or
	// "Deflate64".
	Method string
	// Mode is the Unix mode the item's attributes carry, its type and permission bits; zero when
	// they carry none, as a cabinet's don't.
	Mode fs.FileMode
	// Symlink is the target its Symbolic Link property names, for a format that lists one.
	Symlink string
	// HardLink is the target its Hard Link property names, for a format that lists one.
	HardLink string
}

// IsSymlink reports whether the item is a symbolic link, by its mode or a Symbolic Link property.
func (e Entry) IsSymlink() bool { return e.Mode&fs.ModeSymlink != 0 || e.Symlink != "" }

// IsDevice reports whether the item is a node an extraction would make rather than a file: a
// character or block device, a FIFO, or a socket, by its mode.
func (e Entry) IsDevice() bool {
	return e.Mode&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0
}

// Listing is an archive as 7-Zip lists it: its type, its physical size, and its items, in the
// archive's order.
type Listing struct {
	// Type is the archive's type, as 7-Zip names it, such as "7z" or "Cab".
	Type string
	// PhysicalSize is the archive's size as 7-Zip read it, when it reports one.
	PhysicalSize uint64
	// Method is the archive's Method property, as 7-Zip gives it: the methods a 7z's blocks
	// use, or what 7-Zip read of a zstd frame's header, such as
	// "header-open-only: ... content-size-total:4294967296".
	Method string
	// Entries are the archive's items, directories among them, in the archive's order.
	Entries []Entry
}

// ErrListing is the cause of a listing the package can't parse.
var ErrListing = errors.New("unparsable listing")

// separator divides a technical listing's archive properties from its items' properties.
const separator = "----------"

// parseListing parses 7-Zip's technical listing, -slt: a banner, the archive's properties after
// a "--" line, then a "----------" line and each item's properties, a blank line ending each. A
// property is "Name = value"; a value runs to the end of its line, so a name with a line break in
// it isn't read whole.
func parseListing(out []byte) (Listing, error) {
	var l Listing
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	// section is where the scan is: 0 before the archive's properties, 1 among them, 2 among
	// the items'.
	section := 0
	var cur map[string]string
	flush := func() error {
		if cur == nil {
			return nil
		}
		e, err := entry(cur)
		if err != nil {
			return err
		}
		l.Entries = append(l.Entries, e)
		cur = nil
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case section == 0:
			if line == "--" {
				section = 1
			}
			continue
		case line == separator:
			section = 2
			continue
		}
		name, value, ok := strings.Cut(line, " = ")
		if !ok {
			// "Name =" with no value has no space after its "=".
			name, ok = strings.CutSuffix(line, " =")
		}
		if section == 1 {
			if ok {
				archiveProperty(&l, name, value)
			}
			continue
		}
		if line == "" {
			if err := flush(); err != nil {
				return Listing{}, err
			}
			continue
		}
		if !ok {
			return Listing{}, fmt.Errorf("%w: line %q", ErrListing, line)
		}
		if cur == nil {
			cur = map[string]string{}
		}
		cur[name] = value
	}
	if err := sc.Err(); err != nil {
		return Listing{}, fmt.Errorf("%w: %w", ErrListing, err)
	}
	if err := flush(); err != nil {
		return Listing{}, err
	}
	if section != 2 {
		return Listing{}, fmt.Errorf("%w: no items section", ErrListing)
	}
	return l, nil
}

// archiveProperty sets the archive's property name to value, of those the Listing keeps.
func archiveProperty(l *Listing, name, value string) {
	switch name {
	case "Type":
		l.Type = value
	case "Physical Size":
		if n, err := strconv.ParseUint(value, 10, 64); err == nil {
			l.PhysicalSize = n
		}
	case "Method":
		l.Method = value
	}
}

// contentSizeTotal is the word in a zstd stream's Method property that gives the content size its
// frame headers declare, as "content-size-total:4294967296".
const contentSizeTotal = "content-size-total:"

// zstdSize sets a zstd stream's one item's size, which 7-Zip's listing leaves empty when it reads
// the frame header alone, to the content size its Method property says the frame headers declare,
// when it says one and the item has none.
func zstdSize(l *Listing) {
	if len(l.Entries) != 1 || l.Entries[0].Sized {
		return
	}
	for field := range strings.FieldsSeq(l.Method) {
		if v, ok := strings.CutPrefix(field, contentSizeTotal); ok {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				l.Entries[0].Size, l.Entries[0].Sized = n, true
			}
			return
		}
	}
}

// entry makes an Entry of one item's properties.
func entry(props map[string]string) (Entry, error) {
	e := Entry{
		Path:      props["Path"],
		Encrypted: props["Encrypted"] == "+",
		Method:    props["Method"],
		Symlink:   props["Symbolic Link"],
		HardLink:  props["Hard Link"],
	}
	var err error
	if e.Size, e.Sized, err = number(props, "Size"); err != nil {
		return Entry{}, err
	}
	if e.Packed, _, err = number(props, "Packed Size"); err != nil {
		return Entry{}, err
	}
	if v := props["CRC"]; v != "" {
		crc, err := strconv.ParseUint(v, 16, 32)
		if err != nil {
			return Entry{}, fmt.Errorf("%w: %s's CRC %q", ErrListing, e.Path, v)
		}
		e.CRC, e.HasCRC = uint32(crc), true
	}
	attrs := props["Attributes"]
	e.Mode = unixMode(attrs)
	e.Folder = props["Folder"] == "+" || e.Mode.IsDir() || windowsDirectory(attrs)
	return e, nil
}

// windowsDirectory reports whether an Attributes property's Windows attributes, the letters
// before its Unix mode, as "D" in "D drwxr-xr-x", mark a directory. A property with none begins
// with a space.
func windowsDirectory(attrs string) bool {
	if attrs == "" || attrs[0] == ' ' {
		return false
	}
	letters, _, _ := strings.Cut(attrs, " ")
	return strings.Contains(letters, "D")
}

// number returns the decimal property name of props, and whether it has one; an empty value is
// none.
func number(props map[string]string, name string) (uint64, bool, error) {
	v := props[name]
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: %s %q", ErrListing, name, v)
	}
	return n, true, nil
}

// unixMode returns the Unix mode an Attributes property carries after its Windows attributes, as
// "A -rw-r--r--" or "D drwxr-xr-x" does, its type and permission bits; zero when it carries none.
func unixMode(attrs string) fs.FileMode {
	for field := range strings.FieldsSeq(attrs) {
		if len(field) != 10 || strings.Trim(field[1:], "rwxsStT-") != "" {
			continue
		}
		var mode fs.FileMode
		switch field[0] {
		case '-':
		case 'd':
			mode = fs.ModeDir
		case 'l':
			mode = fs.ModeSymlink
		case 'c':
			mode = fs.ModeDevice | fs.ModeCharDevice
		case 'b':
			mode = fs.ModeDevice
		case 'p':
			mode = fs.ModeNamedPipe
		case 's':
			mode = fs.ModeSocket
		default:
			continue
		}
		for i, c := range field[1:] {
			if c != '-' && c != 'S' && c != 'T' {
				mode |= 1 << (8 - i)
			}
		}
		return mode
	}
	return 0
}
