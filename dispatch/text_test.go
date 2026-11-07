package dispatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// prepareText runs the text stage over content, written to a file named name, and returns its
// one output, its text as the output's file holds it, and the part's file.
func prepareText(t *testing.T, ctx context.Context, name string, content []byte) (dispatch.Output, string, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var outputs []dispatch.Output
	for item, err := range dispatch.NewTextStage().Prepare(ctx, dispatch.Part{Path: path, Name: name}, t.TempDir()) {
		if err != nil {
			return dispatch.Output{}, "", path, err
		}
		if item.Output == nil {
			t.Fatalf("%s: the stage found a part", name)
		}
		outputs = append(outputs, *item.Output)
	}
	if len(outputs) != 1 {
		t.Fatalf("%s: %d outputs, want 1", name, len(outputs))
	}
	o := outputs[0]
	if o.Kind != dispatch.TextOutput || o.Name != "text" || o.MediaType != "text/plain" || o.Value != nil {
		t.Errorf("%s: output %s %q %q, %d bytes inline", name, o.Kind, o.Name, o.MediaType, len(o.Value))
	}
	b, err := os.ReadFile(o.Path)
	if err != nil {
		t.Fatal(err)
	}
	return o, string(b), path, nil
}

// TestTextStage: the encoding is a byte order mark's, UTF-8's, UTF-32's before UTF-16's, either
// byte order, the mark dropped; else UTF-8 when the whole part is; else windows-1252, with a
// warning. Line endings are kept as stored. A byte the encoding can't decode becomes U+FFFD,
// counted in a warning, and a U+FFFD the text stored is not counted.
func TestTextStage(t *testing.T) {
	const fallback = "no byte order mark and not UTF-8: decoded as windows-1252, which may not be the text's encoding"
	cases := []struct {
		name, content, want, encoding string
		warnings                      []string
	}{
		{"ascii", "a,b\r\n1,2\n", "a,b\r\n1,2\n", "utf-8", nil},
		{"empty", "", "", "utf-8", nil},
		{"utf-8 storing U+FFFD", "café �\n", "café �\n", "utf-8", nil},
		{"utf-8 mark", "\xef\xbb\xbfcafé\r\n", "café\r\n", "utf-8 (byte order mark)", nil},
		{"utf-8 mark, invalid byte", "\xef\xbb\xbfa\xffb�", "a�b�", "utf-8 (byte order mark)",
			[]string{"1 U+FFFD replacing what utf-8 can't decode"}},
		{"utf-16le mark", "\xff\xfec\x00\xe9\x00\r\x00\n\x00", "cé\r\n", "utf-16le (byte order mark)", nil},
		{"utf-16be mark", "\xfe\xff\x00c\x00\xe9\x30\x42", "céあ", "utf-16be (byte order mark)", nil},
		{"utf-16le mark, unpaired surrogate and odd byte", "\xff\xfe\x00\xd8a\x00\xfd\xffb",
			"�a��", "utf-16le (byte order mark)",
			[]string{"2 U+FFFD replacing what utf-16le can't decode"}},
		{"utf-32le mark", "\xff\xfe\x00\x00c\x00\x00\x00\x00\xf6\x01\x00", "c\U0001f600", "utf-32le (byte order mark)", nil},
		{"utf-32be mark, past U+10FFFF", "\x00\x00\xfe\xff\x00\x00\x00c\x00\x11\x00\x00", "c�", "utf-32be (byte order mark)",
			[]string{"1 U+FFFD replacing what utf-32be can't decode"}},
		{"windows-1252", "caf\xe9 \x80 \x93q\x94\r\n", "café € “q”\r\n", "windows-1252 (fallback)",
			[]string{fallback}},
		{"windows-1252, undefined bytes", "a\x81b\x9d", "a�b�", "windows-1252 (fallback)",
			[]string{fallback, "2 U+FFFD replacing what windows-1252 can't decode"}},
		{"utf-8 then a stray byte", "café\xff", "cafÃ©ÿ", "windows-1252 (fallback)", []string{fallback}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, got, path, err := prepareText(t, t.Context(), "a.txt", []byte(c.content))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("text %q, want %q", got, c.want)
			}
			if o.Meta["encoding"] != c.encoding {
				t.Errorf("encoding %q, want %q", o.Meta["encoding"], c.encoding)
			}
			var warnings []string
			if w, ok := o.Meta["warnings"]; ok {
				warnings = strings.Split(w, "\n")
			}
			if !slices.Equal(warnings, c.warnings) {
				t.Errorf("warnings %q, want %q", warnings, c.warnings)
			}
			// Valid UTF-8 without a mark is the part's own file; anything else a file of the
			// stage's.
			if pass := c.encoding == "utf-8"; (o.Path == path) != pass {
				t.Errorf("output at %s, the part at %s", o.Path, path)
			}
		})
	}
}

// TestTextStageChunks: a rune split across the stage's 64 KiB reads is still UTF-8, and an
// invalid byte far past the first read still falls back.
func TestTextStageChunks(t *testing.T) {
	split := strings.Repeat("a", 64<<10-1) + "é\n"
	o, got, _, err := prepareText(t, t.Context(), "split.txt", []byte(split))
	if err != nil {
		t.Fatal(err)
	}
	if o.Meta["encoding"] != "utf-8" || got != split {
		t.Errorf("split rune: %q, %d bytes of %d", o.Meta["encoding"], len(got), len(split))
	}
	late := strings.Repeat("a", 200<<10) + "\xe9"
	o, got, _, err = prepareText(t, t.Context(), "late.txt", []byte(late))
	if err != nil {
		t.Fatal(err)
	}
	if o.Meta["encoding"] != "windows-1252 (fallback)" || got != late[:len(late)-1]+"é" {
		t.Errorf("late byte: %q, %d bytes", o.Meta["encoding"], len(got))
	}
}

// TestTextStageCanceled: a dispatch whose context has ended stops the stage's reading.
func TestTextStageCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := prepareText(t, ctx, "a.txt", []byte("caf\xe9")); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want context.Canceled", err)
	}
}

// TestTextDispatch: a part route.Text names, by its extension or its byte order mark, reaches the
// text stage through the dispatcher and is prepared, its one output the stage's, and nothing is
// left once the dispatch is done.
func TestTextDispatch(t *testing.T) {
	work := t.TempDir()
	d, err := dispatch.New(dispatch.Config{
		Stages: dispatch.Stages{route.Text: dispatch.NewTextStage()},
		Index:  dispatch.NewMemoryIndex(), Dir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"notes.csv": "caf\xe9;1\r\n", "notes": "\xff\xfec\x00a\x00f\x00\xe9\x00",
	} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		var text string
		events := dispatchAll(t, d, path, func(ev *dispatch.Event) {
			if ev.Output != nil {
				b, err := os.ReadFile(ev.Output.Path)
				if err != nil {
					t.Fatal(err)
				}
				text = string(b)
			}
		})
		ev := events[len(events)-1]
		if ev.Part.Route != route.Text || ev.Outcome != dispatch.Prepared || ev.Stage != dispatch.NewTextStage().Identity() || len(ev.Outputs) != 1 {
			t.Fatalf("%s: %s %s by %s, %d outputs", name, ev.Part.Route, ev.Outcome, ev.Stage, len(ev.Outputs))
		}
		if want := "café"; !strings.HasPrefix(text, want) {
			t.Errorf("%s: text %q, want it to begin %q", name, text, want)
		}
	}
	wantEmpty(t, work)
}

// TestTextDocs prepares ~/docs' encodings, read only, when this machine has them, and holds each
// to encodings.truth.json: the stage decides by a byte order mark, else UTF-8, else windows-1252,
// so its text is the truth for UTF-8 with or without a mark, UTF-16LE with one, windows-1252,
// with CRLF line endings too, and ISO-8859-1, whose printable text windows-1252 decodes alike;
// and garbled, the stage's known limit, since it detects no encoding, for UTF-16BE without a
// mark and for KOI8-R, EUC-JP, Shift_JIS, and windows-1256, each decoded as windows-1252.
func TestTextDocs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	dir := filepath.Join(home, "docs", "text", "encodings")
	b, err := os.ReadFile(filepath.Join(dir, "encodings.truth.json"))
	if err != nil {
		t.Skip("no ~/docs/text/encodings on this machine")
	}
	var truth map[string]struct {
		Encoding string `json:"encoding"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	const fallback = "windows-1252 (fallback)"
	want := map[string]struct {
		encoding string
		correct  bool
	}{
		"utf8-nobom.txt":          {"utf-8", true},
		"utf8-bom.txt":            {"utf-8 (byte order mark)", true},
		"utf16le-bom.txt":         {"utf-16le (byte order mark)", true},
		"windows-1252.txt":        {fallback, true},
		"crlf-windows-1252.txt":   {fallback, true},
		"iso-8859-1.txt":          {fallback, true},
		"koi8-r.txt":              {fallback, false},
		"euc-jp.txt":              {fallback, false},
		"shift-jis.txt":           {fallback, false},
		"windows-1256-arabic.txt": {fallback, false},
		"utf16be-nobom.txt":       {fallback, false},
	}
	if got := slices.Sorted(maps.Keys(truth)); !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("truth's files %q, want %q", got, slices.Sorted(maps.Keys(want)))
	}
	for _, name := range slices.Sorted(maps.Keys(truth)) {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		o, got, _, err := prepareText(t, t.Context(), name, content)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		correct := got == truth[name].Text
		t.Logf("%s (truth %s): decided %q, correct %t, warnings %q", name, truth[name].Encoding, o.Meta["encoding"], correct, o.Meta["warnings"])
		if w := want[name]; o.Meta["encoding"] != w.encoding || correct != w.correct {
			t.Errorf("%s: decided %q, correct %t; want %q, %t", name, o.Meta["encoding"], correct, w.encoding, w.correct)
		}
	}
}
