//go:build bakeoff && linux

package document_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// The bake-off's five renderers behind document.Renderer, each on the host's exec runner. Each
// renders one page at a DPI to PNG on stdout, reading the PDF by path, one process per page, and
// can also read the PDF on stdin, which the bake-off probes. Four are test code; gs, the winner,
// runs the released renderer, document.Ghostscript, so a rerun ranks released code.

// cliRenderer is a renderer that runs one command per page.
type cliRenderer struct {
	// render is the rendering tool, and launcher the same program run through the launcher,
	// for its peak memory.
	render, launcher *tool.Tool
	// args returns the rendering tool's arguments for page of the PDF at pdf, which is "" when
	// the PDF comes on stdin.
	args func(pdf string, page, dpi int) []string
	// count counts the PDF's pages with a tool of the renderer's own package.
	count func(ctx context.Context, pdf string) (int, error)
	// released, when set, renders in place of running render with args: a released renderer's
	// own run, from the PDF at pdf or from stdin when pdf is "", filling usage.
	released func(ctx context.Context, pdf string, stdin io.Reader, page, dpi int, w io.Writer, usage *tool.Usage) error
}

var _ document.Renderer = (*cliRenderer)(nil)

func (r *cliRenderer) Count(ctx context.Context, pdf string) (int, error) { return r.count(ctx, pdf) }

func (r *cliRenderer) Render(ctx context.Context, pdf string, page, dpi int, w io.Writer) error {
	_, err := r.measure(ctx, pdf, nil, page, dpi, w)
	return err
}

// measure renders page as Render does, from the PDF at pdf, or from stdin when pdf is "", and
// returns the rendering tool's usage.
func (r *cliRenderer) measure(ctx context.Context, pdf string, stdin io.Reader, page, dpi int, w io.Writer) (tool.Usage, error) {
	var usage tool.Usage
	if r.released != nil {
		err := r.released(ctx, pdf, stdin, page, dpi, w, &usage)
		return usage, err
	}
	if err := document.CheckRender(page, dpi); err != nil {
		return usage, err
	}
	err := r.render.Run(ctx, tool.Invocation{Args: r.args(pdf, page, dpi), Stdin: stdin, Stdout: w, Usage: &usage})
	return usage, err
}

// peakMemory renders page from the PDF at pdf through the launcher and returns the rendering
// program's peak resident memory in bytes, as the launcher reports it.
func (r *cliRenderer) peakMemory(ctx context.Context, pdf string, page, dpi int) (int64, error) {
	return launch(ctx, r.launcher, append([]string{r.render.Name()}, r.args(pdf, page, dpi)...))
}

// The launcher measures a program's peak memory, which a tool.Usage leaves out: Go starts a
// program with a vfork-style clone, the child sharing the parent's memory until it execs, and on
// exec Linux carries the shared memory's high-water mark into the program's maxrss. So the maxrss
// of a program the test process runs is never below the test process's own peak resident memory,
// which holds every rendered page. The launcher is the
// test binary, re-executed with launchEnv set: a small process whose own high-water mark is the
// floor instead, which launchFloor measures. It runs its arguments as a program with its own
// standard streams, and writes the program's maxrss to stderr after it, as a last line that
// begins launchPrefix.
const (
	launchEnv    = "BAKEOFF_LAUNCH"
	launchPrefix = "bakeoff-launch: maxrss bytes "
)

func init() {
	if os.Getenv(launchEnv) == "1" {
		os.Exit(launcher(os.Args[1:]))
	}
}

// launcher runs args as a program and reports its maxrss, returning its exit code.
func launcher(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "launcher: no program")
		return 2
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if cmd.ProcessState != nil {
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			fmt.Fprintf(os.Stderr, "\n%s%d\n", launchPrefix, int64(ru.Maxrss)<<10)
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

// launcherTool builds the launcher for spec's program through the exec runner: the test binary,
// checked by running spec's version command through it. Every process the test starts inherits
// launchEnv, which only the test binary reads.
func launcherTool(t *testing.T, spec tool.Spec) *tool.Tool {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(launchEnv, "1")
	spec.VersionArgs = append([]string{spec.Program}, spec.VersionArgs...)
	spec.Program = exe
	return hostTool(t, spec)
}

// launch runs args through launcher and returns the program's maxrss in bytes.
func launch(ctx context.Context, launcher *tool.Tool, args []string) (int64, error) {
	var stderr bytes.Buffer
	if err := launcher.Run(ctx, tool.Invocation{Args: args, Stdout: io.Discard, Stderr: &stderr}); err != nil {
		return 0, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	_, last, ok := strings.Cut(stderr.String(), launchPrefix)
	if !ok {
		return 0, fmt.Errorf("launcher: no maxrss line in %q", stderr.String())
	}
	return strconv.ParseInt(strings.TrimSpace(last), 10, 64)
}

// launchFloor returns the launcher's floor: the maxrss it reports for true, which holds almost
// no memory of its own. A rendering program's peak at the floor is at most the floor.
func (r *cliRenderer) launchFloor(ctx context.Context) (int64, error) {
	return launch(ctx, r.launcher, []string{"true"})
}

// candidate is one renderer in the bake-off, with what the ranking and the report need of it
// beyond its measures.
type candidate struct {
	name string
	// tools are the programs it runs, a delegate it runs through another program included.
	tools []string
	// packages are the Arch packages those programs and their libraries come from, and
	// footprint is their installed size, as recorded below.
	packages []string
	// license is the strictest license among the packages, and licenseRank orders it: 0
	// permissive, 1 LGPL, 2 GPL, 3 AGPL.
	license     string
	licenseRank int
	// build builds the renderer on the host's tools, failing t when one is missing or old.
	build func(t *testing.T) *cliRenderer
}

// footprint is each package's installed size in MiB, as pacman -Qi reported it on the laptop on
// 2026-10-29 for the versions named, with its Licenses line. The test code runs no pacman, so it
// stays on mise's tools; TestBakeoff logs these beside the versions it finds, so a stale record
// shows.
var footprint = map[string]struct {
	version  string
	mib      float64
	licenses string
}{
	"poppler":      {"26.10.0-1", 6.81, "GPL-2.0-only GPL-3.0-or-later LGPL-2.0-or-later LGPL-2.1-or-later MIT HPND-sell-variant"},
	"poppler-glib": {"26.10.0-1", 3.25, "GPL-2.0-only GPL-3.0-or-later LGPL-2.0-or-later LGPL-2.1-or-later MIT HPND-sell-variant"},
	"ghostscript":  {"10.08.0-1", 41.08, "AGPL-3.0-or-later"},
	"libvips":      {"8.18.7-1", 6.03, "LGPL-2.1-or-later"},
	"imagemagick":  {"7.1.2.34-2", 22.04, "ImageMagick"},
}

// footprintMiB returns the installed size of c's packages together.
func (c candidate) footprintMiB() float64 {
	var sum float64
	for _, p := range c.packages {
		sum += footprint[p].mib
	}
	return sum
}

// candidates are the five renderers, in the order the bake-off runs them.
var candidates = []candidate{
	{
		name:  "pdftoppm",
		tools: []string{"pdftoppm", "pdfinfo"}, packages: []string{"poppler"},
		license: "GPL (poppler)", licenseRank: 2,
		build: func(t *testing.T) *cliRenderer {
			return &cliRenderer{
				render: hostTool(t, popplerSpec("pdftoppm")), launcher: launcherTool(t, popplerSpec("pdftoppm")),
				args: func(pdf string, page, dpi int) []string {
					return []string{"-f", itoa(page), "-l", itoa(page), "-r", itoa(dpi), "-png", "-singlefile", stdinAs(pdf, "fd://0")}
				},
				count: pdfinfoCount(hostTool(t, popplerSpec("pdfinfo"))),
			}
		},
	},
	{
		name:  "pdftocairo",
		tools: []string{"pdftocairo", "pdfinfo"}, packages: []string{"poppler"},
		license: "GPL (poppler)", licenseRank: 2,
		build: func(t *testing.T) *cliRenderer {
			return &cliRenderer{
				render: hostTool(t, popplerSpec("pdftocairo")), launcher: launcherTool(t, popplerSpec("pdftocairo")),
				args: func(pdf string, page, dpi int) []string {
					return []string{"-png", "-singlefile", "-f", itoa(page), "-l", itoa(page), "-r", itoa(dpi), stdinAs(pdf, "fd://0"), "-"}
				},
				count: pdfinfoCount(hostTool(t, popplerSpec("pdfinfo"))),
			}
		},
	},
	{
		name:  "gs",
		tools: []string{"gs"}, packages: []string{"ghostscript"},
		license: "AGPL (ghostscript)", licenseRank: 3,
		build: func(t *testing.T) *cliRenderer {
			gs := hostTool(t, document.GhostscriptSpec())
			released, err := document.NewGhostscript(gs)
			if err != nil {
				t.Fatal(err)
			}
			return &cliRenderer{
				render: gs, launcher: launcherTool(t, document.GhostscriptSpec()),
				args:  document.GhostscriptRenderArgs,
				count: released.Count,
				released: func(ctx context.Context, pdf string, stdin io.Reader, page, dpi int, w io.Writer, usage *tool.Usage) error {
					return document.GhostscriptRender(released, ctx, pdf, stdin, page, dpi, w, usage)
				},
			}
		},
	},
	{
		name:  "vips",
		tools: []string{"vips", "vipsheader"}, packages: []string{"libvips", "poppler-glib", "poppler"},
		license: "GPL (libvips is LGPL; its pdfload links poppler-glib)", licenseRank: 2,
		build: func(t *testing.T) *cliRenderer {
			return &cliRenderer{
				render: hostTool(t, vipsSpec("vips")), launcher: launcherTool(t, vipsSpec("vips")),
				// pdfload's page is 0-based. Its PNG is RGBA, the alpha opaque: pdfload fills
				// its background with white.
				args: func(pdf string, page, dpi int) []string {
					if pdf == "" {
						return []string{"pdfload_source", "[descriptor=0]", ".png", "--page", itoa(page - 1), "--dpi", itoa(dpi)}
					}
					return []string{"pdfload", pdf, ".png", "--page", itoa(page - 1), "--dpi", itoa(dpi)}
				},
				count: vipsCount(hostTool(t, vipsSpec("vipsheader"))),
			}
		},
	},
	{
		name:  "magick",
		tools: []string{"magick", "gs"}, packages: []string{"imagemagick", "ghostscript"},
		license: "AGPL (ImageMagick's own license, but it renders PDF through ghostscript)", licenseRank: 3,
		build: func(t *testing.T) *cliRenderer {
			// magick renders a PDF through its gs delegate, which must be present and current.
			hostTool(t, document.GhostscriptSpec())
			magick := hostTool(t, document.MagickSpec())
			return &cliRenderer{
				render: magick, launcher: launcherTool(t, document.MagickSpec()),
				// magick's PDF delegate renders with alpha; flattening on white keeps the page
				// opaque, as the other renderers' are. Its page index is 0-based.
				args: func(pdf string, page, dpi int) []string {
					return []string{"-density", itoa(dpi), fmt.Sprintf("%s[%d]", stdinAs(pdf, "pdf:-"), page-1),
						"-background", "white", "-alpha", "remove", "-alpha", "off", "png:-"}
				},
				count: magickCount(magick),
			}
		},
	},
}

// itoa is strconv.Itoa, short for argument lists.
func itoa(n int) string { return strconv.Itoa(n) }

// stdinAs returns pdf, or stdin's name for the tool when pdf is "".
func stdinAs(pdf, stdin string) string {
	if pdf == "" {
		return stdin
	}
	return pdf
}

// hostTool builds spec's program through the exec runner, failing t when it is missing or older
// than spec's minimum.
func hostTool(t *testing.T, spec tool.Spec) *tool.Tool {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// popplerSpec declares one of poppler's utilities, minimum 22.02, which prints "pdftoppm version
// 26.10.0" for -v.
func popplerSpec(program string) tool.Spec {
	return tool.Spec{
		Program:        program,
		VersionArgs:    []string{"-v"},
		VersionPattern: regexp.MustCompile(program + ` version (\d+(?:\.\d+)*)`),
		Minimum:        "22.02",
	}
}

// vipsSpec declares one of libvips's programs, minimum 8.10, which prints "vips-8.18.7" for
// --version.
func vipsSpec(program string) tool.Spec {
	return tool.Spec{
		Program:        program,
		VersionArgs:    []string{"--version"},
		VersionPattern: regexp.MustCompile(`vips-(\d+(?:\.\d+)*)`),
		Minimum:        "8.10",
	}
}

// output runs tl with args and returns its standard output.
func output(ctx context.Context, tl *tool.Tool, args ...string) (string, error) {
	var out bytes.Buffer
	err := tl.Run(ctx, tool.Invocation{Args: args, Stdout: &out})
	return out.String(), err
}

// pdfPages is pdfinfo's page-count line.
var pdfPages = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)

// pdfinfoCount counts pages with pdfinfo, from its "Pages:" line.
func pdfinfoCount(pdfinfo *tool.Tool) func(context.Context, string) (int, error) {
	return func(ctx context.Context, pdf string) (int, error) {
		out, err := output(ctx, pdfinfo, pdf)
		if err != nil {
			return 0, err
		}
		m := pdfPages.FindStringSubmatch(out)
		if m == nil {
			return 0, fmt.Errorf("pdfinfo: no Pages line in %q", out)
		}
		return strconv.Atoi(m[1])
	}
}

// vipsCount counts pages with vipsheader's n-pages field.
func vipsCount(vipsheader *tool.Tool) func(context.Context, string) (int, error) {
	return func(ctx context.Context, pdf string) (int, error) {
		out, err := output(ctx, vipsheader, "-f", "n-pages", pdf)
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(out))
	}
}

// magickCount counts pages with magick identify, which prints the image's frame count once per
// frame; the first line is enough.
func magickCount(magick *tool.Tool) func(context.Context, string) (int, error) {
	return func(ctx context.Context, pdf string) (int, error) {
		out, err := output(ctx, magick, "identify", "-format", `%n\n`, pdf)
		if err != nil {
			return 0, err
		}
		first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		return strconv.Atoi(first)
	}
}
