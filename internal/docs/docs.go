// Package docs embeds the project documentation so the islandc binary can
// serve it via flags (--help, --docs, --version, --changelog) without
// relying on the filesystem.
//
// The embedded copies live alongside this package (README.md,
// ISLAND_FLAVOURED_HTML.md, version.json, CHANGELOG.md). Keep them in sync
// with the canonical files at the repository root.
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

//go:embed CHANGELOG.md
var Changelog []byte

// Print writes the given bytes to out wrapped in the given XML tag.
func Print(out io.Writer, tag string, b []byte) {
	fmt.Fprintf(out, "<%s>\n", tag)
	fmt.Fprint(out, string(b))
	fmt.Fprintf(out, "</%s>\n", tag)
}
