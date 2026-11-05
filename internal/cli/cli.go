package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// The outcomes of a CLI run that exits nonzero, each its exit code's.
var (
	// ErrNotWritten is exit code 1: the CLI couldn't write its output, its media, or an
	// attachment's content.
	ErrNotWritten = errors.New("not written")
	// ErrUsage is exit code 2: the CLI refused its arguments, a fault in the adapter.
	ErrUsage = errors.New("usage error")
	// ErrRefused is exit code 3: the CLI refused the input, giving its reason.
	ErrRefused = errors.New("refused")
	// ErrFailed is exit code 4: a fault of the CLI's own.
	ErrFailed = errors.New("failed")
)

// outcomes maps the exit codes to their errors.
var outcomes = map[int]error{
	1: ErrNotWritten,
	2: ErrUsage,
	3: ErrRefused,
	4: ErrFailed,
}

// Outcome returns the error of exit code code, ErrNotWritten, ErrUsage, ErrRefused, or
// ErrFailed; nil for a code outside 1 to 4.
func Outcome(code int) error { return outcomes[code] }

// Is reports whether target is the error of exit code code; false for a code outside 1 to 4.
func Is(code int, target error) bool {
	outcome := outcomes[code]
	return outcome != nil && outcome == target
}

// ExitText returns the text of an adapter's ExitError: who, the adapter and its CLI as the
// adapter names them, such as "office" or "email: mail"; then the code's outcome, or the code
// itself outside 1 to 4; then message, the CLI's, if any.
func ExitText(who string, code int, message string) string {
	msg := fmt.Sprintf("%s: exit code %d", who, code)
	if outcome := outcomes[code]; outcome != nil {
		msg = who + ": " + outcome.Error()
	}
	if message != "" {
		msg += ": " + message
	}
	return msg
}

// Program is a CLI as its stderr shows it.
type Program struct {
	// Name is the CLI's name, which begins each line it writes to stderr, followed by ": ".
	Name string
	// Labels are the labels a failure's line begins with after the name, each followed by ": ":
	// mail's one, "error", or office's one per outcome, such as "refused".
	Labels []string
}

// warningLabel begins each warning after a Program's name.
const warningLabel = "warning: "

// ReadStderr reads stderr, the standard error of p's run on path, the input as the CLI was given
// it: its warnings, in order, and the run's message, the first line that isn't blank or a
// warning. A warning is the line with p's name, the warning label, and path taken off: "<where>:
// <what>", or "<what>" for one path begins. Another line of p's is taken with its name, any of
// p.Labels, and path taken off; one that isn't p's, such as a usage text's, as it is. succeeded
// says the run exited zero, which writes only warnings, so any other line is taken as one too.
func (p Program) ReadStderr(stderr, path string, succeeded bool) (warnings []string, message string) {
	prefix := p.Name + ": "
	for line := range strings.Lines(stderr) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rest, own := strings.CutPrefix(line, prefix)
		if w, ok := strings.CutPrefix(rest, warningLabel); own && ok {
			warnings = append(warnings, strings.TrimPrefix(w, path+": "))
			continue
		}
		if own {
			for _, label := range p.Labels {
				if r, ok := strings.CutPrefix(rest, label+": "); ok {
					rest = r
					break
				}
			}
			rest = strings.TrimPrefix(rest, path+": ")
		}
		if succeeded {
			warnings = append(warnings, rest)
		} else if message == "" {
			message = rest
		}
	}
	return warnings, message
}

// LimitedBuffer is a writer that keeps the first Limit bytes written to it and discards the
// rest, for a tool's output that is read only in part, such as a CLI's stderr; its writes never
// fail.
type LimitedBuffer struct {
	strings.Builder
	// Limit is the most it keeps.
	Limit int
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	if room := b.Limit - b.Len(); room > 0 {
		b.Builder.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Spool copies r into a new file in dir, the working directory the caller owns, named spool-*
// and ext, so a tool can read it by path. It returns the file's path and a function that removes
// the file, which the caller runs when its call ends. An empty dir is refused rather than taken
// as the system's temporary directory, and a failed copy removes what it wrote. Its errors begin
// "spool: ", for the caller to prefix with its package.
func Spool(dir string, r io.Reader, ext string) (string, func() error, error) {
	if dir == "" {
		return "", nil, errors.New("spool: no working directory")
	}
	f, err := os.CreateTemp(dir, "spool-*"+ext)
	if err != nil {
		return "", nil, fmt.Errorf("spool: %w", err)
	}
	path := f.Name()
	remove := func() error { return os.Remove(path) }
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", nil, errors.Join(fmt.Errorf("spool: %w", err), remove())
	}
	return path, remove, nil
}
