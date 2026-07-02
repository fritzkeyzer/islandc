# islandc

> Generate self-contained Go wrapper code from island-flavored HTML files.

`islandc` scans a directory for `*.island.html` files and emits one
self-contained `islandc.gen.go` per directory. The generated file imports only
the standard library — it has no runtime dependency on `islandc`. The source
`.island.html` files are embedded at compile time via `//go:embed`, so they
must remain alongside the generated file.

Each `.island.html` file carries placeholder DOM, a JSON Schema, placeholder
JSON, and a client render script. `islandc` turns the schema into typed Go
structs and emits a `Render<Name>(w io.Writer, d <Name>Data) error` function
that writes the HTML with the placeholder JSON replaced by `json.Marshal(d)`.

See `concept.html` for the full format spec and rationale.

## Install

```sh
go install github.com/fritzkeyzer/islandc/cmd/islandc@latest
```

## Usage

```sh
islandc target/dir
```

This writes `target/dir/islandc.gen.go`. Flags:

| Flag   | Default          | Description                                                   |
|--------|------------------|---------------------------------------------------------------|
| `-pkg` | dir base name    | Optional: Go package name for the generated file              |
| `-out` | `islandc.gen.go` | Optional: Name of the generated Go file (written in each dir) |
| `-r`   | `false`          | Optional: Recurse into subdirectories; one `.go` file per dir |
| `-q`   | `false`          | Optional: Suppress progress output                            |

Example:

```sh
islandc -pkg views -r ./web
```
