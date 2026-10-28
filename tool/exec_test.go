//go:build linux

package tool_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinPattern captures the version the stand-in prints as "standin version V".
var standinPattern = regexp.MustCompile(`standin version (\d+(?:\.\d+)*)`)

// standinSpec declares the stand-in tool, printing version, with minimum.
func standinSpec(program, version, minimum string) tool.Spec {
	return tool.Spec{
		Program:        program,
		VersionArgs:    []string{"print", "standin version " + version},
		VersionPattern: standinPattern,
		Minimum:        minimum,
	}
}

func TestNewExecFindsProgramOnPath(t *testing.T) {
	installStandin(t)
	tl, err := tool.NewExec(t.Context(), standinSpec("standin", "1.2.3", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.Name(); got != "standin" {
		t.Errorf("Name() = %q, want standin", got)
	}
	if got := tl.Version().String(); got != "1.2.3" {
		t.Errorf("Version() = %q, want 1.2.3", got)
	}
}

func TestNewExecTakesPathAsGiven(t *testing.T) {
	link := installStandin(t)
	t.Setenv("PATH", t.TempDir()) // the link is no longer on PATH
	tl, err := tool.NewExec(t.Context(), standinSpec(link, "1.2.3", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.Name(); got != "standin" {
		t.Errorf("Name() = %q, want standin", got)
	}
}

func TestNewExecNotFound(t *testing.T) {
	installStandin(t)
	missingPath := filepath.Join(t.TempDir(), "missing-tool")
	for _, program := range []string{"missing-tool", missingPath} {
		t.Run(program, func(t *testing.T) {
			_, err := tool.NewExec(t.Context(), standinSpec(program, "1.0", "1.0"))
			if err == nil {
				t.Fatal("NewExec succeeded, want a not-found error")
			}
			if !strings.Contains(err.Error(), program) {
				t.Errorf("error %q doesn't name %s", err, program)
			}
		})
	}
}

func TestNewExecVersionRule(t *testing.T) {
	installStandin(t)
	tests := []struct {
		version, minimum string
		ok               bool
	}{
		{"1.2.3", "1.2.3", true},    // equal
		{"1.2.4", "1.2.3", true},    // newer in the last component
		{"2.0", "1.9.9", true},      // newer in the first component
		{"1.10", "1.9", true},       // numbers, not text
		{"10.08.0", "10.8", true},   // leading zero, missing component as zero
		{"9", "9.0.0", true},        // missing components as zero
		{"1.2.2", "1.2.3", false},   // older in the last component
		{"1.9", "1.10", false},      // numbers, not text
		{"9", "9.0.1", false},       // a missing component is zero, older than 1
		{"7.1.2", "7.1.2.1", false}, // a missing component is zero
	}
	for _, tt := range tests {
		t.Run(tt.version+" vs "+tt.minimum, func(t *testing.T) {
			_, err := tool.NewExec(t.Context(), standinSpec("standin", tt.version, tt.minimum))
			if tt.ok {
				if err != nil {
					t.Fatalf("NewExec: %v", err)
				}
				return
			}
			if _, ok := errors.AsType[*tool.VersionError](err); !ok {
				t.Fatalf("NewExec error = %v, want a *tool.VersionError", err)
			}
			if !errors.Is(err, tool.ErrTooOld) {
				t.Errorf("error %q isn't ErrTooOld", err)
			}
			assertNames(t, err, "standin", tt.version, tt.minimum)
		})
	}
}

func TestNewExecUnmatchedVersion(t *testing.T) {
	installStandin(t)
	tests := map[string]struct{ output, found string }{
		"no match":         {"something else entirely", ""},
		"capture not dots": {"standin version 1..2", "1..2"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			spec := standinSpec("standin", "", "1.0")
			spec.VersionArgs = []string{"print", tt.output}
			spec.VersionPattern = regexp.MustCompile(`standin version ([\d.]+)`)
			_, err := tool.NewExec(t.Context(), spec)
			if !errors.Is(err, tool.ErrNoVersion) {
				t.Fatalf("NewExec error = %v, want ErrNoVersion", err)
			}
			assertNames(t, err, "standin", tt.found, "1.0")
		})
	}
}

func TestNewExecFailingVersionCommand(t *testing.T) {
	installStandin(t)
	spec := standinSpec("standin", "", "1.0")
	spec.VersionArgs = []string{"exit", "3", "no version here"}
	_, err := tool.NewExec(t.Context(), spec)
	if _, ok := errors.AsType[*tool.VersionError](err); !ok {
		t.Fatalf("NewExec error = %v, want a *tool.VersionError", err)
	}
	if exitErr, ok := errors.AsType[*tool.ExitError](err); !ok || exitErr.Code != 3 {
		t.Errorf("error %q doesn't carry exit code 3", err)
	}
	assertNames(t, err, "standin", "", "1.0")
	if !strings.Contains(err.Error(), "no version here") {
		t.Errorf("error %q doesn't carry the command's output", err)
	}
}

func TestNewExecRefusesBadSpec(t *testing.T) {
	installStandin(t)
	noGroup := standinSpec("standin", "1.0", "1.0")
	noGroup.VersionPattern = regexp.MustCompile(`standin version`)
	badMinimum := standinSpec("standin", "1.0", "v1")
	for name, spec := range map[string]tool.Spec{"no capture group": noGroup, "bad minimum": badMinimum} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.NewExec(t.Context(), spec); err == nil {
				t.Fatal("NewExec succeeded, want an error")
			}
		})
	}
}

func TestRunArgsDirStdinStdout(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	dir := t.TempDir()
	var stdout bytes.Buffer
	err := tl.Run(t.Context(), tool.Invocation{
		Args:   []string{"echo", "a b", "--flag=1", ""},
		Dir:    dir,
		Stdin:  strings.NewReader("from stdin"),
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got echo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decoding %q: %v", stdout.String(), err)
	}
	want := echo{Args: []string{"a b", "--flag=1", ""}, Dir: dir, Stdin: "from stdin"}
	if !slices.Equal(got.Args, want.Args) {
		t.Errorf("args = %q, want %q", got.Args, want.Args)
	}
	if got.Dir != want.Dir {
		t.Errorf("dir = %q, want %q", got.Dir, want.Dir)
	}
	if got.Stdin != want.Stdin {
		t.Errorf("stdin = %q, want %q", got.Stdin, want.Stdin)
	}
}

func TestRunExitError(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{"exit", "7", "it broke"}})
	exitErr, ok := errors.AsType[*tool.ExitError](err)
	if !ok {
		t.Fatalf("Run error = %v, want a *tool.ExitError", err)
	}
	if exitErr.Program != "standin" || exitErr.Code != 7 {
		t.Errorf("ExitError = {%q, %d}, want {standin, 7}", exitErr.Program, exitErr.Code)
	}
	if exitErr.Stderr != "it broke" {
		t.Errorf("Stderr = %q, want %q", exitErr.Stderr, "it broke")
	}
	if msg := err.Error(); !strings.Contains(msg, "standin") || !strings.Contains(msg, "7") {
		t.Errorf("error %q doesn't name the program and its exit code", msg)
	}
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("error %q doesn't unwrap to an *exec.ExitError", err)
	}
}

func TestRunStderrTail(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	for _, n := range []int{100, 4096, 4097, 50000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			err := tl.Run(t.Context(), tool.Invocation{Args: []string{"stderr", strconv.Itoa(n), "1"}})
			exitErr, ok := errors.AsType[*tool.ExitError](err)
			if !ok {
				t.Fatalf("Run error = %v, want a *tool.ExitError", err)
			}
			all := letters(n)
			want := string(all[max(0, n-4096):])
			if exitErr.Stderr != want {
				t.Errorf("Stderr is %d bytes, want the last %d of %d", len(exitErr.Stderr), len(want), n)
			}
		})
	}
}

func TestRunStderrWriter(t *testing.T) {
	installStandin(t)
	tl := newStandin(t)
	var stderr bytes.Buffer
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{"stderr", "50000", "1"}, Stderr: &stderr})
	exitErr, ok := errors.AsType[*tool.ExitError](err)
	if !ok {
		t.Fatalf("Run error = %v, want a *tool.ExitError", err)
	}
	if exitErr.Stderr != "" {
		t.Errorf("ExitError carries a %d-byte tail, want none", len(exitErr.Stderr))
	}
	if !bytes.Equal(stderr.Bytes(), letters(50000)) {
		t.Errorf("stderr writer got %d bytes, want all 50000", stderr.Len())
	}
}

// newStandin builds the stand-in tool through the exec runner.
func newStandin(t *testing.T) *tool.Tool {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), standinSpec("standin", "1.0", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// assertNames checks that err names the program, the version found (when one was), and the
// minimum.
func assertNames(t *testing.T, err error, program, found, minimum string) {
	t.Helper()
	msg := err.Error()
	for _, s := range []string{program, found, minimum} {
		if !strings.Contains(msg, s) {
			t.Errorf("error %q doesn't name %q", msg, s)
		}
	}
	if found == "" && !strings.Contains(msg, "version none") {
		t.Errorf("error %q doesn't say no version was found", msg)
	}
}
