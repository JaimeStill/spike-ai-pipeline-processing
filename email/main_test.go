//go:build linux

package email_test

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

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as a stand-in: the test binary, re-executed through a link named
// spike-ai-pipeline-mail, acting on its arguments as mail instead of running the tests, or
// through one named magick, acting as the magick the picture package's Converter is built over.
const standinEnv = "MAIL_TEST_STANDIN"

// The stand-in's settings, which it reads from the environment it inherits.
const (
	// versionEnv is the version the stand-in's --version prints.
	versionEnv = "MAIL_TEST_STANDIN_VERSION"
	// logEnv names the file each run appends its arguments to, one JSON array per line.
	logEnv = "MAIL_TEST_STANDIN_LOG"
	// stdoutEnv, when set, is what a run writes to stdout in place of standinLines.
	stdoutEnv = "MAIL_TEST_STANDIN_STDOUT"
	// stderrEnv, when set, is what a run writes to stderr, after its stdout, each {path} in it the
	// input's path, its last argument, as the run was given it.
	stderrEnv = "MAIL_TEST_STANDIN_STDERR"
	// exitEnv, when set, is the exit code of a run.
	exitEnv = "MAIL_TEST_STANDIN_EXIT"
	// sleepEnv, when set, makes a run sleep a minute after writing its stdout, for a test to
	// cancel it or stop reading.
	sleepEnv = "MAIL_TEST_STANDIN_SLEEP"
)

// standinLines is the stand-in's output when stdoutEnv isn't set: a .msg's store line and its
// message.
const standinLines = `{"line":"store","schema":"1","format":"msg"}
{"line":"message","class":"IPM.Note","subject":"Hello"}
`

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		switch name := filepath.Base(os.Args[0]); name {
		case "spike-ai-pipeline-mail":
			os.Exit(standin(os.Args[1:]))
		case "magick":
			os.Exit(pictureStandin(name, os.Args[1:]))
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

// standin is the stand-in mail. With --version alone it prints the version alone, as mail does.
// Otherwise it appends its arguments to the log file, writes stdoutEnv's value, or standinLines,
// to stdout, sleeps when sleepEnv is set, writes stderrEnv's value to stderr, and exits with
// exitEnv's code when it is set, else zero. It reads no input.
func standin(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(os.Getenv(versionEnv))
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "stand-in: no input")
		return 2
	}
	if err := appendLog(os.Getenv(logEnv), args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	out, set := os.LookupEnv(stdoutEnv)
	if !set {
		out = standinLines
	}
	if _, err := os.Stdout.WriteString(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if os.Getenv(sleepEnv) != "" {
		time.Sleep(time.Minute)
	}
	fmt.Fprint(os.Stderr, strings.ReplaceAll(os.Getenv(stderrEnv), "{path}", args[len(args)-1]))
	if code := os.Getenv(exitEnv); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return n
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

// installStandin links the test binary as spike-ai-pipeline-mail in a directory of its own, makes
// that directory the whole PATH, and has the stand-in report version. It returns a function that
// returns the arguments of every run since, in order, the version commands left out.
func installStandin(t *testing.T, version string) func() [][]string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, "spike-ai-pipeline-mail")); err != nil {
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

// standinReader builds a Reader over the stand-in mail at 0.2.0, built from MailSpec through the
// exec runner, and returns it with the stand-in's run log.
func standinReader(t *testing.T) (*email.Reader, func() [][]string) {
	t.Helper()
	runs := installStandin(t, "0.2.0")
	tl, err := tool.NewExec(t.Context(), email.MailSpec())
	if err != nil {
		t.Fatal(err)
	}
	r, err := email.NewReader(email.Config{Mail: tl})
	if err != nil {
		t.Fatal(err)
	}
	return r, runs
}

// mustRead returns a function that returns Read's Stream, given Read's results, failing the test
// if Read refused the request: mustRead(t)(r.Read(ctx, req)).
func mustRead(t *testing.T) func(*email.Stream, error) *email.Stream {
	return func(s *email.Stream, err error) *email.Stream {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

// collect ranges over s's records, returning those yielded and the error, if any, that ended
// them. It takes Read's results as they are: err, Read's refusal, is returned at once.
func collect(s *email.Stream, err error) ([]email.Record, error) {
	if err != nil {
		return nil, err
	}
	var recs []email.Record
	for rec, err := range s.Records() {
		if err != nil {
			return recs, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}
