//go:build linux

package document

// Spool exposes spool to the black-box tests, which test its contract on its own.
var Spool = spool

// GhostscriptRender and GhostscriptRenderArgs expose the released renderer's run and its
// arguments to the renderer bake-off, which reads a PDF from stdin to probe streaming, measures
// each run's usage, and runs the same arguments through its launcher for peak memory.
var (
	GhostscriptRender     = (*Ghostscript).render
	GhostscriptRenderArgs = renderArgs
)
