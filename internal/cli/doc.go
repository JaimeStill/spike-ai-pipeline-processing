// Package cli holds the contract the format CLIs this module runs share, office's
// spike-ai-pipeline-office and mail's spike-ai-pipeline-mail, OneNote's to come, and the helpers
// the adapters that run them, and the document package's renderers, share: one definition of
// each, so every adapter reads a CLI the same way.
//
// The contract: a CLI exits 0 having written its output; 1 when it couldn't write its output or
// its media; 2 when it refused its arguments, a fault in the adapter; 3 when it refused the
// input, giving its reason; and 4 at a fault of its own. Each line it writes to stderr begins
// with its name and ": ", a warning's then "warning: <where>: <what>", and a failure's its label
// and its message. Each adapter binds its exported exit errors, such as office.ErrRefused and
// email.ErrRefused, to the shared ones here, so errors.Is matches a refusal of any CLI through
// any adapter's error, and keeps its own ExitError type, whose Is and Error are [Is] and
// [ExitText].
//
// Its exported identifiers:
//
//   - [ErrNotWritten], [ErrUsage], [ErrRefused], and [ErrFailed] are exit codes 1 to 4.
//   - [Outcome] returns an exit code's error.
//   - [Is] reports whether an exit code's error is a target, for an ExitError's Is.
//   - [ExitText] returns an ExitError's text: its adapter, its outcome, and the CLI's message.
//   - [Program] names a CLI's stderr prefix and the labels its failures begin with;
//     [Program.ReadStderr] reads its warnings and its message.
//   - [LimitedBuffer] keeps the first bytes written to it, for a tool's output read only in
//     part.
//   - [Spool] copies an input into a file in a working directory, for a tool that reads a path.
package cli
