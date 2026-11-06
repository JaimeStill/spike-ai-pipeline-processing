//go:build findings && linux

// Package corpustest holds the findings tier's readers of the test corpus at ~/docs, which the
// test packages that measure findings on its sources share: audio's long sources and media's
// videos. It builds under the findings tag alone, so no released file imports it.
//
// Every corpus file a finding reads is checked by its SHA-256 and size against the corpus
// manifest, MANIFEST.json at the corpus root, before it is read. The truths it reads are AMI's
// word-level transcription, its four speakers' words ordered by their segments, and a SubRip
// file's cues, as plain text a finding scores a transcript against.
//
// Its exported identifiers:
//
//   - [DocsEnv] names the environment variable that names the corpus root.
//   - [Root] returns the corpus root, ~/docs unless DocsEnv names another.
//   - [Entry] is a file's manifest entry; [Check] checks files against the manifest and returns
//     their entries.
//   - [AMITruth] are the corpus paths of AMI ES2002a's truth files; [ReadAMI] reads them.
//   - [ReadSRT] reads a SubRip file's cues as text.
package corpustest
