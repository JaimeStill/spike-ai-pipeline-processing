//go:build linux

package tool_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// standinEnv marks a process as the stand-in tool: the test binary, re-executed through a link
// named standin, acting on its arguments instead of running the tests.
const standinEnv = "TOOL_TEST_STANDIN"

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		if filepath.Base(os.Args[0]) == interpreterName {
			os.Exit(interpreter(os.Args[1:]))
		}
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
//	sleep D           exits 0 on SIGTERM, writing ready to stdout once it handles it; else
//	                  sleeps D and exits 1
//	stubborn D [FILE] ignores SIGTERM, then writes ready to stdout and its pid to FILE, then
//	                  sleeps D and exits 1
//	spawn FILE [held] starts a stubborn child, which writes its pid to FILE, then acts as
//	                  sleep 1m; the child has no streams, or with held, holds stdout and stderr
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
	case "sleep":
		return sleep(args[1])
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("ready")
		if len(args) > 2 {
			if err := writePid(args[2]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
		}
		time.Sleep(duration(args[1]))
		return 1
	case "spawn":
		return spawn(args[1], len(args) > 2 && args[2] == "held")
	}
	fmt.Fprintf(os.Stderr, "standin: unknown command %q\n", args[0])
	return 2
}

// sleep waits for SIGTERM and exits 0 on it, or exits 1 after d.
func sleep(d string) int {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	fmt.Println("ready")
	select {
	case <-term:
		return 0
	case <-time.After(duration(d)):
		return 1
	}
}

// spawn starts a stubborn stand-in as a child, in the stand-in's process group, which writes its
// pid to file once it ignores SIGTERM; then it sleeps as sleep does. The child holds none of the
// stand-in's streams, or, when held, its stdout and stderr.
func spawn(file string, held bool) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	child := exec.Command(exe, "stubborn", "1m", file)
	if held {
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
	}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return sleep("1m")
}

// writePid writes the process's pid and a newline to file, whole, through a rename, so a reader
// never sees part of it.
func writePid(file string) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// interpreterName is the stand-in interpreter's name: the test binary, linked under this name,
// acts as a sh with a version command.
const interpreterName = "standin-sh"

// interpreter is the stand-in interpreter. With --version V it writes "standin-sh version V" to
// stdout; otherwise it replaces itself with /bin/sh on the same arguments, so a script runs in a
// real shell with the stand-in's pid and process group.
func interpreter(args []string) int {
	if len(args) == 2 && args[0] == "--version" {
		fmt.Println(interpreterName + " version " + args[1])
		return 0
	}
	err := syscall.Exec("/bin/sh", append([]string{"sh"}, args...), os.Environ())
	fmt.Fprintln(os.Stderr, err)
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

func duration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return d
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return n
}

// installStandin links the stand-in tool as standin, and the stand-in interpreter as standin-sh,
// in a directory of its own, makes that directory the whole PATH, and returns standin's path.
func installStandin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "standin")
	for _, name := range []string{"standin", interpreterName} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return link
}
