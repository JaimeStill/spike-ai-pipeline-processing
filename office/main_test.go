//go:build linux

package office_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as the stand-in office: the test binary, re-executed through a link
// named spike-ai-pipeline-office, acting on its arguments as office instead of running the tests.
const standinEnv = "OFFICE_TEST_STANDIN"

// The stand-in's settings, which it reads from the environment it inherits.
const (
	// versionEnv is the version the stand-in's --version prints.
	versionEnv = "OFFICE_TEST_STANDIN_VERSION"
	// logEnv names the file each run appends its working directory, arguments, and input to, one
	// JSON object per line.
	logEnv = "OFFICE_TEST_STANDIN_LOG"
	// exitEnv, when set, is the exit code of a run, which then writes nothing to stdout.
	exitEnv = "OFFICE_TEST_STANDIN_EXIT"
	// stderrEnv, when set, is what a run writes to stderr, each {path} in it the input's path as
	// the run was given it.
	stderrEnv = "OFFICE_TEST_STANDIN_STDERR"
	// stdoutEnv, when set, is what a run that exits zero writes to stdout in place of
	// standinDocument.
	stdoutEnv = "OFFICE_TEST_STANDIN_STDOUT"
	// mediaEnv, when set, names the files, comma-separated, a run writes to its --media
	// directory, each holding its own name.
	mediaEnv = "OFFICE_TEST_STANDIN_MEDIA"
	// sleepEnv, when set, makes a run sleep a minute after logging, for a test to cancel it.
	sleepEnv = "OFFICE_TEST_STANDIN_SLEEP"
)

// standinDocument is the stand-in's JSON model when stdoutEnv isn't set: an empty docx.
const standinDocument = `{"schema": "1", "format": "docx", "blocks": []}` + "\n"

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		if name := filepath.Base(os.Args[0]); name != "spike-ai-pipeline-office" {
			fmt.Fprintf(os.Stderr, "stand-in: no tool named %s\n", name)
			os.Exit(2)
		}
		os.Exit(standin(os.Args[1:]))
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

// standin is the stand-in office. With --version alone it prints the version alone, as office
// does. Otherwise it appends its working directory, arguments, and its input file's content to
// the log file, sleeps when sleepEnv is set, writes the media mediaEnv names into the directory
// after --media, writes stderrEnv's value to stderr, and exits with exitEnv's code when it is
// set, or else writes stdoutEnv's value, or standinDocument, to stdout and exits zero.
func standin(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(os.Getenv(versionEnv))
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "stand-in: no input")
		return 2
	}
	input, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := appendLog(os.Getenv(logEnv), args, string(input)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if os.Getenv(sleepEnv) != "" {
		time.Sleep(time.Minute)
	}
	if names := os.Getenv(mediaEnv); names != "" {
		dir := ""
		for i, a := range args {
			if a == "--media" && i+1 < len(args) {
				dir = args[i+1]
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		for name := range strings.SplitSeq(names, ",") {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
		}
	}
	fmt.Fprint(os.Stderr, strings.ReplaceAll(os.Getenv(stderrEnv), "{path}", args[0]))
	if code := os.Getenv(exitEnv); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return n
	}
	out, set := os.LookupEnv(stdoutEnv)
	if !set {
		out = standinDocument
	}
	if _, err := os.Stdout.WriteString(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// standinRun is one logged run of the stand-in: its working directory, its arguments, and its
// input file's content.
type standinRun struct {
	Dir   string   `json:"dir"`
	Args  []string `json:"args"`
	Input string   `json:"input"`
}

// appendLog appends the working directory, args, and input to file as one JSON object and a
// newline.
func appendLog(file string, args []string, input string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	line, err := json.Marshal(standinRun{Dir: dir, Args: args, Input: input})
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

// installStandin links the test binary as spike-ai-pipeline-office in a directory of its own,
// makes that directory the whole PATH, and has the stand-in report version. It returns a
// function that returns every run since, in order, the version command's left out.
func installStandin(t *testing.T, version string) func() []standinRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, "spike-ai-pipeline-office")); err != nil {
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

// standinConverter builds a Converter over the stand-in office at 0.1.0, built from OfficeSpec
// through the exec runner, and returns it with the stand-in's run log.
func standinConverter(t *testing.T) (*office.Converter, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "0.1.0")
	tl, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := office.NewConverter(office.Config{Office: tl})
	if err != nil {
		t.Fatal(err)
	}
	return c, runs
}

// entries returns the names in dir, in name order.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}
