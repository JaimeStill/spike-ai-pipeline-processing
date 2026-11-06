//go:build linux

package audio

import (
	"cmp"
	"fmt"
	"strings"
	"unicode"
)

// DefaultSeamWords is the seam word bound a JoinRequest's zero SeamWords asks for: 16, twice the
// words a DefaultOverlap of 2 s holds at a fast 4 words a second. A plan made with a longer
// Overlap wants about 8 words for each second of it.
const DefaultSeamWords = 16

// JoinRequest is how Join merges window transcripts.
type JoinRequest struct {
	// SeamWords bounds each seam's search: Join aligns at most the last SeamWords words of the
	// earlier window with at most the first SeamWords words of the later one, so the words a
	// seam's overlap can hold are searched and a phrase the speaker repeats further from the seam
	// is not. Zero means DefaultSeamWords; it is otherwise at least 2, the fewest words a seam
	// matches on.
	SeamWords int
}

// Seam is how Join merged one pair of neighbouring windows: Seams[i] is the seam between window i
// and window i+1.
type Seam struct {
	// Matched is whether the seam's alignment matched at least 2 words, so the windows were
	// spliced; an unmatched seam's windows are concatenated whole.
	Matched bool
	// Matches is how many words the chosen alignment matched; 0 when the seam is unmatched.
	Matches int
	// Overlap is how many words of each window the chosen alignment overlaps, the matched ones
	// among them; 0 when the seam is unmatched.
	Overlap int
	// Position is the index in the transcript's Words of the first word the later window gave, the
	// splice; the words before it are the earlier windows'.
	Position int
}

// Transcript is window transcripts joined: their words, in order, and how each seam was merged.
type Transcript struct {
	// Words are the joined words as the windows' transcripts gave them, case and punctuation kept.
	Words []string
	// Seams are the seams between the windows, one fewer than the windows.
	Seams []Seam
}

// Text returns the transcript's words, each separated by one space.
func (t Transcript) Text() string { return strings.Join(t.Words, " ") }

// Join merges window transcripts, one for each window of a plan in plan order, into one
// transcript, as req says. A transcript's words are its runs of non-whitespace; the joined
// transcript keeps each word as given, and its Text collapses the whitespace between them.
//
// Each seam is merged with the best-offset merge Hugging Face transformers uses to stitch its
// chunked speech recognition, _find_longest_common_sequence (transformers 5.20.2,
// models/whisper/tokenization_whisper.py, the form pipelines/automatic_speech_recognition.py's
// copy of it takes for token ids, with the splice at the overlap's middle). The earlier window's
// tail slides over the later window's head, one word at a time; each offset i, the i-th slide,
// aligns the words the two then overlap, and scores matches/i + i/10000, the epsilon favouring a
// longer perfect match. The best offset is the first, in sliding order, with the highest score
// among those matching more than 1 word, a later offset replacing it only when it scores strictly
// more. The splice keeps the earlier window's words before the middle of the aligned overlap and
// the later window's words from its middle on, as transformers does: the left is trusted for the
// left of the overlap and the right for the right of it. A seam whose every offset matches fewer
// than 2 words is concatenated, the earlier window's words and then the later one's, all of
// them, and reported unmatched. The next seam aligns what the later window kept, as transformers
// carries its right sequence on.
//
// Words match on their folded forms: lowercased, with every rune but a letter, digit, or mark
// dropped, so "Future," matches "future". A word that folds to nothing, a dash alone, matches no
// word. The search is bounded by req's SeamWords, so a phrase repeated beyond the overlap's reach
// is never the splice.
//
// A window whose transcription failed is given as the empty string, as is a silent one: it has
// no words, so its seams on both sides match nothing and are reported unmatched, and the words of
// the windows either side are concatenated around it. Join reads nothing but its arguments, so
// the same arguments give the same transcript. No transcripts give an empty transcript.
//
// Join's settled prefix: each seam is merged from the windows up to the later of its two alone, so
// re-running Join over the transcripts received so far, a stream's windows in plan order, leaves
// every word before the latest seam unchanged. For transcripts t1 to tn, Join of t1 to tk, k at
// least 2, has the same Seams as the first k-1 of Join of t1 to tn, and the same Words before its
// last seam's Position; only the words from there on, the last window's, may change as more
// arrive. A caller joining as the windows arrive may thus treat the words before the latest seam
// as final.
//
// A SeamWords below zero, or 1, is an error wrapping ErrRequest.
func Join(transcripts []string, req JoinRequest) (Transcript, error) {
	bound := cmp.Or(req.SeamWords, DefaultSeamWords)
	if bound < 2 {
		return Transcript{}, fmt.Errorf("audio: seam words %d isn't 0 or at least 2: %w", req.SeamWords, ErrRequest)
	}
	var t Transcript
	if len(transcripts) == 0 {
		return t, nil
	}
	left := strings.Fields(transcripts[0])
	for _, s := range transcripts[1:] {
		right := strings.Fields(s)
		tail := left[len(left)-min(len(left), bound):]
		head := right[:min(len(right), bound)]
		a := bestAlignment(fold(tail), fold(head))
		t.Words = append(t.Words, left[:len(left)-len(tail)+(a.leftStart+a.leftStop)/2]...)
		seam := Seam{Position: len(t.Words)}
		if a.matches > 1 {
			seam.Matched, seam.Matches, seam.Overlap = true, a.matches, a.leftStop-a.leftStart
		}
		t.Seams = append(t.Seams, seam)
		left = right[(a.rightStart+a.rightStop)/2:]
	}
	t.Words = append(t.Words, left...)
	return t, nil
}

// alignment is the chosen offset of a seam: the aligned words, left[leftStart:leftStop] with
// right[rightStart:rightStop], and how many of them match.
type alignment struct {
	leftStart, leftStop, rightStart, rightStop int
	matches                                    int
}

// bestAlignment returns transformers' best offset of right's head over left's tail, both folded:
// the loop of _find_longest_common_sequence in models/whisper/tokenization_whisper.py, offsets
// from 1 to len(left)+len(right)-1, each scored matches/i + i/10000 in float64 as numpy does, kept
// when it matches more than 1 word and scores strictly more than the best before it. With no such
// offset it returns transformers' initial alignment, empty at left's end and right's start, which
// splices left whole before right whole.
func bestAlignment(left, right []string) alignment {
	best := alignment{leftStart: len(left), leftStop: len(left)}
	score := 0.0
	for i := 1; i < len(left)+len(right); i++ {
		a := alignment{
			leftStart:  max(0, len(left)-i),
			leftStop:   min(len(left), len(left)+len(right)-i),
			rightStart: max(0, i-len(left)),
			rightStop:  min(len(right), i),
		}
		for k := range a.leftStop - a.leftStart {
			if w := left[a.leftStart+k]; w != "" && w == right[a.rightStart+k] {
				a.matches++
			}
		}
		if s := float64(a.matches)/float64(i) + float64(i)/10000.0; a.matches > 1 && s > score {
			best, score = a, s
		}
	}
	return best
}

// fold returns each word's folded form, lowercased with every rune but a letter, digit, or mark
// dropped.
func fold(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, w)
	}
	return out
}
