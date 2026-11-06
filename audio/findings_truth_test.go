//go:build findings && linux

package audio_test

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/corpustest"
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
		Name:            "ami",
		Title:           "AMI ES2002a, Mix-Headset (21.2 min, 4 speakers, a meeting)",
		Audio:           "audio/long/ami-es2002a-mix-headset.wav",
		Truth:           corpustest.AMITruth,
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

// readAMI reads ES2002a's truth through corpustest.ReadAMI: the whole truth, and the excerpt's,
// every word whose middle is before excerptLength.
func readAMI(docs string) (truth, error) {
	text, excerpt, err := corpustest.ReadAMI(docs, excerptLength)
	return truth{Text: text, Excerpt: excerpt, Timed: true}, err
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
	docs := corpustest.Root(t)
	for _, src := range findingSources {
		t.Run(src.Name, func(t *testing.T) {
			if _, err := corpustest.Check(docs, append([]string{src.Audio}, src.Truth...)); err != nil {
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
