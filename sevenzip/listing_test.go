package sevenzip_test

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
)

// listed lists an archive in format through a stand-in 7zz that prints out as its listing, so the
// listing is one no fixture gives.
func listed(t *testing.T, format sevenzip.Format, out string) (sevenzip.Listing, error) {
	t.Helper()
	r, err := sevenzip.NewReader(sevenzip.Config{SevenZip: standin(t, "26.05")})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(standinListingEnv, out)
	return r.List(t.Context(), sevenzip.ListRequest{Path: "t." + string(format), Format: format})
}

// slt is a technical listing as 7zz writes one, its banner, archive properties, and items: a
// directory, a file, a symbolic link, a FIFO, an encrypted file, and an item with no Unix mode.
const slt = `
7-Zip (z) 26.05 (x64) : Copyright (c) 1999-2026 Igor Pavlov : 2026-10-21
 64-bit locale=en_US.UTF-8 Threads:22 OPEN_MAX:524288, ASM

Scanning the drive for archives:
1 file, 229 bytes (1 KiB)

Listing archive: t.7z

--
Path = t.7z
Type = 7z
Physical Size = 229
Headers Size = 202
Method = LZMA2:12 7zAES:19
Solid = +
Blocks = 1

----------
Path = docs
Size = 0
Packed Size = 0
Attributes = D drwxr-xr-x
CRC =
Encrypted = -
Method =
Block =

Path = docs/notes.txt
Size = 22
Packed Size = 27
Attributes = A -rw-r--r--
CRC = C389908F
Encrypted = -
Method = LZMA2:12
Block = 0

Path = passwd
Size = 11
Packed Size =
Attributes = A lrwxrwxrwx
CRC = 291FB90A
Encrypted = -
Method = LZMA2:12
Block = 0

Path = pipe
Size = 0
Attributes = A prw-r--r--
CRC =
Encrypted = -
Method =

Path = secret.txt
Size = 38
Attributes = A -rw-r--r--
CRC = 361CAAB9
Encrypted = +
Method = LZMA2:12 7zAES:19

Path = C:/evil.txt
Size = 15
Attributes = A
Method = MSZip
`

// TestListParses: the archive's type, size, and method, and each item's name, size, CRC-32,
// encryption, method, and Unix mode, a directory by its attributes, are read; an item with no
// Unix mode has none, and an empty value is no value.
func TestListParses(t *testing.T) {
	l, err := listed(t, sevenzip.SevenZ, slt)
	if err != nil {
		t.Fatal(err)
	}
	if l.Type != "7z" || l.PhysicalSize != 229 || l.Method != "LZMA2:12 7zAES:19" || len(l.Entries) != 6 {
		t.Fatalf("listing %+v", l)
	}
	want := []sevenzip.Entry{
		{Path: "docs", Folder: true, Sized: true, Mode: fs.ModeDir | 0o755},
		{Path: "docs/notes.txt", Size: 22, Sized: true, Packed: 27, CRC: 0xC389908F, HasCRC: true, Method: "LZMA2:12", Mode: 0o644},
		{Path: "passwd", Size: 11, Sized: true, CRC: 0x291FB90A, HasCRC: true, Method: "LZMA2:12", Mode: fs.ModeSymlink | 0o777},
		{Path: "pipe", Sized: true, Mode: fs.ModeNamedPipe | 0o644},
		{Path: "secret.txt", Size: 38, Sized: true, CRC: 0x361CAAB9, HasCRC: true, Encrypted: true, Method: "LZMA2:12 7zAES:19", Mode: 0o644},
		{Path: "C:/evil.txt", Size: 15, Sized: true, Method: "MSZip"},
	}
	for i, w := range want {
		if l.Entries[i] != w {
			t.Errorf("entry %d:\n got %+v\nwant %+v", i, l.Entries[i], w)
		}
	}
	if !l.Entries[2].IsSymlink() || l.Entries[2].IsDevice() || !l.Entries[3].IsDevice() || l.Entries[1].IsSymlink() {
		t.Error("IsSymlink or IsDevice misread a mode")
	}
}

// TestListParseErrors: a listing with no items section, an item line that isn't a property,
// and a size that isn't a number are refused, with sevenzip.ErrListing.
func TestListParseErrors(t *testing.T) {
	for name, out := range map[string]string{
		"no items":    "--\nPath = a.7z\nType = 7z\n",
		"not a pair":  "--\nType = 7z\n----------\nPath = a\nnot a property\n",
		"bad size":    "--\nType = 7z\n----------\nPath = a\nSize = many\n",
		"bad CRC":     "--\nType = 7z\n----------\nPath = a\nCRC = XYZ\n",
		"bad packing": "--\nType = 7z\n----------\nPath = a\nPacked Size = -1\n",
	} {
		if _, err := listed(t, sevenzip.SevenZ, out); !errors.Is(err, sevenzip.ErrListing) {
			t.Errorf("%s: %v, want sevenzip.ErrListing", name, err)
		}
	}
}

// TestListZstdSize: a zstd stream's unsized item takes the content size its Method reports, and an
// item with a size, or a Method with none, is left as it is.
func TestListZstdSize(t *testing.T) {
	zstd := func(method, size string) sevenzip.Entry {
		t.Helper()
		out := "--\nPath = t.zst\nType = zstd\nMethod = " + method + "\n----------\nPath = t\nSize = " + size + "\n"
		l, err := listed(t, sevenzip.Zstd, out)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Entries) != 1 {
			t.Fatalf("%d entries, want 1", len(l.Entries))
		}
		return l.Entries[0]
	}
	declared := "header-open-only: NO-XXH64 wnd-desc-log-MAX:17 wnd-MAX:128KiB content-size-frame-max:4294967296 content-size-total:4294967296"
	if e := zstd(declared, ""); !e.Sized || e.Size != 4<<30 {
		t.Errorf("declared: %+v", e)
	}
	if e := zstd("header-open-only: XXH64 wnd-desc-log-MAX:21 wnd-MAX:2MiB unknown-content-size", ""); e.Sized {
		t.Errorf("unknown size: %+v", e)
	}
	if e := zstd(declared, "7"); e.Size != 7 {
		t.Errorf("listed size replaced: %+v", e)
	}
}
