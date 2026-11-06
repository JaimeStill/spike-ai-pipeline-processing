//go:build findings && linux

package audio_test

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
	"unicode"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
)

// findingSource is one long source of the audio finding, in the test corpus at ~/docs: its audio,
// its truth files, and how its truth is read.
type findingSource struct {
	// Name names the source in AUDIO_FINDINGS_SOURCE and its results file.
	Name string
	// Title describes it.
	Title string
	// Audio is the audio's path in the corpus.
	Audio string
	// Truth are the truth files' paths in the corpus, each checked against the manifest.
	Truth []string
	// Redistributable is whether the audio may be shared, as the manifest says: only a
	// redistributable source's transcripts are kept in full in its results.
	Redistributable bool
	// TruthNote is the truth's caveat, logged and kept in the results.
	TruthNote string
	// read reads the truth from the corpus root.
	read func(docs string) (truth, error)
}

// truth is a source's truth as the finding reads it.
type truth struct {
	// Text is the whole truth, its words separated by single spaces.
	Text string
	// Excerpt is the truth of the source's first excerptLength, when the truth gives each word's
	// time: every word whose middle is before it. Empty when the truth has no times; then the
	// excerpt's truth is derived from the transcripts (excerptTruth).
	Excerpt string
	// Timed is whether Excerpt was cut by time.
	Timed bool
	// CrossCheck, when not nil, gives where the excerpt ends in Text's normalized words by
	// another means than the transcripts, to check the derived end against.
	CrossCheck *crossCheck
}

// crossCheck is a second reckoning of where the excerpt ends in the truth's normalized words.
type crossCheck struct {
	// Method says how it was reckoned.
	Method string `json:"method"`
	// Words is the excerpt's end, in the truth's normalized words.
	Words int `json:"words"`
}

// findingSources are the finding's sources, in the order the task runs them.
var findingSources = []findingSource{
	{
		Name:  "ami",
		Title: "AMI ES2002a, Mix-Headset (21.2 min, 4 speakers, a meeting)",
		Audio: "audio/long/ami-es2002a-mix-headset.wav",
		Truth: []string{
			"audio/long/ami-es2002a-truth/ES2002a.A.words.xml", "audio/long/ami-es2002a-truth/ES2002a.A.segments.xml",
			"audio/long/ami-es2002a-truth/ES2002a.B.words.xml", "audio/long/ami-es2002a-truth/ES2002a.B.segments.xml",
			"audio/long/ami-es2002a-truth/ES2002a.C.words.xml", "audio/long/ami-es2002a-truth/ES2002a.C.segments.xml",
			"audio/long/ami-es2002a-truth/ES2002a.D.words.xml", "audio/long/ami-es2002a-truth/ES2002a.D.segments.xml",
		},
		Redistributable: true,
		TruthNote: "AMI's manual word-level transcription of each speaker, ordered by its segments' starts, " +
			"so a segment's words stay together; overlapping speech is ordered by segment, which no " +
			"single-stream transcript can match exactly",
		read: readAMI,
	},
	{
		Name:  "earnings21",
		Title: "Earnings-21 4330115 (41.0 min, 8 speakers, an earnings call, MP3)",
		Audio: "audio/long/earnings21-4330115.mp3",
		Truth: []string{"audio/long/earnings21-4330115.truth.nlp"},
		TruthNote: "Rev's verbatim reference transcript, tokens in order without times; numbers as written " +
			"(\"2020\"), which the normalization doesn't verbalize",
		read: func(docs string) (truth, error) {
			text, err := readNLP(filepath.Join(docs, "audio/long/earnings21-4330115.truth.nlp"))
			return truth{Text: text}, err
		},
	},
	{
		Name:  "earnings22",
		Title: "Earnings-22 4479944 (64.9 min, an earnings call in accented English, MP3 at 44.1 kHz)",
		Audio: "audio/long/earnings22-4479944.mp3",
		Truth: []string{"audio/long/earnings22-4479944.truth.nlp", "audio/long/earnings22-4479944.truth-aligned.nlp"},
		TruthNote: "Rev's verbatim reference transcript, tokens in order without times; numbers as written. " +
			"Rev's force-aligned copy, its tokens verbalized and with times, checks where the excerpt ends",
		read: readEarnings22,
	},
	{
		Name:            "librivox",
		Title:           "LibriVox, The Statement of Stella Maberly (4.4 h, one reader, an audiobook, M4B, one stream)",
		Audio:           "audio/long/librivox-stella-maberly.m4b",
		Truth:           []string{"audio/long/librivox-stella-maberly.truth-approx.txt"},
		Redistributable: true,
		TruthNote: "approximate: the 1896 edition's OCR (Internet Archive), cleaned by cleanBookOCR; OCR " +
			"misreadings stay, chapter headings are numerals where the reader says \"Chapter one\", and " +
			"LibriVox's spoken preamble and closing of each chapter are in no truth",
		read: func(docs string) (truth, error) {
			data, err := os.ReadFile(filepath.Join(docs, "audio/long/librivox-stella-maberly.truth-approx.txt"))
			return truth{Text: cleanBookOCR(string(data))}, err
		},
	},
}

// manifestEntry is the part of a corpus manifest entry the finding reads.
type manifestEntry struct {
	Path            string `json:"path"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
	License         string `json:"license"`
	Redistributable bool   `json:"redistributable"`
	SourceURL       string `json:"source_url"`
}

// checkCorpus checks each of paths in the corpus at docs against its MANIFEST.json entry by its
// SHA-256 and size, and returns the entries.
func checkCorpus(docs string, paths []string) ([]manifestEntry, error) {
	data, err := os.ReadFile(filepath.Join(docs, "MANIFEST.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Files []manifestEntry `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(docs, "MANIFEST.json"), err)
	}
	var out []manifestEntry
	for _, p := range paths {
		i := slices.IndexFunc(manifest.Files, func(e manifestEntry) bool { return e.Path == p })
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

// readNLP returns the tokens of a Rev .nlp file, its first column, in order, separated by single
// spaces; the punctuation column is left out.
func readNLP(path string) (string, error) {
	tokens, _, err := nlpTokens(path)
	return strings.Join(tokens, " "), err
}

// nlpTokens returns a .nlp file's tokens and each one's start time, its ts column, as given:
// empty for a token with none.
func nlpTokens(path string) (tokens, starts []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	header := true
	for sc.Scan() {
		if header {
			header = false
			continue
		}
		cols := strings.Split(sc.Text(), "|")
		if len(cols) < 3 {
			return nil, nil, fmt.Errorf("%s: line %q has %d columns", path, sc.Text(), len(cols))
		}
		tokens, starts = append(tokens, cols[0]), append(starts, cols[2])
	}
	return tokens, starts, sc.Err()
}

// readEarnings22 reads Earnings-22 4479944's reference, and checks the excerpt's end against Rev's
// force-aligned copy: the aligned tokens before the first one timed at or after excerptLength,
// normalized, mapped to the reference's normalized words through one alignment of the two.
func readEarnings22(docs string) (truth, error) {
	text, err := readNLP(filepath.Join(docs, "audio/long/earnings22-4479944.truth.nlp"))
	if err != nil {
		return truth{}, err
	}
	tokens, starts, err := nlpTokens(filepath.Join(docs, "audio/long/earnings22-4479944.truth-aligned.nlp"))
	if err != nil {
		return truth{}, err
	}
	cut := len(tokens)
	for i, s := range starts {
		if s == "" {
			continue
		}
		ts, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return truth{}, fmt.Errorf("aligned token %d's time %q: %w", i, s, err)
		}
		if ts >= excerptLength.Seconds() {
			cut = i
			break
		}
	}
	ref := textscore.NormalizeWords(text)
	aligned := textscore.NormalizeWords(strings.Join(tokens, " "))
	within := len(textscore.NormalizeWords(strings.Join(tokens[:cut], " ")))
	words := len(ref)
	for _, e := range textscore.Align(ref, aligned) {
		if e.Got >= within {
			words = e.Truth
			break
		}
	}
	return truth{Text: text, CrossCheck: &crossCheck{
		Method: fmt.Sprintf("Rev's force-aligned tokens before the first timed at %.0f s or later (%d of %d tokens), "+
			"mapped to the reference's normalized words through one alignment", excerptLength.Seconds(), cut, len(tokens)),
		Words: words,
	}}, nil
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

// readAMI reads ES2002a's four speakers' words and segments. The truth is every segment's words,
// segments in order of their start and then speaker, each segment's words in its file's order:
// the <w> elements, punctuation (punc="true") left out, vocal sounds, disfluency markers, and
// gaps too. The excerpt's truth is the same words whose middle is before excerptLength.
func readAMI(docs string) (truth, error) {
	type placed struct {
		seg   amiSegment
		words []amiWord
	}
	var all []placed
	for _, speaker := range []string{"A", "B", "C", "D"} {
		dir := filepath.Join(docs, "audio/long/ami-es2002a-truth")
		words, ordinals, err := readAMIWords(filepath.Join(dir, "ES2002a."+speaker+".words.xml"))
		if err != nil {
			return truth{}, err
		}
		segs, err := readAMISegments(filepath.Join(dir, "ES2002a."+speaker+".segments.xml"), speaker)
		if err != nil {
			return truth{}, err
		}
		covered := 0
		for _, s := range segs {
			first, ok1 := ordinals[s.first]
			last, ok2 := ordinals[s.last]
			if !ok1 || !ok2 {
				return truth{}, fmt.Errorf("speaker %s: segment at %.3f s names %s..%s, not in its words", speaker, s.start, s.first, s.last)
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
			return truth{}, fmt.Errorf("speaker %s: segments hold %d of %d words", speaker, covered, len(words))
		}
	}
	slices.SortStableFunc(all, func(a, b placed) int {
		return cmp.Or(cmp.Compare(a.seg.start, b.seg.start), cmp.Compare(a.seg.speaker, b.seg.speaker))
	})
	var text, excerpt []string
	for _, p := range all {
		for _, w := range p.words {
			text = append(text, w.text)
			if (w.start+w.end)/2 < excerptLength.Seconds() {
				excerpt = append(excerpt, w.text)
			}
		}
	}
	return truth{Text: strings.Join(text, " "), Excerpt: strings.Join(excerpt, " "), Timed: true}, nil
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

// runningHead matches the OCR's running heads, "The Statement of Stella Maberly" misread a page
// at a time, such as "viii    QTlje  Statement  of  Stella  ittaberls-".
var runningHead = regexp.MustCompile(`Statement\s+of\s+Stella`)

// cleanBookOCR returns the book's text from its OCR, as the finding scores the audiobook against:
//
//  1. The lines from the first "INTRODUCTION." to the first "THE END.", both kept: the library
//     stamps, title pages, and the publisher's advertisements after the story are dropped.
//  2. A running head, a line naming "Statement of Stella", is dropped with its page number.
//  3. A line with no letter, a page number, is dropped, and so is a line of only i, v, x, l, and u,
//     a front-matter page number in Roman numerals as OCR read it ("vu").
//  4. A word a line's end hyphenates is joined to the first word of the next line that holds one:
//     "won-" and "der" are "wonder". A hyphenated compound broken at its hyphen is joined too,
//     which the normalization, splitting at hyphens, would have split.
//
// What OCR misread inside the text stays: the truth is approximate.
func cleanBookOCR(ocr string) string {
	lines := strings.Split(ocr, "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "INTRODUCTION." })
	end := slices.IndexFunc(lines, func(l string) bool { return strings.Join(strings.Fields(l), " ") == "THE END." })
	if start < 0 || end < start {
		return ""
	}
	var words []string
	joining := false
	for _, l := range lines[start : end+1] {
		if runningHead.MatchString(l) || !strings.ContainsFunc(l, unicode.IsLetter) ||
			strings.Trim(strings.TrimSpace(l), "ivxlu") == "" {
			continue
		}
		fields := strings.Fields(l)
		if joining {
			words[len(words)-1] += fields[0]
			fields = fields[1:]
		}
		words = append(words, fields...)
		joining = false
		if n := len(words); n > 0 {
			last := []rune(words[n-1])
			if len(last) > 1 && last[len(last)-1] == '-' && unicode.IsLetter(last[len(last)-2]) {
				words[n-1] = string(last[:len(last)-1])
				joining = true
			}
		}
	}
	return strings.Join(words, " ")
}

// TestFindingsTruth reads every source's truth from the corpus, each file checked against the
// manifest, with no model: it logs each truth's normalized words, its first and last words, and
// the timed excerpt's or the cross-check's words, so the readers can be checked before a run.
func TestFindingsTruth(t *testing.T) {
	docs := corpusRoot(t)
	for _, src := range findingSources {
		t.Run(src.Name, func(t *testing.T) {
			if _, err := checkCorpus(docs, append([]string{src.Audio}, src.Truth...)); err != nil {
				t.Fatal(err)
			}
			tru, err := src.read(docs)
			if err != nil {
				t.Fatal(err)
			}
			words := textscore.NormalizeWords(tru.Text)
			if len(words) == 0 {
				t.Fatal("the truth has no words")
			}
			head, tail := words[:min(len(words), 12)], words[max(0, len(words)-12):]
			t.Logf("recorded: %s: %d normalized truth words: %s ... %s", src.Name, len(words), strings.Join(head, " "), strings.Join(tail, " "))
			if tru.Timed {
				t.Logf("recorded: %s: the excerpt's truth by time: %d normalized words", src.Name, len(textscore.NormalizeWords(tru.Excerpt)))
			}
			if tru.CrossCheck != nil {
				t.Logf("recorded: %s: cross-check: %d words, %s", src.Name, tru.CrossCheck.Words, tru.CrossCheck.Method)
			}
		})
	}
}

// corpusRoot returns the corpus root DOCS names, ~/docs when it is unset.
func corpusRoot(t *testing.T) string {
	t.Helper()
	if docs := os.Getenv(docsEnv); docs != "" {
		return docs
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "docs")
}
