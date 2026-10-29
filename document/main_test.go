//go:build linux

package document_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as the stand-in magick: the test binary, re-executed through a link
// named magick, acting on its arguments instead of running the tests.
const standinEnv = "DOCUMENT_TEST_STANDIN"

// The stand-in magick's settings, which it reads from the environment it inherits.
const (
	// versionEnv is the ImageMagick version -version prints, as "7.1.2-34".
	versionEnv = "DOCUMENT_TEST_MAGICK_VERSION"
	// logEnv names the file each run appends its arguments to, one JSON array per line.
	logEnv = "DOCUMENT_TEST_MAGICK_LOG"
	// failEnv, when set, makes a run write its value to stderr and exit 1 after logging.
	failEnv = "DOCUMENT_TEST_MAGICK_FAIL"
)

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		os.Exit(magick(os.Args[1:]))
	}
	// The stand-in inherits the environment. Under -race, GORACE's atexit_sleep_ms keeps it from
	// sleeping a second as it exits.
	for key, value := range map[string]string{standinEnv: "1", "GORACE": "atexit_sleep_ms=0"} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// magick is the stand-in magick. With -version alone it prints a version line in magick's own
// form. Otherwise it appends its arguments to the log file, then fails when failEnv is set, or
// else copies stdin to stdout, so a corrected image is the image it was given.
func magick(args []string) int {
	if len(args) == 1 && args[0] == "-version" {
		fmt.Printf("Version: ImageMagick %s Q16-HDRI x86_64 https://imagemagick.org\n", os.Getenv(versionEnv))
		return 0
	}
	if err := appendLog(os.Getenv(logEnv), args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if msg := os.Getenv(failEnv); msg != "" {
		fmt.Fprint(os.Stderr, msg)
		return 1
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// appendLog appends args to file as one JSON array and a newline.
func appendLog(file string, args []string) error {
	line, err := json.Marshal(args)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// standinMagick installs the stand-in magick at version, as installMagick does, builds the Tool
// from spec through the exec runner, and returns it with the stand-in's run log.
func standinMagick(t *testing.T, version string, spec tool.Spec) (*tool.Tool, func() [][]string) {
	t.Helper()
	runs := installMagick(t, version)
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return tl, runs
}

// installMagick links the stand-in magick as magick in a directory of its own, makes that
// directory the whole PATH, and has the stand-in report version. It returns a function that
// returns the arguments of every run since, in order, the version command's left out.
func installMagick(t *testing.T, version string) func() [][]string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, "magick")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(versionEnv, version)
	log := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv(logEnv, log)
	return func() [][]string {
		t.Helper()
		f, err := os.Open(log)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		var runs [][]string
		lines := bufio.NewScanner(f)
		for lines.Scan() {
			var args []string
			if err := json.Unmarshal(lines.Bytes(), &args); err != nil {
				t.Fatal(err)
			}
			runs = append(runs, args)
		}
		if err := lines.Err(); err != nil {
			t.Fatal(err)
		}
		return runs
	}
}

// standinCorrector builds the corrections stage over the stand-in magick at 7.1.2-34, and
// returns it with the stand-in's run log.
func standinCorrector(t *testing.T) (*document.Corrector, func() [][]string) {
	t.Helper()
	tl, runs := standinMagick(t, "7.1.2-34", document.MagickSpec())
	c, err := document.NewCorrector(tl)
	if err != nil {
		t.Fatal(err)
	}
	return c, runs
}
