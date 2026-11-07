package dispatch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// tarTypes name the tar entry types the stage writes no part for, in its records' words.
var tarTypes = map[byte]string{
	tar.TypeSymlink: "symbolic link", tar.TypeLink: "hard link", tar.TypeChar: "character device",
	tar.TypeBlock: "block device", tar.TypeFifo: "FIFO",
}

// tarChecks are the inspect checks that flag the tar entry types the stage writes no part for.
var tarChecks = map[byte]inspect.Check{
	tar.TypeSymlink: inspect.TarSymlink, tar.TypeLink: inspect.TarHardlink, tar.TypeChar: inspect.TarDevice,
	tar.TypeBlock: inspect.TarDevice, tar.TypeFifo: inspect.TarDevice,
}

// tarEntry is what a tar entry the stage can't write is recorded as: its stored name, its type,
// its link's target, its size as its header declares it, the bytes written before its writing
// stopped, and why it has no part, with the inspect check, its class, and its evidence, when a
// check names it.
type tarEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Link     string `json:"link,omitzero"`
	Declared int64  `json:"declared"`
	Written  uint64 `json:"written,omitzero"`
	Reason   string `json:"reason"`
	Check    string `json:"check,omitzero"`
	Class    string `json:"class,omitzero"`
	Evidence string `json:"evidence,omitzero"`
}

// tar reads the tar at p.Path through archive/tar, one entry at a time, in the tar's order,
// yielding each entry's item. A regular file, a sparse one among them, is written by its
// SHA-256 and found; a directory or a global header is no part; any other entry is a record. A
// header the reader can't read fails the part, after the entries before it.
func (s *ZipStage) tar(ctx context.Context, p Part, dir string, yield func(Item, error) bool) {
	f, err := os.Open(p.Path)
	if err != nil {
		yield(Item{}, fmt.Errorf("zip stage: %w", err))
		return
	}
	defer func() { _ = f.Close() }() // read only: a close error loses nothing
	tr := tar.NewReader(f)
	for {
		if err := ctx.Err(); err != nil {
			yield(Item{}, err)
			return
		}
		h, err := tr.Next()
		switch {
		case errors.Is(err, io.EOF):
			return
		case err != nil && !errors.Is(err, tar.ErrInsecurePath):
			// ErrInsecurePath comes with its header: the name is only ever a name here.
			yield(Item{}, fmt.Errorf("zip stage: tar: %w", err))
			return
		}
		if h.Typeflag == tar.TypeDir || h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		item, err := s.tarEntry(ctx, h, tr, uint64(p.Size), dir)
		if err != nil {
			yield(Item{}, err)
			return
		}
		if !yield(item, nil) {
			return
		}
	}
}

// tarEntry returns h's item, its content read from r: its part, written into dir by its
// SHA-256, or its record. The bomb rule weighs the bytes written against whole, the tar's size,
// since an entry's bytes come from no more than the whole tar: only a sparse entry's holes, read
// as zeros, can pass it. An error is the stage's own, the directory unwritable or ctx ending; an
// entry's content never fails it.
func (s *ZipStage) tarEntry(ctx context.Context, h *tar.Header, r io.Reader, whole uint64, dir string) (Item, error) {
	where := "tar entry " + h.Name
	rec := tarEntry{Name: h.Name, Type: tarTypes[h.Typeflag], Link: h.Linkname, Declared: h.Size}
	recorded := func(rec tarEntry) (Item, error) {
		return unwritten(where, rec, h.Name, rec.Reason, rec.Check, rec.Class, rec.Evidence)
	}
	quoted := fmt.Sprintf("entry %q", h.Name)
	if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeGNUSparse {
		if check, ok := tarChecks[h.Typeflag]; ok {
			// Inspect quarantined the tar for it; a policy let it proceed. No link and no node
			// is made, and a link's target is only its record's text.
			rec.Reason, rec.Check, rec.Class, rec.Evidence = rec.Type+": no part", string(check), string(inspect.Unsafe), quoted
			return recorded(rec)
		}
		rec.Type = fmt.Sprintf("%q", h.Typeflag)
		rec.Reason = fmt.Sprintf("entry type %q has no processor yet", h.Typeflag)
		return recorded(rec)
	}
	rec.Type = "file"

	path, written, why, err := s.extract(ctx, r, dir, whole, func(w *bombWriter, copyErr error) error {
		switch {
		case copyErr != nil:
			return fmt.Errorf("doesn't read: %w", copyErr)
		case int64(w.n) != h.Size:
			return fmt.Errorf("%d bytes written, %d declared", w.n, h.Size)
		}
		return nil
	})
	switch {
	case err != nil:
		return Item{}, err
	case errors.Is(why, errBomb):
		rec.Written, rec.Reason = written, "past the bomb thresholds as written"
		rec.Check, rec.Class = string(inspect.TarBomb), string(inspect.Unsafe)
		rec.Evidence = fmt.Sprintf("%s: %d bytes written from a %d-byte tar, %.0f:1, declared %d; past %d bytes and %g:1",
			quoted, written, whole, float64(written)/float64(max(whole, 1)), h.Size, s.size, s.ratio)
		return recorded(rec)
	case why != nil:
		rec.Written, rec.Reason = written, why.Error()
		return recorded(rec)
	}
	return PartItem(Found{Path: path, Name: h.Name, Where: where}), nil
}

// gzipContent is what a gzip stream the stage can't write the content of is recorded as: the
// name its content was to have, the stream's size, the bytes written before its writing
// stopped, and why it has no part, with the inspect check, its class, and its evidence, when a
// check names it.
type gzipContent struct {
	Name       string `json:"name"`
	Compressed uint64 `json:"compressed"`
	Written    uint64 `json:"written,omitzero"`
	Reason     string `json:"reason"`
	Check      string `json:"check,omitzero"`
	Class      string `json:"class,omitzero"`
	Evidence   string `json:"evidence,omitzero"`
}

// gzip decompresses the gzip stream at p.Path through compress/gzip, its members concatenated as
// gzip concatenates them, into one part, written into dir by its SHA-256 and found, named by its
// header's name else by the stream's own name's stem, "gzip content <name>". The dispatcher
// routes it again, so a .tar.gz's content is a tar, read by this stage below it. The bomb rule
// weighs the bytes written against the stream's size. A stream whose header doesn't read fails
// the part; content that doesn't decompress, or passes the bomb rule, is a record.
func (s *ZipStage) gzip(ctx context.Context, p Part, dir string, yield func(Item, error) bool) {
	f, err := os.Open(p.Path)
	if err != nil {
		yield(Item{}, fmt.Errorf("zip stage: %w", err))
		return
	}
	defer func() { _ = f.Close() }() // read only: a close error loses nothing
	zr, err := gzip.NewReader(f)
	if err != nil {
		yield(Item{}, fmt.Errorf("zip stage: gzip: %w", err))
		return
	}
	defer func() { _ = zr.Close() }() // a gzip reader's close reports nothing it hasn't
	name := zr.Name
	if name == "" {
		name = gzipStem(p.Name)
	}
	where := "gzip content " + name
	compressed := uint64(p.Size)
	path, written, why, err := s.extract(ctx, zr, dir, compressed, func(_ *bombWriter, copyErr error) error {
		if copyErr != nil {
			return fmt.Errorf("doesn't decompress: %w", copyErr)
		}
		return nil
	})
	rec := gzipContent{Name: name, Compressed: compressed, Written: written}
	switch {
	case err != nil:
		yield(Item{}, err)
		return
	case errors.Is(why, errBomb):
		rec.Reason, rec.Check, rec.Class = "past the bomb thresholds as written", string(inspect.GzipBomb), string(inspect.Unsafe)
		rec.Evidence = fmt.Sprintf("content %q: %d bytes written from %d, %.0f:1; past %d bytes and %g:1",
			name, written, compressed, float64(written)/float64(max(compressed, 1)), s.size, s.ratio)
	case why != nil:
		rec.Reason = why.Error()
	default:
		yield(PartItem(Found{Path: path, Name: name, Where: where}), nil)
		return
	}
	yield(unwritten(where, rec, name, rec.Reason, rec.Check, rec.Class, rec.Evidence))
}

// gzipStem returns the name a gzip stream's content takes when its header names none: the
// stream's name without its .gz, or with a .tgz's .tar, as gzip -d names it; a name with
// neither is kept.
func gzipStem(name string) string {
	ext := path.Ext(name)
	switch strings.ToLower(ext) {
	case ".gz":
		if stem := strings.TrimSuffix(name, ext); stem != "" {
			return stem
		}
	case ".tgz":
		return strings.TrimSuffix(name, ext) + ".tar"
	}
	return name
}
