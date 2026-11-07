//go:build linux

package office_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as a stand-in: the test binary, re-executed through a link named
// spike-ai-pipeline-office, soffice, or magick, acting on its arguments as office, LibreOffice,
// or ImageMagick instead of running the tests.
const standinEnv = "OFFICE_TEST_STANDIN"

// The stand-in's settings, which it reads from the environment it inherits.
const (
	// versionEnv is the version the stand-in's --version prints.
	versionEnv = "OFFICE_TEST_STANDIN_VERSION"
	// logEnv names the file each run appends its working directory, arguments, and input to, one
	// JSON object per line.
	logEnv = "OFFICE_TEST_STANDIN_LOG"
	// exitEnv, when set, is the exit code of a run, which then writes nothing to stdout.
	exitEnv = "OFFICE_TEST_STANDIN_EXIT"
	// stderrEnv, when set, is what a run writes to stderr, each {path} in it the input's path as
	// the run was given it.
	stderrEnv = "OFFICE_TEST_STANDIN_STDERR"
	// stdoutEnv, when set, is what a run that exits zero writes to stdout in place of
	// standinDocument.
	stdoutEnv = "OFFICE_TEST_STANDIN_STDOUT"
	// mediaEnv, when set, names the files, comma-separated, a run writes to its --media
	// directory, each holding its own name.
	mediaEnv = "OFFICE_TEST_STANDIN_MEDIA"
	// sleepEnv, when set, makes a run sleep a minute after logging, for a test to cancel it.
	sleepEnv = "OFFICE_TEST_STANDIN_SLEEP"
)

// The stand-in soffice's settings; it logs to logEnv's file too.
const (
	// sofficeVersionEnv is the version the stand-in soffice's --version prints, after the line
	// of desktop-library noise the real one writes to stderr in a container.
	sofficeVersionEnv = "OFFICE_TEST_SOFFICE_VERSION"
	// sofficeExitEnv, when set, is the exit code of a conversion, which then writes nothing.
	sofficeExitEnv = "OFFICE_TEST_SOFFICE_EXIT"
	// sofficeOutputEnv, when set, is what a conversion writes to stderr.
	sofficeOutputEnv = "OFFICE_TEST_SOFFICE_OUTPUT"
	// sofficeNothingEnv, when set, makes a conversion exit zero having written nothing, as
	// soffice does when it can't export what it loaded.
	sofficeNothingEnv = "OFFICE_TEST_SOFFICE_NOTHING"
	// sofficeSkipEnv, when set, names an input, by its base name, that a conversion writes
	// nothing for, as soffice does for one picture of a run it can't export.
	sofficeSkipEnv = "OFFICE_TEST_SOFFICE_SKIP"
)

// The stand-in magick's settings; it logs to logEnv's file too.
const (
	// magickVersionEnv is the version the stand-in magick's -version prints.
	magickVersionEnv = "OFFICE_TEST_MAGICK_VERSION"
	// magickExitEnv, when set, is the exit code of a conversion, which then writes nothing.
	magickExitEnv = "OFFICE_TEST_MAGICK_EXIT"
	// magickOutputEnv, when set, is what a conversion writes to stderr.
	magickOutputEnv = "OFFICE_TEST_MAGICK_OUTPUT"
	// magickRawEnv, when set, makes a conversion write its output without PNG's signature.
	magickRawEnv = "OFFICE_TEST_MAGICK_RAW"
)

// convertedPrefix begins the document the stand-in soffice writes, and the PNG either stand-in
// writes after PNG's signature: the prefix, then its input.
const convertedPrefix = "converted: "

// pngSignature begins every PNG, and the stand-ins' PNG output.
const pngSignature = "\x89PNG\r\n\x1a\n"

// standinDocument is the stand-in's JSON model when stdoutEnv isn't set: an empty docx.
const standinDocument = `{"schema": "1", "format": "docx", "blocks": []}` + "\n"

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		switch name := filepath.Base(os.Args[0]); name {
		case "spike-ai-pipeline-office":
			os.Exit(standin(os.Args[1:]))
		case "soffice":
			os.Exit(sofficeStandin(os.Args[1:]))
		case "magick":
			os.Exit(magickStandin(os.Args[1:]))
		default:
			fmt.Fprintf(os.Stderr, "stand-in: no tool named %s\n", name)
			os.Exit(2)
		}
	}
	// The stand-in inherits the environment. Under -race, GORACE's atexit_sleep_ms keeps it from
	// sleeping a second as it exits.
	for key, value := range map[string]string{standinEnv: "1", "GORACE": "atexit_sleep_ms=0"} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// standin is the stand-in office. With --version alone it prints the version alone, as office
// does. Otherwise it appends its working directory, arguments, and its input file's content to
// the log file, sleeps when sleepEnv is set, writes the media mediaEnv names into the directory
// after --media, writes stderrEnv's value to stderr, and exits with exitEnv's code when it is
// set, or else writes stdoutEnv's value, or standinDocument, to stdout and exits zero.
func standin(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(os.Getenv(versionEnv))
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "stand-in: no input")
		return 2
	}
	input, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := appendLog(os.Getenv(logEnv), "spike-ai-pipeline-office", args, string(input), ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if os.Getenv(sleepEnv) != "" {
		time.Sleep(time.Minute)
	}
	if names := os.Getenv(mediaEnv); names != "" {
		dir := ""
		for i, a := range args {
			if a == "--media" && i+1 < len(args) {
				dir = args[i+1]
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		for name := range strings.SplitSeq(names, ",") {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
		}
	}
	fmt.Fprint(os.Stderr, strings.ReplaceAll(os.Getenv(stderrEnv), "{path}", args[0]))
	if code := os.Getenv(exitEnv); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return n
	}
	out, set := os.LookupEnv(stdoutEnv)
	if !set {
		out = standinDocument
	}
	if _, err := os.Stdout.WriteString(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// sofficeStandin is the stand-in soffice. With --version alone it writes a line of noise to
// stderr and the version line to stdout, as the real one does in a container. Otherwise its
// inputs are its arguments that are neither options nor their values. It appends its working
// directory, arguments, its first input's content, and the registrymodifications.xcu its
// -env:UserInstallation profile starts with, if any, to the log file; exits with
// sofficeExitEnv's code, having written sofficeOutputEnv's value to stderr, when it is set; exits
// zero having written nothing when sofficeNothingEnv is set; and else writes each input's
// conversion into the directory after --outdir, named for its input's stem with the extension
// --convert-to names before its colon: convertedPrefix and the input, after PNG's signature for
// png. It writes nothing for the input sofficeSkipEnv names.
func sofficeStandin(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stderr, "(process:20): dconf-CRITICAL **: unable to create directory '/.cache/dconf'")
		fmt.Printf("LibreOffice %s 680(Build:1)\n", os.Getenv(sofficeVersionEnv))
		return 0
	}
	var outdir, format, profile string
	var inputs []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case strings.HasPrefix(a, "-env:UserInstallation=file://"):
			profile = strings.TrimPrefix(a, "-env:UserInstallation=file://")
		case a == "--outdir" && i+1 < len(args):
			i++
			outdir = args[i]
		case a == "--convert-to" && i+1 < len(args):
			i++
			format, _, _ = strings.Cut(args[i], ":")
		case !strings.HasPrefix(a, "-"):
			inputs = append(inputs, a)
		}
	}
	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "stand-in: no input")
		return 2
	}
	contents := make([][]byte, len(inputs))
	for i, path := range inputs {
		var err error
		if contents[i], err = os.ReadFile(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	registry, err := os.ReadFile(filepath.Join(profile, "user", "registrymodifications.xcu"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := appendLog(os.Getenv(logEnv), "soffice", args, string(contents[0]), string(registry)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Fprint(os.Stderr, os.Getenv(sofficeOutputEnv))
	if code := os.Getenv(sofficeExitEnv); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return n
	}
	if os.Getenv(sofficeNothingEnv) != "" {
		return 0
	}
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for i, path := range inputs {
		if filepath.Base(path) == os.Getenv(sofficeSkipEnv) {
			continue
		}
		out := convertedPrefix + string(contents[i])
		if format == "png" {
			out = pngSignature + out
		}
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if err := os.WriteFile(filepath.Join(outdir, base+"."+format), []byte(out), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	return 0
}

// magickStandin is the stand-in magick. With -version alone it prints the version line the real
// one prints first, and with -list format a listing of no formats, which the picture package's
// Converter reads when it is built; neither is logged. Otherwise it takes its first argument as its input, with any format prefix
// and frame suffix, "tiff:path[0]", and its last as its output, "png:path". It appends its
// working directory, arguments, and its input's content to the log file; exits with
// magickExitEnv's code, having written magickOutputEnv's value to stderr, when it is set; and
// else writes PNG's signature, convertedPrefix, and its input, less any PNG signature it begins
// with, to the output, without the signature when magickRawEnv is set. A metafile's PNG, which
// soffice and then magick wrote, holds convertedPrefix twice.
func magickStandin(args []string) int {
	if len(args) == 1 && args[0] == "-version" {
		fmt.Printf("Version: ImageMagick %s Q16-HDRI x86_64 https://imagemagick.org\n", os.Getenv(magickVersionEnv))
		return 0
	}
	if len(args) == 2 && args[0] == "-list" && args[1] == "format" {
		fmt.Println("   Format  Module    Mode  Description")
		return 0
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "stand-in: want an input and an output")
		return 2
	}
	strip := func(arg string) string {
		if _, path, ok := strings.Cut(arg, ":"); ok {
			arg = path
		}
		return strings.TrimSuffix(arg, "[0]")
	}
	input, err := os.ReadFile(strip(args[0]))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := appendLog(os.Getenv(logEnv), "magick", args, string(input), ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Fprint(os.Stderr, os.Getenv(magickOutputEnv))
	if code := os.Getenv(magickExitEnv); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return n
	}
	out := convertedPrefix + strings.TrimPrefix(string(input), pngSignature)
	if os.Getenv(magickRawEnv) == "" {
		out = pngSignature + out
	}
	if err := os.WriteFile(strip(args[len(args)-1]), []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// standinRun is one logged run of a stand-in: its program, its working directory, its
// arguments, its input file's content, and, for soffice, the registrymodifications.xcu its
// profile started with, "" for none. The log holds the content as bytes, Raw, since a legacy
// document's isn't UTF-8; reading it sets Input from Raw.
type standinRun struct {
	Program  string   `json:"program"`
	Dir      string   `json:"dir"`
	Args     []string `json:"args"`
	Raw      []byte   `json:"input"`
	Registry string   `json:"registry"`
	Input    string   `json:"-"`
}

// appendLog appends program, the working directory, args, input, and registry to file as one
// JSON object and a newline.
func appendLog(file, program string, args []string, input, registry string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	line, err := json.Marshal(standinRun{Program: program, Dir: dir, Args: args, Raw: []byte(input), Registry: registry})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// installStandin links the test binary as spike-ai-pipeline-office, soffice, and magick in a
// directory of its own, makes that directory the whole PATH, and has the stand-in office report
// version, the stand-in soffice 26.8.1.1, and the stand-in magick 7.1.2-34. It returns a function
// that returns every run of any of them since, in order, the version commands left out.
func installStandin(t *testing.T, version string) func() []standinRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"spike-ai-pipeline-office", "soffice", "magick"} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv(versionEnv, version)
	t.Setenv(sofficeVersionEnv, "26.8.1.1")
	t.Setenv(magickVersionEnv, "7.1.2-34")
	log := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv(logEnv, log)
	return func() []standinRun {
		t.Helper()
		f, err := os.Open(log)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		var runs []standinRun
		lines := bufio.NewScanner(f)
		for lines.Scan() {
			var r standinRun
			if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			r.Input = string(r.Raw)
			runs = append(runs, r)
		}
		if err := lines.Err(); err != nil {
			t.Fatal(err)
		}
		return runs
	}
}

// standinConverter builds a Converter over the stand-in office at 0.1.0, built from OfficeSpec
// through the exec runner, and returns it with the stand-in's run log.
func standinConverter(t *testing.T) (*office.Converter, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "0.1.0")
	tl, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := office.NewConverter(t.Context(), office.Config{Office: tl})
	if err != nil {
		t.Fatal(err)
	}
	return c, runs
}

// legacyConverter builds a Converter over the stand-in office at 0.1.0 and the stand-in soffice
// at 26.8.1.1, built from OfficeSpec and LibreOfficeSpec through the exec runner, and returns it
// with the stand-ins' run log.
func legacyConverter(t *testing.T) (*office.Converter, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "0.1.0")
	o, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	lo, err := tool.NewExec(t.Context(), picture.LibreOfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := office.NewConverter(t.Context(), office.Config{Office: o, LibreOffice: lo})
	if err != nil {
		t.Fatal(err)
	}
	return c, runs
}

// entries returns the names in dir, in name order.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}
