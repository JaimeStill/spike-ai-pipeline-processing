//go:build linux

package document

// GhostscriptRender and GhostscriptRenderArgs expose the released renderer's run and its
// arguments to the renderer bake-off, its measurement probe, which no exported API can serve: it
// reads a PDF from stdin to probe streaming, measures each run's usage, and runs the same
// arguments through its launcher for peak memory.
var (
	GhostscriptRender     = (*Ghostscript).render
	GhostscriptRenderArgs = renderArgs
)
