//go:build linux

package dispatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// adapterVersion is the version of the format packages' adapters, part of every adapter's
// configuration hash: a change to what an adapter yields for the same tools and options changes
// it, so the index prepares content again.
const adapterVersion = "3"

// Versions returns the versions of the tools behind a stage, for its config's Version: each
// tool's name and version, as "gs 10.05.1", joined by ", " in the order given.
func Versions(tools ...*tool.Tool) string {
	parts := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != nil {
			parts = append(parts, t.Name()+" "+t.Version().String())
		}
	}
	return strings.Join(parts, ", ")
}

// adapterIdentity returns the identity of the adapter named name over tools of version, its
// configuration hashing config with adapterVersion. It refuses an empty version, which would key
// every tool version's outputs alike.
func adapterIdentity(name, version string, config any) (Identity, error) {
	if version == "" {
		return Identity{}, fmt.Errorf("dispatch: %s stage: no version", name)
	}
	hash, err := HashConfig(struct {
		Adapter string `json:"adapter"`
		Config  any    `json:"config"`
	}{adapterVersion, config})
	if err != nil {
		return Identity{}, err
	}
	return Identity{Name: name, Version: version, Config: hash}, nil
}

// markdown returns a Markdown output named name holding md inline, with meta.
func markdown(name, md string, meta map[string]string) Output {
	return Output{Kind: MarkdownOutput, Name: name, MediaType: "text/markdown", Value: []byte(md), Meta: meta}
}

// stash places the files a CLI wrote into a directory of their own each, before a stage yields
// them as found parts, and removes each original once the dispatcher has settled its content.
//
// A CLI that writes each distinct content once, as mail and onenote do, names one file, by the
// content's SHA-256, for every record that holds that content, records the stage may meet after
// the first is dispatched; the dispatcher removes a found part's file once its event is done, so
// the stash yields a hard link, which is removed in the original's place, the original staying
// for those later records. A found part whose file is named by a SHA-256 carries it, and a
// Release that removes the original: the dispatcher calls it once the index holds the content's
// entry, so the disk holds the originals of the records in flight, and of content the
// dispatcher couldn't settle, a later record of which is dispatched from the original again. A
// later record naming a released original is found with no file, by its SHA-256, which the
// dispatcher sees from the index.
type stash struct {
	dir string
	n   int
	// last is the directory of the last link placed, removed with the next place once the
	// dispatcher has removed its link.
	last string
	// released are the originals removed on the dispatcher's release, their SHA-256 by path.
	released map[string]string
}

// find returns the found part f describes for the file a CLI wrote at path, and whether it has
// one: a link to the file, carrying its name's SHA-256 and a release, when it is there; a part
// with no file, carrying its SHA-256, when the stash released it; and none when neither.
func (s *stash) find(path string, f Found) (Found, bool, error) {
	ok, err := exists(path)
	if err != nil {
		return Found{}, false, err
	}
	if !ok {
		sum, released := s.released[path]
		f.SHA256 = sum
		return f, released, nil
	}
	if f.Path, err = s.place(path); err != nil {
		return Found{}, false, err
	}
	if sum := nameSum(path); sum != "" {
		f.SHA256 = sum
		f.Release = func() { s.release(path, sum) }
	}
	return f, true, nil
}

// release removes the original at path, whose content's SHA-256 is sum, and remembers it, so a
// later record naming it is found with no file. An original that can't be removed stays, and a
// later record finds it there.
func (s *stash) release(path, sum string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	if s.released == nil {
		s.released = map[string]string{}
	}
	s.released[path] = sum
}

// nameSum returns the SHA-256 the file at path is named by, its base name's up to its first
// dot, lowercase hex, or "" when its name is no SHA-256.
func nameSum(path string) string {
	sum, _, _ := strings.Cut(filepath.Base(path), ".")
	if len(sum) != 64 {
		return ""
	}
	for _, c := range sum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return sum
}

// place returns the path of a link to the file at path, in a new directory under the stash's,
// named as path is: a hard link, else a copy where the file system refuses one. It removes the
// last link's directory, which is empty once the dispatcher has removed the link, unless the
// caller kept it.
func (s *stash) place(path string) (string, error) {
	if s.last != "" {
		_ = os.Remove(s.last) // a directory still holding a kept link stays
	}
	s.n++
	dir := filepath.Join(s.dir, fmt.Sprintf("found-%d", s.n))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("dispatch: %w", err)
	}
	s.last = dir
	dst := filepath.Join(dir, filepath.Base(path))
	if err := os.Link(path, dst); err == nil {
		return dst, nil
	}
	if err := copyFile(path, dst); err != nil {
		return "", fmt.Errorf("dispatch: %w", err)
	}
	return dst, nil
}

// copyFile copies the file at src to a new file at dst.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	_, err = io.Copy(out, in)
	return err
}

// exists reports whether a file is at path, an error other than its absence failing.
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("dispatch: %w", err)
}
