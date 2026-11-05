package dispatch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// The file index's defaults.
const (
	// DefaultStale is how old a claim may grow before another claimant takes it over: longer
	// than any one part's stage should run.
	DefaultStale = 30 * time.Minute
	// DefaultPoll is how often a waiter looks again at a claim it waits on.
	DefaultPoll = 100 * time.Millisecond
)

// FileOptions tune a [FileIndex]; the zero FileOptions is the default.
type FileOptions struct {
	// Stale is how old a claim may grow before another claimant reclaims it, as a claimant
	// that died would leave it; [DefaultStale] when zero. A claim is never renewed, so Stale
	// must outlast the slowest stage.
	Stale time.Duration
	// Poll is how often a waiter reads the claim it waits on; [DefaultPoll] when zero.
	Poll time.Duration
	// Now is the clock a claim's age is told by; time.Now when nil.
	Now func() time.Time
}

// FileIndex is an [Index] on disk, on the standard library alone, for dispatches in one process
// or many sharing a directory. Under the caller's directory it keeps one file per record:
// entries/ holds each key's entry, named by the SHA-256 of the key, and flags/ each content's
// flags, named by its SHA-256, each JSON written to a temporary file and renamed into place, so a
// reader sees a whole record or none. claims/ holds each claim, a file created with O_EXCL,
// holding its token and the time it was made; a claim older than [FileOptions.Stale] is taken
// over by the next claimant, and a waiter polls the claim every [FileOptions.Poll].
type FileIndex struct {
	dir   string
	stale time.Duration
	poll  time.Duration
	now   func() time.Time
}

// The file index's subdirectories.
const (
	entriesDir = "entries"
	flagsDir   = "flags"
	claimsDir  = "claims"
)

// NewFileIndex returns a FileIndex in dir, making dir and its subdirectories when they don't
// exist.
func NewFileIndex(dir string, opts FileOptions) (*FileIndex, error) {
	if dir == "" {
		return nil, errors.New("dispatch: file index: no directory")
	}
	if opts.Stale < 0 || opts.Poll < 0 {
		return nil, errors.New("dispatch: file index: negative stale or poll duration")
	}
	f := &FileIndex{dir: dir, stale: opts.Stale, poll: opts.Poll, now: opts.Now}
	if f.stale == 0 {
		f.stale = DefaultStale
	}
	if f.poll == 0 {
		f.poll = DefaultPoll
	}
	if f.now == nil {
		f.now = time.Now
	}
	for _, sub := range []string{entriesDir, flagsDir, claimsDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("dispatch: file index: %w", err)
		}
	}
	return f, nil
}

// keyName returns the file name key's entry and claim have: the hex SHA-256 of its fields.
func keyName(key Key) string {
	b, _ := json.Marshal(key) // a Key's fields are strings, which always marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fileEntry is an entry's file: the key it is for, so a reader can tell it is the key's, and the
// entry.
type fileEntry struct {
	Key   Key   `json:"key"`
	Entry Entry `json:"entry"`
}

// Lookup reads key's entry file.
func (f *FileIndex) Lookup(ctx context.Context, key Key) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	var fe fileEntry
	ok, err := readJSON(filepath.Join(f.dir, entriesDir, keyName(key)+".json"), &fe)
	if err != nil || !ok {
		return Entry{}, false, err
	}
	if fe.Key != key {
		return Entry{}, false, fmt.Errorf("dispatch: file index: entry %s holds another key", keyName(key))
	}
	return fe.Entry, true, nil
}

// claimFile is a claim's content: its token and when it was made, by the index's clock.
type claimFile struct {
	Token string    `json:"token"`
	Time  time.Time `json:"time"`
}

// claimPath returns key's claim file.
func (f *FileIndex) claimPath(key Key) string {
	return filepath.Join(f.dir, claimsDir, keyName(key)+".claim")
}

// Claim creates key's claim file with O_EXCL, unless key is recorded; it takes a stale claim
// over, once.
func (f *FileIndex) Claim(ctx context.Context, key Key) (string, bool, error) {
	path := f.claimPath(key)
	for range 2 {
		if _, ok, err := f.Lookup(ctx, key); err != nil || ok {
			return "", false, err
		}
		token := rand.Text()
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			b, _ := json.Marshal(claimFile{Token: token, Time: f.now()})
			_, werr := file.Write(b)
			if cerr := file.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				_ = os.Remove(path)
				return "", false, fmt.Errorf("dispatch: file index: claim: %w", werr)
			}
			// A claimant that recorded the key between the lookup and the create has removed
			// its claim; the key is recorded, so this claim is given up.
			if _, ok, err := f.Lookup(ctx, key); err != nil || ok {
				_ = f.Release(ctx, key, token)
				return "", false, err
			}
			return token, true, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", false, fmt.Errorf("dispatch: file index: claim: %w", err)
		}
		held, stale, err := f.readClaim(path)
		if err != nil {
			return "", false, err
		}
		if !stale {
			return "", false, nil
		}
		if err := f.reclaim(path, held); err != nil {
			return "", false, err
		}
	}
	return "", false, nil
}

// readClaim reads the claim file at path: its content, and whether it is stale. A missing claim
// is stale, with no token. A claim whose content can't be read, as one whose claimant died
// before writing it, is told stale by its modification time.
func (f *FileIndex) readClaim(path string) (claimFile, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return claimFile{}, true, nil
	}
	if err != nil {
		return claimFile{}, false, fmt.Errorf("dispatch: file index: claim: %w", err)
	}
	var c claimFile
	if json.Unmarshal(b, &c) != nil || c.Token == "" {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return claimFile{}, true, nil
		}
		if err != nil {
			return claimFile{}, false, fmt.Errorf("dispatch: file index: claim: %w", err)
		}
		return claimFile{}, f.now().Sub(info.ModTime()) > f.stale, nil
	}
	return c, f.now().Sub(c.Time) > f.stale, nil
}

// reclaim removes the stale claim held at path. It renames the claim aside first, so of two
// reclaimers only one moves it, and puts back a claim that turns out not to be the stale one,
// as one a third claimant made after held was read.
func (f *FileIndex) reclaim(path string, held claimFile) error {
	aside := path + ".stale-" + rand.Text()
	if err := os.Rename(path, aside); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("dispatch: file index: reclaim: %w", err)
	}
	defer func() { _ = os.Remove(aside) }()
	var moved claimFile
	b, err := os.ReadFile(aside)
	if err == nil {
		_ = json.Unmarshal(b, &moved)
	}
	if moved.Token != held.Token {
		// Not the claim found stale: put it back, unless a claim has taken its place.
		if err := os.Link(aside, path); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("dispatch: file index: reclaim: %w", err)
		}
	}
	return nil
}

// Wait polls key's claim until it is gone or stale, then reads key's entry.
func (f *FileIndex) Wait(ctx context.Context, key Key) (Entry, bool, error) {
	path := f.claimPath(key)
	for {
		e, ok, err := f.Lookup(ctx, key)
		if err != nil || ok {
			return e, ok, err
		}
		if _, stale, err := f.readClaim(path); err != nil || stale {
			if err != nil {
				return Entry{}, false, err
			}
			// The claim ended, or its claimant is gone: a record may have landed between the
			// two reads.
			return f.Lookup(ctx, key)
		}
		t := time.NewTimer(f.poll)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return Entry{}, false, ctx.Err()
		}
	}
}

// Record writes key's entry file, then ends token's claim.
func (f *FileIndex) Record(ctx context.Context, key Key, token string, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(f.dir, entriesDir, keyName(key)+".json")
	if err := writeJSON(path, fileEntry{Key: key, Entry: e}); err != nil {
		return err
	}
	return f.Release(ctx, key, token)
}

// Release removes key's claim file if token holds it.
func (f *FileIndex) Release(_ context.Context, key Key, token string) error {
	path := f.claimPath(key)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("dispatch: file index: release: %w", err)
	}
	var c claimFile
	if json.Unmarshal(b, &c) != nil || c.Token != token {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("dispatch: file index: release: %w", err)
	}
	return nil
}

// Flags reads sha256's flags file.
func (f *FileIndex) Flags(ctx context.Context, sha256 string) ([]inspect.Flag, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	var flags []inspect.Flag
	ok, err := readJSON(filepath.Join(f.dir, flagsDir, sha256+".json"), &flags)
	return flags, ok, err
}

// RecordFlags writes sha256's flags file.
func (f *FileIndex) RecordFlags(ctx context.Context, sha256 string, flags []inspect.Flag) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if flags == nil {
		flags = []inspect.Flag{}
	}
	return writeJSON(filepath.Join(f.dir, flagsDir, sha256+".json"), flags)
}

// readJSON decodes the JSON file at path into v, returning false for a missing file.
func readJSON(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	if err != nil {
		return false, fmt.Errorf("dispatch: file index: %w", err)
	}
	return true, nil
}

// writeJSON writes v's JSON to path atomically: to a temporary file in path's directory, synced,
// then renamed over path.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("dispatch: file index: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("dispatch: file index: %w", err)
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("dispatch: file index: %w", err)
	}
	return nil
}
