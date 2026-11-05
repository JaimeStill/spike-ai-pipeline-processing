package dispatch

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strconv"
	"sync"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// MemoryIndex is an [Index] in memory, for the dispatches of one process: a waiter wakes the
// moment the claimant records or releases. It holds every recorded output, and a claim's pending
// ones, in memory, so it suits a process whose inputs' outputs fit there; [FileIndex] holds them on
// disk. Its zero value is not ready; [NewMemoryIndex] makes one.
type MemoryIndex struct {
	mu      sync.Mutex
	entries map[Key]memoryEntry
	flags   map[string][]inspect.Flag
	claims  map[Key]*memoryClaim
	tokens  uint64
}

// memoryEntry is a recorded entry and its outputs, never changed once recorded: a later record
// replaces it.
type memoryEntry struct {
	entry   Entry
	outputs []Output
}

// memoryClaim is a held claim: its token, a channel closed when it ends, and its pending
// outputs.
type memoryClaim struct {
	token   string
	done    chan struct{}
	pending []Output
}

// errNotClaimed is a memory index's refusal of an append or record whose token doesn't hold the
// key's claim.
var errNotClaimed = errors.New("dispatch: memory index: the token doesn't hold the key's claim")

// NewMemoryIndex returns an empty MemoryIndex.
func NewMemoryIndex() *MemoryIndex {
	return &MemoryIndex{
		entries: map[Key]memoryEntry{},
		flags:   map[string][]inspect.Flag{},
		claims:  map[Key]*memoryClaim{},
	}
}

// Lookup returns key's entry, a copy, and whether there is one.
func (m *MemoryIndex) Lookup(ctx context.Context, key Key) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	return e.entry.clone(), ok, ctx.Err()
}

// Outputs yields a copy of each of key's recorded outputs.
func (m *MemoryIndex) Outputs(ctx context.Context, key Key) iter.Seq2[Output, error] {
	return func(yield func(Output, error) bool) {
		m.mu.Lock()
		// A recorded entry's outputs never change, so the slice is read outside the lock.
		outputs := m.entries[key].outputs
		m.mu.Unlock()
		for _, o := range outputs {
			if err := ctx.Err(); err != nil {
				yield(Output{}, err)
				return
			}
			if !yield(o.clone(), nil) {
				return
			}
		}
	}
}

// Claim claims key unless it is recorded or claimed.
func (m *MemoryIndex) Claim(ctx context.Context, key Key) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; ok {
		return "", false, nil
	}
	if _, ok := m.claims[key]; ok {
		return "", false, nil
	}
	m.tokens++
	token := strconv.FormatUint(m.tokens, 10)
	m.claims[key] = &memoryClaim{token: token, done: make(chan struct{})}
	return token, true, nil
}

// Wait waits for key's claim to end, then returns its entry if it has one.
func (m *MemoryIndex) Wait(ctx context.Context, key Key) (Entry, bool, error) {
	m.mu.Lock()
	c := m.claims[key]
	m.mu.Unlock()
	if c != nil {
		select {
		case <-c.done:
		case <-ctx.Done():
			return Entry{}, false, ctx.Err()
		}
	}
	return m.Lookup(ctx, key)
}

// Append appends a copy of o to token's claim's pending outputs, refusing a token that doesn't
// hold key's claim.
func (m *MemoryIndex) Append(ctx context.Context, key Key, token string, o Output) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.claims[key]
	if !ok || c.token != token {
		return errNotClaimed
	}
	c.pending = append(c.pending, o.clone())
	return ctx.Err()
}

// Record records a copy of e as key's entry, with token's claim's pending outputs, and ends the
// claim, refusing a token that doesn't hold it.
func (m *MemoryIndex) Record(ctx context.Context, key Key, token string, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.claims[key]
	if !ok || c.token != token {
		return errNotClaimed
	}
	e = e.clone()
	e.Outputs = len(c.pending)
	m.entries[key] = memoryEntry{entry: e, outputs: c.pending}
	m.end(key, token)
	return ctx.Err()
}

// Release ends token's claim of key, dropping its pending outputs.
func (m *MemoryIndex) Release(ctx context.Context, key Key, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.end(key, token)
	return ctx.Err()
}

// end ends key's claim if token holds it, waking its waiters; m.mu is held.
func (m *MemoryIndex) end(key Key, token string) {
	if c, ok := m.claims[key]; ok && c.token == token {
		delete(m.claims, key)
		close(c.done)
	}
}

// Flags returns a copy of the flags recorded for sha256.
func (m *MemoryIndex) Flags(ctx context.Context, sha256 string) ([]inspect.Flag, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.flags[sha256]
	return slices.Clone(f), ok, ctx.Err()
}

// RecordFlags records a copy of flags for sha256.
func (m *MemoryIndex) RecordFlags(ctx context.Context, sha256 string, flags []inspect.Flag) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flags[sha256] = slices.Clone(flags)
	return ctx.Err()
}
