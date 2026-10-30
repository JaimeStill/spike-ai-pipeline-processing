//go:build linux

package document_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// This file is the first pass of the staged flow, ruling N of task documents: the request the
// inference layer sends for a page without a text layer, and the reading of its answer. It is
// test code, a proof of concept the inference layer will follow, not a released API. The bake-off
// tier's flow runs on it (flow_bakeoff_test.go); the tests below check the request's pieces and
// the reading, so they stay in the check.
//
// The flow: a page with a text layer is read from it and never sent; a page without one is
// assessed, and its first pass returns, under a strict schema built from the correction catalog,
// the transcription, a judgement of the image's quality, and the corrections to apply; a second
// pass, only for a page the first flagged, sends the page corrected as the first pass asked with
// the plain transcription instructions.

// firstPassInstructions returns the first pass's instructions: transcribe the page as
// transcribeInstructions asks, judge the image, and choose corrections from the catalog, each
// listed with its String and Description, in the order the stage applies them.
func firstPassInstructions() string {
	var b strings.Builder
	b.WriteString("You are given one page image of a document and measurements of that image. " +
		"Answer in JSON with four fields.\n" +
		"transcription: every word on the page, in reading order, as plain text: no commentary, " +
		"no description of the page, and no Markdown or other formatting.\n" +
		"quality: \"good\" when the image is clear enough to transcribe accurately, \"poor\" when " +
		"defects in the image make it hard to read.\n" +
		"reason: one short sentence on why.\n" +
		"corrections: the image corrections, from the list below, that would make the page easier " +
		"to read on a second look at the corrected image; an empty list when the page reads well as " +
		"it is. Use the measurements and what you see.\n" +
		"The corrections:\n")
	for _, c := range document.Corrections() {
		fmt.Fprintf(&b, "- %s: %s\n", c, c.Description())
	}
	return b.String()
}

// firstPassSchema returns the first pass's JSON schema: every field required and no others, as
// strict structured output requires, quality one of good and poor, and corrections an array of
// the catalog's names.
func firstPassSchema() string {
	var names []string
	for _, c := range document.Corrections() {
		names = append(names, c.String())
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"transcription": map[string]any{"type": "string", "description": "The page's text in reading order."},
			"quality":       map[string]any{"type": "string", "enum": []string{"good", "poor"}},
			"reason":        map[string]any{"type": "string", "description": "One short sentence."},
			"corrections": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": names},
			},
		},
		"required":             []string{"transcription", "quality", "reason", "corrections"},
		"additionalProperties": false,
	}
	b, err := json.Marshal(schema)
	if err != nil {
		// A map of strings and slices of strings always marshals.
		panic(err)
	}
	return string(b)
}

// figures returns a page's Assessment as the text beside its image in the first pass: each
// measure with its scale, as the Assessment documents it.
func figures(a document.Assessment) string {
	gray := "no"
	if a.Grayscale {
		gray = "yes"
	}
	return fmt.Sprintf("Measurements of this page image: size %d x %d pixels; "+
		"skew %.2f degrees (0 is level); "+
		"contrast %.4f (the luminance's standard deviation, 0 to 0.5; faded print is lower); "+
		"noise %.4f (the mean difference from a 3x3 median, 0 to 1; speckle is higher); "+
		"sharpness %.4f (about 0.05 for a clean 150-dpi page of print; blur is lower, and speckle raises it); "+
		"colored %.4f of the pixels (grayscale: %s).",
		a.Width, a.Height, a.Skew, a.Contrast, a.Noise, a.Sharpness, a.Colored, gray)
}

// firstPass is the first pass's answer, as its schema requires it.
type firstPass struct {
	Transcription *string  `json:"transcription"`
	Quality       *string  `json:"quality"`
	Reason        *string  `json:"reason"`
	Corrections   []string `json:"corrections"`
}

// parseFirstPass reads a first pass's output text: its transcription, quality, and reason, the
// corrections it names that ParseCorrection reads, each once, in the order the stage applies
// them, and the names it doesn't read, which are counted and dropped. Output that isn't the
// schema's object, a field missing, is an error.
func parseFirstPass(out string) (fp firstPass, corrections []document.Correction, unknown []string, err error) {
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fp); err != nil {
		return firstPass{}, nil, nil, fmt.Errorf("first pass: %w", err)
	}
	if fp.Transcription == nil || fp.Quality == nil || fp.Reason == nil || fp.Corrections == nil {
		return firstPass{}, nil, nil, errors.New("first pass: a field is missing")
	}
	for _, name := range fp.Corrections {
		c, err := document.ParseCorrection(name)
		if err != nil {
			unknown = append(unknown, name)
			continue
		}
		corrections = append(corrections, c)
	}
	return fp, slices.Compact(slices.Sorted(slices.Values(corrections))), unknown, nil
}

// TestFirstPassInstructions checks the first pass's instructions list every correction of the
// catalog, with its description, in the catalog's order.
func TestFirstPassInstructions(t *testing.T) {
	got := firstPassInstructions()
	last := -1
	for _, c := range document.Corrections() {
		line := fmt.Sprintf("- %s: %s\n", c, c.Description())
		i := strings.Index(got, line)
		if i < 0 {
			t.Errorf("the instructions don't list %s as %q", c, line)
			continue
		}
		if i < last {
			t.Errorf("the instructions list %s out of the catalog's order", c)
		}
		last = i
	}
}

// TestFirstPassSchema checks the schema is strict structured output's: an object of the four
// fields, all required and no others, its corrections the catalog's names.
func TestFirstPassSchema(t *testing.T) {
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Enum  []string `json:"enum"`
			Items struct {
				Enum []string `json:"enum"`
			} `json:"items"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties *bool    `json:"additionalProperties"`
	}
	if err := json.Unmarshal([]byte(firstPassSchema()), &schema); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range document.Corrections() {
		names = append(names, c.String())
	}
	if schema.Type != "object" || len(schema.Properties) != 4 || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Errorf("the schema is %+v, want an object of four properties and no others", schema)
	}
	if !slices.Equal(slices.Sorted(slices.Values(schema.Required)), []string{"corrections", "quality", "reason", "transcription"}) {
		t.Errorf("the schema requires %v, want every field", schema.Required)
	}
	if got := schema.Properties["corrections"].Items.Enum; !slices.Equal(got, names) {
		t.Errorf("the corrections' names are %v, want the catalog's %v", got, names)
	}
	if got := schema.Properties["quality"].Enum; !slices.Equal(got, []string{"good", "poor"}) {
		t.Errorf("the quality's values are %v, want good and poor", got)
	}
}

// TestFigures checks the figures carry each measure of the Assessment.
func TestFigures(t *testing.T) {
	got := figures(document.Assessment{Width: 1275, Height: 1650, Skew: -2.5, Contrast: 0.3125, Noise: 0.0123,
		Sharpness: 0.0512, Colored: 0.0002, Grayscale: true})
	for _, want := range []string{"1275 x 1650", "skew -2.50", "contrast 0.3125", "noise 0.0123", "sharpness 0.0512", "colored 0.0002", "grayscale: yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("the figures %q don't hold %q", got, want)
		}
	}
}

// TestParseFirstPass checks a first pass's answer is read: its corrections parsed without regard
// to case, each once in the stage's order, the names that are none counted and dropped; and that
// output that isn't the schema's object is an error.
func TestParseFirstPass(t *testing.T) {
	fp, corrections, unknown, err := parseFirstPass(`{"transcription":"SECRET\nNOFORN","quality":"poor",` +
		`"reason":"The foot is faded.","corrections":["unsharp","Deskew","Sharpen","UNSHARP"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if *fp.Transcription != "SECRET\nNOFORN" || *fp.Quality != "poor" || *fp.Reason != "The foot is faded." {
		t.Errorf("the answer is %+v, want its fields", fp)
	}
	if want := []document.Correction{document.Deskew, document.Unsharp}; !slices.Equal(corrections, want) {
		t.Errorf("the corrections are %v, want %v", corrections, want)
	}
	if !slices.Equal(unknown, []string{"Sharpen"}) {
		t.Errorf("the unknown names are %v, want Sharpen", unknown)
	}
	if _, corrections, _, err := parseFirstPass(`{"transcription":"x","quality":"good","reason":"r","corrections":[]}`); err != nil || len(corrections) != 0 {
		t.Errorf("an empty list: %v, %v, want no corrections and no error", corrections, err)
	}
	for name, out := range map[string]string{
		"not JSON":           "The Harbor Survey",
		"cut off":            `{"transcription":"The Harbor`,
		"a field missing":    `{"transcription":"x","quality":"good","reason":"r"}`,
		"a field too many":   `{"transcription":"x","quality":"good","reason":"r","corrections":[],"page":1}`,
		"corrections null":   `{"transcription":"x","quality":"good","reason":"r","corrections":null}`,
		"transcription null": `{"transcription":null,"quality":"good","reason":"r","corrections":[]}`,
	} {
		if _, _, _, err := parseFirstPass(out); err == nil {
			t.Errorf("%s: parseFirstPass(%q) succeeded, want an error", name, out)
		}
	}
}
