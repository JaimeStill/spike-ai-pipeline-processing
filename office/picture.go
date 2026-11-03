//go:build linux

package office

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNoMagick is the cause of the error ConvertPicture returns for a TIFF, EMF, or WMF when the
// Converter has no magick Tool. Nothing runs for it.
var ErrNoMagick = errors.New("no magick converter")

// PictureRequest is one picture's conversion to PNG outside a document, such as an attachment's.
type PictureRequest struct {
	// Dir is the working directory, which the caller owns: the picture is copied into a directory
	// of its own made in it, picture-*, which is removed when the conversion ends, and the PNG is
	// written into it and left. It is required; an empty Dir is refused rather than taken as the
	// system's temporary directory.
	Dir string
	// Path is the picture's file, which is read and never changed. Its kind, TIFF, EMF, or WMF,
	// is told by its content's signature, never by its name, which may carry any extension or
	// none.
	Path string
	// MetafileScale is the scale an EMF or WMF is rendered at, as Request's: zero is
	// DefaultMetafileScale, and it may be at most MaxMetafileScale. A TIFF ignores it.
	MetafileScale int
}

// The signatures ConvertPicture tells a picture's kind by, the ones package email routes a
// picture to office's conversion by. An EMF's signature is at offset 40, and a WMF's standard
// header is checked by its size and version as well as its type.
var (
	tiffLE       = []byte("II*\x00")
	tiffBE       = []byte("MM\x00*")
	emfSignature = []byte(" EMF")
	wmfPlaceable = []byte{0xd7, 0xcd, 0xc6, 0x9a}
)

// pictureHeadLen is the most of a picture ConvertPicture reads to tell its kind: enough for an
// EMF's signature at offset 40.
const pictureHeadLen = 64

// pictureKind returns the stored extension of the picture kind head, a file's first bytes,
// begins, ".tiff", ".emf", or ".wmf", by its signature; "" for any other content.
func pictureKind(head []byte) string {
	switch {
	case bytes.HasPrefix(head, tiffLE), bytes.HasPrefix(head, tiffBE):
		return ".tiff"
	case len(head) >= 44 && binary.LittleEndian.Uint32(head) == 1 && bytes.Equal(head[40:44], emfSignature):
		return ".emf"
	case bytes.HasPrefix(head, wmfPlaceable):
		return ".wmf"
	case len(head) >= 6:
		typ := binary.LittleEndian.Uint16(head)
		size := binary.LittleEndian.Uint16(head[2:])
		version := binary.LittleEndian.Uint16(head[4:])
		if (typ == 1 || typ == 2) && size == 9 && (version == 0x0300 || version == 0x0100) {
			return ".wmf"
		}
	}
	return ""
}

// ConvertPicture converts the picture at req.Path, a TIFF, EMF, or WMF told by its content's
// signature, to PNG, as Convert converts a document's picture of the kind, through the same
// code: an EMF or WMF rendered at req.MetafileScale through the LibreOffice converter and trimmed
// to its picture through magick, a TIFF's first image converted through magick. Each writes PNG,
// so nothing is encoded lossily, and without the time of the run, so a picture converted twice is
// the same bytes both times. It returns the PNG's absolute path: req.Dir's file named by the
// lowercase hex SHA-256 of the picture's bytes and .png, which an earlier conversion's PNG of the
// same picture is replaced by. A picture mail named by its SHA-256 thus gives the PNG of its own
// stem, as a document's picture does.
//
// The picture is copied into a directory of its own in req.Dir, under its SHA-256 and its
// kind's extension, so the runners, which mount req.Dir alone, see it wherever it is, and
// soffice reads its kind by its extension; the directory is removed when ConvertPicture returns,
// however it ends, and the file at req.Path is never changed.
//
// Content of no other kind is an error before anything runs, as is a picture whose converter
// the Converter wasn't given: an EMF or WMF without a LibreOffice converter matches
// ErrNoLibreOffice, and any of the three without magick ErrNoMagick. A MetafileScale below zero
// or above MaxMetafileScale is an error too. A conversion that fails, or writes no PNG, is an
// error with the converter's message, as Convert's is, and leaves no PNG. When ctx ends the run,
// the error matches ctx's, as every Tool run's does.
func (c *Converter) ConvertPicture(ctx context.Context, req PictureRequest) (string, error) {
	scale, err := metafileScale(req.MetafileScale)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if req.Dir == "" {
		return "", errors.New("office: no working directory")
	}
	dir, err := filepath.Abs(req.Dir)
	if err != nil {
		return "", fmt.Errorf("office: %w", err)
	}
	f, err := os.Open(req.Path)
	if err != nil {
		return "", fmt.Errorf("office: picture: %w", err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, pictureHeadLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("office: picture: %w", err)
	}
	head = head[:n]
	ext := pictureKind(head)
	name := filepath.Base(req.Path)
	switch {
	case ext == "":
		return "", fmt.Errorf("office: %s isn't a TIFF, EMF, or WMF picture", name)
	case ext != ".tiff" && c.libreoffice == nil:
		return "", fmt.Errorf("office: %s is %s picture: %w", name, kindName(ext), ErrNoLibreOffice)
	case c.magick == nil:
		return "", fmt.Errorf("office: %s is %s picture: %w", name, kindName(ext), ErrNoMagick)
	}
	work, err := os.MkdirTemp(dir, "picture-*")
	if err != nil {
		return "", fmt.Errorf("office: picture directory: %w", err)
	}
	png, err := c.convertPicture(ctx, dir, work, io.MultiReader(bytes.NewReader(head), f), ext, scale)
	if rerr := os.RemoveAll(work); rerr != nil {
		err = errors.Join(err, fmt.Errorf("office: removing the picture directory: %w", rerr))
		if png != "" {
			err = errors.Join(err, os.Remove(png))
		}
	}
	if err != nil {
		return "", err
	}
	return png, nil
}

// convertPicture copies the picture read from r, its kind's stored extension ext, into work, a
// directory in dir, the working directory, under its SHA-256 and ext, converts it through
// convertPictures, metafiles rendered at scale, and moves the PNG it wrote beside the copy into
// dir. It returns the PNG's path once it is in dir, with any error.
func (c *Converter) convertPicture(ctx context.Context, dir, work string, r io.Reader, ext string, scale int) (string, error) {
	copied := filepath.Join(work, "picture"+ext)
	out, err := os.OpenFile(copied, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("office: picture: %w", err)
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, sum), r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("office: picture: %w", err)
	}
	stem := hex.EncodeToString(sum.Sum(nil))
	stored := filepath.Join(work, stem+ext)
	if err := os.Rename(copied, stored); err != nil {
		return "", fmt.Errorf("office: picture: %w", err)
	}
	media := []MediaFile{{Name: stem + ext, ContentType: ContentType(ext), File: stored}}
	if err := c.convertPictures(ctx, dir, scale, media); err != nil {
		return "", err
	}
	png := filepath.Join(dir, stem+".png")
	if err := os.Rename(media[0].File, png); err != nil {
		return "", fmt.Errorf("office: picture: %w", err)
	}
	return png, nil
}

// kindName returns the name of the picture kind whose stored extension is ext, with its article:
// a TIFF, an EMF, or a WMF.
func kindName(ext string) string {
	return map[string]string{".tiff": "a TIFF", ".emf": "an EMF", ".wmf": "a WMF"}[ext]
}
