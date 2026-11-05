//go:build linux

package email

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// Route returns the attachment's route and why, in a few words, such as "PDF signature". An
// embedded message is route.Nested. Any other attachment is routed by route.File over its
// content's file at Path: an attachment with no Path, read without an attachments directory or
// with no content file, and one whose file mail didn't write, are route.Unrouted. The name
// route.File reads the extension of is Filename's, else File's, the name to give office's
// Convert. It returns an error only when the file is there but can't be read.
func (a *Attachment) Route() (route.Route, string, error) {
	if a.Method == "embedded_message" {
		if a.Message == nil {
			return route.Nested, "embedded message, listed without its content", nil
		}
		return route.Nested, "embedded message, nested under its record", nil
	}
	switch {
	case a.File == "":
		return route.Unrouted, "no content file", nil
	case a.Path == "":
		return route.Unrouted, "not written: no attachments directory", nil
	}
	name := a.Filename
	if name == "" {
		name = a.File
	}
	r, why, err := route.File(a.Path, name, a.MimeType)
	if errors.Is(err, fs.ErrNotExist) {
		return route.Unrouted, "not written", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("email: routing %s: %w", a.File, err)
	}
	return r, why, nil
}
