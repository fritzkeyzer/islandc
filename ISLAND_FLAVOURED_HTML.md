# Island Flavoured HTML

A `.island.html` file is plain HTML with four conventions. `islandc` parses them by byte scan (no DOM parse) and emits typed Go code.

## Anatomy

```
profile.island.html
├── 1. Root mount          <div id="island-root"> ... </div>      placeholder DOM, replaced by the render script at runtime
├── 2. Schema block       <script type="application/schema+json" id="island-schema">  JSON Schema → Go struct
├── 3. Data island        <script type="application/json" id="island-data">          placeholder JSON, overwritten at serve time
└── 4. Render script      <script type="module" data-island-render>                 client JS, reads #island-data, rewrites #island-root
```

The island name is inferred from the filename: `profile.island.html` → `profile`, generating `ProfileData` and `RenderProfile`.

## Conventions

- **Root mount** — any element with `id="island-root"`. Holds styled sample output. The render script replaces its `innerHTML` at runtime.
- **Schema block** — `type="application/schema+json"`, `id="island-schema"`. Root must be `type: "object"` with at least one property. Supported types: `string`, `number`, `integer`, `boolean`, `array` (with `items`), `object` (with `properties`). Nested objects become named Go structs. Optional `"tag"` overrides the JSON tag.
- **Data island** — `type="application/json"`, `id="island-data"`. Holds placeholder JSON in source. Must be valid JSON and shape-compatible with the schema (best-effort check). At serve time, `islandc`'s generated `Render<Name>` splices `json.Marshal(data)` into this slot, replacing the placeholder.
- **Render script** — `type="module"`, `data-island-render`. Pure client code. Typically reads `#island-data`, builds DOM, and writes it into `#island-root`. `islandc` does not parse or execute it; it only requires its presence.

## Complete example

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Profile</title>
    <style>
      /* Styles live in the source file and ship verbatim. */
      .who { display: flex; gap: 12px; align-items: center; }
      .name { font-weight: 600; }
      .role { color: #888; }
      .stats { display: flex; gap: 16px; margin-top: 12px; }
      .stat .v { font-size: 20px; font-weight: 600; }
      .stat .l { font-size: 11px; color: #888; }
    </style>
  </head>
  <body>

    <!-- 1. Root mount — placeholder DOM.
         Real, styled sample output. The render script replaces this at runtime.
         Must be present; id must be "island-root". -->
    <div id="island-root">
      <div class="who">
        <img src="https://i.pravatar.cc/120?img=47" alt="" />
        <div>
          <div class="name">Mara Okafor</div>
          <div class="role">Staff Engineer · Platform</div>
        </div>
      </div>
      <div class="stats">
        <div class="stat">
          <div class="v">142</div>
          <div class="l">commits / week</div>
        </div>
        <div class="stat">
          <div class="v">38</div>
          <div class="l">reviews / week</div>
        </div>
        <div class="stat">
          <div class="v">11.4</div>
          <div class="l">p50 latency</div>
        </div>
      </div>
    </div>

    <!-- 2. Schema block — becomes a Go struct.
         type="application/schema+json"  id="island-schema"
         Root type must be "object" with at least one property.
         Supported: string, number, integer, boolean, array (items), object (properties).
         Nested objects become named structs (e.g. ProfileDataStats).
         Optional "tag" overrides the json tag. -->
    <script type="application/schema+json" id="island-schema">
      {
        "type": "object",
        "properties": {
          "name":   { "type": "string", "tag": "name" },
          "role":   { "type": "string", "tag": "role" },
          "avatar": { "type": "string", "tag": "avatar" },
          "stats": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "label": { "type": "string" },
                "value": { "type": "number" }
              }
            }
          }
        }
      }
    </script>

    <!-- 3. Data island — placeholder JSON in source.
         type="application/json"  id="island-data"
         Must be valid JSON and shape-compatible with the schema above.
         At serve time the generated RenderProfile overwrites this slot
         with json.Marshal(data), so the placeholder never leaks. -->
    <script type="application/json" id="island-data">
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

    <!-- 4. Render script — pure client, replaces #island-root.
         type="module"  data-island-render
         islandc does not parse or run this; it only requires its presence.
         Reads the (now real) data from #island-data and rebuilds the DOM. -->
    <script type="module" data-island-render>
      const data = JSON.parse(
        document.getElementById("island-data").textContent,
      );
      const root = document.getElementById("island-root");
      root.innerHTML = `
        <div class="who">
          <img src="${data.avatar}" alt="" />
          <div>
            <div class="name">${data.name}</div>
            <div class="role">${data.role}</div>
          </div>
        </div>
        <div class="stats">
          ${data.stats.map((s) => `
            <div class="stat">
              <div class="v">${s.value}</div>
              <div class="l">${s.label}</div>
            </div>
          `).join("")}
        </div>
      `;
    </script>

  </body>
</html>
```

## What `islandc` generates from this

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

func RenderProfile(w io.Writer, d ProfileData) error { /* splices json.Marshal(d) into the island-data slot */ }
```

The generated file imports only the standard library — no runtime dependency on `islandc`.
