// Package deps resolves CDN lib imports found in island files: it downloads
// each unique http(s) URL once into a per-target vendor cache directory and
// records metadata in a manifest. The cache feeds the generated code, which
// embeds the vendored files and splices them in place of the CDN tags at
// render time; unresolved deps fall back gracefully to the original CDN URL
// shipping verbatim.
//
// CSS resolution is hermetic-by-default: each CSS file is rewritten at
// download time so every url() and @import is resolved and inlined as a
// data URI (fonts, images, imported CSS all baked in). Sub-resources that
// can't be fetched are left as absolute URLs and produce a Warning — they
// survive as external URLs and the --strict audit catches them.
//
// Cache layout (under <target-dir>/islandc.deps/):
//
//	islandc.manifest.json   # [{url, file, kind, sha256, downloaded_at}]
//	<sha256>.css            # vendored dep content, named by SHA-256 of the URL
//	<sha256>.js
//
// The cache is intentionally dumb: if a file exists for a URL (per the
// manifest), it is not re-downloaded. SHA-256 of the cached content is
// recorded per entry for reproducibility: two builds of the same URL
// produce the same hash, so manifests are diffable and artifacts are
// bit-identical.
package deps

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const CacheDir = "islandc.deps"
const ManifestName = "islandc.manifest.json"

// Entry is one record in the manifest: a vendored URL and its local file.
type Entry struct {
	URL          string    `json:"url"`
	File         string    `json:"file"`
	Kind         string    `json:"kind"`
	SHA256       string    `json:"sha256"`
	DownloadedAt time.Time `json:"downloaded_at"`
}

// Manifest is the on-disk index of vendored deps.
type Manifest struct {
	Entries []Entry `json:"entries"`
}

// Warning is one non-fatal issue encountered while resolving a dep (e.g. a
// CSS sub-resource that could not be fetched and was left as an external
// URL). The CLI surfaces these to stderr.
type Warning struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

func (w Warning) String() string {
	if w.URL == "" {
		return w.Reason
	}
	return w.URL + ": " + w.Reason
}

// Resolver manages the vendor cache for one target directory. Reuse across
// multiple target directories by calling Resolve per dir.
type Resolver struct {
	// HTTPGet fetches a URL's body for the main dep download. Defaults to
	// http.Get; overridable for tests.
	HTTPGet func(url string) (io.ReadCloser, error)
	// HTTPFetch fetches a CSS sub-resource and returns its body plus the
	// Content-Type header (used for MIME detection). Defaults to a real
	// HTTP GET; overridable for tests.
	HTTPFetch func(url string) (body []byte, contentType string, err error)
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
		HTTPFetch: func(u string) ([]byte, string, error) {
			resp, err := client.Get(u)
			if err != nil {
				return nil, "", err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, "", err
			}
			return b, resp.Header.Get("Content-Type"), nil
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
	// Warnings are non-fatal issues: CSS sub-resources that could not be
	// fetched and were left as external URLs, or recursion errors.
	Warnings []Warning
}

// Resolve ensures every URL in urls has a cached file in targetDir/CacheDir.
// For each URL without a cached file, it attempts to download it. Existing
// files are never re-downloaded.
//
// kindOf maps each URL to its kind ("css" or "js") so the manifest can
// record it; URLs not in the map default to "js". CSS bodies are rewritten
// at download time to inline all url()/@import sub-resources as data URIs.
func (r *Resolver) Resolve(targetDir string, urls []string, kindOf map[string]string) (*Result, error) {
	dir := filepath.Join(targetDir, CacheDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("deps: create cache dir %s: %w", dir, err)
	}
	man, err := loadManifest(dir)
	if err != nil {
		return nil, err
	}
	// Backfill SHA256 for entries written before Phase 2 (one-time migration).
	for i := range man.Entries {
		if man.Entries[i].SHA256 == "" {
			if sha, ok := hashFile(dir, man.Entries[i].File); ok {
				man.Entries[i].SHA256 = sha
			}
		}
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
		file, sha, warns, err := r.download(dir, u, kind)
		if err != nil {
			res.Missing = append(res.Missing, u)
			continue
		}
		res.Warnings = append(res.Warnings, warns...)
		entry := Entry{URL: u, File: file, Kind: kind, SHA256: sha, DownloadedAt: r.Now()}
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
// the URL, with an extension derived from kind. For CSS, the body is first
// rewritten so every url()/@import sub-resource is inlined as a data URI.
// Returns the filename, the SHA-256 hex of the written content, and any
// warnings from the CSS rewrite.
func (r *Resolver) download(dir, u, kind string) (file string, sha string, warnings []Warning, err error) {
	body, err := r.HTTPGet(u)
	if err != nil {
		return "", "", nil, fmt.Errorf("deps: fetch %s: %w", u, err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return "", "", nil, fmt.Errorf("deps: read %s: %w", u, err)
	}

	if kind == "css" {
		rewritten, warns, rerr := rewriteCSS(data, u, 0, r.fetchSubResource)
		if rerr != nil {
			return "", "", nil, fmt.Errorf("deps: rewrite CSS %s: %w", u, rerr)
		}
		data = rewritten
		warnings = warns
	}

	ext := ".js"
	if kind == "css" {
		ext = ".css"
	}
	name := hashURL(u) + ext
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", "", nil, fmt.Errorf("deps: create %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return "", "", nil, fmt.Errorf("deps: rename %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	return name, hex.EncodeToString(sum[:]), warnings, nil
}

// fetchSubResource fetches a CSS url()/@import sub-resource via the resolver's
// HTTPFetch hook. Falls back to HTTPGet (no content-type) when HTTPFetch is
// unset.
func (r *Resolver) fetchSubResource(u string) ([]byte, string, error) {
	if r.HTTPFetch != nil {
		return r.HTTPFetch(u)
	}
	body, err := r.HTTPGet(u)
	if err != nil {
		return nil, "", err
	}
	defer body.Close()
	b, err := io.ReadAll(body)
	return b, "", err
}

func hashURL(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:])
}

// hashFile reads dir/file and returns the hex SHA-256 of its contents. Returns
// ok=false if the file can't be read (caller leaves the entry unchanged).
func hashFile(dir, file string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
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

// rewriteCSS makes a CSS body self-contained: every url() and @import is
// resolved against baseURL, fetched, and inlined as a data URI (for url())
// or merged inline (for @import, recursively). Sub-resources that can't be
// fetched are left as absolute URLs and recorded as Warnings — they survive
// as external URLs and the --strict audit catches them. data: URIs and
// fragment refs (url(#id)) are left untouched.
//
// depth caps @import recursion at 10 to defuse cycles.
func rewriteCSS(body []byte, baseURL string, depth int, get func(url string) (body []byte, contentType string, err error)) ([]byte, []Warning, error) {
	if depth > 10 {
		return nil, nil, fmt.Errorf("css @import recursion too deep (>%d) at %s", 10, baseURL)
	}
	var warnings []Warning
	out, warnings, err := inlineImports(body, baseURL, depth, get, warnings)
	if err != nil {
		return nil, warnings, err
	}
	out, warnings = rewriteURLs(out, baseURL, get, warnings)
	return out, warnings, nil
}

// importRe matches an @import statement in either url(...) or quoted-string
// form, capturing the referenced URL in one of groups 1-5. The match
// consumes the trailing ; (and any media-query tokens between the URL and
// the ;).
var importRe = regexp.MustCompile(`@import\s+(?:url\(\s*(?:'([^']*)'|"([^"]*)"|([^'")\s]+))\s*\)|'([^']*)'|"([^"]*)")\s*[^;]*;`)

// importURL extracts the referenced URL from an importRe match.
func importURL(m [][]byte) string {
	for _, g := range m[1:] {
		if len(g) > 0 {
			return string(g)
		}
	}
	return ""
}

// inlineImports replaces each @import with the recursively-rewritten CSS body
// of the imported file. On fetch/recursion failure, leaves an absolute
// @import url("..."); and records a Warning.
func inlineImports(body []byte, baseURL string, depth int, get func(url string) ([]byte, string, error), warnings []Warning) ([]byte, []Warning, error) {
	matches := importRe.FindAllSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return body, warnings, nil
	}
	var out bytes.Buffer
	cursor := 0
	for _, mi := range matches {
		out.Write(body[cursor:mi[0]])
		cursor = mi[1]
		ref := importURL(importRe.FindSubmatch(body[mi[0]:mi[1]]))
		if isLocalRef(ref) {
			// data:/fragment-only — leave verbatim.
			out.Write(body[mi[0]:mi[1]])
			continue
		}
		abs, err := resolveRef(baseURL, ref)
		if err != nil {
			return nil, warnings, fmt.Errorf("css @import %q: %w", ref, err)
		}
		subBody, _, err := get(abs)
		if err != nil {
			warnings = append(warnings, Warning{URL: abs, Reason: "css @import fetch failed; left as absolute @import"})
			fmt.Fprintf(&out, `@import url("%s");`, abs)
			continue
		}
		rewritten, subWarns, err := rewriteCSS(subBody, abs, depth+1, get)
		if err != nil {
			warnings = append(warnings, Warning{URL: abs, Reason: "css @import rewrite failed: " + err.Error()})
			fmt.Fprintf(&out, `@import url("%s");`, abs)
			continue
		}
		warnings = append(warnings, subWarns...)
		out.Write(rewritten)
	}
	out.Write(body[cursor:])
	return out.Bytes(), warnings, nil
}

// urlRe matches a CSS url(...) token, capturing the referenced URL in one of
// groups 1-3 (single-quoted, double-quoted, or bare). Used to inline
// sub-resources as data URIs.
var urlRe = regexp.MustCompile(`url\(\s*(?:'([^']*)'|"([^"]*)"|([^'")\s]+))\s*\)`)

// urlRef extracts the referenced URL from a urlRe match.
func urlRef(m [][]byte) string {
	for _, g := range m[1:] {
		if len(g) > 0 {
			return string(g)
		}
	}
	return ""
}

// rewriteURLs replaces each url(...) with a data URI of the fetched
// sub-resource. data: URIs and fragment refs (url(#id)) are left untouched.
// Fetch failures leave an absolute url("...") and record a Warning.
func rewriteURLs(body []byte, baseURL string, get func(url string) ([]byte, string, error), warnings []Warning) ([]byte, []Warning) {
	return urlRe.ReplaceAllFunc(body, func(m []byte) []byte {
		ref := urlRef(urlRe.FindSubmatch(m))
		if isLocalRef(ref) {
			return m
		}
		abs, err := resolveRef(baseURL, ref)
		if err != nil {
			return m
		}
		subBody, ct, err := get(abs)
		if err != nil {
			warnings = append(warnings, Warning{URL: abs, Reason: "css url() sub-resource fetch failed; left as absolute url()"})
			return []byte(`url("` + abs + `")`)
		}
		mt := mimeType(ct, abs)
		b64 := base64.StdEncoding.EncodeToString(subBody)
		return []byte(`url("data:` + mt + `;base64,` + b64 + `")`)
	}), warnings
}

// isLocalRef reports whether ref is a non-network reference that should be
// left untouched: a data: URI, a fragment (#id), or empty.
func isLocalRef(ref string) bool {
	return ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#")
}

// resolveRef resolves ref against base into an absolute URL. Absolute URLs
// (http(s)://) are returned as-is.
func resolveRef(base, ref string) (string, error) {
	baseU, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	refU, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return baseU.ResolveReference(refU).String(), nil
}

// mimeType picks the MIME type for a data URI from the Content-Type header,
// falling back to the URL extension, then to a generic binary type.
func mimeType(contentType, absURL string) string {
	if ct := strings.TrimSpace(strings.Split(contentType, ";")[0]); ct != "" {
		return ct
	}
	ext := strings.ToLower(filepath.Ext(absURL))
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	// A handful of font extensions mime.TypeByExtension doesn't always know.
	switch ext {
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	}
	return "application/octet-stream"
}
