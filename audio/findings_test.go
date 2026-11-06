//go:build findings && linux

package audio_test

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/audio"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/audiotest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/corpustest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/textscore"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/visiontest"
)

// The findings tier measures evidence 5 on long real sources, never in CI or the check: each
// source of findingSources, in the test corpus at ~/docs and checked there against its manifest,
// is cut at its silences into windows, transcribed by audiotest.Model on the router
// SPIKE_ROUTER_URL names, at most findingConcurrency windows in flight, set A's audio slots, and
// joined; scored against its truth by normalized word error rate and by the edits at each seam;
// its seams sent, a few words at a time, to passModel for a seam pass, scored again; and its first
// excerptLength sent whole, as one unsplit request, beside the same excerpt windowed. Each is run
// AUDIO_FINDINGS_RUNS times, greedily. `mise run findings-audio` runs it; what it found is in
// docs/findings/audio.md. Its settings, from the environment:
//
//   - AUDIO_FINDINGS_SOURCE names the one source TestFindingsSource runs; unset skips it. The
//     task runs each source as a go test run of its own.
//   - DOCS names the corpus root, ~/docs when unset.
//   - AUDIO_FINDINGS_RUNS is the runs of each source, 3 when unset.
//   - AUDIO_FINDINGS_RESULTS names a directory TestFindingsSource writes its source's results to
//     as JSON, after each run, and TestFindingsReport reads them from; unset writes nothing.
const (
	sourceEnv  = "AUDIO_FINDINGS_SOURCE"
	runsEnv    = "AUDIO_FINDINGS_RUNS"
	resultsEnv = "AUDIO_FINDINGS_RESULTS"
)

const (
	// findingConcurrency is the windows in flight, E4B's 4 slots in set A.
	findingConcurrency = 4
	// excerptLength is the excerpt sent unsplit: 10 minutes, which llama.cpp b11669 cuts into 30 s
	// chunks of its own, about 15,000 audio tokens, inside E4B's 32,768-token context.
	excerptLength = 10 * time.Minute
	// passModel is the seam pass's model, set A's gpt-oss-120b.
	passModel = "ggml-org/gpt-oss-120b-GGUF:MXFP4"
	// passConcurrency is the seam pass's requests in flight, the slots of passModel in set A.
	passConcurrency = 4
	// passOutputCap bounds a seam pass answer, its reasoning included.
	passOutputCap = 2048
)

// passInstructions is the seam pass's instruction, minimal: remove, don't rewrite.
const passInstructions = "The user's message is a few words from a speech transcript, where two transcripts " +
	"of overlapping stretches of audio were joined. Remove any words the join duplicated or garbled. " +
	"Do not paraphrase, reorder, correct, or add words. Reply with the remaining words alone."

// score is a word error count: edits over the truth's normalized words.
type score struct {
	Edits  int     `json:"edits"`
	Length int     `json:"length"`
	Rate   float64 `json:"rate"`
}

// wer scores got against truth by NormalizedWordErrors.
func wer(truth, got string) score {
	e, n := textscore.NormalizedWordErrors(truth, got)
	return score{Edits: e, Length: n, Rate: textscore.Rate(e, n)}
}

// seamStats is how a joined transcript's seams fared against the truth.
type seamStats struct {
	// Seams, Matched, and Unmatched count the seams, as Join reported each.
	Seams     int `json:"seams"`
	Matched   int `json:"matched"`
	Unmatched int `json:"unmatched"`
	// Edits is the edits within textscore.SeamRadius words of every seam, and PerSeam each seam's.
	Edits   int   `json:"edits"`
	PerSeam []int `json:"per_seam"`
	// Distribution counts the seams by their edits: Distribution[k] seams had k edits.
	Distribution map[int]int `json:"distribution"`
	// UnmatchedEdits is Edits of the unmatched seams alone.
	UnmatchedEdits int `json:"unmatched_edits"`
	// Controls is the control positions, each the middle of a window between two seams, at least
	// twice SeamRadius words from either; ControlEdits is the edits within SeamRadius words of
	// them, counted as a seam's are: the error rate away from the seams, for the seams' to be read
	// against.
	Controls     int `json:"controls"`
	ControlEdits int `json:"control_edits"`
}

// seamScore counts the edits at seams and at the control positions between them, in one
// alignment of joined's words against truth's. matched gives each seam's Matched.
func seamScore(truth string, words []string, seams []int, matched []bool) seamStats {
	var controls []int
	for i := 1; i < len(seams); i++ {
		if seams[i]-seams[i-1] > 4*textscore.SeamRadius {
			controls = append(controls, (seams[i-1]+seams[i])/2)
		}
	}
	counts := textscore.SeamErrors(truth, strings.Join(words, " "), append(slices.Clone(seams), controls...))
	s := seamStats{Seams: len(seams), PerSeam: counts[:len(seams)], Distribution: map[int]int{}, Controls: len(controls)}
	for i, c := range s.PerSeam {
		s.Edits += c
		s.Distribution[c]++
		if matched[i] {
			s.Matched++
		} else {
			s.Unmatched++
			s.UnmatchedEdits += c
		}
	}
	for _, c := range counts[len(seams):] {
		s.ControlEdits += c
	}
	return s
}

// windowedRun is one windowed transcription of a source or its excerpt.
type windowedRun struct {
	// Windows and Failed count the plan's windows and the ones whose cut or transcription failed;
	// Cuts counts the windows by what ended them.
	Windows int            `json:"windows"`
	Failed  int            `json:"failed"`
	Cuts    map[string]int `json:"cuts"`
	// PlanTime, Wall, and Total are the plan's time, the windows' from the first cut to the last
	// transcript, and the two together.
	PlanTime time.Duration `json:"plan_time"`
	Wall     time.Duration `json:"wall"`
	Total    time.Duration `json:"total"`
	// MeanLatency is the mean of the windows' Transcribe calls' durations, and MaxInFlight the
	// most calls in flight at once.
	MeanLatency time.Duration `json:"mean_latency"`
	MaxInFlight int           `json:"max_in_flight"`
	// InputTokens and OutputTokens are the windows' tokens together.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// WER scores the joined transcript; Seams its seams.
	WER   score     `json:"wer"`
	Seams seamStats `json:"seams"`
	// Pass is the seam pass over the joined transcript, for a whole source's run.
	Pass *passResult `json:"pass,omitempty"`
	// Transcript is the joined transcript, kept for a redistributable source only.
	Transcript string `json:"transcript,omitempty"`

	// words and seams are the joined words and seam positions, for the pass; text is the joined
	// transcript, kept for scoring.
	words   []string
	seams   []audio.Seam
	text    string
	matched []bool
}

// unsplitRun is the excerpt sent whole as one transcription request.
type unsplitRun struct {
	Wall  time.Duration             `json:"wall"`
	Usage client.TranscriptionUsage `json:"usage"`
	Words int                       `json:"words"`
	WER   score                     `json:"wer"`
	// Transcript is kept for a redistributable source only.
	Transcript string `json:"transcript,omitempty"`
	text       string
}

// passSpan is one span of the seam pass: the words around one seam, or around seams near enough
// that their spans meet, and what passModel answered.
type passSpan struct {
	// Seams are the seams' indices, From and To the span of the joined words sent.
	Seams []int `json:"seams"`
	From  int   `json:"from"`
	To    int   `json:"to"`
	// Status is "changed" when the answer was spliced in and differs from the span, "unchanged"
	// when it equals it, and "failed" when it was refused: Reason says why, and the span is kept.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Latency is the request's duration and OutputTokens its output, reasoning included.
	Latency      time.Duration `json:"latency"`
	OutputTokens int           `json:"output_tokens"`
	// Before and After are the span's words and the answer, kept for a redistributable source, and
	// for any source when the answer changed or failed: short snippets, never a whole transcript.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// passResult is the seam pass over one joined transcript.
type passResult struct {
	Model    string        `json:"model"`
	Requests int           `json:"requests"`
	Changed  int           `json:"changed"`
	Same     int           `json:"unchanged"`
	Failed   int           `json:"failed"`
	Failures []string      `json:"failures,omitempty"`
	Wall     time.Duration `json:"wall"`
	// WER and Seams score the transcript after the pass, the seams at their positions after it.
	WER   score      `json:"wer"`
	Seams seamStats  `json:"seams"`
	Spans []passSpan `json:"spans"`
	// Transcript is the transcript after the pass, kept for a redistributable source only.
	Transcript string `json:"transcript,omitempty"`
}

// findingRun is one run of a source: the excerpt unsplit and windowed, and the whole source
// windowed, with its seam pass.
type findingRun struct {
	Run             int          `json:"run"`
	Unsplit         *unsplitRun  `json:"unsplit"`
	ExcerptWindowed *windowedRun `json:"excerpt_windowed"`
	Full            *windowedRun `json:"full"`
}

// excerptInfo is how the excerpt and its truth were made.
type excerptInfo struct {
	Seconds float64 `json:"seconds"`
	// Method says how the excerpt's truth was cut, and TruthWords is its normalized words.
	Method     string `json:"method"`
	TruthWords int    `json:"truth_words"`
	// Prefixes are, for each excerpt transcript, the length of the truth's normalized prefix
	// nearest it (textscore.BestPrefix), labelled by transcript; Spread is their range, the
	// derived end's uncertainty in words.
	Prefixes map[string]int `json:"prefixes"`
	Spread   int            `json:"spread"`
	// CrossCheck is where the excerpt ends by another means, when the source has one.
	CrossCheck *crossCheck `json:"cross_check,omitempty"`
}

// findingResult is one source's results.
type findingResult struct {
	Source struct {
		Name            string  `json:"name"`
		Title           string  `json:"title"`
		Audio           string  `json:"audio"`
		SHA256          string  `json:"sha256"`
		Bytes           int64   `json:"bytes"`
		License         string  `json:"license"`
		SourceURL       string  `json:"source_url"`
		Redistributable bool    `json:"redistributable"`
		Seconds         float64 `json:"seconds"`
		TruthNote       string  `json:"truth_note"`
		TruthWords      int     `json:"truth_words"`
	} `json:"source"`
	Model       string        `json:"model"`
	PassModel   string        `json:"pass_model"`
	Concurrency int           `json:"concurrency"`
	Started     time.Time     `json:"started"`
	Excerpt     excerptInfo   `json:"excerpt"`
	Runs        []*findingRun `json:"runs"`
}

// TestFindingsSource runs the source AUDIO_FINDINGS_SOURCE names: AUDIO_FINDINGS_RUNS runs, each
// the excerpt sent unsplit, the excerpt windowed, and the whole source windowed and then seam
// passed; then the excerpt's truth is cut and every transcript scored. It writes the source's
// results after each run, and logs a recorded line for each.
func TestFindingsSource(t *testing.T) {
	name := os.Getenv(sourceEnv)
	if name == "" {
		t.Skipf("%s is unset: name one of the finding's sources", sourceEnv)
	}
	i := slices.IndexFunc(findingSources, func(s findingSource) bool { return s.Name == name })
	if i < 0 {
		t.Fatalf("%s=%s names no source", sourceEnv, name)
	}
	src := findingSources[i]
	docs := corpustest.Root(t)
	runs := 3
	if v := os.Getenv(runsEnv); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("%s=%q isn't a positive count", runsEnv, v)
		}
		runs = n
	}
	entries, err := corpustest.Check(docs, append([]string{src.Audio}, src.Truth...))
	if err != nil {
		t.Fatal(err)
	}
	tr := audiotest.Greedy(t)
	tru, err := src.read(docs)
	if err != nil {
		t.Fatal(err)
	}
	truthWords := textscore.NormalizeWords(tru.Text)
	if len(truthWords) == 0 {
		t.Fatal("the truth has no words")
	}
	s := speechRunners["exec"](t)
	audioPath := filepath.Join(docs, src.Audio)
	probe, err := s.Probe(t.Context(), audioPath)
	if err != nil {
		t.Fatal(err)
	}

	var res findingResult
	res.Source.Name, res.Source.Title, res.Source.Audio = src.Name, src.Title, src.Audio
	res.Source.SHA256, res.Source.Bytes, res.Source.License = entries[0].SHA256, entries[0].Bytes, entries[0].License
	res.Source.SourceURL, res.Source.Redistributable = entries[0].SourceURL, entries[0].Redistributable
	res.Source.Seconds, res.Source.TruthNote, res.Source.TruthWords = probe.Duration.Seconds(), src.TruthNote, len(truthWords)
	res.Model, res.PassModel, res.Concurrency, res.Started = audiotest.Model, passModel, findingConcurrency, time.Now().UTC()
	res.Excerpt.Seconds = excerptLength.Seconds()
	t.Logf("recorded: %s: %s, %s, %.1f s (%s, %d Hz, %d channels), %d truth words; truth: %s",
		src.Name, src.Title, src.Audio, probe.Duration.Seconds(), probe.Codec, probe.SampleRate, probe.Channels, len(truthWords), src.TruthNote)

	excerpt := filepath.Join(t.TempDir(), "excerpt.wav")
	if err := cutExcerpt(t.Context(), audioPath, excerpt); err != nil {
		t.Fatal(err)
	}
	c := visiontest.Router(t)
	for run := 1; run <= runs; run++ {
		r := &findingRun{Run: run}
		res.Runs = append(res.Runs, r)
		if r.Unsplit, err = transcribeWhole(t.Context(), tr, excerpt); err != nil {
			t.Fatalf("run %d: unsplit: %v", run, err)
		}
		t.Logf("recorded: %s run %d: unsplit excerpt: %v, %d input and %d output tokens, %d words",
			src.Name, run, r.Unsplit.Wall.Round(time.Millisecond), r.Unsplit.Usage.InputTokens, r.Unsplit.Usage.OutputTokens, r.Unsplit.Words)
		if r.ExcerptWindowed, err = windowed(t, s, tr, excerpt); err != nil {
			t.Fatalf("run %d: excerpt windowed: %v", run, err)
		}
		logWindowed(t, src.Name, run, "excerpt windowed", r.ExcerptWindowed)
		if r.Full, err = windowed(t, s, tr, audioPath); err != nil {
			t.Fatalf("run %d: windowed: %v", run, err)
		}
		r.Full.WER = wer(tru.Text, r.Full.text)
		r.Full.Seams = seamScore(tru.Text, r.Full.words, positions(r.Full.seams), r.Full.matched)
		logWindowed(t, src.Name, run, "whole source windowed", r.Full)
		pass := seamPass(t.Context(), c, r.Full.words, r.Full.seams)
		r.Full.Pass = pass.passResult
		after := strings.Join(pass.words, " ")
		r.Full.Pass.WER = wer(tru.Text, after)
		r.Full.Pass.Seams = seamScore(tru.Text, pass.words, pass.positions, r.Full.matched)
		t.Logf("recorded: %s run %d: seam pass: %d requests in %v, %d changed, %d unchanged, %d failed %v; WER %.4f (%d of %d) from %.4f; seam edits %d from %d, control edits %d from %d",
			src.Name, run, r.Full.Pass.Requests, r.Full.Pass.Wall.Round(time.Millisecond), r.Full.Pass.Changed, r.Full.Pass.Same,
			r.Full.Pass.Failed, r.Full.Pass.Failures, r.Full.Pass.WER.Rate, r.Full.Pass.WER.Edits, r.Full.Pass.WER.Length, r.Full.WER.Rate,
			r.Full.Pass.Seams.Edits, r.Full.Seams.Edits, r.Full.Pass.Seams.ControlEdits, r.Full.Seams.ControlEdits)
		if src.Redistributable {
			r.Unsplit.Transcript, r.ExcerptWindowed.Transcript = r.Unsplit.text, r.ExcerptWindowed.text
			r.Full.Transcript, r.Full.Pass.Transcript = r.Full.text, after
		}
		scoreExcerpts(t, src, tru, truthWords, &res)
		writeResult(t, &res)
	}
	for _, r := range res.Runs {
		if r.Full.Failed > 0 || r.ExcerptWindowed.Failed > 0 {
			t.Errorf("run %d: %d windows of the source and %d of the excerpt failed", r.Run, r.Full.Failed, r.ExcerptWindowed.Failed)
		}
	}
}

// cutExcerpt writes the source's first excerptLength, its first audio stream, to out as 16 kHz
// mono 16-bit WAV, the windows' format, through the host's ffmpeg.
func cutExcerpt(ctx context.Context, source, out string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-i", source,
		"-map", "0:a:0", "-t", strconv.FormatFloat(excerptLength.Seconds(), 'f', -1, 64),
		"-ac", "1", "-ar", strconv.Itoa(audio.SampleRate), "-c:a", "pcm_s16le", "-y", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg excerpt: %w: %s", err, b)
	}
	return nil
}

// transcribeWhole sends the file at path as one transcription request, as tr sends a window.
func transcribeWhole(ctx context.Context, tr audiotest.Transcriber, path string) (*unsplitRun, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	start := time.Now()
	out, err := tr.Client.Transcribe(ctx, &client.TranscriptionRequest{
		Model: tr.Model, File: f, FileName: "excerpt.wav", Language: tr.Language, Prompt: tr.Prompt, Native: tr.Native,
	})
	if err != nil {
		return nil, err
	}
	return &unsplitRun{Wall: time.Since(start), Usage: out.Usage, Words: len(strings.Fields(out.Text)), text: out.Text}, nil
}

// windowed runs the harness over the file at path at findingConcurrency.
func windowed(t *testing.T, s *audio.Splitter, tr audiotest.Transcriber, path string) (*windowedRun, error) {
	r, err := audiotest.Run(t.Context(), audiotest.Request{
		Splitter:    s,
		Source:      audio.Request{Path: path},
		Dir:         t.TempDir(),
		Transcriber: tr,
		Concurrency: findingConcurrency,
	})
	if err != nil {
		return nil, err
	}
	w := &windowedRun{
		Windows: len(r.Windows), Cuts: map[string]int{}, PlanTime: r.PlanTime, Wall: r.Wall, Total: r.PlanTime + r.Wall,
		MaxInFlight: r.MaxInFlight, words: r.Transcript.Words, seams: r.Transcript.Seams, text: r.Transcript.Text(),
	}
	var latency time.Duration
	for _, win := range r.Windows {
		w.Cuts[win.File.Window.Cut.String()]++
		if win.Err != nil {
			w.Failed++
			t.Logf("recorded: window %d %v failed: %v", win.File.Index, win.File.Window.Span, win.Err)
			continue
		}
		latency += win.Latency()
		w.InputTokens += win.Usage.InputTokens
		w.OutputTokens += win.Usage.OutputTokens
	}
	if n := w.Windows - w.Failed; n > 0 {
		w.MeanLatency = latency / time.Duration(n)
	}
	for _, seam := range w.seams {
		w.matched = append(w.matched, seam.Matched)
	}
	return w, nil
}

// positions returns the seams' positions in the joined words.
func positions(seams []audio.Seam) []int {
	out := make([]int, len(seams))
	for i, s := range seams {
		out[i] = s.Position
	}
	return out
}

// logWindowed logs a windowed run's recorded line.
func logWindowed(t *testing.T, name string, run int, what string, w *windowedRun) {
	t.Helper()
	t.Logf("recorded: %s run %d: %s: %d windows (%v), %d failed; plan %v, wall %v, total %v; mean latency %v, %d in flight at most; %d input and %d output tokens; WER %.4f (%d of %d); seams %d (%d unmatched), %d edits near them, %d near %d controls",
		name, run, what, w.Windows, w.Cuts, w.Failed, w.PlanTime.Round(time.Millisecond), w.Wall.Round(time.Millisecond),
		w.Total.Round(time.Millisecond), w.MeanLatency.Round(time.Millisecond), w.MaxInFlight, w.InputTokens, w.OutputTokens,
		w.WER.Rate, w.WER.Edits, w.WER.Length, w.Seams.Seams, w.Seams.Unmatched, w.Seams.Edits, w.Seams.ControlEdits, w.Seams.Controls)
}

// seamPassOut is passResult with the words after the pass and the seams' positions in them.
type seamPassOut struct {
	*passResult
	words     []string
	positions []int
}

// seamPass sends passModel, for each seam, only the words around it: textscore.SeamRadius words
// before its position and SeamRadius from it on, spans that meet merged into one. Each answer
// replaces exactly its span's words when its normalized words are a subsequence of the span's:
// only removals. Any other answer, an error, an incomplete response, an empty answer to a span
// with words, or one that adds, changes, or reorders a word, is a failure of the pass, recorded,
// and the span is kept as it was. A seam's position after the pass is the same offset into its
// span's replacement, clipped to the replacement's length.
func seamPass(ctx context.Context, c *client.Client, words []string, seams []audio.Seam) seamPassOut {
	type span struct {
		seams    []int
		from, to int
	}
	var spans []span
	for i, s := range seams {
		from, to := max(0, s.Position-textscore.SeamRadius), min(len(words), s.Position+textscore.SeamRadius)
		if n := len(spans); n > 0 && from <= spans[n-1].to {
			spans[n-1].seams = append(spans[n-1].seams, i)
			spans[n-1].to = max(spans[n-1].to, to)
			continue
		}
		spans = append(spans, span{seams: []int{i}, from: from, to: to})
	}
	res := &passResult{Model: passModel, Requests: len(spans), Spans: make([]passSpan, len(spans))}
	answers := make([][]string, len(spans))
	slots := make(chan struct{}, passConcurrency)
	var wg sync.WaitGroup
	start := time.Now()
	for k, sp := range spans {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			ps := &res.Spans[k]
			ps.Seams, ps.From, ps.To = sp.seams, sp.from, sp.to
			before := words[sp.from:sp.to]
			ps.Before = strings.Join(before, " ")
			begun := time.Now()
			answer, tokens, reason := askSeam(ctx, c, ps.Before)
			ps.Latency, ps.OutputTokens = time.Since(begun), tokens
			ps.After = strings.Join(answer, " ")
			switch {
			case reason != "":
				ps.Status, ps.Reason = "failed", reason
			case len(answer) == 0 && len(before) > 0:
				ps.Status, ps.Reason = "failed", "an empty answer"
			case !isSubsequence(textscore.NormalizeWords(ps.After), textscore.NormalizeWords(ps.Before)):
				ps.Status, ps.Reason = "failed", "words beyond removals"
			case slices.Equal(answer, before):
				ps.Status = "unchanged"
				answers[k] = answer
			default:
				ps.Status = "changed"
				answers[k] = answer
			}
		})
	}
	wg.Wait()
	res.Wall = time.Since(start)
	out := seamPassOut{passResult: res, positions: make([]int, len(seams))}
	prev := 0
	for k, sp := range spans {
		ps := &res.Spans[k]
		switch ps.Status {
		case "changed":
			res.Changed++
		case "unchanged":
			res.Same++
		default:
			res.Failed++
			res.Failures = append(res.Failures, ps.Reason)
			answers[k] = words[sp.from:sp.to]
		}
		out.words = append(out.words, words[prev:sp.from]...)
		at := len(out.words)
		out.words = append(out.words, answers[k]...)
		for _, i := range sp.seams {
			out.positions[i] = at + min(seams[i].Position-sp.from, len(answers[k]))
		}
		prev = sp.to
	}
	out.words = append(out.words, words[prev:]...)
	return out
}

// askSeam sends passModel one span's words and returns its answer's words, its output tokens,
// and, when the request failed or its response isn't completed, why.
func askSeam(ctx context.Context, c *client.Client, words string) (answer []string, tokens int, reason string) {
	resp, err := c.Generate(ctx, &protocol.Request{
		Model:        passModel,
		Instructions: passInstructions,
		Input: []protocol.Item{protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
			protocol.InputText{Text: words},
		}}},
		Reasoning:       protocol.ReasoningParam{Effort: protocol.ReasoningEffortLow},
		MaxOutputTokens: passOutputCap,
		Native:          jsontext.Value(`{"temperature":0}`),
	})
	if err != nil {
		return nil, 0, err.Error()
	}
	if resp.Status != protocol.StatusCompleted {
		return nil, resp.Usage.OutputTokens, "response " + visiontest.ResponseStatus(resp)
	}
	return strings.Fields(visiontest.ResponseText(resp)), resp.Usage.OutputTokens, ""
}

// isSubsequence reports whether a's elements are b's, some left out, in order.
func isSubsequence(a, b []string) bool {
	i := 0
	for _, w := range b {
		if i < len(a) && a[i] == w {
			i++
		}
	}
	return i == len(a)
}

// scoreExcerpts cuts the excerpt's truth and scores every run's excerpt transcripts against it.
// A timed truth's excerpt is cut by time. Otherwise the excerpt's truth is the prefix of the whole
// truth's normalized words nearest the excerpt's transcripts: for each transcript so far, unsplit
// and windowed, the prefix textscore.BestPrefix finds, and of those the median, the lower of the
// middle two; their range is its uncertainty. A timed truth records the derived prefixes too, as a
// check of the method.
func scoreExcerpts(t *testing.T, src findingSource, tru truth, truthWords []string, res *findingResult) {
	t.Helper()
	res.Excerpt.Prefixes = map[string]int{}
	var ks []int
	for _, r := range res.Runs {
		for label, text := range map[string]string{
			fmt.Sprintf("run %d unsplit", r.Run):  r.Unsplit.text,
			fmt.Sprintf("run %d windowed", r.Run): r.ExcerptWindowed.text,
		} {
			k, _ := textscore.BestPrefix(truthWords, textscore.NormalizeWords(text))
			res.Excerpt.Prefixes[label] = k
			ks = append(ks, k)
		}
	}
	slices.Sort(ks)
	res.Excerpt.Spread = ks[len(ks)-1] - ks[0]
	res.Excerpt.CrossCheck = tru.CrossCheck
	excerptTruth := tru.Excerpt
	if tru.Timed {
		res.Excerpt.Method = fmt.Sprintf("by time: every truth word whose middle is before %.0f s", excerptLength.Seconds())
	} else {
		k := ks[(len(ks)-1)/2]
		excerptTruth = strings.Join(truthWords[:k], " ")
		res.Excerpt.Method = fmt.Sprintf("derived: the truth's first %d normalized words, the median of the %d prefixes "+
			"textscore.BestPrefix finds nearest each excerpt transcript, unsplit and windowed", k, len(ks))
	}
	res.Excerpt.TruthWords = len(textscore.NormalizeWords(excerptTruth))
	for _, r := range res.Runs {
		r.Unsplit.WER = wer(excerptTruth, r.Unsplit.text)
		w := r.ExcerptWindowed
		w.WER = wer(excerptTruth, w.text)
		w.Seams = seamScore(excerptTruth, w.words, positions(w.seams), w.matched)
	}
	r := res.Runs[len(res.Runs)-1]
	t.Logf("recorded: %s: excerpt truth %s, %d words; prefixes %v, spread %d; cross-check %+v",
		src.Name, res.Excerpt.Method, res.Excerpt.TruthWords, res.Excerpt.Prefixes, res.Excerpt.Spread, res.Excerpt.CrossCheck)
	t.Logf("recorded: %s run %d: excerpt: unsplit WER %.4f (%d of %d) in %v; windowed WER %.4f (%d of %d) in %v total, seam edits %d at %d seams, %d at %d controls",
		src.Name, r.Run, r.Unsplit.WER.Rate, r.Unsplit.WER.Edits, r.Unsplit.WER.Length, r.Unsplit.Wall.Round(time.Millisecond),
		r.ExcerptWindowed.WER.Rate, r.ExcerptWindowed.WER.Edits, r.ExcerptWindowed.WER.Length, r.ExcerptWindowed.Total.Round(time.Millisecond),
		r.ExcerptWindowed.Seams.Edits, r.ExcerptWindowed.Seams.Seams, r.ExcerptWindowed.Seams.ControlEdits, r.ExcerptWindowed.Seams.Controls)
}

// writeResult writes res to AUDIO_FINDINGS_RESULTS as <source>.json, when it is set. A source
// that isn't redistributable keeps the pass's snippets only for the spans it changed or failed.
func writeResult(t *testing.T, res *findingResult) {
	t.Helper()
	dir := os.Getenv(resultsEnv)
	if dir == "" {
		return
	}
	if !res.Source.Redistributable {
		for _, r := range res.Runs {
			for i := range r.Full.Pass.Spans {
				if sp := &r.Full.Pass.Spans[i]; sp.Status == "unchanged" {
					sp.Before, sp.After = "", ""
				}
			}
		}
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, res.Source.Name+".json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindingsReport reads the sources' results from AUDIO_FINDINGS_RESULTS, those present, and
// logs each source's runs as the finding's tables give them: the excerpt unsplit and windowed,
// the whole source windowed, its seams beside their controls, and the seam pass. Unset skips it.
func TestFindingsReport(t *testing.T) {
	dir := os.Getenv(resultsEnv)
	if dir == "" {
		t.Skipf("%s is unset: name the directory the sources' results are in", resultsEnv)
	}
	ms := func(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s" }
	found := 0
	for _, src := range findingSources {
		data, err := os.ReadFile(filepath.Join(dir, src.Name+".json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var res findingResult
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatalf("%s: %v", src.Name, err)
		}
		found++
		t.Logf("recorded: %s: %d runs; excerpt truth %d words (%s), prefix spread %d, cross-check %+v",
			src.Name, len(res.Runs), res.Excerpt.TruthWords, res.Excerpt.Method, res.Excerpt.Spread, res.Excerpt.CrossCheck)
		for _, r := range res.Runs {
			u, e, f := r.Unsplit, r.ExcerptWindowed, r.Full
			t.Logf("recorded: excerpt | %s | %d | unsplit %.3f (%d/%d), %s, %d+%d tokens | windowed %.3f (%d/%d), %s, %d windows, %s mean latency | seams %d/%d (%d unmatched), controls %d/%d |",
				src.Name, r.Run, u.WER.Rate, u.WER.Edits, u.WER.Length, ms(u.Wall), u.Usage.InputTokens, u.Usage.OutputTokens,
				e.WER.Rate, e.WER.Edits, e.WER.Length, ms(e.Total), e.Windows, ms(e.MeanLatency),
				e.Seams.Edits, e.Seams.Seams, e.Seams.Unmatched, e.Seams.ControlEdits, e.Seams.Controls)
			t.Logf("recorded: full | %s | %d | %.3f (%d/%d) | %s (plan %s) | %d windows %v | %s mean latency | seams %d edits at %d (%.2f each; %d unmatched, %d edits), controls %d at %d (%.2f each) | distribution %v |",
				src.Name, r.Run, f.WER.Rate, f.WER.Edits, f.WER.Length, ms(f.Total), ms(f.PlanTime), f.Windows, f.Cuts, ms(f.MeanLatency),
				f.Seams.Edits, f.Seams.Seams, perPosition(f.Seams.Edits, f.Seams.Seams), f.Seams.Unmatched, f.Seams.UnmatchedEdits,
				f.Seams.ControlEdits, f.Seams.Controls, perPosition(f.Seams.ControlEdits, f.Seams.Controls), f.Seams.Distribution)
			p := f.Pass
			t.Logf("recorded: pass | %s | %d | %d requests in %s | %d changed, %d unchanged, %d failed %v | WER %.3f (%d/%d) | seams %d edits (%.2f each), controls %d |",
				src.Name, r.Run, p.Requests, ms(p.Wall), p.Changed, p.Same, p.Failed, p.Failures, p.WER.Rate, p.WER.Edits, p.WER.Length,
				p.Seams.Edits, perPosition(p.Seams.Edits, p.Seams.Seams), p.Seams.ControlEdits)
		}
	}
	if found == 0 {
		t.Fatalf("no source's results in %s", dir)
	}
}

// perPosition returns edits over positions, and 0 for none.
func perPosition(edits, positions int) float64 { return textscore.Rate(edits, positions) }
