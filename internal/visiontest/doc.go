//go:build integration && linux

// Package visiontest holds the integration tier's vision helpers, which the test packages that
// send pictures or pages to a model on the llama.cpp router share. It builds under the
// integration tag alone, so no released file imports it or spike-ai-pipeline-client.
//
// Its exported identifiers:
//
//   - [Router] returns a client of the router SPIKE_ROUTER_URL names, skipping the test when it
//     is unset.
//   - [PixelSize] returns a picture's width and height, for a recorded line.
//   - [ResponseText] returns a response's output text.
//   - [ResponseStatus] returns a response's status, with its incomplete reason or error.
package visiontest
