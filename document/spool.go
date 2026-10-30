//go:build linux

package document

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// spool copies pdf into a new file in dir, the working directory the caller owns, so a renderer
// can read it by path. It returns the file's path and a function that removes the file, which
// the caller runs when its call ends. An empty dir is refused rather than falling back to the
// system's temporary directory, and a failed copy removes what it wrote.
func spool(dir string, pdf io.Reader) (string, func() error, error) {
	if dir == "" {
		return "", nil, errors.New("document: spool: no working directory")
	}
	f, err := os.CreateTemp(dir, "spool-*.pdf")
	if err != nil {
		return "", nil, fmt.Errorf("document: spool: %w", err)
	}
	path := f.Name()
	remove := func() error { return os.Remove(path) }
	_, err = io.Copy(f, pdf)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", nil, errors.Join(fmt.Errorf("document: spool: %w", err), remove())
	}
	return path, remove, nil
}
