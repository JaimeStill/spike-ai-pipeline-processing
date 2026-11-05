// Package textscore holds the transcription scores the test tiers share, document's bake-offs
// and text-layer tests and dispatch's OneNote ink finding: a character error rate and a word
// error rate, each an edit distance over a ground truth's length, both texts whitespace-collapsed
// first so line breaks and spacing never count as errors, case counting. It builds untagged so
// the unit tier proves it, and no released file imports it.
//
// Its exported identifiers:
//
//   - [Collapse] makes every run of whitespace one space.
//   - [EditDistance] returns the Levenshtein distance between two sequences.
//   - [CharErrors] and [WordErrors] return a transcription's character and word edit distances
//     from its truth, with the truth's length.
//   - [Rate] returns edits over a length, an error rate.
package textscore
