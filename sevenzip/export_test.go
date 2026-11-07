package sevenzip

// ParseListing and ZstdSize expose the listing's parser and a zstd stream's sizing to the tests,
// which hold them to listings no fixture gives, such as a malformed one, without running 7zz.
var (
	ParseListing = parseListing
	ZstdSize     = zstdSize
)
