//go:build linux

package office

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// spool copies doc into a new file in dir, the working directory the caller owns, so office can
// read it by path. It returns the file's path and a function that removes the file, which the
// caller runs when its call ends. A failed copy removes what it wrote. office tells the format
// from the content, so the file has no extension.
func spool(dir string, doc io.Reader) (string, func() error, error) {
	f, err := os.CreateTemp(dir, "spool-*")
	if err != nil {
		return "", nil, fmt.Errorf("office: spool: %w", err)
	}
	path := f.Name()
	remove := func() error { return os.Remove(path) }
	_, err = io.Copy(f, doc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", nil, errors.Join(fmt.Errorf("office: spool: %w", err), remove())
	}
	return path, remove, nil
}
