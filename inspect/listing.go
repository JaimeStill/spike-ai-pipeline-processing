package inspect

import (
	"fmt"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// EntryType names what a listed entry is, as the program that lists the archive reports it.
type EntryType string

// The entry types a listing names.
const (
	// FileEntry is a regular file.
	FileEntry EntryType = "file"
	// DirEntry is a directory.
	DirEntry EntryType = "directory"
	// SymlinkEntry is a symbolic link.
	SymlinkEntry EntryType = "symbolic link"
	// HardlinkEntry is a hard link.
	HardlinkEntry EntryType = "hard link"
	// DeviceEntry is a node an extraction would make rather than a file: a character or block
	// device, a FIFO, or a socket.
	DeviceEntry EntryType = "device"
)

// Listed is one entry of an archive's listing, as a program that reads the archive, such as
// 7-Zip, lists it before anything is extracted: its stored name, its type, a link's target, the
// expanded size it declares, when Sized, and whether it is encrypted.
type Listed struct {
	Name      string
	Type      EntryType
	Link      string
	Declared  uint64
	Sized     bool
	Encrypted bool
}

// listingChecks are the checks a kind's listing raises, each for one rule; a rule the kind's
// format can't carry, such as a cabinet's links, has none.
type listingChecks struct {
	traversal, absolute, symlink, hardlink, device, bomb, encrypted Check
}

// listings are the checks of each kind [CheckListing] reads.
var listings = map[route.Kind]listingChecks{
	route.SevenZip: {
		traversal: SevenZipTraversal, absolute: SevenZipAbsolutePath, symlink: SevenZipSymlink,
		hardlink: SevenZipHardlink, device: SevenZipDevice, bomb: SevenZipBomb, encrypted: SevenZipEncrypted,
	},
	route.MSCF: {traversal: CabTraversal, absolute: CabAbsolutePath, bomb: CabBomb},
	route.Zstd: {bomb: ZstdBomb},
}

// CheckListing checks an archive's listing, entries, as a program that reads the archive lists
// it, under the rules [Inspect] holds a tar's headers to: a name with a ".." element, an absolute
// one, or one that begins with a drive letter, by the same name checks; a symbolic or hard link;
// a device; and an entry past both bomb thresholds by the size it declares, the ratio against
// size, the whole archive's, since a 7z's or cabinet's entries share their packed blocks. An
// encrypted entry is of class Active. kind is the archive's: [route.SevenZip], [route.MSCF], or
// [route.Zstd], whose one entry is its content, checked by its declared size alone. It returns
// the report the flags come to under opts, its Kind kind, and an error for opts it refuses,
// matching [ErrOptions], or a kind it doesn't read.
//
// Inspect runs no program, so it never lists such an archive itself: the stage that reads the
// archive through its program calls CheckListing before it extracts anything, and quarantines
// the archive on the report's verdict.
func CheckListing(kind route.Kind, size int64, entries []Listed, opts Options) (Report, error) {
	opts, err := opts.check()
	if err != nil {
		return Report{}, err
	}
	checks, ok := listings[kind]
	if !ok {
		return Report{}, fmt.Errorf("inspect: no listing checks for kind %s", kind)
	}
	in := &inspection{size: size, kind: kind, opts: opts, found: map[Check]*finding{}}
	for _, e := range entries {
		in.listed(checks, e)
	}
	flags := in.flags()
	return Report{Kind: kind, Flags: flags, Verdict: opts.verdict(flags)}, nil
}

// listed checks one listed entry against the checks of its archive's kind.
func (in *inspection) listed(c listingChecks, e Listed) {
	quoted := fmt.Sprintf("entry %q", e.Name)
	raise := func(check Check, evidence string) {
		if check != "" {
			in.flag(check, evidence)
		}
	}
	if traverses(e.Name) {
		raise(c.traversal, quoted)
	}
	if absolute(e.Name) {
		raise(c.absolute, quoted)
	}
	// A link's target is its evidence when the listing names it; a 7z lists none, its target
	// being the entry's content.
	linked := quoted
	if e.Link != "" {
		linked = fmt.Sprintf("%s to %q", quoted, e.Link)
	}
	switch e.Type {
	case SymlinkEntry:
		raise(c.symlink, linked)
	case HardlinkEntry:
		raise(c.hardlink, linked)
	case DeviceEntry:
		raise(c.device, quoted)
	}
	if e.Encrypted {
		raise(c.encrypted, quoted)
	}
	if !e.Sized {
		return
	}
	whole := uint64(max(in.size, 1))
	if ratio := float64(e.Declared) / float64(whole); e.Declared > in.opts.BombSize && ratio > in.opts.BombRatio {
		raise(c.bomb, fmt.Sprintf("%s: %d bytes declared in a %d-byte archive, %.0f:1", quoted, e.Declared, in.size, ratio))
	}
}
