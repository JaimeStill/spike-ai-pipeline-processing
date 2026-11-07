package inspect

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
)

// tar reads a tar's headers, as zip reads a ZIP's central directory, extracting no entry, and
// checks each entry's name, type, and declared size. archive/tar seeks past each entry's
// content, so only the headers are read. A header archive/tar can't read, a truncated tar's
// among them, is [TarUnreadable], and the entries after it go unchecked.
func (in *inspection) tar(ctx context.Context) error {
	tr := tar.NewReader(io.NewSectionReader(in.file, 0, in.size))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil && !errors.Is(err, tar.ErrInsecurePath):
			// ErrInsecurePath comes with its header, which tarEntry checks itself.
			in.flag(TarUnreadable, err.Error())
			return nil
		}
		in.tarEntry(h)
	}
}

// tarEntry checks one entry's name, its type, and its size. The bomb rule reads the size the
// header declares against the whole tar's, since a tar stores an entry's bytes as they are: only
// a sparse entry, whose holes aren't stored, or a header that lies, declares more than the tar
// holds. The stage that extracts the entry enforces the same limits on the bytes it writes.
func (in *inspection) tarEntry(h *tar.Header) {
	quoted := fmt.Sprintf("entry %q", h.Name)
	if traverses(h.Name) {
		in.flag(TarTraversal, quoted)
	}
	if absolute(h.Name) {
		in.flag(TarAbsolutePath, quoted)
	}
	switch h.Typeflag {
	case tar.TypeSymlink:
		in.flag(TarSymlink, fmt.Sprintf("%s to %q", quoted, h.Linkname))
	case tar.TypeLink:
		in.flag(TarHardlink, fmt.Sprintf("%s to %q", quoted, h.Linkname))
	case tar.TypeChar:
		in.flag(TarDevice, fmt.Sprintf("character device %s, %d:%d", quoted, h.Devmajor, h.Devminor))
	case tar.TypeBlock:
		in.flag(TarDevice, fmt.Sprintf("block device %s, %d:%d", quoted, h.Devmajor, h.Devminor))
	case tar.TypeFifo:
		in.flag(TarDevice, fmt.Sprintf("FIFO %s", quoted))
	}
	if h.Size < 0 {
		return
	}
	expanded, whole := uint64(h.Size), uint64(max(in.size, 1))
	if ratio := float64(expanded) / float64(whole); expanded > in.opts.BombSize && ratio > in.opts.BombRatio {
		in.flag(TarBomb, fmt.Sprintf("%s: %d bytes declared in a %d-byte tar, %.0f:1", quoted, expanded, in.size, ratio))
	}
}
