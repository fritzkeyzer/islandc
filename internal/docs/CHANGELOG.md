# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.0.1] - 2026-07-01

First public release. `islandc` generates self-contained Go wrapper code from
island-flavored HTML files.

### Added
- `islandc` CLI: scans a target directory for `*.island.html` files and writes
  one self-contained `islandc.gen.go` per directory.
  - Flags: `-pkg` (Go package name), `-out` (output filename, default
    `islandc.gen.go`), `-r` (recurse into subdirectories), `-q` (quiet).
- Parser (`internal/island`) for the island file format: a JSON Schema block
  (`<script type="application/schema+json" id="island-schema">`), a placeholder
  data island (`<script type="application/json" id="island-data">`), a render
  script (`<script type="module" data-island-render>`), and a root mount
  (`id="island-root"`). Best-effort shape check between the schema and the
  placeholder JSON.
- Code generator (`internal/codegen`) emitting, per island:
  - a typed `Data` struct inferred from the JSON Schema (supports string,
    number, integer, boolean, array, object; nested objects become named
    structs),
  - the source HTML embedded as a byte literal,
  - a `Render<Name>(w io.Writer, d <Name>Data) error` function that splices
    `json.Marshal(d)` into the island-data slot at serve time,
  - the `injectIsland` runtime helper (stdlib only; no runtime dependency on
    `islandc`).
- `testdata/profile.island.html` fixture and end-to-end tests covering parse,
  codegen, CLI, recursive mode, and a compile-and-run check of the generated
  file.
- `concept.html` describing the file format and design rationale.
- `justfile` with a `test` recipe.

[0.0.1]: https://github.com/fritzkeyzer/islandc/releases/tag/v0.0.1
