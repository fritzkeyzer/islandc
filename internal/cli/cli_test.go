package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const outputFileName = "islandc.gen.go"

// TestRun_endToEnd builds the islandc binary, runs it against the testdata
// dir, and verifies the generated .go file compiles and works.
func TestRun_endToEnd(t *testing.T) {
	// Build the binary into a temp dir.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "islandc")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/islandc")
	build.Dir = projectRoot(t)
	var buf bytes.Buffer
	build.Stdout = &buf
	build.Stderr = &buf
	if err := build.Run(); err != nil {
		t.Fatalf("go build islandc: %v\n%s", err, buf.String())
	}

	// Copy testdata into a writable temp dir (the CLI writes into the target).
	work := t.TempDir()
	viewsDir := filepath.Join(work, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(projectRoot(t), "testdata", "profile.island.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "profile.island.html"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	// Run the CLI.
	var out, errw bytes.Buffer
	code := Run([]string{"-pkg", "views", viewsDir}, &out, &errw)
	if code != 0 {
		t.Fatalf("Run exit=%d stderr=%s stdout=%s", code, errw.String(), out.String())
	}
	genPath := filepath.Join(viewsDir, outputFileName)
	if _, err := os.Stat(genPath); err != nil {
		t.Fatalf("generated file not written: %v", err)
	}
	if !strings.Contains(out.String(), "wrote") {
		t.Errorf("expected progress message, got %q", out.String())
	}

	// The generated file must contain the expected symbols.
	gen, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatal(err)
	}
	gens := string(gen)
	for _, want := range []string{
		"package views",
		"type ProfileData struct {",
		"func RenderProfile(",
		"func writeParts(",
	} {
		if !strings.Contains(gens, want) {
			t.Errorf("generated file missing %q", want)
		}
	}
	// It must NOT depend on islandc at runtime. Check for a quoted import
	// path (the generated header comment may mention the repo URL, which
	// is fine — we only want to forbid an actual import).
	if strings.Contains(gens, "\"github.com/fritzkeyzer/islandc") {
		t.Errorf("generated file imports islandc; it must be self-contained")
	}

	// Compile-check: write a go.mod and a driver, then `go build`.
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module clitest\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := `package main

import (
	"bytes"
	"fmt"
	"clitest/views"
)

func main() {
	var buf bytes.Buffer
	if err := views.RenderProfile(&buf, views.ProfileData{Name: "B"}); err != nil {
		fmt.Println("ERR", err)
		return
	}
	fmt.Println("OK", buf.Len() > 0)
}
`
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}
	run := exec.Command("go", "run", ".")
	run.Dir = work
	var dbuf bytes.Buffer
	run.Stdout = &dbuf
	run.Stderr = &dbuf
	if err := run.Run(); err != nil {
		t.Fatalf("go run driver: %v\n%s", err, dbuf.String())
	}
	if !strings.Contains(dbuf.String(), "OK true") {
		t.Errorf("driver output = %q, want %q", dbuf.String(), "OK true")
	}
}

// TestRun_noIslands verifies the CLI reports cleanly when a dir has no islands.
func TestRun_noIslands(t *testing.T) {
	empty := t.TempDir()
	var out, errw bytes.Buffer
	code := Run([]string{empty}, &out, &errw)
	if code != 1 {
		t.Errorf("exit=%d, want 1", code)
	}
	if !strings.Contains(errw.String(), "no directories with .island.html") {
		t.Errorf("stderr=%q, want mention of no islands", errw.String())
	}
}

// TestRun_recursive verifies -r walks subdirectories.
func TestRun_recursive(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(projectRoot(t), "testdata", "profile.island.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "profile.island.html"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errw bytes.Buffer
	code := Run([]string{"-r", "-q", root}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errw.String())
	}
	genPath := filepath.Join(sub, outputFileName)
	if _, err := os.Stat(genPath); err != nil {
		t.Errorf("expected generated file in nested dir: %v", err)
	}
}

// TestRun_infoFlags verifies the info flags (--help, --docs, --version)
// each print the corresponding embedded doc wrapped in the expected XML tag.
func TestRun_infoFlags(t *testing.T) {
	cases := []struct {
		flag string
		tag  string
	}{
		{"--help", "<readme>"},
		{"--docs", "<island-flavoured-html>"},
		{"--version", "<version>"},
	}
	for _, c := range cases {
		var out, errw bytes.Buffer
		code := Run([]string{c.flag}, &out, &errw)
		if code != 0 {
			t.Errorf("%s: exit=%d stderr=%s", c.flag, code, errw.String())
			continue
		}
		s := out.String()
		if !strings.Contains(s, c.tag) {
			t.Errorf("%s: output missing %q", c.flag, c.tag)
		}
		if !strings.Contains(s, "</"+c.tag[1:]) {
			t.Errorf("%s: output missing closing %q", c.flag, "</"+c.tag[1:])
		}
	}
}

// TestSanitizePkgName covers the package-name sanitizer.
func TestSanitizePkgName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"views", "views"},
		{"my-app", "my_app"},
		{"123go", "v123go"},
		{"", "views"},
		{"with space", "with_space"},
	}
	for _, c := range cases {
		if got := sanitizePkgName(c.in); got != c.want {
			t.Errorf("sanitizePkgName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRun_localDeps verifies local file deps (<script src="./x.js">,
// <link rel=stylesheet href="./x.css">) are always-on: present files are
// embedded from the package dir and splice in at render time; missing files
// warn by default and fail under --strict.
func TestRun_localDeps(t *testing.T) {
	const islandHTML = `<!DOCTYPE html><html><body>
<link rel="stylesheet" href="./style.css" />
<script src="./bundle.js"></script>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`

	present := t.TempDir()
	if err := os.WriteFile(filepath.Join(present, "x.island.html"), []byte(islandHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(present, "style.css"), []byte("body{color:green}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(present, "bundle.js"), []byte("window.b=true"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errw bytes.Buffer
	if code := Run([]string{present}, &out, &errw); code != 0 {
		t.Fatalf("present local deps: exit=%d stderr=%s", code, errw.String())
	}
	gen, err := os.ReadFile(filepath.Join(present, "islandc.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"//go:embed style.css", "//go:embed bundle.js"} {
		if !strings.Contains(string(gen), want) {
			t.Errorf("generated file missing %q: %s", want, gen)
		}
	}

	// Missing local dep: warning by default (exit 0).
	missing := t.TempDir()
	if err := os.WriteFile(filepath.Join(missing, "x.island.html"), []byte(islandHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errw.Reset()
	if code := Run([]string{missing}, &out, &errw); code != 0 {
		t.Errorf("missing local dep (non-strict): exit=%d, want 0; stderr=%s", code, errw.String())
	}
	if !strings.Contains(errw.String(), "local dep") {
		t.Errorf("missing local dep should warn: %s", errw.String())
	}

	// Missing local dep under --strict: exit 1.
	out.Reset()
	errw.Reset()
	if code := Run([]string{"-strict", missing}, &out, &errw); code != 1 {
		t.Errorf("missing local dep (strict): exit=%d, want 1; stderr=%s", code, errw.String())
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/cli -> project root is two levels up.
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("go.mod not found at %s", root)
	}
	return root
}

// TestRun_strictFailsOnExternalURL verifies that --strict returns exit 1 when
// an island has a surviving external URL, and exit 0 when the island is
// hermetic.
func TestRun_strict(t *testing.T) {
	const externalHTML = `<!DOCTYPE html><html><body>
<img src="https://example.com/a.png" alt="" />
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`
	const hermeticHTML = `<!DOCTYPE html><html><body>
<img src="./local.png" alt="" />
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`

	mk := func(html string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "x.island.html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// Non-hermetic under --strict → exit 1.
	var out, errw bytes.Buffer
	if code := Run([]string{"-strict", mk(externalHTML)}, &out, &errw); code != 1 {
		t.Errorf("strict + external URL: exit=%d, want 1; stderr=%s", code, errw.String())
	}
	if !strings.Contains(errw.String(), "external URL in <img src>") {
		t.Errorf("strict stderr missing finding: %s", errw.String())
	}

	// Hermetic under --strict → exit 0.
	out.Reset()
	errw.Reset()
	if code := Run([]string{"-strict", mk(hermeticHTML)}, &out, &errw); code != 0 {
		t.Errorf("strict + hermetic: exit=%d, want 0; stderr=%s", code, errw.String())
	}

	// Non-hermetic WITHOUT --strict → still exit 0 (warning only).
	out.Reset()
	errw.Reset()
	if code := Run([]string{mk(externalHTML)}, &out, &errw); code != 0 {
		t.Errorf("non-strict + external URL: exit=%d, want 0; stderr=%s", code, errw.String())
	}
}
