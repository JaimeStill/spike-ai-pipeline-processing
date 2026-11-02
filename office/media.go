//go:build linux

package office

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MediaFile is one file in a conversion's media directory: a picture the document embeds, each
// distinct content once.
type MediaFile struct {
	// Name is office's name for the file, the lowercase hex SHA-256 of its bytes and its stored
	// extension, as the JSON model's Media and the Markdown's image links give it.
	Name string
	// ContentType is the file's content type, by its stored extension, as ContentType says.
	ContentType string
	// File is the absolute path of the file handed onward, the stored file itself unless it was
	// converted; "" when none is.
	File string
	// Converted is whether File is a conversion of the stored file rather than the file itself.
	Converted bool
}

// contentTypes are the content types of the extensions office's media are stored with, as
// Office's own packages type them.
var contentTypes = map[string]string{
	".bmp":  "image/bmp",
	".emf":  "image/x-emf",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".wdp":  "image/vnd.ms-photo",
	".wmf":  "image/x-wmf",
}

// ContentType returns the content type of a media file by its name's extension, without regard
// to case, as Office's own packages type it; application/octet-stream for an extension it
// doesn't know. A JSON model's Media gives the content type the package stored, which agrees
// for every extension ContentType knows.
func ContentType(name string) string {
	if t, ok := contentTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// listMedia lists the regular files in dir, the media directory, in name order, each handed on
// as stored.
func listMedia(dir string) ([]MediaFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("office: media directory: %w", err)
	}
	var media []MediaFile
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		media = append(media, MediaFile{
			Name:        e.Name(),
			ContentType: ContentType(e.Name()),
			File:        filepath.Join(dir, e.Name()),
		})
	}
	return media, nil
}
