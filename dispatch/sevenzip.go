package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
)

// sevenZipKind is how the archive stage reads one of the kinds it reads through 7-Zip: what it
// is, in a reason's words, the format 7-Zip is told to read it as, the words its entries are
// found under, and the inspect checks its entries' records name.
type sevenZipKind struct {
	what   string
	format sevenzip.Format
	where  string
	bomb   inspect.Check
	// encrypted, symlink, hardlink, and device are the checks a record of such an entry names,
	// none for a kind whose format can't carry it.
	encrypted, symlink, hardlink, device inspect.Check
}

// sevenZipKinds are the kinds the archive stage reads through 7-Zip alone.
var sevenZipKinds = map[route.Kind]sevenZipKind{
	route.SevenZip: {
		what: "a 7z archive", format: sevenzip.SevenZ, where: "7z entry ", bomb: inspect.SevenZipBomb,
		encrypted: inspect.SevenZipEncrypted, symlink: inspect.SevenZipSymlink,
		hardlink: inspect.SevenZipHardlink, device: inspect.SevenZipDevice,
	},
	route.MSCF: {what: "a cabinet", format: sevenzip.Cabinet, where: "cabinet entry ", bomb: inspect.CabBomb},
	route.Zstd: {what: "a zstd stream", format: sevenzip.Zstd, where: "zstd content ", bomb: inspect.ZstdBomb},
}

// listedEntry is what an entry of an archive 7-Zip reads that the stage can't write is recorded
// as: its stored name, its type, a link's target, its method, its size as its listing declares
// it, the bytes written before its writing stopped, and why it has no part, with the inspect
// check, its class, and its evidence, when a check names it.
type listedEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Link     string `json:"link,omitzero"`
	Method   string `json:"method,omitzero"`
	Declared uint64 `json:"declared,omitzero"`
	Written  uint64 `json:"written,omitzero"`
	Reason   string `json:"reason"`
	Check    string `json:"check,omitzero"`
	Class    string `json:"class,omitzero"`
	Evidence string `json:"evidence,omitzero"`
}

// listedType returns what 7-Zip's listing says e is, in inspect's words, and a link's target.
func listedType(e sevenzip.Entry) (inspect.EntryType, string) {
	switch {
	case e.Folder:
		return inspect.DirEntry, ""
	case e.HardLink != "":
		return inspect.HardlinkEntry, e.HardLink
	case e.IsSymlink():
		return inspect.SymlinkEntry, e.Symlink
	case e.IsDevice():
		return inspect.DeviceEntry, ""
	}
	return inspect.FileEntry, ""
}

// listedChecks returns the listing's verdict on entries, the archive p's, through
// inspect.CheckListing under the stage's options: nil when they may be extracted, else the
// *QuarantineError holding the flags, or the stage's failure.
func (s *ArchiveStage) listedChecks(p Part, entries []inspect.Listed) error {
	rep, err := inspect.CheckListing(p.Kind, p.Size, entries, s.opts)
	switch {
	case err != nil:
		return fmt.Errorf("archive stage: %w", err)
	case rep.Verdict == inspect.Quarantine:
		return &QuarantineError{Flags: rep.Flags}
	}
	return nil
}

// list lists the archive p through 7-Zip as k's format, ending the stage with ErrEncrypted when
// its headers are encrypted.
func (s *ArchiveStage) list(ctx context.Context, p Part, k sevenZipKind) (sevenzip.Listing, error) {
	l, err := s.sevenZip.List(ctx, sevenzip.ListRequest{Path: p.Path, Format: k.format})
	switch {
	case errors.Is(err, sevenzip.ErrEncrypted):
		return l, fmt.Errorf("archive stage: %s whose headers are encrypted: %w: %w", k.what, ErrEncrypted, err)
	case err != nil:
		return l, fmt.Errorf("archive stage: %w", err)
	}
	return l, nil
}

// listed reads the 7z archive or cabinet at p.Path through 7-Zip: its listing, checked before
// anything is extracted, then each file, one at a time, in the listing's order, yielding each
// one's item. An archive with an encrypted entry ends with ErrEncrypted once the rest are
// dispatched.
func (s *ArchiveStage) listed(ctx context.Context, p Part, dir string, k sevenZipKind, yield func(Item, error) bool) {
	l, err := s.list(ctx, p, k)
	if err != nil {
		yield(Item{}, err)
		return
	}
	checked := make([]inspect.Listed, len(l.Entries))
	for i, e := range l.Entries {
		typ, link := listedType(e)
		checked[i] = inspect.Listed{Name: e.Path, Type: typ, Link: link, Declared: e.Size, Sized: e.Sized, Encrypted: e.Encrypted}
	}
	if err := s.listedChecks(p, checked); err != nil {
		yield(Item{}, err)
		return
	}
	files, encrypted := 0, 0
	for _, e := range l.Entries {
		if err := ctx.Err(); err != nil {
			yield(Item{}, err)
			return
		}
		if e.Folder {
			continue
		}
		files++
		if e.Encrypted {
			encrypted++
		}
		item, err := s.listedEntry(ctx, p, k, e, dir)
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

// listedEntry returns e's item, of the archive p, read through 7-Zip as k's format: its part,
// written into dir by its SHA-256, or its record. The bomb rule weighs the bytes 7zz writes
// against the whole archive's size, since a 7z's or cabinet's entries share their packed blocks,
// and the bytes are held to the listing's size and CRC-32. An error is the stage's own, the
// directory unwritable or ctx ending; an entry's content never fails it.
func (s *ArchiveStage) listedEntry(ctx context.Context, p Part, k sevenZipKind, e sevenzip.Entry, dir string) (Item, error) {
	where := k.where + e.Path
	typ, link := listedType(e)
	rec := listedEntry{Name: e.Path, Type: string(typ), Link: link, Method: e.Method, Declared: e.Size}
	recorded := func(rec listedEntry) (Item, error) {
		return unwritten(where, rec, e.Path, rec.Reason, rec.Check, rec.Class, rec.Evidence)
	}
	quoted := fmt.Sprintf("entry %q", e.Path)
	if e.Encrypted {
		rec.Reason, rec.Evidence = "encrypted", quoted
		if k.encrypted != "" {
			rec.Check, rec.Class = string(k.encrypted), string(inspect.Active)
		}
		return recorded(rec)
	}
	if typ != inspect.FileEntry {
		// The listing's checks quarantined the archive for it; a policy let it proceed. No link
		// and no node is made, and a link's target is only its record's text.
		check := map[inspect.EntryType]inspect.Check{
			inspect.SymlinkEntry: k.symlink, inspect.HardlinkEntry: k.hardlink, inspect.DeviceEntry: k.device,
		}[typ]
		rec.Reason = rec.Type + ": no part"
		if check != "" {
			rec.Check, rec.Class, rec.Evidence = string(check), string(inspect.Unsafe), quoted
		}
		return recorded(rec)
	}

	whole := uint64(p.Size)
	path, written, why, err := s.extract(ctx, func(w io.Writer) error {
		return s.sevenZip.Extract(ctx, sevenzip.ExtractRequest{Path: p.Path, Format: k.format, Name: e.Path}, w)
	}, dir, whole, func(w *bombWriter, fillErr error) error {
		switch {
		case fillErr != nil:
			return fmt.Errorf("doesn't extract: %w", fillErr)
		case e.Sized && w.n != e.Size:
			return fmt.Errorf("%d bytes written, %d listed", w.n, e.Size)
		case e.HasCRC && w.crc.Sum32() != e.CRC:
			return fmt.Errorf("CRC-32 %08x, %08x listed", w.crc.Sum32(), e.CRC)
		}
		return nil
	})
	switch {
	case err != nil:
		return Item{}, err
	case errors.Is(why, errBomb):
		rec.Written, rec.Reason = written, "past the bomb thresholds as written"
		rec.Check, rec.Class = string(k.bomb), string(inspect.Unsafe)
		rec.Evidence = fmt.Sprintf("%s: %d bytes written from a %d-byte archive, %.0f:1, listed %d; past %d bytes and %g:1",
			quoted, written, whole, float64(written)/float64(max(whole, 1)), e.Size, s.size, s.ratio)
		return recorded(rec)
	case why != nil:
		rec.Written, rec.Reason = written, why.Error()
		return recorded(rec)
	}
	return PartItem(Found{Path: path, Name: e.Path, Where: where}), nil
}

// zstd decompresses the zstd stream at p.Path through 7-Zip into one part, written into dir by
// its SHA-256 and found, named by the stream's own name's stem, "zstd content <name>"; the
// dispatcher routes it again, so a .tar.zst's content is a tar, read by this stage below it. A
// content size the frame header declares is checked first, as a listing is. The bomb rule weighs
// the bytes 7zz writes against the stream's size. Content that doesn't decompress, or passes the
// bomb rule, is a record.
func (s *ArchiveStage) zstd(ctx context.Context, p Part, dir string, yield func(Item, error) bool) {
	k := sevenZipKinds[route.Zstd]
	l, err := s.list(ctx, p, k)
	if err != nil {
		yield(Item{}, err)
		return
	}
	name := streamStem(p.Name, ".zst", ".tzst")
	// A zstd stream lists one item, its content, with no name; its size is its frame header's,
	// when the header declares one.
	var content sevenzip.Entry
	if len(l.Entries) > 0 {
		content = l.Entries[0]
	}
	checked := []inspect.Listed{{Name: name, Type: inspect.FileEntry, Declared: content.Size, Sized: content.Sized}}
	if err := s.listedChecks(p, checked); err != nil {
		yield(Item{}, err)
		return
	}
	where := k.where + name
	compressed := uint64(p.Size)
	path, written, why, err := s.extract(ctx, func(w io.Writer) error {
		return s.sevenZip.Extract(ctx, sevenzip.ExtractRequest{Path: p.Path, Format: k.format}, w)
	}, dir, compressed, func(w *bombWriter, fillErr error) error {
		switch {
		case fillErr != nil:
			return fmt.Errorf("doesn't decompress: %w", fillErr)
		case content.Sized && w.n != content.Size:
			return fmt.Errorf("%d bytes written, %d declared", w.n, content.Size)
		}
		return nil
	})
	rec := listedEntry{Name: name, Type: string(inspect.FileEntry), Declared: content.Size, Written: written}
	switch {
	case err != nil:
		yield(Item{}, err)
		return
	case errors.Is(why, errBomb):
		rec.Reason, rec.Check, rec.Class = "past the bomb thresholds as written", string(inspect.ZstdBomb), string(inspect.Unsafe)
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
