// Package deps resolves CDN lib imports found in island files: it downloads
// each unique http(s) URL once into a per-target vendor cache directory and
// records metadata in a manifest. The cache feeds generation-time inlining
// (see codegen.Bake); unresolved deps fall back gracefully to the original
// CDN URL shipping verbatim.
//
// Cache layout (under <target-dir>/islandc.deps/):
//
//	islandc.manifest.json   # [{url, file, kind, downloaded_at}]
//	<sha256>.css            # vendored dep content, named by SHA-256 of the URL
//	<sha256>.js
//
// The cache is intentionally dumb: if a file exists for a URL (per the
// manifest), it is not re-downloaded.
package deps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const CacheDir = "islandc.deps"
const ManifestName = "islandc.manifest.json"

// Entry is one record in the manifest: a vendored URL and its local file.
type Entry struct {
	URL          string    `json:"url"`
	File         string    `json:"file"`
	Kind         string    `json:"kind"`
	DownloadedAt time.Time `json:"downloaded_at"`
}

// Manifest is the on-disk index of vendored deps.
type Manifest struct {
	Entries []Entry `json:"entries"`
}

// Resolver manages the vendor cache for one target directory. Reuse across
// multiple target directories by calling Resolve per dir.
type Resolver struct {
	// HTTPGet fetches a URL's body. Defaults to http.Get; overridable for tests.
	HTTPGet func(url string) (io.ReadCloser, error)
	// Now returns the current time. Defaults to time.Now; overridable for tests.
	Now func() time.Time
}

// NewResolver returns a Resolver with default HTTP and clock hooks. The
// default fetch uses a 30s timeout and treats any non-200 response as a
// resolution failure (so error pages are never vendored).
func NewResolver() *Resolver {
	client := &http.Client{Timeout: 30 * time.Second}
	return &Resolver{
		HTTPGet: func(url string) (io.ReadCloser, error) {
			resp, err := client.Get(url)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			return resp.Body, nil
		},
		Now: time.Now,
	}
}

// Result reports the outcome of resolving a set of URLs against a cache dir.
type Result struct {
	Dir string
	// Resolved maps each URL that has a cached file to its filename within Dir.
	Resolved map[string]string
	// Missing is the list of URLs that could not be resolved. These fall
	// back to the verbatim CDN URL at render time.
	Missing []string
}

// Resolve ensures every URL in urls has a cached file in targetDir/CacheDir.
// For each URL without a cached file, it attempts to download it. Existing
// files are never re-downloaded.
//
// kindOf maps each URL to its kind ("css" or "js") so the manifest can
// record it; URLs not in the map default to "js".
func (r *Resolver) Resolve(targetDir string, urls []string, kindOf map[string]string) (*Result, error) {
	dir := filepath.Join(targetDir, CacheDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("deps: create cache dir %s: %w", dir, err)
	}
	man, err := loadManifest(dir)
	if err != nil {
		return nil, err
	}
	byURL := map[string]Entry{}
	for _, e := range man.Entries {
		byURL[e.URL] = e
	}

	res := &Result{Dir: dir, Resolved: map[string]string{}}
	seen := map[string]bool{}
	for _, u := range urls {
		if seen[u] {
			continue
		}
		seen[u] = true
		if e, ok := byURL[u]; ok {
			// Trust the manifest + file existence; re-download if removed.
			if _, statErr := os.Stat(filepath.Join(dir, e.File)); statErr == nil {
				res.Resolved[u] = e.File
				continue
			}
		}
		kind := kindOf[u]
		if kind == "" {
			kind = "js"
		}
		file, err := r.download(dir, u, kind)
		if err != nil {
			res.Missing = append(res.Missing, u)
			continue
		}
		entry := Entry{URL: u, File: file, Kind: kind, DownloadedAt: r.Now()}
		man.Entries = append(man.Entries, entry)
		byURL[u] = entry
		res.Resolved[u] = file
	}

	if err := saveManifest(dir, man); err != nil {
		return nil, err
	}
	sort.Strings(res.Missing)
	return res, nil
}

// download fetches u and writes its body to a file named by the SHA-256 of
// the URL, with an extension derived from kind.
func (r *Resolver) download(dir, u, kind string) (string, error) {
	body, err := r.HTTPGet(u)
	if err != nil {
		return "", fmt.Errorf("deps: fetch %s: %w", u, err)
	}
	defer body.Close()

	ext := ".js"
	if kind == "css" {
		ext = ".css"
	}
	name := hashURL(u) + ext
	tmp := filepath.Join(dir, name+".tmp")
	out, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("deps: create %s: %w", tmp, err)
	}
	if _, err := io.Copy(out, body); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("deps: write %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("deps: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("deps: rename %s: %w", name, err)
	}
	return name, nil
}

func hashURL(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:])
}

func loadManifest(dir string) (*Manifest, error) {
	p := filepath.Join(dir, ManifestName)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{}, nil
		}
		return nil, fmt.Errorf("deps: read manifest %s: %w", p, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("deps: parse manifest %s: %w", p, err)
	}
	return &m, nil
}

func saveManifest(dir string, m *Manifest) error {
	p := filepath.Join(dir, ManifestName)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("deps: marshal manifest: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return fmt.Errorf("deps: write manifest %s: %w", p, err)
	}
	return nil
}
