//go:build linux

package tool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// testRef is the stand-in image, pinned by digest.
const testRef = "registry.example:5000/team/standin:1.0@sha256:" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// containerNamePattern matches a run's container name: the runner's prefix and its random suffix.
var containerNamePattern = regexp.MustCompile(`^tool-[a-z2-7]{26}$`)

// newContainer builds the stand-in tool, version 1.2.3, in image, through the stand-in docker.
func newContainer(t *testing.T, image tool.Image) *tool.Tool {
	t.Helper()
	tl, err := tool.NewContainer(t.Context(), "", image, standinSpec("standin", "1.2.3", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func TestNewContainer(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	if got := tl.Name(); got != "standin" {
		t.Errorf("Name() = %q, want standin", got)
	}
	if got := tl.Version().String(); got != "1.2.3" {
		t.Errorf("Version() = %q, want 1.2.3", got)
	}
	want := [][]string{{"version", "--format={{.Server.Version}}"}}
	if got := dockerCalls(t, dir, "version"); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("version calls = %q, want %q", got, want)
	}
	if got := dockerCalls(t, dir, "pull"); len(got) != 0 {
		t.Errorf("pulled %q, want no pull of a present image", got)
	}
	runs := dockerCalls(t, dir, "run")
	if len(runs) != 1 || !slices.Equal(runs[0][len(runs[0])-3:], []string{testRef, "print", "standin version 1.2.3"}) {
		t.Errorf("run calls = %q, want the version command in the image", runs)
	}
}

func TestNewContainerTakesDockerPathAsGiven(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	docker := filepath.Join(filepath.SplitList(os.Getenv("PATH"))[0], "docker")
	t.Setenv("PATH", t.TempDir()) // docker is no longer on PATH
	_, err := tool.NewContainer(t.Context(), docker, tool.Image{Ref: testRef}, standinSpec("standin", "1.2.3", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewContainerDockerNotFound(t *testing.T) {
	installDocker(t, "29.0.1")
	_, err := tool.NewContainer(t.Context(), "missing-docker", tool.Image{Ref: testRef}, standinSpec("standin", "1.2.3", "1.0"))
	if err == nil || !strings.Contains(err.Error(), "missing-docker") {
		t.Fatalf("NewContainer error = %v, want one naming missing-docker", err)
	}
}

// TestNewContainerRefusesRef checks that a reference without a digest and its tag is refused
// before docker runs at all.
func TestNewContainerRefusesRef(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	digest := "@sha256:" + strings.Repeat("ab", 32)
	for _, ref := range []string{
		"standin:1.0",                                    // a tag alone
		"standin" + digest,                               // a digest without a tag
		"registry.example:5000/standin" + digest,         // a port isn't a tag
		"standin:1.0@sha256:abcdef",                      // a short digest
		"standin:1.0@sha256:" + strings.Repeat("AB", 32), // upper-case hex
		"standin:1.0@sha512:" + strings.Repeat("ab", 64), // another algorithm
		"",
	} {
		t.Run(ref, func(t *testing.T) {
			_, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: ref}, standinSpec("standin", "1.2.3", "1.0"))
			if err == nil || !strings.Contains(err.Error(), "sha256") {
				t.Fatalf("NewContainer error = %v, want a refused reference", err)
			}
		})
	}
	if got := dockerCalls(t, dir, "version"); len(got) != 0 {
		t.Errorf("docker ran %q, want nothing run for a refused reference", got)
	}
}

func TestNewContainerDaemonUnreachable(t *testing.T) {
	installDocker(t, "")
	_, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: testRef}, standinSpec("standin", "1.2.3", "1.0"))
	if _, ok := errors.AsType[*tool.VersionError](err); !ok {
		t.Fatalf("NewContainer error = %v, want a *tool.VersionError", err)
	}
	assertNames(t, err, "docker", "", "20.10")
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("error %q doesn't carry docker's own message", err)
	}
}

func TestNewContainerDockerVersion(t *testing.T) {
	tests := []struct {
		server string
		ok     bool
	}{
		{"20.10.0", true},
		{"29.0.1", true},
		{"20.10.24+dfsg1", true}, // a distribution's suffix
		{"20.9.9", false},
		{"19.03.15", false},
	}
	for _, tt := range tests {
		t.Run(tt.server, func(t *testing.T) {
			dir := installDocker(t, tt.server)
			addImage(t, dir, testRef)
			_, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: testRef}, standinSpec("standin", "1.2.3", "1.0"))
			if tt.ok {
				if err != nil {
					t.Fatalf("NewContainer: %v", err)
				}
				return
			}
			if !errors.Is(err, tool.ErrTooOld) {
				t.Fatalf("NewContainer error = %v, want ErrTooOld", err)
			}
			assertNames(t, err, "docker", strings.TrimSuffix(tt.server, "+dfsg1"), "20.10")
		})
	}
}

func TestNewContainerPullsAbsentImage(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	newContainer(t, tool.Image{Ref: testRef})
	want := [][]string{{"pull", testRef}}
	if got := dockerCalls(t, dir, "pull"); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("pull calls = %q, want %q", got, want)
	}
	inspects := dockerCalls(t, dir, "image")
	if len(inspects) != 1 || inspects[0][len(inspects[0])-1] != testRef {
		t.Errorf("inspect calls = %q, want one of %s", inspects, testRef)
	}
}

func TestNewContainerVersionInImage(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	t.Run("too old", func(t *testing.T) {
		_, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: testRef}, standinSpec("standin", "1.2.3", "1.3"))
		if !errors.Is(err, tool.ErrTooOld) {
			t.Fatalf("NewContainer error = %v, want ErrTooOld", err)
		}
		assertNames(t, err, "standin", "1.2.3", "1.3")
	})
	t.Run("wrong entrypoint", func(t *testing.T) {
		_, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: testRef}, standinSpec("not-in-image", "1.2.3", "1.0"))
		if _, ok := errors.AsType[*tool.VersionError](err); !ok {
			t.Fatalf("NewContainer error = %v, want a *tool.VersionError", err)
		}
		if exitErr, ok := errors.AsType[*tool.ExitError](err); !ok || exitErr.Code != 127 || exitErr.Program != "not-in-image" {
			t.Errorf("error %q doesn't carry not-in-image's exit code 127", err)
		}
		if !strings.Contains(err.Error(), "executable file not found") {
			t.Errorf("error %q doesn't carry docker's message", err)
		}
	})
}

// TestContainerRunArgs checks each run's arguments, the lockdown whole, with and without stdin.
func TestContainerRunArgs(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	work := t.TempDir()
	user := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	tests := map[string]struct {
		stdin       *strings.Reader
		interactive []string
		wantStdin   string
	}{
		"no stdin": {nil, nil, ""},
		"stdin":    {strings.NewReader("from stdin"), []string{"--interactive"}, "from stdin"},
	}
	var names []string
	for label, tt := range tests {
		t.Run(label, func(t *testing.T) {
			before := len(dockerCalls(t, dir, "run"))
			inv := tool.Invocation{Args: []string{"echo", "a b", "--flag=1"}, Dir: work}
			var stdout bytes.Buffer
			inv.Stdout = &stdout
			if tt.stdin != nil {
				inv.Stdin = tt.stdin
			}
			if err := tl.Run(t.Context(), inv); err != nil {
				t.Fatal(err)
			}
			runs := dockerCalls(t, dir, "run")
			if len(runs) != before+1 {
				t.Fatalf("run calls = %q, want one more", runs)
			}
			got := runs[before]
			name := flagValue(got, "--name")
			if !containerNamePattern.MatchString(name) {
				t.Errorf("container name %q doesn't match %s", name, containerNamePattern)
			}
			names = append(names, name)
			want := slices.Concat([]string{
				"run", "--rm", "--init", "--network=none", "--user=" + user, "--cap-drop=ALL",
				"--pull=never", "--entrypoint=standin", "--name=" + name,
				"--volume=" + work + ":" + work, "--workdir=" + work,
			}, tt.interactive, []string{testRef, "echo", "a b", "--flag=1"})
			if !slices.Equal(got, want) {
				t.Errorf("run args =\n%q\nwant\n%q", got, want)
			}
			var e echo
			if err := json.Unmarshal(stdout.Bytes(), &e); err != nil {
				t.Fatalf("decoding %q: %v", stdout.String(), err)
			}
			if e.Dir != work || e.Stdin != tt.wantStdin {
				t.Errorf("container saw dir %q and stdin %q, want %q and %q", e.Dir, e.Stdin, work, tt.wantStdin)
			}
		})
	}
	if len(names) == 2 && names[0] == names[1] {
		t.Errorf("two runs shared the container name %s", names[0])
	}
}

// TestContainerRunDir checks that an empty Dir is the process's working directory and a
// relative one is made absolute, and that a colon, which docker's --volume can't take, is
// refused.
func TestContainerRunDir(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	for given, want := range map[string]string{"": work, "sub": filepath.Join(work, "sub")} {
		t.Run(given, func(t *testing.T) {
			if err := tl.Run(t.Context(), tool.Invocation{Args: []string{"print", "x"}, Dir: given}); err != nil {
				t.Fatal(err)
			}
			runs := dockerCalls(t, dir, "run")
			last := runs[len(runs)-1]
			if got := flagValue(last, "--volume"); got != want+":"+want {
				t.Errorf("--volume = %q, want %s:%s", got, want, want)
			}
			if got := flagValue(last, "--workdir"); got != want {
				t.Errorf("--workdir = %q, want %s", got, want)
			}
		})
	}
	t.Run("colon", func(t *testing.T) {
		err := tl.Run(t.Context(), tool.Invocation{Args: []string{"print", "x"}, Dir: "/tmp/a:b"})
		if err == nil || !strings.Contains(err.Error(), "colon") {
			t.Errorf("Run error = %v, want a refused working directory", err)
		}
	})
}

func TestContainerRelaxations(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef, Network: "bridge", CapAdd: []string{"NET_RAW", "CHOWN"}})
	if err := tl.Run(t.Context(), tool.Invocation{Args: []string{"print", "x"}}); err != nil {
		t.Fatal(err)
	}
	runs := dockerCalls(t, dir, "run")
	got := runs[len(runs)-1]
	user := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	want := []string{
		"run", "--rm", "--init", "--network=bridge", "--user=" + user, "--cap-drop=ALL",
		"--cap-add=NET_RAW", "--cap-add=CHOWN", "--pull=never", "--entrypoint=standin",
	}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("run args =\n%q\nwant them to begin\n%q", got, want)
	}
}

func TestContainerExitError(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	err := tl.Run(t.Context(), tool.Invocation{Args: []string{"exit", "3", "it broke"}})
	exitErr, ok := errors.AsType[*tool.ExitError](err)
	if !ok {
		t.Fatalf("Run error = %v, want a *tool.ExitError", err)
	}
	if exitErr.Program != "standin" || exitErr.Code != 3 || exitErr.Stderr != "it broke" {
		t.Errorf("ExitError = %+v, want standin, code 3, stderr %q", exitErr, "it broke")
	}
}

// TestContainerCancel checks that a cancelled run stops its container by docker stop, with the
// KillAfter period rounded up to whole seconds, and returns the context's error. The stand-in CLI
// ignores SIGTERM, so only docker stop ends the container.
func TestContainerCancel(t *testing.T) {
	tests := map[string]struct {
		killAfter time.Duration
		command   string
		time      string
		min       time.Duration
	}{
		"terminates":      {1500 * time.Millisecond, "sleep", "--time=2", 0},
		"killed after it": {time.Second, "stubborn", "--time=1", time.Second},
		"default":         {0, "sleep", "--time=10", 0},
	}
	for label, tt := range tests {
		t.Run(label, func(t *testing.T) {
			dir := installDocker(t, "29.0.1")
			addImage(t, dir, testRef)
			spec := standinSpec("standin", "1.2.3", "1.0")
			spec.KillAfter = tt.killAfter
			tl, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: testRef}, spec)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := newReadyWriter()
			done := runAsync(ctx, tl, tool.Invocation{Args: []string{tt.command, "1m"}, Stdout: ready})
			ready.wait(t)
			start := time.Now()
			cancel()
			err = <-done
			elapsed := time.Since(start)
			assertContextError(t, err, context.Canceled)
			if elapsed < tt.min || elapsed > tt.min+5*time.Second {
				t.Errorf("Run took %v after cancel, want %v to %v", elapsed, tt.min, tt.min+5*time.Second)
			}
			runs := dockerCalls(t, dir, "run")
			name := flagValue(runs[len(runs)-1], "--name")
			stops := dockerCalls(t, dir, "stop")
			if len(stops) == 0 || !slices.Equal(stops[0], []string{"stop", tt.time, name}) {
				t.Errorf("stop calls = %q, want [stop %s %s]", stops, tt.time, name)
			}
			if rms := dockerCalls(t, dir, "rm"); len(rms) != 1 || !slices.Equal(rms[0], []string{"rm", "--force", name}) {
				t.Errorf("rm calls = %q, want [rm --force %s]", rms, name)
			}
			if running := runningContainers(t, dir); len(running) != 0 {
				t.Errorf("containers %q outlived the cancelled run", running)
			}
		})
	}
}

// TestContainerCancelHeldStdin checks that a cancelled run returns though the caller holds its
// stdin, a pipe, open.
func TestContainerCancelHeldStdin(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	stdin, held, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close(); _ = stdin.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := newReadyWriter()
	done := runAsync(ctx, tl, tool.Invocation{Args: []string{"sleep", "1m"}, Stdin: stdin, Stdout: ready})
	ready.wait(t)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		assertContextError(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never returned after cancel with its stdin held open")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %v after cancel, want a few seconds at most", elapsed)
	}
}

func TestContainerDeadlineExceeded(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := tl.Run(ctx, tool.Invocation{Args: []string{"sleep", "1m"}})
	assertContextError(t, err, context.DeadlineExceeded)
	if running := runningContainers(t, dir); len(running) != 0 {
		t.Errorf("containers %q outlived the run", running)
	}
}

// TestContainerRunContextAlreadyDone checks that a run whose context has ended never starts a
// container.
func TestContainerRunContextAlreadyDone(t *testing.T) {
	dir := installDocker(t, "29.0.1")
	addImage(t, dir, testRef)
	tl := newContainer(t, tool.Image{Ref: testRef})
	before := len(dockerCalls(t, dir, "run"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := tl.Run(ctx, tool.Invocation{Args: []string{"sleep", "1m"}})
	assertContextError(t, err, context.Canceled)
	if got := len(dockerCalls(t, dir, "run")); got != before {
		t.Errorf("a run started under an ended context")
	}
}

// flagValue returns the value of the flag joined to it with = in args, empty when absent.
func flagValue(args []string, flag string) string {
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, flag+"="); ok {
			return value
		}
	}
	return ""
}
