//go:build linux

package onenote

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strconv"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// MediumKind is what a Medium is: an image, an embedded file, or an ink drawing.
type MediumKind string

// The kinds of Medium.
const (
	// ImageMedium is an image block's or an image container's image.
	ImageMedium MediumKind = "image"
	// EmbeddedFileMedium is an embedded file block's or an embedded file container's file, a
	// recording among them.
	EmbeddedFileMedium MediumKind = "embedded file"
	// InkMedium is an ink record: the page's own ink, a paragraph's handwriting, or an ink block.
	InkMedium MediumKind = "ink"
)

// Medium is one image, embedded file, or ink drawing a page names, wherever it sits, with the
// route its content goes to next.
type Medium struct {
	// Kind is what it is.
	Kind MediumKind
	// File is its content's name in the media directory, as its record gives it; "" when
	// onenote wrote no content for it, its data missing or invalid.
	File string
	// Path is where the caller opens its content: File in the media directory Media was given;
	// "" when Media was given none, or File is "".
	Path string
	// Filename is the name an image was inserted from or a file attached under, as stored; never
	// a path to use. Ink has none.
	Filename string
	// Size is its content's size in bytes, when its data is there; nil otherwise, and for ink.
	Size *int64
	// Type is an embedded file's: "audio" or "video" for a recording; "" otherwise.
	Type string
	// Where is where it sits on the page, for a dispatcher to report a part's origin: each step
	// down from the page, joined by ", ". A container is its kind and its place in the page's
	// Content, from 1: "title 1", "outline 2", "image container 3", "embedded file container 4".
	// Below it come "item N" for an outline's, a title's, or a group's Nth item, "child N" for an
	// element's Nth child, "block N" for an element's Nth block, "table cell rRcC" for a table's
	// cell at row R, column C, and "paragraph ink" for a paragraph's handwriting, such as
	// "outline 2, item 1, block 3, table cell r1c2, item 1, block 1". The page's own ink is
	// "page ink".
	Where string
	// Route is the stage its content goes to next, and Reason why, in a few words.
	Route  route.Route
	Reason string
	// Image, EmbeddedFile, and Ink are its typed record: the one of its Kind is set, the others
	// nil. An image container's or embedded file container's is the container's own.
	Image        *Image
	EmbeddedFile *EmbeddedFile
	Ink          *Ink
}

// Media iterates every image, embedded file, and ink drawing the page names, depth-first in
// its Content's order, then the page's own Ink: its image and embedded file containers; its
// title's and outlines' items at any depth, an element's blocks before its children and a
// group's items in order; each block's image, embedded file, or ink; a paragraph's handwriting;
// and a table's cells, row by row, their items as an outline's. An ink container is no medium:
// its strokes are drawn in the page's Ink. An ink block without strokes has no ink record, and a
// container, item, or block of a kind the package doesn't know is not looked into, so neither
// yields one.
//
// mediaDir is the media directory the read's Request gave onenote, "" for none. Each medium is
// routed by route.File over its content's file there, the name whose extension it reads being
// Filename, else File, and no stored MIME type; ink's reason names it OneNote ink, its file an
// SVG, which route.File routes route.VisionConverted. A recording, an embedded file of Type
// "audio" or "video", is routed by its signature first: when route.File's route stands on a
// signature, it stands, whatever Type says, so a recording whose bytes are a video's is
// route.Video and one whose bytes are an executable's stays route.Unrouted. Only content of no
// signature route.Identify knows takes its route from Type, route.Audio or route.Video. A medium
// is route.Unrouted, with no Path, when onenote wrote no content for it (File is ""), when
// mediaDir is "", and when its file isn't in mediaDir; and when File isn't a plain file name,
// which onenote never writes.
//
// A medium whose file is there but can't be read is yielded with the error, its route unset; a
// loop may go on to the next. Media reads nothing but the media's files.
func (p *Page) Media(mediaDir string) iter.Seq2[Medium, error] {
	return func(yield func(Medium, error) bool) {
		w := mediaWalk{dir: mediaDir, yield: yield}
		for i, c := range p.Content {
			at := strconv.Itoa(i + 1)
			var ok bool
			switch c := c.(type) {
			case *Title:
				ok = w.items(c.Items, "title "+at, "item")
			case *Outline:
				ok = w.items(c.Items, "outline "+at, "item")
			case *ImageContainer:
				ok = w.image(&c.Image, "image container "+at)
			case *EmbeddedFileContainer:
				ok = w.embeddedFile(&c.EmbeddedFile, "embedded file container "+at)
			default:
				ok = true
			}
			if !ok {
				return
			}
		}
		if p.Ink != nil {
			w.ink(p.Ink, "page ink")
		}
	}
}

// mediaWalk is one walk of a page's media: the media directory, and the loop's yield. Each of
// its methods returns false once the loop has stopped.
type mediaWalk struct {
	dir   string
	yield func(Medium, error) bool
}

// items walks list, its items each named step and its place under where.
func (w *mediaWalk) items(list Items, where, step string) bool {
	for i, it := range list {
		at := where + ", " + step + " " + strconv.Itoa(i+1)
		switch it := it.(type) {
		case *Element:
			if !w.blocks(it.Blocks, at) || !w.items(it.Children, at, "child") {
				return false
			}
		case *Group:
			if !w.items(it.Items, at, "item") {
				return false
			}
		}
	}
	return true
}

// blocks walks an element's blocks under where.
func (w *mediaWalk) blocks(list Blocks, where string) bool {
	for i, b := range list {
		at := where + ", block " + strconv.Itoa(i+1)
		var ok bool
		switch b := b.(type) {
		case *Paragraph:
			ok = b.Ink == nil || w.ink(b.Ink, at+", paragraph ink")
		case *Table:
			ok = w.table(b, at)
		case *Image:
			ok = w.image(b, at)
		case *EmbeddedFile:
			ok = w.embeddedFile(b, at)
		case *InkBlock:
			ok = b.Ink == nil || w.ink(b.Ink, at)
		default:
			ok = true
		}
		if !ok {
			return false
		}
	}
	return true
}

// table walks a table's cells, row by row, under where.
func (w *mediaWalk) table(t *Table, where string) bool {
	for r, row := range t.Rows {
		for c, cell := range row {
			at := where + ", table cell r" + strconv.Itoa(r+1) + "c" + strconv.Itoa(c+1)
			if !w.items(cell.Items, at, "item") {
				return false
			}
		}
	}
	return true
}

// image yields img, found at where.
func (w *mediaWalk) image(img *Image, where string) bool {
	return w.medium(Medium{
		Kind: ImageMedium, File: img.File, Filename: img.Filename, Size: img.Size,
		Where: where, Image: img,
	})
}

// embeddedFile yields f, found at where.
func (w *mediaWalk) embeddedFile(f *EmbeddedFile, where string) bool {
	return w.medium(Medium{
		Kind: EmbeddedFileMedium, File: f.File, Filename: f.Filename, Size: f.Size, Type: f.Type,
		Where: where, EmbeddedFile: f,
	})
}

// ink yields ink, found at where.
func (w *mediaWalk) ink(ink *Ink, where string) bool {
	return w.medium(Medium{Kind: InkMedium, File: ink.File, Where: where, Ink: ink})
}

// medium routes m and yields it, with the error routing it returned, if any.
func (w *mediaWalk) medium(m Medium) bool {
	err := w.route(&m)
	return w.yield(m, err)
}

// route sets m's Path, Route, and Reason, as Media documents.
func (w *mediaWalk) route(m *Medium) error {
	switch {
	case m.File == "":
		m.Route, m.Reason = route.Unrouted, "no content written: its data is missing or invalid"
		return nil
	case w.dir == "":
		m.Route, m.Reason = route.Unrouted, "not written: no media directory"
		return nil
	case m.File != filepath.Base(m.File) || m.File == "." || m.File == "..":
		m.Route, m.Reason = route.Unrouted, "its file name isn't a plain file name"
		return nil
	}
	path := filepath.Join(w.dir, m.File)
	name := m.Filename
	if name == "" {
		name = m.File
	}
	r, why, err := route.File(path, name, "")
	if errors.Is(err, fs.ErrNotExist) {
		m.Route, m.Reason = route.Unrouted, "not written: its file isn't in the media directory"
		return nil
	}
	if err != nil {
		return fmt.Errorf("onenote: routing %s at %s: %w", m.File, m.Where, err)
	}
	m.Path = path
	switch {
	case m.Kind == InkMedium:
		why = "OneNote ink, an SVG: " + why
	case m.Type == "audio" || m.Type == "video":
		if r == route.Unrouted {
			known, err := signed(path)
			if err != nil {
				m.Path = ""
				return fmt.Errorf("onenote: routing %s at %s: %w", m.File, m.Where, err)
			}
			if !known {
				// No signature decides, so the recording's type does.
				r = route.Audio
				if m.Type == "video" {
					r = route.Video
				}
				why = "OneNote " + m.Type + " recording, of no known signature"
			}
		}
	}
	m.Route, m.Reason = r, why
	return nil
}

// signed reports whether the file at path begins with a signature route.Identify knows.
func signed(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, route.HeadLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return route.Identify(head[:n]) != route.Unknown, nil
}
