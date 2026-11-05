//go:build (integration || findings) && linux

// Package audiotest holds the integration and findings tiers' speech harness, which the test
// packages that send a source's windows to an audio model on the llama.cpp router share. It
// builds under the integration or findings tag alone, so no released file imports it or
// spike-ai-pipeline-client: inference is test code, and the audio package only prepares.
//
// The harness plans a source through an audio.Splitter, streams its windows through the
// Splitter's Windows, sends each to the router's transcription endpoint as it is cut, at most a
// given number of Transcribe calls in flight, and joins the transcripts, kept in window order,
// with audio.Join. It never holds more than the windows in flight: each window is read from its
// file as it is uploaded, and the file is removed once transcribed unless the request keeps it,
// so a source hours long is transcribed in the disk and memory of a few windows.
//
// Its exported identifiers:
//
//   - [Model] is set A's audio model, Gemma 4 E4B.
//   - [Transcriber] is the client, model, language, and native fields each window is sent with.
//   - [Greedy] returns the router's Transcriber for Model: language en, temperature 0.
//   - [Request] is one source through the harness: its Splitter, source, media directory,
//     Transcriber, and concurrency.
//   - [Window] is one window's transcript and timings.
//   - [Result] is a run's plan, windows, joined transcript, and timings.
//   - [Run] runs the harness over a Request.
package audiotest
