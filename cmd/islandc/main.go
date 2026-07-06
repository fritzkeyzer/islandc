// Command islandc generates Go code from .island.html files.
//
// An .island.html file is HTML with one convention: a data island
// (<script id="island-data"> whose body is `const islandData = { ... };`,
// where the object literal is JWCC). islandc infers the schema from the
// placeholder literal, emits typed structs + Render<Name> functions that
// replace the literal with json.Marshal(data) at serve time. Everything
// else in the file is userspace.
//
// See ISLAND_FLAVOURED_HTML.md for the full spec.
package main

import (
	"os"

	"github.com/fritzkeyzer/islandc/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
