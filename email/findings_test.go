//go:build findings && linux

package email_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The findings tier measures the adapter for evidence 16, never in CI or the check: threads and
// routes over every fixture and MIME input, scored where the tests hold a hand truth; and, on
// plaso's 59 MB store, the scale store, and the scale mboxrd, threads and routes counted and the
// adapter's time and peak memory, threads included, beside mail's own. `mise run findings-email`
// runs it; what it found is in docs/findings/email.md. Its settings, from the environment:
//
//   - EMAIL_PLASO_STORE names where plaso's store is kept: fetched there at plasoCommit when it is
//     absent, and refused unless its SHA-256 is plasoSHA256. Unset skips plaso's store.
//   - EMAIL_SCALE_STORE names the scale store, the scale.pst mail's generator builds
//     (MISE_ENV=fixtures mise run scale, in a checkout of mail's tag); a scale.json beside it is
//     logged. Unset skips the scale store. Neither store is ever committed.
//   - EMAIL_SCALE_MBOX names the scale mboxrd, the scale.mboxrd the same generator writes beside
//     scale.pst, the same messages; a scale.json beside it is logged. Unset skips it. It is never
//     committed.
//   - EMAIL_FINDINGS_RUNS is the timed runs of each large store each way, 3 when unset.
//   - EMAIL_FINDINGS_RESULTS names a directory each test writes its results to as JSON; unset
//     writes nothing, so a run keeps its results only when asked to.
const (
	plasoEnv   = "EMAIL_PLASO_STORE"
	scaleEnv   = "EMAIL_SCALE_STORE"
	mboxEnv    = "EMAIL_SCALE_MBOX"
	runsEnv    = "EMAIL_FINDINGS_RUNS"
	resultsEnv = "EMAIL_FINDINGS_RESULTS"
)

// plaso's store, test_data/nromanoff@stark-research-labs.com.pst from log2timeline/plaso
// (Apache-2.0), at the commit that added it, by its SHA-256: the pin mail's own finding fetches
// (mail's scripts/findings.sh).
const (
	plasoCommit = "f5bf35fcc102585eed58b930392da56a318690f6"
	plasoSHA256 = "bab64f3589f8ce758df9e595b89a5e4b9be159929e49100397152fdcc7bd6013"
	plasoURL    = "https://raw.githubusercontent.com/log2timeline/plaso/" + plasoCommit +
		"/test_data/nromanoff%40stark-research-labs.com.pst"
)

// The measurement processes. A process's peak resident memory, ru_maxrss, can't be read from the
// test process: Go starts a program with a vfork-style clone, and on exec Linux carries the
// parent's high-water mark into the program's maxrss (tool.Usage), so every program the test runs
// would read at least the test's own peak. The test binary is re-executed instead, with modeEnv
// naming its part:
//
//   - "launch" runs its arguments as a program, with modeEnv set to childEnv's value, and writes
//     the program's wait4 rusage to stderr after it, as a last line that begins launchPrefix. Its
//     own high-water mark, a small process's, is the floor the program's maxrss starts at.
//   - "measure" is the adapter under measurement: it builds a Reader over the mail on PATH, reads
//     its first argument, with its second as the attachments directory when not empty, feeds every
//     record to a Threader, routes every attachment when it has a directory, builds the threads,
//     and writes a survey as JSON to stdout. Its own rusage is the adapter's; its children's is
//     mail's, the version command's and the read's, whose maxrss floor is the measuring process's
//     high-water mark when it started mail, before any record was read.
//   - "floor" exits at once: launched, it measures the floor.
const (
	modeEnv      = "EMAIL_FINDINGS_MODE"
	childEnv     = "EMAIL_FINDINGS_CHILD"
	launchPrefix = "findings-launch: "
)

func init() {
	switch os.Getenv(modeEnv) {
	case "launch":
		os.Exit(launcher(os.Args[1:]))
	case "measure":
		os.Exit(measure(os.Args[1:]))
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

// launcher runs args as a program with modeEnv set to childEnv's value and reports its rusage and
// wall time, returning its exit code.
func launcher(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "launcher: no program")
		return 2
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), modeEnv+"="+os.Getenv(childEnv))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	if cmd.ProcessState != nil {
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			u := fromRusage(ru)
			u.Wall = wall
			line, _ := json.Marshal(u)
			fmt.Fprintf(os.Stderr, "\n%s%s\n", launchPrefix, line)
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

// measured is what the measuring process writes: its survey, its own rusage, and its children's,
// mail's.
type measured struct {
	Survey survey `json:"survey"`
	Self   usage  `json:"self"`
	Mail   usage  `json:"mail"`
}

// measure is the measuring process: it surveys args[0], with args[1] as the attachments directory
// when it is given and not empty, and writes what it measured to stdout.
func measure(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "measure: want an input and an optional attachments directory")
		return 2
	}
	dir := ""
	if len(args) == 2 {
		dir = args[1]
	}
	ctx := context.Background()
	tl, err := tool.NewExec(ctx, email.MailSpec())
	if err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 2
	}
	r, err := email.NewReader(email.Config{Mail: tl})
	if err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 2
	}
	s, _, _, err := surveyStore(ctx, r, args[0], dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 1
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
	out, err := json.Marshal(measured{Survey: s, Self: fromRusage(&self), Mail: fromRusage(&children)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		return 2
	}
	if _, err := os.Stdout.Write(append(out, '\n')); err != nil {
		return 1
	}
	return 0
}

// survey is what one read of a store found: its records by kind, mail's warnings, its attachments
// and their routes, its threads, and the time the read and the threads took.
type survey struct {
	Folders  int `json:"folders"`
	Messages int `json:"messages"`
	Skipped  int `json:"skipped"`
	Unknown  int `json:"unknown,omitzero"`
	Warnings int `json:"warnings"`
	// Attachments counts every attachment record, those of nested messages included.
	Attachments int `json:"attachments"`
	// Routes counts the attachments by route; only a read with an attachments directory routes.
	Routes  map[email.Route]int `json:"routes,omitzero"`
	Threads threadStats         `json:"threads"`
	// Elapsed is from the read's start until the threads are built, mail's run included.
	Elapsed time.Duration `json:"elapsed"`
}

// threadStats counts a store's threads.
type threadStats struct {
	// Threads is the roots, each a thread, a single message among them.
	Threads int `json:"threads"`
	// Multi is the threads of more than one message, and InMulti the messages they hold.
	Multi   int `json:"multi"`
	InMulti int `json:"in_multi"`
	// Placeholders are those left in the trees once pruned: each holds several roots together.
	Placeholders int `json:"placeholders"`
	// Largest is the messages of the largest thread, and Deepest the most levels of one.
	Largest int `json:"largest"`
	Deepest int `json:"deepest"`
}

// statsOf counts threads.
func statsOf(threads []*email.Thread) threadStats {
	var st threadStats
	// walk returns the messages and the levels of the tree under t.
	var walk func(t *email.Thread) (messages, depth int)
	walk = func(t *email.Thread) (int, int) {
		messages, depth := 0, 0
		if t.Placeholder {
			st.Placeholders++
		} else {
			messages = 1
		}
		for _, c := range t.Children {
			m, d := walk(c)
			messages += m
			depth = max(depth, d)
		}
		return messages, depth + 1
	}
	st.Threads = len(threads)
	for _, t := range threads {
		m, d := walk(t)
		if m > 1 {
			st.Multi++
			st.InMulti += m
		}
		st.Largest = max(st.Largest, m)
		st.Deepest = max(st.Deepest, d)
	}
	return st
}

// surveyStore reads input through r, with dir as its attachments directory when not empty,
// feeding every record to a Threader and, with a directory, routing every attachment as it is
// read. It returns the survey, the threads, and each attachment's filename and route in the order
// read.
func surveyStore(ctx context.Context, r *email.Reader, input, dir string) (survey, []*email.Thread, []routed, error) {
	s := survey{Routes: map[email.Route]int{}}
	var tr email.Threader
	var routes []routed
	start := time.Now()
	stream, err := r.Read(ctx, email.Request{Input: input, Attachments: dir})
	if err != nil {
		return s, nil, nil, err
	}
	for rec, err := range stream.Records() {
		if err != nil {
			return s, nil, nil, err
		}
		tr.Add(rec)
		switch rec := rec.(type) {
		case *email.Folder:
			s.Folders++
		case *email.Skipped:
			s.Skipped++
		case *email.UnknownRecord:
			s.Unknown++
		case *email.Message:
			s.Messages++
			for _, a := range attachmentsOf([]email.Record{rec}) {
				s.Attachments++
				if dir == "" {
					continue
				}
				route, _, err := a.Route()
				if err != nil {
					return s, nil, nil, err
				}
				s.Routes[route]++
				routes = append(routes, routed{a.Filename, route})
			}
		}
	}
	threads := tr.Threads()
	s.Elapsed = time.Since(start)
	s.Warnings = len(stream.Warnings())
	s.Threads = statsOf(threads)
	return s, threads, routes, nil
}

// parentsOf returns each message's parent in threads, by store id: "" for a root, a message's id,
// or "*" and the Message-ID a placeholder stands for.
func parentsOf(threads []*email.Thread) map[string]string {
	parents := map[string]string{}
	var walk func(t *email.Thread, parent string)
	walk = func(t *email.Thread, parent string) {
		name := "*" + t.MessageID
		if !t.Placeholder {
			name = strconv.FormatUint(t.ID, 10)
			parents[name] = parent
		}
		for _, c := range t.Children {
			walk(c, name)
		}
	}
	for _, t := range threads {
		walk(t, "")
	}
	return parents
}

// parseParents returns each message's parent in rendered, threads as render writes them, as
// parentsOf gives them.
func parseParents(rendered string) map[string]string {
	parents := map[string]string{}
	var stack []string
	last := ""
	flush := func(tok string) {
		if tok == "" {
			return
		}
		parent := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		if !strings.HasPrefix(tok, "*") {
			parents[tok] = parent
		}
		last = tok
	}
	var tok strings.Builder
	for _, c := range rendered {
		switch c {
		case ' ', '[', ']':
			flush(tok.String())
			tok.Reset()
			switch c {
			case '[':
				stack = append(stack, last)
			case ']':
				stack = stack[:len(stack)-1]
			}
		default:
			tok.WriteRune(c)
		}
	}
	flush(tok.String())
	return parents
}

// fixtureResult is one fixture's survey and, where the tests hold a hand truth, its scores.
type fixtureResult struct {
	Fixture string `json:"fixture"`
	survey
	// Rendered is its threads, as render writes them.
	Rendered string `json:"rendered"`
	// ThreadsTruth is the hand truth's threads, "" for a fixture without one; ThreadsRight
	// counts the messages under the parent the truth gives them, of ThreadsScored.
	ThreadsTruth  string `json:"threads_truth,omitzero"`
	ThreadsScored int    `json:"threads_scored,omitzero"`
	ThreadsRight  int    `json:"threads_right,omitzero"`
	// RoutesScored is the attachments the hand truth routes, and RoutesRight those routed as it
	// says, in order.
	RoutesScored int `json:"routes_scored"`
	RoutesRight  int `json:"routes_right"`
}

// fixtureInput is one input TestFindingsFixtures reads: its name, as the hand truth keys it, its
// path, the routes the truth gives its attachments, and its threads, "" where there is no truth.
type fixtureInput struct {
	name, path string
	routes     []routed
	threads    string
}

// TestFindingsFixtures reads every fixture and MIME input through the real pinned mail with an
// attachments directory, threads it, and routes every attachment. The fixtures are mail's .pst
// and .msg copy and the built .msg: their routes are scored against fixtureRoutes and
// builtSources, which cover every fixture, and their threads against realThreads, Tika's two
// stores and the generated one. The MIME inputs are mail's .eml and mbox copy and the built .eml:
// their routes are scored against mimeRoutes and their threads against mimeThreads, which cover
// every one. It fails on any miss, and records each input's counts and scores, and each group's
// totals.
func TestFindingsFixtures(t *testing.T) {
	r := realReader(t)
	root, err := filepath.Abs(filepath.Join(mailCopy, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	stores, mime := realInputs(t)
	var fixtures, mimeInputs []fixtureInput
	for _, rel := range stores {
		fixtures = append(fixtures, fixtureInput{rel, filepath.Join(root, rel), fixtureRoutes[rel], realThreads[rel]})
	}
	fixtures = append(fixtures, fixtureInput{"built/" + builtMsg, filepath.Join(builtDir, builtMsg), builtRoutes(), ""})
	for _, rel := range mime {
		mimeInputs = append(mimeInputs, fixtureInput{rel, filepath.Join(testdata, rel), mimeRoutes[rel], mimeThreads[rel]})
	}

	var results []fixtureResult
	for _, group := range []struct {
		name   string
		inputs []fixtureInput
	}{{".pst and .msg", fixtures}, {"MIME", mimeInputs}} {
		var total fixtureResult
		for _, in := range group.inputs {
			res := scoreFixture(t, r, in)
			results = append(results, res)
			total.Folders += res.Folders
			total.Messages += res.Messages
			total.Skipped += res.Skipped
			total.Warnings += res.Warnings
			total.Attachments += res.Attachments
			total.RoutesScored += res.RoutesScored
			total.RoutesRight += res.RoutesRight
			total.ThreadsScored += res.ThreadsScored
			total.ThreadsRight += res.ThreadsRight
			if total.Routes == nil {
				total.Routes = map[email.Route]int{}
			}
			for route, n := range res.Routes {
				total.Routes[route] += n
			}
		}
		t.Logf("recorded: %s, %d inputs: %d folders, %d messages, %d skipped, %d warnings; %d attachments, routes %s; routes right %d of %d; threads right %d of %d messages scored",
			group.name, len(group.inputs), total.Folders, total.Messages, total.Skipped, total.Warnings, total.Attachments,
			routeCounts(total.Routes), total.RoutesRight, total.RoutesScored, total.ThreadsRight, total.ThreadsScored)
	}
	saveFindings(t, "fixtures.json", results)
}

// scoreFixture reads in with an attachments directory, threads and routes it, scores it against
// its truth, reporting every miss, and logs it.
func scoreFixture(t *testing.T, r *email.Reader, in fixtureInput) fixtureResult {
	t.Helper()
	s, threads, routes, err := surveyStore(t.Context(), r, in.path, t.TempDir())
	if err != nil {
		t.Fatalf("%s: %v", in.name, err)
	}
	res := fixtureResult{Fixture: in.name, survey: s, Rendered: render(threads), RoutesScored: len(in.routes)}
	for i, want := range in.routes {
		if i < len(routes) && routes[i] == want {
			res.RoutesRight++
		}
	}
	if res.RoutesRight != res.RoutesScored || len(routes) != len(in.routes) {
		t.Errorf("%s: routes %v, want %v", in.name, routes, in.routes)
	}
	if in.threads != "" {
		res.ThreadsTruth = in.threads
		want, got := parseParents(in.threads), parentsOf(threads)
		res.ThreadsScored = len(want)
		for id, parent := range want {
			if p, ok := got[id]; ok && p == parent {
				res.ThreadsRight++
			}
		}
		if res.Rendered != in.threads {
			t.Errorf("%s: threads %s, want %s", in.name, res.Rendered, in.threads)
		}
	}
	t.Logf("recorded: fixture %s: %d folders, %d messages, %d skipped, %d warnings; %d attachments, routes %s; %s; threads %s%s; %v",
		in.name, s.Folders, s.Messages, s.Skipped, s.Warnings, s.Attachments, routeCounts(s.Routes),
		threadLine(s.Threads), res.Rendered, scoreLine(res), s.Elapsed.Round(time.Microsecond))
	return res
}

// scoreLine writes res's thread score, when it has a truth, and its route score, when the truth
// routes any attachment.
func scoreLine(res fixtureResult) string {
	var b strings.Builder
	if res.ThreadsTruth != "" {
		fmt.Fprintf(&b, ", %d of %d messages under the truth's parent", res.ThreadsRight, res.ThreadsScored)
	}
	if res.RoutesScored > 0 {
		fmt.Fprintf(&b, ", %d of %d routes the truth's", res.RoutesRight, res.RoutesScored)
	}
	return b.String()
}

// routeCounts writes counts by route, in the routes' declared order, those with none left out.
func routeCounts(counts map[email.Route]int) string {
	var parts []string
	for _, route := range []email.Route{
		email.RouteDocument, email.RouteOffice, email.RouteVision, email.RouteVisionConverted,
		email.RouteNested, email.RouteUnrouted,
	} {
		if n := counts[route]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", route, n))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// threadLine writes st.
func threadLine(st threadStats) string {
	return fmt.Sprintf("%d threads, %d of more than one message holding %d, %d placeholders, largest %d, deepest %d",
		st.Threads, st.Multi, st.InMulti, st.Placeholders, st.Largest, st.Deepest)
}

// saveFindings writes v as JSON to name in EMAIL_FINDINGS_RESULTS, when it is set.
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

// TestFindingsPlaso fetches plaso's store into EMAIL_PLASO_STORE when it is absent, refuses it
// unless its SHA-256 is plasoSHA256, and measures the adapter on it.
func TestFindingsPlaso(t *testing.T) {
	path := os.Getenv(plasoEnv)
	if path == "" {
		t.Skip(plasoEnv + " is unset")
	}
	if err := fetchPlaso(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	t.Logf("plaso's store %s at %s, SHA-256 %s", path, plasoCommit, plasoSHA256)
	measureStore(t, "plaso", path, map[string]any{"commit": plasoCommit, "sha256": plasoSHA256, "url": plasoURL})
}

// fetchPlaso fetches plaso's store to path when no file is there, and returns an error unless the
// file's SHA-256 is plasoSHA256, removing a fetched file that isn't.
func fetchPlaso(ctx context.Context, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, plasoURL, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("fetching plaso's store: %s", resp.Status)
		}
		part := path + ".part"
		f, err := os.Create(part)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, resp.Body)
		if err := errors.Join(err, f.Close()); err != nil {
			return err
		}
		if err := os.Rename(part, path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != plasoSHA256 {
		return fmt.Errorf("%s isn't plaso's store: its SHA-256 is %s, want %s", path, got, plasoSHA256)
	}
	return nil
}

// TestFindingsScale measures the adapter on the scale store EMAIL_SCALE_STORE names. The store
// carries no threading fields, so its threads are by subject alone and measure cost only.
func TestFindingsScale(t *testing.T) {
	path := os.Getenv(scaleEnv)
	if path == "" {
		t.Skip(scaleEnv + " is unset")
	}
	measureStore(t, "scale", path, scaleRecord(t, path))
}

// TestFindingsScaleMbox measures the adapter on the scale mboxrd EMAIL_SCALE_MBOX names, the scale
// store's messages as an mboxrd. They carry no threading fields there either, so its threads, too,
// measure cost only.
func TestFindingsScaleMbox(t *testing.T) {
	path := os.Getenv(mboxEnv)
	if path == "" {
		t.Skip(mboxEnv + " is unset")
	}
	measureStore(t, "scale-mbox", path, scaleRecord(t, path))
}

// scaleRecord returns the generator's record, the scale.json beside path, and logs its content
// SHA-256, its mboxrd's SHA-256, and its parameters; nil when there is none.
func scaleRecord(t *testing.T, path string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "scale.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	var record any
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if m, ok := record.(map[string]any); ok {
		var mbox any
		if mb, ok := m["mboxrd"].(map[string]any); ok {
			mbox = mb["sha256"]
		}
		t.Logf("scale set %s: content SHA-256 %v, mboxrd SHA-256 %v, parameters %v", path, m["content_sha256"], mbox, m["parameters"])
	}
	return record
}

// The ways a large store is timed: the adapter, the measuring process, reading and threading, and
// with an attachments directory routing too; and mail alone, writing to /dev/null, the same two
// ways.
const (
	wayAdapter            = "adapter"
	wayAdapterAttachments = "adapter, attachments"
	wayMail               = "mail"
	wayMailAttachments    = "mail, attachments"
)

// run is one timed run of a large store.
type run struct {
	Way string `json:"way"`
	// Launched is the measured process's rusage and wall time, as the launcher reports them. wait4
	// folds the process's reaped children into its rusage, so for the adapter's ways its maxrss
	// is the larger of the adapter's and mail's, and its CPU time both together.
	Launched usage `json:"launched"`
	// Measured is the measuring process's own report, for the adapter's ways.
	Measured *measured `json:"measured,omitzero"`
	// Written is the files and bytes in the attachments directory once the run ends.
	WrittenFiles int   `json:"written_files,omitzero"`
	WrittenBytes int64 `json:"written_bytes,omitzero"`
}

// storeResult is a large store's measurement.
type storeResult struct {
	Store  string `json:"store"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Record any    `json:"record,omitzero"`
	// Floor is the launcher's: what a process that does nothing reads.
	Floor usage `json:"floor"`
	Runs  []run `json:"runs"`
}

// measureStore times the store at path each way, EMAIL_FINDINGS_RUNS times each, back to back,
// logs each run and each way's median wall time and peak memory, and saves them as name.json.
func measureStore(t *testing.T, name, path string, record any) {
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	runs := 3
	if v := os.Getenv(runsEnv); v != "" {
		if runs, err = strconv.Atoi(v); err != nil || runs < 1 {
			t.Fatalf("%s %q: want a positive number", runsEnv, v)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mail, err := exec.LookPath(email.MailSpec().Program)
	if err != nil {
		t.Fatal(err)
	}
	res := storeResult{Store: name, Path: path, Size: info.Size(), Record: record}
	if res.Floor, _, err = launch(t.Context(), "floor", []string{exe}, false); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded: %s floor: maxrss %s", name, mib(res.Floor.MaxRSS))
	for _, way := range []string{wayAdapter, wayAdapterAttachments, wayMail, wayMailAttachments} {
		var walls []time.Duration
		var peak, mailPeak int64
		for i := range runs {
			dir := ""
			if way == wayAdapterAttachments || way == wayMailAttachments {
				dir = t.TempDir()
			}
			var rn run
			switch way {
			case wayAdapter, wayAdapterAttachments:
				args := []string{exe, path}
				if dir != "" {
					args = append(args, dir)
				}
				u, out, err := launch(t.Context(), "measure", args, true)
				if err != nil {
					t.Fatal(err)
				}
				var m measured
				if err := json.Unmarshal(out, &m); err != nil {
					t.Fatalf("measure's output %q: %v", out, err)
				}
				rn = run{Way: way, Launched: u, Measured: &m}
				peak = max(peak, m.Self.MaxRSS)
				mailPeak = max(mailPeak, m.Mail.MaxRSS)
			default:
				args := []string{mail, path}
				if dir != "" {
					args = []string{mail, "--attachments", dir, path}
				}
				u, _, err := launch(t.Context(), "", args, false)
				if err != nil {
					t.Fatal(err)
				}
				rn = run{Way: way, Launched: u}
				peak = max(peak, u.MaxRSS)
			}
			if dir != "" {
				if rn.WrittenFiles, rn.WrittenBytes, err = dirSize(dir); err != nil {
					t.Fatal(err)
				}
				// The scale set writes about 1.9 GB each run: none is kept.
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			walls = append(walls, rn.Launched.Wall)
			t.Logf("recorded: %s %s run %d: %s", name, way, i+1, runLine(rn))
			res.Runs = append(res.Runs, rn)
		}
		slices.Sort(walls)
		// For the adapter's ways the peak is the adapter's own, and its mail's beside it.
		line := fmt.Sprintf("recorded: %s %s, %d runs: median wall %v, peak memory %s", name, way, runs,
			walls[len(walls)/2].Round(time.Millisecond), mib(peak))
		if mailPeak > 0 {
			line += ", its mail's " + mib(mailPeak)
		}
		t.Log(line)
	}
	saveFindings(t, name+".json", res)
}

// runLine writes one run.
func runLine(rn run) string {
	u := rn.Launched
	line := fmt.Sprintf("wall %v, user %v, system %v, maxrss %s", u.Wall.Round(time.Millisecond),
		u.User.Round(time.Millisecond), u.System.Round(time.Millisecond), mib(u.MaxRSS))
	if m := rn.Measured; m != nil {
		s := m.Survey
		line = fmt.Sprintf("wall %v, user %v, system %v, with mail; adapter maxrss %s, user %v, system %v; mail maxrss %s, user %v, system %v; read and threads %v; %d folders, %d messages, %d skipped, %d warnings; %d attachments, routes %s; %s",
			u.Wall.Round(time.Millisecond), u.User.Round(time.Millisecond), u.System.Round(time.Millisecond),
			mib(m.Self.MaxRSS), m.Self.User.Round(time.Millisecond), m.Self.System.Round(time.Millisecond), mib(m.Mail.MaxRSS),
			m.Mail.User.Round(time.Millisecond), m.Mail.System.Round(time.Millisecond),
			s.Elapsed.Round(time.Millisecond), s.Folders, s.Messages, s.Skipped, s.Warnings, s.Attachments,
			routeCounts(s.Routes), threadLine(s.Threads))
	}
	if rn.WrittenFiles > 0 {
		line += fmt.Sprintf("; wrote %d files, %d bytes", rn.WrittenFiles, rn.WrittenBytes)
	}
	return line
}

// mib writes n bytes in MiB.
func mib(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// launch runs args through the launcher, the measured process given child as its modeEnv, and
// returns the measured process's usage and, when capture is set, its stdout; otherwise its stdout
// goes to /dev/null.
func launch(ctx context.Context, child string, args []string, capture bool) (usage, []byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return usage{}, nil, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = append(os.Environ(), modeEnv+"=launch", childEnv+"="+child)
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
	_, last, ok := strings.Cut(stderr.String(), launchPrefix)
	if !ok {
		return usage{}, nil, fmt.Errorf("launcher: no rusage line in %q", stderr.String())
	}
	var u usage
	if err := json.Unmarshal([]byte(strings.TrimSpace(last)), &u); err != nil {
		return usage{}, nil, fmt.Errorf("launcher: %w", err)
	}
	return u, stdout.Bytes(), nil
}

// dirSize returns the files in dir and their bytes.
func dirSize(dir string) (int, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	var n int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return 0, 0, err
		}
		n += info.Size()
	}
	return len(entries), n, nil
}
