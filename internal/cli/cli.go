// Package cli implements the islandc command-line interface.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fritzkeyzer/islandc/internal/codegen"
	"github.com/fritzkeyzer/islandc/internal/deps"
	"github.com/fritzkeyzer/islandc/internal/docs"
	"github.com/fritzkeyzer/islandc/internal/island"
)

// Run is the CLI entrypoint. args are the program arguments (excluding argv[0]).
// out and errw are the stdout/stderr sinks (typically os.Stdout / os.Stderr).
func Run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("islandc", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.Usage = func() {
		fmt.Fprintln(errw, "islandc — generate Go code from .island.html files")
		fmt.Fprintln(errw, "")
		fmt.Fprintln(errw, "Usage: islandc [flags] [target-dir]")
		fmt.Fprintln(errw, "")
		fmt.Fprintln(errw, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(errw, "")
		fmt.Fprintln(errw, "Scans <target-dir> for *.island.html, writes one .go file per dir.")
		fmt.Fprintln(errw, "CDN deps (http(s) <link>/<script src>) ship verbatim by default;")
		fmt.Fprintln(errw, "--resolve-deps downloads and bakes them inline.")
	}

	pkgName := fs.String("pkg", "", "Go package name (default: dir base name)")
	outName := fs.String("out", "islandc.gen.go", "name of the generated Go file")
	recursive := fs.Bool("r", false, "recurse into subdirectories; one .go file per dir")
	resolveDeps := fs.Bool("resolve-deps", false, "download CDN deps into <target>/islandc.deps/ and bake inlined <name>.island.gen.html files; unresolved deps ship verbatim")
	quiet := fs.Bool("q", false, "suppress progress output")
	showHelp := fs.Bool("help", false, "print the README (wrapped in <readme> XML)")
	showDocs := fs.Bool("docs", false, "print the island-flavoured HTML reference (wrapped in <island-flavoured-html> XML)")
	showVersion := fs.Bool("version", false, "print the version info (wrapped in <version> XML)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	switch {
	case *showHelp:
		docs.Print(out, "readme", docs.README)
		return 0
	case *showDocs:
		docs.Print(out, "island-flavoured-html", docs.IslandFlavouredHTML)
		return 0
	case *showVersion:
		docs.Print(out, "version", docs.Version)
		return 0
	}

	target := "."
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(errw, "islandc: too many arguments; expected a single target dir")
		return 2
	}

	dirs, err := collectDirs(target, *recursive)
	if err != nil {
		fmt.Fprintf(errw, "islandc: %v\n", err)
		return 1
	}
	if len(dirs) == 0 {
		fmt.Fprintf(errw, "islandc: no directories with .island.html files under %q\n", target)
		return 1
	}

	var hadError bool
	for _, dir := range dirs {
		if err := generateDir(dir, *pkgName, *outName, *resolveDeps, *quiet, out, errw); err != nil {
			fmt.Fprintf(errw, "islandc: %s: %v\n", dir, err)
			hadError = true
		}
	}
	if hadError {
		return 1
	}
	return 0
}

// collectDirs returns the directories under root that contain at least one
// .island.html file. If recursive is false, only root itself is considered.
func collectDirs(root string, recursive bool) ([]string, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", root)
	}

	var dirs []string
	seen := map[string]bool{}
	addIfHasIslands := func(dir string) error {
		has, err := dirHasIslands(dir)
		if err != nil {
			return err
		}
		if has && !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
		return nil
	}

	if !recursive {
		if err := addIfHasIslands(root); err != nil {
			return nil, err
		}
		return dirs, nil
	}

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		// Skip hidden dirs (e.g. .git, node_modules).
		if path != root && strings.HasPrefix(filepath.Base(path), ".") {
			return filepath.SkipDir
		}
		return addIfHasIslands(path)
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return dirs, nil
}

func dirHasIslands(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".island.html") {
			return true, nil
		}
	}
	return false, nil
}

func generateDir(dir, pkgName, outName string, resolveDeps bool, quiet bool, out, errw io.Writer) error {
	files, err := parseDir(dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	if pkgName == "" {
		pkgName = sanitizePkgName(filepath.Base(dir))
	}

	baked, err := resolveAndBake(dir, files, resolveDeps, quiet, out, errw)
	if err != nil {
		return err
	}

	generated, err := codegen.Generate(codegen.Config{
		PackageName: pkgName,
		Files:       files,
		Version:     currentVersion(),
		Baked:       baked,
	})
	if err != nil {
		return err
	}

	// Remove stale baked files for islands not baked this run.
	for _, f := range files {
		if _, ok := baked[f.Path]; !ok {
			os.Remove(filepath.Join(dir, codegen.BakedPath(f.Path)))
		}
	}

	outPath := filepath.Join(dir, outName)
	if err := os.WriteFile(outPath, generated, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	if !quiet {
		fmt.Fprintf(out, "islandc: wrote %s (%d island(s))\n", outPath, len(files))
	}
	return nil
}

func parseDir(dir string) ([]*island.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*island.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".island.html") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		f, err := island.Parse(e.Name(), src)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		files = append(files, f)
	}
	return files, nil
}

// resolveAndBake downloads any missing CDN deps and bakes each island's
// resolved deps into an inlined sibling file. Unresolved deps fall back to
// the verbatim CDN URL. Returns nil map when dep resolution is disabled or
// there are no deps to resolve.
func resolveAndBake(dir string, files []*island.File, resolveDeps, quiet bool, out, errw io.Writer) (map[string]*codegen.Baked, error) {
	if !resolveDeps {
		return nil, nil
	}
	urls, kindOf := collectDepURLs(files)
	if len(urls) == 0 {
		return nil, nil
	}
	res, err := deps.NewResolver().Resolve(dir, urls, kindOf)
	if err != nil {
		return nil, fmt.Errorf("resolve deps: %w", err)
	}
	for _, u := range res.Missing {
		fmt.Fprintf(errw, "islandc: %s: warning: could not resolve CDN dep %q; it will ship verbatim\n", dir, u)
	}
	if !quiet {
		fmt.Fprintf(out, "islandc: %s: resolved %d dep(s), %d unresolved\n", dir, len(res.Resolved), len(res.Missing))
	}
	return bakeFiles(dir, files, res, errw)
}

// bakeFiles inlines each island's resolved deps and writes the baked sibling
// files. Islands with no resolved deps are absent from the map.
func bakeFiles(dir string, files []*island.File, res *deps.Result, errw io.Writer) (map[string]*codegen.Baked, error) {
	baked := map[string]*codegen.Baked{}
	read := func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(res.Dir, name))
	}
	for _, f := range files {
		b, warnings, err := codegen.Bake(f, res.Resolved, read)
		for _, w := range warnings {
			fmt.Fprintf(errw, "islandc: %s: warning: %s\n", dir, w)
		}
		if err != nil {
			return nil, err
		}
		if b == nil {
			continue
		}
		bakedPath := filepath.Join(dir, codegen.BakedPath(f.Path))
		if err := os.WriteFile(bakedPath, b.HTML, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", bakedPath, err)
		}
		baked[f.Path] = b
	}
	return baked, nil
}

// collectDepURLs returns the unique CDN URLs across all files and a map from
// each URL to its kind ("css" or "js") for the manifest.
func collectDepURLs(files []*island.File) (urls []string, kindOf map[string]string) {
	seen := map[string]bool{}
	kindOf = map[string]string{}
	for _, f := range files {
		for _, d := range f.Deps {
			if seen[d.URL] {
				continue
			}
			seen[d.URL] = true
			urls = append(urls, d.URL)
			kindOf[d.URL] = string(d.Kind)
		}
	}
	return urls, kindOf
}

// currentVersion parses the embedded version.json and returns the "version"
// field. Returns "" on any parse error.
func currentVersion() string {
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(docs.Version, &v); err != nil {
		return ""
	}
	return v.Version
}

func sanitizePkgName(s string) string {
	r := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, s)
	if r == "" {
		return "views"
	}
	if r[0] >= '0' && r[0] <= '9' {
		r = "v" + r
	}
	return strings.ToLower(r)
}
