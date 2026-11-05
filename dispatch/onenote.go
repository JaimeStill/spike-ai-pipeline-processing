//go:build linux

package dispatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// OneNoteConfig is what a [OneNoteStage] is built over: the caller's Reader and the version of
// the tool behind it.
type OneNoteConfig struct {
	// Reader reads the notebooks and sections; it is required.
	Reader *onenote.Reader
	// Version names onenote's version, as [Versions] gives it; it is required.
	Version string
}

// OneNoteStage prepares a OneNote section, table of contents, or package, route.OneNote's parts,
// through onenote's Reader, its media written into the part's directory. Each of onenote's
// records is a [RecordOutput], its typed JSON as onenote's record marshals it, in onenote's
// order: the notebook, named "notebook"; each section group, "section group <id>", and section,
// "section <id>", their Meta naming their path; each page, "page <id>", with its Markdown as a
// [MarkdownOutput] of the same name, the Meta of both naming its id, its section's id, and its
// title; each skipped record, "skipped <kind>", its Meta naming the record and its reason; and a
// record of a kind onenote doesn't know, by its kind. onenote's warnings, when it gave any, are a
// last record, "warnings", a JSON array.
//
// Each of a page's media, its images, embedded files, and ink, wherever it sits, that onenote
// wrote content for is a [Found] part, the dispatcher's to route and inspect again, named by the
// name it was inserted or attached under, else by its file's, its Where naming the page and the
// medium's place, "page <id>, <where>", as "page {...}, page ink". A page's ink is an SVG, which
// route.File routes vision-converted, to the picture stage's PNG. A medium onenote wrote no
// content for is a record named by its place, its Meta naming why it has none, never dropped.
type OneNoteStage struct {
	reader *onenote.Reader
	id     Identity
}

// NewOneNoteStage builds a OneNoteStage over cfg, refusing one with no Reader or Version.
func NewOneNoteStage(cfg OneNoteConfig) (*OneNoteStage, error) {
	if cfg.Reader == nil {
		return nil, errors.New("dispatch: onenote stage: no reader")
	}
	id, err := adapterIdentity("onenote", cfg.Version, struct {
		Schema string `json:"schema"`
	}{onenote.Schema})
	if err != nil {
		return nil, err
	}
	return &OneNoteStage{reader: cfg.Reader, id: id}, nil
}

// Identity returns the stage's identity: "onenote", onenote's version, and the schema's hash.
func (s *OneNoteStage) Identity() Identity { return s.id }

// Prepare reads the notebook or section at p.Path, as the type's doc says.
func (s *OneNoteStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		media := filepath.Join(dir, "media")
		if err := os.Mkdir(media, 0o700); err != nil {
			yield(Item{}, fmt.Errorf("onenote stage: %w", err))
			return
		}
		stream, err := s.reader.Read(ctx, onenote.Request{Input: p.Path, Media: media})
		if err != nil {
			yield(Item{}, err)
			return
		}
		w := onenoteWalk{stash: stash{dir: dir}, media: media, yield: yield}
		for rec, err := range stream.Records() {
			if err != nil {
				yield(Item{}, err)
				return
			}
			if err := w.record(rec); err != nil {
				if !errors.Is(err, errStopped) {
					yield(Item{}, err)
				}
				return
			}
		}
		if warnings := stream.Warnings(); len(warnings) > 0 {
			o, err := record("warnings", warnings, nil)
			if err != nil {
				yield(Item{}, err)
				return
			}
			yield(OutputItem(o), nil)
		}
	}
}

// onenoteWalk is one read's walk of its records: its stash, its media directory, and the loop's
// yield.
type onenoteWalk struct {
	stash stash
	media string
	yield func(Item, error) bool
}

// output yields o, or returns err, record's error, or errStopped once the loop stops.
func (w *onenoteWalk) output(o Output, err error) error {
	if err != nil {
		return err
	}
	if !w.yield(OutputItem(o), nil) {
		return errStopped
	}
	return nil
}

// record yields rec's outputs and parts.
func (w *onenoteWalk) record(rec onenote.Record) error {
	switch r := rec.(type) {
	case *onenote.Notebook:
		return w.output(record("notebook", r, map[string]string{"id": r.ID, "name": r.Name}))
	case *onenote.SectionGroup:
		return w.output(record("section group "+r.ID, r, map[string]string{"id": r.ID, "path": strings.Join(r.Path, "/")}))
	case *onenote.Section:
		return w.output(record("section "+r.ID, r, map[string]string{"id": r.ID, "path": strings.Join(r.Path, "/")}))
	case *onenote.Page:
		return w.page(r)
	case *onenote.Skipped:
		return w.output(record("skipped "+r.Kind, r, map[string]string{"name": r.Name, "reason": r.Reason}))
	case *onenote.UnknownRecord:
		return w.output(Output{Kind: RecordOutput, Name: r.Kind, MediaType: "application/json", Value: r.Raw}, nil)
	}
	return fmt.Errorf("onenote stage: a record of type %T", rec)
}

// medium is what a medium without content is recorded as: its kind, its record's names and
// size, its place, and why it has no content.
type medium struct {
	Kind     onenote.MediumKind `json:"kind"`
	File     string             `json:"file,omitzero"`
	Filename string             `json:"filename,omitzero"`
	Size     *int64             `json:"size,omitzero"`
	Type     string             `json:"type,omitzero"`
	Where    string             `json:"where"`
	Route    route.Route        `json:"route"`
	Reason   string             `json:"reason"`
}

// page yields p's Markdown and record, then each of its media, as the stage's doc says.
func (w *onenoteWalk) page(p *onenote.Page) error {
	name := "page " + p.ID
	meta := map[string]string{"id": p.ID, "section": p.Section, "title": p.Title}
	if err := w.output(markdown(name, p.Markdown, meta), nil); err != nil {
		return err
	}
	if err := w.output(record(name, p, meta)); err != nil {
		return err
	}
	for m, err := range p.Media(w.media) {
		where := name + ", " + m.Where
		if err != nil {
			return fmt.Errorf("onenote stage: %s: %w", where, err)
		}
		if m.Path == "" {
			rec := medium{m.Kind, m.File, m.Filename, m.Size, m.Type, m.Where, m.Route, m.Reason}
			if err := w.output(record(where, rec, map[string]string{"reason": m.Reason})); err != nil {
				return err
			}
			continue
		}
		path, err := w.stash.place(m.Path)
		if err != nil {
			return err
		}
		if !w.yield(PartItem(Found{Path: path, Name: cmp.Or(m.Filename, m.File), Where: where}), nil) {
			return errStopped
		}
	}
	return nil
}
