package parts

import (
	"bytes"
	"testing"

	"github.com/fritzkeyzer/islandc/internal/island"
)

func mustParse(t *testing.T, name string, src []byte) *island.File {
	t.Helper()
	f, err := island.Parse(name, src)
	if err != nil {
		t.Fatalf("Parse %s: %v", name, err)
	}
	return f
}

func TestPlan_sourceOnlyIsland(t *testing.T) {
	f := mustParse(t, "x.island.html", []byte(
		`<!DOCTYPE html><html><body>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`))
	parts := Plan(f, nil)
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3: %+v", len(parts), parts)
	}
	if parts[0].DepURL != "" || parts[0].Blob || parts[0].Src[1] != f.DataOpen {
		t.Errorf("part0 = %+v, want Src ending at DataOpen", parts[0])
	}
	if !parts[1].Blob {
		t.Errorf("part1 = %+v, want Blob", parts[1])
	}
	if parts[2].DepURL != "" || parts[2].Blob || parts[2].Src[0] != f.DataClose || parts[2].Src[1] != len(f.HTML) {
		t.Errorf("part2 = %+v, want Src from DataClose to end", parts[2])
	}
}

func TestPlan_inlinesResolvedDepsDedup(t *testing.T) {
	cssURL := "https://cdn.example.com/a.css"
	jsURL := "https://cdn.example.com/b.js"
	src := []byte(`<!DOCTYPE html><html><body>
<link rel="stylesheet" href="` + cssURL + `" />
<link rel="stylesheet" href="` + cssURL + `" />
<script src="` + jsURL + `" defer></script>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f := mustParse(t, "x.island.html", src)
	resolved := map[string]string{cssURL: "a.css", jsURL: "b.js"}
	parts := Plan(f, resolved)

	var got []string
	for _, p := range parts {
		switch {
		case p.Blob:
			got = append(got, "blob")
		case p.DepURL != "":
			got = append(got, "dep:"+p.DepURL+":"+string(p.Kind)+":"+p.ScriptOpenTag)
		default:
			got = append(got, "src")
		}
	}
	want := []string{"src", "dep:" + cssURL + ":css:", "src", "src", "dep:" + jsURL + ":js:<script defer>", "src", "blob", "src"}
	if len(got) != len(want) {
		t.Fatalf("parts = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("part %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPlan_unresolvedDepsVerbatim(t *testing.T) {
	cssURL := "https://cdn.example.com/a.css"
	src := []byte(`<!DOCTYPE html><html><body>
<link rel="stylesheet" href="` + cssURL + `" />
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f := mustParse(t, "x.island.html", src)
	parts := Plan(f, nil)
	for _, p := range parts {
		if p.DepURL != "" {
			t.Errorf("unresolved dep must not produce a Dep part: %+v", p)
		}
	}
	// The whole HTML (link tag verbatim + data slot) must be reconstructable
	// from the Src parts + blob.
	var assembled []byte
	for _, p := range parts {
		if p.Blob {
			assembled = append(assembled, []byte("<BLOB>")...)
			continue
		}
		assembled = append(assembled, f.HTML[p.Src[0]:p.Src[1]]...)
	}
	if !bytes.Contains(assembled, []byte(`href="`+cssURL+`"`)) {
		t.Errorf("verbatim CDN tag missing from assembled output: %s", assembled)
	}
}
