//go:build linux

// Package tool runs mature system tools behind one contract: a Tool bound to one program, built by
// a runner's constructor, which finds the program and checks its version so a missing or outdated
// tool fails at startup. Tool.Run runs one Invocation, arguments, a working directory the caller
// owns, and optional standard streams, under the caller's context. An Invocation names nothing
// about the runner, so an adapter written against Tool runs the same code whichever runner built
// it. The package builds on Linux only, on the standard library alone.
//
// The exec runner runs a program directly: NewExec looks a name up on PATH and takes a path with a
// separator as given, and NewScript runs a script through its interpreter, which it finds and
// checks as NewExec does a binary. The container runner, NewContainer, runs the program inside an
// Image through the docker CLI, the only runtime. It runs the CLI through the exec runner and
// checks the server's version, minimum 20.10. The image's reference is pinned by digest;
// construction pulls it when it isn't present and runs the version command inside it. Every run
// is locked down, --rm, --init, --network=none, the caller's uid:gid, --cap-drop=ALL, and
// --pull=never, and the Image's Network and CapAdd are its only relaxations. A run mounts its
// working directory at the same absolute path and works in it, so the same arguments name the same
// files through either runner.
//
// A Spec declares the version check: arguments that print the version, a pattern whose first
// capture group is the version, and a minimum. A Version is dotted numbers compared as numbers,
// a missing component counting as zero, and the check is a minimum, never an exact version.
//
// Cancelling a run's context stops its tool. Through the exec runner the program's process group
// gets SIGTERM, then SIGKILL once the Spec's KillAfter period passes, 10 s when unset. Through the
// container runner, docker stop on the run's container stops it, given the KillAfter period; the
// CLI's own signal forwarding is never relied on. Either way, a run that fails once its context
// has ended returns an error matching the context's, errors.Is context.Canceled or
// context.DeadlineExceeded, never an *ExitError; it names the program's own outcome once, as in
// "tool: ffprobe: context canceled (exit status 143)". If the process running the package is
// itself SIGKILLed, a running container runs on until its tool exits, and --rm removes it then.
//
// A program that exits nonzero is an *ExitError, which keeps the last 4 KiB of the program's
// standard error when the caller passed no writer for it; a constructor's refusal over the version
// is a *VersionError. An Invocation's Stdin, when it isn't an *os.File, is copied through a pipe
// Run owns, so a reader the caller holds open never keeps Run from returning; the cost is that the
// copy may make one more Read of it after Run returns.
//
// An Invocation's Usage, when not nil, asks for the run's resource usage, which Run sets whatever
// the run's outcome. The exec runner fills it once the program is reaped, from the program's
// rusage: peak resident memory in bytes, and user and system CPU time, the children the program
// itself reaped included. The container runner leaves it zero, since the docker CLI's usage isn't
// the container's.
//
// Its exported identifiers:
//
//   - [Spec] declares a tool: its program, its version command and pattern, its minimum version,
//     and a cancelled run's KillAfter period.
//   - [Invocation] is one run of a tool: arguments, working directory, standard streams, and
//     where to report its usage.
//   - [Usage] is a run's resource usage: peak resident memory, and user and system CPU time.
//   - [Tool] is one program, found and version-checked by the runner's constructor that built it.
//   - [NewExec] builds a Tool that runs a program directly, the exec runner.
//   - [NewScript] builds a Tool that runs a script through its interpreter, on the exec runner.
//   - [NewContainer] builds a Tool that runs a program inside a pinned image through the docker
//     CLI, the container runner.
//   - [Image] is a container tool's image, pinned by digest, and its relaxations of the lockdown.
//   - [Tool.Name] returns the program's name, or the script's.
//   - [Tool.Version] returns the version found at construction.
//   - [Tool.Run] runs the program once under the caller's context.
//   - [ExitError] reports a program that exited nonzero: its name, exit code, and stderr tail.
//   - [VersionError] reports a tool its constructor refused over its version.
//   - [ErrTooOld] and [ErrNoVersion] are a VersionError's causes: a version older than the
//     minimum, and output with no version in it.
//   - [Version] is a tool's version, dotted numbers compared as numbers.
//   - [ParseVersion] parses dotted numbers into a Version.
//   - [Version.Compare] orders two versions.
//   - [Version.String] returns a version's dotted form.
package tool
