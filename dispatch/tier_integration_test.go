//go:build linux && integration

package dispatch_test

import (
	"bufio"
	"cmp"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// The integration tier's run of every fixture through Dispatch against the real stages: office
// over the office CLI, LibreOffice in its pinned image for the legacy formats, and magick; the
// document stage over gs; the email stage over mail; the onenote stage over onenote; the picture
// stage over magick and LibreOffice; the vision stage; and the zip stage. Each route is held to
// testdata/tier-routes.tsv, the hand route truth, and a sample of the image outputs is sent to
// Gemma 4 26B-A4B on the router SPIKE_ROUTER_URL names, which set A loads.

const (
	// tierRoutes is the hand route truth, as its header says.
	tierRoutes = "testdata/tier-routes.tsv"
	// tierModel transcribes the sampled image outputs, as it transcribes document's pages.
	tierModel = "google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0"
	// tierPrompt is the text beside each sampled image: a picture with no text still gets a
	// sentence, so every response has text.
	tierPrompt = "Transcribe the text in this picture. If it has no text, describe it in one sentence."
	// tierOutputCap bounds a transcription's output; a page runs well under it, and a model that
	// loops on its text stops at it.
	tierOutputCap = 2048
	// tierLogEnv names the file the full routes-to-stages log is written to, when it is set.
	tierLogEnv = "DISPATCH_TIER_LOG"
)

// tierInputs are the committed fixture inputs, as globs under the repository's root, in the
// order the tier dispatches them, each glob's matches sorted, but for a README.md or a Go file,
// which is no fixture: every input the format packages' fixtures hold, the inspect package's
// hazards, and the tool package's WAV. A notebook is its .onetoc2, which onenote reads with the
// sections beside it.
var tierInputs = []string{
	"document/testdata/*.pdf",
	"office/testdata/ooxml/*",
	"office/testdata/legacy/generated/*.*.*",
	"office/testdata/legacy/real/*.???",
	"office/testdata/pictures/*.docx",
	"picture/testdata/*.svg",
	"email/testdata/mail/fixtures/generated/eml/*.eml",
	"email/testdata/mail/fixtures/generated/msg/*.msg",
	"email/testdata/mail/fixtures/generated/pst/*.pst",
	"email/testdata/mail/fixtures/mime/eml/*.eml",
	"email/testdata/mail/fixtures/mime/*.mboxrd",
	"email/testdata/mail/fixtures/mime/rlug/mbox.*",
	"email/testdata/mail/fixtures/msg/poi/*.msg",
	"email/testdata/mail/fixtures/msg/tika/*.msg",
	"email/testdata/mail/fixtures/pst/*/*.pst",
	"email/testdata/built/eml/*.eml",
	"email/testdata/built/msg/*.msg",
	"onenote/testdata/onenote/fixtures/interop/*/*.one*",
	"onenote/testdata/onenote/fixtures/onenote-rs/*.one*",
	"onenote/testdata/onenote/fixtures/onenote-rs/*/*.one*",
	"inspect/testdata/*",
	"tool/testdata/*.wav",
}

// truthRow is one row of the hand route truth, as tierRoutes' header says.
type truthRow struct {
	line    int
	input   string
	place   string
	name    string
	route   route.Route
	outcome dispatch.Outcome
}

// readTruth reads the hand route truth.
func readTruth(t *testing.T) []truthRow {
	t.Helper()
	f, err := os.Open(tierRoutes)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var rows []truthRow
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 5 {
			t.Fatalf("%s:%d: %d columns, want 5", tierRoutes, n, len(cols))
		}
		rows = append(rows, truthRow{n, cols[0], cols[1], cols[2], route.Route(cols[3]), dispatch.Outcome(cols[4])})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// glob reports whether s matches pattern, in which "*" matches any run of characters, "/" among
// them, and every other character itself.
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// tierEvent is a part's own event as the tier logs it: the input it came from, under the
// repository's root, its place, each Where from the input's child down to it joined by " > ",
// "-" for the input, and the event.
type tierEvent struct {
	input string
	place string
	ev    dispatched
}

// places returns events, one input's parts' events in the post-order Dispatch yields them, with
// each part's place: a part's parent's event is the first after it one level up.
func places(input string, events []dispatched) []tierEvent {
	out := make([]tierEvent, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		place := "-"
		if ev.Part.Depth > 0 {
			place = ev.Part.Where
			for j := i + 1; j < len(events); j++ {
				if events[j].Part.Depth == ev.Part.Depth-1 {
					if out[j].place != "-" {
						place = out[j].place + " > " + place
					}
					break
				}
			}
		}
		out[i] = tierEvent{input, place, ev}
	}
	return out
}

// sampled is an image output sent to the model: the input and part it came from, its output's
// name and media type, its bytes, and its key, the stage that wrote it and the part's kind.
type sampled struct {
	key, input, where, part, output, mediaType string
	data                                       []byte
}

// sampleKey returns the key an image output is sampled by, one image a key: the stage that
// prepared it and its part's kind, a page's ink apart from the other SVGs, and a picture office
// converted to PNG apart from a stored PNG, by the format it was converted from.
func sampleKey(ev dispatch.Event) string {
	key := ev.Stage.Name + " " + string(ev.Part.Kind)
	if strings.HasSuffix(ev.Part.Where, ", page ink") {
		key = ev.Stage.Name + " ink"
	}
	if where, ok := strings.CutSuffix(ev.Part.Where, ", as PNG"); ok {
		key += " from " + strings.TrimPrefix(filepath.Ext(where), ".")
	}
	return key
}

// sampleSide is the least width and height an image output is sampled at: a smaller one, such
// as a 2x2 placeholder, says little of the model's reading.
const sampleSide = 32

// sampleable reports whether data is an image the model is sent: one that decodes, at least
// sampleSide on each side.
func sampleable(data []byte) bool {
	var w, h int
	if _, err := fmt.Sscanf(visiontest.PixelSize(data), "%dx%d", &w, &h); err != nil {
		return false
	}
	return w >= sampleSide && h >= sampleSide
}

// TestTierRoutes dispatches every committed fixture input, tierInputs in order, through every
// adapter, over one file index, so content an input shares with one dispatched before it is
// seen. It holds each part's route and outcome to the hand route truth: every row must match an
// event, and every event a row matches must have its route and outcome; every input must have a
// row of its own, and a failed part one too. It then sends one prepared image output per stage
// and part kind, as sampleKey keys them, the first that decodes at least sampleSide on each
// side, to tierModel, and asserts each response is completed with text, recording its model,
// image tokens, and wall time.
//
// It logs every part's event, one line each, its counts by route and outcome, and the vision
// rows; with DISPATCH_TIER_LOG set, it writes the same log to the file that names.
func TestTierRoutes(t *testing.T) {
	truth := readTruth(t)
	stages := allStages(t)
	zs, err := dispatch.NewZipStage(dispatch.ZipConfig{})
	if err != nil {
		t.Fatal(err)
	}
	stages[route.Archive] = zs
	index, err := dispatch.NewFileIndex(t.TempDir(), dispatch.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := newDispatcher(t, stages, index)

	var inputs []string
	for _, pattern := range tierInputs {
		matches, err := filepath.Glob(filepath.Join("..", pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			t.Fatalf("%s matches no fixture", pattern)
		}
		for _, m := range matches {
			if filepath.Base(m) != "README.md" && filepath.Ext(m) != ".go" {
				inputs = append(inputs, m)
			}
		}
	}

	var log strings.Builder
	logf := func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) }
	logf("# routes to stages: %d inputs from %s, dispatched in order over one file index", len(inputs), strings.Join(tierInputs, " "))
	logf("# input\tplace\tname\tdepth\troute\treason\tstage\toutcome\toutputs\tchildren\twhy")

	start := time.Now()
	var all []tierEvent
	samples := map[string]*sampled{}
	var sampleOrder []string
	for _, path := range inputs {
		input := strings.TrimPrefix(filepath.ToSlash(path), "../")
		began := time.Now()
		events := dispatchAll(t, d, path, func(ev *dispatch.Event) {
			o := ev.Output
			if o == nil || o.Kind != dispatch.ImageOutput || ev.Outcome != "" {
				return
			}
			key := sampleKey(*ev)
			if _, ok := samples[key]; ok {
				return
			}
			data, err := os.ReadFile(o.Path)
			if err != nil {
				t.Errorf("%s: %s's %s: %v", input, ev.Part.Name, o.Name, err)
				return
			}
			if !sampleable(data) {
				return
			}
			samples[key] = &sampled{key, input, ev.Part.Where, ev.Part.Name, o.Name, o.MediaType, data}
			sampleOrder = append(sampleOrder, key)
		})
		placed := places(input, events)
		all = append(all, placed...)
		for _, e := range placed {
			ev := e.ev
			why := ""
			if ev.Outcome != dispatch.Prepared {
				why = ev.Reason
			}
			logf("%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%d\t%d\t%s", input, e.place, ev.Part.Name, ev.Part.Depth,
				ev.Part.Route, ev.Part.Reason, cmp.Or(ev.Stage.Name, "-"), ev.Outcome, ev.Event.Outputs, ev.Children, why)
		}
		t.Logf("%s: %d parts, %v", input, len(events), time.Since(began).Round(time.Millisecond))
	}
	took := time.Since(start)

	checkTruth(t, truth, inputs, all)

	counts := map[string]int{}
	for _, e := range all {
		counts[string(e.ev.Part.Route)+" × "+string(e.ev.Outcome)]++
	}
	logf("")
	logf("# counts by route × outcome: %d parts from %d inputs, dispatched in %v", len(all), len(inputs), took.Round(time.Millisecond))
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		logf("%s\t%d", k, counts[k])
	}

	logf("")
	logf("# vision: one prepared image output per stage and part kind, the first that decodes at least %d on each side, to %s", sampleSide, tierModel)
	logf("# key\tinput\twhere\tpart\toutput\tmedia type\tsize\tbytes\tmodel\timage tokens\toutput tokens\twall\tstatus\ttranscription")
	tierVision(t, samples, sampleOrder, logf)

	t.Log("\n" + log.String())
	if path := os.Getenv(tierLogEnv); path != "" {
		if err := os.WriteFile(path, []byte(log.String()), 0o644); err != nil {
			t.Error(err)
		}
	}
}

// checkTruth holds all, every input's events, to truth, as TestTierRoutes says.
func checkTruth(t *testing.T, truth []truthRow, inputs []string, all []tierEvent) {
	t.Helper()
	tops := map[string]bool{}
	matched := make([]bool, len(all))
	for _, row := range truth {
		if row.place == "-" {
			tops[row.input] = true
		}
		n := 0
		for i, e := range all {
			if e.input != row.input || !glob(row.place, e.place) || !glob(row.name, e.ev.Part.Name) {
				continue
			}
			n++
			matched[i] = true
			if e.ev.Part.Route != row.route || e.ev.Outcome != row.outcome {
				t.Errorf("%s:%d: %s at %q, %s: %s %s (%s), want %s %s", tierRoutes, row.line, row.input, e.place,
					e.ev.Part.Name, e.ev.Part.Route, e.ev.Outcome, e.ev.Reason, row.route, row.outcome)
			}
		}
		if n == 0 {
			t.Errorf("%s:%d: no event of %s at %q named %q", tierRoutes, row.line, row.input, row.place, row.name)
		}
	}
	for _, path := range inputs {
		if input := strings.TrimPrefix(filepath.ToSlash(path), "../"); !tops[input] {
			t.Errorf("%s has no row in %s", input, tierRoutes)
		}
	}
	for i, e := range all {
		if e.ev.Outcome == dispatch.Failed && !matched[i] {
			t.Errorf("%s at %q, %s: failed, and no row expects it: %s", e.input, e.place, e.ev.Part.Name, e.ev.Reason)
		}
	}
}

// tierVision sends each sample to tierModel beside tierPrompt, one at a time, so each wall time
// is the image's alone, and logs a row of each by logf. Image tokens are the request's input
// tokens less those of the same request without the image, as office's TestVisionPictures
// measures them.
func tierVision(t *testing.T, samples map[string]*sampled, order []string, logf func(string, ...any)) {
	t.Helper()
	if len(order) == 0 {
		t.Fatal("no image output to sample")
	}
	c := visiontest.Router(t)
	ask := func(maxOut int, native string, parts ...protocol.ContentPart) (*protocol.Response, time.Duration, error) {
		start := time.Now()
		resp, err := c.Generate(t.Context(), &protocol.Request{
			Model:           tierModel,
			Input:           []protocol.Item{protocol.Message{Role: protocol.RoleUser, Content: parts}},
			Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortNone},
			MaxOutputTokens: maxOut,
			Native:          jsontext.Value(native),
		})
		return resp, time.Since(start), err
	}
	prompt := protocol.InputText{Text: tierPrompt}
	baseline, _, err := ask(0, `{"temperature":0,"max_completion_tokens":1}`, prompt)
	if err != nil {
		t.Fatalf("the text-only baseline: %v", err)
	}
	without := baseline.Usage.InputTokens

	var wall time.Duration
	var imageTokens int
	for _, key := range order {
		s := samples[key]
		size := visiontest.PixelSize(s.data)
		resp, elapsed, err := ask(tierOutputCap, `{"temperature":0}`, prompt, protocol.InputImage{ImageURL: protocol.DataURL(s.mediaType, s.data)})
		if err != nil {
			t.Errorf("%s: %s's %s, %s %s: %v", key, s.input, s.output, s.mediaType, size, err)
			logf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t-\t-\t%v\tfailed: %v\t", key, s.input, cmp.Or(s.where, "-"), s.part,
				s.output, s.mediaType, size, len(s.data), tierModel, elapsed.Round(time.Millisecond), err)
			continue
		}
		out := strings.TrimSpace(visiontest.ResponseText(resp))
		if resp.Status != protocol.StatusCompleted || out == "" {
			t.Errorf("%s: %s's %s: the response is %s with %q, want completed with text", key, s.input, s.output, visiontest.ResponseStatus(resp), out)
		}
		img := resp.Usage.InputTokens - without
		wall += elapsed
		imageTokens += img
		logf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%d\t%d\t%v\t%s\t%.120q", key, s.input, cmp.Or(s.where, "-"), s.part,
			s.output, s.mediaType, size, len(s.data), tierModel, img, resp.Usage.OutputTokens, elapsed.Round(time.Millisecond),
			visiontest.ResponseStatus(resp), out)
	}
	logf("# %d images, %v wall, %d image tokens; text-only baseline %d input tokens", len(order), wall.Round(time.Millisecond), imageTokens, without)
}
