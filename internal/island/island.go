// Package island parses .island.html files: HTML with one convention — a
// data island (<script id="island-data" type="application/json"> with a JWCC
// object body). The schema is inferred from the placeholder; trailing
// comments become Go doc comments. CDN deps (http(s) <link>/<script src>) are
// detected for optional vendoring via --resolve-deps.
package island

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/tailscale/hujson"
	"golang.org/x/net/html"
)

// File is one parsed .island.html file.
type File struct {
	// Path is the source file path as given to Parse.
	Path string
	// Name is the PascalCase island name derived from the filename.
	Name string
	// RenderFunc is the generated Go function name: Render<Name>.
	RenderFunc string
	// HTML is the source file verbatim, including placeholder data.
	HTML []byte
	// Schema is the data shape inferred from the placeholder literal.
	Schema *Schema
	// DataOpen and DataClose are the byte offsets of the data island
	// script's inner body within HTML. The generated code replaces the
	// whole body with json.Marshal(data) at serve time.
	DataOpen, DataClose int
	// Deps are the CDN lib imports found in the file, in document order.
	Deps []DepRef
}

// DepKind identifies the kind of a CDN lib import.
type DepKind string

const (
	DepCSS DepKind = "css"
	DepJS  DepKind = "js"
)

// DepRef is one occurrence of a CDN lib import.
type DepRef struct {
	URL  string
	Kind DepKind
	// TagStart and TagEnd delimit the whole tag within HTML: for CSS the
	// <link ...> tag, for JS the <script ...>...</script> block.
	TagStart, TagEnd int
	// ScriptOpenTag is a rebuilt <script ...> opening tag with the src
	// attribute dropped, used when inlining the dep content. Empty for CSS.
	ScriptOpenTag string
}

// Schema is a minimal description of a data shape sufficient for Go struct
// generation, inferred from the placeholder literal.
type Schema struct {
	Type       string
	Properties map[string]*Schema
	Items      *Schema
	// Comment is the trailing // or /* */ comment on the property, emitted
	// as a Go doc comment on the generated field.
	Comment string
}

// Parse scans src (the contents of an .island.html file) and returns a File
// describing the island.
func Parse(filePath string, src []byte) (*File, error) {
	name := deriveName(filePath)

	doc, err := scan(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	if !doc.foundData {
		return nil, fmt.Errorf("%s: data island not found (need <script id=\"island-data\" type=\"application/json\">)", filePath)
	}

	body := src[doc.dataOpen:doc.dataClose]
	v, err := hujson.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%s: island-data placeholder is not valid JWCC: %w", filePath, err)
	}
	obj, ok := v.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("%s: island-data placeholder must be a JSON object literal", filePath)
	}
	if len(obj.Members) == 0 {
		return nil, fmt.Errorf("%s: island-data placeholder object is empty", filePath)
	}
	schema, err := inferObject(obj)
	if err != nil {
		return nil, fmt.Errorf("%s: island-data: %w", filePath, err)
	}

	return &File{
		Path:       filePath,
		Name:       name,
		RenderFunc: "Render" + name,
		HTML:       src,
		Schema:     schema,
		DataOpen:   doc.dataOpen,
		DataClose:  doc.dataClose,
		Deps:       doc.deps,
	}, nil
}

// doc holds the results of one tokenizer pass over the HTML.
type doc struct {
	foundData           bool
	dataOpen, dataClose int
	deps                []DepRef
}

// pendingScript tracks a <script ...> start tag until its matching </script>.
// The tokenizer treats script content as raw text, so nothing inside can be
// mistaken for a tag.
type pendingScript struct {
	tagStart int    // start of the <script ...> tag
	openEnd  int    // just past the opening tag
	isData   bool   // id="island-data"
	srcURL   string // http(s) src attribute, if any
	openTag  string // rebuilt open tag without src (for dep inlining)
}

// scan tokenizes src once, locating the island-data script body bounds and
// all CDN dep imports. Byte offsets are recovered from the tokenizer's raw
// output.
func scan(src []byte) (*doc, error) {
	z := html.NewTokenizer(bytes.NewReader(src))
	d := &doc{}
	offset := 0
	var script *pendingScript

	for {
		tt := z.Next()
		raw := z.Raw()
		start := offset
		offset += len(raw)

		switch tt {
		case html.ErrorToken:
			if script != nil {
				return nil, fmt.Errorf("unclosed <script> tag")
			}
			return d, nil

		case html.StartTagToken, html.SelfClosingTagToken:
			tag, attrs := tagAttrs(z)
			switch tag {
			case "link":
				if attrs["rel"] == "stylesheet" && isCDNURL(attrs["href"]) {
					d.deps = append(d.deps, DepRef{
						URL: attrs["href"], Kind: DepCSS,
						TagStart: start, TagEnd: offset,
					})
				}
			case "script":
				if tt == html.SelfClosingTagToken {
					break
				}
				script = &pendingScript{tagStart: start, openEnd: offset}
				if attrs["id"] == "island-data" {
					if d.foundData {
						return nil, fmt.Errorf("multiple island-data scripts")
					}
					if attrs["type"] != "application/json" {
						return nil, fmt.Errorf("island-data script must have type=\"application/json\" (got %q); the body is a JWCC object literal, not executable JS", attrs["type"])
					}
					script.isData = true
				} else if isCDNURL(attrs["src"]) {
					script.srcURL = attrs["src"]
					script.openTag = rebuildTag("script", attrs, "src")
				}
			}

		case html.EndTagToken:
			name, _ := z.TagName()
			if string(name) != "script" || script == nil {
				continue
			}
			if script.isData {
				d.foundData = true
				d.dataOpen = script.openEnd
				d.dataClose = start
			} else if script.srcURL != "" {
				d.deps = append(d.deps, DepRef{
					URL: script.srcURL, Kind: DepJS,
					TagStart: script.tagStart, TagEnd: offset,
					ScriptOpenTag: script.openTag,
				})
			}
			script = nil
		}
	}
}

// tagAttrs reads the current tag's name and attributes from the tokenizer.
// Attribute keys are lowercased by the tokenizer.
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

// rebuildTag reconstructs an opening tag from parsed attributes, dropping the
// named attribute. Attribute order is not preserved from the source; values
// are double-quoted with '"' escaped as &quot;.
func rebuildTag(name string, attrs map[string]string, drop string) string {
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(name)
	for _, k := range sortedKeys(attrs) {
		if k == drop {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(k)
		if v := attrs[k]; v != "" {
			b.WriteString(`="`)
			b.WriteString(strings.ReplaceAll(v, `"`, "&quot;"))
			b.WriteByte('"')
		}
	}
	b.WriteByte('>')
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isCDNURL(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

// inferObject builds an object Schema from a hujson object, attaching each
// member's trailing comment (same line, after the value) as its Comment.
func inferObject(obj *hujson.Object) (*Schema, error) {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}}
	for i, m := range obj.Members {
		key := m.Name.Value.(hujson.Literal).String()
		sub, err := inferValue(m.Value.Value)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", key, err)
		}
		next := hujson.Extra(obj.AfterExtra)
		if i+1 < len(obj.Members) {
			next = obj.Members[i+1].Name.BeforeExtra
		}
		sub.Comment = trailingComment(m.Value.AfterExtra, next)
		s.Properties[key] = sub
	}
	return s, nil
}

func inferValue(v hujson.ValueTrimmed) (*Schema, error) {
	switch x := v.(type) {
	case hujson.Literal:
		switch x.Kind() {
		case '"':
			return &Schema{Type: "string"}, nil
		case 't', 'f':
			return &Schema{Type: "boolean"}, nil
		case '0':
			if bytes.ContainsAny(x, ".eE") {
				return &Schema{Type: "number"}, nil
			}
			return &Schema{Type: "integer"}, nil
		default:
			return nil, fmt.Errorf("cannot infer type from %q", string(x))
		}
	case *hujson.Object:
		return inferObject(x)
	case *hujson.Array:
		var items *Schema
		for _, el := range x.Elements {
			sub, err := inferValue(el.Value)
			if err != nil {
				return nil, err
			}
			items, err = mergeSchema(items, sub)
			if err != nil {
				return nil, err
			}
		}
		return &Schema{Type: "array", Items: items}, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

// mergeSchema combines schemas inferred from sibling array elements. Integer
// promotes to number when a float is present; otherwise mixed types are an
// error. Object properties merge property-by-property.
func mergeSchema(a, b *Schema) (*Schema, error) {
	if a == nil {
		return b, nil
	}
	if a.Type != b.Type {
		if (a.Type == "number" && b.Type == "integer") || (a.Type == "integer" && b.Type == "number") {
			return &Schema{Type: "number"}, nil
		}
		return nil, fmt.Errorf("mixed array element types %q and %q", a.Type, b.Type)
	}
	switch a.Type {
	case "object":
		merged := &Schema{Type: "object", Properties: map[string]*Schema{}}
		for k, v := range a.Properties {
			merged.Properties[k] = v
		}
		for k, v := range b.Properties {
			if prev, ok := merged.Properties[k]; ok {
				m, err := mergeSchema(prev, v)
				if err != nil {
					return nil, fmt.Errorf("property %q: %w", k, err)
				}
				m.Comment = prev.Comment
				merged.Properties[k] = m
			} else {
				merged.Properties[k] = v
			}
		}
		return merged, nil
	case "array":
		items, err := mergeSchema(a.Items, b.Items)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "array", Items: items}, nil
	default:
		return a, nil
	}
}

// trailingComment extracts a same-line trailing comment from the extras
// surrounding a member value: after (before the comma) then next (after the
// comma). Text past the first newline belongs to the next line and is
// ignored. Supports // and single-line /* */ comments.
func trailingComment(after, next hujson.Extra) string {
	for _, extra := range []hujson.Extra{after, next} {
		line := string(extra)
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, "//"); i >= 0 {
			return strings.TrimSpace(line[i+2:])
		}
		if i := strings.Index(line, "/*"); i >= 0 {
			if j := strings.Index(line[i+2:], "*/"); j >= 0 {
				return strings.TrimSpace(line[i+2 : i+2+j])
			}
		}
	}
	return ""
}

// deriveName turns a filename into a PascalCase island name:
// "profile.island.html" -> "Profile", "user_card.island.html" -> "UserCard",
// "user-card.island.html" -> "UserCard".
func deriveName(path string) string {
	base := path
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return toPascalCase(base)
}

// toPascalCase converts a snake_case or kebab-case identifier to PascalCase.
func toPascalCase(s string) string {
	var b strings.Builder
	startWord := true
	for _, r := range s {
		if r == '_' || r == '-' {
			startWord = true
			continue
		}
		if startWord {
			b.WriteRune(unicode.ToUpper(r))
			startWord = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
