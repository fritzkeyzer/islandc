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
		"//go:embed profile.island.html",
		"var profileHTML []byte",
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
	// The generated file embeds the source .island.html via //go:embed,
	// so the source file must be present alongside views.go at build time.
	if err := os.WriteFile(filepath.Join(viewsDir, f.Path), f.HTML, 0o644); err != nil {
		t.Fatalf("write %s: %v", f.Path, err)
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

// mkIsland builds a minimal valid island HTML source from a name and a
// JSON schema string, with a shape-compatible placeholder. It is a helper
// for tests that want to exercise the generator without a fixture file.
func mkIsland(t *testing.T, name, schema, placeholder string) *island.File {
	t.Helper()
	src := []byte(`<!DOCTYPE html><html><body>
<div id="island-root"></div>
<script type="application/schema+json" id="island-schema">` + schema + `</script>
<script type="application/json" id="island-data">` + placeholder + `</script>
<script type="module" data-island-render></script>
</body></html>`)
	f, err := island.Parse(name, src)
	if err != nil {
		t.Fatalf("Parse %s: %v", name, err)
	}
	return f
}

// writeTempModule writes a self-contained generated views package plus a
// driver into a temp dir and returns the dir path. The driver source must
// import "gentest/views". The source .island.html files are written
// alongside the generated views.go so //go:embed can find them at build
// time.
func writeTempModule(t *testing.T, files []*island.File, generated []byte, driver string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module gentest\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	viewsDir := filepath.Join(dir, "views")
	if err := os.Mkdir(viewsDir, 0o755); err != nil {
		t.Fatalf("mkdir views: %v", err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "views.go"), generated, 0o644); err != nil {
		t.Fatalf("write views.go: %v", err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(viewsDir, f.Path), f.HTML, 0o644); err != nil {
			t.Fatalf("write %s: %v", f.Path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(driver), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return dir
}

// TestGenerate_counter exercises integer + boolean scalar fields and
// verifies the generated Render<Name> splices live data into the slot.
func TestGenerate_counter(t *testing.T) {
	schema := `{"type":"object","properties":{"count":{"type":"integer"},"label":{"type":"string"},"active":{"type":"boolean"}}}`
	placeholder := `{"count":0,"label":"clicks","active":true}`
	f := mkIsland(t, "counter.island.html", schema, placeholder)

	out, err := Generate(Config{PackageName: "views", Files: []*island.File{f}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("generated output is not valid Go: %v\n---\n%s", err, out)
	}

	s := normalizeSpaces(string(out))
	for _, want := range []string{
		"type CounterData struct {",
		"Count int `json:\"count\"`",
		"Label string `json:\"label\"`",
		"Active bool `json:\"active\"`",
		"func RenderCounter(w io.Writer, d CounterData) error {",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated output missing %q\n---\n%s", want, out)
		}
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
	err := views.RenderCounter(&buf, views.CounterData{Count: 42, Label: "clicks", Active: true})
	if err != nil { fmt.Println("ERR", err); return }
	out := buf.String()
	if !strings.Contains(out, "island-root") { fmt.Println("MISSING island-root"); return }
	openIdx := strings.Index(out, "island-data")
	gt := strings.Index(out[openIdx:], ">")
	slotStart := openIdx + gt + 1
	closeIdx := strings.Index(out[slotStart:], "</script>")
	slot := out[slotStart : slotStart+closeIdx]
	if !strings.Contains(slot, "42") { fmt.Println("count not spliced"); return }
	if strings.Contains(slot, "0") { fmt.Println("placeholder count leaked"); return }
	fmt.Println("OK")
}
`
	dir := writeTempModule(t, []*island.File{f}, out, driver)
	if build := exec(t, dir, "go", "build", "./..."); build != "" {
		t.Fatalf("go build failed:\n%s", build)
	}
	if got := exec(t, dir, "go", "run", "."); got != "OK\n" {
		t.Fatalf("go run output = %q, want %q", got, "OK\n")
	}
}

// TestGenerate_todoList exercises an array of nested objects (each item is
// an object with string + boolean fields) and checks the generated nested
// struct name and field types.
func TestGenerate_todoList(t *testing.T) {
	schema := `{"type":"object","properties":{"title":{"type":"string"},"items":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"done":{"type":"boolean"}}}}}}`
	placeholder := `{"title":"Today","items":[{"text":"write tests","done":false}]}`
	f := mkIsland(t, "todo_list.island.html", schema, placeholder)

	out, err := Generate(Config{PackageName: "views", Files: []*island.File{f}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("generated output is not valid Go: %v\n---\n%s", err, out)
	}

	s := normalizeSpaces(string(out))
	for _, want := range []string{
		"type TodoListData struct {",
		"Title string `json:\"title\"`",
		"Items []TodoListDataItems `json:\"items\"`",
		"type TodoListDataItems struct {",
		"Text string `json:\"text\"`",
		"Done bool `json:\"done\"`",
		"func RenderTodoList(w io.Writer, d TodoListData) error {",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated output missing %q\n---\n%s", want, out)
		}
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
	err := views.RenderTodoList(&buf, views.TodoListData{
		Title: "Today",
		Items: []views.TodoListDataItems{
			{Text: "write tests", Done: true},
			{Text: "ship it", Done: false},
		},
	})
	if err != nil { fmt.Println("ERR", err); return }
	out := buf.String()
	if !strings.Contains(out, "island-root") { fmt.Println("MISSING island-root"); return }
	openIdx := strings.Index(out, "island-data")
	gt := strings.Index(out[openIdx:], ">")
	slotStart := openIdx + gt + 1
	closeIdx := strings.Index(out[slotStart:], "</script>")
	slot := out[slotStart : slotStart+closeIdx]
	if !strings.Contains(slot, "ship it") { fmt.Println("second item not spliced"); return }
	if strings.Contains(slot, "write tests") && !strings.Contains(slot, "true") {
		fmt.Println("done flag not marshaled"); return
	}
	fmt.Println("OK")
}
`
	dir := writeTempModule(t, []*island.File{f}, out, driver)
	if build := exec(t, dir, "go", "build", "./..."); build != "" {
		t.Fatalf("go build failed:\n%s", build)
	}
	if got := exec(t, dir, "go", "run", "."); got != "OK\n" {
		t.Fatalf("go run output = %q, want %q", got, "OK\n")
	}
}

// TestGenerate_chatWidget combines a nested object (the user) with an array
// of nested objects (messages), exercising both object and array nesting in
// one schema. It verifies the generated nested struct names and that the
// rendered output contains the live data.
func TestGenerate_chatWidget(t *testing.T) {
	schema := `{"type":"object","properties":{"user":{"type":"object","properties":{"name":{"type":"string"},"online":{"type":"boolean"}}},"messages":{"type":"array","items":{"type":"object","properties":{"author":{"type":"string"},"body":{"type":"string"},"ts":{"type":"number"}}}}}}`
	placeholder := `{"user":{"name":"Mara","online":true},"messages":[{"author":"Mara","body":"hi","ts":1.5}]}`
	f := mkIsland(t, "chat-widget.island.html", schema, placeholder)

	out, err := Generate(Config{PackageName: "views", Files: []*island.File{f}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("generated output is not valid Go: %v\n---\n%s", err, out)
	}

	s := normalizeSpaces(string(out))
	for _, want := range []string{
		"type ChatWidgetData struct {",
		"User ChatWidgetDataUser `json:\"user\"`",
		"Messages []ChatWidgetDataMessages `json:\"messages\"`",
		"type ChatWidgetDataUser struct {",
		"Name string `json:\"name\"`",
		"Online bool `json:\"online\"`",
		"type ChatWidgetDataMessages struct {",
		"Author string `json:\"author\"`",
		"Body string `json:\"body\"`",
		"Ts float64 `json:\"ts\"`",
		"func RenderChatWidget(w io.Writer, d ChatWidgetData) error {",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated output missing %q\n---\n%s", want, out)
		}
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
	err := views.RenderChatWidget(&buf, views.ChatWidgetData{
		User: views.ChatWidgetDataUser{Name: "Mara", Online: false},
		Messages: []views.ChatWidgetDataMessages{
			{Author: "Mara", Body: "hello world", Ts: 12.5},
		},
	})
	if err != nil { fmt.Println("ERR", err); return }
	out := buf.String()
	if !strings.Contains(out, "island-root") { fmt.Println("MISSING island-root"); return }
	openIdx := strings.Index(out, "island-data")
	gt := strings.Index(out[openIdx:], ">")
	slotStart := openIdx + gt + 1
	closeIdx := strings.Index(out[slotStart:], "</script>")
	slot := out[slotStart : slotStart+closeIdx]
	if !strings.Contains(slot, "hello world") { fmt.Println("message body not spliced"); return }
	if strings.Contains(slot, "hi") { fmt.Println("placeholder body leaked"); return }
	fmt.Println("OK")
}
`
	dir := writeTempModule(t, []*island.File{f}, out, driver)
	if build := exec(t, dir, "go", "build", "./..."); build != "" {
		t.Fatalf("go build failed:\n%s", build)
	}
	if got := exec(t, dir, "go", "run", "."); got != "OK\n" {
		t.Fatalf("go run output = %q, want %q", got, "OK\n")
	}
}

// TestGenerate_multipleInteractiveIslands verifies that several islands in
// one directory produce stable, non-colliding identifiers and that the
// combined file compiles. It also checks the snake/kebab -> PascalCase
// normalization across multiple files at once.
func TestGenerate_multipleInteractiveIslands(t *testing.T) {
	counter := mkIsland(t, "counter.island.html",
		`{"type":"object","properties":{"count":{"type":"integer"}}}`,
		`{"count":0}`)
	todo := mkIsland(t, "todo_list.island.html",
		`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"}}}}}}`,
		`{"items":[{"text":"a"}]}`)
	chat := mkIsland(t, "chat-widget.island.html",
		`{"type":"object","properties":{"user":{"type":"object","properties":{"name":{"type":"string"}}}}}`,
		`{"user":{"name":"x"}}`)

	out, err := Generate(Config{PackageName: "views", Files: []*island.File{counter, todo, chat}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("generated output is not valid Go: %v\n---\n%s", err, out)
	}

	s := normalizeSpaces(string(out))
	for _, want := range []string{
		"type CounterData struct {",
		"func RenderCounter(",
		"type TodoListData struct {",
		"func RenderTodoList(",
		"type ChatWidgetData struct {",
		"func RenderChatWidget(",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated output missing %q\n---\n%s", want, out)
		}
	}

	// Stable order: ChatWidget, Counter, TodoList (sorted by Name).
	iChat := bytes.Index(out, []byte("type ChatWidgetData struct"))
	iCounter := bytes.Index(out, []byte("type CounterData struct"))
	iTodo := bytes.Index(out, []byte("type TodoListData struct"))
	if iChat < 0 || iCounter < 0 || iTodo < 0 {
		t.Fatalf("missing one of the generated structs")
	}
	if !(iChat < iCounter && iCounter < iTodo) {
		t.Errorf("expected ChatWidget < Counter < TodoList; got chat@%d counter@%d todo@%d", iChat, iCounter, iTodo)
	}

	driver := `package main

import (
	"bytes"
	"fmt"
	"gentest/views"
)

func main() {
	var buf bytes.Buffer
	if err := views.RenderCounter(&buf, views.CounterData{Count: 7}); err != nil { fmt.Println("ERR counter", err); return }
	buf.Reset()
	if err := views.RenderTodoList(&buf, views.TodoListData{Items: []views.TodoListDataItems{{Text: "x"}}}); err != nil { fmt.Println("ERR todo", err); return }
	buf.Reset()
	if err := views.RenderChatWidget(&buf, views.ChatWidgetData{User: views.ChatWidgetDataUser{Name: "y"}}); err != nil { fmt.Println("ERR chat", err); return }
	fmt.Println("OK")
}
`
	dir := writeTempModule(t, []*island.File{counter, todo, chat}, out, driver)
	if build := exec(t, dir, "go", "build", "./..."); build != "" {
		t.Fatalf("go build failed:\n%s", build)
	}
	if got := exec(t, dir, "go", "run", "."); got != "OK\n" {
		t.Fatalf("go run output = %q, want %q", got, "OK\n")
	}
}

// TestGenerate_nameNormalization is a table-driven check that filenames in
// snake_case, kebab-case, and mixed forms produce the expected PascalCase
// island name and Render function.
func TestGenerate_nameNormalization(t *testing.T) {
	cases := []struct {
		filename string
		wantName string
		wantFunc string
	}{
		{"profile.island.html", "Profile", "RenderProfile"},
		{"user_card.island.html", "UserCard", "RenderUserCard"},
		{"user-card.island.html", "UserCard", "RenderUserCard"},
		{"nav_bar_top.island.html", "NavBarTop", "RenderNavBarTop"},
		{"pricing-table.island.html", "PricingTable", "RenderPricingTable"},
		{"mixed_snake-kebab.island.html", "MixedSnakeKebab", "RenderMixedSnakeKebab"},
		{"alreadyPascal.island.html", "AlreadyPascal", "RenderAlreadyPascal"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.filename, func(t *testing.T) {
			f := mkIsland(t, c.filename,
				`{"type":"object","properties":{"a":{"type":"string"}}}`,
				`{"a":"hi"}`)
			if f.Name != c.wantName {
				t.Errorf("Name = %q, want %q", f.Name, c.wantName)
			}
			if f.RenderFunc != c.wantFunc {
				t.Errorf("RenderFunc = %q, want %q", f.RenderFunc, c.wantFunc)
			}
		})
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
