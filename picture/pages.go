//go:build linux

package picture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// tiffPages converts r's picture, a TIFF, to one PNG per page, as ConvertPages says: its copy
// is staged in a picture-* directory in r.dir, as ConvertBatch stages a picture, which is
// removed however the conversion ends, and the pages written are removed when it fails.
func (c *Converter) tiffPages(ctx context.Context, r request) ([]string, error) {
	if c.magick == nil {
		return nil, fmt.Errorf("picture: %s is %s picture: %w", r.name, kinds[route.TIFF].name, ErrNoMagick)
	}
	work, err := os.MkdirTemp(r.dir, "picture-*")
	if err != nil {
		return nil, fmt.Errorf("picture: picture directory: %w", err)
	}
	var written []string
	err = c.convertPages(ctx, r, work, &written)
	if rerr := os.RemoveAll(work); rerr != nil {
		err = errors.Join(err, fmt.Errorf("picture: removing the picture directory: %w", rerr))
	}
	if err != nil {
		for _, png := range written {
			err = errors.Join(err, os.Remove(png))
		}
		return nil, err
	}
	return written, nil
}

// convertPages copies r's TIFF into a directory of its own in work, has magick write each of its
// images there as the PNG pagePattern names, in a run whose working directory is that directory,
// so the pattern is relative and nothing in r.dir's path is read as one, and moves each page's PNG
// to r.dir's PNG pageName names, appending each moved PNG to written.
func (c *Converter) convertPages(ctx context.Context, r request, work string, written *[]string) error {
	s, err := stage(work, 0, Picture{Path: r.path, Kind: route.TIFF, PNG: filepath.Join(r.dir, r.sum+".png")})
	if err != nil {
		return err
	}
	own := filepath.Dir(s.copied)
	if err := c.runMagick(ctx, own, s.name, pageArgs(s, r.sum), filepath.Join(own, pageOutput(r.sum, 0))); err != nil {
		return err
	}
	n, err := countPages(own, s.name, r.sum)
	if err != nil {
		return fmt.Errorf("picture: magick: %s: %w", s.name, err)
	}
	for i := range n {
		png := filepath.Join(r.dir, pageName(r.sum, i+1))
		if err := os.Rename(filepath.Join(own, pageOutput(r.sum, i)), png); err != nil {
			return fmt.Errorf("picture: %s: %w", s.name, err)
		}
		*written = append(*written, png)
	}
	return nil
}

// pageArgs are magick's arguments for s, a TIFF whose SHA-256 is sum, converted page by page:
// every image it holds, read as TIFF whatever its content, each written to a PNG of its own,
// named by pagePattern, which +adjoin and the pattern's %d, the image's index from 0, ask for.
func pageArgs(s staged, sum string) []string {
	return slices.Concat([]string{"tiff:" + s.copied, "+adjoin"}, reproducible, []string{"png:" + sum + pagePattern})
}

// pagePattern follows a TIFF's SHA-256 in the name magick writes each of its pages to, in the
// directory of the TIFF's copy: magick puts the image's index from 0 for %d.
const pagePattern = "-%d.png"

// pageOutput returns the name magick writes the page at index, from 0, of the TIFF whose
// SHA-256 is sum to, as pagePattern says.
func pageOutput(sum string, index int) string {
	return sum + "-" + strconv.Itoa(index) + ".png"
}

// pageName returns the name of the PNG of page, from 1, of the TIFF whose SHA-256 is sum: the
// SHA-256, "-page", the page's number, and .png.
func pageName(sum string, page int) string {
	return sum + "-page" + strconv.Itoa(page) + ".png"
}

// countPages returns how many pages magick wrote into own, the directory of the TIFF's copy,
// named copied, whose SHA-256 is sum: each a PNG named by pageOutput, their indexes running from
// 0 without a gap. A file of any other name, a gap, or a page that isn't a PNG is an error.
func countPages(own, copied, sum string) (int, error) {
	list, err := os.ReadDir(own)
	if err != nil {
		return 0, err
	}
	output := regexp.MustCompile(`^` + regexp.QuoteMeta(sum) + `-(0|[1-9][0-9]*)\.png$`)
	seen := map[int]bool{}
	for _, e := range list {
		if e.Name() == copied {
			continue
		}
		m := output.FindStringSubmatch(e.Name())
		if m == nil {
			return 0, fmt.Errorf("wrote %s, which isn't a page", e.Name())
		}
		i, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("wrote %s, which isn't a page", e.Name())
		}
		if err := checkPNG(filepath.Join(own, e.Name())); err != nil {
			return 0, err
		}
		seen[i] = true
	}
	for i := range len(seen) {
		if !seen[i] {
			return 0, fmt.Errorf("wrote %d pages without page %d", len(seen), i+1)
		}
	}
	return len(seen), nil
}
