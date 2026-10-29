//go:build linux

package document_test

import (
	"errors"
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
