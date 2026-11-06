// Package textscore holds the transcription scores the test tiers share, document's bake-offs
// and text-layer tests and dispatch's OneNote ink finding: a character error rate and a word
// error rate, each an edit distance over a ground truth's length, both texts whitespace-collapsed
// first so line breaks and spacing never count as errors, case counting. For speech, audio's
// findings score a word error rate over normalized words instead: lowercased, with punctuation
// and fillers dropped, hyphenated words split, and numbers left as written. They also count the
// edits near each seam of a joined transcript, in one alignment of its words against the truth's.
// It builds untagged so the unit tier proves it, and no released file imports it.
//
// Its exported identifiers:
//
//   - [Collapse] makes every run of whitespace one space.
//   - [EditDistance] returns the Levenshtein distance between two sequences.
//   - [CharErrors] and [WordErrors] return a transcription's character and word edit distances
//     from its truth, with the truth's length.
//   - [Rate] returns edits over a length, an error rate.
//   - [NormalizeWords] returns a speech transcript's normalized words, [Fillers] dropped.
//   - [NormalizedWordErrors] returns WordErrors over normalized words, speech's word error rate.
//   - [Align] returns a least-edit alignment, each step an [Edit] of an [Op], in memory that
//     grows as the square root of the truth's length times the transcript's.
//   - [BestPrefix] returns the prefix of a truth nearest a transcript of its beginning.
//   - [SeamErrors] counts the edits within [SeamRadius] words of each seam of a joined transcript.
package textscore
