# islandc

> Generate Go code from island-flavoured HTML files.

`islandc` scans a directory for `*.island.html` files and emits one self-contained `islandc.gen.go` per directory. The generated file imports only the standard library — no runtime dependency on `islandc`.

An `.island.html` file is plain HTML with **one convention**: a data island — `<script id="island-data" type="application/json">` with a JWCC object body (JSON with comments and trailing commas). Everything else in the file is userspace; islandc ignores it.

`islandc` infers the data schema from the placeholder, emits a typed Go struct, and generates `Render<Name>(w io.Writer, d <Name>Data) error` — which writes the HTML with the data island body replaced by `json.Marshal(d)`. Trailing comments on properties become Go doc comments.

See `ISLAND_FLAVOURED_HTML.md` (or `islandc --docs`) for the full spec.

## Install

```sh
go install github.com/fritzkeyzer/islandc/cmd/islandc@latest
```

## Usage

```sh
islandc target/dir
```

Writes `target/dir/islandc.gen.go`. Flags:

| Flag   | Default          | Description                                                |
|--------|------------------|------------------------------------------------------------|
| `-pkg` | dir base name    | Go package name for the generated file                     |
| `-out` | `islandc.gen.go` | Name of the generated Go file                              |
| `-r`   | `false`          | Recurse into subdirectories; one `.go` file per dir        |
| `-resolve-deps` | `false` | Download CDN deps into `<target>/islandc.deps/` and bake inlined `<name>.island.gen.html` files; unresolved deps ship verbatim |
| `-q`   | `false`          | Suppress progress output                                   |

```sh
islandc -pkg views -r ./web
```

## CDN lib imports

`<link rel="stylesheet" href="https://...">` and `<script src="https://...">` with http(s) URLs are CDN deps. They ship verbatim by default — the page fetches them from the CDN at runtime.

`--resolve-deps` downloads each URL into `<target>/islandc.deps/` (dumb cache, `islandc.manifest.json` index) and bakes an inlined `<name>.island.gen.html` sibling per island, embedded instead of the source. Unresolved URLs fall back to the verbatim CDN tag with a warning. Commit the cache and baked files for hermetic builds.

```sh
islandc --resolve-deps ./web
```
