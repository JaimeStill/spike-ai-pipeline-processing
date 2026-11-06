//go:build linux

// Package mediatest holds the unit tier's stand-in for media's ffmpeg run, which the test packages
// that run media's Streamer over a stand-in ffmpeg share: media's own and dispatch's video stage's.
// Each package's stand-in decides where its PCM and frames come from; mediatest writes them as
// media's run of ffmpeg does, the frames reported on stderr as the metadata filter reports them
// and written as the image2 muxer writes them, interleaved with the PCM by source time. It builds
// on Linux, as media does, untagged, so the unit tier proves it through those packages' tests,
// and no released file imports it.
//
// Its exported identifiers:
//
//   - [Frame] is a frame the stand-in takes: its time, scene score, and size.
//   - [Report] writes a frame's report to stderr, as the metadata filter prints it after settb.
//   - [WritePNG] writes a frame's PNG to its image2 pattern's file, through a temporary file.
//   - [Stream] writes PCM and frames in source-time order, half a second of PCM at a time.
package mediatest
