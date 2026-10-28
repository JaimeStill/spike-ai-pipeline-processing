//go:build linux

// Package tool runs mature system tools behind one contract: a Tool bound to one program, built by
// a runner's constructor, which finds the program and checks its version so a missing or outdated
// tool fails at startup. An adapter written against Tool runs the same code whichever runner built
// it. The package builds on Linux only.
//
// Its exported identifiers:
//
//   - [Spec] declares a tool: its program, its version command and pattern, its minimum version,
//     and a cancelled run's KillAfter period.
//   - [Invocation] is one run of a tool: arguments, working directory, and standard streams.
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
