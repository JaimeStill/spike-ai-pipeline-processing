package dispatch

import (
	"context"
	"iter"
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
// the key is neither recorded nor claimed. The claimant appends each output to the key's pending
// entry as it has it, by Append, so no implementation need hold a part's outputs in memory; Record
// commits the pending outputs, in the order appended, with the entry's outcome, reason, and
// children, and ends the claim, and Release drops them and ends the claim without a record, after
// a failure, so another may claim it. Wait blocks a dispatch that lost the claim until the
// claimant records the key, returning its record, or releases it, returning none so the waiter may
// claim it in turn. A recorded entry's outputs are read back one at a time by Outputs, never all
// at once.
type Index interface {
	// Lookup returns the record of key, and whether there is one; its Outputs is how many
	// outputs Outputs yields.
	Lookup(ctx context.Context, key Key) (Entry, bool, error)
	// Outputs yields key's recorded outputs, in the order they were appended, streamed: an
	// implementation holds one at a time. It yields none for a key with no record, and an error,
	// last, when one can't be read.
	Outputs(ctx context.Context, key Key) iter.Seq2[Output, error]
	// Claim claims key, returning the claim's token and true; it returns false, claiming
	// nothing, when key is recorded or another holds its claim.
	Claim(ctx context.Context, key Key) (token string, ok bool, err error)
	// Wait waits while key is claimed, then returns its record and true if the claimant recorded
	// it, or false if the claim ended without a record. It returns at once when key is recorded
	// or unclaimed, and an error matching the context's when ctx ends first.
	Wait(ctx context.Context, key Key) (Entry, bool, error)
	// Append appends o to the pending entry of the claim token names. An implementation may
	// refuse an append whose token doesn't hold key's claim, or drop it at Record.
	Append(ctx context.Context, key Key, token string, o Output) error
	// Record records e as key's record, its outputs those appended under token, e's own
	// Outputs count ignored, and ends the claim token names.
	Record(ctx context.Context, key Key, token string, e Entry) error
	// Release ends the claim token names without a record, dropping its pending outputs; a
	// claim another now holds is left.
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
// [NoProcessor], or [Encrypted], its reason, how many outputs the stage prepared, which the
// index's Outputs yields as the caller left them, their Refs set, and the parts the stage found,
// as references.
type Entry struct {
	Outcome  Outcome `json:"outcome"`
	Reason   string  `json:"reason,omitzero"`
	Outputs  int     `json:"outputs,omitzero"`
	Children []Child `json:"children,omitzero"`
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
	e.Children = slices.Clone(e.Children)
	return e
}

// clone returns a deep copy of o, so an index's output and a caller's never share memory.
func (o Output) clone() Output {
	o.Value = slices.Clone(o.Value)
	o.Meta = maps.Clone(o.Meta)
	return o
}
