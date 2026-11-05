package cli_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
)

// TestOutcome: exit codes 1 to 4 are each their own error and match no other's; any other code
// has none and matches nothing, nil among it.
func TestOutcome(t *testing.T) {
	all := []error{cli.ErrNotWritten, cli.ErrUsage, cli.ErrRefused, cli.ErrFailed}
	for i, want := range all {
		code := i + 1
		if got := cli.Outcome(code); got != want {
			t.Errorf("Outcome(%d) = %v, want %v", code, got, want)
		}
		for _, other := range all {
			if cli.Is(code, other) != (other == want) {
				t.Errorf("Is(%d, %v) = %t", code, other, other != want)
			}
		}
	}
	for _, code := range []int{0, 5, 9, -1} {
		if got := cli.Outcome(code); got != nil {
			t.Errorf("Outcome(%d) = %v, want nil", code, got)
		}
		if cli.Is(code, nil) || slices.ContainsFunc(all, func(e error) bool { return cli.Is(code, e) }) {
			t.Errorf("Is(%d, ...) matches", code)
		}
	}
}

// TestExitText: the text names the adapter, the code's outcome or the code outside 1 to 4, and
// the message when there is one.
func TestExitText(t *testing.T) {
	for _, c := range []struct {
		who     string
		code    int
		message string
		want    string
	}{
		{"office", 3, "encrypted", "office: refused: encrypted"},
		{"email: mail", 1, "", "email: mail: not written"},
		{"office", 9, "m", "office: exit code 9: m"},
		{"office", 0, "", "office: exit code 0"},
	} {
		if got := cli.ExitText(c.who, c.code, c.message); got != c.want {
			t.Errorf("ExitText(%q, %d, %q) = %q, want %q", c.who, c.code, c.message, got, c.want)
		}
	}
}

// TestReadStderr: a warning loses the name, its label, and the path; a failure's line its name,
// its label, and the path; a line not the program's stays as it is; blank lines are skipped. On
// failure the message is the first line that isn't a warning; on success every line is a
// warning.
func TestReadStderr(t *testing.T) {
	p := cli.Program{Name: "prog", Labels: []string{"media not written", "refused"}}
	const path = "/w/spool-1"
	stderr := "prog: warning: message 3: odd\n" +
		"\n" +
		"prog: warning: /w/spool-1: header skipped\r\n" +
		"prog: refused: /w/spool-1: encrypted\n" +
		"prog: media not written: denied\n" +
		"Usage: prog <input>\n"

	warnings, message := p.ReadStderr(stderr, path, false)
	if want := []string{"message 3: odd", "header skipped"}; !slices.Equal(warnings, want) {
		t.Errorf("failure's warnings %q, want %q", warnings, want)
	}
	if message != "encrypted" {
		t.Errorf("message %q, want %q", message, "encrypted")
	}

	warnings, message = p.ReadStderr(stderr, path, true)
	if want := []string{"message 3: odd", "header skipped", "encrypted", "denied", "Usage: prog <input>"}; !slices.Equal(warnings, want) {
		t.Errorf("success's warnings %q, want %q", warnings, want)
	}
	if message != "" {
		t.Errorf("success's message %q, want none", message)
	}

	if _, message := p.ReadStderr("other: refused: x\n", path, false); message != "other: refused: x" {
		t.Errorf("another program's line: message %q", message)
	}
	if warnings, message := p.ReadStderr("", path, false); warnings != nil || message != "" {
		t.Errorf("empty stderr: %q, %q", warnings, message)
	}
}

// TestLimitedBuffer: the buffer keeps the first Limit bytes, reports every write whole, and never
// fails.
func TestLimitedBuffer(t *testing.T) {
	b := &cli.LimitedBuffer{Limit: 5}
	for _, p := range []string{"abc", "defg", "hij"} {
		if n, err := b.Write([]byte(p)); n != len(p) || err != nil {
			t.Errorf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if b.String() != "abcde" {
		t.Errorf("kept %q, want %q", b.String(), "abcde")
	}
	var zero cli.LimitedBuffer
	_, _ = zero.Write([]byte("x"))
	if zero.String() != "" {
		t.Errorf("zero limit kept %q", zero.String())
	}
}

// TestSpool: Spool writes the input into dir under spool-* and the extension, and its remove
// removes it; an empty dir is refused, and a failed copy leaves nothing.
func TestSpool(t *testing.T) {
	dir := t.TempDir()
	path, remove, err := cli.Spool(dir, strings.NewReader("doc"), ".pdf")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "spool-") || filepath.Ext(path) != ".pdf" {
		t.Errorf("path %q", path)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "doc" {
		t.Errorf("content %q, %v", b, err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after remove: %v", err)
	}

	if _, _, err := cli.Spool("", strings.NewReader("doc"), ""); err == nil || err.Error() != "spool: no working directory" {
		t.Errorf("empty dir: %v", err)
	}
	broken := errors.New("broken")
	_, _, err = cli.Spool(dir, io.MultiReader(strings.NewReader("part"), iotest.ErrReader(broken)), "")
	if !errors.Is(err, broken) || !strings.HasPrefix(err.Error(), "spool: ") {
		t.Errorf("failed copy: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("failed copy left %v", entries)
	}
}
