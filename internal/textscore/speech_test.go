package textscore_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
)

func TestNormalizeWords(t *testing.T) {
	cases := []struct {
		name, s string
		want    []string
	}{
		{"case and punctuation", "He was in a FEVERED state, of mind.", []string{"he", "was", "in", "a", "fevered", "state", "of", "mind"}},
		{"apostrophes fold", "WIFE'S wife’s", []string{"wifes", "wifes"}},
		{"fillers drop", "Um, I— uh, mm-hmm. Uh-huh hmm MHM er", []string{"i"}},
		{"hyphens split", "twenty-one well–known", []string{"twenty", "one", "well", "known"}},
		{"a hyphenated word that isn't a filler keeps its parts", "uh-uh, ah", []string{"uh", "uh", "ah"}},
		{"numbers stay as written", "21 twenty 1,000 3.5", []string{"21", "twenty", "1000", "35"}},
		{"punctuation alone is nothing", "— ... ,", nil},
		{"nothing", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := textscore.NormalizeWords(tc.s); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NormalizeWords(%q) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

func TestNormalizedWordErrors(t *testing.T) {
	cases := []struct {
		name, truth, got string
		edits, length    int
	}{
		{"case, punctuation, and fillers don't count", "HE WAS IN A FEVERED STATE", "He was, um, in a fevered state.", 0, 6},
		{"apostrophes don't count", "HIS WIFE'S ACTION", "his wife’s action", 0, 3},
		{"a hyphen splits on both sides", "TWENTY-ONE", "twenty one", 0, 2},
		{"numbers aren't normalized", "TWENTY ONE", "21", 2, 2},
		{"a word wrong", "THE CHIEF POINTS", "the cheap points", 1, 3},
		{"fillers in the truth don't count", "UM HE WAS", "he was", 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edits, length := textscore.NormalizedWordErrors(tc.truth, tc.got)
			if edits != tc.edits || length != tc.length {
				t.Errorf("NormalizedWordErrors = %d edits over %d, want %d over %d", edits, length, tc.edits, tc.length)
			}
		})
	}
}

func TestAlign(t *testing.T) {
	type e = textscore.Edit
	const (
		eq  = textscore.OpEqual
		sub = textscore.OpSubstitute
		del = textscore.OpDelete
		ins = textscore.OpInsert
	)
	cases := []struct {
		truth, got string
		want       []textscore.Edit
	}{
		{"a b c d", "a x c d e", []e{{eq, 0, 0}, {sub, 1, 1}, {eq, 2, 2}, {eq, 3, 3}, {ins, 4, 4}}},
		{"a b c", "a c", []e{{eq, 0, 0}, {del, 1, 1}, {eq, 2, 1}}},
		{"", "x", []e{{ins, 0, 0}}},
		{"x", "", []e{{del, 0, 0}}},
		{"", "", nil},
	}
	for _, tc := range cases {
		if got := textscore.Align(strings.Fields(tc.truth), strings.Fields(tc.got)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Align(%q, %q) = %v, want %v", tc.truth, tc.got, got, tc.want)
		}
	}
}

// TestAlignEditDistance aligns random sequences: an alignment's edits are EditDistance's count,
// and its steps walk both sequences in order, pairing equal elements only as OpEqual.
func TestAlignEditDistance(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 9))
	seq := func() []byte {
		s := make([]byte, rng.IntN(12))
		for i := range s {
			s[i] = 'a' + byte(rng.IntN(3))
		}
		return s
	}
	for range 500 {
		truth, got := seq(), seq()
		edits := textscore.Align(truth, got)
		n, i, j := 0, 0, 0
		for _, e := range edits {
			if e.Truth != i || e.Got != j {
				t.Fatalf("Align(%q, %q): step %v out of order at %d, %d", truth, got, e, i, j)
			}
			switch e.Op {
			case textscore.OpEqual, textscore.OpSubstitute:
				if (truth[i] == got[j]) != (e.Op == textscore.OpEqual) {
					t.Fatalf("Align(%q, %q): step %v pairs %q with %q", truth, got, e, truth[i], got[j])
				}
				i, j = i+1, j+1
			case textscore.OpDelete:
				i++
			case textscore.OpInsert:
				j++
			}
			if e.Op != textscore.OpEqual {
				n++
			}
		}
		if i != len(truth) || j != len(got) {
			t.Fatalf("Align(%q, %q) ends at %d, %d", truth, got, i, j)
		}
		if want := textscore.EditDistance(truth, got); n != want {
			t.Fatalf("Align(%q, %q) has %d edits, want %d", truth, got, n, want)
		}
	}
}

func TestSeamErrors(t *testing.T) {
	// words are twenty truth words, w0 to w19.
	words := make([]string, 20)
	for i := range words {
		words[i] = fmt.Sprintf("w%d", i)
	}
	truth := strings.ToUpper(strings.Join(words, " "))
	// with returns the words edited as edit says.
	with := func(edit func([]string) []string) string {
		return strings.Join(edit(slices.Clone(words)), " ")
	}
	cases := []struct {
		name   string
		joined string
		seams  []int
		want   []int
	}{
		{
			name:   "exact, case and punctuation aside",
			joined: with(func(w []string) []string { w[9] += ","; w[10] = "W10"; return w }),
			seams:  []int{10},
			want:   []int{0},
		},
		{
			// A substitution at word 10 is within 5 words of seams from 6 to 15.
			name:   "a substitution",
			joined: with(func(w []string) []string { w[10] = "x"; return w }),
			seams:  []int{10, 3, 15, 16, 6, 5},
			want:   []int{1, 0, 1, 0, 1, 0},
		},
		{
			// The deletion is in the gap before joined word 10, w11, within 5 of seams 5 to 15.
			name:   "a deletion",
			joined: with(func(w []string) []string { return slices.Delete(w, 10, 11) }),
			seams:  []int{5, 15, 4, 16},
			want:   []int{1, 1, 0, 0},
		},
		{
			name:   "an insertion",
			joined: with(func(w []string) []string { return slices.Insert(w, 10, "x") }),
			seams:  []int{6, 15, 5, 16},
			want:   []int{1, 1, 0, 0},
		},
		{
			// The filler is a word of the joined transcript the seams count, though it isn't
			// scored: the substitution is joined word 11, within 5 of seams 7 to 16.
			name:   "positions count words normalizing drops",
			joined: "um " + with(func(w []string) []string { w[10] = "x"; return w }),
			seams:  []int{16, 6, 7, 17},
			want:   []int{1, 0, 1, 0},
		},
		{
			name:   "two edits near one seam",
			joined: with(func(w []string) []string { w[8], w[11] = "x", "y"; return w }),
			seams:  []int{10, 0},
			want:   []int{2, 0},
		},
		{
			name:   "no seams",
			joined: with(func(w []string) []string { w[10] = "x"; return w }),
			want:   []int{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := textscore.SeamErrors(truth, tc.joined, tc.seams); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SeamErrors = %v, want %v", got, tc.want)
			}
		})
	}
}
