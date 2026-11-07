// Package sevenzip lists and extracts archives through 7-Zip's 7zz: a 7z archive, a zstd stream,
// a Microsoft cabinet, and a ZIP's entries of methods archive/zip doesn't read, such as
// Deflate64, implode, LZMA, xz, zstd, and PPMd. It provides pre-inference preparation only: the
// dispatch package's archive stage reads these kinds through it. Its types build on any
// platform, on the standard library alone; its Reader builds on Linux only, on this module's
// tool package, and the program it runs is a tool.Tool the caller built: 7-Zip's 7zz,
// SevenZipSpec declaring it, minimum 24.01, the first release whose zstd decoder reads a zstd
// stream and a ZIP's zstd entries.
//
// A Reader, built over a Config of the Tool, runs 7zz for two things, each one run under the
// caller's context. List reads an archive's headers through 7zz's technical listing, -slt, and
// returns its Listing: its type and each item, a directory among them, as an Entry, its stored
// name, its declared size and CRC-32, whether it is encrypted, its method, the Unix mode its
// attributes carry, and a link's target, in the archive's order. Extract writes one item's
// content to the caller's writer, 7zz writing to its standard output alone, -so: 7zz is never
// given a path to write to, so an item's stored name, a traversing or absolute one included, is
// only ever a name. The item is named by its stored name, matched whole: -spd keeps it from being
// a wildcard, and -i! from being a list file. The caller holds what it reads to the listing's size
// and CRC-32, and enforces any limit on it as it reads: a write that fails stops 7zz. A zstd
// stream's one item has no name, and is extracted with none.
//
// Every run is told its archive's format, -t, so 7zz never guesses one, and given an empty
// password, -p, so it never prompts for one: an archive whose headers are encrypted fails to
// list, and an encrypted item to extract, with ErrEncrypted, which a caller reports as encrypted.
// A file 7zz can't open as the format named fails with ErrNotArchive. A run 7zz ends with a
// warning, exit code 1, has succeeded. Any other failure names 7zz's own error lines, and wraps
// the run's *tool.ExitError.
//
// The listing is parsed line by line, each property's value running to its line's end, so a
// stored name with a line break in it isn't read whole; the item then fails to match, and the
// caller's size check records it. Extracting one item of a solid 7z block decodes the block from
// its start, so an archive's items read one at a time cost more than one extraction of them all.
//
// Its exported identifiers:
//
//   - [SevenZipSpec] declares 7-Zip's 7zz for either runner's constructor.
//   - [SevenZipMinimum] is the oldest 7zz the package accepts, 24.01.
//   - [Config] is the Tool a Reader is built over.
//   - [Reader] lists and extracts through 7zz; [NewReader] builds one.
//   - [Reader.List] lists an archive, given a [ListRequest], into a [Listing] of [Entry] items.
//   - [Reader.Extract] writes one item's content, given an [ExtractRequest], to a writer.
//   - [Entry.IsSymlink] and [Entry.IsDevice] report what an item's mode or link makes it.
//   - [Format] names the format 7zz reads: [SevenZ], [Zstd], [Cabinet], or [ZIP].
//   - [ErrNoSevenZip], [ErrEncrypted], [ErrNotArchive], [ErrRequest], and [ErrListing] are the
//     causes of a refusal or a failure.
package sevenzip
