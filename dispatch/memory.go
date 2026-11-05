package dispatch

import (
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
)

// MemoryIndex is an [Index] in memory, for the dispatches of one process: a waiter wakes the
// moment the claimant records or releases. Its zero value is not ready; [NewMemoryIndex] makes
// one.
type MemoryIndex struct {
	mu      sync.Mutex
	entries map[Key]Entry
	flags   map[string][]inspect.Flag
	claims  map[Key]*memoryClaim
	tokens  uint64
}

// memoryClaim is a held claim: its token, and a channel closed when it ends.
type memoryClaim struct {
	token string
	done  chan struct{}
}

// NewMemoryIndex returns an empty MemoryIndex.
func NewMemoryIndex() *MemoryIndex {
	return &MemoryIndex{
		entries: map[Key]Entry{},
		flags:   map[string][]inspect.Flag{},
		claims:  map[Key]*memoryClaim{},
	}
}

// Lookup returns key's entry, a copy, and whether there is one.
func (m *MemoryIndex) Lookup(ctx context.Context, key Key) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	return e.clone(), ok, ctx.Err()
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

// Record records a copy of e as key's entry and ends token's claim.
func (m *MemoryIndex) Record(ctx context.Context, key Key, token string, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = e.clone()
	m.end(key, token)
	return ctx.Err()
}

// Release ends token's claim of key.
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
