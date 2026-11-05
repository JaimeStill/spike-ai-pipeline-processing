package goldentest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

// RecordEnv, set to 1, makes Check write the run's sums to the golden file in place of comparing
// them with it.
const RecordEnv = "GOLDEN_RECORD"

// Sum returns the lowercase hex SHA-256 of b.
func Sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// pngSignature begins every PNG.
const pngSignature = "\x89PNG\r\n\x1a\n"

// CanonicalPNG returns png without its tIME chunk and the tEXt, zTXt, and iTXt chunks whose
// keyword begins "date:", which ImageMagick writes with the time of the run unless told not to,
// so two runs of the same conversion compare equal. Every other chunk is kept, in order, byte
// for byte. Content that isn't a well-formed PNG is returned as it is.
func CanonicalPNG(png []byte) []byte {
	if !bytes.HasPrefix(png, []byte(pngSignature)) {
		return png
	}
	out := []byte(pngSignature)
	for rest := png[len(pngSignature):]; len(rest) > 0; {
		if len(rest) < 12 {
			return png
		}
		n := int(binary.BigEndian.Uint32(rest))
		if n < 0 || len(rest) < 12+n {
			return png
		}
		chunk, kind, data := rest[:12+n], string(rest[4:8]), rest[8:8+n]
		rest = rest[12+n:]
		switch kind {
		case "tIME":
			continue
		case "tEXt", "zTXt", "iTXt":
			if bytes.HasPrefix(data, []byte("date:")) {
				continue
			}
		}
		out = append(out, chunk...)
	}
	return out
}

// Check compares got, a run's sums by key, with the golden file at path, a line "sum  key" per
// output, sorted by key, and fails t for each key whose sum differs, is missing, or is new. With
// RecordEnv set to 1 it writes got to path instead.
func Check(t *testing.T, path string, got map[string]string) {
	t.Helper()
	if os.Getenv(RecordEnv) == "1" {
		var b strings.Builder
		for _, key := range slices.Sorted(maps.Keys(got)) {
			fmt.Fprintf(&b, "%s  %s\n", got[key], key)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d sums in %s", len(got), path)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for line := range strings.Lines(string(data)) {
		sum, key, ok := strings.Cut(strings.TrimRight(line, "\n"), "  ")
		if !ok {
			t.Fatalf("%s: malformed line %q", path, line)
		}
		want[key] = sum
	}
	for _, key := range slices.Sorted(maps.Keys(want)) {
		switch g, ok := got[key]; {
		case !ok:
			t.Errorf("%s: no output", key)
		case g != want[key]:
			t.Errorf("%s: sum %s, golden %s", key, g, want[key])
		}
	}
	for _, key := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[key]; !ok {
			t.Errorf("%s: an output the golden file doesn't hold", key)
		}
	}
}
