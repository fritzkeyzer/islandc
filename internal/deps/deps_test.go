package deps

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolver_downloadsMissingAndCachesExisting(t *testing.T) {
	dir := t.TempDir()

	const cssURL = "https://cdn.example.com/base.css"
	const jsURL = "https://cdn.example.com/util.js"
	cssBody := []byte(".shared{color:#0f0}\n")
	jsBody := []byte("window.util=function(){return 42;};\n")

	fetches := map[string][]byte{
		cssURL: cssBody,
		jsURL:  jsBody,
	}
	r := &Resolver{
		HTTPGet: func(url string) (io.ReadCloser, error) {
			b, ok := fetches[url]
			if !ok {
				t.Fatalf("unexpected fetch: %s", url)
			}
			return io.NopCloser(bytes.NewReader(b)), nil
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}

	kindOf := map[string]string{cssURL: "css", jsURL: "js"}
	res, err := r.Resolve(dir, []string{cssURL, jsURL}, kindOf)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Resolved) != 2 {
		t.Fatalf("resolved %d, want 2: %+v", len(res.Resolved), res.Resolved)
	}
	if len(res.Missing) != 0 {
		t.Errorf("missing %d, want 0: %+v", len(res.Missing), res.Missing)
	}

	// Files must exist in the cache dir and contain the fetched content.
	for u, want := range map[string][]byte{cssURL: cssBody, jsURL: jsBody} {
		name, ok := res.Resolved[u]
		if !ok {
			t.Errorf("no resolved file for %s", u)
			continue
		}
		got, err := os.ReadFile(filepath.Join(res.Dir, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("content of %s = %q, want %q", name, got, want)
		}
	}

	// Manifest must record both entries.
	man, err := loadManifest(res.Dir)
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	if len(man.Entries) != 2 {
		t.Errorf("manifest has %d entries, want 2", len(man.Entries))
	}
	for _, e := range man.Entries {
		if e.DownloadedAt.Unix() != 1700000000 {
			t.Errorf("entry %s has wrong time: %v", e.URL, e.DownloadedAt)
		}
	}

	// Second Resolve must NOT re-download (cache is dumb): remove the fetch
	// map entries; if Resolve tries to fetch, it will fail the test.
	fetches[cssURL] = nil
	fetches[jsURL] = nil
	r.HTTPGet = func(url string) (io.ReadCloser, error) {
		t.Fatalf("unexpected re-download: %s", url)
		return nil, nil
	}
	res2, err := r.Resolve(dir, []string{cssURL, jsURL}, kindOf)
	if err != nil {
		t.Fatalf("Resolve (cached): %v", err)
	}
	if len(res2.Resolved) != 2 {
		t.Errorf("second resolve got %d, want 2", len(res2.Resolved))
	}
}

func TestResolver_missingDownloadFallsBackGracefully(t *testing.T) {
	dir := t.TempDir()
	const goodURL = "https://cdn.example.com/ok.js"
	const badURL = "https://cdn.example.com/missing.js"
	r := &Resolver{
		HTTPGet: func(url string) (io.ReadCloser, error) {
			if url == badURL {
				return nil, &downloadError{msg: "simulated network failure"}
			}
			return io.NopCloser(strings.NewReader("ok")), nil
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}
	res, err := r.Resolve(dir, []string{goodURL, badURL}, map[string]string{goodURL: "js", badURL: "js"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := res.Resolved[goodURL]; !ok {
		t.Errorf("good URL not resolved: %+v", res.Resolved)
	}
	if len(res.Missing) != 1 || res.Missing[0] != badURL {
		t.Errorf("missing = %+v, want [%s]", res.Missing, badURL)
	}
}

type downloadError struct{ msg string }

func (e *downloadError) Error() string { return e.msg }

func TestResolver_default_rejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok.js" {
			io.WriteString(w, "ok")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	res, err := NewResolver().Resolve(t.TempDir(), []string{srv.URL + "/ok.js", srv.URL + "/gone.js"}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := res.Resolved[srv.URL+"/ok.js"]; !ok {
		t.Errorf("ok.js not resolved: %+v", res.Resolved)
	}
	if len(res.Missing) != 1 || res.Missing[0] != srv.URL+"/gone.js" {
		t.Errorf("missing = %+v, want the 404 URL (error pages must never be vendored)", res.Missing)
	}
}
