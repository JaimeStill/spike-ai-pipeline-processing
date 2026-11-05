package dispatch

import (
	"archive/zip"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// zipVersion is the version of the zip stage, which runs no tool but archive/zip: this
// package's.
const zipVersion = "1"

// ZipConfig is what a [ZipStage] is built over: the inspection options whose zip bomb
// thresholds it enforces on the bytes it writes.
type ZipConfig struct {
	// Inspect is the dispatcher's Config.Inspect: its BombRatio and BombSize, inspect's
	// defaults when zero, are the thresholds an entry's written bytes may not pass both of. Its
	// Policy is the dispatcher's, not the stage's.
	Inspect inspect.Options
}

// ZipStage prepares a ZIP that is neither an office document nor an ODF package, route.Archive's
// ZIP parts, through archive/zip, one entry at a time, in the central directory's order. Each
// file entry is written into the part's directory under its content's SHA-256, streamed into a
// temporary file while it is hashed, then renamed, and is a [Found] part, the dispatcher's to
// route and inspect again, named by its stored name and found "zip entry <name>". The stored name
// is never a path: a name with a ".." element, an absolute one, or one with a drive letter, which
// inspect quarantines the ZIP for, is a name all the same under a policy that lets the ZIP
// proceed, its content written by its hash inside the part's directory. A symbolic link's entry
// is its target's text, written as a file; no link is made. A directory's entry is skipped.
//
// An entry the stage can't write is a [RecordOutput] named by its place, "zip entry <name>", its
// Meta naming the entry and why, never dropped:
//
//   - an encrypted entry, which archive/zip can't decrypt, its Meta's check "zip-encrypted" and
//     its outcome "encrypted";
//   - an entry whose written bytes pass both the bomb ratio, against its compressed size, and the
//     bomb size, the thresholds of [ZipConfig.Inspect], whatever its central directory declares:
//     its writing stops there and its partial file is removed, its Meta's check "zip-bomb", its
//     class "unsafe", and its outcome "quarantined", with the evidence;
//   - an entry whose compression archive/zip doesn't read, neither stored nor deflated; and
//   - an entry whose bytes don't match its central directory, its CRC-32 or its size, or that
//     doesn't decompress.
//
// A ZIP with an encrypted entry ends, once every other entry is dispatched, with [ErrEncrypted],
// naming how many of its entries are encrypted, so its outcome is [Encrypted], its event holding
// the record of each encrypted entry and its readable entries' parts. A tar, gzip, zstd, or 7z
// archive, or a cabinet, route.Archive's other kinds, has no processor yet.
type ZipStage struct {
	ratio float64
	size  uint64
	id    Identity
}

// NewZipStage builds a ZipStage over cfg, refusing a negative bomb ratio.
func NewZipStage(cfg ZipConfig) (*ZipStage, error) {
	ratio, size := cfg.Inspect.BombRatio, cfg.Inspect.BombSize
	if ratio < 0 {
		return nil, fmt.Errorf("dispatch: zip stage: bomb ratio %g", ratio)
	}
	if ratio == 0 {
		ratio = inspect.DefaultBombRatio
	}
	if size == 0 {
		size = inspect.DefaultBombSize
	}
	hash, err := HashConfig(struct {
		Adapter   string  `json:"adapter"`
		BombRatio float64 `json:"bomb_ratio"`
		BombSize  uint64  `json:"bomb_size"`
	}{zipVersion, ratio, size})
	if err != nil {
		return nil, err
	}
	return &ZipStage{ratio: ratio, size: size, id: Identity{Name: "zip", Version: zipVersion, Config: hash}}, nil
}

// Identity returns the stage's identity: "zip", this package's version of it, and a hash of its
// bomb thresholds.
func (s *ZipStage) Identity() Identity { return s.id }

// unarchived names the archive kinds the stage has no processor for yet, the formats task's.
var unarchived = map[route.Kind]string{
	route.Tar:      "a tar archive",
	route.Gzip:     "a gzip stream",
	route.Zstd:     "a zstd stream",
	route.SevenZip: "a 7z archive",
	route.MSCF:     "a cabinet",
}

// Prepare reads the ZIP at p.Path, as the type's doc says.
func (s *ZipStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		if p.Kind != route.ZIP {
			what := unarchived[p.Kind]
			if what == "" {
				what = "content of kind " + string(p.Kind)
			}
			yield(Item{}, fmt.Errorf("zip stage: %s: %w", what, ErrNoProcessor))
			return
		}
		zr, err := zip.OpenReader(p.Path)
		if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
			yield(Item{}, fmt.Errorf("zip stage: %w", err))
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
			item, err := s.entry(ctx, f, dir)
			if err != nil {
				yield(Item{}, err)
				return
			}
			if !yield(item, nil) {
				return
			}
		}
		if encrypted > 0 {
			yield(Item{}, fmt.Errorf("zip stage: %d of %d entries: %w", encrypted, files, ErrEncrypted))
		}
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

// entry returns f's item: its part, written into dir by its SHA-256, or its record. An error is
// the stage's own, the directory unwritable or ctx ending; an entry's content never fails it.
func (s *ZipStage) entry(ctx context.Context, f *zip.File, dir string) (Item, error) {
	where := "zip entry " + f.Name
	rec := zipEntry{Name: f.Name, Method: f.Method, Compressed: f.CompressedSize64, Declared: f.UncompressedSize64}
	recorded := func(rec zipEntry) (Item, error) {
		meta := map[string]string{"name": f.Name, "reason": rec.Reason}
		if rec.Check != "" {
			meta["check"], meta["class"], meta["evidence"] = rec.Check, rec.Class, rec.Evidence
		}
		switch rec.Check {
		case string(inspect.ZIPBomb):
			meta["outcome"] = string(Quarantined)
		case string(inspect.ZIPEncrypted):
			meta["outcome"] = string(Encrypted)
		}
		o, err := record(where, rec, meta)
		return OutputItem(o), err
	}
	quoted := fmt.Sprintf("entry %q", f.Name)
	switch {
	case f.Flags&0x1 != 0:
		rec.Reason, rec.Check, rec.Class, rec.Evidence = "encrypted", string(inspect.ZIPEncrypted), string(inspect.Active), quoted
		return recorded(rec)
	case f.Method != zip.Store && f.Method != zip.Deflate:
		rec.Reason = fmt.Sprintf("compression method %d has no processor yet", f.Method)
		return recorded(rec)
	}

	path, written, why, err := s.write(ctx, f, dir)
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

// errBomb is a written entry's word that its bytes passed both bomb thresholds.
var errBomb = errors.New("past the bomb thresholds")

// write writes f's content into dir, streamed into a temporary file while its SHA-256 and CRC-32
// are taken, then renamed to its SHA-256, and returns its path and the bytes written. why is the
// reason the content has no file, errBomb or a mismatch with the central directory, its partial
// file removed; err is the stage's own failure.
func (s *ZipStage) write(ctx context.Context, f *zip.File, dir string) (path string, written uint64, why, err error) {
	raw, err := f.OpenRaw()
	if err != nil {
		return "", 0, fmt.Errorf("unreadable: %w", err), nil
	}
	r := raw
	if f.Method == zip.Deflate {
		fr := flate.NewReader(raw)
		defer func() { _ = fr.Close() }() // a flate reader's close frees it; it reports nothing
		r = fr
	}
	tmp, err := os.CreateTemp(dir, "entry-*")
	if err != nil {
		return "", 0, nil, fmt.Errorf("zip stage: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp.Name()) // a temporary file of the part's own directory
		}
	}()
	w := &bombWriter{
		ctx: ctx, w: tmp, sum: sha256.New(), crc: crc32.NewIEEE(),
		compressed: max(f.CompressedSize64, 1), ratio: s.ratio, size: s.size,
	}
	_, copyErr := io.Copy(w, r)
	if err := tmp.Close(); err != nil {
		return "", w.n, nil, fmt.Errorf("zip stage: %w", err)
	}
	switch {
	case ctx.Err() != nil:
		return "", w.n, nil, ctx.Err()
	case errors.Is(copyErr, errBomb):
		return "", w.n, errBomb, nil
	case w.writeErr != nil:
		return "", w.n, nil, fmt.Errorf("zip stage: %w", w.writeErr)
	case copyErr != nil:
		return "", w.n, fmt.Errorf("doesn't decompress: %w", copyErr), nil
	case w.n != f.UncompressedSize64:
		return "", w.n, fmt.Errorf("%d bytes written, %d declared", w.n, f.UncompressedSize64), nil
	case f.CRC32 != 0 && w.crc.Sum32() != f.CRC32:
		// archive/zip checks a CRC-32 the central directory declares, none when it declares 0.
		return "", w.n, fmt.Errorf("CRC-32 %08x, %08x declared", w.crc.Sum32(), f.CRC32), nil
	}
	path = filepath.Join(dir, hex.EncodeToString(w.sum.Sum(nil)))
	// The same content met earlier in this ZIP may still be at path, kept by the caller: its
	// bytes are these, so it is found again rather than replaced.
	if _, err := os.Lstat(path); err == nil {
		return path, w.n, nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", w.n, nil, fmt.Errorf("zip stage: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", w.n, nil, fmt.Errorf("zip stage: %w", err)
	}
	keep = true
	return path, w.n, nil, nil
}

// bombWriter writes an entry's bytes to w while hashing them, and stops with errBomb once they
// pass both the bomb size and the bomb ratio against the entry's compressed size, or with ctx's
// error once it ends. writeErr is w's own failure.
type bombWriter struct {
	ctx        context.Context
	w          io.Writer
	sum        hash.Hash
	crc        hash.Hash32
	n          uint64
	compressed uint64
	ratio      float64
	size       uint64
	writeErr   error
}

func (b *bombWriter) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := b.w.Write(p)
	b.n += uint64(n)
	b.sum.Write(p[:n])
	b.crc.Write(p[:n])
	if err != nil {
		b.writeErr = err
		return n, err
	}
	if b.n > b.size && float64(b.n)/float64(b.compressed) > b.ratio {
		return n, errBomb
	}
	return n, nil
}
