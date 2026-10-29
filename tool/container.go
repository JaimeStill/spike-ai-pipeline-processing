//go:build linux

package tool

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Image is the image a container tool runs in, and the relaxations of a run's lockdown that the
// image needs. A run is otherwise locked down: no network, no capabilities, the caller's user.
type Image struct {
	// Ref is the image's reference, pinned by digest with its tag kept for readers:
	// repo:tag@sha256: and 64 hex digits.
	Ref string
	// Network is the network a run joins, a docker network name such as bridge; empty is none.
	Network string
	// CapAdd are the capabilities a run gets back after it drops them all, such as NET_RAW.
	CapAdd []string
}

// imageRef matches a reference pinned by digest that keeps its tag. The repository is greedy, so
// the tag is what follows its last colon, and a tag can't hold a slash, so a registry's port is
// never taken for one.
var imageRef = regexp.MustCompile(`^[a-z0-9][^\s@]*:[\w][\w.-]{0,127}@sha256:[0-9a-f]{64}$`)

// dockerSpec declares the docker CLI for program, "docker" when empty. Its version is the
// server's, so a daemon the CLI can't reach fails the version command; 20.10 is the first with
// run's --pull.
func dockerSpec(program string) Spec {
	if program == "" {
		program = "docker"
	}
	return Spec{
		Program:        program,
		VersionArgs:    []string{"version", "--format={{.Server.Version}}"},
		VersionPattern: regexp.MustCompile(`^(\d+(?:\.\d+)*)`),
		Minimum:        "20.10",
	}
}

// containerPrefix begins every run's container name; a random suffix follows it.
const containerPrefix = "tool-"

// stopMargin bounds docker stop beyond a run's KillAfter period: the daemon's own work in stopping
// and removing the container.
const stopMargin = 15 * time.Second

// cliKillAfter is the docker CLI's own KillAfter period in a run: its context ends only once its
// container is stopped, so it has only to exit.
const cliKillAfter = 2 * time.Second

// NewContainer builds a Tool that runs spec's program inside image, the container runner. docker
// is the docker CLI, a name looked up on PATH or a path, "docker" when empty; it runs through the
// exec runner, found and checked as NewExec checks a program, against the server's version,
// minimum 20.10, so a daemon it can't reach is an error here. It validates spec and image first,
// refusing a reference without a digest; then it checks docker, pulls the image by its reference
// when it isn't present, and runs spec's version command inside the image, spec's program as the
// entrypoint, checking it as NewExec does. All of it runs under ctx.
//
// The Tool is named for spec's program, which is the image's, never looked up on the host. Each
// run passes --rm, --init, --network=none, the caller's uid:gid as --user, --cap-drop=ALL,
// --pull=never, a name of its own, and spec's program as --entrypoint; it mounts the invocation's
// working directory at the same absolute path and works in it, so the same arguments name the
// same files through either runner, and files the tool writes there belong to the caller. An
// empty Dir is the process's working directory, and a relative one is made absolute. It passes
// --interactive only when the invocation has stdin. The image's Network and CapAdd relax the
// network and add capabilities; nothing else of the lockdown changes.
//
// A cancelled run's container is stopped by docker stop on its name, under a context of its own,
// given spec's KillAfter period rounded up to whole seconds; the CLI's own signal forwarding is
// never relied on. Run then waits for the CLI, and returns an error matching ctx's, which names
// the CLI's outcome once, as the exec runner's names the program's. A tool that exits nonzero
// gives an *ExitError naming spec's program; docker's own failures exit 125, and an entrypoint it
// can't run, 126 or 127.
func NewContainer(ctx context.Context, docker string, image Image, spec Spec) (*Tool, error) {
	name := filepath.Base(spec.Program)
	if spec.Program == "" {
		return nil, errors.New("tool: container: no program")
	}
	minimum, err := spec.minimum(name)
	if err != nil {
		return nil, err
	}
	if !imageRef.MatchString(image.Ref) {
		return nil, fmt.Errorf("tool: %s: image %q: want repo:tag@sha256: and 64 hex digits", name, image.Ref)
	}
	dspec := dockerSpec(docker)
	dname := filepath.Base(dspec.Program)
	dminimum, err := dspec.minimum(dname)
	if err != nil {
		return nil, err
	}
	dpath, err := lookPath(dspec.Program)
	if err != nil {
		return nil, err
	}
	drun := execRun(dname, dpath, nil, dspec.killAfter())
	if _, err := checkVersion(ctx, dname, dspec, dminimum, drun); err != nil {
		return nil, err
	}
	if err := ensureImage(ctx, name, image.Ref, drun); err != nil {
		return nil, err
	}
	run := containerRun(name, dpath, drun, runArgs(spec.Program, image), image.Ref, spec.killAfter())
	version, err := checkVersion(ctx, name, spec, minimum, run)
	if err != nil {
		return nil, err
	}
	return &Tool{name: name, version: version, run: run}, nil
}

// ensureImage pulls ref through drun, docker's runFunc, unless docker image inspect finds it
// present. Any failure to inspect counts as absent, and the pull reports what's wrong.
func ensureImage(ctx context.Context, name, ref string, drun runFunc) error {
	if drun(ctx, Invocation{Args: []string{"image", "inspect", "--format={{.Id}}", ref}}) == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tool: %s: image %s: %w", name, ref, err)
	}
	if err := drun(ctx, Invocation{Args: []string{"pull", ref}}); err != nil {
		return fmt.Errorf("tool: %s: pull %s: %w", name, ref, err)
	}
	return nil
}

// runArgs returns the arguments of docker run that every run of program in image shares, the
// lockdown and its relaxations, each valued flag joined to its value with = so no value is ever
// read as a flag.
func runArgs(program string, image Image) []string {
	network := image.Network
	if network == "" {
		network = "none"
	}
	args := []string{
		"run", "--rm", "--init",
		"--network=" + network,
		"--user=" + strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--cap-drop=ALL",
	}
	for _, c := range image.CapAdd {
		args = append(args, "--cap-add="+c)
	}
	return append(args, "--pull=never", "--entrypoint="+program)
}

// containerRun returns the container runner's runFunc. Each run executes the docker CLI at dpath
// with base, the run's name, its mount and working directory, --interactive when it has stdin,
// ref, and then the invocation's arguments; drun runs docker's other commands.
//
// Cancellation goes around the exec runner's own: the CLI runs under a context of its own, which
// ctx's end doesn't reach, since the CLI forwards SIGTERM to its container but can't forward
// SIGKILL, and a CLI killed by its KillAfter period would leave the container running. When ctx
// ends, stopContainer runs docker stop on the run's name, and only once the container is stopped
// does it end the CLI's context. The exec runner's SIGTERM then reaches a CLI with nothing left to
// forward, and its cliKillAfter period bounds a CLI that doesn't exit on its own. Run waits for the
// CLI either way, and returns an error matching ctx's.
func containerRun(name, dpath string, drun runFunc, base []string, ref string, killAfter time.Duration) runFunc {
	cli := execRun(name, dpath, nil, cliKillAfter)
	return func(ctx context.Context, inv Invocation) error {
		// A run whose context has already ended never starts, so no container can start after
		// the stop meant for it.
		if err := ctx.Err(); err != nil {
			return &cancelError{program: name, ctxErr: err}
		}
		dir, err := workDir(inv.Dir)
		if err != nil {
			return fmt.Errorf("tool: %s: %w", name, err)
		}
		cname := containerName()
		args := slices.Concat(base, []string{"--name=" + cname, "--volume=" + dir + ":" + dir, "--workdir=" + dir})
		if inv.Stdin != nil {
			args = append(args, "--interactive")
		}
		args = append(append(args, ref), inv.Args...)

		cliCtx, endCLI := context.WithCancel(context.WithoutCancel(ctx))
		defer endCLI()
		exited := make(chan struct{})
		stopped := make(chan bool, 1)
		go func() {
			select {
			case <-exited:
				stopped <- false
			case <-ctx.Done():
				stopContainer(ctx, cname, drun, killAfter, exited, endCLI)
				stopped <- true
			}
		}()
		err = cli(cliCtx, Invocation{Args: args, Stdin: inv.Stdin, Stdout: inv.Stdout, Stderr: inv.Stderr})
		close(exited)
		if <-stopped {
			// ctx ended the run: its error is the cause, whatever the CLI's exit. The CLI's error
			// is the exec runner's, an ExitError or, once endCLI has ended the CLI's context, a
			// cancelError of its own, so only its outcome is named.
			var outcome error
			if err != nil {
				outcome = runOutcome(err)
			}
			return &cancelError{program: name, ctxErr: ctx.Err(), outcome: outcome}
		}
		return err
	}
}

// stopContainer stops the container cname once ctx, its run's context, has ended, and returns once
// exited closes, the CLI having exited. It runs docker stop with killAfter rounded up to whole
// seconds, SIGTERM and then SIGKILL, under a fresh context bounded by killAfter and stopMargin. A
// stop can come before the CLI has created its container, and fails then, so it is repeated each
// second while the CLI runs and the bound allows. Once a stop succeeds, or the bound passes, endCLI
// ends the CLI's own context; once the CLI has exited, docker rm --force removes whatever
// container a stop might have missed, so none named for the run remains.
func stopContainer(ctx context.Context, cname string, drun runFunc, killAfter time.Duration, exited <-chan struct{}, endCLI context.CancelFunc) {
	secs := strconv.FormatInt(int64((killAfter+time.Second-1)/time.Second), 10)
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), killAfter+stopMargin)
	defer cancel()
	for drun(stopCtx, Invocation{Args: []string{"stop", "--time=" + secs, cname}, Stdout: io.Discard, Stderr: io.Discard}) != nil {
		select {
		case <-exited:
			return
		case <-stopCtx.Done():
		case <-time.After(time.Second):
		}
		if stopCtx.Err() != nil {
			break
		}
	}
	endCLI()
	<-exited
	rmCtx, cancelRm := context.WithTimeout(context.WithoutCancel(ctx), stopMargin)
	defer cancelRm()
	_ = drun(rmCtx, Invocation{Args: []string{"rm", "--force", cname}, Stdout: io.Discard, Stderr: io.Discard})
}

// workDir returns dir as the absolute path a run mounts and works in: the process's working
// directory when dir is empty. A colon would split docker's --volume, so a path with one is
// refused.
func workDir(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("working directory %s: %w", dir, err)
	}
	if strings.Contains(abs, ":") {
		return "", fmt.Errorf("working directory %s: a colon can't be mounted", abs)
	}
	return abs, nil
}

// containerName returns a new run's container name: containerPrefix and 26 random base32
// digits, lowercased, 130 bits.
func containerName() string {
	return containerPrefix + strings.ToLower(rand.Text())
}
