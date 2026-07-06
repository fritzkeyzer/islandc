# Island Flavoured HTML

A `.island.html` file is HTML with **one convention**: a data island. Everything else is plain HTML — islandc doesn't care about it.

```
<script id="island-data" type="application/json"> { ... } </script>
```

The island name comes from the filename, PascalCased: `profile.island.html` → `Profile`, `user_card.island.html` → `UserCard`.

## Data island

`<script id="island-data" type="application/json">` with a JWCC object body (JSON with comments and trailing commas). This is the standard [inert JSON data block](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/script#embedding_data_in_html) — browsers don't execute it.

```html
<script id="island-data" type="application/json">
  {
    "count": 0, // current click count
    "step": 1,  // amount added/removed per click
  }
</script>
```

**islandc owns this body.** `Render<Name>` replaces it with `json.Marshal(data)` at serve time. The `type="application/json"` attribute is required.

The client reads the data with:

```js
const data = JSON.parse(document.getElementById("island-data").textContent);
```

### JWCC

- `//` and `/* */` comments are legal in the source. Trailing comments on properties become Go doc comments. (Rendered output is pure JSON.)
- Trailing commas are legal.
- Types are inferred from the placeholder: integers → `int`, floats → `float64`, etc. Across array elements, `int` promotes to `float64` if a float is present; otherwise mixed types are an error.

### The rest is userspace

islandc ignores everything else in the file — the root mount, client scripts, styles, whatever you put in there. A common pattern is an element with `id="island-root"` as a mount point for a client script, but that's your business, not a convention.

## CDN lib imports

`<link rel="stylesheet" href="https://...">` and `<script src="https://...">` with http(s) URLs are CDN deps. They ship verbatim by default.

`--resolve-deps` downloads each unique URL into `<target>/islandc.deps/` (dumb cache, indexed by `islandc.manifest.json`) and bakes a fully-inlined `<name>.island.gen.html` sibling per island, embedded instead of the source:

- `<link rel="stylesheet" href="https://...">` → `<style>...</style>`
- `<script src="https://..." defer></script>` → `<script defer>...</script>` (other attrs preserved, `src` dropped)

Duplicates within a file are inlined once. Unresolved URLs (download failures, non-200) and JS containing `</script>` fall back to the verbatim CDN tag with a warning. Commit the cache and baked files for hermetic builds.

Non-CDN refs (relative, `/abs`, `//protocol-relative`, `data:`) are always left untouched.

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

    <!-- Data island — islandc replaces this body with json.Marshal(data) -->
    <script id="island-data" type="application/json">
      {
        "name": "Mara Okafor",
        "role": "Staff Engineer · Platform",
        "avatar": "https://i.pravatar.cc/120?img=47",
        "stats": [
          { "label": "commits / week", "value": 142 },
          { "label": "reviews / week", "value": 38 },
          { "label": "p50 latency", "value": 11.4 }
        ]
      }
    </script>

    <!-- Client script — reads the data block, rebuilds #island-root -->
    <script type="module">
      const data = JSON.parse(document.getElementById("island-data").textContent);
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
    <script id="island-data" type="application/json">
      {
        "count": 0, // current click count
        "step": 1,  // amount added/removed per click
      }
    </script>

    <!-- Alpine component factory -->
    <script>
      document.addEventListener("alpine:init", () => {
        const data = JSON.parse(document.getElementById("island-data").textContent);
        window.Alpine.data("counter", () => ({
          ...data,
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

func RenderProfile(w io.Writer, d ProfileData) error { /* HTML with data island body = json.Marshal(d) */ }
```

Generated code imports only the standard library. `islandc` itself uses `golang.org/x/net/html` and `github.com/tailscale/hujson` at generation time only.
