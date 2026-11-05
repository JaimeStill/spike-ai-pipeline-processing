package textscore_test

import (
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
)

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
		if got := textscore.EditDistance([]rune(tc.a), []rune(tc.b)); got != tc.want {
			t.Errorf("EditDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := textscore.EditDistance([]rune(tc.b), []rune(tc.a)); got != tc.want {
			t.Errorf("EditDistance(%q, %q) = %d, want %d, as the other way", tc.b, tc.a, got, tc.want)
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
			edits, length := textscore.CharErrors(tc.truth, tc.got)
			if edits != tc.edits || length != tc.length {
				t.Errorf("CharErrors = %d edits over %d, want %d over %d", edits, length, tc.edits, tc.length)
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
			edits, length := textscore.WordErrors(tc.truth, tc.got)
			if edits != tc.edits || length != tc.length {
				t.Errorf("WordErrors = %d edits over %d, want %d over %d", edits, length, tc.edits, tc.length)
			}
		})
	}
}

func TestRate(t *testing.T) {
	if got := textscore.Rate(3, 12); got != 0.25 {
		t.Errorf("Rate(3, 12) = %v, want 0.25", got)
	}
	if got := textscore.Rate(4, 0); got != 0 {
		t.Errorf("Rate(4, 0) = %v, want 0 for an empty truth", got)
	}
}
