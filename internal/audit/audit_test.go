package audit

import (
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

func strictURLFindings(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Strict && f.URL != "" {
			out = append(out, f)
		}
	}
	return out
}

func jsFindings(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if !f.Strict {
			out = append(out, f)
		}
	}
	return out
}

func TestCheckIsland_strictFindingsAcrossDocument(t *testing.T) {
	cssURL := "https://cdn.example.com/base.css"
	missingJSURL := "https://cdn.example.com/missing.js"
	src := []byte(`<!DOCTYPE html><html><body>
<img src="https://i.pravatar.cc/120?img=47" alt="" />
<link rel="stylesheet" href="` + cssURL + `" />
<script src="` + missingJSURL + `"></script>
<style>
  .bg { background: url("https://cdn.example.com/bg.png"); }
  @import "https://cdn.example.com/extra.css";
</style>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f := mustParse(t, "x.island.html", src)

	// base.css is resolved: its inlined content has a surviving external url()
	// (a Phase-2 fetch failure). missing.js is unresolved: its CDN tag stays
	// verbatim and is scanned as user markup.
	depContents := map[string][]byte{
		cssURL: []byte(`.x { background: url("https://cdn.example.com/fallback.png"); }`),
	}

	findings := CheckIsland(f, depContents)
	stricts := strictURLFindings(findings)
	// Expect: img src, unresolved JS src, surviving CSS url() in inlined
	// dep, plus the user-markup <style> url() and @import. 5 strict.
	if len(stricts) != 5 {
		t.Fatalf("got %d strict findings, want 5: %+v", len(stricts), findStrictURLs(t, findings))
	}
	gotURLs := map[string]bool{}
	for _, s := range stricts {
		gotURLs[s.URL] = true
	}
	for _, want := range []string{
		"https://i.pravatar.cc/120?img=47",
		missingJSURL,
		"https://cdn.example.com/fallback.png",
		"https://cdn.example.com/bg.png",
		"https://cdn.example.com/extra.css",
	} {
		if !gotURLs[want] {
			t.Errorf("missing strict finding for %q in %+v", want, stricts)
		}
	}
}

func TestCheckIsland_jsHeuristicsAreNonStrict(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script id="island-data">const islandData = {"a":"hi"};</script>
<script>
  const mod = import("./dynamic");
  const w = new Worker("./worker.js");
  fetch("https://api.example.com/v1");
  importScripts("./old.js");
  import { x } from "https://cdn.example.com/mod.js";
</script>
</body></html>`)
	f := mustParse(t, "x.island.html", src)
	findings := CheckIsland(f, nil)
	js := jsFindings(findings)
	if len(js) != 5 {
		t.Fatalf("got %d JS heuristic findings, want 5: %+v", len(js), js)
	}
	for _, f := range js {
		if f.Strict {
			t.Errorf("JS heuristic finding must not be strict: %+v", f)
		}
	}
	if len(strictURLFindings(findings)) != 0 {
		t.Errorf("no strict findings expected for JS-only island: %+v", findings)
	}
}

func TestCheckIsland_dataURIsAndRelativeNotFlagged(t *testing.T) {
	cssURL := "https://cdn.example.com/base.css"
	src := []byte(`<!DOCTYPE html><html><body>
<img src="data:image/png;base64,iVBORw0KG" alt="" />
<a href="./local-page">local</a>
<link rel="stylesheet" href="` + cssURL + `" />
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f := mustParse(t, "x.island.html", src)
	// A fully-rewritten CSS dep: only data: URIs inside. Hermetic.
	depContents := map[string][]byte{
		cssURL: []byte(`.x { background: url("data:image/gif;base64,R0lGODlh"); } @import "data:;";`),
	}
	findings := CheckIsland(f, depContents)
	if len(strictURLFindings(findings)) != 0 {
		t.Errorf("data: URIs and relative refs must not be strict: %+v", findings)
	}
}

func TestCheckIsland_resolvedCDNDepIsHermetic(t *testing.T) {
	cssURL := "https://cdn.example.com/base.css"
	jsURL := "https://cdn.example.com/app.js"
	src := []byte(`<!DOCTYPE html><html><body>
<link rel="stylesheet" href="` + cssURL + `" />
<script src="` + jsURL + `" defer></script>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f := mustParse(t, "x.island.html", src)
	depContents := map[string][]byte{
		cssURL: []byte(`.x { background: url("data:image/gif;base64,R0lGODlh"); }`),
		jsURL:  []byte(`console.log("no external refs");`),
	}
	findings := CheckIsland(f, depContents)
	if len(strictURLFindings(findings)) != 0 {
		t.Errorf("fully-resolved deps must be hermetic: %+v", findings)
	}
}

func TestCheckIsland_localDepsAreHermetic(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<link rel="stylesheet" href="./style.css" />
<script src="./bundle.js" defer></script>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f := mustParse(t, "x.island.html", src)
	depContents := map[string][]byte{
		"./style.css": []byte(`body { color: green; }`),
		"./bundle.js": []byte(`console.log("local");`),
	}
	findings := CheckIsland(f, depContents)
	if len(strictURLFindings(findings)) != 0 {
		t.Errorf("local deps must be hermetic (no external URL findings): %+v", findings)
	}
}

func findStrictURLs(t *testing.T, findings []Finding) []string {
	t.Helper()
	var out []string
	for _, f := range findings {
		if f.Strict {
			out = append(out, f.URL+" ("+f.Reason+")")
		}
	}
	return out
}
