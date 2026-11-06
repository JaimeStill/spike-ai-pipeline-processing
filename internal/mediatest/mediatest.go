//go:build linux

package mediatest

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
)

// Frame is a frame the stand-in ffmpeg takes: its source time, in milliseconds, its scene score,
// and its PNG's size.
type Frame struct {
	MS     int64   `json:"ms"`
	Score  float64 `json:"score"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
}

// Report writes frame i, f, to stderr as media's metadata filter prints it, after settb puts its
// pts in microseconds: its count and pts, then its scene score.
func Report(i int, f Frame) {
	fmt.Fprintf(os.Stderr, "[Parsed_metadata_2 @ 0x55d0c0de] frame:%-4d pts:%-7d pts_time:%g\n", i, f.MS*1000, float64(f.MS)/1000)
	fmt.Fprintf(os.Stderr, "[Parsed_metadata_2 @ 0x55d0c0de] lavfi.scene_score=%f\n", f.Score)
}

// WritePNG writes frame i, f, as a PNG of its size, one colour from its time and index, to
// pattern's file for i, through a temporary file renamed into place, as the image2 muxer's atomic
// writing does.
func WritePNG(pattern string, i int, f Frame) error {
	img := image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
	c := color.RGBA{R: uint8(f.MS / 1000), G: uint8(i), B: 7, A: 255}
	for y := range f.Height {
		for x := range f.Width {
			img.SetRGBA(x, y, c)
		}
	}
	path := fmt.Sprintf(pattern, i)
	out, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	err = png.Encode(out, img)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Stream writes pcm, mono 16-bit PCM at audio.SampleRate, through write, half a second at a
// time, write given each chunk's first byte and the byte it ends before; and each of frames
// through frame, given its index, in order: each before the PCM from its time on, and those left
// once the PCM is written. The first error either returns ends it.
func Stream(pcm []byte, frames []Frame, write func(at, end int) error, frame func(i int) error) error {
	const chunk = 2 * audio.SampleRate / 2
	next := 0
	for at := 0; at < len(pcm) || next < len(frames); at += chunk {
		ms := int64(at) * 1000 / (2 * audio.SampleRate)
		for next < len(frames) && (frames[next].MS <= ms || at >= len(pcm)) {
			if err := frame(next); err != nil {
				return err
			}
			next++
		}
		if at < len(pcm) {
			if err := write(at, min(at+chunk, len(pcm))); err != nil {
				return err
			}
		}
	}
	return nil
}
