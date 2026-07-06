# Island Flavoured HTML

A `.island.html` file is HTML with **one convention**: a data island. Everything else is plain HTML — islandc doesn't care about it.

```
<script id="island-data"> const islandData = { ... }; </script>
```

The island name comes from the filename, PascalCased: `profile.island.html` → `Profile`, `user_card.island.html` → `UserCard`.

## Data island

`<script id="island-data">` whose body is a single assignment statement — conventionally `const islandData = { ... };` — where the object literal is JWCC (JSON with comments and trailing commas). A JWCC object literal is valid JavaScript, so the browser executes the assignment natively: the raw file works when opened directly in a browser, comments and all. No server, no build step, no parse helper.

```html
<script id="island-data">
  const islandData = {
    "count": 0, // current click count
    "step": 1,  // amount added/removed per click
  };
</script>
```

**islandc owns the object literal** — everything from the first `{` to its matching `}`. `Render<Name>` replaces it with `json.Marshal(data)` at serve time. The assignment prefix (`const islandData = `) and suffix (`;`) are userspace and ship verbatim, so you can rename the binding if you need to (e.g. to compose multiple islands into one page).

The data script must **not** have a `type` attribute: `type="application/json"` would make it inert (the assignment never runs, breaking the standalone preview) and `type="module"` would scope the `const` away from your other scripts. A top-level `const` in a classic script creates a global lexical binding, readable from every other script on the page — including `type="module"` scripts:

```js
const data = islandData;
```

Splicing is safe by construction: Go's `json.Marshal` HTML-escapes `<`, `>`, and `&`, so rendered data can never contain `</script>`.

Note for strict CSP setups: the data island is an inline script like any other, so `script-src` must allow it (islands already rely on inline scripts).

### JWCC

- `//` and `/* */` comments are legal in the source — in the browser too, since the literal is JavaScript. Trailing comments on properties become Go doc comments. (Rendered output is pure JSON.)
- Trailing commas are legal.
- Types are inferred from the placeholder: integers → `int`, floats → `float64`, etc. Across array elements, `int` promotes to `float64` if a float is present; otherwise mixed types are an error.

### The rest is userspace

islandc ignores everything else in the file — the root mount, client scripts, styles, whatever you put in there. A common pattern is an element with `id="island-root"` as a mount point for a client script, but that's your business, not a convention.

## Lib imports

`<link rel="stylesheet" href="...">` and `<script src="...">` are lib imports. Two flavours:

### CDN

`<link rel="stylesheet" href="https://...">` and `<script src="https://...">` with http(s) URLs are CDN deps. They ship verbatim by default.

`--resolve-deps` downloads each unique URL into `<target>/islandc.deps/` (dumb cache, indexed by `islandc.manifest.json`, sha256-pinned per entry for reproducible builds). CSS files are rewritten at download time so every `url()` and `@import` is resolved and inlined as a data URI — fonts and images baked in, the cached CSS is self-contained. Sub-resources that can't be fetched are left as absolute URLs and produce a warning (they survive as external URLs and the hermeticity check catches them). The generated Go file embeds the vendored files alongside the source island and splices them in place of the CDN tags at render time — no intermediate files, the source island stays pristine:

- `<link rel="stylesheet" href="https://...">` → `<style>...</style>`
- `<script src="https://..." defer></script>` → `<script defer>...</script>` (other attrs preserved, `src` dropped)

Duplicates within a file are inlined once. Unresolved URLs (download failures, non-200) fall back to the verbatim CDN tag with a warning. JS containing `</script` is escaped to `<\/script` so it always inlines safely. Commit the cache for hermetic builds.

### Local (bring your own bundler)

`<link rel="stylesheet" href="./style.css">` and `<script src="./bundle.js">` with relative paths (`./x` or bare `sub/x`) are local deps. They're **always-on** — no flag. The file sits in the package dir and is embedded directly (no copy, no cache entry). This is the hatch for bundled or complex single-file libs. Missing files warn (or fail under `--strict`). Non-local relative refs (`/abs`, `//protocol-relative`, `data:`) are always left untouched.

## Hermeticity

Use single-file builds of JS libs. `islandc` will inline them and tell you if the result isn't hermetic.

By default, `islandc` prints a notice for any external URL that survives into the generated output — in your markup (`<img src="https://…">`), an unresolved CDN dep tag, or a surviving `url()`/`@import` inside inlined CSS. Under `--strict`, those are build errors. Local file deps are hermetic by construction (embedded at build time) and never trip the check. `data:` URIs (the ones `--resolve-deps` generated) are ignored, as is the data-island placeholder body.

A handful of JS patterns (`import(`, `from "https://..."`, `new Worker(`, `fetch("https://..."`, `importScripts(`) are reported as warnings only — they indicate a runtime fetch, never a strict failure.

## Example (vanilla JS)

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Profile</title>
    <style>
      .who { display: flex; gap: 12px; align-items: center; }
      .name { font-weight: 600; }
      .role { color: #888; }
      .stats { display: flex; gap: 16px; margin-top: 12px; }
      .stat .v { font-size: 20px; font-weight: 600; }
      .stat .l { font-size: 11px; color: #888; }
    </style>
  </head>
  <body>
    <div id="island-root">
      <div class="who">
        <img src="https://i.pravatar.cc/120?img=47" alt="" />
        <div>
          <div class="name">Mara Okafor</div>
          <div class="role">Staff Engineer · Platform</div>
        </div>
      </div>
    </div>

    <!-- Data island — islandc replaces the object literal with json.Marshal(data) -->
    <script id="island-data">
      const islandData = {
        "name": "Mara Okafor",
        "role": "Staff Engineer · Platform",
        "avatar": "https://i.pravatar.cc/120?img=47",
        "stats": [
          { "label": "commits / week", "value": 142 },
          { "label": "reviews / week", "value": 38 },
          { "label": "p50 latency", "value": 11.4 }
        ]
      };
    </script>

    <!-- Client script — reads the data binding, rebuilds #island-root -->
    <script type="module">
      const data = islandData;
      const root = document.getElementById("island-root");
      root.innerHTML = `
        <div class="who">
          <img src="${data.avatar}" alt="" />
          <div>
            <div class="name">${data.name}</div>
            <div class="role">${data.role}</div>
          </div>
        </div>
      `;
    </script>
  </body>
</html>
```

## Example (Alpine.js)

No render script needed — the root mount binds declaratively.

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Counter</title>
    <script defer src="https://cdn.jsdelivr.net/npm/alpinejs@3.14.1/dist/cdn.min.js"></script>
  </head>
  <body>
    <div id="island-root">
      <div class="counter" x-data="counter()">
        <button @click="dec()">−</button>
        <span x-text="count"></span>
        <button @click="inc()">+</button>
      </div>
    </div>

    <!-- Data island -->
    <script id="island-data">
      const islandData = {
        "count": 0, // current click count
        "step": 1,  // amount added/removed per click
      };
    </script>

    <!-- Alpine component factory -->
    <script>
      document.addEventListener("alpine:init", () => {
        window.Alpine.data("counter", () => ({
          ...islandData,
          inc() { this.count += this.step },
          dec() { this.count -= this.step },
        }));
      });
    </script>
  </body>
</html>
```

## Generated output

For `profile.island.html`, `islandc` emits into `islandc.gen.go`:

```go
type ProfileData struct {
    Avatar string             `json:"avatar"`
    Name   string             `json:"name"`
    Role   string             `json:"role"`
    Stats  []ProfileDataStats `json:"stats"`
}

type ProfileDataStats struct {
    Label string  `json:"label"`
    Value float64 `json:"value"`
}

func RenderProfile(w io.Writer, d ProfileData) error { /* HTML with the data object literal = json.Marshal(d) */ }
```

Generated code imports only the standard library. `islandc` itself uses `golang.org/x/net/html` and `github.com/tailscale/hujson` at generation time only.
