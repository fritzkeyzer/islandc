# islandc

> Generate Go code from island-flavoured HTML files.

`islandc` scans a directory for `*.island.html` files and emits one self-contained `islandc.gen.go` per directory. Generated code imports only the standard library.

An `.island.html` file is plain HTML with **one convention**: a data island — `<script id="island-data">` containing `const islandData = { ... };`, where the object literal is JWCC (JSON with comments and trailing commas). JWCC is valid JavaScript, so the raw file works when opened directly in a browser — no server, no build step. Everything else in the file is userspace.

`islandc` infers a typed Go struct from the placeholder literal and generates `Render<Name>(w io.Writer, d <Name>Data) error`, which writes the HTML with the literal replaced by `json.Marshal(d)`.

Full spec: `ISLAND_FLAVOURED_HTML.md` or `islandc --docs`.

## Install

```sh
go install github.com/fritzkeyzer/islandc/cmd/islandc@latest
```

## Usage

```sh
islandc target/dir           # writes target/dir/islandc.gen.go
islandc -pkg views -r ./web  # recurse, custom package name
```

| Flag            | Default          | Description                                        |
|-----------------|------------------|----------------------------------------------------|
| `-pkg`          | dir base name    | Go package name                                    |
| `-out`          | `islandc.gen.go` | Generated file name                                |
| `-r`            | off              | Recurse into subdirectories; one `.go` file per dir |
| `-resolve-deps` | off              | Download CDN deps into `<target>/islandc.deps/` and embed them |
| `-prune`        | on with `-resolve-deps` | Delete cached deps (files + manifest entries) no longer referenced |
| `-no-prune`     | off              | Keep unreferenced cached deps                      |
| `-strict`       | off              | Fail if any external URL survives into the output  |
| `-q`            | off              | Suppress progress output                           |

## Dependencies

`<link rel="stylesheet" href="...">` and `<script src="...">` are lib imports:

- **CDN** (`https://...`): ship verbatim by default. With `--resolve-deps`, each unique URL is downloaded into `<target>/islandc.deps/` (sha256-pinned cache — commit it for hermetic builds), embedded, and inlined in place of the tag at render time. Cached CSS is rewritten to be self-contained (fonts and images inlined as data URIs).
- **Local** (`./bundle.js`, `./style.css`): always embedded from the package dir, no flag needed. This is the bring-your-own-bundler hatch.

`islandc` warns about any external URL that survives into the generated output; `--strict` makes that an error. Use single-file builds of JS libs.
