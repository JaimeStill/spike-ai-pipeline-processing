//go:build linux

package tool_test

import (
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		v, w string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"9.0", "9.0.0", 0},   // a missing component is zero
		{"10.08", "10.8", 0},  // numbers, so a leading zero is nothing
		{"1.2.4", "1.2.3", 1}, // newer in the last component
		{"2.0", "1.9.9", 1},
		{"1.10", "1.9", 1}, // numbers, not text
		{"9.0.1", "9", 1},
		{"1.2.2", "1.2.3", -1},
		{"1.9", "1.10", -1},
		{"9", "9.0.1", -1},
	}
	for _, tt := range tests {
		t.Run(tt.v+" vs "+tt.w, func(t *testing.T) {
			v, w := mustParse(t, tt.v), mustParse(t, tt.w)
			if got := v.Compare(w); got != tt.want {
				t.Errorf("%s.Compare(%s) = %d, want %d", tt.v, tt.w, got, tt.want)
			}
			if got := w.Compare(v); got != -tt.want {
				t.Errorf("%s.Compare(%s) = %d, want %d", tt.w, tt.v, got, -tt.want)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	for given, want := range map[string]string{"9.0.2": "9.0.2", "10.08.0": "10.8.0", "007": "7"} {
		if got := mustParse(t, given).String(); got != want {
			t.Errorf("ParseVersion(%q).String() = %q, want %q", given, got, want)
		}
	}
}

func TestParseVersionRefuses(t *testing.T) {
	for _, s := range []string{"", "n9.0.2", "7.1.2-34", "1..2", "1.", ".1", "+1", "1.-2", "99999999999999999999"} {
		if v, err := tool.ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) = %s, want an error", s, v)
		}
	}
}

func mustParse(t *testing.T, s string) tool.Version {
	t.Helper()
	v, err := tool.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}
