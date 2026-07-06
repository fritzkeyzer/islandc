// Package docs embeds the project documentation so the islandc binary can
// serve it via flags (--help, --docs, --version) without relying on the
// filesystem. The embedded copies live alongside this package (README.md,
// ISLAND_FLAVOURED_HTML.md, version.json) and are kept in sync with the
// canonical files at the repository root by the `just gen` recipe.
package docs

import (
	_ "embed"
	"fmt"
	"io"
)

//go:embed README.md
var README []byte

//go:embed ISLAND_FLAVOURED_HTML.md
var IslandFlavouredHTML []byte

//go:embed version.json
var Version []byte

// Print writes b to out wrapped in the given XML tag.
func Print(out io.Writer, tag string, b []byte) {
	fmt.Fprintf(out, "<%s>\n", tag)
	out.Write(b)
	fmt.Fprintf(out, "</%s>\n", tag)
}
