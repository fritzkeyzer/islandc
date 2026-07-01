// Command islandc generates self-contained Go handlers from .island.html files.
//
// See the project README and concept.html for the file format. In short:
// each .island.html file carries placeholder DOM, a JSON Schema, placeholder
// JSON, and a render script. islandc emits one .go file per target directory
// with typed structs, embedded HTML bytes, and Render<Name> functions that
// splice marshaled data into the island-data slot at serve time.
package main

import (
	"os"

	"github.com/fritzkeyzer/islandc/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
