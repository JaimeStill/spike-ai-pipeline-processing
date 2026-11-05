package textscore

import "strings"

// Collapse returns s with every run of whitespace made one space, and none at either end.
func Collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// EditDistance returns the Levenshtein distance from a to b: the fewest insertions, deletions,
// and substitutions of one element that turn a into b. It keeps one row of the table.
func EditDistance[T comparable](a, b []T) int {
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

// CharErrors returns the character edit distance from truth to got and truth's length in
// characters, both texts collapsed and compared rune by rune. Their ratio is the character error
// rate; a caller summing them over pages gets the rate of the pages together.
func CharErrors(truth, got string) (edits, length int) {
	t := []rune(Collapse(truth))
	return EditDistance(t, []rune(Collapse(got))), len(t)
}

// WordErrors returns the word edit distance from truth to got and truth's length in words, words
// being runs of non-whitespace. Their ratio is the word error rate.
func WordErrors(truth, got string) (edits, length int) {
	t := strings.Fields(truth)
	return EditDistance(t, strings.Fields(got)), len(t)
}

// Rate returns edits over length, and 0 for a length of 0.
func Rate(edits, length int) float64 {
	if length == 0 {
		return 0
	}
	return float64(edits) / float64(length)
}
