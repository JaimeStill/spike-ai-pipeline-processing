//go:build linux

package audio_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
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

// checkSettled joins every prefix of transcripts, the first k, and checks Join's settled prefix
// against the join of them all: the words before the prefix's latest seam, and its seams, are the
// whole join's.
func checkSettled(t *testing.T, transcripts []string, req audio.JoinRequest) {
	t.Helper()
	full, err := audio.Join(transcripts, req)
	if err != nil {
		t.Fatal(err)
	}
	for k := 1; k <= len(transcripts); k++ {
		part, err := audio.Join(transcripts[:k], req)
		if err != nil {
			t.Fatal(err)
		}
		settled := 0
		if k > 1 {
			settled = part.Seams[k-2].Position
		}
		if !slices.Equal(part.Words[:settled], full.Words[:settled]) {
			t.Fatalf("the first %d of %q: settled words %q, the whole join's %q",
				k, transcripts, part.Words[:settled], full.Words[:settled])
		}
		if !slices.Equal(part.Seams, full.Seams[:k-1]) {
			t.Fatalf("the first %d of %q: seams %+v, the whole join's %+v", k, transcripts, part.Seams, full.Seams[:k-1])
		}
	}
}

// TestJoinSettledPrefix joins random window transcripts, from a fixed seed, as they would arrive,
// each prefix of them in turn: the words before the latest seam never change as more arrive. The
// transcripts are drawn from a small vocabulary, so words repeat across seams, and each window's
// edges are perturbed as a cut word is heard differently, some windows empty as a failed one is,
// so seams match, splice short, and fail to match.
func TestJoinSettledPrefix(t *testing.T) {
	vocabulary := strings.Fields("the a of to he she was in it his her and money state mind future, Future. — would")
	rng := rand.New(rand.NewPCG(8, 9))
	for range 500 {
		var words []string
		for range 20 + rng.IntN(200) {
			words = append(words, vocabulary[rng.IntN(len(vocabulary))])
		}
		var transcripts []string
		for start := 0; start < len(words); {
			end := min(len(words), start+3+rng.IntN(30))
			w := slices.Clone(words[start:end])
			if rng.IntN(4) == 0 && len(w) > 0 {
				w[0] = vocabulary[rng.IntN(len(vocabulary))]
			}
			if rng.IntN(4) == 0 && len(w) > 0 {
				w = w[:len(w)-1]
			}
			if rng.IntN(10) == 0 {
				w = nil
			}
			transcripts = append(transcripts, strings.Join(w, " "))
			start = max(start+1, end-rng.IntN(9))
		}
		checkSettled(t, transcripts, audio.JoinRequest{SeamWords: []int{0, 2, 5, 40}[rng.IntN(4)]})
	}
}

// TestJoinSettledPrefixReal joins the levels fixture's transcript cut into windows as a plan of it
// would be, each window's last few words heard again at the next one's start, one cut word heard
// differently and one window failed: the words before the latest seam never change as more
// arrive.
func TestJoinSettledPrefixReal(t *testing.T) {
	var f levelsFixture
	readTruth(t, levelsTruth, &f)
	words := strings.Fields(strings.ToLower(strings.Join(f.Transcript, " ")))
	var transcripts []string
	for start := 0; start < len(words); start += 40 {
		w := slices.Clone(words[start:min(len(words), start+45)])
		transcripts = append(transcripts, strings.Join(w, " "))
	}
	transcripts[1] = "ing " + transcripts[1] + "."
	transcripts[3] = ""
	checkSettled(t, transcripts, audio.JoinRequest{})
}
