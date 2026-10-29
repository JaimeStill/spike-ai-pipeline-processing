# Changelog

All notable changes to `github.com/JaimeStill/spike-ai-pipeline-processing` are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `tool`, which runs mature system tools behind one contract, on Linux and the standard library
  alone. A `Spec` declares a tool: its program, the arguments that print its version, a pattern
  whose first capture group is the version, the minimum version, and a cancelled run's
  `KillAfter` period. A runner's constructor finds the program and checks its version, so a
  missing or outdated tool fails at startup; the `Tool` it returns runs an `Invocation`
  (arguments, working directory, and standard streams) with `Tool.Run` under the caller's
  context, and an adapter written against `Tool` runs the same code whichever runner built it.
- The version rule: `Version`, dotted numbers compared as numbers, `ParseVersion`, and
  `Version.Compare`. A tool older than its minimum, or whose output holds no version, is a
  `*VersionError` wrapping `ErrTooOld` or `ErrNoVersion`. A program that exits nonzero is an
  `*ExitError`: its name, exit code, and stderr's tail.
- The exec runner: `NewExec` runs a program directly, and `NewScript` runs a script through its
  interpreter.
- Cancellation: a cancelled run's process group gets SIGTERM, then SIGKILL once the spec's
  `KillAfter` period passes, 10 s when unset, and `Run` returns an error matching the context's.
  The group's SIGKILL lands before its leader is reaped, and a cancelled run never waits on the
  caller's stdin.
- The container runner: `NewContainer` runs a program inside an `Image` pinned by digest,
  `repo:tag@sha256:` and 64 hex digits, through the docker CLI, pulling the image when it isn't
  present and checking the program's version inside it. A run is locked down (no network, no
  capabilities, the caller's uid:gid, `--rm`, `--init`, `--pull=never`), the image's `Network`
  and `CapAdd` its only relaxations, and it mounts the working directory at the same path, so
  the same arguments name the same files through either runner. A cancelled run's container is
  stopped by `docker stop` on its name, given the `KillAfter` period.
- The CI pair: a check workflow on every pull request into and push to main, and a release
  workflow that publishes a pushed `v*` tag's section of this changelog.

[Unreleased]: https://github.com/JaimeStill/spike-ai-pipeline-processing/commits/main
