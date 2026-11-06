//go:build linux

package audio_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// matched is a matched seam's report.
func matched(matches, overlap, position int) audio.Seam {
	return audio.Seam{Matched: true, Matches: matches, Overlap: overlap, Position: position}
}

// unmatched is an unmatched seam's report, the later window's words from position on.
func unmatched(position int) audio.Seam { return audio.Seam{Position: position} }

// repeated is a nine-word phrase of the fixture's, its words distinct, which the repeated-phrase
// cases say twice and more.
const repeated = "he would have to pay her the money which"

func TestJoin(t *testing.T) {
	tests := []struct {
		name        string
		transcripts []string
		seamWords   int
		text        string
		seams       []audio.Seam
	}{
		{
			name:        "exact overlap",
			transcripts: []string{"he was in a fevered state of mind", "state of mind owing to the blight"},
			// The three words overlap at the third offset; the splice keeps "state" of the left and
			// "of mind" of the right.
			text:  "he was in a fevered state of mind owing to the blight",
			seams: []audio.Seam{matched(3, 3, 6)},
		},
		{
			name: "partial overlap with differences at the edges",
			transcripts: []string{
				"hurstwood walked the floor mentally arranging the chief point",
				"ranging the chief points of his situation",
			},
			// Each window heard a word cut at its edge differently: 2 of the 4 overlapped words
			// match, and the splice at their middle drops both cut forms.
			text:  "hurstwood walked the floor mentally arranging the chief points of his situation",
			seams: []audio.Seam{matched(2, 4, 7)},
		},
		{
			name:        "punctuation and case differ",
			transcripts: []string{"He also thought of his managerial position.", "His Managerial Position, Hurstwood walked"},
			text:        "He also thought of his Managerial Position, Hurstwood walked",
			seams:       []audio.Seam{matched(3, 3, 5)},
		},
		{
			name:        "no overlap is concatenated",
			transcripts: []string{"it did not matter", "what he did"},
			text:        "it did not matter what he did",
			seams:       []audio.Seam{unmatched(4)},
		},
		{
			name:        "one matching word is concatenated",
			transcripts: []string{"the money which", "which she would"},
			text:        "the money which which she would",
			seams:       []audio.Seam{unmatched(3)},
		},
		{
			name:        "dashes match nothing",
			transcripts: []string{"a b — --", "— -- c d"},
			text:        "a b — -- — -- c d",
			seams:       []audio.Seam{unmatched(4)},
		},
		{
			name:        "single window has no seams",
			transcripts: []string{"  He was  in\na state "},
			text:        "He was in a state",
		},
		{
			name: "no windows",
			text: "",
		},
		{
			name:        "empty windows",
			transcripts: []string{"", " "},
			text:        "",
			seams:       []audio.Seam{unmatched(0)},
		},
		{
			name:        "an empty window between two is unmatched on both sides",
			transcripts: []string{"a b c", "", "c d e"},
			// The windows either side don't share a seam, so their words are concatenated, the
			// "c" each heard kept twice.
			text:  "a b c c d e",
			seams: []audio.Seam{unmatched(3), unmatched(3)},
		},
		{
			name: "three windows chain on what each kept",
			transcripts: []string{
				"hurstwood walked the floor",
				"the floor mentally arranging the chief",
				"the chief points of his situation",
			},
			text:  "hurstwood walked the floor mentally arranging the chief points of his situation",
			seams: []audio.Seam{matched(2, 2, 3), matched(2, 2, 7)},
		},
		{
			name: "a phrase repeated beyond the bound isn't the splice",
			transcripts: []string{
				repeated + " " + repeated,
				repeated + " " + repeated + " or there",
			},
			// The default bound of 16 can't align the two windows' whole 18-word repeats, so the
			// splice is the nine-word overlap and the left's second repeat and the right's two are
			// all kept.
			text:  strings.Repeat(repeated+" ", 3) + "or there",
			seams: []audio.Seam{matched(9, 9, 13)},
		},
		{
			name: "a bound wide enough latches onto the repeat",
			transcripts: []string{
				repeated + " " + repeated,
				repeated + " " + repeated + " or there",
			},
			seamWords: 40,
			// Unbounded, transformers' score prefers the 18-word perfect match, a repeat dropped.
			text:  strings.Repeat(repeated+" ", 2) + "or there",
			seams: []audio.Seam{matched(18, 18, 9)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := audio.Join(tc.transcripts, audio.JoinRequest{SeamWords: tc.seamWords})
			if err != nil {
				t.Fatalf("Join: %v", err)
			}
			if got.Text() != tc.text {
				t.Errorf("text\n%q, want\n%q", got.Text(), tc.text)
			}
			if !reflect.DeepEqual(got.Seams, tc.seams) {
				t.Errorf("seams %+v, want %+v", got.Seams, tc.seams)
			}
		})
	}
}

func TestJoinRefusesBound(t *testing.T) {
	for _, n := range []int{-1, 1} {
		if _, err := audio.Join([]string{"a", "b"}, audio.JoinRequest{SeamWords: n}); !errors.Is(err, audio.ErrRequest) {
			t.Errorf("SeamWords %d: err %v, want ErrRequest", n, err)
		}
	}
}

func TestJoinDeterministic(t *testing.T) {
	transcripts := []string{
		"He was in a fevered state of mind, owing to the blight",
		"to the blight his wife's action threatened to cast",
		"to cast upon his entire future.",
	}
	first, err := audio.Join(transcripts, audio.JoinRequest{})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	for range 5 {
		again, err := audio.Join(transcripts, audio.JoinRequest{})
		if err != nil {
			t.Fatalf("Join: %v", err)
		}
		if !reflect.DeepEqual(again, first) {
			t.Fatalf("Join gave %+v, then %+v", first, again)
		}
	}
}

// TestJoinReconstructs cuts random runs of distinct words into windows that overlap by 2 to 8
// words, each keeping words of its own, and joins them back: the run comes back whole, every seam
// matched on its overlap, the splice where the later window's kept words begin.
func TestJoinReconstructs(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 2))
	for trial := range 200 {
		var words, transcripts []string
		var overlaps, positions []int
		end := 0
		windows := 1 + rng.IntN(5)
		for w := range windows {
			start := 0
			if w > 0 {
				k := 2 + rng.IntN(7)
				start = end - k
				overlaps = append(overlaps, k)
				positions = append(positions, start+k/2)
			}
			end += 9 + rng.IntN(10)
			for len(words) < end {
				words = append(words, fmt.Sprintf("w%d", len(words)))
			}
			transcripts = append(transcripts, strings.Join(words[start:end], " "))
		}
		got, err := audio.Join(transcripts, audio.JoinRequest{})
		if err != nil {
			t.Fatalf("trial %d: Join: %v", trial, err)
		}
		if got.Text() != strings.Join(words, " ") {
			t.Fatalf("trial %d: joined %q, want %q, from %q", trial, got.Text(), strings.Join(words, " "), transcripts)
		}
		if len(got.Seams) != windows-1 {
			t.Fatalf("trial %d: %d seams, want %d", trial, len(got.Seams), windows-1)
		}
		for i, s := range got.Seams {
			if want := matched(overlaps[i], overlaps[i], positions[i]); s != want {
				t.Errorf("trial %d: seam %d is %+v, want %+v", trial, i, s, want)
			}
		}
	}
}
