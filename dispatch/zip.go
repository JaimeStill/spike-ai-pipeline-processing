package dispatch

import (
	"archive/zip"
	"compress/bzip2"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
)

// zipBzip2 is the ZIP compression method of a bzip2 entry, 12, which archive/zip names no
// constant for.
const zipBzip2 uint16 = 12

// zipDecompressors are the archive/zip decompressors the stage reads a ZIP entry's compressed
// bytes through, by method: archive/zip's own deflate, and compress/bzip2 for bzip2. They are the
// stage's, applied to an entry's raw bytes, so its own rules hold the bytes to the central
// directory; none is registered with archive/zip, whose registry is the process's. A stored
// entry's bytes need none, and an entry of any other method is read through 7-Zip.
var zipDecompressors = map[uint16]zip.Decompressor{
	zip.Deflate: flate.NewReader,
	zipBzip2:    func(r io.Reader) io.ReadCloser { return io.NopCloser(bzip2.NewReader(r)) },
}

// zip reads the ZIP at p.Path through archive/zip, one entry at a time, yielding each entry's
// item.
func (s *ArchiveStage) zip(ctx context.Context, p Part, dir string, yield func(Item, error) bool) {
	zr, err := zip.OpenReader(p.Path)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		yield(Item{}, fmt.Errorf("archive stage: %w", err))
		return
	}
	defer func() { _ = zr.Close() }() // read only: a close error loses nothing
	files, encrypted := 0, 0
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			yield(Item{}, err)
			return
		}
		if f.Mode().IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		files++
		if f.Flags&0x1 != 0 {
			encrypted++
		}
		item, err := s.zipEntry(ctx, p.Path, f, dir)
		if err != nil {
			yield(Item{}, err)
			return
		}
		if !yield(item, nil) {
			return
		}
	}
	if encrypted > 0 {
		yield(Item{}, fmt.Errorf("archive stage: %d of %d entries: %w", encrypted, files, ErrEncrypted))
	}
}

// zipEntry is what an entry the stage can't write is recorded as: its stored name, its
// compression method, its sizes as the central directory declares them, the bytes written before
// its writing stopped, and why it has no part, with the inspect check, its class, and its
// evidence, when a check names it.
type zipEntry struct {
	Name       string `json:"name"`
	Method     uint16 `json:"method"`
	Compressed uint64 `json:"compressed"`
	Declared   uint64 `json:"declared"`
	Written    uint64 `json:"written,omitzero"`
	Reason     string `json:"reason"`
	Check      string `json:"check,omitzero"`
	Class      string `json:"class,omitzero"`
	Evidence   string `json:"evidence,omitzero"`
}

// zipEntry returns f's item, of the ZIP at archive: its part, written into dir by its SHA-256, or
// its record. An error is the stage's own, the directory unwritable or ctx ending; an entry's
// content never fails it.
func (s *ArchiveStage) zipEntry(ctx context.Context, archive string, f *zip.File, dir string) (Item, error) {
	where := "zip entry " + f.Name
	rec := zipEntry{Name: f.Name, Method: f.Method, Compressed: f.CompressedSize64, Declared: f.UncompressedSize64}
	recorded := func(rec zipEntry) (Item, error) {
		return unwritten(where, rec, f.Name, rec.Reason, rec.Check, rec.Class, rec.Evidence)
	}
	quoted := fmt.Sprintf("entry %q", f.Name)
	_, standard := zipDecompressors[f.Method]
	switch {
	case f.Flags&0x1 != 0:
		rec.Reason, rec.Check, rec.Class, rec.Evidence = "encrypted", string(inspect.ZIPEncrypted), string(inspect.Active), quoted
		return recorded(rec)
	case f.Method != zip.Store && !standard && s.sevenZip == nil:
		rec.Reason = fmt.Sprintf("compression method %d has no processor yet without 7-Zip", f.Method)
		return recorded(rec)
	}

	path, written, why, err := s.zipWrite(ctx, archive, f, dir)
	switch {
	case err != nil:
		return Item{}, err
	case errors.Is(why, errBomb):
		rec.Written, rec.Reason = written, "past the bomb thresholds as written"
		rec.Check, rec.Class = string(inspect.ZIPBomb), string(inspect.Unsafe)
		rec.Evidence = fmt.Sprintf("%s: %d bytes written from %d, %.0f:1, declared %d; past %d bytes and %g:1",
			quoted, written, f.CompressedSize64, float64(written)/float64(max(f.CompressedSize64, 1)),
			f.UncompressedSize64, s.size, s.ratio)
		return recorded(rec)
	case why != nil:
		rec.Written, rec.Reason = written, why.Error()
		return recorded(rec)
	}
	return PartItem(Found{Path: path, Name: f.Name, Where: where}), nil
}

// zipWrite writes f's content into dir by extract, and holds the bytes written to the central
// directory's size and CRC-32. A stored entry's raw bytes are its content, and a deflated or
// bzip2 entry's are decompressed by its zipDecompressors entry; any other method's entry is
// extracted alone from archive, the ZIP's file, by 7-Zip, to its standard output. why is the
// reason the content has no file, errBomb, a mismatch with the central directory, or content
// that doesn't decompress, its partial file removed; err is the stage's own failure.
func (s *ArchiveStage) zipWrite(ctx context.Context, archive string, f *zip.File, dir string) (path string, written uint64, why, err error) {
	var fill func(io.Writer) error
	switch decompress, standard := zipDecompressors[f.Method]; {
	case f.Method == zip.Store || standard:
		raw, err := f.OpenRaw()
		if err != nil {
			return "", 0, fmt.Errorf("unreadable: %w", err), nil
		}
		r := raw
		if standard {
			rc := decompress(raw)
			defer func() { _ = rc.Close() }() // a decompressor's close frees it; it reports nothing
			r = rc
		}
		fill = copyFrom(r)
	default:
		fill = func(w io.Writer) error {
			return s.sevenZip.Extract(ctx, sevenzip.ExtractRequest{Path: archive, Format: sevenzip.ZIP, Name: f.Name}, w)
		}
	}
	return s.extract(ctx, fill, dir, f.CompressedSize64, func(w *bombWriter, fillErr error) error {
		switch {
		case fillErr != nil:
			return fmt.Errorf("doesn't decompress: %w", fillErr)
		case w.n != f.UncompressedSize64:
			return fmt.Errorf("%d bytes written, %d declared", w.n, f.UncompressedSize64)
		case f.CRC32 != 0 && w.crc.Sum32() != f.CRC32:
			// archive/zip checks a CRC-32 the central directory declares, none when it declares 0.
			return fmt.Errorf("CRC-32 %08x, %08x declared", w.crc.Sum32(), f.CRC32)
		}
		return nil
	})
}
