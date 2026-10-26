# Standards

The judgement calls the check can't enforce, one line each, for the standards-reviewer. What
`mise run check` enforces is never restated here.

The architecture pages that apply, in the architecture repository:

- `principles/minimal-footprint.md`
- `principles/composition-root.md`
- `principles/validation-first.md`
- `principles/rolling-currency.md`
- `standards/go-elemental/principles/topology-and-naming.md`
- `standards/go-elemental/principles/dependencies.md`
- `standards/go-elemental/principles/baseline-standards.md`
- `standards/go-elemental/principles/tests-and-docs.md`

## Processing

- A mature system tool is run through an adapter, never reimplemented in Go
  (`standards-lab/context/system-tools.md`).
- An adapter checks its tool's version when it is constructed, so a missing or outdated tool
  fails at startup.
- A tool runs under the caller's context, and its temporary files live in a directory the caller
  owns.
- A tool's options, such as an image correction, are per-request values, never library policy.
