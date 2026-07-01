package codegen

import (
	"bytes"
	"go/format"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fritzkeyzer/islandc/internal/island"
)

// execCmd is wrapped so tests could swap it, but mainly to keep the
// import name distinct from the local exec() helper.
var execCmd = osexec.Command

func TestGenerate_profileFixture(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profile.island.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := island.Parse("profile.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Generate(Config{
		PackageName: "views",
		Files:       []*island.File{f},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Output must be gofmt-clean (Generate already formats, but verify).
	if _, err := format.Source(out); err != nil {
		t.Fatalf("generated output is not valid Go: %v\n---\n%s", err, out)
	}

	s := normalizeSpaces(string(out))
	for _, want := range []string{
		"package views",
		"import (",
		"\"encoding/json\"",
		"\"io\"",
		"type ProfileData struct {",
		"Name string `json:\"name\"`",
		"Role string `json:\"role\"`",
		"Avatar string `json:\"avatar\"`",
		"Stats []ProfileDataStats `json:\"stats\"`",
		"type ProfileDataStats struct {",
		"Label string `json:\"label\"`",
		"Value float64 `json:\"value\"`",
		"var profileHTML = []byte(",
		"func RenderProfile(w io.Writer, d ProfileData) error {",
		"json.Marshal(d)",
		"injectIsland(w, profileHTML, blob,",
		"func injectIsland(w io.Writer, html, blob []byte, openEnd, closeStart int) error {",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated output missing %q\n---\n%s", want, out)
		}
	}
}

// TestGenerate_compilesAndRuns verifies the generated code actually compiles
// and that RenderProfile splices the marshaled blob into the island-data slot.
func TestGenerate_compilesAndRuns(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profile.island.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := island.Parse("profile.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Generate(Config{PackageName: "views", Files: []*island.File{f}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Write the generated file into a temp module and run `go build` +
	// a tiny driver that calls RenderProfile. This is the real end-to-end
	// check: the generated file must be self-contained and compile.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module gentest\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	viewsDir := filepath.Join(dir, "views")
	if err := os.Mkdir(viewsDir, 0o755); err != nil {
		t.Fatalf("mkdir views: %v", err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "views.go"), out, 0o644); err != nil {
		t.Fatalf("write views.go: %v", err)
	}
	driver := `package main

	import (
		"bytes"
		"fmt"
		"gentest/views"
		"strings"
	)

func main() {
	var buf bytes.Buffer
	err := views.RenderProfile(&buf, views.ProfileData{
		Name:   "Alice Tanaka",
		Role:   "Principal Engineer",
		Avatar: "https://example.com/a.png",
		Stats: []views.ProfileDataStats{
			{Label: "commits", Value: 88},
		},
	})
	if err != nil { fmt.Println("ERR", err); return }
	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("Alice Tanaka")) {
		fmt.Println("MISSING name in output")
		return
	}
	if !bytes.Contains([]byte(out), []byte("island-root")) {
		fmt.Println("MISSING island-root")
		return
	}
	// The island-data slot must now hold the marshaled real data, not the
	// placeholder. Locate the slot by finding the opening tag end and the
	// closing </script>, then inspect the bytes between.
	openIdx := strings.Index(out, "island-data")
	if openIdx < 0 { fmt.Println("NO island-data marker"); return }
	gt := strings.Index(out[openIdx:], ">")
	if gt < 0 { fmt.Println("NO opening tag end"); return }
	slotStart := openIdx + gt + 1
	closeIdx := strings.Index(out[slotStart:], "</script>")
	if closeIdx < 0 { fmt.Println("NO closing script"); return }
	slot := out[slotStart : slotStart+closeIdx]
	if strings.Contains(slot, "Mara Okafor") {
		fmt.Println("PLACEHOLDER LEAKED into island-data slot")
		return
	}
	if !strings.Contains(slot, "Alice Tanaka") {
		fmt.Println("real data missing from island-data slot")
		return
	}
	fmt.Println("OK")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(driver), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// go mod tidy is unnecessary (stdlib only); just build & run.
	build := exec(t, dir, "go", "build", "./...")
	if build != "" {
		t.Fatalf("go build failed:\n%s", build)
	}
	got := exec(t, dir, "go", "run", ".")
	if got != "OK\n" {
		t.Fatalf("go run output = %q, want %q", got, "OK\n")
	}
}

func TestGenerate_multipleFilesStableOrder(t *testing.T) {
	mk := func(name, schema string) *island.File {
		src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">` + schema + `</script>
<script type="application/json" id="island-data">{"a":"hi"}</script>
<script type="module" data-island-render></script>
</body></html>`)
		f, err := island.Parse(name, src)
		if err != nil {
			t.Fatalf("Parse %s: %v", name, err)
		}
		return f
	}
	zeta := mk("zeta.island.html", `{"type":"object","properties":{"a":{"type":"string"}}}`)
	alpha := mk("alpha.island.html", `{"type":"object","properties":{"a":{"type":"string"}}}`)

	out1, err := Generate(Config{PackageName: "views", Files: []*island.File{zeta, alpha}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	out2, err := Generate(Config{PackageName: "views", Files: []*island.File{alpha, zeta}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Errorf("output is not stable across input orderings")
	}
	// alpha must appear before zeta (sorted by Name).
	iAlpha := bytes.Index(out1, []byte("type AlphaData struct"))
	iZeta := bytes.Index(out1, []byte("type ZetaData struct"))
	if iAlpha < 0 || iZeta < 0 || iAlpha > iZeta {
		t.Errorf("expected Alpha before Zeta; alpha@%d zeta@%d", iAlpha, iZeta)
	}
}

// exec runs a command in dir and returns combined output as a string.
// It fails the test only on a non-build/run error (the caller interprets
// the output text to decide pass/fail).
func exec(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := execCmd(name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String() + "\n[error: " + err.Error() + "]"
	}
	return buf.String()
}

// normalizeSpaces collapses runs of spaces and tabs into a single space so
// assertions don't depend on gofmt's exact alignment.
func normalizeSpaces(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}
