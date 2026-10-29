//go:build linux

package tool_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The scripts run through the stand-in interpreter, standin-sh, which answers its version
// command itself and otherwise execs /bin/sh, so the version check is the stand-in's and the
// script runs in a real shell.

// interpreterPattern captures the version the stand-in interpreter prints.
var interpreterPattern = regexp.MustCompile(`standin-sh version (\d+(?:\.\d+)*)`)

// interpreterSpec declares the stand-in interpreter, printing version, with minimum.
func interpreterSpec(version, minimum string) tool.Spec {
	return tool.Spec{
		Program:        "standin-sh",
		VersionArgs:    []string{"--version", version},
		VersionPattern: interpreterPattern,
		Minimum:        minimum,
	}
}

func TestNewScriptRunsThroughInterpreter(t *testing.T) {
	installStandin(t)
	scriptDir := t.TempDir()
	script := writeScript(t, scriptDir, "args.sh", `printf '%s\n' "$0" "$@"; pwd`)
	// A relative script names the same file whatever the invocation's Dir.
	t.Chdir(scriptDir)
	tl, err := tool.NewScript(t.Context(), interpreterSpec("5.2", "5"), "args.sh")
	if err != nil {
		t.Fatal(err)
	}
	if got := tl.Name(); got != "args.sh" {
		t.Errorf("Name() = %q, want args.sh", got)
	}
	if got := tl.Version().String(); got != "5.2" {
		t.Errorf("Version() = %q, want the interpreter's 5.2", got)
	}
	dir := t.TempDir()
	var stdout bytes.Buffer
	err = tl.Run(t.Context(), tool.Invocation{Args: []string{"a b", "--flag=1", ""}, Dir: dir, Stdout: &stdout})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{script, "a b", "--flag=1", "", dir}, "\n") + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want the script, then the arguments in order, then Dir: %q", got, want)
	}
}

func TestNewScriptMissing(t *testing.T) {
	installStandin(t)
	missing := filepath.Join(t.TempDir(), "missing.sh")
	for _, script := range []string{missing, t.TempDir()} {
		t.Run(filepath.Base(script), func(t *testing.T) {
			_, err := tool.NewScript(t.Context(), interpreterSpec("5.2", "5"), script)
			if err == nil {
				t.Fatal("NewScript succeeded, want an error")
			}
			if !strings.Contains(err.Error(), script) {
				t.Errorf("error %q doesn't name %s", err, script)
			}
		})
	}
}

func TestNewScriptChecksInterpreter(t *testing.T) {
	installStandin(t)
	script := writeScript(t, t.TempDir(), "ok.sh", "exit 0")
	t.Run("not found", func(t *testing.T) {
		spec := interpreterSpec("5.2", "5")
		spec.Program = "missing-sh"
		_, err := tool.NewScript(t.Context(), spec, script)
		if err == nil || !strings.Contains(err.Error(), "missing-sh") {
			t.Fatalf("NewScript error = %v, want one naming missing-sh", err)
		}
	})
	t.Run("bad spec before lookup", func(t *testing.T) {
		spec := interpreterSpec("5.2", "v5")
		spec.Program = "missing-sh"
		_, err := tool.NewScript(t.Context(), spec, script)
		if err == nil || !strings.Contains(err.Error(), `minimum: version "v5"`) {
			t.Fatalf("NewScript error = %v, want one naming the bad minimum", err)
		}
	})
	t.Run("too old", func(t *testing.T) {
		_, err := tool.NewScript(t.Context(), interpreterSpec("4.9", "5"), script)
		if !errors.Is(err, tool.ErrTooOld) {
			t.Fatalf("NewScript error = %v, want ErrTooOld", err)
		}
		assertNames(t, err, "standin-sh", "4.9", "5")
	})
}

func TestScriptExitError(t *testing.T) {
	installStandin(t)
	script := writeScript(t, t.TempDir(), "fail.sh", `echo "it broke" >&2; exit 3`)
	tl, err := tool.NewScript(t.Context(), interpreterSpec("5.2", "5"), script)
	if err != nil {
		t.Fatal(err)
	}
	err = tl.Run(t.Context(), tool.Invocation{})
	exitErr, ok := errors.AsType[*tool.ExitError](err)
	if !ok {
		t.Fatalf("Run error = %v, want a *tool.ExitError", err)
	}
	if exitErr.Program != "fail.sh" || exitErr.Code != 3 || exitErr.Stderr != "it broke\n" {
		t.Errorf("ExitError = {%q, %d, %q}, want {fail.sh, 3, \"it broke\\n\"}", exitErr.Program, exitErr.Code, exitErr.Stderr)
	}
}

func TestCancelScriptEndsChild(t *testing.T) {
	installStandin(t)
	// The background child ignores SIGTERM before it writes its pid, so only the group's SIGKILL
	// ends it.
	script := writeScript(t, t.TempDir(), "spawn.sh", `standin stubborn 1m "$1" >/dev/null 2>&1 &
wait`)
	tl, err := tool.NewScript(t.Context(), interpreterSpec("5.2", "5"), script)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	done := runAsync(ctx, tl, tool.Invocation{Args: []string{pidFile}})
	pid := readPid(t, pidFile)
	cancel()
	assertContextError(t, <-done, context.Canceled)
	assertGone(t, pid)
}

// writeScript writes a sh script named name into dir and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
