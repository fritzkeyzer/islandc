# Island Flavoured HTML

A `.island.html` file is HTML with **one convention**: a data island. Everything else is plain HTML.

```
<script id="island-data"> const islandData = { ... }; </script>
```

The island name comes from the filename, PascalCased: `profile.island.html` → `Profile`, `user_card.island.html` → `UserCard`.

## Data island

A `<script id="island-data">` whose body is a single assignment — `const islandData = { ... };` — where the object literal is JWCC (JSON with comments and trailing commas). JWCC is valid JavaScript, so the browser executes the assignment and the raw file works when opened directly.

```html
<script id="island-data">
  const islandData = {
    "count": 0, // current click count
    "step": 1,  // amount added/removed per click
  };
</script>
```

**islandc replaces the object literal:** 
- `Render<Name>` replaces everything from the first `{` to its matching `}` 
- with `json.Marshal(data)` at serve time. 

Rules:

- The data script must **not** have a `type` attribute: `type="application/json"` makes it inert (breaking the standalone preview) and `type="module"` scopes the `const` away from other scripts. A top-level `const` in a classic script is readable from every other script on the page, including modules.
- Comments: `//` and `/* */` are legal; trailing comments on properties become Go doc comments. Rendered output is pure JSON.
- Types are inferred from the placeholder: integers → `int`, floats → `float64`, etc. Across array elements, `int` promotes to `float64` if a float is present; otherwise mixed types are an error.
- `json.Marshal` HTML-escapes `<`, `>`, and `&`, so rendered data cannot contain `</script>`.
- Strict CSP: the data island is an inline script, so `script-src` must allow it.

### External data

When multiple islands share the same schema, data can be referenced from an external file:

```html
<script id="island-data" src="./shared_data.json"></script>
```

- **Tag form**: `<script id="island-data" src="./file.json"></script>`. Must have an empty body and no `type` attribute.
- **Path resolution**: `src` must be a local relative path (`./x` or bare `sub/x` — not `http(s)`, `//`, `/abs`, or `data:`), resolved against the island file's directory.
- **JWCC format**: The JSON file is JWCC (comments and trailing commas allowed), must be a non-empty object literal, and is dev-time schema input only — it is never embedded in the generated Go file.
- **Tag replacement**: At render time, `islandc` replaces the whole script tag with `<script id="island-data">const islandData = <json.Marshal(d)>;</script>`.
- **Shared struct naming**: The struct type name derives from the JSON filename PascalCased + `Data` (e.g. `shared_data.json` → `SharedDataData`). Islands referencing the same JSON file share a single struct definition. Referencing different schemas under the same derived type name is a build error.
- **Error semantics**: Missing or invalid JSON fails the build immediately, even without `--strict`.

Everything else — mount elements, client scripts, styles — is userspace.

## Schema inference

`islandc` infers Go struct definitions from the placeholder object literal:

- **JSON types**:
  - `string` → `string`
  - `boolean` (`true` / `false`) → `bool`
  - Integers (numbers without decimal points or exponents, e.g. `0`, `42`) → `int`
  - Floats (numbers with `.`, `e`, or `E`, e.g. `11.4`, `1e-3`) → `float64`
  - Nested objects → named nested Go structs (`<Parent><FieldName>`, e.g. `ProfileDataStats`)
  - Arrays → typed slices (`[]T`). An empty array (`[]`) infers as `[]interface{}`.
- **Array element type merging**: Schemas of sibling elements are merged:
  - If an array contains both `int` and `float64` numbers, `int` promotes to `float64`.
  - Incompatible mixed types in an array (e.g. `string` and `int`, or object and primitive) produce an error.
  - Sibling objects merge property-by-property.
- **Field naming & tags**: Property names are converted to exported PascalCase field names (`count` → `Count`, `hour24` → `Hour24`, `h` → `H`). Struct tags preserve the exact JSON key (`json:"hour24"`). Fields within each struct are sorted alphabetically.
- **Doc comments**: Same-line trailing comments (`//` or `/* */`) after a property value become Go doc comments (`// <FieldName> <comment>`). Comments on separate lines before a property are not attached to fields.

## Lib imports

`<link rel="stylesheet" href="...">` and `<script src="...">` are lib imports, in two forms:

### CDN

http(s) URLs ship verbatim by default. With `--resolve-deps`, each unique URL is downloaded into `<target>/islandc.deps/` (indexed by `islandc.manifest.json`, sha256-pinned — commit the cache for hermetic builds). CSS is rewritten so every `url()` and `@import` is inlined as a data URI. The generated Go file embeds the cached files and splices them in place of the tags at render time:

- `<link rel="stylesheet" href="https://...">` → `<style>...</style>`
- `<script src="https://..." defer></script>` → `<script defer>...</script>` (other attrs preserved, `src` dropped)

Duplicates within a file are inlined once. Unresolved URLs fall back to the verbatim CDN tag with a warning. JS containing `</script` is escaped to `<\/script`.

### Local (bring your own bundler)

Relative paths (`./x` or bare `sub/x`) are local deps — always-on, no flag. Embedded directly from the package dir. Use this for bundled or complex single-file libs. Missing files warn (or fail under `--strict`). Non-local refs (`/abs`, `//protocol-relative`, `data:`) are left untouched.

## Hermeticity

`islandc` inlines single-file JS builds and warns if the result is not hermetic.

By default, any external URL that survives into the generated output (markup, unresolved dep tag, or `url()`/`@import` inside inlined CSS) prints a warning; under `--strict` it's a build error. Local deps and `data:` URIs are exempt. JS patterns that indicate a runtime fetch (`import(`, `new Worker(`, `fetch("https://..."`, …) are warnings only.

## Example (vanilla JS)

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Profile</title>
  </head>
  <body>
    <div id="profile-root">
      <div class="name">Mara Okafor</div>
      <div class="role">Staff Engineer · Platform</div>
    </div>

    <!-- Data island — islandc replaces the object literal with json.Marshal(data) -->
    <script id="island-data">
      const islandData = {
        "name": "Mara Okafor",
        "role": "Staff Engineer · Platform",
        "stats": [
          { "label": "commits / week", "value": 142 },
          { "label": "p50 latency", "value": 11.4 }
        ]
      };
    </script>

    <!-- Client script — reads the data binding, rebuilds the mount element -->
    <script type="module">
      const data = islandData;
      const root = document.getElementById("profile-root");
      root.innerHTML = `
        <div class="name">${data.name}</div>
        <div class="role">${data.role}</div>
      `;
    </script>
  </body>
</html>
```

## Example (Alpine.js)

Alpine.js binds declaratively, so no render script is needed.

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Counter</title>
    <script defer src="https://cdn.jsdelivr.net/npm/alpinejs@3.14.1/dist/cdn.min.js"></script>
  </head>
  <body>
    <div class="counter" x-data="counter()">
      <button @click="dec()">−</button>
      <span x-text="count"></span>
      <button @click="inc()">+</button>
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
    Name  string             `json:"name"`
    Role  string             `json:"role"`
    Stats []ProfileDataStats `json:"stats"`
}

type ProfileDataStats struct {
    Label string  `json:"label"`
    Value float64 `json:"value"`
}

func RenderProfile(w io.Writer, d ProfileData) error { /* HTML with the object literal = json.Marshal(d) */ }
```

And for `counter.island.html` (with trailing comments on properties):

```go
type CounterData struct {
    // Count current click count
    Count int `json:"count"`
    // Step amount added/removed per click
    Step  int `json:"step"`
}

func RenderCounter(w io.Writer, d CounterData) error { /* HTML with the object literal = json.Marshal(d) */ }
```

Generated code imports only the standard library.
