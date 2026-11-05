package textscore

import (
	"slices"
	"strings"
	"unicode"
)

// Fillers are the hesitations and backchannels NormalizeWords drops, a speaker's sounds a
// transcript may or may not write: um, umm, uh, uhm, er, erm, hm, hmm, mm, mmm, mhm, mm-hmm, and
// uh-huh. A word that means something, "ah", "oh", "eh", or "uh-uh", is kept.
var Fillers = []string{"um", "umm", "uh", "uhm", "er", "erm", "hm", "hmm", "mm", "mmm", "mhm", "mm-hmm", "uh-huh"}

// NormalizeWords returns a speech transcript's words as the word error rate of speech compares
// them, decided in-house: each run of non-whitespace lowercased, a filler dropped, then split at
// its hyphens and dashes, each part with every rune but a letter, digit, or mark dropped, and an
// empty part dropped. A filler is matched with its punctuation but hyphens dropped, so "Um," and
// "mm-hmm." are fillers. Numbers are not normalized: "twenty" and "20" are different words.
func NormalizeWords(s string) []string {
	words, _ := normalize(s)
	return words
}

// normalize returns NormalizeWords of s, and for each word the index of the run of non-whitespace
// in s it came from.
func normalize(s string) (words []string, origin []int) {
	for i, field := range strings.Fields(s) {
		lower := strings.ToLower(field)
		if slices.Contains(Fillers, strings.Map(keepHyphen, lower)) {
			continue
		}
		for part := range strings.FieldsFuncSeq(lower, isDash) {
			if w := strings.Map(keepWord, part); w != "" {
				words, origin = append(words, w), append(origin, i)
			}
		}
	}
	return words, origin
}

// isDash reports whether r is a hyphen or a dash, Unicode's dash punctuation.
func isDash(r rune) bool { return unicode.Is(unicode.Pd, r) }

// keepWord keeps a letter, digit, or mark and drops any other rune.
func keepWord(r rune) rune {
	if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
		return r
	}
	return -1
}

// keepHyphen keeps what keepWord keeps and a hyphen or dash, as a hyphen.
func keepHyphen(r rune) rune {
	if isDash(r) {
		return '-'
	}
	return keepWord(r)
}

// NormalizedWordErrors returns WordErrors of truth and got after NormalizeWords of each: the word
// edit distance from truth to got, and truth's length in normalized words. Their ratio is the
// word error rate of speech.
func NormalizedWordErrors(truth, got string) (edits, length int) {
	t := NormalizeWords(truth)
	return EditDistance(t, NormalizeWords(got)), len(t)
}

// SeamRadius is how many words on each side of a seam SeamErrors counts edits in: 5.
const SeamRadius = 5

// SeamErrors returns the edits near each seam of a joined transcript, in one alignment of its
// normalized words against truth's: seams[i] is a seam's position, the index among joined's runs
// of non-whitespace of the first word after it, as audio.Seam's Position is, and the i-th count is
// the edits within SeamRadius words of it. A substitution or insertion counts when the joined
// word it is of came from one of the SeamRadius words before the seam or the SeamRadius words
// from it on; a deletion, a truth word joined lacks, counts when the gap it is in, before the
// joined word that follows it, is from SeamRadius words before the seam to SeamRadius after it.
// Seams nearer each other than twice SeamRadius both count an edit between them.
func SeamErrors(truth, joined string, seams []int) []int {
	got, origin := normalize(joined)
	fields := len(strings.Fields(joined))
	counts := make([]int, len(seams))
	for _, e := range Align(NormalizeWords(truth), got) {
		var lo, hi int
		switch e.Op {
		case OpEqual:
			continue
		case OpDelete:
			gap := fields
			if e.Got < len(origin) {
				gap = origin[e.Got]
			}
			lo, hi = gap-SeamRadius, gap+SeamRadius
		default:
			lo, hi = origin[e.Got]-SeamRadius+1, origin[e.Got]+SeamRadius
		}
		for i, p := range seams {
			if p >= lo && p <= hi {
				counts[i]++
			}
		}
	}
	return counts
}

// Op is what an alignment does with a truth element and a transcription's.
type Op int

const (
	// OpEqual pairs a truth element with an equal transcribed one.
	OpEqual Op = iota + 1
	// OpSubstitute pairs a truth element with a different transcribed one.
	OpSubstitute
	// OpDelete is a truth element the transcription lacks.
	OpDelete
	// OpInsert is a transcribed element the truth lacks.
	OpInsert
)

// Edit is one step of an alignment: its Op, at Truth in the truth and Got in the transcription.
// A deletion's Got is the gap it is in, the index of the transcribed element after it, and an
// insertion's Truth likewise the truth's.
type Edit struct {
	Op    Op
	Truth int
	Got   int
}

// Align returns a least-edit alignment of got against truth, in order: as many substitutions,
// deletions, and insertions as EditDistance(truth, got), and an equal pairing for every other
// element. Of the alignments with the fewest edits it takes the one a traceback from the ends
// preferring a pairing, then a deletion, then an insertion gives, so the same arguments give the
// same alignment. It keeps the whole table, len(truth)+1 by len(got)+1.
func Align[T comparable](truth, got []T) []Edit {
	n, m := len(truth), len(got)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if truth[i-1] == got[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j-1]+cost, d[i-1][j]+1, d[i][j-1]+1)
		}
	}
	var edits []Edit
	for i, j := n, m; i > 0 || j > 0; {
		switch {
		case i > 0 && j > 0 && truth[i-1] == got[j-1] && d[i][j] == d[i-1][j-1]:
			i, j = i-1, j-1
			edits = append(edits, Edit{OpEqual, i, j})
		case i > 0 && j > 0 && d[i][j] == d[i-1][j-1]+1:
			i, j = i-1, j-1
			edits = append(edits, Edit{OpSubstitute, i, j})
		case i > 0 && d[i][j] == d[i-1][j]+1:
			i--
			edits = append(edits, Edit{OpDelete, i, j})
		default:
			j--
			edits = append(edits, Edit{OpInsert, i, j})
		}
	}
	slices.Reverse(edits)
	return edits
}
