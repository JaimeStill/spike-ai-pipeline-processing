//go:build findings && linux

package corpustest

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// DocsEnv names the corpus root; unset, Root returns ~/docs.
const DocsEnv = "DOCS"

// Root returns the corpus root DocsEnv names, ~/docs when it is unset.
func Root(t testing.TB) string {
	t.Helper()
	if docs := os.Getenv(DocsEnv); docs != "" {
		return docs
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "docs")
}

// Entry is the part of a corpus manifest entry a finding reads.
type Entry struct {
	Path            string `json:"path"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
	License         string `json:"license"`
	Redistributable bool   `json:"redistributable"`
	SourceURL       string `json:"source_url"`
}

// Check checks each of paths in the corpus at docs against its MANIFEST.json entry by its SHA-256
// and size, and returns the entries, in paths' order.
func Check(docs string, paths []string) ([]Entry, error) {
	data, err := os.ReadFile(filepath.Join(docs, "MANIFEST.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Files []Entry `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(docs, "MANIFEST.json"), err)
	}
	var out []Entry
	for _, p := range paths {
		i := slices.IndexFunc(manifest.Files, func(e Entry) bool { return e.Path == p })
		if i < 0 {
			return nil, fmt.Errorf("%s isn't in the corpus manifest", p)
		}
		e := manifest.Files[i]
		f, err := os.Open(filepath.Join(docs, p))
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		if sum := hex.EncodeToString(h.Sum(nil)); sum != e.SHA256 || n != e.Bytes {
			return nil, fmt.Errorf("%s: SHA-256 %s, %d bytes; the manifest's %s, %d bytes", p, sum, n, e.SHA256, e.Bytes)
		}
		out = append(out, e)
	}
	return out, nil
}

// AMITruth are AMI ES2002a's truth files in the corpus, each speaker's words and segments, which
// ReadAMI reads.
var AMITruth = []string{
	"audio/long/ami-es2002a-truth/ES2002a.A.words.xml", "audio/long/ami-es2002a-truth/ES2002a.A.segments.xml",
	"audio/long/ami-es2002a-truth/ES2002a.B.words.xml", "audio/long/ami-es2002a-truth/ES2002a.B.segments.xml",
	"audio/long/ami-es2002a-truth/ES2002a.C.words.xml", "audio/long/ami-es2002a-truth/ES2002a.C.segments.xml",
	"audio/long/ami-es2002a-truth/ES2002a.D.words.xml", "audio/long/ami-es2002a-truth/ES2002a.D.segments.xml",
}

// amiWord is one word of an AMI words file: its text and times, and its place among the file's
// elements.
type amiWord struct {
	ordinal    int
	text       string
	start, end float64
}

// amiSegment is one segment of an AMI segments file: its start, and the range of its words file's
// elements it holds.
type amiSegment struct {
	speaker     string
	start       float64
	first, last string
}

// amiRange matches a segment's child reference: one element, or a range of them.
var amiRange = regexp.MustCompile(`#id\(([^)]+)\)(?:\.\.id\(([^)]+)\))?`)

// ReadAMI reads ES2002a's four speakers' words and segments from the corpus at docs. The truth,
// text, is every segment's words, segments in order of their start and then speaker, each
// segment's words in its file's order: the <w> elements, punctuation (punc="true") left out, vocal
// sounds, disfluency markers, and gaps too. excerpt is the same words whose middle is before
// within, empty when within is zero. Words are separated by single spaces.
func ReadAMI(docs string, within time.Duration) (text, excerpt string, err error) {
	type placed struct {
		seg   amiSegment
		words []amiWord
	}
	var all []placed
	for _, speaker := range []string{"A", "B", "C", "D"} {
		dir := filepath.Join(docs, "audio/long/ami-es2002a-truth")
		words, ordinals, err := readAMIWords(filepath.Join(dir, "ES2002a."+speaker+".words.xml"))
		if err != nil {
			return "", "", err
		}
		segs, err := readAMISegments(filepath.Join(dir, "ES2002a."+speaker+".segments.xml"), speaker)
		if err != nil {
			return "", "", err
		}
		covered := 0
		for _, s := range segs {
			first, ok1 := ordinals[s.first]
			last, ok2 := ordinals[s.last]
			if !ok1 || !ok2 {
				return "", "", fmt.Errorf("speaker %s: segment at %.3f s names %s..%s, not in its words", speaker, s.start, s.first, s.last)
			}
			p := placed{seg: s}
			for _, w := range words {
				if w.ordinal >= first && w.ordinal <= last {
					p.words = append(p.words, w)
				}
			}
			covered += len(p.words)
			all = append(all, p)
		}
		if covered != len(words) {
			return "", "", fmt.Errorf("speaker %s: segments hold %d of %d words", speaker, covered, len(words))
		}
	}
	slices.SortStableFunc(all, func(a, b placed) int {
		return cmp.Or(cmp.Compare(a.seg.start, b.seg.start), cmp.Compare(a.seg.speaker, b.seg.speaker))
	})
	var words, inside []string
	for _, p := range all {
		for _, w := range p.words {
			words = append(words, w.text)
			if (w.start+w.end)/2 < within.Seconds() {
				inside = append(inside, w.text)
			}
		}
	}
	return strings.Join(words, " "), strings.Join(inside, " "), nil
}

// latin1 is the CharsetReader for AMI's ISO-8859-1 files: each byte is the rune of its value.
func latin1(charset string, r io.Reader) (io.Reader, error) {
	if !strings.EqualFold(charset, "ISO-8859-1") {
		return nil, fmt.Errorf("charset %s", charset)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, c := range data {
		b.WriteRune(rune(c))
	}
	return strings.NewReader(b.String()), nil
}

// readAMIWords returns an AMI words file's words, punctuation left out, and every element's
// ordinal by its id, which a segment's range names.
func readAMIWords(path string) ([]amiWord, map[string]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = latin1
	var words []amiWord
	ordinals := map[string]int{}
	ordinal := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local == "root" {
			continue
		}
		attr := map[string]string{}
		for _, a := range se.Attr {
			attr[a.Name.Local] = a.Value
		}
		ordinal++
		ordinals[attr["id"]] = ordinal
		if se.Name.Local != "w" {
			continue
		}
		var w struct {
			Text string `xml:",chardata"`
		}
		if err := d.DecodeElement(&w, &se); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		if attr["punc"] == "true" {
			continue
		}
		start, err1 := strconv.ParseFloat(attr["starttime"], 64)
		end, err2 := strconv.ParseFloat(attr["endtime"], 64)
		if err := cmp.Or(err1, err2); err != nil {
			return nil, nil, fmt.Errorf("%s: word %s: %w", path, attr["id"], err)
		}
		words = append(words, amiWord{ordinal: ordinal, text: strings.TrimSpace(w.Text), start: start, end: end})
	}
	return words, ordinals, nil
}

// readAMISegments returns an AMI segments file's segments.
func readAMISegments(path, speaker string) ([]amiSegment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = latin1
	var segs []amiSegment
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "segment" {
			continue
		}
		var s struct {
			Start string `xml:"transcriber_start,attr"`
			Child []struct {
				Href string `xml:"href,attr"`
			} `xml:"child"`
		}
		if err := d.DecodeElement(&s, &se); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		start, err := strconv.ParseFloat(s.Start, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: segment start %q: %w", path, s.Start, err)
		}
		for _, c := range s.Child {
			m := amiRange.FindStringSubmatch(c.Href)
			if m == nil {
				return nil, fmt.Errorf("%s: segment child %q", path, c.Href)
			}
			segs = append(segs, amiSegment{speaker: speaker, start: start, first: m[1], last: cmp.Or(m[2], m[1])})
		}
	}
	return segs, nil
}

// srtTiming matches a SubRip cue's timing line, its start and end.
var srtTiming = regexp.MustCompile(`^(\d+):(\d{2}):(\d{2})[,.](\d{3})\s+-->\s+(\d+):(\d{2}):(\d{2})[,.](\d{3})`)

// srtTag matches a SubRip cue's markup, such as <i> and {\an8}, which ReadSRT drops.
var srtTag = regexp.MustCompile(`<[^>]*>|\{[^}]*\}`)

// ReadSRT reads the SubRip file at path and returns its cues' text, in file order, markup dropped
// and the lines of every cue whose middle is before within joined by single spaces; within zero
// takes every cue. It returns the cues taken, too. A subtitle file isn't a verbatim transcript, so
// a finding scores against it as approximate truth.
func ReadSRT(path string, within time.Duration) (text string, cues int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	var (
		words []string
		take  bool
		inCue bool
	)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		switch m := srtTiming.FindStringSubmatch(line); {
		case m != nil:
			start, end := srtTime(m[1:5]), srtTime(m[5:9])
			take = within == 0 || (start+end)/2 < within
			inCue = true
			if take {
				cues++
			}
		case line == "":
			inCue = false
		case inCue && take:
			words = append(words, strings.Fields(srtTag.ReplaceAllString(line, " "))...)
		}
	}
	if err := sc.Err(); err != nil {
		return "", 0, err
	}
	return strings.Join(words, " "), cues, nil
}

// srtTime returns a SubRip time's hours, minutes, seconds, and milliseconds as a duration; the
// timing pattern has checked them as digits.
func srtTime(parts []string) time.Duration {
	n := make([]int, 4)
	for i, p := range parts {
		n[i], _ = strconv.Atoi(p)
	}
	return time.Duration(n[0])*time.Hour + time.Duration(n[1])*time.Minute +
		time.Duration(n[2])*time.Second + time.Duration(n[3])*time.Millisecond
}
