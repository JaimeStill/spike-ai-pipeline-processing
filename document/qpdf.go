//go:build linux

package document

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// QPDFMinimum is the oldest qpdf the package accepts: 11.0, the first whose JSON is version 2,
// whose string encodings Form reads.
const QPDFMinimum = "11.0"

// QPDFSpec declares qpdf, minimum 11.0, for either runner's constructor. qpdf --version prints
// "qpdf version 12.4.2" and a line on its license, and the pattern takes the dotted numbers after
// "qpdf version". A caller that needs another program path or a KillAfter period sets them on the
// Spec returned.
func QPDFSpec() tool.Spec {
	return tool.Spec{
		Program:        "qpdf",
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`qpdf version (\d+(?:\.\d+)*)`),
		Minimum:        QPDFMinimum,
	}
}

// QPDF reads what a PDF's structure says before it is rendered, through qpdf: whether it needs a
// password to open, and its AcroForm's fields. Each method is one qpdf run, which reads the PDF by
// its absolute path in the PDF's directory, so either runner's Tool sees the same file, and writes
// nothing. Its methods are safe for concurrent use.
type QPDF struct {
	qpdf *tool.Tool
}

// NewQPDF builds a QPDF over qpdf, which the caller built through either runner, typically from
// QPDFSpec. It refuses a nil Tool, and one older than QPDFMinimum, whatever minimum its own Spec
// declared, with a *tool.VersionError wrapping tool.ErrTooOld.
func NewQPDF(qpdf *tool.Tool) (*QPDF, error) {
	if qpdf == nil {
		return nil, errors.New("document: qpdf: no qpdf tool")
	}
	if err := atLeast(qpdf, QPDFMinimum); err != nil {
		return nil, err
	}
	return &QPDF{qpdf: qpdf}, nil
}

// atLeast refuses t when its version is older than minimum, with a *tool.VersionError wrapping
// tool.ErrTooOld.
func atLeast(t *tool.Tool, minimum string) error {
	m, err := tool.ParseVersion(minimum)
	if err != nil {
		return err
	}
	if v := t.Version(); v.Compare(m) < 0 {
		return &tool.VersionError{Program: t.Name(), Found: v.String(), Minimum: minimum, Err: tool.ErrTooOld}
	}
	return nil
}

// stderrLimit is how much of a tool's standard error a qpdf or pdfdetach run keeps for its error.
const stderrLimit = 16 << 10

// formLimit is the most JSON a Form run may write: an AcroForm past it fails rather than being
// held in memory.
const formLimit = 64 << 20

// RequiresPassword reports whether the PDF at pdf needs a password to open, through qpdf
// --requires-password, which opens it with no password: exit 0 when it needs one, 2 when it isn't
// encrypted, and 3 when it is encrypted but opens without one, as a PDF restricted only by its
// permissions does. qpdf exits 2 too when it can't open the file at all; such a run, whose
// standard error holds a line other than a warning, fails with qpdf's message.
func (q *QPDF) RequiresPassword(ctx context.Context, pdf string) (bool, error) {
	abs, err := filepath.Abs(pdf)
	if err != nil {
		return false, fmt.Errorf("document: qpdf: %w", err)
	}
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	err = q.qpdf.Run(ctx, tool.Invocation{
		Args: []string{"--requires-password", abs}, Dir: filepath.Dir(abs), Stderr: stderr,
	})
	exit, ok := errors.AsType[*tool.ExitError](err)
	switch {
	case err == nil:
		return true, nil
	case !ok:
		return false, fmt.Errorf("document: qpdf: %w", err)
	case exit.Code == 3:
		return false, nil
	case exit.Code == 2:
		if message := qpdfMessage(stderr.String()); message != "" {
			return false, fmt.Errorf("document: qpdf: %s: %w", message, err)
		}
		return false, nil
	}
	return false, qpdfError(stderr.String(), err)
}

// Form returns the AcroForm of the PDF at pdf, through qpdf's JSON, --json --json-key=acroform:
// each terminal field's full name, type, and value, in the AcroForm's order, a radio group's
// buttons as one field. A PDF with no AcroForm, or one with no fields, has a Form with none. A run
// qpdf ends with warnings, exit code 3, has succeeded. JSON past 64 MiB fails.
func (q *QPDF) Form(ctx context.Context, pdf string) (Form, error) {
	abs, err := filepath.Abs(pdf)
	if err != nil {
		return Form{}, fmt.Errorf("document: qpdf: %w", err)
	}
	var stdout bytes.Buffer
	stderr := &cli.LimitedBuffer{Limit: stderrLimit}
	err = q.qpdf.Run(ctx, tool.Invocation{
		Args: []string{"--json=2", "--json-key=acroform", abs}, Dir: filepath.Dir(abs),
		Stdout: &capped{w: &stdout, limit: formLimit}, Stderr: stderr,
	})
	if exit, ok := errors.AsType[*tool.ExitError](err); err != nil && (!ok || exit.Code != 3) {
		if ok {
			return Form{}, qpdfError(stderr.String(), err)
		}
		return Form{}, fmt.Errorf("document: qpdf: %w", err)
	}
	f, err := parseForm(stdout.Bytes())
	if err != nil {
		return Form{}, fmt.Errorf("document: qpdf: form of %s: %w", filepath.Base(abs), err)
	}
	return f, nil
}

// qpdfError returns a failed run's error, naming qpdf's message when its stderr holds one.
func qpdfError(stderr string, err error) error {
	if message := qpdfMessage(stderr); message != "" {
		return fmt.Errorf("document: qpdf: %s: %w", message, err)
	}
	return fmt.Errorf("document: qpdf: %w", err)
}

// qpdfMessage returns qpdf's stderr lines that aren't warnings, joined by "; ": its error.
func qpdfMessage(stderr string) string {
	var lines []string
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "WARNING: ") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "; ")
}

// capped is a writer that refuses the write that would take it past limit bytes, so a run whose
// output is too large stops.
type capped struct {
	w     io.Writer
	n     int
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.n+len(p) > c.limit {
		return 0, fmt.Errorf("output past %d bytes", c.limit)
	}
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

// Form is a PDF's AcroForm: its terminal fields, in the AcroForm's order, as qpdf reads them.
type Form struct {
	Fields []FormField `json:"fields"`
}

// The field types a FormField names.
const (
	TextField       = "text"
	CheckboxField   = "checkbox"
	RadioField      = "radio"
	ChoiceField     = "choice"
	PushbuttonField = "pushbutton"
	SignatureField  = "signature"
)

// FormField is one terminal field of an AcroForm: its full name, its parents' partial names and
// its own joined by dots; its type, one of the field types, or the PDF's own field type, such as
// "Btn", for one none names; and its value.
//
// Value is the field's value when it is one string or name: a text field's text; a checkbox's or a
// radio group's state, the name of the appearance it shows, such as "Yes" or "Off"; and a choice's
// value when one is selected. A name is given without its slash, and a string as text, a binary
// one with any bytes that aren't UTF-8 replaced. Checked, for a checkbox or a radio group, says
// whether its state is other than "Off". For a choice, Options are its options as qpdf gives them,
// each option's display text, and Selected its selected values, its export values, one or more; a
// choice with several selected has no Value.
type FormField struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Value    string   `json:"value,omitzero"`
	Checked  *bool    `json:"checked,omitzero"`
	Options  []string `json:"options,omitzero"`
	Selected []string `json:"selected,omitzero"`
}

// qpdfForm is the part of qpdf's JSON Form reads.
type qpdfForm struct {
	Version  int `json:"version"`
	AcroForm struct {
		HasAcroForm bool `json:"hasacroform"`
		Fields      []struct {
			FullName      string          `json:"fullname"`
			FieldType     string          `json:"fieldtype"`
			FieldFlags    int64           `json:"fieldflags"`
			IsText        bool            `json:"istext"`
			IsCheckbox    bool            `json:"ischeckbox"`
			IsRadioButton bool            `json:"isradiobutton"`
			IsChoice      bool            `json:"ischoice"`
			Choices       []string        `json:"choices"`
			Value         json.RawMessage `json:"value"`
		} `json:"fields"`
	} `json:"acroform"`
}

// pushbutton is a button field's Pushbutton flag, bit 17 of its field flags.
const pushbutton = 1 << 16

// parseForm reads qpdf's JSON, version 2, into a Form. qpdf lists a field once for each of its
// widgets, so a radio group's buttons, each a widget of the one field, are one field here, at
// its first button's place.
func parseForm(b []byte) (Form, error) {
	var q qpdfForm
	if err := json.Unmarshal(b, &q); err != nil {
		return Form{}, err
	}
	if q.Version != 2 {
		return Form{}, fmt.Errorf("qpdf JSON version %d, want 2", q.Version)
	}
	f := Form{Fields: []FormField{}}
	if !q.AcroForm.HasAcroForm {
		return f, nil
	}
	seen := map[string]bool{}
	for _, qf := range q.AcroForm.Fields {
		if seen[qf.FullName] {
			continue
		}
		seen[qf.FullName] = true
		field := FormField{Name: qf.FullName}
		values, err := qpdfValues(qf.Value)
		if err != nil {
			return Form{}, fmt.Errorf("field %q: %w", qf.FullName, err)
		}
		if len(values) == 1 {
			field.Value = values[0]
		}
		switch {
		case qf.IsText:
			field.Type = TextField
		case qf.IsCheckbox, qf.IsRadioButton:
			field.Type = CheckboxField
			if qf.IsRadioButton {
				field.Type = RadioField
			}
			field.Checked = new(field.Value != "" && field.Value != "Off")
		case qf.IsChoice:
			field.Type = ChoiceField
			field.Options = qf.Choices
			field.Selected = values
		case qf.FieldType == "/Btn" && qf.FieldFlags&pushbutton != 0:
			field.Type = PushbuttonField
		case qf.FieldType == "/Sig":
			field.Type = SignatureField
		default:
			field.Type = strings.TrimPrefix(qf.FieldType, "/")
		}
		f.Fields = append(f.Fields, field)
	}
	return f, nil
}

// qpdfValues returns a field's value, as qpdf's JSON version 2 gives it, as text: null as none; a
// string or a name as one; an array as each of its strings and names. A string is "u:" and its
// text, or "b:" and its bytes in hex; a name is "/" and its text, or "n:/" and its text where it
// isn't UTF-8.
func qpdfValues(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		v, err := qpdfText(one)
		if err != nil {
			return nil, err
		}
		return []string{v}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("value %s: neither a string, a name, nor an array of them", raw)
	}
	values := make([]string, 0, len(many))
	for _, s := range many {
		v, err := qpdfText(s)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}

// qpdfText returns the text of one string or name in qpdf's JSON version 2.
func qpdfText(s string) (string, error) {
	switch {
	case strings.HasPrefix(s, "u:"):
		return s[2:], nil
	case strings.HasPrefix(s, "b:"):
		b, err := hex.DecodeString(s[2:])
		if err != nil {
			return "", fmt.Errorf("binary string %q: %w", s, err)
		}
		return strings.ToValidUTF8(string(b), "\uFFFD"), nil
	case strings.HasPrefix(s, "n:/"):
		return s[3:], nil
	case strings.HasPrefix(s, "/"):
		return s[1:], nil
	}
	return "", fmt.Errorf("value %q: neither a string nor a name", s)
}
