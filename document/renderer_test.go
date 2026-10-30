//go:build linux

package document_test

import (
	"errors"
	"math"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// TestCheckRender checks CheckRender's refusals. It runs no tool and has no count, so a page of
// any size past 1 passes.
func TestCheckRender(t *testing.T) {
	cases := []struct {
		name      string
		page, dpi int
		want      error
	}{
		{"dpi zero", 1, 0, document.ErrDPI},
		{"dpi negative", 1, -150, document.ErrDPI},
		{"dpi zero before page zero", 0, 0, document.ErrDPI},
		{"page zero", 0, 150, document.ErrPageRange},
		{"page negative", -1, 150, document.ErrPageRange},
		{"first page", 1, 150, nil},
		{"a page no count bounds", 1 << 20, 72, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := document.CheckRender(tc.page, tc.dpi)
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("CheckRender(page %d, %d dpi) returned %v, want %v", tc.page, tc.dpi, err, tc.want)
			}
		})
	}
}

// TestCheckRegion checks CheckRegion's refusals: a fraction outside 0 to 1 or not a number, a
// top not above its bottom, a left not left of its right, and the zero Region.
func TestCheckRegion(t *testing.T) {
	cases := []struct {
		name string
		r    document.Region
		ok   bool
	}{
		{"whole page", document.Region{Top: 0, Bottom: 1, Left: 0, Right: 1}, true},
		{"footer band", document.Region{Top: 0.9, Bottom: 1, Left: 0, Right: 1}, true},
		{"a corner", document.Region{Top: 0.25, Bottom: 0.5, Left: 0.5, Right: 0.75}, true},
		{"zero", document.Region{}, false},
		{"top below zero", document.Region{Top: -0.1, Bottom: 1, Left: 0, Right: 1}, false},
		{"bottom past one", document.Region{Top: 0, Bottom: 1.01, Left: 0, Right: 1}, false},
		{"left below zero", document.Region{Top: 0, Bottom: 1, Left: -1, Right: 1}, false},
		{"right past one", document.Region{Top: 0, Bottom: 1, Left: 0, Right: 2}, false},
		{"top at bottom", document.Region{Top: 0.5, Bottom: 0.5, Left: 0, Right: 1}, false},
		{"top below bottom", document.Region{Top: 0.9, Bottom: 0.1, Left: 0, Right: 1}, false},
		{"left at right", document.Region{Top: 0, Bottom: 1, Left: 0.3, Right: 0.3}, false},
		{"left right of right", document.Region{Top: 0, Bottom: 1, Left: 0.8, Right: 0.2}, false},
		{"not a number", document.Region{Top: math.NaN(), Bottom: 1, Left: 0, Right: 1}, false},
		{"infinite", document.Region{Top: 0, Bottom: math.Inf(1), Left: 0, Right: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := document.CheckRegion(tc.r)
			if tc.ok && err != nil {
				t.Errorf("CheckRegion(%+v) returned %v, want nil", tc.r, err)
			}
			if !tc.ok && !errors.Is(err, document.ErrRegion) {
				t.Errorf("CheckRegion(%+v) returned %v, want document.ErrRegion", tc.r, err)
			}
		})
	}
}

// TestHasText checks the rule for a page having a text layer: its text not empty once whitespace
// is trimmed.
func TestHasText(t *testing.T) {
	for text, want := range map[string]bool{
		"":                        false,
		" \n\t \r\n":              false,
		"\f":                      false,
		"      The Harbor Survey": true,
		"\n\nx\n":                 true,
	} {
		if got := document.HasText(text); got != want {
			t.Errorf("HasText(%q) = %v, want %v", text, got, want)
		}
	}
}
