package deps

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolver_cssRewriteInlinesFontAndImport(t *testing.T) {
	dir := t.TempDir()
	const cssURL = "https://cdn.example.com/base.css"
	const fontURL = "https://cdn.example.com/font.woff2"
	const importedURL = "https://cdn.example.com/other.css"
	const imgURL = "https://cdn.example.com/bg.png"

	cssBody := []byte(`@import url("./other.css");
@import "./other.css";
body { background: url("./bg.png"); }
@font-face { src: url("./font.woff2") format("woff2"); }
`)
	importedBody := []byte(`.x { color: red; } /* imported */
.y { background: url("data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"); }
`)
	fontBody := []byte{0x00, 0x01, 0x02, 0x03, 0x77, 0x4F, 0x46, 0x32} // woff2-ish bytes
	imgBody := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}     // PNG signature

	fetches := map[string]struct {
		body []byte
		ct   string
	}{
		cssURL:      {cssBody, "text/css"},
		importedURL: {importedBody, "text/css"},
		fontURL:     {fontBody, "font/woff2"},
		imgURL:      {imgBody, "image/png"},
	}
	r := &Resolver{
		HTTPGet: func(u string) (io.ReadCloser, error) {
			f, ok := fetches[u]
			if !ok {
				t.Fatalf("unexpected HTTPGet: %s", u)
			}
			return io.NopCloser(bytes.NewReader(f.body)), nil
		},
		HTTPFetch: func(u string) ([]byte, string, error) {
			f, ok := fetches[u]
			if !ok {
				t.Fatalf("unexpected HTTPFetch: %s", u)
			}
			return f.body, f.ct, nil
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}

	res, err := r.Resolve(dir, []string{cssURL}, map[string]string{cssURL: "css"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Missing) != 0 {
		t.Errorf("missing: %+v", res.Missing)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings: %+v", res.Warnings)
	}

	file := res.Resolved[cssURL]
	cached, err := os.ReadFile(filepath.Join(res.Dir, file))
	if err != nil {
		t.Fatalf("read cached: %v", err)
	}
	got := string(cached)

	// The imported CSS body must appear inline (its rule and the data: URI it
	// already contained, untouched).
	if !strings.Contains(got, ".x { color: red; }") {
		t.Errorf("imported CSS rule not inlined: %s", got)
	}
	// The pre-existing data: URI inside the imported CSS is preserved verbatim.
	if !strings.Contains(got, "image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7") {
		t.Errorf("imported data URI not preserved: %s", got)
	}
	// bg.png and font.woff2 are inlined as data URIs (PNG and woff2).
	if c := strings.Count(got, "data:image/png;base64,"); c != 1 {
		t.Errorf("bg.png data URI count = %d, want 1: %s", c, got)
	}
	if c := strings.Count(got, "data:font/woff2;base64,"); c != 1 {
		t.Errorf("font.woff2 data URI count = %d, want 1: %s", c, got)
	}
	// No surviving relative url() — only data: URIs and (possibly) the
	// absolute fallbacks for failures. Here everything resolved.
	if strings.Contains(got, `url("./`) || strings.Contains(got, `url(./`) {
		t.Errorf("relative url() survived rewrite: %s", got)
	}
	// Both @import statements consumed; the second occurrence of the same
	// imported URL is also inlined (no @import url("https://...") fallback).
	if strings.Contains(got, "@import") {
		t.Errorf("surviving @import in rewritten CSS: %s", got)
	}

	// Manifest must record SHA256, stable across runs.
	man, err := loadManifest(res.Dir)
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	if len(man.Entries) != 1 || man.Entries[0].SHA256 == "" {
		t.Fatalf("manifest entry missing SHA256: %+v", man.Entries)
	}
	firstSHA := man.Entries[0].SHA256

	// Second resolve: cached, no re-download, SHA stable.
	cached2, _ := os.ReadFile(filepath.Join(res.Dir, file))
	if !bytes.Equal(cached, cached2) {
		t.Fatalf("cached CSS changed between runs")
	}
	man2, _ := loadManifest(res.Dir)
	if man2.Entries[0].SHA256 != firstSHA {
		t.Errorf("SHA256 not stable: %q vs %q", firstSHA, man2.Entries[0].SHA256)
	}
}

func TestResolver_cssSubResource404LeftVerbatimWithWarning(t *testing.T) {
	dir := t.TempDir()
	const cssURL = "https://cdn.example.com/base.css"
	const fontURL = "https://cdn.example.com/missing.woff2"

	cssBody := []byte(`@font-face { src: url("./missing.woff2") format("woff2"); }`)
	r := &Resolver{
		HTTPGet: func(u string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(cssBody)), nil
		},
		HTTPFetch: func(u string) ([]byte, string, error) {
			return nil, "", &downloadError{msg: "404"}
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}

	res, err := r.Resolve(dir, []string{cssURL}, map[string]string{cssURL: "css"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Missing) != 0 {
		t.Errorf("the CSS itself resolved; missing should be empty: %+v", res.Missing)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].URL != fontURL {
		t.Errorf("warnings = %+v, want one for %s", res.Warnings, fontURL)
	}
	cached, _ := os.ReadFile(filepath.Join(res.Dir, res.Resolved[cssURL]))
	if !bytes.Contains(cached, []byte(`url("`+fontURL+`")`)) {
		t.Errorf("failed sub-resource should be left as absolute url(): %s", cached)
	}
}

func TestResolver_sha256BackfilledForOldManifest(t *testing.T) {
	dir := t.TempDir()
	const jsURL = "https://cdn.example.com/legacy.js"
	body := []byte("console.log(1)\n")

	r := &Resolver{
		HTTPGet: func(u string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}
	_, err := r.Resolve(dir, []string{jsURL}, map[string]string{jsURL: "js"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Simulate a pre-Phase-2 manifest: drop SHA256 from the entry, leaving
	// the cached file in place.
	man, _ := loadManifest(filepath.Join(dir, CacheDir))
	man.Entries[0].SHA256 = ""
	if err := saveManifest(filepath.Join(dir, CacheDir), man); err != nil {
		t.Fatalf("saveManifest: %v", err)
	}

	// Next resolve backfills SHA256 from the cached file.
	_, err = r.Resolve(dir, []string{jsURL}, map[string]string{jsURL: "js"})
	if err != nil {
		t.Fatalf("Resolve (backfill): %v", err)
	}
	man, _ = loadManifest(filepath.Join(dir, CacheDir))
	if man.Entries[0].SHA256 == "" {
		t.Errorf("SHA256 not backfilled for old manifest entry")
	}
}
