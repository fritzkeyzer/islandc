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

	"github.com/fritzkeyzer/islandc/internal/audit"
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
		fmt.Fprintln(errw, "--resolve-deps downloads and embeds them, spliced in at render time.")
		fmt.Fprintln(errw, "Local file deps (./x.js, ./x.css) are always embedded from the package dir.")
		fmt.Fprintln(errw, "--strict fails the build if any external URL survives into the output.")
	}

	pkgName := fs.String("pkg", "", "Go package name (default: dir base name)")
	outName := fs.String("out", "islandc.gen.go", "name of the generated Go file")
	recursive := fs.Bool("r", false, "recurse into subdirectories; one .go file per dir")
	resolveDeps := fs.Bool("resolve-deps", false, "download CDN deps into <target>/islandc.deps/, embed them, and splice them in at render time; unresolved deps ship verbatim")
	strict := fs.Bool("strict", false, "fail the build if any external URL survives into the generated output (hermeticity check)")
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
		if err := generateDir(dir, *pkgName, *outName, *resolveDeps, *strict, *quiet, out, errw); err != nil {
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

func generateDir(dir, pkgName, outName string, resolveDeps, strict, quiet bool, out, errw io.Writer) error {
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

	resolved, err := resolveCDNDeps(dir, files, resolveDeps, quiet, out, errw)
	if err != nil {
		return err
	}

	// Local file deps are always-on: stat each and add the existing ones to
	// the resolved map (value = path with leading ./ stripped, so codegen
	// embeds from the package dir, not the deps cache). Missing local files
	// warn (or fail under --strict) and ship verbatim.
	resolved, err = resolveLocalDeps(dir, files, resolved, strict, errw)
	if err != nil {
		return err
	}

	generated, err := codegen.Generate(codegen.Config{
		PackageName: pkgName,
		Files:       files,
		Version:     currentVersion(),
		Resolved:    resolved,
		DepsDir:     deps.CacheDir,
	})
	if err != nil {
		return err
	}

	outPath := filepath.Join(dir, outName)
	if err := os.WriteFile(outPath, generated, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	if !quiet {
		fmt.Fprintf(out, "islandc: wrote %s (%d island(s))\n", outPath, len(files))
	}

	// Hermeticity audit: always runs. Findings are warnings by default;
	// under --strict, any Strict finding fails the build.
	depContents, err := readDepContents(dir, files, resolved)
	if err != nil {
		return err
	}
	var strictFailure bool
	for _, f := range files {
		for _, finding := range audit.CheckIsland(f, depContents) {
			if finding.Strict && strict {
				fmt.Fprintf(errw, "islandc: %s: error: %s\n", f.Path, finding)
				strictFailure = true
			} else {
				fmt.Fprintf(errw, "islandc: %s: warning: %s\n", f.Path, finding)
			}
		}
	}
	if strictFailure {
		return fmt.Errorf("hermeticity check failed (external URLs survive under --strict)")
	}
	return nil
}

// readDepContents reads each resolved dep's cached file into a map keyed by
// URL, so the audit can scan inlined CSS/JS content. CDN deps read from the
// deps cache dir; local deps read from the package dir. Returns an empty map
// when nothing is resolved.
func readDepContents(dir string, files []*island.File, resolved map[string]string) (map[string][]byte, error) {
	if len(resolved) == 0 {
		return nil, nil
	}
	localURLs := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Deps {
			if d.Local {
				localURLs[d.URL] = true
			}
		}
	}
	depsDir := filepath.Join(dir, deps.CacheDir)
	out := make(map[string][]byte, len(resolved))
	for u, name := range resolved {
		var p string
		if localURLs[u] {
			p = filepath.Join(dir, name)
		} else {
			p = filepath.Join(depsDir, name)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			// Stale cache or missing file: skip — the dep ships verbatim
			// and the audit catches the surviving tag.
			continue
		}
		out[u] = b
	}
	return out, nil
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

// resolveCDNDeps downloads any missing CDN deps into the vendor cache and
// returns the URL -> cache filename map for codegen to embed and splice at
// render time. Unresolved deps fall back to the verbatim CDN URL. Returns
// nil when dep resolution is disabled or there are no deps to resolve.
func resolveCDNDeps(dir string, files []*island.File, resolveDeps, quiet bool, out, errw io.Writer) (map[string]string, error) {
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
	for _, w := range res.Warnings {
		fmt.Fprintf(errw, "islandc: %s: warning: dep %s\n", dir, w)
	}
	if !quiet {
		fmt.Fprintf(out, "islandc: %s: resolved %d dep(s), %d unresolved\n", dir, len(res.Resolved), len(res.Missing))
	}
	return res.Resolved, nil
}

// collectDepURLs returns the unique CDN URLs across all files and a map from
// each URL to its kind ("css" or "js") for the manifest. Local deps are
// excluded — they're handled by resolveLocalDeps.
func collectDepURLs(files []*island.File) (urls []string, kindOf map[string]string) {
	seen := map[string]bool{}
	kindOf = map[string]string{}
	for _, f := range files {
		for _, d := range f.Deps {
			if d.Local {
				continue
			}
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

// resolveLocalDeps stats each local file dep and adds existing ones to
// resolved (value = path with leading "./" stripped, embedded from the
// package dir). Missing local files warn, or fail the build under --strict.
func resolveLocalDeps(dir string, files []*island.File, resolved map[string]string, strict bool, errw io.Writer) (map[string]string, error) {
	seen := map[string]bool{}
	var hadMissing bool
	for _, f := range files {
		for _, d := range f.Deps {
			if !d.Local || seen[d.URL] {
				continue
			}
			seen[d.URL] = true
			rel := strings.TrimPrefix(d.URL, "./")
			if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
				fmt.Fprintf(errw, "islandc: %s: warning: local dep %q not found; it will ship verbatim\n", f.Path, d.URL)
				if strict {
					hadMissing = true
				}
				continue
			}
			if resolved == nil {
				resolved = map[string]string{}
			}
			resolved[d.URL] = rel
		}
	}
	if hadMissing {
		return resolved, fmt.Errorf("hermeticity check failed (missing local dep under --strict)")
	}
	return resolved, nil
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
