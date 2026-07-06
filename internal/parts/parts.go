// Package parts computes the cut/inset plan for one island's render
// function: an ordered list of parts that, when concatenated, produces the
// island's HTML with CDN lib imports spliced in and the data island's object
// literal replaced by the marshaled data blob.
//
// The plan is pure data — no Go-source emission, no file I/O — so both the
// codegen emitter and the audit hermeticity checker can share it.
package parts

import (
	"sort"

	"github.com/fritzkeyzer/islandc/internal/island"
)

// Part is one element of the render plan: either a slice of the source HTML
// (Src), a dep content splice (DepURL set), or the data island's object
// literal placeholder (Blob). Exactly one of {Src, DepURL, Blob} is the
// "active" discriminator; the others are zero values.
type Part struct {
	// Src is the source HTML byte range [start, end) to emit verbatim.
	// Both zero when this part is a Dep or the Blob.
	Src [2]int

	// DepURL identifies a dep to splice in here. Empty for Src/Blob parts.
	DepURL string

	// Kind is the dep kind (only meaningful when DepURL != "").
	Kind island.DepKind

	// ScriptOpenTag is the rebuilt <script ...> opening tag for inlined JS
	// deps, with the src attribute dropped. Empty for CSS deps and
	// non-Dep parts.
	ScriptOpenTag string

	// Blob is true for the data island's object literal placeholder —
	// emitted at render time as json.Marshal(d).
	Blob bool
}

// Plan computes the ordered parts for one island's render function: slices
// of the pristine source HTML interleaved with resolved dep content (first
// occurrence inlined, duplicates dropped, unresolved deps verbatim) and the
// marshaled data blob at the data island slot.
//
// resolved maps a dep URL (CDN or local) to its vendored/embed filename.
// Any URL present is "resolved — splice it in"; URLs absent from the map
// ship verbatim. The caller is responsible for adding existing local file
// deps to resolved. May be nil.
func Plan(f *island.File, resolved map[string]string) []Part {
	type edit struct {
		start, end int
		part       *Part
	}
	var edits []edit
	inlined := map[string]bool{}
	for _, d := range f.Deps {
		if _, ok := resolved[d.URL]; !ok {
			continue // unresolved (CDN or missing local): tag ships verbatim
		}
		e := edit{start: d.TagStart, end: d.TagEnd}
		if !inlined[d.URL] {
			inlined[d.URL] = true
			e.part = &Part{DepURL: d.URL, Kind: d.Kind, ScriptOpenTag: d.ScriptOpenTag}
		}
		edits = append(edits, e)
	}
	edits = append(edits, edit{start: f.DataOpen, end: f.DataClose, part: &Part{Blob: true}})
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })

	var parts []Part
	cursor := 0
	for _, e := range edits {
		if e.start > cursor {
			parts = append(parts, Part{Src: [2]int{cursor, e.start}})
		}
		if e.part != nil {
			parts = append(parts, *e.part)
		}
		cursor = e.end
	}
	if cursor < len(f.HTML) {
		parts = append(parts, Part{Src: [2]int{cursor, len(f.HTML)}})
	}
	return parts
}
