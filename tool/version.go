//go:build linux

package tool

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a tool's version: dotted numbers, such as 9.0.2. Two versions compare component by
// component as numbers, a missing component counting as zero, so 9.0 equals 9.0.0.
type Version struct {
	parts []int
}

// ParseVersion parses dotted numbers, such as "10.08.0". Anything else, a prefix such as "n" or a
// suffix such as "-34" among them, is an error: a Spec's pattern captures only the numbers.
func ParseVersion(s string) (Version, error) {
	if s == "" {
		return Version{}, fmt.Errorf("version %q: empty", s)
	}
	var parts []int
	for field := range strings.SplitSeq(s, ".") {
		if field == "" || strings.Trim(field, "0123456789") != "" {
			return Version{}, fmt.Errorf("version %q: not dotted numbers", s)
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: %w", s, err)
		}
		parts = append(parts, n)
	}
	return Version{parts: parts}, nil
}

// Compare returns -1 when v is older than w, 0 when they are equal, and +1 when v is newer.
func (v Version) Compare(w Version) int {
	for i := range max(len(v.parts), len(w.parts)) {
		a, b := v.component(i), w.component(i)
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

// component returns the i-th component, zero when v has fewer.
func (v Version) component(i int) int {
	if i < len(v.parts) {
		return v.parts[i]
	}
	return 0
}

// String returns the version's components, dotted and without leading zeros.
func (v Version) String() string {
	fields := make([]string, len(v.parts))
	for i, n := range v.parts {
		fields[i] = strconv.Itoa(n)
	}
	return strings.Join(fields, ".")
}
