//go:build findings && linux

package dispatch_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/onenote"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The findings tier measures the dispatcher and the onenote adapter for task onenote's finding,
// never in CI or the check: the onenote adapter's time and peak memory beside the onenote CLI
// alone on three large sections; the dispatcher over mail's scale store, its time, peak memory,
// and peak disk beside the email adapter and mail alone; a resumed dispatch of the scale store
// against a whole one; the pool at 1, 2, 4, and 8 workers over plaso's store, the scale store, a
// byte copy of it, and mail's fixtures; and the page ink of handwriting_recognition.one through
// the picture stage to Gemma 4 26B-A4B on the router SPIKE_ROUTER_URL names, beside OneNote's
// own recognition. `mise run findings-onenote` runs it; what it found is in
// docs/findings/onenote.md. Its settings, from the environment:
//
//   - ONENOTE_LARGE names the directory holding LargeData.one, "Large Desktop.one", and
//     "Large OneDrive.one", ~/docs/onenote/large/; unset skips them.
//   - ONENOTE_SCALE_STORE names the scale.pst mail's generator builds (MISE_ENV=fixtures mise run
//     scale, in mail's checkout); unset skips every measurement of it. It is never committed.
//   - ONENOTE_SCALE_COPY names where the pool's byte copy of the scale store is kept: copied there
//     when absent, and refused unless its size is the store's. It is never committed.
//   - ONENOTE_PLASO_STORE names plaso's store, which mise run findings-email fetches; refused
//     unless its SHA-256 is plasoSHA256, and unset skips the pool.
//   - ONENOTE_FINDINGS_RUNS is the timed runs of each input each way, 3 when unset.
//   - ONENOTE_FINDINGS_RESULTS names a directory each test writes its results to as JSON; unset
//     writes nothing, so a run keeps its results only when asked to.
const (
	largeEnv     = "ONENOTE_LARGE"
	scaleEnv     = "ONENOTE_SCALE_STORE"
	scaleCopyEnv = "ONENOTE_SCALE_COPY"
	plasoEnv     = "ONENOTE_PLASO_STORE"
	runsEnv      = "ONENOTE_FINDINGS_RUNS"
	resultsEnv   = "ONENOTE_FINDINGS_RESULTS"
)

// plasoSHA256 is plaso's store's SHA-256, test_data/nromanoff@stark-research-labs.com.pst from
// log2timeline/plaso at f5bf35fc, the pin the email finding and mail's own fetch it at.
const plasoSHA256 = "bab64f3589f8ce758df9e595b89a5e4b9be159929e49100397152fdcc7bd6013"

// The measurement processes, as the email finding's: a process's peak resident memory can't be
// read from the test process, which carries its own high-water mark into every program it starts
// (tool.Usage), so the test binary is re-executed, with findingsModeEnv naming its part:
//
//   - "launch" runs its arguments as a program, with findingsModeEnv set to findingsChildEnv's
//     value, and writes the program's wait4 rusage and wall time to stderr after it, as a last line
//     that begins findingsLaunchPrefix.
//   - "dispatch" is the dispatcher under measurement: it dispatches the job its first argument
//     holds, as JSON, and writes a dispatchSurvey and its rusage, its own and its children's, to
//     stdout.
//   - "email" is the email adapter alone: a Reader over the mail on PATH reads its first argument
//     with its second as the attachments directory, routing every attachment.
//   - "onenote" is the onenote adapter alone: a Reader over the onenote on PATH reads its first
//     argument, with its second as the media directory when not empty, routing every medium.
//   - "floor" exits at once: launched, it measures the floor.
const (
	findingsModeEnv      = "ONENOTE_FINDINGS_MODE"
	findingsChildEnv     = "ONENOTE_FINDINGS_CHILD"
	findingsLaunchPrefix = "findings-launch: "
)

func init() {
	switch os.Getenv(findingsModeEnv) {
	case "launch":
		os.Exit(findingsLauncher(os.Args[1:]))
	case "dispatch":
		os.Exit(measured(func(ctx context.Context) (any, error) { return dispatchJobOf(ctx, os.Args[1:]) }))
	case "email":
		os.Exit(measured(func(ctx context.Context) (any, error) { return readMail(ctx, os.Args[1:]) }))
	case "onenote":
		os.Exit(measured(func(ctx context.Context) (any, error) { return readOneNote(ctx, os.Args[1:]) }))
	case "floor":
		os.Exit(0)
	}
}

// usage is a process's resource usage, from its rusage.
type usage struct {
	// MaxRSS is the peak resident memory in bytes.
	MaxRSS int64 `json:"max_rss"`
	// User and System are the CPU time, the process's and its reaped children's.
	User   time.Duration `json:"user"`
	System time.Duration `json:"system"`
	// Wall is from the start of the program to its exit, as the launcher measures it.
	Wall time.Duration `json:"wall,omitzero"`
}

// fromRusage returns ru's usage; Linux gives ru_maxrss in KiB.
func fromRusage(ru *syscall.Rusage) usage {
	return usage{
		MaxRSS: ru.Maxrss << 10,
		User:   time.Duration(ru.Utime.Nano()),
		System: time.Duration(ru.Stime.Nano()),
	}
}

// findingsLauncher runs args as a program with findingsModeEnv set to findingsChildEnv's value and
// reports its rusage and wall time, returning its exit code.
func findingsLauncher(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "launcher: no program")
		return 2
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), findingsModeEnv+"="+os.Getenv(findingsChildEnv))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	if cmd.ProcessState != nil {
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			u := fromRusage(ru)
			u.Wall = wall
			line, _ := json.Marshal(u)
			fmt.Fprintf(os.Stderr, "\n%s%s\n", findingsLaunchPrefix, line)
		}
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return max(exitErr.ExitCode(), 1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "launcher:", err)
		return 2
	}
	return 0
}

// report is what a measuring process writes: its survey, its own rusage, and its children's, the
// CLIs and converters it ran, whose maxrss is the largest of any one of them, its floor the
// measuring process's size when it started that one.
type report struct {
	Survey   json.RawMessage `json:"survey"`
	Self     usage           `json:"self"`
	Children usage           `json:"children"`
}

// measured runs survey as a measuring process and writes its report to stdout, returning the
// process's exit code.
func measured(survey func(ctx context.Context) (any, error)) int {
	s, err := survey(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 1
	}
	b, err := json.Marshal(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 2
	}
	var self, children syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &self); err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 2
	}
	if err := syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children); err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 2
	}
	out, err := json.Marshal(report{Survey: b, Self: fromRusage(&self), Children: fromRusage(&children)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 2
	}
	if _, err := os.Stdout.Write(append(out, '\n')); err != nil {
		return 1
	}
	return 0
}

// launch runs args through the launcher, the measured process given child as its
// findingsModeEnv, and returns the measured process's usage and, when capture is set, its stdout;
// otherwise its stdout goes to /dev/null.
func launch(ctx context.Context, child string, args []string, capture bool) (usage, []byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return usage{}, nil, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = append(os.Environ(), findingsModeEnv+"=launch", findingsChildEnv+"="+child)
	var stdout, stderr bytes.Buffer
	if capture {
		cmd.Stdout = &stdout
	} else {
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return usage{}, nil, err
		}
		defer func() { _ = devnull.Close() }()
		cmd.Stdout = devnull
	}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return usage{}, nil, fmt.Errorf("%v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	_, last, ok := strings.Cut(stderr.String(), findingsLaunchPrefix)
	if !ok {
		return usage{}, nil, fmt.Errorf("launcher: no rusage line in %q", stderr.String())
	}
	var u usage
	if err := json.Unmarshal([]byte(strings.TrimSpace(last)), &u); err != nil {
		return usage{}, nil, fmt.Errorf("launcher: %w", err)
	}
	return u, stdout.Bytes(), nil
}

// launchReport launches a measuring process of mode child over args and decodes its report, its
// survey into survey.
func launchReport(ctx context.Context, child string, args []string, survey any) (usage, report, error) {
	exe, err := os.Executable()
	if err != nil {
		return usage{}, report{}, err
	}
	u, out, err := launch(ctx, child, append([]string{exe}, args...), true)
	if err != nil {
		return usage{}, report{}, err
	}
	var r report
	if err := json.Unmarshal(out, &r); err != nil {
		return usage{}, report{}, fmt.Errorf("%s's report %.200q: %w", child, out, err)
	}
	if err := json.Unmarshal(r.Survey, survey); err != nil {
		return usage{}, report{}, fmt.Errorf("%s's survey: %w", child, err)
	}
	return u, r, nil
}

// findingStages returns every adapter that needs no model, as a composition root wires them:
// the document stage over this machine's gs, reading each page's text layer; office over the
// office mise pins, LibreOffice through the container runner in its pinned image, and magick;
// the picture stage over magick and LibreOffice; the vision stage, which passes a picture
// through; the email stage over the mail mise pins; the onenote stage over the onenote mise
// pins; and the zip stage under the default thresholds.
func findingStages(ctx context.Context) (dispatch.Stages, error) {
	execs := map[string]*tool.Tool{}
	for name, spec := range map[string]tool.Spec{
		"gs": document.GhostscriptSpec(), "magick": picture.MagickSpec(), "office": office.OfficeSpec(),
		"mail": email.MailSpec(), "onenote": onenote.OneNoteSpec(),
	} {
		tl, err := tool.NewExec(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		execs[name] = tl
	}
	lo, err := tool.NewContainer(ctx, "", tool.Image{Ref: picture.LibreOfficeImage}, picture.LibreOfficeSpec())
	if err != nil {
		return nil, err
	}
	gs, mg, of := execs["gs"], execs["magick"], execs["office"]
	render, err := document.NewGhostscript(gs)
	if err != nil {
		return nil, err
	}
	prep, err := document.NewPreparer(render, nil)
	if err != nil {
		return nil, err
	}
	docs, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{
		Preparer: prep, Version: dispatch.Versions(gs), Request: document.ImagesRequest{Text: true},
	})
	if err != nil {
		return nil, err
	}
	oc, err := office.NewConverter(office.Config{Office: of, LibreOffice: lo, Magick: mg})
	if err != nil {
		return nil, err
	}
	offices, err := dispatch.NewOfficeStage(dispatch.OfficeConfig{Converter: oc, Version: dispatch.Versions(of, lo, mg)})
	if err != nil {
		return nil, err
	}
	pc, err := picture.NewConverter(picture.Config{Magick: mg, LibreOffice: lo})
	if err != nil {
		return nil, err
	}
	pictures, err := dispatch.NewPictureStage(dispatch.PictureConfig{Converter: pc, Version: dispatch.Versions(mg, lo)})
	if err != nil {
		return nil, err
	}
	mr, err := email.NewReader(email.Config{Mail: execs["mail"]})
	if err != nil {
		return nil, err
	}
	mails, err := dispatch.NewEmailStage(dispatch.EmailConfig{Reader: mr, Version: dispatch.Versions(execs["mail"])})
	if err != nil {
		return nil, err
	}
	nr, err := onenote.NewReader(onenote.Config{OneNote: execs["onenote"]})
	if err != nil {
		return nil, err
	}
	notes, err := dispatch.NewOneNoteStage(dispatch.OneNoteConfig{Reader: nr, Version: dispatch.Versions(execs["onenote"])})
	if err != nil {
		return nil, err
	}
	zips, err := dispatch.NewZipStage(dispatch.ZipConfig{})
	if err != nil {
		return nil, err
	}
	return dispatch.Stages{
		route.Document:        docs,
		route.Office:          offices,
		route.VisionConverted: pictures,
		route.Vision:          dispatch.NewVisionStage(),
		route.Nested:          mails,
		route.OneNote:         notes,
		route.Archive:         zips,
	}, nil
}

// treeBytes returns the regular files under dir and the bytes they hold, a hard link's once. A
// file or directory removed while it walks is passed over, as the dispatcher removes them.
func treeBytes(dir string) (int, int64, error) {
	seen := map[uint64]bool{}
	var n int
	var size int64
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if seen[st.Ino] {
				return nil
			}
			seen[st.Ino] = true
		}
		n++
		size += info.Size()
		return nil
	})
	return n, size, err
}

// diskSampleEvery is how often a dispatch's working directory is walked for its bytes.
const diskSampleEvery = 10 * time.Millisecond

// diskSampler walks a directory every diskSampleEvery until stopped, keeping the most bytes it
// held, and the samples taken.
type diskSampler struct {
	peak, samples atomic.Int64
	err           error
	stop          chan struct{}
	done          sync.WaitGroup
}

// sampleDisk starts sampling dir.
func sampleDisk(dir string) *diskSampler {
	s := &diskSampler{stop: make(chan struct{})}
	s.done.Go(func() {
		tick := time.NewTicker(diskSampleEvery)
		defer tick.Stop()
		for {
			s.sample(dir)
			select {
			case <-s.stop:
				return
			case <-tick.C:
			}
		}
	})
	return s
}

// sample walks dir once.
func (s *diskSampler) sample(dir string) {
	_, n, err := treeBytes(dir)
	if err != nil {
		s.err = err
		return
	}
	s.samples.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

// end stops the sampling and returns its peak and samples.
func (s *diskSampler) end() (int64, int64, error) {
	close(s.stop)
	s.done.Wait()
	return s.peak.Load(), s.samples.Load(), s.err
}

// dispatchJob is what a dispatching process dispatches: its inputs, over Workers workers, or, at
// 0, the one input by Dispatch alone, in Work, a working directory, over a file index in Index.
type dispatchJob struct {
	Inputs  []string `json:"inputs"`
	Workers int      `json:"workers"`
	Work    string   `json:"work"`
	Index   string   `json:"index"`
}

// inputDone is one input's own event, as a dispatch survey records it: its outcome, its outputs
// and children, and when it came, from the dispatch's start.
type inputDone struct {
	Input    string           `json:"input"`
	Size     int64            `json:"size"`
	Outcome  dispatch.Outcome `json:"outcome"`
	Recorded dispatch.Outcome `json:"recorded,omitzero"`
	Reason   string           `json:"reason,omitzero"`
	Outputs  int              `json:"outputs"`
	Children int              `json:"children"`
	At       time.Duration    `json:"at"`
}

// dispatchSurvey is what one dispatch found and cost.
type dispatchSurvey struct {
	Workers    int   `json:"workers"`
	InputBytes int64 `json:"input_bytes"`
	// Parts counts every part's own event, the inputs' among them, and Counts them by "route ×
	// outcome".
	Parts  int            `json:"parts"`
	Counts map[string]int `json:"counts"`
	// SeenInputs and SeenParts are the dedupe hits: inputs, and parts below an input, the index
	// already held.
	SeenInputs int `json:"seen_inputs"`
	SeenParts  int `json:"seen_parts"`
	// Messages are the inputs' own messages, their records prepared; SeenMessages those streamed
	// from the index for an input seen.
	Messages     int `json:"messages"`
	SeenMessages int `json:"seen_messages"`
	// Outputs counts every output event.
	Outputs int `json:"outputs"`
	// Failed holds each failed part's reason, by its name.
	Failed []string `json:"failed,omitzero"`
	// Tops is each input's own event, as it came.
	Tops []inputDone `json:"tops"`
	// Elapsed is from before the first input is dispatched until the last event.
	Elapsed time.Duration `json:"elapsed"`
	// PeakDisk is the most bytes the working directory held, sampled every diskSampleEvery, of
	// DiskSamples samples; IndexFiles and IndexBytes are the index's directory once done.
	PeakDisk    int64 `json:"peak_disk"`
	DiskSamples int64 `json:"disk_samples"`
	IndexFiles  int   `json:"index_files"`
	IndexBytes  int64 `json:"index_bytes"`
}

// ownMessage reports whether o is the record of a message of its read's own, not an embedded one
// nor an attachment's record.
func ownMessage(o *dispatch.Output) bool {
	return o.Kind == dispatch.RecordOutput && strings.HasPrefix(o.Name, "message") &&
		!strings.Contains(o.Name, ",") && o.Meta["embedded_in"] == ""
}

// take counts ev, an event of in's dispatch, began being the dispatch's start.
func (s *dispatchSurvey) take(in string, ev dispatch.Event, began time.Time) {
	if o := ev.Output; o != nil {
		s.Outputs++
		if ownMessage(o) && ev.Stage.Name == "email" {
			if ev.Outcome == dispatch.Seen {
				s.SeenMessages++
			} else {
				s.Messages++
			}
		}
		return
	}
	s.Parts++
	s.Counts[string(ev.Part.Route)+" × "+string(ev.Outcome)]++
	if ev.Outcome == dispatch.Seen {
		if ev.Part.Depth == 0 {
			s.SeenInputs++
		} else {
			s.SeenParts++
		}
	}
	if ev.Outcome == dispatch.Failed {
		s.Failed = append(s.Failed, ev.Part.Name+": "+ev.Reason)
	}
	if ev.Part.Depth == 0 {
		s.Tops = append(s.Tops, inputDone{in, ev.Part.Size, ev.Outcome, ev.Recorded, ev.Reason, ev.Outputs, ev.Children, time.Since(began)})
	}
}

// dispatchJobOf is the dispatching process: it dispatches the job args[0] holds, as a caller
// that persists nothing would, every output and part left to the dispatcher, and surveys it.
func dispatchJobOf(ctx context.Context, args []string) (dispatchSurvey, error) {
	var job dispatchJob
	s := dispatchSurvey{Counts: map[string]int{}}
	if len(args) != 1 {
		return s, errors.New("dispatch: want a job")
	}
	if err := json.Unmarshal([]byte(args[0]), &job); err != nil {
		return s, err
	}
	s.Workers = job.Workers
	for _, in := range job.Inputs {
		info, err := os.Stat(in)
		if err != nil {
			return s, err
		}
		s.InputBytes += info.Size()
	}
	stages, err := findingStages(ctx)
	if err != nil {
		return s, err
	}
	index, err := dispatch.NewFileIndex(job.Index, dispatch.FileOptions{})
	if err != nil {
		return s, err
	}
	d, err := dispatch.New(dispatch.Config{Stages: stages, Index: index, Dir: job.Work})
	if err != nil {
		return s, err
	}
	disk := sampleDisk(job.Work)
	began := time.Now()
	if job.Workers == 0 {
		if len(job.Inputs) != 1 {
			return s, errors.New("dispatch: a dispatch alone takes one input")
		}
		for ev, err := range d.Dispatch(ctx, dispatch.Input{Path: job.Inputs[0]}) {
			if err != nil {
				return s, err
			}
			s.take(job.Inputs[0], ev, began)
		}
	} else {
		for pe, err := range d.Pool(ctx, slices.Values(inputsOf(job.Inputs)), job.Workers) {
			if err != nil {
				return s, fmt.Errorf("%s: %w", pe.Input.Path, err)
			}
			s.take(pe.Input.Path, pe.Event, began)
		}
	}
	s.Elapsed = time.Since(began)
	if s.PeakDisk, s.DiskSamples, err = disk.end(); err != nil {
		return s, err
	}
	if s.IndexFiles, s.IndexBytes, err = treeBytes(job.Index); err != nil {
		return s, err
	}
	return s, nil
}

// inputsOf returns paths as inputs.
func inputsOf(paths []string) []dispatch.Input {
	ins := make([]dispatch.Input, len(paths))
	for i, p := range paths {
		ins[i] = dispatch.Input{Path: p}
	}
	return ins
}

// readSurvey is what one read through an adapter alone found: its records, the files the read
// named and their routes, its warnings, and the read's time.
type readSurvey struct {
	Records  map[string]int      `json:"records"`
	Named    int                 `json:"named"`
	Routes   map[route.Route]int `json:"routes,omitzero"`
	Warnings int                 `json:"warnings"`
	Elapsed  time.Duration       `json:"elapsed"`
}

// readMail is the email adapter alone: it reads args[0] through a Reader over the mail on PATH,
// its attachments into args[1], routing every attachment, a nested message's among them.
func readMail(ctx context.Context, args []string) (readSurvey, error) {
	s := readSurvey{Records: map[string]int{}, Routes: map[route.Route]int{}}
	if len(args) != 2 {
		return s, errors.New("email: want an input and an attachments directory")
	}
	tl, err := tool.NewExec(ctx, email.MailSpec())
	if err != nil {
		return s, err
	}
	r, err := email.NewReader(email.Config{Mail: tl})
	if err != nil {
		return s, err
	}
	start := time.Now()
	stream, err := r.Read(ctx, email.Request{Input: args[0], Attachments: args[1]})
	if err != nil {
		return s, err
	}
	var walk func(m *email.Message) error
	walk = func(m *email.Message) error {
		for i := range m.Attachments {
			a := &m.Attachments[i]
			s.Named++
			rt, _, err := a.Route()
			if err != nil {
				return err
			}
			s.Routes[rt]++
			if a.Message != nil {
				if err := walk(a.Message); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for rec, err := range stream.Records() {
		if err != nil {
			return s, err
		}
		s.Records[fmt.Sprintf("%T", rec)]++
		if m, ok := rec.(*email.Message); ok {
			if err := walk(m); err != nil {
				return s, err
			}
		}
	}
	s.Elapsed = time.Since(start)
	s.Warnings = len(stream.Warnings())
	return s, nil
}

// readOneNote is the onenote adapter alone: it reads args[0] through a Reader over the onenote on
// PATH, with args[1] as the media directory when it is given and not empty, every page's media
// iterated and, with a directory, routed.
func readOneNote(ctx context.Context, args []string) (readSurvey, error) {
	s := readSurvey{Records: map[string]int{}, Routes: map[route.Route]int{}}
	if len(args) < 1 || len(args) > 2 {
		return s, errors.New("onenote: want an input and an optional media directory")
	}
	media := ""
	if len(args) == 2 {
		media = args[1]
	}
	tl, err := tool.NewExec(ctx, onenote.OneNoteSpec())
	if err != nil {
		return s, err
	}
	r, err := onenote.NewReader(onenote.Config{OneNote: tl})
	if err != nil {
		return s, err
	}
	start := time.Now()
	stream, err := r.Read(ctx, onenote.Request{Input: args[0], Media: media})
	if err != nil {
		return s, err
	}
	for rec, err := range stream.Records() {
		if err != nil {
			return s, err
		}
		s.Records[rec.Line()]++
		p, ok := rec.(*onenote.Page)
		if !ok {
			continue
		}
		for m, err := range p.Media(media) {
			if err != nil {
				return s, err
			}
			s.Named++
			if media != "" {
				s.Routes[m.Route]++
			}
		}
	}
	s.Elapsed = time.Since(start)
	s.Warnings = len(stream.Warnings())
	return s, nil
}

// findingsRuns returns ONENOTE_FINDINGS_RUNS, 3 when unset.
func findingsRuns(t *testing.T) int {
	t.Helper()
	v := os.Getenv(runsEnv)
	if v == "" {
		return 3
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("%s %q: want a positive number", runsEnv, v)
	}
	return n
}

// saveFindings writes v as JSON to name in ONENOTE_FINDINGS_RESULTS, when it is set.
func saveFindings(t *testing.T, name string, v any) {
	t.Helper()
	dir := os.Getenv(resultsEnv)
	if dir == "" {
		return
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mib writes n bytes in MiB.
func mib(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// median returns the median of ds.
func median(ds []time.Duration) time.Duration {
	s := slices.Sorted(slices.Values(ds))
	return s[len(s)/2]
}

// floor launches a process that does nothing and logs its maxrss, the least any launched process
// reads.
func floor(t *testing.T, name string) usage {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := launch(t.Context(), "floor", []string{exe}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded: %s floor: maxrss %s", name, mib(u.MaxRSS))
	return u
}

// timedRun is one timed run of a measured way.
type timedRun struct {
	Way string `json:"way"`
	// Launched is the measured process's rusage and wall time, as the launcher reports them:
	// wait4 folds its reaped children into it, so its maxrss is the largest of the process's and
	// any child's.
	Launched usage `json:"launched"`
	// Self and Children are a measuring process's own report, for an adapter's or the
	// dispatcher's way.
	Self     *usage `json:"self,omitzero"`
	Children *usage `json:"children,omitzero"`
	// Survey is a measuring process's survey.
	Survey any `json:"survey,omitzero"`
	// WrittenFiles and WrittenBytes are what the run left in its media or attachments directory.
	WrittenFiles int   `json:"written_files,omitzero"`
	WrittenBytes int64 `json:"written_bytes,omitzero"`
}

// line writes rn for a recorded line.
func (rn timedRun) line() string {
	u := rn.Launched
	line := fmt.Sprintf("wall %v, user %v, system %v, maxrss %s", u.Wall.Round(time.Millisecond),
		u.User.Round(time.Millisecond), u.System.Round(time.Millisecond), mib(u.MaxRSS))
	if rn.Self != nil {
		line += fmt.Sprintf("; own maxrss %s, user %v, system %v; children's maxrss %s, user %v, system %v",
			mib(rn.Self.MaxRSS), rn.Self.User.Round(time.Millisecond), rn.Self.System.Round(time.Millisecond),
			mib(rn.Children.MaxRSS), rn.Children.User.Round(time.Millisecond), rn.Children.System.Round(time.Millisecond))
	}
	if rn.WrittenFiles > 0 {
		line += fmt.Sprintf("; wrote %d files, %d bytes", rn.WrittenFiles, rn.WrittenBytes)
	}
	return line
}

// way is one way an input is measured: a name, and a run that returns one timed run, given a
// directory of its own for what it writes.
type way struct {
	name string
	run  func(dir string) (timedRun, error)
}

// cliWay runs program alone over args, its stdout to /dev/null.
func cliWay(ctx context.Context, name, program string, args func(dir string) []string) way {
	return way{name, func(dir string) (timedRun, error) {
		u, _, err := launch(ctx, "", append([]string{program}, args(dir)...), false)
		return timedRun{Way: name, Launched: u}, err
	}}
}

// adapterWay runs a measuring process of mode child over args, its survey decoded into a fresh
// value newSurvey returns.
func adapterWay(ctx context.Context, name, child string, args func(dir string) []string, newSurvey func() any) way {
	return way{name, func(dir string) (timedRun, error) {
		s := newSurvey()
		u, r, err := launchReport(ctx, child, args(dir), s)
		if err != nil {
			return timedRun{}, err
		}
		return timedRun{Way: name, Launched: u, Self: &r.Self, Children: &r.Children, Survey: s}, nil
	}}
}

// measuredInput is one input's timed runs, each way.
type measuredInput struct {
	Input string     `json:"input"`
	Path  string     `json:"path"`
	Size  int64      `json:"size"`
	Floor usage      `json:"floor"`
	Runs  []timedRun `json:"runs"`
}

// measureWays runs each way runs times, back to back, each in a fresh directory removed after,
// logging each run and each way's median wall time and peak memory, its own and its children's.
func measureWays(t *testing.T, name, path string, runs int, ways []way) measuredInput {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	res := measuredInput{Input: name, Path: path, Size: info.Size(), Floor: floor(t, name)}
	for _, w := range ways {
		var walls []time.Duration
		var peak, own, children int64
		for i := range runs {
			dir, err := os.MkdirTemp("", "findings-")
			if err != nil {
				t.Fatal(err)
			}
			rn, err := w.run(dir)
			if err != nil {
				t.Fatal(err)
			}
			if rn.WrittenFiles, rn.WrittenBytes, err = treeBytes(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			walls = append(walls, rn.Launched.Wall)
			peak = max(peak, rn.Launched.MaxRSS)
			if rn.Self != nil {
				own, children = max(own, rn.Self.MaxRSS), max(children, rn.Children.MaxRSS)
			}
			t.Logf("recorded: %s %s run %d: %s", name, w.name, i+1, rn.line())
			res.Runs = append(res.Runs, rn)
		}
		line := fmt.Sprintf("recorded: %s %s, %d runs: median wall %v, peak memory %s", name, w.name, runs,
			median(walls).Round(time.Millisecond), mib(peak))
		if own > 0 {
			line += fmt.Sprintf(", its own %s, its children's %s", mib(own), mib(children))
		}
		t.Log(line)
	}
	return res
}

// largeSections are the three large sections onenote's own finding times, under ONENOTE_LARGE.
var largeSections = []string{"LargeData.one", "Large Desktop.one", "Large OneDrive.one"}

// TestFindingsOneNoteLarge times the onenote adapter, a Reader reading every record and every
// page's media, and the onenote CLI alone, each with and without a media directory, on each of the
// large sections ONENOTE_LARGE holds, with their peak memory.
func TestFindingsOneNoteLarge(t *testing.T) {
	dir := os.Getenv(largeEnv)
	if dir == "" {
		t.Skip(largeEnv + " is unset")
	}
	cli, err := exec.LookPath(onenote.OneNoteSpec().Program)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	runs := findingsRuns(t)
	newSurvey := func() any { return &readSurvey{} }
	var results []measuredInput
	for _, name := range largeSections {
		path := filepath.Join(dir, name)
		results = append(results, measureWays(t, name, path, runs, []way{
			adapterWay(ctx, "adapter", "onenote", func(string) []string { return []string{path} }, newSurvey),
			adapterWay(ctx, "adapter, media", "onenote", func(d string) []string { return []string{path, d} }, newSurvey),
			cliWay(ctx, "onenote", cli, func(string) []string { return []string{path} }),
			cliWay(ctx, "onenote, media", cli, func(d string) []string { return []string{"--media", d, path} }),
		}))
	}
	saveFindings(t, "onenote-large.json", results)
}

// scaleStore returns the scale store ONENOTE_SCALE_STORE names, skipping t when it is unset, and
// logs the generator's record beside it, which it returns.
func scaleStore(t *testing.T) (string, any) {
	t.Helper()
	path := os.Getenv(scaleEnv)
	if path == "" {
		t.Skip(scaleEnv + " is unset")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "scale.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	} else if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	t.Logf("scale store %s: content SHA-256 %v, parameters %v", path, record["content_sha256"], record["parameters"])
	return path, record
}

// dispatchWay dispatches the inputs args returns over workers, 0 for a Dispatch alone, in a
// working directory and over a fresh file index, both in the run's directory.
func dispatchWay(ctx context.Context, name string, workers int, inputs []string) way {
	return way{name, func(dir string) (timedRun, error) {
		job := dispatchJob{Inputs: inputs, Workers: workers, Work: filepath.Join(dir, "work"), Index: filepath.Join(dir, "index")}
		if err := os.Mkdir(job.Work, 0o700); err != nil {
			return timedRun{}, err
		}
		b, err := json.Marshal(job)
		if err != nil {
			return timedRun{}, err
		}
		s := &dispatchSurvey{}
		u, r, err := launchReport(ctx, "dispatch", []string{string(b)}, s)
		if err != nil {
			return timedRun{}, err
		}
		return timedRun{Way: name, Launched: u, Self: &r.Self, Children: &r.Children, Survey: s}, nil
	}}
}

// countsLine writes counts sorted by key.
func countsLine(counts map[string]int) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

// surveyLine writes s for a recorded line.
func surveyLine(s *dispatchSurvey) string {
	return fmt.Sprintf("dispatch %v, %d parts (%s), %d seen inputs, %d seen parts, %d messages, %d seen messages, %d outputs, %d failed; peak disk %d bytes of %d samples; index %d files, %d bytes",
		s.Elapsed.Round(time.Millisecond), s.Parts, countsLine(s.Counts), s.SeenInputs, s.SeenParts, s.Messages, s.SeenMessages,
		s.Outputs, len(s.Failed), s.PeakDisk, s.DiskSamples, s.IndexFiles, s.IndexBytes)
}

// TestFindingsScaleDispatch dispatches the scale store through every stage that needs no model,
// over a fresh file index each run, and times it beside the email adapter alone and mail alone,
// each writing the store's attachments: the dispatcher's time, its peak memory and its children's,
// and its working directory's peak bytes, the CLI's originals released as each is settled.
func TestFindingsScaleDispatch(t *testing.T) {
	path, record := scaleStore(t)
	mail, err := exec.LookPath(email.MailSpec().Program)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	res := measureWays(t, "scale", path, findingsRuns(t), []way{
		dispatchWay(ctx, "dispatch", 0, []string{path}),
		adapterWay(ctx, "email adapter, attachments", "email", func(d string) []string { return []string{path, d} },
			func() any { return &readSurvey{} }),
		cliWay(ctx, "mail, attachments", mail, func(d string) []string { return []string{"--attachments", d, path} }),
	})
	for _, rn := range res.Runs {
		if s, ok := rn.Survey.(*dispatchSurvey); ok {
			t.Logf("recorded: scale %s: %s", rn.Way, surveyLine(s))
			if len(s.Failed) > 0 {
				t.Errorf("scale: failed parts: %q", s.Failed)
			}
		}
	}
	saveFindings(t, "scale-dispatch.json", map[string]any{"record": record, "result": res})
}

// cursorCaller is a caller dispatching a mail store by the cursor rule of the package doc's
// "Resuming a store", as storeCaller is, but for the Threader: storeCaller saves it at every
// message, which costs a marshal of every message's threading keys so far, each message, and so
// grows with the square of the store, as the doc says; cursorCaller saves it only when asked,
// with the cursor.
type cursorCaller struct {
	// committed are the committed message outputs, as "kind name"; messages counts the messages
	// among them.
	committed []string
	messages  int
	// pending are the outputs of the message whose last output hasn't come.
	pending []string
	// cursor is the last committed message's id.
	cursor  uint64
	threads email.Threader
	// last is the last part's own event, the input's once the dispatch is done; seen counts the
	// parts seen.
	last dispatch.Event
	seen int
}

// take takes ev, as the caller's loop body would, and reports whether it committed a message.
func (c *cursorCaller) take(t *testing.T, ev dispatch.Event) bool {
	t.Helper()
	if ev.Output == nil {
		c.last = ev
		if ev.Outcome == dispatch.Seen {
			c.seen++
		}
		return false
	}
	o := ev.Output
	if ev.Part.Depth > 0 || !strings.HasPrefix(o.Name, "message") {
		return false
	}
	o.Ref = "persisted " + o.Name
	c.pending = append(c.pending, string(o.Kind)+" "+o.Name)
	if o.Meta["embedded_in"] != "" || o.Meta["id"] == "" {
		return false
	}
	if o.Kind == dispatch.RecordOutput {
		var m email.Message
		if err := jsonv2.Unmarshal(o.Value, &m); err != nil {
			t.Fatal(err)
		}
		c.threads.Add(&m)
		if o.Meta["markdown"] == "follows" {
			return false
		}
	}
	id, err := strconv.ParseUint(o.Meta["id"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c.committed = append(c.committed, c.pending...)
	c.pending = nil
	c.cursor = id
	c.messages++
	return true
}

// resumeAfter is the messages the stopped dispatch commits before its loop stops.
const resumeAfter = 25000

// TestFindingsResume dispatches the scale store whole, then, over a fresh file index, again
// until the caller's cursor rule, the package doc's "Resuming a store", has committed
// resumeAfter messages, where its loop stops. The cursor and the Threader, saved with it, are
// written to files and read back, and the store is dispatched again over the same index with
// After set to the cursor. The committed message outputs of the two must equal the whole
// dispatch's, in order, none twice, and the resumed threads the whole read's. It records each
// dispatch's time, and what the Threader's save costs at the cursor.
func TestFindingsResume(t *testing.T) {
	path, _ := scaleStore(t)
	stages, err := findingStages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	timed := func(d *dispatch.Dispatcher, in dispatch.Input, c *cursorCaller, stop bool) time.Duration {
		start := time.Now()
		for ev, err := range d.Dispatch(t.Context(), in) {
			if err != nil {
				t.Fatal(err)
			}
			if c.take(t, ev) && stop && c.messages == resumeAfter {
				break
			}
		}
		return time.Since(start)
	}

	whole := &cursorCaller{}
	wholeTook := timed(newDispatcher(t, stages, fileIndex(t, t.TempDir(), 0)), dispatch.Input{Path: path}, whole, false)
	if len(whole.pending) != 0 {
		t.Fatalf("the whole dispatch left %d outputs uncommitted", len(whole.pending))
	}
	t.Logf("recorded: resume whole: %v, %d messages, %d message outputs committed", wholeTook.Round(time.Millisecond),
		whole.messages, len(whole.committed))

	indexDir, state := t.TempDir(), t.TempDir()
	first := &cursorCaller{}
	firstTook := timed(newDispatcher(t, stages, fileIndex(t, indexDir, 0)), dispatch.Input{Path: path}, first, true)
	began := time.Now()
	saved, err := first.threads.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	saveTook := time.Since(began)
	cursorFile, threaderFile := filepath.Join(state, "cursor"), filepath.Join(state, "threader")
	if err := os.WriteFile(cursorFile, []byte(strconv.FormatUint(first.cursor, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(threaderFile, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded: resume first: stopped at cursor %d after %v, %d messages, %d message outputs committed, %d pending; the Threader saved in %d bytes, in %v",
		first.cursor, firstTook.Round(time.Millisecond), first.messages, len(first.committed), len(first.pending), len(saved), saveTook.Round(time.Microsecond))
	if first.messages != resumeAfter {
		t.Fatalf("stopped after %d messages, want %d", first.messages, resumeAfter)
	}

	b, err := os.ReadFile(cursorFile)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(threaderFile)
	if err != nil {
		t.Fatal(err)
	}
	resumed := &cursorCaller{}
	if err := resumed.threads.UnmarshalBinary(restored); err != nil {
		t.Fatal(err)
	}
	resumedTook := timed(newDispatcher(t, stages, fileIndex(t, indexDir, 0)), dispatch.Input{Path: path, After: cursor}, resumed, false)
	if len(resumed.pending) != 0 {
		t.Errorf("the resumed dispatch left %d outputs uncommitted", len(resumed.pending))
	}

	union := slices.Concat(first.committed, resumed.committed)
	twice := 0
	once := map[string]bool{}
	for _, o := range union {
		if once[o] {
			twice++
		}
		once[o] = true
	}
	equal := slices.Equal(union, whole.committed)
	if !equal || twice > 0 {
		t.Errorf("committed message outputs: %d, %d twice; want the whole dispatch's %d, in order", len(union), twice, len(whole.committed))
	}
	wantThreads, gotThreads := whole.threads.Threads(), resumed.threads.Threads()
	threadsEqual := slices.EqualFunc(gotThreads, wantThreads, threadEqual)
	if !threadsEqual {
		t.Errorf("the resumed threads differ from the whole read's: %d roots, want %d", len(gotThreads), len(wantThreads))
	}
	store := resumed.last
	t.Logf("recorded: resume resumed: after %d, %v, %d messages, %d message outputs committed; the store %s (recorded %q), %d parts seen",
		cursor, resumedTook.Round(time.Millisecond), resumed.messages, len(resumed.committed), store.Outcome, store.Recorded, resumed.seen)
	t.Logf("recorded: resume union: %d message outputs, %d twice, equal to the whole dispatch's in order %v; threads %d roots, equal %v; resumed %v against whole %v (%.2f), first %v",
		len(union), twice, equal, len(gotThreads), threadsEqual, resumedTook.Round(time.Millisecond), wholeTook.Round(time.Millisecond),
		resumedTook.Seconds()/wholeTook.Seconds(), firstTook.Round(time.Millisecond))
	saveFindings(t, "resume.json", map[string]any{
		"store": path, "resume_after": resumeAfter, "cursor": cursor,
		"threader": map[string]any{"bytes": len(saved), "save": saveTook},
		"whole":    map[string]any{"elapsed": wholeTook, "messages": whole.messages, "committed": len(whole.committed)},
		"first":    map[string]any{"elapsed": firstTook, "messages": first.messages, "committed": len(first.committed), "pending": len(first.pending)},
		"resumed": map[string]any{"elapsed": resumedTook, "messages": resumed.messages, "committed": len(resumed.committed),
			"store_outcome": store.Outcome, "store_recorded": store.Recorded, "seen": resumed.seen},
		"union": map[string]any{"committed": len(union), "twice": twice, "equal": equal, "threads": len(gotThreads), "threads_equal": threadsEqual},
	})
}

// threadEqual reports whether a and b are the same tree: their ids, Message-IDs, placeholders,
// and children, in order.
func threadEqual(a, b *email.Thread) bool {
	return a.ID == b.ID && a.MessageID == b.MessageID && a.Placeholder == b.Placeholder &&
		slices.EqualFunc(a.Children, b.Children, threadEqual)
}

// mailFixtureInputs returns mail's fixtures, the inputs among the email package's copy of them:
// every .pst, .msg, .eml, and mbox, sorted.
func mailFixtureInputs(t *testing.T) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(mailFixtures, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch ext := filepath.Ext(path); {
		case ext == ".pst" || ext == ".msg" || ext == ".eml" || ext == ".mboxrd" || strings.HasPrefix(d.Name(), "mbox."):
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			paths = append(paths, abs)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	return paths
}

// fileSHA256 returns the SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scaleCopy returns the byte copy of the scale store at path ONENOTE_SCALE_COPY names, copying it
// there when it is absent, and refusing one whose size isn't the store's.
func scaleCopy(t *testing.T, store string) string {
	t.Helper()
	dst := os.Getenv(scaleCopyEnv)
	if dst == "" {
		t.Skip(scaleCopyEnv + " is unset")
	}
	want, err := os.Stat(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := copyBytes(store, dst); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size() != want.Size() {
		t.Fatalf("%s is %d bytes, the scale store %d", dst, got.Size(), want.Size())
	}
	return dst
}

// copyBytes copies src to dst through a temporary file renamed into place.
func copyBytes(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err := errors.Join(err, out.Close()); err != nil {
		return err
	}
	return os.Rename(part, dst)
}

// TestFindingsPool dispatches plaso's store, the scale store, a byte copy of it, and mail's
// fixtures through the pool at 1, 2, 4, and 8 workers, each over a fresh file index: the
// throughput, the dedupe hits, the copy seen whole and the attachments the inputs share, and the
// peak memory and disk.
func TestFindingsPool(t *testing.T) {
	store, _ := scaleStore(t)
	plaso := os.Getenv(plasoEnv)
	if plaso == "" {
		t.Skip(plasoEnv + " is unset")
	}
	if sum, err := fileSHA256(plaso); err != nil || sum != plasoSHA256 {
		t.Fatalf("%s isn't plaso's store: SHA-256 %s, %v; want %s (mise run findings-email fetches it)", plaso, sum, err, plasoSHA256)
	}
	cp := scaleCopy(t, store)
	inputs := slices.Concat([]string{plaso, store, cp}, mailFixtureInputs(t))
	t.Logf("pool inputs: plaso's store, the scale store, its copy %s, and %d of mail's fixtures", cp, len(inputs)-3)
	ctx := t.Context()
	var results []measuredInput
	for _, workers := range []int{1, 2, 4, 8} {
		name := fmt.Sprintf("pool %d", workers)
		res := measureWays(t, name, store, 1, []way{dispatchWay(ctx, "dispatch", workers, inputs)})
		res.Input, res.Path, res.Size = name, "", 0
		for _, rn := range res.Runs {
			s := rn.Survey.(*dispatchSurvey)
			mbs := float64(s.InputBytes) / (1 << 20) / s.Elapsed.Seconds()
			t.Logf("recorded: %s: %s; %d input bytes, %.1f MiB/s, %.0f messages/s", name, surveyLine(s), s.InputBytes, mbs,
				float64(s.Messages+s.SeenMessages)/s.Elapsed.Seconds())
			for _, top := range s.Tops {
				if top.Size > 1<<20 {
					t.Logf("recorded: %s: input %s, %d bytes: %s (recorded %q), %d outputs, %d children, done at %v", name, top.Input,
						top.Size, top.Outcome, top.Recorded, top.Outputs, top.Children, top.At.Round(time.Millisecond))
				}
			}
			if len(s.Failed) > 0 {
				t.Errorf("%s: failed parts: %q", name, s.Failed)
			}
		}
		results = append(results, res)
	}
	saveFindings(t, "pool.json", map[string]any{"inputs": inputs, "results": results})
}
