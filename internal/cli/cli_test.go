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
		"func injectIsland(",
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
