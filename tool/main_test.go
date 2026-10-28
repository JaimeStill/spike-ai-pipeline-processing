//go:build linux

package tool_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// standinEnv marks a process as the stand-in tool: the test binary, re-executed through a link
// named standin, acting on its arguments instead of running the tests.
const standinEnv = "TOOL_TEST_STANDIN"

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		os.Exit(standin(os.Args[1:]))
	}
	// The stand-ins inherit the environment. Under -race, GORACE's atexit_sleep_ms keeps each
	// one from sleeping a second as it exits.
	for key, value := range map[string]string{standinEnv: "1", "GORACE": "atexit_sleep_ms=0"} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// standin is the stand-in tool. Its first argument picks what it does:
//
//	print TEXT        writes TEXT and a newline to stdout
//	exit CODE [TEXT]  writes TEXT to stderr, then exits with CODE
//	stderr N CODE     writes N bytes of stderr, letters a to z repeating, then exits with CODE
//	echo ARGS...      writes its arguments, working directory, and stdin to stdout as JSON
func standin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "standin: no command")
		return 2
	}
	switch args[0] {
	case "print":
		fmt.Println(args[1])
		return 0
	case "exit":
		if len(args) > 2 {
			fmt.Fprint(os.Stderr, args[2])
		}
		return atoi(args[1])
	case "stderr":
		_, _ = os.Stderr.Write(letters(atoi(args[1])))
		return atoi(args[2])
	case "echo":
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		stdin, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if err := json.NewEncoder(os.Stdout).Encode(echo{Args: args[1:], Dir: dir, Stdin: string(stdin)}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "standin: unknown command %q\n", args[0])
	return 2
}

// echo is what the stand-in's echo command writes.
type echo struct {
	Args  []string `json:"args"`
	Dir   string   `json:"dir"`
	Stdin string   `json:"stdin"`
}

// letters returns n bytes, a to z repeating.
func letters(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return n
}

// installStandin links the stand-in tool as standin in a directory of its own, makes that
// directory the whole PATH, and returns the link's path.
func installStandin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "standin")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return link
}
