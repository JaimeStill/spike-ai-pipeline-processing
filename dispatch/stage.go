package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"

	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// Stage prepares the parts of one route. The dispatcher calls Prepare once for each part it
// prepares, never for content the index already holds, and recurses into every part the stage
// finds before taking the stage's next item.
type Stage interface {
	// Identity names the stage, the version of the tool behind it, and a hash of its
	// configuration: the index keys a part's outputs by its SHA-256 and this identity, so a new
	// tool version or a changed configuration prepares the content again.
	Identity() Identity
	// Prepare prepares p, the file at p.Path, writing whatever files it makes, the parts it
	// finds among them, into dir, a directory the dispatcher made for p alone and removes once
	// the caller is done with p. It yields each output it prepares and each part it finds, in
	// any order, as it has them; a loop that stops early stops the stage. An error ends the
	// stage: one matching [ErrNoProcessor] says the stage has no processor for this kind of
	// content, one matching [ErrEncrypted] that it can't read the content because it is
	// encrypted, and any other that it failed.
	Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error]
}

// Stages are the stages a caller registers, by the route whose parts each prepares. A route
// with no stage is reported [NoProcessor].
type Stages map[route.Route]Stage

// Identity is a stage's identity: its name, the version of the tool behind it, and a hash of
// its configuration, such as [HashConfig] returns. It is comparable, and part of every key the
// index records a stage's outputs under.
type Identity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Config  string `json:"config"`
}

// String returns the identity as name@version#config, for a reason or a log.
func (id Identity) String() string { return id.Name + "@" + id.Version + "#" + id.Config }

// HashConfig returns the lowercase hex SHA-256 of v's JSON, for a stage's [Identity.Config]:
// the same configuration gives the same hash on every run, a map's keys being sorted.
func HashConfig(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("dispatch: hash config: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// The errors a stage ends with to name its outcome; a stage wraps one with its reason, which
// becomes the event's.
var (
	// ErrNoProcessor is a stage's word that it has no processor for this part's kind yet, such
	// as office given a WordPerfect document: the part's outcome is [NoProcessor].
	ErrNoProcessor = errors.New("no processor yet")
	// ErrEncrypted is a stage's word that it can't read the part because it is encrypted: the
	// part's outcome is [Encrypted].
	ErrEncrypted = errors.New("encrypted")
)

// Item is one thing a stage yields: an output it prepared, or a part it found. Exactly one of
// its fields is set.
type Item struct {
	Output *Output
	Part   *Found
}

// OutputItem returns an Item holding o.
func OutputItem(o Output) Item { return Item{Output: &o} }

// PartItem returns an Item holding f.
func PartItem(f Found) Item { return Item{Part: &f} }

// Found is a part a stage found in the part it prepares: a file the stage wrote, usually in
// the directory Prepare was given, its name, its stored MIME type, if it has one, and where it
// was found, in the stage's words, such as "attachment 2" or "page 3, outline 1, image 2". The
// dispatcher hashes it, routes it by route.File, and dispatches it as a part of its own, then
// removes the file once the caller is done with it, unless the caller keeps it.
type Found struct {
	Path  string
	Name  string
	MIME  string
	Where string
}

// OutputKind names what an output holds.
type OutputKind string

const (
	// TextOutput is plain text, such as a page's text layer.
	TextOutput OutputKind = "text"
	// MarkdownOutput is Markdown, such as an office document's or a OneNote page's.
	MarkdownOutput OutputKind = "markdown"
	// ImageOutput is a picture prepared for a model's vision, such as a PDF page's PNG.
	ImageOutput OutputKind = "image"
	// RecordOutput is a structured record, as JSON, such as a mail message or an office document's
	// model.
	RecordOutput OutputKind = "record"
)

// Output is one thing a stage prepared for a model: its kind, a name the stage gives it, such as
// "page 3", its media type, and either a file the stage wrote, at Path, or its bytes inline, in
// Value: text and Markdown as UTF-8, a record as JSON. Meta holds whatever else the stage says
// of it, such as a page's number and count.
//
// Ref is the caller's: the dispatcher never sets it. A caller that persists an output sets its
// Ref, through the event's Outputs slice, to its own key for it, and the index records the Ref,
// so a later [Seen] event carries the caller's key rather than a file the dispatcher has since
// removed. The recorded output keeps its Path only when the caller kept the event's files.
type Output struct {
	Kind      OutputKind        `json:"kind"`
	Name      string            `json:"name,omitzero"`
	MediaType string            `json:"media_type,omitzero"`
	Path      string            `json:"path,omitzero"`
	Value     []byte            `json:"value,omitzero"`
	Meta      map[string]string `json:"meta,omitzero"`
	Ref       string            `json:"ref,omitzero"`
}

// record returns a record output named name holding v as JSON, through encoding/json/v2, as the
// format packages' records marshal back to their lines, with meta.
func record(name string, v any, meta map[string]string) (Output, error) {
	b, err := jsonv2.Marshal(v)
	if err != nil {
		return Output{}, fmt.Errorf("dispatch: %s: %w", name, err)
	}
	return Output{Kind: RecordOutput, Name: name, MediaType: "application/json", Value: b, Meta: meta}, nil
}

// Part is one part of an input, the input itself among them: the file at Path, the name it was
// given, its stored MIME type, its size and SHA-256, the kind its signature names, its route and
// why, its inspection's flags, the SHA-256 of the part it was found in, "" for the input, its
// depth, 0 for the input, and where it was found, in its parent's stage's words. After is the
// input's [Input.After], the message its stage resumes after, on the input's part alone; a stage
// that doesn't resume ignores it.
type Part struct {
	Path   string
	Name   string
	MIME   string
	Size   int64
	SHA256 string
	Kind   route.Kind
	Route  route.Route
	Reason string
	Flags  []inspect.Flag
	Parent string
	Depth  int
	Where  string
	After  uint64
}
