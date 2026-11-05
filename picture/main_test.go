//go:build linux

package picture_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as a stand-in: the test binary, re-executed through a link named
// magick or soffice, acting on its arguments as ImageMagick or LibreOffice instead of running
// the tests.
const standinEnv = "PICTURE_TEST_STANDIN"

// The stand-ins' settings, which they read from the environment they inherit.
const (
	// magickVersionEnv is the version the stand-in magick's -version prints.
	magickVersionEnv = "PICTURE_TEST_MAGICK_VERSION"
	// sofficeVersionEnv is the version the stand-in soffice's --version prints, after the line
	// of desktop-library noise the real one writes to stderr in a container.
	sofficeVersionEnv = "PICTURE_TEST_SOFFICE_VERSION"
	// logEnv names the file each run appends its program, working directory, arguments, and
	// input to, one JSON object per line.
	logEnv = "PICTURE_TEST_STANDIN_LOG"
	// failEnv, when set, makes a magick run write its value to stderr and exit 1 after logging.
	failEnv = "PICTURE_TEST_MAGICK_FAIL"
	// stdoutEnv, when set, is what a magick run that writes to stdout writes in place of its
	// copy of stdin.
	stdoutEnv = "PICTURE_TEST_MAGICK_STDOUT"
	// rawEnv, when set, makes a magick run that writes a file write it without PNG's signature.
	rawEnv = "PICTURE_TEST_MAGICK_RAW"
	// sofficeFailEnv, when set, makes a soffice run write its value to stderr and exit 1 after
	// logging.
	sofficeFailEnv = "PICTURE_TEST_SOFFICE_FAIL"
	// sofficeNothingEnv, when set, makes a soffice run exit zero having written nothing, as
	// soffice does when it can't export what it loaded.
	sofficeNothingEnv = "PICTURE_TEST_SOFFICE_NOTHING"
)

// convertedPrefix begins what either stand-in writes for a picture after PNG's signature: the
// prefix, then its input.
const convertedPrefix = "converted: "

// pngSignature begins every PNG, and the stand-ins' PNG output.
const pngSignature = "\x89PNG\r\n\x1a\n"

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" {
		switch name := filepath.Base(os.Args[0]); name {
		case "magick":
			os.Exit(magickStandin(os.Args[1:]))
		case "soffice":
			os.Exit(sofficeStandin(os.Args[1:]))
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

// filePath returns the path an argument of magick's names, its format prefix, "tiff:", and
// frame suffix, "[0]", taken off, and whether it names one: an absolute path.
func filePath(arg string) (string, bool) {
	if _, rest, ok := strings.Cut(arg, ":"); ok {
		arg = rest
	}
	arg = strings.TrimSuffix(arg, "[0]")
	return arg, filepath.IsAbs(arg)
}

// magickStandin is the stand-in magick. With -version alone it prints the version line the real
// one prints first. A run whose first argument is png:-, the corrections stage's, reads stdin:
// it logs its arguments, then fails when failEnv is set, or else writes stdoutEnv's value when it
// is set, and copies stdin when it isn't, so a corrected image is the image it was given. Any
// other run, a conversion's, reads its input, its first argument that names an absolute path,
// "tiff:path[0]", "rsvg:path", or "png:path", and writes its last, "png:path": it logs its
// arguments and its input's content, then fails when failEnv is set, or else writes PNG's
// signature, convertedPrefix, and its input, less any PNG signature it begins with, without the
// signature when rawEnv is set. A metafile's PNG, which soffice and then magick wrote, holds
// convertedPrefix twice.
func magickStandin(args []string) int {
	if len(args) == 1 && args[0] == "-version" {
		fmt.Printf("Version: ImageMagick %s Q16-HDRI x86_64 https://imagemagick.org\n", os.Getenv(magickVersionEnv))
		return 0
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "stand-in: want an input and an output")
		return 2
	}
	if args[0] == "png:-" {
		return magickStdout(args)
	}
	var input []byte
	for _, a := range args[:len(args)-1] {
		if path, ok := filePath(a); ok {
			var err error
			if input, err = os.ReadFile(path); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			break
		}
	}
	if err := appendLog("magick", args, string(input), ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if msg := os.Getenv(failEnv); msg != "" {
		fmt.Fprint(os.Stderr, msg)
		return 1
	}
	out := convertedPrefix + strings.TrimPrefix(string(input), pngSignature)
	if os.Getenv(rawEnv) == "" {
		out = pngSignature + out
	}
	path, _ := filePath(args[len(args)-1])
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// magickStdout is the stand-in magick's run that reads stdin, as magickStandin says.
func magickStdout(args []string) int {
	if err := appendLog("magick", args, "", ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if msg := os.Getenv(failEnv); msg != "" {
		fmt.Fprint(os.Stderr, msg)
		return 1
	}
	if out, set := os.LookupEnv(stdoutEnv); set {
		if _, err := os.Stdout.WriteString(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// sofficeStandin is the stand-in soffice. With --version alone it writes a line of noise to
// stderr and the version line to stdout, as the real one does in a container. Otherwise its
// inputs are its arguments that are neither options nor their values. It logs its arguments, its
// first input's content, and the registrymodifications.xcu its -env:UserInstallation profile
// starts with; exits 1, having written sofficeFailEnv's value to stderr, when it is set; exits
// zero having written nothing when sofficeNothingEnv is set; and else writes each input's page
// into the directory after --outdir, named for its input's stem and .png: PNG's signature,
// convertedPrefix, and the input.
func sofficeStandin(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stderr, "(process:20): dconf-CRITICAL **: unable to create directory '/.cache/dconf'")
		fmt.Printf("LibreOffice %s 680(Build:1)\n", os.Getenv(sofficeVersionEnv))
		return 0
	}
	var outdir, profile string
	var inputs []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case strings.HasPrefix(a, "-env:UserInstallation=file://"):
			profile = strings.TrimPrefix(a, "-env:UserInstallation=file://")
		case (a == "--outdir" || a == "--convert-to") && i+1 < len(args):
			i++
			if a == "--outdir" {
				outdir = args[i]
			}
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
	if err := appendLog("soffice", args, string(contents[0]), string(registry)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if msg := os.Getenv(sofficeFailEnv); msg != "" {
		fmt.Fprint(os.Stderr, msg)
		return 1
	}
	if os.Getenv(sofficeNothingEnv) != "" {
		return 0
	}
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for i, path := range inputs {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		out := pngSignature + convertedPrefix + string(contents[i])
		if err := os.WriteFile(filepath.Join(outdir, base+".png"), []byte(out), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	return 0
}

// standinRun is one logged run of a stand-in: its program, its working directory, its
// arguments, its input file's content, "" for a run that reads stdin, and, for soffice, the
// registrymodifications.xcu its profile started with.
type standinRun struct {
	Program  string   `json:"program"`
	Dir      string   `json:"dir"`
	Args     []string `json:"args"`
	Input    string   `json:"input"`
	Registry string   `json:"registry"`
}

// appendLog appends program, the working directory, args, input, and registry to logEnv's file
// as one JSON object and a newline.
func appendLog(program string, args []string, input, registry string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	line, err := json.Marshal(standinRun{Program: program, Dir: dir, Args: args, Input: input, Registry: registry})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(os.Getenv(logEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// installStandin links the test binary as magick and soffice in a directory of its own, makes
// that directory the whole PATH, and has the stand-in magick report magickVersion and the
// stand-in soffice 26.8.1.1. It returns a function that returns every run of either since, in
// order, the version commands left out.
func installStandin(t *testing.T, magickVersion string) func() []standinRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"magick", "soffice"} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv(magickVersionEnv, magickVersion)
	t.Setenv(sofficeVersionEnv, "26.8.1.1")
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
		lines.Buffer(nil, 1<<20)
		for lines.Scan() {
			var r standinRun
			if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			runs = append(runs, r)
		}
		if err := lines.Err(); err != nil {
			t.Fatal(err)
		}
		return runs
	}
}

// installMagick installs the stand-ins with magick at version, as installStandin does, and
// returns a function that returns the arguments of every run since, in order.
func installMagick(t *testing.T, version string) func() [][]string {
	t.Helper()
	runs := installStandin(t, version)
	return func() [][]string {
		t.Helper()
		var args [][]string
		for _, r := range runs() {
			args = append(args, r.Args)
		}
		return args
	}
}

// standinMagick installs the stand-in magick at version, as installMagick does, builds the Tool
// from spec through the exec runner, and returns it with the stand-in's run log.
func standinMagick(t *testing.T, version string, spec tool.Spec) (*tool.Tool, func() [][]string) {
	t.Helper()
	runs := installMagick(t, version)
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return tl, runs
}

// standinCorrector builds the corrections stage over the stand-in magick at 7.1.2-34, and
// returns it with the stand-in's run log.
func standinCorrector(t *testing.T) (*picture.Corrector, func() [][]string) {
	t.Helper()
	tl, runs := standinMagick(t, "7.1.2-34", picture.MagickSpec())
	c, err := picture.NewCorrector(tl)
	if err != nil {
		t.Fatal(err)
	}
	return c, runs
}

// standinConverter builds a Converter over the stand-in magick at 7.1.2-34 and the stand-in
// soffice at 26.8.1.1, each when asked, built from its Spec through the exec runner, and returns
// it with the stand-ins' run log.
func standinConverter(t *testing.T, libreoffice, magick bool) (*picture.Converter, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "7.1.2-34")
	var cfg picture.Config
	var err error
	if libreoffice {
		if cfg.LibreOffice, err = tool.NewExec(t.Context(), picture.LibreOfficeSpec()); err != nil {
			t.Fatal(err)
		}
	}
	if magick {
		if cfg.Magick, err = tool.NewExec(t.Context(), picture.MagickSpec()); err != nil {
			t.Fatal(err)
		}
	}
	c, err := picture.NewConverter(cfg)
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
