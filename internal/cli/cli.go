// Package cli implements the islandc command-line interface.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fritzkeyzer/islandc/internal/codegen"
	"github.com/fritzkeyzer/islandc/internal/docs"
	"github.com/fritzkeyzer/islandc/internal/island"
)

// Run is the CLI entrypoint. args are the program arguments (excluding argv[0]).
// out and errw are the stdout/stderr sinks (typically os.Stdout / os.Stderr).
func Run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("islandc", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.Usage = func() {
		fmt.Fprintln(errw, "islandc — generate self-contained Go handlers from .island.html files")
		fmt.Fprintln(errw, "")
		fmt.Fprintln(errw, "Usage:")
		fmt.Fprintln(errw, "  islandc [flags] [target-dir]")
		fmt.Fprintln(errw, "")
		fmt.Fprintln(errw, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(errw, "")
		fmt.Fprintln(errw, "By default, scans <target-dir> for *.island.html files and writes one")
		fmt.Fprintln(errw, "self-contained .go file per directory (default: islandc_views.go).")
		fmt.Fprintln(errw, "The generated file has no dependency on islandc at runtime.")
	}

	pkgName := fs.String("pkg", "", "Go package name for generated files (default: dir base name)")
	outName := fs.String("out", "islandc.gen.go", "name of the generated Go file (written into each target dir)")
	recursive := fs.Bool("r", false, "recurse into subdirectories; emit one .go file per dir containing islands")
	quiet := fs.Bool("q", false, "suppress progress output")
	showHelp := fs.Bool("help", false, "print the README (wrapped in <readme> XML)")
	showDocs := fs.Bool("docs", false, "print the island-flavoured HTML reference (wrapped in <island-flavoured-html> XML)")
	showVersion := fs.Bool("version", false, "print the version info (wrapped in <version> XML)")
	showChangelog := fs.Bool("changelog", false, "print the changelog (wrapped in <changelog> XML)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// Info flags: print the requested embedded doc and exit.
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
	case *showChangelog:
		docs.Print(out, "changelog", docs.Changelog)
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
		if err := generateDir(dir, *pkgName, *outName, *quiet, out); err != nil {
			fmt.Fprintf(errw, "islandc: %s: %v\n", dir, err)
			hadError = true
		}
	}
	if hadError {
		return 1
	}
	return 0
}

// collectDirs returns the list of directories under root that contain at
// least one .island.html file. If recursive is false, only root itself is
// considered (and only included if it has islands).
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
		base := filepath.Base(path)
		if path != root && strings.HasPrefix(base, ".") {
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

func generateDir(dir, pkgName, outName string, quiet bool, out io.Writer) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []*island.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".island.html") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		f, err := island.Parse(e.Name(), src)
		if err != nil {
			return fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil
	}

	if pkgName == "" {
		pkgName = sanitizePkgName(filepath.Base(dir))
	}

	generated, err := codegen.Generate(codegen.Config{
		PackageName: pkgName,
		Files:       files,
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
	return nil
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
