package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
)

// textVersion is the version of the text stage, which runs no tool: this package's. Its
// decoding is golang.org/x/text's, at the version go.mod requires; a change to that module
// that changes what the stage yields for the same bytes changes textVersion, so the index
// prepares text again.
const textVersion = "1"

// The encodings the text stage names in its output's Meta "encoding", and how it decided each.
const (
	textByBOM    = " (byte order mark)"
	textFallback = "windows-1252 (fallback)"
)

// textEncoding is one encoding the text stage decodes: its name, the byte order mark that
// decides it, if any, its decoder, and how it stores U+FFFD, the unit it is aligned on, so the
// stage counts only the U+FFFD its decoding put in, never those the text stored.
type textEncoding struct {
	name string
	bom  []byte
	enc  encoding.Encoding
	// fffd is U+FFFD as the encoding stores it, nil for one that can't store it.
	fffd []byte
	// unit is the alignment fffd is found on, from the end of the byte order mark.
	unit int
}

// textBOMs are the encodings a byte order mark decides, the UTF-32 marks before the UTF-16
// ones they begin with, as route.Identify reads them.
var textBOMs = []textEncoding{
	{"utf-8", []byte{0xef, 0xbb, 0xbf}, unicode.UTF8, []byte{0xef, 0xbf, 0xbd}, 1},
	{"utf-32le", []byte{0xff, 0xfe, 0x00, 0x00}, utf32.UTF32(utf32.LittleEndian, utf32.IgnoreBOM), []byte{0xfd, 0xff, 0x00, 0x00}, 4},
	{"utf-32be", []byte{0x00, 0x00, 0xfe, 0xff}, utf32.UTF32(utf32.BigEndian, utf32.IgnoreBOM), []byte{0x00, 0x00, 0xff, 0xfd}, 4},
	{"utf-16le", []byte{0xff, 0xfe}, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), []byte{0xfd, 0xff}, 2},
	{"utf-16be", []byte{0xfe, 0xff}, unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), []byte{0xff, 0xfd}, 2},
}

// textWindows1252 is the fallback for text that has no byte order mark and isn't UTF-8.
// golang.org/x/text decodes the five bytes windows-1252 leaves undefined, 0x81, 0x8d, 0x8f, 0x90,
// and 0x9d, as U+FFFD.
var textWindows1252 = textEncoding{name: textFallback, enc: charmap.Windows1252, unit: 1}

// textChunk is how much of a text part the stage reads at once.
const textChunk = 64 << 10

// TextStage prepares text, route.Text's parts, to one [TextOutput] named "text", UTF-8, its line
// endings as stored. Its encoding is decided, never detected, in this order: a byte order mark,
// UTF-8's, UTF-32's, or UTF-16's, either byte order, which the output drops; else UTF-8, when the
// whole part is valid UTF-8, ASCII among it; else windows-1252, as a fallback, with a warning
// that the text may be in another encoding. Its decoding is golang.org/x/text's. Its Meta
// "encoding" names the encoding and how it was decided: "utf-8 (byte order mark)", "utf-16le
// (byte order mark)", and the like, "utf-8", or "windows-1252 (fallback)". Bytes the encoding
// can't decode, invalid UTF-8 after UTF-8's mark, an unpaired UTF-16 surrogate, a code point past
// U+10FFFF, a trailing partial unit, or a byte windows-1252 leaves undefined, become U+FFFD, and
// the Meta "warnings" says how many, a line each, as the office stage's warnings are.
//
// Valid UTF-8 without a mark is passed through, the output's Path the part's own file, as the
// vision stage passes a picture; any other text is decoded, streamed, into a file in the part's
// directory, so a large part is never held in memory. It sets no cap of its own: a large part is
// held to the dispatcher's [Limits], as every part is. It runs no tool, and its identity's
// version is this package's.
type TextStage struct {
	id Identity
}

// NewTextStage builds a TextStage.
func NewTextStage() *TextStage {
	hash, err := HashConfig(struct {
		Adapter string `json:"adapter"`
	}{textVersion})
	if err != nil {
		// A struct of one string always marshals.
		panic(err)
	}
	return &TextStage{id: Identity{Name: "text", Version: textVersion, Config: hash}}
}

// Identity returns the stage's identity: "text", this package's version of it, and a hash of no
// options.
func (s *TextStage) Identity() Identity { return s.id }

// Prepare decodes the text at p.Path, as the type's doc says.
func (s *TextStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		o, err := decodeText(ctx, p.Path, dir)
		if err != nil {
			yield(Item{}, fmt.Errorf("text stage: %w", err))
			return
		}
		yield(OutputItem(o), nil)
	}
}

// decodeText decides the encoding of the text at path and returns its output: the file itself
// for UTF-8 without a mark, else the text decoded into a file in dir.
func decodeText(ctx context.Context, path, dir string) (Output, error) {
	f, err := os.Open(path)
	if err != nil {
		return Output{}, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 4)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Output{}, err
	}
	head = head[:n]
	for _, e := range textBOMs {
		if bytes.HasPrefix(head, e.bom) {
			e.name += textByBOM
			return decodeFile(ctx, f, int64(len(e.bom)), e, dir, nil)
		}
	}
	valid, err := validUTF8(ctx, f)
	if err != nil {
		return Output{}, err
	}
	if valid {
		return textOutput(path, "utf-8", nil), nil
	}
	return decodeFile(ctx, f, 0, textWindows1252, dir,
		[]string{"no byte order mark and not UTF-8: decoded as windows-1252, which may not be the text's encoding"})
}

// decodeFile decodes f from offset in e into a file in dir and returns its output, its warnings
// those given and, when the decoding put any U+FFFD in, how many.
func decodeFile(ctx context.Context, f *os.File, offset int64, e textEncoding, dir string, warnings []string) (o Output, err error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return Output{}, err
	}
	stored := &fffdCounter{pattern: e.fffd, unit: e.unit}
	src := io.TeeReader(&ctxReader{ctx: ctx, r: f}, stored)
	path := filepath.Join(dir, "text.txt")
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Output{}, err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	decoded := &fffdCounter{pattern: []byte(string(utf8.RuneError)), unit: 1}
	bw := bufio.NewWriterSize(io.MultiWriter(out, decoded), textChunk)
	if _, err := io.Copy(bw, e.enc.NewDecoder().Reader(src)); err != nil {
		return Output{}, fmt.Errorf("decode as %s: %w", e.name, err)
	}
	if err := bw.Flush(); err != nil {
		return Output{}, err
	}
	if put := decoded.n - stored.n; put > 0 {
		name, _, _ := strings.Cut(e.name, " ")
		warnings = append(warnings, strconv.Itoa(put)+" U+FFFD replacing what "+name+" can't decode")
	}
	return textOutput(path, e.name, warnings), nil
}

// textOutput returns the text output of the UTF-8 file at path, decided as encoding, with
// warnings.
func textOutput(path, encoding string, warnings []string) Output {
	meta := map[string]string{"encoding": encoding}
	if len(warnings) > 0 {
		meta["warnings"] = strings.Join(warnings, "\n")
	}
	return Output{Kind: TextOutput, Name: "text", MediaType: "text/plain", Path: path, Meta: meta}
}

// validUTF8 reports whether f, from its start, is valid UTF-8, reading it a chunk at a time,
// a rune split across chunks carried into the next.
func validUTF8(ctx context.Context, f *os.File) (bool, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	r := &ctxReader{ctx: ctx, r: f}
	buf := make([]byte, textChunk+utf8.UTFMax)
	carry := 0
	for {
		n, err := r.Read(buf[carry : carry+textChunk])
		n += carry
		if err == io.EOF {
			return utf8.Valid(buf[:n]), nil
		}
		if err != nil {
			return false, err
		}
		// Hold back a rune the chunk ends inside: from the last rune's start, if it isn't whole.
		end := n
		for i := n - 1; i >= 0 && i >= n-utf8.UTFMax; i-- {
			if utf8.RuneStart(buf[i]) {
				if !utf8.FullRune(buf[i:n]) {
					end = i
				}
				break
			}
		}
		if !utf8.Valid(buf[:end]) {
			return false, nil
		}
		carry = copy(buf, buf[end:n])
	}
}

// fffdCounter counts the times pattern occurs in what is written to it, at offsets a multiple of
// unit from the first byte; a nil pattern counts none.
type fffdCounter struct {
	pattern []byte
	unit    int
	n       int
	off     int64
	window  []byte
}

func (c *fffdCounter) Write(p []byte) (int, error) {
	if len(c.pattern) == 0 {
		return len(p), nil
	}
	k := len(c.pattern)
	for _, b := range p {
		if len(c.window) == k {
			copy(c.window, c.window[1:])
			c.window[k-1] = b
		} else {
			c.window = append(c.window, b)
		}
		c.off++
		if len(c.window) == k && (c.off-int64(k))%int64(c.unit) == 0 && bytes.Equal(c.window, c.pattern) {
			c.n++
		}
	}
	return len(p), nil
}

// ctxReader reads r until ctx ends, so a large part's decoding stops with the dispatch.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
