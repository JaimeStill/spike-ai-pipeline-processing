package dispatch

import (
	"context"
	"maps"
	"slices"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// Index is a content-addressed record of what has been prepared, shared by every dispatch that
// uses it, concurrent ones among them. It holds a stage's outputs for content by a [Key], the
// content's SHA-256 and the stage's [Identity], and the content's inspection flags by its
// SHA-256 alone. An implementation is safe for concurrent use.
//
// A claim makes one dispatch the preparer of a key: Claim succeeds for one claimant only, while
// the key is neither recorded nor claimed; Record records the key's outputs and ends the claim,
// and Release ends it without a record, after a failure, so another may claim it. Wait blocks a
// dispatch that lost the claim until the claimant records the key, returning its record, or
// releases it, returning none so the waiter may claim it in turn.
type Index interface {
	// Lookup returns the record of key, and whether there is one.
	Lookup(ctx context.Context, key Key) (Entry, bool, error)
	// Claim claims key, returning the claim's token and true; it returns false, claiming
	// nothing, when key is recorded or another holds its claim.
	Claim(ctx context.Context, key Key) (token string, ok bool, err error)
	// Wait waits while key is claimed, then returns its record and true if the claimant recorded
	// it, or false if the claim ended without a record. It returns at once when key is recorded
	// or unclaimed, and an error matching the context's when ctx ends first.
	Wait(ctx context.Context, key Key) (Entry, bool, error)
	// Record records e as key's record and ends the claim token names.
	Record(ctx context.Context, key Key, token string, e Entry) error
	// Release ends the claim token names without a record; a claim another now holds is left.
	Release(ctx context.Context, key Key, token string) error
	// Flags returns the inspection flags recorded for content by its SHA-256, and whether there
	// are any recorded.
	Flags(ctx context.Context, sha256 string) ([]inspect.Flag, bool, error)
	// RecordFlags records content's inspection flags by its SHA-256.
	RecordFlags(ctx context.Context, sha256 string, flags []inspect.Flag) error
}

// Key is what an index records a stage's outputs under: the content's SHA-256, lowercase hex,
// and the stage's identity.
type Key struct {
	SHA256 string   `json:"sha256"`
	Stage  Identity `json:"stage"`
}

// Entry is what the index holds for a key: the outcome the part came to, [Prepared],
// [NoProcessor], or [Encrypted], its reason, the stage's outputs, as the caller left them, its
// Refs set, and the parts the stage found, as references.
type Entry struct {
	Outcome  Outcome  `json:"outcome"`
	Reason   string   `json:"reason,omitzero"`
	Outputs  []Output `json:"outputs,omitzero"`
	Children []Child  `json:"children,omitzero"`
}

// Child references a part a stage found, as its parent's [Entry] records it: its SHA-256, by
// which its own entry and flags are found, its name, where it was found, its size, its route,
// and the outcome it came to.
type Child struct {
	SHA256  string      `json:"sha256"`
	Name    string      `json:"name,omitzero"`
	Where   string      `json:"where,omitzero"`
	Size    int64       `json:"size"`
	Route   route.Route `json:"route,omitzero"`
	Outcome Outcome     `json:"outcome"`
}

// clone returns a deep copy of e, so an index's entry and a caller's never share memory.
func (e Entry) clone() Entry {
	e.Outputs = slices.Clone(e.Outputs)
	for i := range e.Outputs {
		e.Outputs[i].Value = slices.Clone(e.Outputs[i].Value)
		e.Outputs[i].Meta = maps.Clone(e.Outputs[i].Meta)
	}
	e.Children = slices.Clone(e.Children)
	return e
}
