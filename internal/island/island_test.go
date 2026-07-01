package island

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse_profileFixture(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profile.island.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := Parse("profile.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Name != "profile" {
		t.Errorf("Name = %q, want %q", f.Name, "profile")
	}
	if f.RenderFunc != "RenderProfile" {
		t.Errorf("RenderFunc = %q, want %q", f.RenderFunc, "RenderProfile")
	}
	if f.Schema == nil || f.Schema.Type != "object" {
		t.Fatalf("Schema not parsed: %+v", f.Schema)
	}
	if _, ok := f.Schema.Properties["name"]; !ok {
		t.Errorf("schema missing 'name' property")
	}
	if _, ok := f.Schema.Properties["stats"]; !ok {
		t.Errorf("schema missing 'stats' property")
	}
	if f.Schema.Properties["stats"].Items == nil {
		t.Errorf("stats schema missing items")
	}

	// Splice bounds must point at the island-data slot.
	prefix := f.HTML[:f.DataOpen]
	suffix := f.HTML[f.DataClose:]
	if !contains(prefix, []byte(`id="island-data"`)) {
		t.Errorf("DataOpen does not include the island-data opening tag")
	}
	if !contains(suffix, []byte("</script>")) {
		t.Errorf("DataClose does not start at the closing script tag")
	}
	// The placeholder JSON must be the bytes between the bounds.
	middle := f.HTML[f.DataOpen:f.DataClose]
	if !contains(middle, []byte("Mara Okafor")) {
		t.Errorf("placeholder JSON not between splice bounds; got %q", middle)
	}
}

func contains(hay, needle []byte) bool {
	return string(hay) != "" && bytesIndex(hay, needle) >= 0
}

func bytesIndex(hay, needle []byte) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestParse_missingSchema(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/json" id="island-data">{"a":1}</script>
<script type="module" data-island-render></script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected error for missing schema, got nil")
	}
}

func TestParse_missingData(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">
{"type":"object","properties":{"a":{"type":"string"}}}
</script>
<script type="module" data-island-render></script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected error for missing data island, got nil")
	}
}

func TestParse_missingRenderScript(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">
{"type":"object","properties":{"a":{"type":"string"}}}
</script>
<script type="application/json" id="island-data">{"a":"hi"}</script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected error for missing render script, got nil")
	}
}

func TestParse_missingRoot(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script type="application/schema+json" id="island-schema">
{"type":"object","properties":{"a":{"type":"string"}}}
</script>
<script type="application/json" id="island-data">{"a":"hi"}</script>
<script type="module" data-island-render></script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected error for missing root mount, got nil")
	}
}

func TestParse_emptyDataSlot(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">
{"type":"object","properties":{"a":{"type":"string"}}}
</script>
<script type="application/json" id="island-data">   </script>
<script type="module" data-island-render></script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected error for empty data slot, got nil")
	}
}

func TestParse_invalidPlaceholderJSON(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">
{"type":"object","properties":{"a":{"type":"string"}}}
</script>
<script type="application/json" id="island-data">{not json}</script>
<script type="module" data-island-render></script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected error for invalid placeholder JSON, got nil")
	}
}

func TestParse_shapeMismatch(t *testing.T) {
	// schema says a is string; placeholder has a as object -> mismatch
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">
{"type":"object","properties":{"a":{"type":"string"}}}
</script>
<script type="application/json" id="island-data">{"a":{"x":1}}</script>
<script type="module" data-island-render></script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected shape mismatch error, got nil")
	}
}

func TestParse_nameInferredFromFilename(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">
{"type":"object","properties":{"a":{"type":"string"}}}
</script>
<script type="application/json" id="island-data">{"a":"hi"}</script>
<script type="module" data-island-render></script>
</body></html>`)
	f, err := Parse("user_card.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Name != "user_card" {
		t.Errorf("Name = %q, want %q", f.Name, "user_card")
	}
	if f.RenderFunc != "RenderUser_card" {
		t.Errorf("RenderFunc = %q, want %q", f.RenderFunc, "RenderUser_card")
	}
}
