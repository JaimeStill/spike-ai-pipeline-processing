//go:build linux

package dispatch

import (
	"encoding/json/v2"
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
const adapterVersion = "1"

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

// record returns a record output named name holding v as JSON, through encoding/json/v2, as the
// format packages' records marshal back to their lines, with meta.
func record(name string, v any, meta map[string]string) (Output, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Output{}, fmt.Errorf("dispatch: %s: %w", name, err)
	}
	return Output{Kind: RecordOutput, Name: name, MediaType: "application/json", Value: b, Meta: meta}, nil
}

// markdown returns a Markdown output named name holding md inline, with meta.
func markdown(name, md string, meta map[string]string) Output {
	return Output{Kind: MarkdownOutput, Name: name, MediaType: "text/markdown", Value: []byte(md), Meta: meta}
}

// stash places the files a CLI wrote into a directory of their own each, before a stage yields
// them as found parts. The dispatcher removes a found part's file once the caller is done with it,
// and a CLI that writes each distinct content once, as mail and onenote do, names one file for
// every record that holds that content, records the stage may meet after the first is removed;
// a stashed link is removed in the original's place, which stays until the part's directory is.
type stash struct {
	dir string
	n   int
}

// place returns the path of a link to the file at path, in a new directory under the stash's,
// named as path is: a hard link, else a copy where the file system refuses one.
func (s *stash) place(path string) (string, error) {
	s.n++
	dir := filepath.Join(s.dir, fmt.Sprintf("found-%d", s.n))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("dispatch: %w", err)
	}
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
