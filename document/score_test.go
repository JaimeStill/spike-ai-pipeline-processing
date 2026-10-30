//go:build linux

package document_test

import (
	"strings"
	"testing"
)

// The bake-off's scores, which the check tests here and the bakeoff tier uses: a character error
// rate and a word error rate, each an edit distance over a ground truth's length, both texts
// whitespace-collapsed first so line breaks and spacing never count as errors.

// collapse returns s with every run of whitespace made one space, and none at either end.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// editDistance returns the Levenshtein distance from a to b: the fewest insertions, deletions,
// and substitutions of one element that turn a into b. It keeps one row of the table.
func editDistance[T comparable](a, b []T) int {
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(a); i++ {
		diag := row[0]
		row[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			above := row[j]
			row[j] = min(above+1, row[j-1]+1, diag+cost)
			diag = above
		}
	}
	return row[len(b)]
}

// charErrors returns the character edit distance from truth to got and truth's length in
// characters, both texts whitespace-collapsed and compared rune by rune. Their ratio is the
// character error rate; a caller summing them over pages gets the rate of the pages together.
func charErrors(truth, got string) (edits, length int) {
	t := []rune(collapse(truth))
	return editDistance(t, []rune(collapse(got))), len(t)
}

// wordErrors returns the word edit distance from truth to got and truth's length in words, words
// being runs of non-whitespace. Their ratio is the word error rate.
func wordErrors(truth, got string) (edits, length int) {
	t := strings.Fields(truth)
	return editDistance(t, strings.Fields(got)), len(t)
}

// rate returns edits over length, and 0 for a length of 0.
func rate(edits, length int) float64 {
	if length == 0 {
		return 0
	}
	return float64(edits) / float64(length)
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"café", "cafe", 1},
		{"ab", "ba", 2},
	}
	for _, tc := range cases {
		if got := editDistance([]rune(tc.a), []rune(tc.b)); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := editDistance([]rune(tc.b), []rune(tc.a)); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d, as the other way", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestCharErrors(t *testing.T) {
	cases := []struct {
		name, truth, got string
		edits, length    int
	}{
		{"exact", "The Harbor Survey", "The Harbor Survey", 0, 17},
		{"whitespace collapses", "Full name:  Margaret\n\nCity: Fairhaven\n", " Full name: Margaret City:\tFairhaven", 0, 35},
		{"one substitution", "Postal code: 04821", "Postal code: 04B21", 1, 18},
		{"case counts", "SECRET", "Secret", 5, 6},
		{"nothing transcribed", "12/9/2002", "", 9, 9},
		{"an insertion past the truth", "Page 1", "Page 1 of 2", 5, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edits, length := charErrors(tc.truth, tc.got)
			if edits != tc.edits || length != tc.length {
				t.Errorf("charErrors = %d edits over %d, want %d over %d", edits, length, tc.edits, tc.length)
			}
		})
	}
}

func TestWordErrors(t *testing.T) {
	cases := []struct {
		name, truth, got string
		edits, length    int
	}{
		{"exact across lines", "CAVEATS: NOFORN\nTERMS: NONE", "CAVEATS: NOFORN TERMS: NONE", 0, 4},
		{"one word wrong", "the shoal off the old ferry landing", "the shoal of the old ferry landing", 1, 7},
		{"a word dropped and one added", "a b c d", "a c d e", 2, 4},
		{"nothing transcribed", "Page 1 of 2", "", 4, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edits, length := wordErrors(tc.truth, tc.got)
			if edits != tc.edits || length != tc.length {
				t.Errorf("wordErrors = %d edits over %d, want %d over %d", edits, length, tc.edits, tc.length)
			}
		})
	}
}

func TestRate(t *testing.T) {
	if got := rate(3, 12); got != 0.25 {
		t.Errorf("rate(3, 12) = %v, want 0.25", got)
	}
	if got := rate(4, 0); got != 0 {
		t.Errorf("rate(4, 0) = %v, want 0 for an empty truth", got)
	}
}
