package dispatch

import (
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
	"path"
	"path/filepath"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
)

// archiveVersion is the version of the archive stage: this package's, with the version of the
// 7-Zip it is given, if any, in its configuration's hash. Version 2 read tar and gzip, which
// version 1 recorded as no processor yet; version 3 reads, through 7-Zip, the zstd streams, 7z
// archives, cabinets, and ZIP entries of other methods version 2 recorded as no processor yet,
// and ZIP's bzip2 entries.
const archiveVersion = "3"

// SevenZip is what an [ArchiveStage] reads 7-Zip's kinds through: sevenzip's Reader, which lists
// an archive and extracts one item to a writer, through 7zz's standard output alone.
type SevenZip interface {
	List(ctx context.Context, req sevenzip.ListRequest) (sevenzip.Listing, error)
	Extract(ctx context.Context, req sevenzip.ExtractRequest, w io.Writer) error
}

// ArchiveConfig is what an [ArchiveStage] is built over: the inspection options whose bomb
// thresholds and policy it enforces, and, optionally, 7-Zip.
type ArchiveConfig struct {
	// Inspect is the dispatcher's Config.Inspect: its BombRatio and BombSize, inspect's
	// defaults when zero, are the thresholds an archive entry's, or a gzip or zstd stream's,
	// written bytes may not pass both of, and its Policy is the one a 7-Zip listing's flags are
	// weighed under, as inspect.CheckListing weighs them.
	Inspect inspect.Options
	// SevenZip, when not nil, reads a zstd stream, a 7z archive, a cabinet, and a ZIP entry of a
	// method archive/zip doesn't read; nil leaves those no processor yet.
	SevenZip SevenZip
	// Version names the 7-Zip behind SevenZip, as [Versions] gives it; required with SevenZip,
	// and part of the stage's identity.
	Version string
}

// ArchiveStage prepares route.Archive's parts: a ZIP that is neither an office document nor an
// ODF package, a tar, and a gzip stream, on the standard library, and, given 7-Zip, a zstd
// stream, a 7z archive, and a cabinet not named .onepkg.
//
// A ZIP is read through archive/zip, one entry at a time, in the central directory's order. Each
// file entry is written into the part's directory under its content's SHA-256, streamed into a
// temporary file while it is hashed, then renamed, and is a [Found] part, the dispatcher's to
// route and inspect again, named by its stored name and found "zip entry <name>". The stored name
// is never a path: a name with a ".." element, an absolute one, or one with a drive letter, which
// inspect quarantines the ZIP for, is a name all the same under a policy that lets the ZIP
// proceed, its content written by its hash inside the part's directory. A symbolic link's entry
// is its target's text, written as a file; no link is made. A directory's entry is skipped. A
// stored, deflated, or bzip2 entry is decompressed by the standard library, bzip2 through
// compress/bzip2 as an archive/zip decompressor; an entry of any other method, such as
// Deflate64, implode, LZMA, xz, zstd, or PPMd, is read through 7-Zip, extracted alone to 7zz's
// standard output, under the same rules.
//
// An entry the stage can't write is a [RecordOutput] named by its place, "zip entry <name>", its
// Meta naming the entry and why, never dropped:
//
//   - an encrypted entry, which archive/zip can't decrypt, its Meta's check "zip-encrypted" and
//     its outcome "encrypted";
//   - an entry whose written bytes pass both the bomb ratio, against its compressed size, and the
//     bomb size, the thresholds of [ArchiveConfig.Inspect], whatever its central directory
//     declares: its writing stops there and its partial file is removed, its Meta's check
//     "zip-bomb", its class "unsafe", and its outcome "quarantined", with the evidence;
//   - an entry of a method the standard library doesn't read, with no 7-Zip;
//   - an entry whose bytes don't match its central directory, its CRC-32 or its size, or that
//     doesn't decompress.
//
// A ZIP with an encrypted entry ends, once every other entry is dispatched, with [ErrEncrypted],
// naming how many of its entries are encrypted, so its outcome is [Encrypted], its event holding
// the record of each encrypted entry and its readable entries' parts.
//
// A tar is read through archive/tar, one entry at a time, in the tar's order, its headers as
// inspect reads them. Each regular file, a sparse one among them, is written as a ZIP's file
// entry is and found "tar entry <name>", named by its stored name, which is never a path. A
// directory is no part. A symbolic or hard link, a device, or a FIFO, which inspect quarantines
// the tar for, is a record under a policy that lets the tar proceed, its Meta's check inspect's,
// and no link or node is made. An entry whose written bytes pass both thresholds, the ratio
// against the whole tar's size, since an entry's bytes come from no more than the tar, as only a
// sparse entry's holes can, is stopped and recorded as a ZIP's is, its check "tar-bomb". An
// entry of another type, or whose content doesn't read, is a record, and a header that can't be
// read fails the tar once the entries before it are dispatched.
//
// A gzip stream is decompressed through compress/gzip into one part, written by its SHA-256 and
// found "gzip content <name>", named by its header's name, else by the stream's own name without
// its .gz, or with a .tgz's .tar. The dispatcher routes it again, so a .tar.gz is a gzip part
// holding a tar part. Content whose written bytes pass both thresholds, the ratio against the
// stream's size, is stopped and recorded as a ZIP entry's is, its check "gzip-bomb", and content
// that doesn't decompress is a record; a header that doesn't read fails the part.
//
// A 7z archive and a cabinet are read through 7-Zip: listed first, by 7zz's technical listing,
// and the listing checked by inspect.CheckListing under the rules inspect holds a tar to, a
// traversing, absolute, or drive letter name, a link, a device, and an entry past both
// thresholds by its declared size against the archive's, before anything is extracted. A listing
// whose flags come to quarantine, under [ArchiveConfig.Inspect]'s policy, ends the stage with a
// [QuarantineError] holding them, so the archive is [Quarantined] with that evidence and nothing
// of it is extracted. Otherwise each file is extracted alone, in the listing's order, to 7zz's
// standard output, written as a ZIP's entry is, and found "7z entry <name>" or "cabinet entry
// <name>"; a directory is no part. The bomb rule weighs the bytes read from 7zz against the whole
// archive's size, its check "7z-bomb" or "cab-bomb", and the bytes are held to the listing's
// size and CRC-32. A link or device let through by policy is a record, as a tar's is. An
// encrypted 7z entry is a record, its check "7z-encrypted" and its outcome "encrypted", and the
// archive [Encrypted] once its readable entries are dispatched; a 7z whose headers are encrypted,
// so nothing of it can be listed, ends with [ErrEncrypted] at once.
//
// A zstd stream is read through 7-Zip into one part, as a gzip stream is, found "zstd content
// <name>", named by the stream's own name without its .zst, or with a .tzst's .tar, and routed
// again, so a .tar.zst is a zstd part holding a tar part. A content size its frame header
// declares is checked first, as a listing is, its check "zstd-bomb", and the bomb rule weighs the
// bytes read from 7zz against the stream's size.
//
// With no 7-Zip, a zstd stream, a 7z archive, and a cabinet have no processor yet, and a ZIP
// entry of a method the standard library doesn't read is a record; the rest is unaffected.
type ArchiveStage struct {
	ratio    float64
	size     uint64
	opts     inspect.Options
	sevenZip SevenZip
	id       Identity
}

// NewArchiveStage builds an ArchiveStage over cfg, refusing a negative bomb ratio, a policy
// inspect refuses, and a SevenZip with no Version.
func NewArchiveStage(cfg ArchiveConfig) (*ArchiveStage, error) {
	ratio, size := cfg.Inspect.BombRatio, cfg.Inspect.BombSize
	if ratio < 0 {
		return nil, fmt.Errorf("dispatch: archive stage: bomb ratio %g", ratio)
	}
	// CheckListing refuses what Inspect refuses; an empty listing checks the options alone.
	if _, err := inspect.CheckListing(route.Zstd, 0, nil, cfg.Inspect); err != nil {
		return nil, fmt.Errorf("dispatch: archive stage: %w", err)
	}
	if ratio == 0 {
		ratio = inspect.DefaultBombRatio
	}
	if size == 0 {
		size = inspect.DefaultBombSize
	}
	if cfg.SevenZip != nil && cfg.Version == "" {
		return nil, errors.New("dispatch: archive stage: 7-Zip with no version")
	}
	if cfg.SevenZip == nil {
		cfg.Version = ""
	}
	hash, err := HashConfig(struct {
		Adapter   string  `json:"adapter"`
		BombRatio float64 `json:"bomb_ratio"`
		BombSize  uint64  `json:"bomb_size"`
		SevenZip  string  `json:"seven_zip,omitzero"`
	}{archiveVersion, ratio, size, cfg.Version})
	if err != nil {
		return nil, err
	}
	return &ArchiveStage{
		ratio: ratio, size: size, opts: cfg.Inspect, sevenZip: cfg.SevenZip,
		id: Identity{Name: "archive", Version: archiveVersion, Config: hash},
	}, nil
}

// Identity returns the stage's identity: "archive", this package's version of it, and a hash of
// its bomb thresholds and its 7-Zip's version.
func (s *ArchiveStage) Identity() Identity { return s.id }

// Prepare reads the archive at p.Path by its kind, as the type's doc says.
func (s *ArchiveStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		// Each kind the stage reads has its reader here; 7-Zip's kinds need 7-Zip.
		if k, ok := sevenZipKinds[p.Kind]; ok && s.sevenZip == nil {
			yield(Item{}, fmt.Errorf("archive stage: %s without 7-Zip: %w", k.what, ErrNoProcessor))
			return
		}
		switch p.Kind {
		case route.ZIP:
			s.zip(ctx, p, dir, yield)
		case route.Tar:
			s.tar(ctx, p, dir, yield)
		case route.Gzip:
			s.gzip(ctx, p, dir, yield)
		case route.Zstd:
			s.zstd(ctx, p, dir, yield)
		case route.SevenZip, route.MSCF:
			s.listed(ctx, p, dir, sevenZipKinds[p.Kind], yield)
		default:
			yield(Item{}, fmt.Errorf("archive stage: content of kind %s: %w", p.Kind, ErrNoProcessor))
		}
	}
}

// unwritten returns the record of an entry the stage wrote no part for, rec, named by its place,
// where: its Meta names the entry, by name, and why, and, when a check names it, the check, its
// class, and its evidence, with the outcome a bomb, quarantined, or an encrypted entry,
// encrypted, comes to.
func unwritten(where string, rec any, name, reason, check, class, evidence string) (Item, error) {
	meta := map[string]string{"name": name, "reason": reason}
	if check != "" {
		meta["check"], meta["class"], meta["evidence"] = check, class, evidence
	}
	switch inspect.Check(check) {
	case inspect.ZIPBomb, inspect.TarBomb, inspect.GzipBomb, inspect.SevenZipBomb, inspect.CabBomb, inspect.ZstdBomb:
		meta["outcome"] = string(Quarantined)
	case inspect.ZIPEncrypted, inspect.SevenZipEncrypted:
		meta["outcome"] = string(Encrypted)
	}
	o, err := record(where, rec, meta)
	return OutputItem(o), err
}

// errBomb is a written entry's word that its bytes passed both bomb thresholds.
var errBomb = errors.New("past the bomb thresholds")

// copyFrom returns a fill for extract that copies r.
func copyFrom(r io.Reader) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	}
}

// extract writes the bytes fill writes into dir, streamed into a temporary file while their
// SHA-256 and CRC-32 are taken, then renamed to their SHA-256, and returns the path and the bytes
// written. fill copies a reader, or runs 7zz with its standard output the writer it is given. The
// bomb rule weighs the bytes written against compressed, the bytes they come from; past both
// thresholds the writer refuses the next write, and fill, a reader's copy or 7zz, stops. whole,
// given the writer and fill's error once the bytes are written, says why they aren't the content
// whole, or nil when they are. why is the reason the content has no file, errBomb or whole's
// word, its partial file removed; err is the stage's own failure.
func (s *ArchiveStage) extract(ctx context.Context, fill func(io.Writer) error, dir string, compressed uint64,
	whole func(w *bombWriter, fillErr error) error,
) (path string, written uint64, why, err error) {
	tmp, err := os.CreateTemp(dir, "entry-*")
	if err != nil {
		return "", 0, nil, fmt.Errorf("archive stage: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp.Name()) // a temporary file of the part's own directory
		}
	}()
	w := &bombWriter{
		ctx: ctx, w: tmp, sum: sha256.New(), crc: crc32.NewIEEE(),
		compressed: max(compressed, 1), ratio: s.ratio, size: s.size,
	}
	fillErr := fill(w)
	if err := tmp.Close(); err != nil {
		return "", w.n, nil, fmt.Errorf("archive stage: %w", err)
	}
	switch {
	case ctx.Err() != nil:
		return "", w.n, nil, ctx.Err()
	// A bomb is the writer's own word: 7zz, stopped by the refused write, fails in its own words.
	case w.bomb:
		return "", w.n, errBomb, nil
	case w.writeErr != nil:
		return "", w.n, nil, fmt.Errorf("archive stage: %w", w.writeErr)
	}
	if why := whole(w, fillErr); why != nil {
		return "", w.n, why, nil
	}
	path = filepath.Join(dir, hex.EncodeToString(w.sum.Sum(nil)))
	// The same content met earlier in this archive may still be at path, kept by the caller:
	// its bytes are these, so it is found again rather than replaced.
	if _, err := os.Lstat(path); err == nil {
		return path, w.n, nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", w.n, nil, fmt.Errorf("archive stage: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", w.n, nil, fmt.Errorf("archive stage: %w", err)
	}
	keep = true
	return path, w.n, nil, nil
}

// bombWriter writes an entry's bytes to w while hashing them, and stops with errBomb, setting
// bomb, once they pass both the bomb size and the bomb ratio against compressed, the bytes they
// come from, or with ctx's error once it ends. writeErr is w's own failure.
type bombWriter struct {
	ctx        context.Context
	w          io.Writer
	sum        hash.Hash
	crc        hash.Hash32
	n          uint64
	compressed uint64
	ratio      float64
	size       uint64
	bomb       bool
	writeErr   error
}

func (b *bombWriter) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.bomb {
		return 0, errBomb
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
		b.bomb = true
		return n, errBomb
	}
	return n, nil
}

// streamStem returns the name a single stream's content takes from the stream's own name: the
// name without ext, or, for short, an extension naming a tar in the stream, the name with .tar in
// its place, as gzip -d and zstd -d name it; a name with neither is kept.
func streamStem(name, ext, short string) string {
	got := path.Ext(name)
	switch strings.ToLower(got) {
	case ext:
		if stem := strings.TrimSuffix(name, got); stem != "" {
			return stem
		}
	case short:
		return strings.TrimSuffix(name, got) + ".tar"
	}
	return name
}
