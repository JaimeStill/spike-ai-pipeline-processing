//go:build linux

package document

import "strings"

// limitedBuffer is a writer that keeps the first limit bytes written to it and discards the rest,
// for a tool's output that the package reads only the start of: Assess's measures and Text's
// stderr.
type limitedBuffer struct {
	strings.Builder
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room > 0 {
		b.Builder.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
