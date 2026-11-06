package textscore

import (
	"math"
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
// same alignment.
//
// It never holds the whole table, len(truth)+1 by len(got)+1, which for two transcripts of an
// audiobook's 40,000 words each would be 12.8 GB of ints. It holds every b-th row of it, b about
// sqrt(len(truth)), from one pass over the rows, and traces back one block of b rows at a time,
// each block's rows recomputed from the row held before it: the same rows, so the same traceback
// as the whole table's, in about 2*sqrt(len(truth)) rows of memory and twice the work of one
// pass. Those two 40,000-word transcripts take about 64 MB. Each entry is an int32, so the
// lengths together must be under 2^31.
func Align[T comparable](truth, got []T) []Edit {
	n, m := len(truth), len(got)
	if int64(n)+int64(m) >= 1<<31 {
		panic("textscore: Align's sequences are too long for its int32 table")
	}
	b := max(1, int(math.Ceil(math.Sqrt(float64(n+1)))))
	// rows[k] is row k*b of the table, and row(prev, i, cur) computes row i from row i-1.
	row := func(prev []int32, i int, cur []int32) {
		cur[0] = int32(i)
		for j := 1; j <= m; j++ {
			cost := int32(1)
			if truth[i-1] == got[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j-1]+cost, prev[j]+1, cur[j-1]+1)
		}
	}
	rows := make([][]int32, n/b+1)
	rows[0] = make([]int32, m+1)
	for j := range rows[0] {
		rows[0][j] = int32(j)
	}
	prev, cur := slices.Clone(rows[0]), make([]int32, m+1)
	for i := 1; i <= n; i++ {
		row(prev, i, cur)
		if i%b == 0 {
			rows[i/b] = slices.Clone(cur)
		}
		prev, cur = cur, prev
	}
	// block[r] is row base+r of the table while the traceback is in the block from base.
	block := make([][]int32, b+1)
	for r := range block {
		block[r] = make([]int32, m+1)
	}
	var edits []Edit
	base := -1
	for i, j := n, m; i > 0 || j > 0; {
		if i > 0 && (base < 0 || i == base) {
			base = (i - 1) / b * b
			copy(block[0], rows[base/b])
			for r := 1; r <= i-base; r++ {
				row(block[r-1], base+r, block[r])
			}
		}
		d := func(i, j int) int32 { return block[i-base][j] }
		switch {
		case i > 0 && j > 0 && truth[i-1] == got[j-1] && d(i, j) == d(i-1, j-1):
			i, j = i-1, j-1
			edits = append(edits, Edit{OpEqual, i, j})
		case i > 0 && j > 0 && d(i, j) == d(i-1, j-1)+1:
			i, j = i-1, j-1
			edits = append(edits, Edit{OpSubstitute, i, j})
		case i > 0 && d(i, j) == d(i-1, j)+1:
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

// BestPrefix returns the prefix of truth nearest got: its length k, the longest of those with
// the fewest edits, and EditDistance(truth[:k], got). It finds where in a long truth a transcript
// of the truth's beginning, such as of a recording's first minutes, ends. Of equal prefixes the
// longest is taken, so a last word misheard is a substitution, not an insertion with the truth's
// word left out. It keeps one row of len(truth)+1.
func BestPrefix[T comparable](truth, got []T) (k, edits int) {
	// row[c] is the distance from got's first r elements to truth's first c, for each r in turn.
	row := make([]int, len(truth)+1)
	for c := range row {
		row[c] = c
	}
	for r := 1; r <= len(got); r++ {
		diag := row[0]
		row[0] = r
		for c := 1; c <= len(truth); c++ {
			cost := 1
			if got[r-1] == truth[c-1] {
				cost = 0
			}
			above := row[c]
			row[c] = min(above+1, row[c-1]+1, diag+cost)
			diag = above
		}
	}
	k = 0
	for c, e := range row {
		if e <= row[k] {
			k = c
		}
	}
	return k, row[k]
}
