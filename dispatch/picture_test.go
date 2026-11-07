//go:build linux

package dispatch_test

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The picture stage's hermetic tests run its Converter over a stand-in magick: the test binary,
// re-executed through a link named magick, acting on its arguments as ImageMagick would, as the
// picture package's own tests do.
const (
	// pictureStandinEnv marks a process as the stand-in.
	pictureStandinEnv = "DISPATCH_PICTURE_STANDIN"
	// pictureFormatsEnv is the format listing the stand-in's -list format prints.
	pictureFormatsEnv = "DISPATCH_PICTURE_FORMATS"
	// picturePagesEnv is how many pages the stand-in's pages run writes.
	picturePagesEnv = "DISPATCH_PICTURE_PAGES"
)

// pictureListing is a format listing whose HEIC, WebP, and AVIF readers magick has, in the form
// ImageMagick 7.1.2-34 prints it.
const pictureListing = `   Format  Module    Mode  Description
-------------------------------------------------------------------------------
     AVIF  HEIC      rw+   AV1 Image File Format (1.23.6)
     HEIC  HEIC      rw+   High Efficiency Image Format (1.23.6)
     WEBP* WEBP      rw+   WebP Image Format (libwebp 1.6.0 [0210])
`

// The pictures the picture stage's tests dispatch, by their signatures.
const (
	pictureTIFF = "II*\x00\x08\x00\x00\x00" + "a stand-in TIFF"
	pictureHEIC = "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"
	pictureWebP = "RIFF\x10\x00\x00\x00WEBPVP8 "
	pictureAVIF = "\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avif"
)

func init() {
	if os.Getenv(pictureStandinEnv) == "1" {
		os.Exit(pictureStandin(os.Args[1:]))
	}
}

// pictureStandin is the stand-in magick. With -version alone it prints its version, 7.1.2-34, and
// with -list format pictureFormatsEnv's listing. Otherwise its last argument names its output,
// "png:path": a pages run, one with +adjoin, writes picturePagesEnv's count of PNGs, each to the
// path with %d put for its index from 0, holding PNG's signature and "page N"; any other run
// writes one, holding PNG's signature and its first argument, the input it read.
func pictureStandin(args []string) int {
	switch {
	case len(args) == 1 && args[0] == "-version":
		fmt.Println("Version: ImageMagick 7.1.2-34 Q16-HDRI x86_64 https://imagemagick.org")
		return 0
	case len(args) == 2 && args[0] == "-list" && args[1] == "format":
		fmt.Print(os.Getenv(pictureFormatsEnv))
		return 0
	case len(args) < 2:
		fmt.Fprintf(os.Stderr, "stand-in: unexpected arguments %q\n", args)
		return 2
	}
	_, out, _ := strings.Cut(args[len(args)-1], ":")
	if !slices.Contains(args, "+adjoin") {
		if err := os.WriteFile(out, []byte(route.PNGSignature+args[0]), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	n, err := strconv.Atoi(os.Getenv(picturePagesEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for i := range n {
		if err := os.WriteFile(fmt.Sprintf(out, i), []byte(fmt.Sprintf("%spage %d", route.PNGSignature, i+1)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	return 0
}

// standinPictureStage builds a picture stage over the stand-in magick, through the exec runner,
// with listing as its format listing and pages as the pages its pages run writes.
func standinPictureStage(t *testing.T, listing string, pages int) *dispatch.PictureStage {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, "magick")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(pictureStandinEnv, "1")
	// Under -race, GORACE's atexit_sleep_ms keeps a stand-in from sleeping a second as it exits.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv(pictureFormatsEnv, listing)
	t.Setenv(picturePagesEnv, strconv.Itoa(pages))
	mg, err := tool.NewExec(t.Context(), picture.MagickSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := picture.NewConverter(t.Context(), picture.Config{Magick: mg})
	if err != nil {
		t.Fatal(err)
	}
	s, err := dispatch.NewPictureStage(dispatch.PictureConfig{Converter: c, Version: dispatch.Versions(mg)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestPictureStageTIFFPages: a TIFF of three pages is prepared as three image outputs, in page
// order, each a PNG named "page N", its Meta naming the kind, its page, and the page count.
func TestPictureStageTIFFPages(t *testing.T) {
	d := newDispatcher(t, dispatch.Stages{route.VisionConverted: standinPictureStage(t, pictureListing, 3)}, dispatch.NewMemoryIndex())
	var held []string
	events := dispatchAll(t, d, input(t, "scan.tif", pictureTIFF), func(ev *dispatch.Event) {
		if ev.Output != nil {
			b, err := os.ReadFile(ev.Output.Path)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, string(b))
		}
	})
	wantSteps(t, events, []step{{"scan.tif", 0, dispatch.Prepared}})
	outputs := events[0].Outputs
	if len(outputs) != 3 {
		t.Fatalf("%d outputs, want 3", len(outputs))
	}
	for i, o := range outputs {
		n := strconv.Itoa(i + 1)
		want := map[string]string{"from": "tiff", "page": n, "count": "3"}
		if o.Kind != dispatch.ImageOutput || o.Name != "page "+n || o.MediaType != "image/png" || !maps.Equal(o.Meta, want) {
			t.Errorf("output %d: %s %q %s %v; want an image named %q, meta %v", i, o.Kind, o.Name, o.MediaType, o.Meta, "page "+n, want)
		}
		if held[i] != route.PNGSignature+"page "+n {
			t.Errorf("output %d holds %q, want page %s", i, held[i], n)
		}
	}
}

// TestPictureStageRasters: a HEIC, a WebP, and an AVIF are each prepared as one image output, a
// PNG named "picture", its Meta naming the kind, its first image read through its kind's coder.
func TestPictureStageRasters(t *testing.T) {
	d := newDispatcher(t, dispatch.Stages{route.VisionConverted: standinPictureStage(t, pictureListing, 0)}, dispatch.NewMemoryIndex())
	for name, c := range map[string]struct {
		content string
		kind    route.Kind
	}{
		"x.heic": {pictureHEIC, route.HEIC},
		"x.webp": {pictureWebP, route.WebP},
		"x.avif": {pictureAVIF, route.AVIF},
	} {
		var held string
		events := dispatchAll(t, d, input(t, name, c.content), func(ev *dispatch.Event) {
			if ev.Output != nil {
				b, err := os.ReadFile(ev.Output.Path)
				if err != nil {
					t.Fatal(err)
				}
				held = string(b)
			}
		})
		wantSteps(t, events, []step{{name, 0, dispatch.Prepared}})
		o := events[0].Outputs
		if len(o) != 1 || o[0].Kind != dispatch.ImageOutput || o[0].Name != "picture" || !maps.Equal(o[0].Meta, map[string]string{"from": string(c.kind)}) {
			t.Errorf("%s: outputs %+v", name, o)
		}
		if coder := string(c.kind) + ":"; !strings.HasPrefix(held, route.PNGSignature+coder) || !strings.HasSuffix(held, "[0]") {
			t.Errorf("%s: PNG holds %q; want its first image read through %s", name, held, coder)
		}
	}
}

// TestPictureNoReader: a HEIC, a WebP, and an AVIF whose readers magick's listing doesn't mark
// have no processor yet, each reason naming the missing reader.
func TestPictureNoReader(t *testing.T) {
	d := newDispatcher(t, dispatch.Stages{route.VisionConverted: standinPictureStage(t, "", 0)}, dispatch.NewMemoryIndex())
	for name, c := range map[string]struct{ content, what string }{
		"x.heic": {pictureHEIC, "a HEIC"},
		"x.webp": {pictureWebP, "a WebP"},
		"x.avif": {pictureAVIF, "an AVIF"},
	} {
		ev := dispatchAll(t, d, input(t, name, c.content), nil)[0]
		want := "no processor yet: picture: " + name + " is " + c.what + " picture: no magick reader"
		if ev.Outcome != dispatch.NoProcessor || !strings.Contains(ev.Reason, want) {
			t.Errorf("%s: %s, %q; want no processor, %q", name, ev.Outcome, ev.Reason, want)
		}
	}
}
