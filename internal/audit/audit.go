// Package audit checks one island for hermeticity: any external URL surviving
// into the generated output is a Finding. By default findings are warnings;
// under --strict, Strict findings fail the build.
//
// The check assembles the static output (source HTML slices + inlined dep
// contents + an empty data-island placeholder) using parts.Plan, then scans
// it for:
//
//   - External URLs (strict): http(s):// or protocol-relative // in
//     src/href/srcset/poster attributes and in CSS url()/@import inside
//     <style> blocks. Caught in user markup, unresolved CDN dep tags, and
//     surviving url()/@import inside inlined CSS.
//   - JS heuristics (warning only, never strict): import(), from "https://",
//     new Worker(), fetch("https://"), importScripts() — patterns that
//     indicate runtime fetches.
//
// data: URIs (the ones Phase 2 generated) and the data-island placeholder
// blob are not scanned.
package audit

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/fritzkeyzer/islandc/internal/island"
	"github.com/fritzkeyzer/islandc/internal/parts"
	"golang.org/x/net/html"
)

// Finding is one hermeticity issue in one island.
type Finding struct {
	Island string // source filename
	URL    string // the offending URL (empty for JS heuristics without a URL)
	Reason string // human-readable explanation
	// Strict is true when this finding fails the build under --strict.
	// External URLs are strict; JS heuristics are never strict.
	Strict bool
}

func (f Finding) String() string {
	if f.URL == "" {
		return f.Reason
	}
	return fmt.Sprintf("%s (%s)", f.URL, f.Reason)
}

// CheckIsland assembles f's static output (using depContents for inlined
// deps) and scans it for surviving external URLs and JS fetch heuristics.
// depContents maps a dep URL to its vendored body; URLs not in the map are
// treated as unresolved (their CDN tag ships verbatim and is scanned as
// user markup).
func CheckIsland(f *island.File, depContents map[string][]byte) []Finding {
	out := assemble(f, depContents)
	return scan(out, f.Path)
}

// assemble builds the static output for one island: source HTML slices
// interleaved with inlined dep content (CSS wrapped in <style>, JS wrapped
// in a <script> with the dep's rebuilt open tag) and an empty data-island
// placeholder. The placeholder is intentionally empty so the data island
// — whose real content is json.Marshal(d) at serve time and unknown at
// build time — is not scanned.
func assemble(f *island.File, depContents map[string][]byte) []byte {
	resolved := make(map[string]string, len(depContents))
	for u := range depContents {
		resolved[u] = ""
	}
	var out bytes.Buffer
	for _, p := range parts.Plan(f, resolved) {
		switch {
		case p.Blob:
			// empty placeholder — not scanned
		case p.DepURL != "":
			body := depContents[p.DepURL]
			switch p.Kind {
			case island.DepCSS:
				out.WriteString("<style>")
				out.Write(body)
				out.WriteString("</style>")
			case island.DepJS:
				out.WriteString(p.ScriptOpenTag)
				// Escape </script so the assembled output parses as one
				// script block (matches the codegen render-time escape).
				out.Write(bytes.ReplaceAll(body, []byte("</script"), []byte("<\\/script")))
				out.WriteString("</script>")
			}
		default:
			out.Write(f.HTML[p.Src[0]:p.Src[1]])
		}
	}
	return out.Bytes()
}

// scan tokenizes the assembled output and collects findings.
func scan(out []byte, islandPath string) []Finding {
	var findings []Finding
	z := html.NewTokenizer(bytes.NewReader(out))
	var inStyle, inScript, scriptIsData bool
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return findings
		case html.StartTagToken, html.SelfClosingTagToken:
			tag, attrs := tagAttrs(z)
			for _, ref := range externalAttrURLs(tag, attrs) {
				findings = append(findings, Finding{
					Island: islandPath,
					URL:    ref.url,
					Reason: fmt.Sprintf("external URL in <%s %s>", tag, ref.attr),
					Strict: true,
				})
			}
			switch tag {
			case "style":
				inStyle = true
			case "script":
				inScript = true
				scriptIsData = attrs["id"] == "island-data"
			}
		case html.TextToken:
			raw := z.Raw()
			switch {
			case inStyle:
				findings = append(findings, scanCSSRefs(raw, islandPath)...)
			case inScript && !scriptIsData:
				findings = append(findings, scanJSHeuristics(raw, islandPath)...)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "style":
				inStyle = false
			case "script":
				inScript = false
				scriptIsData = false
			}
		}
	}
}

type attrRef struct {
	attr string
	url  string
}

// externalAttrURLs returns the external URLs found in src/href/srcset/poster
// attributes of a start tag. data: URIs and relative refs are ignored.
func externalAttrURLs(tag string, attrs map[string]string) []attrRef {
	var refs []attrRef
	for _, attr := range []string{"src", "href", "poster"} {
		if v, ok := attrs[attr]; ok {
			if u := externalURL(v); u != "" {
				refs = append(refs, attrRef{attr, u})
			}
		}
	}
	if v, ok := attrs["srcset"]; ok {
		for _, u := range externalURLsInSrcset(v) {
			refs = append(refs, attrRef{"srcset", u})
		}
	}
	return refs
}

// externalURL returns the URL if v is an external (http(s):// or
// protocol-relative //) ref, else "". data: URIs and relative refs are not
// external.
func externalURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "data:") {
		return ""
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "//") {
		return v
	}
	return ""
}

// externalURLsInSrcset splits a srcset value into candidates and returns
// those that are external URLs.
func externalURLsInSrcset(v string) []string {
	var out []string
	for _, cand := range strings.Split(v, ",") {
		fields := strings.Fields(strings.TrimSpace(cand))
		if len(fields) == 0 {
			continue
		}
		if u := externalURL(fields[0]); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// cssURLRe and cssImportRe mirror the deps package's CSS scanners: they find
// url(...) and @import refs inside <style> blocks. data: URIs and fragment
// refs are ignored.
var (
	cssURLRe    = regexp.MustCompile(`url\(\s*(?:'([^']*)'|"([^"]*)"|([^'")\s]+))\s*\)`)
	cssImportRe = regexp.MustCompile(`@import\s+(?:url\(\s*(?:'([^']*)'|"([^"]*)"|([^'")\s]+))\s*\)|'([^']*)'|"([^"]*)")`)
)

// scanCSSRefs scans CSS text (the body of a <style>) for external url() and
// @import refs — the survivors of Phase 2's rewrite. Each is a strict finding.
func scanCSSRefs(text []byte, islandPath string) []Finding {
	var findings []Finding
	for _, m := range cssURLRe.FindAllSubmatch(text, -1) {
		ref := pickGroup(m)
		if isLocalRef(ref) {
			continue
		}
		if u := externalURL(ref); u != "" {
			findings = append(findings, Finding{Island: islandPath, URL: u, Reason: "css url() not rewritten (external URL survives in inlined CSS)", Strict: true})
		}
	}
	for _, m := range cssImportRe.FindAllSubmatch(text, -1) {
		ref := pickGroup(m)
		if isLocalRef(ref) {
			continue
		}
		if u := externalURL(ref); u != "" {
			findings = append(findings, Finding{Island: islandPath, URL: u, Reason: "css @import not rewritten (external URL survives in inlined CSS)", Strict: true})
		}
	}
	return findings
}

// pickGroup returns the first non-empty capture group from a submatch slice
// (the URL captured by one of the CSS regexes' alternations).
func pickGroup(m [][]byte) string {
	for _, g := range m[1:] {
		if len(g) > 0 {
			return string(g)
		}
	}
	return ""
}

func isLocalRef(ref string) bool {
	return ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#")
}

// jsHeuristicRe matches patterns that indicate a runtime fetch from JS:
// dynamic import(), remote import-from, new Worker(), fetch() of an external
// URL, and importScripts(). These are warning-only — never strict.
var jsHeuristicRe = regexp.MustCompile(`from\s+["']https?://[^"']+["']|import\(|new\s+Worker\s*\(|fetch\s*\(\s*["']https?://[^"']+["']|importScripts\s*\(`)

func scanJSHeuristics(text []byte, islandPath string) []Finding {
	var findings []Finding
	for _, m := range jsHeuristicRe.FindAll(text, -1) {
		findings = append(findings, Finding{
			Island: islandPath,
			Reason: classifyJSHeuristic(string(m)),
			Strict: false,
		})
	}
	return findings
}

func classifyJSHeuristic(m string) string {
	switch {
	case strings.HasPrefix(m, "import("):
		return "JS dynamic import()"
	case strings.HasPrefix(m, "from"):
		return `JS remote import (from "https://…")`
	case strings.HasPrefix(m, "new"):
		return "JS new Worker()"
	case strings.HasPrefix(m, "fetch"):
		return `JS fetch() to external URL`
	case strings.HasPrefix(m, "importScripts"):
		return "JS importScripts()"
	}
	return "JS external ref heuristic"
}

// tagAttrs reads the current tag's name and attributes from the tokenizer.
func tagAttrs(z *html.Tokenizer) (string, map[string]string) {
	name, hasAttr := z.TagName()
	attrs := map[string]string{}
	for hasAttr {
		var k, v []byte
		k, v, hasAttr = z.TagAttr()
		attrs[string(k)] = string(v)
	}
	return string(name), attrs
}
