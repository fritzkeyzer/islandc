# islandc

> Generate Go code from island-flavoured HTML files.

`islandc` scans a directory for `*.island.html` files and emits one self-contained `islandc.gen.go` per directory. The generated file imports only the standard library — no runtime dependency on `islandc`.

An `.island.html` file is plain HTML with **one convention**: a data island — `<script id="island-data">` whose body is a single assignment, `const islandData = { ... };`, where the object literal is JWCC (JSON with comments and trailing commas). JWCC is valid JavaScript, so the raw file works when opened directly in a browser — no server, no build step. Everything else in the file is userspace; islandc ignores it.

`islandc` infers the data schema from the placeholder literal, emits a typed Go struct, and generates `Render<Name>(w io.Writer, d <Name>Data) error` — which writes the HTML with the object literal replaced by `json.Marshal(d)`. Trailing comments on properties become Go doc comments.

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
| `-resolve-deps` | `false` | Download CDN deps into `<target>/islandc.deps/`, embed them, and splice them in at render time; cached CSS is rewritten to be self-contained (fonts inlined); each entry pinned by sha256 for reproducibility |
| `-strict` | `false`       | Fail the build if any external URL survives into the generated output (hermeticity check); warnings otherwise |
| `-q`   | `false`          | Suppress progress output                                   |

```sh
islandc -pkg views -r ./web
```

## Lib imports

`<link rel="stylesheet" href="...">` and `<script src="...">` are lib imports. They come in two flavours:

- **CDN** (`https://...`): ship verbatim by default. With `--resolve-deps`, each unique URL is downloaded into `<target>/islandc.deps/` (dumb cache, `islandc.manifest.json` index, sha256-pinned for reproducible builds). CSS files are rewritten at download time so every `url()` and `@import` is resolved and inlined as a data URI — fonts and images baked in, no sub-resource files. Unresolved URLs fall back to the verbatim CDN tag with a warning.
- **Local** (`./bundle.js`, `./style.css`, or bare relative paths): always-on, no flag. The file sits in the package dir and is embedded directly (no copy, no cache entry). This is the bring-your-own-bundler hatch — use it for bundled or complex single-file libs. Missing files warn (or fail under `--strict`).

The generated Go file embeds the vendored/local files and splices them in place of the import tags at render time — no intermediate files, the source island stays pristine:

- `<link rel="stylesheet" href="...">` → `<style>...</style>`
- `<script src="..." defer></script>` → `<script defer>...</script>` (other attrs preserved, `src` dropped)

Duplicates within a file are inlined once. JS containing `</script` is escaped to `<\/script` so it always inlines safely. Commit the cache for hermetic builds.

```sh
islandc --resolve-deps ./web
```

## Hermeticity

Use single-file builds of JS libs. `islandc` will inline them and tell you if the result isn't hermetic.

By default, `islandc` prints a notice for any external URL that survives into the generated output (in your markup, an unresolved CDN dep tag, or a `url()`/`@import` inside inlined CSS). Under `--strict`, those are build errors. Local file deps are hermetic by construction (embedded at build time) and never trip the check. `data:` URIs (the ones `--resolve-deps` generated) are ignored.

A handful of JS patterns (`import(`, `from "https://..."`, `new Worker(`, `fetch("https://..."`, `importScripts(`) are reported as warnings only — they indicate a runtime fetch, never a strict failure.
