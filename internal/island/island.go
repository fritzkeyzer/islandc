// Package island parses islandc-flavored .html files.
//
// An island file is plain HTML with four recognized conventions:
//
//   - A schema block:  <script type="application/schema+json" id="island-schema"> ... </script>
//   - A data island:   <script type="application/json" id="island-data"> ... </script>
//     (holds placeholder JSON in source; Go overwrites it at serve time)
//   - A render script: <script type="module" data-island-render> ... </script>
//
// Plus a root mount element with id="island-root" holding placeholder DOM.
//
// The island name is inferred from the filename and normalized to PascalCase
// (e.g. profile.island.html -> "Profile", user_card.island.html -> "UserCard"),
// producing idiomatic Go identifiers: RenderUserCard, UserCardData.
//
// The parser locates these by byte scan, not DOM parse, so it never
// interprets markup. It returns a File describing the island plus the
// raw HTML bytes (verbatim, including placeholder JSON) for embedding.
package island

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// File is a parsed islandc-flavored HTML file.
type File struct {
	// Path is the source file path relative to the target dir.
	Path string
	// Name is the island name in PascalCase, derived from the filename.
	// Used in generated identifiers: ProfileData, RenderProfile, profileHTML;
	// user_card.island.html -> UserCard -> RenderUserCard, UserCardData,
	// userCardHTML.
	Name string
	// RenderFunc is the generated Go function name. Defaults to Render<Name>.
	RenderFunc string
	// HTML is the source file verbatim, including placeholder JSON.
	// Embedded via //go:embed in the generated Go file; the source
	// .island.html file must remain alongside the generated file.
	HTML []byte
	// Schema is the parsed JSON Schema describing the data shape.
	Schema *Schema
	// DataOpen is the byte offset where the island-data slot's opening
	// tag ends inside HTML. The injector splices the marshaled blob here.
	DataOpen int
	// DataClose is the byte offset of the island-data slot's closing
	// </script> tag inside HTML.
	DataClose int
}

// Schema is a minimal JSON Schema subset sufficient for Go struct generation.
//
// Supported types: string, number, integer, boolean, array, object.
// "properties" defines nested fields. "items" defines array element shape.
type Schema struct {
	Type       string             `json:"type"`
	Properties map[string]*Schema `json:"properties,omitempty"`
	Items      *Schema            `json:"items,omitempty"`
	// Tag overrides the json tag for this property. Optional.
	Tag string `json:"tag,omitempty"`
}

// Parse parses a single islandc-flavored HTML file.
//
// It validates that all required conventions are present and that the
// placeholder JSON in the island-data slot is shape-compatible with the schema
// (best-effort: it unmarshals the placeholder into interface{} and checks the
// top-level keys against schema properties).
//
// The island name is inferred from the filename and normalized to PascalCase:
// profile.island.html -> "Profile", user_card.island.html -> "UserCard".
func Parse(path string, src []byte) (*File, error) {
	name := deriveName(path)
	renderFunc := "Render" + name

	schemaStart, schemaEnd, ok := locateScript(src, `type="application/schema+json"`, "id=\"island-schema\"")
	if !ok {
		return nil, fmt.Errorf("%s: schema block not found (need <script type=\"application/schema+json\" id=\"island-schema\">)", path)
	}
	var schema Schema
	if err := json.Unmarshal(src[schemaStart:schemaEnd], &schema); err != nil {
		return nil, fmt.Errorf("%s: schema block is not valid JSON: %w", path, err)
	}
	if schema.Type != "object" {
		return nil, fmt.Errorf("%s: schema root type must be \"object\", got %q", path, schema.Type)
	}
	if len(schema.Properties) == 0 {
		return nil, fmt.Errorf("%s: schema has no properties", path)
	}

	dataStart, dataEnd, ok := locateScript(src, `type="application/json"`, "id=\"island-data\"")
	if !ok {
		return nil, fmt.Errorf("%s: data island not found (need <script type=\"application/json\" id=\"island-data\">)", path)
	}
	placeholder := bytes.TrimSpace(src[dataStart:dataEnd])
	if len(placeholder) == 0 {
		return nil, fmt.Errorf("%s: island-data slot is empty; it must hold placeholder JSON in source", path)
	}
	var placeholderAny interface{}
	if err := json.Unmarshal(placeholder, &placeholderAny); err != nil {
		return nil, fmt.Errorf("%s: island-data placeholder is not valid JSON: %w", path, err)
	}
	if err := checkShape(placeholderAny, &schema, ""); err != nil {
		return nil, fmt.Errorf("%s: placeholder/data shape mismatch: %w", path, err)
	}

	// The render script must be present. We do not parse or execute it; we
	// only require its existence so the generated file is meaningful.
	if !hasRenderScript(src) {
		return nil, fmt.Errorf("%s: render script not found (need <script type=\"module\" data-island-render>)", path)
	}

	// The root mount must exist.
	if !bytes.Contains(src, []byte(`id="island-root"`)) && !bytes.Contains(src, []byte(`id='island-root'`)) {
		return nil, fmt.Errorf("%s: root mount not found (need an element with id=\"island-root\")", path)
	}

	// Compute splice offsets relative to the full HTML bytes. locateScript
	// returned offsets relative to the inner content; we need the absolute
	// byte offsets of the opening tag end and the closing tag start so the
	// injector can splice:  html[:openEnd] + blob + html[closeStart:]
	openTagEnd, closeTagStart, ok := locateScriptBounds(src, `type="application/json"`, "id=\"island-data\"")
	if !ok {
		return nil, fmt.Errorf("%s: island-data: internal error locating tag bounds", path)
	}

	return &File{
		Path:       path,
		Name:       name,
		RenderFunc: renderFunc,
		HTML:       src,
		Schema:     &schema,
		DataOpen:   openTagEnd,
		DataClose:  closeTagStart,
	}, nil
}

// locateScript finds a <script ...>...</script> block whose opening tag
// contains all of the given needle substrings (in any order). Returns the
// byte offsets of the inner content (exclusive of the tags) relative to src.
func locateScript(src []byte, needles ...string) (int, int, bool) {
	openEnd, closeStart, ok := locateScriptBounds(src, needles...)
	if !ok {
		return 0, 0, false
	}
	return openEnd, closeStart, true
}

// locateScriptBounds returns the absolute byte offset just past the opening
// <script ...> tag (openEnd) and the absolute byte offset of the closing
// </script> tag (closeStart). The inner content is src[openEnd:closeStart].
func locateScriptBounds(src []byte, needles ...string) (int, int, bool) {
	i := 0
	for {
		s := bytes.Index(src[i:], []byte("<script"))
		if s < 0 {
			return 0, 0, false
		}
		s += i
		// find end of opening tag
		gt := bytes.IndexByte(src[s:], '>')
		if gt < 0 {
			return 0, 0, false
		}
		openTag := src[s : s+gt] // without the '>'
		openEnd := s + gt + 1
		// check needles
		match := true
		for _, n := range needles {
			if !bytes.Contains(openTag, []byte(n)) {
				match = false
				break
			}
		}
		i = openEnd
		if !match {
			continue
		}
		// find closing </script>
		c := bytes.Index(src[openEnd:], []byte("</script>"))
		if c < 0 {
			return 0, 0, false
		}
		closeStart := openEnd + c
		return openEnd, closeStart, true
	}
}

func hasRenderScript(src []byte) bool {
	_, _, ok := locateScript(src, `type="module"`, "data-island-render")
	return ok
}

// checkShape is a best-effort, recursive check that a placeholder value is
// shape-compatible with a schema. It verifies object keys, array nesting,
// and scalar types. The placeholder is sample data; the authoritative type
// safety comes from the generated Go struct, but this check catches obvious
// drift between the schema and the placeholder JSON early.
func checkShape(value interface{}, schema *Schema, path string) error {
	if schema == nil {
		return nil
	}
	switch schema.Type {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected string, got %T", path, value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s: expected number, got %T", path, value)
		}
	case "integer":
		f, ok := value.(float64)
		if !ok {
			return fmt.Errorf("%s: expected integer, got %T", path, value)
		}
		if f != float64(int64(f)) {
			return fmt.Errorf("%s: expected integer, got non-integer number %v", path, f)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected boolean, got %T", path, value)
		}
	case "object":
		m, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s: expected object, got %T", path, value)
		}
		for k, sub := range schema.Properties {
			v, present := m[k]
			if !present {
				// missing keys are allowed; the renderer handles absent data
				continue
			}
			if err := checkShape(v, sub, path+"."+k); err != nil {
				return err
			}
		}
	case "array":
		arr, ok := value.([]interface{})
		if !ok {
			return fmt.Errorf("%s: expected array, got %T", path, value)
		}
		if schema.Items != nil {
			for idx, v := range arr {
				if err := checkShape(v, schema.Items, fmt.Sprintf("%s[%d]", path, idx)); err != nil {
					return err
				}
			}
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, schema.Type)
	}
	return nil
}

// deriveName turns a filename into a PascalCase island name:
// "profile.island.html" -> "Profile", "user_card.island.html" -> "UserCard",
// "user-card.island.html" -> "UserCard". The result is suitable for Go
// exported identifiers (RenderUserCard, UserCardData).
func deriveName(path string) string {
	base := path
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	// strip the .island.html suffix
	if strings.HasSuffix(base, ".island.html") {
		base = strings.TrimSuffix(base, ".island.html")
	} else if strings.HasSuffix(base, ".html") {
		base = strings.TrimSuffix(base, ".html")
	}
	// take the first dot-separated segment as the name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return toPascalCase(base)
}

// toPascalCase converts a snake_case or kebab-case identifier to PascalCase.
// Each segment separated by '_' or '-' has its first rune uppercased; the
// rest of the segment is preserved. Already-PascalCase input is unchanged,
// so "UserCard" stays "UserCard" and "profile" becomes "Profile".
func toPascalCase(s string) string {
	var b strings.Builder
	startWord := true
	for _, r := range s {
		if r == '_' || r == '-' {
			startWord = true
			continue
		}
		if startWord {
			b.WriteRune([]rune(strings.ToUpper(string(r)))[0])
			startWord = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
