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
	"slices"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as a stand-in tool: the test binary, re-executed through a link
// named magick or gs, acting on its arguments as that program instead of running the tests.
const standinEnv = "DOCUMENT_TEST_STANDIN"

// The stand-in's settings, which it reads from the environment it inherits.
const (
	// versionEnv is the version the stand-in's version command prints, in its program's form:
	// "7.1.2-34" for magick, "10.08.0" for gs.
	versionEnv = "DOCUMENT_TEST_STANDIN_VERSION"
	// logEnv names the file each run appends its working directory and arguments to, one JSON
	// object per line.
	logEnv = "DOCUMENT_TEST_STANDIN_LOG"
	// failEnv, when set, makes a run write its value to stderr and exit 1 after logging.
	failEnv = "DOCUMENT_TEST_STANDIN_FAIL"
	// stdoutEnv, when set, is what the stand-in writes to stdout for a run in place of its own:
	// gs's page count or its page, or magick's copy of its stdin.
	stdoutEnv = "DOCUMENT_TEST_STANDIN_STDOUT"
	// stderrEnv, when set, is what the stand-in gs writes to stderr for a run that succeeds.
	stderrEnv = "DOCUMENT_TEST_STANDIN_STDERR"
)

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		switch name := filepath.Base(os.Args[0]); name {
		case "magick":
			os.Exit(magick(os.Args[1:]))
		case "gs":
			os.Exit(gs(os.Args[1:]))
		default:
			fmt.Fprintf(os.Stderr, "stand-in: no tool named %s\n", name)
			os.Exit(2)
		}
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
// else writes stdoutEnv's value to stdout when it is set, and copies stdin to stdout when it
// isn't, so a corrected image is the image it was given.
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
	if out, set := os.LookupEnv(stdoutEnv); set {
		if _, err := os.Stdout.WriteString(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// standinText is the stand-in gs's text layer for a page: lines as txtwrite lays them out.
const standinText = "      The Harbor Survey\n      The harbor survey began in the spring.\n"

// standinPage is the stand-in gs's page: the PNG signature and a tail that names it.
var standinPage = []byte("\x89PNG\r\n\x1a\nstand-in gs page")

// gs is the stand-in gs. With --version alone it prints the version alone, as gs does.
// Otherwise it appends its working directory and arguments to the log file, then fails when
// failEnv is set, or else writes stderrEnv's value to stderr when it is set, and stdoutEnv's
// value to stdout when it is set, and its own output when it isn't: a page count of 6 for a run
// with -dNODISPLAY, a page's text for a run with the txtwrite device, and standinPage for any
// other.
func gs(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(os.Getenv(versionEnv))
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
	fmt.Fprint(os.Stderr, os.Getenv(stderrEnv))
	out, set := os.LookupEnv(stdoutEnv)
	switch {
	case set:
	case slices.Contains(args, "-dNODISPLAY"):
		out = "6\n"
	case slices.Contains(args, "-sDEVICE=txtwrite"):
		out = standinText
	default:
		out = string(standinPage)
	}
	if _, err := os.Stdout.WriteString(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// standinRun is one logged run of a stand-in: its working directory and arguments.
type standinRun struct {
	Dir  string   `json:"dir"`
	Args []string `json:"args"`
}

// appendLog appends the working directory and args to file as one JSON object and a newline.
func appendLog(file string, args []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	line, err := json.Marshal(standinRun{Dir: dir, Args: args})
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
	runs := installStandin(t, "magick", version)
	return func() [][]string {
		t.Helper()
		var args [][]string
		for _, r := range runs() {
			args = append(args, r.Args)
		}
		return args
	}
}

// installStandin links the test binary as program in a directory of its own, makes that
// directory the whole PATH, and has the stand-in report version. It returns a function that
// returns every run since, in order, the version command's left out.
func installStandin(t *testing.T, program, version string) func() []standinRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, program)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(versionEnv, version)
	log := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv(logEnv, log)
	return func() []standinRun {
		t.Helper()
		f, err := os.Open(log)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		var runs []standinRun
		lines := bufio.NewScanner(f)
		for lines.Scan() {
			var r standinRun
			if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			runs = append(runs, r)
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
