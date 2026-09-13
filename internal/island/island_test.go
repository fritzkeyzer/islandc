package island

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wrap builds a minimal island file around the given data object literal.
func wrap(body string) []byte {
	return []byte(`<!DOCTYPE html><html><body>
<script id="island-data">const islandData = ` + body + `;</script>
</body></html>`)
}

func TestParse_profileFixture(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "profile.island.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := Parse("profile.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Name != "Profile" || f.RenderFunc != "RenderProfile" {
		t.Errorf("Name/RenderFunc = %q/%q", f.Name, f.RenderFunc)
	}
	if f.Schema == nil || f.Schema.Type != "object" {
		t.Fatalf("Schema not inferred: %+v", f.Schema)
	}
	if _, ok := f.Schema.Properties["name"]; !ok {
		t.Errorf("schema missing 'name' property")
	}
	if s := f.Schema.Properties["stats"]; s == nil || s.Items == nil {
		t.Errorf("stats schema missing items")
	}

	// Splice bounds are the object literal: the generated code splices
	// json.Marshal(data) between the '{' and matching '}', preserving the
	// assignment prefix/suffix verbatim.
	body := f.HTML[f.DataOpen:f.DataClose]
	if !bytes.Contains(body, []byte(`"name"`)) {
		t.Errorf("body must contain the placeholder JSON; got %q", body)
	}
	if body[0] != '{' || body[len(body)-1] != '}' {
		t.Errorf("literal bounds must delimit the object; got %q...%q", body[0], body[len(body)-1])
	}
	if !bytes.Contains(f.HTML[:f.DataOpen], []byte(`id="island-data"`)) {
		t.Errorf("DataOpen does not follow the island-data opening tag")
	}
	if !bytes.Contains(f.HTML[:f.DataOpen], []byte(`const islandData`)) {
		t.Errorf("DataOpen does not follow the assignment prefix")
	}
	if !bytes.Contains(f.HTML[f.DataClose:], []byte("</script>")) {
		t.Errorf("DataClose must precede the closing tag; got %q", f.HTML[f.DataClose:f.DataClose+12])
	}
}

func TestParse_infersTypesFromPlaceholder(t *testing.T) {
	f, err := Parse("x.island.html", wrap(`
{
	"a": "hi",
	"b": 42,
	"c": 3.14,
	"d": true,
	"e": [{ "x": 1, "y": "z" }]
}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := f.Schema.Properties
	for k, want := range map[string]string{"a": "string", "b": "integer", "c": "number", "d": "boolean", "e": "array"} {
		if p[k].Type != want {
			t.Errorf("%s: got %q, want %q", k, p[k].Type, want)
		}
	}
	items := p["e"].Items
	if items == nil || items.Type != "object" {
		t.Fatalf("e items: got %+v, want object", items)
	}
	if items.Properties["x"].Type != "integer" || items.Properties["y"].Type != "string" {
		t.Errorf("e[] fields: %+v", items.Properties)
	}
}

func TestParse_promotesIntToNumberAcrossArrayElements(t *testing.T) {
	f, err := Parse("x.island.html", wrap(`{"vals":[{"v":1},{"v":2.5}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.Schema.Properties["vals"].Items.Properties["v"].Type; got != "number" {
		t.Errorf("v: got %q, want number", got)
	}
}

func TestParse_emptyAndPopulatedSiblingArrays(t *testing.T) {
	f, err := Parse("x.island.html", wrap(`{"rows":[{"tags":["a"]},{"tags":[]}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tags := f.Schema.Properties["rows"].Items.Properties["tags"]
	if tags.Type != "array" || tags.Items == nil || tags.Items.Type != "string" {
		t.Errorf("tags: %+v", tags)
	}
}

func TestParse_mixedArrayTypesIsError(t *testing.T) {
	_, err := Parse("x.island.html", wrap(`{"vals":[1, "a"]}`))
	if err == nil || !strings.Contains(err.Error(), "mixed array element types") {
		t.Fatalf("expected mixed-type error, got %v", err)
	}
}

func TestParse_extractsComments(t *testing.T) {
	f, err := Parse("x.island.html", wrap(`
{
	"a": 1, // the first field
	"b": "x", /* the second field */
	"c": {
		"a": 2, // nested a
	},
	"d": true // last, no comma
}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := f.Schema.Properties
	for k, want := range map[string]string{
		"a": "the first field",
		"b": "the second field",
		"d": "last, no comma",
	} {
		if p[k].Comment != want {
			t.Errorf("%s comment = %q, want %q", k, p[k].Comment, want)
		}
	}
	// Nested duplicate key gets its own comment, not the outer one.
	if got := p["c"].Properties["a"].Comment; got != "nested a" {
		t.Errorf("c.a comment = %q, want %q", got, "nested a")
	}
}

func TestParse_allowsTrailingCommas(t *testing.T) {
	f, err := Parse("x.island.html", wrap(`{"a": 1, "b": [1, 2,],}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Schema.Properties["b"].Items.Type != "integer" {
		t.Errorf("b items: %+v", f.Schema.Properties["b"].Items)
	}
}

func TestParse_dataScriptRejectsTypeAttr(t *testing.T) {
	cases := []struct {
		name string
		html string
	}{
		{
			"old inert JSON form",
			`<!DOCTYPE html><html><body>
<div id="root"></div>
<script id="island-data" type="application/json">{"a":"hi"}</script>
</body></html>`,
		},
		{
			"module type",
			`<!DOCTYPE html><html><body>
<div id="root"></div>
<script id="island-data" type="module">const islandData = {"a":"hi"};</script>
</body></html>`,
		},
		{
			"redundant default type",
			`<!DOCTYPE html><html><body>
<div id="root"></div>
<script id="island-data" type="text/javascript">const islandData = {"a":"hi"};</script>
</body></html>`,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("x.island.html", []byte(c.html))
			if err == nil {
				t.Fatal("expected error for type attribute on data script, got nil")
			}
			if !strings.Contains(err.Error(), "must not have a type attribute") {
				t.Errorf("error should mention the type attribute; got %v", err)
			}
		})
	}
}

func TestParse_literalBoundsInAssignment(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script id="island-data">
	// leading comment with a stray { brace
	const islandData = {
		"a": "hi }", // trailing comment with a } brace
		"b": { "c": 1 },
	};
</script>
</body></html>`)
	f, err := Parse("x.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	lit := string(f.HTML[f.DataOpen:f.DataClose])
	if !strings.HasPrefix(lit, "{") || !strings.HasSuffix(lit, "}") {
		t.Errorf("literal bounds wrong: %q", lit)
	}
	if strings.Contains(lit, "const islandData") || strings.Contains(lit, ";") {
		t.Errorf("literal must exclude the assignment prefix/suffix: %q", lit)
	}
	if !strings.Contains(string(f.HTML[:f.DataOpen]), "const islandData =") {
		t.Errorf("prefix must precede DataOpen")
	}
	if !strings.Contains(string(f.HTML[f.DataClose:]), ";") {
		t.Errorf("suffix must follow DataClose")
	}
	if got := f.Schema.Properties["a"].Comment; got != "trailing comment with a } brace" {
		t.Errorf("a comment = %q", got)
	}
}

func TestParse_bareObjectBodyIsError(t *testing.T) {
	// A bare object without an assignment is a JS syntax error in the
	// browser (parsed as a block statement), so require the assignment.
	// The scanner only needs a '{' — a bare literal still parses — but an
	// empty/absent literal must error.
	src := []byte(`<!DOCTYPE html><html><body>
<script id="island-data">const islandData = ;</script>
</body></html>`)
	if _, err := Parse("x.island.html", src); err == nil {
		t.Fatal("expected error for missing object literal, got nil")
	}
}

func TestParse_unbalancedBracesIsError(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script id="island-data">const islandData = {"a": {"b": 1};</script>
</body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil || !strings.Contains(err.Error(), "unbalanced") {
		t.Fatalf("expected unbalanced-braces error, got %v", err)
	}
}

func TestParse_missingData(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body><div id="root"></div></body></html>`)
	if _, err := Parse("x.island.html", src); err == nil {
		t.Fatal("expected error for missing data island, got nil")
	}
}

func TestParse_emptyDataObject(t *testing.T) {
	if _, err := Parse("x.island.html", wrap(`{}`)); err == nil {
		t.Fatal("expected error for empty data object, got nil")
	}
}

func TestParse_invalidPlaceholder(t *testing.T) {
	if _, err := Parse("x.island.html", wrap(`{not json}`)); err == nil {
		t.Fatal("expected error for invalid placeholder, got nil")
	}
}

func TestParse_nonObjectPlaceholder(t *testing.T) {
	if _, err := Parse("x.island.html", wrap(`[1,2,3]`)); err == nil {
		t.Fatal("expected error for non-object placeholder, got nil")
	}
}

func TestParse_attributeQuotingVariants(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script  id = 'island-data' >const islandData = {"a":"hi"};</script>
</body></html>`)
	if _, err := Parse("x.island.html", src); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestParse_nameNormalization(t *testing.T) {
	for file, want := range map[string]string{
		"user_card.island.html": "UserCard",
		"user-card.island.html": "UserCard",
		"profile.island.html":   "Profile",
	} {
		f, err := Parse(file, wrap(`{"a":"hi"}`))
		if err != nil {
			t.Fatalf("Parse %s: %v", file, err)
		}
		if f.Name != want {
			t.Errorf("%s: Name = %q, want %q", file, f.Name, want)
		}
	}
}

func TestParse_findsCDNDeps(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<link rel="stylesheet" href="https://cdn.example.com/base.css" />
<script src="https://cdn.example.com/util.js" defer></script>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f, err := Parse("widget.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Deps) != 2 {
		t.Fatalf("got %d deps, want 2: %+v", len(f.Deps), f.Deps)
	}
	css, js := f.Deps[0], f.Deps[1]
	if css.Kind != DepCSS || css.URL != "https://cdn.example.com/base.css" {
		t.Errorf("css dep = %+v", css)
	}
	if js.Kind != DepJS || js.URL != "https://cdn.example.com/util.js" {
		t.Errorf("js dep = %+v", js)
	}
	if got := string(src[css.TagStart:css.TagEnd]); got != `<link rel="stylesheet" href="https://cdn.example.com/base.css" />` {
		t.Errorf("css tag = %q", got)
	}
	if got := string(src[js.TagStart:js.TagEnd]); got != `<script src="https://cdn.example.com/util.js" defer></script>` {
		t.Errorf("js tag = %q", got)
	}
	if js.ScriptOpenTag != "<script defer>" {
		t.Errorf("js open tag = %q, want %q", js.ScriptOpenTag, "<script defer>")
	}
}

func TestParse_ignoresNonCDNDeps(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<link rel="stylesheet" href="/abs/style.css" />
<link rel="stylesheet" href="./relative.css" />
<link rel="stylesheet" href="//cdn.example.com/proto.css" />
<script src="./local.js"></script>
<script src="data:text/javascript,alert(1)"></script>
<script data-src="https://cdn.example.com/not-a-dep.js"></script>
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f, err := Parse("x.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Local file deps (./relative.css, ./local.js) are always-on: they
	// become DepRefs with Local=true. /abs, //proto, and data: remain
	// ignored (not local, not CDN).
	if len(f.Deps) != 2 {
		t.Fatalf("got %d deps, want 2 (./relative.css and ./local.js): %+v", len(f.Deps), f.Deps)
	}
	css, js := f.Deps[0], f.Deps[1]
	if css.URL != "./relative.css" || css.Kind != DepCSS || !css.Local {
		t.Errorf("css dep = %+v, want Local CSS ./relative.css", css)
	}
	if js.URL != "./local.js" || js.Kind != DepJS || !js.Local {
		t.Errorf("js dep = %+v, want Local JS ./local.js", js)
	}
}

func TestParse_recordsDuplicateDepOccurrences(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<link rel="stylesheet" href="https://cdn.example.com/base.css" />
<link rel="stylesheet" href="https://cdn.example.com/base.css" />
<script id="island-data">const islandData = {"a":"hi"};</script>
</body></html>`)
	f, err := Parse("x.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Deps) != 2 || f.Deps[0].TagStart == f.Deps[1].TagStart {
		t.Fatalf("want 2 distinct occurrences, got %+v", f.Deps)
	}
}

func TestParse_ignoresURLsInsideScriptContent(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script id="island-data">const islandData = {"a":"hi"};</script>
<script type="module">
	const s = '<link rel="stylesheet" href="https://cdn.example.com/fake.css">';
	console.log(s);
</script>
</body></html>`)
	f, err := Parse("x.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Deps) != 0 {
		t.Errorf("got %d deps, want 0 (tags inside script raw text are not deps): %+v", len(f.Deps), f.Deps)
	}
}

func TestParse_externalDataRef(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script id="island-data" src="./profile.json"></script>
</body></html>`)
	f, err := Parse("profile.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Name != "Profile" || f.RenderFunc != "RenderProfile" {
		t.Errorf("Name/RenderFunc = %q/%q", f.Name, f.RenderFunc)
	}
	if f.DataSrc != "./profile.json" {
		t.Errorf("DataSrc = %q, want %q", f.DataSrc, "./profile.json")
	}
	if f.DataTypeName != "ProfileData" {
		t.Errorf("DataTypeName = %q, want %q", f.DataTypeName, "ProfileData")
	}
	if f.Schema != nil {
		t.Errorf("Schema before SetData = %+v, want nil", f.Schema)
	}
	if f.DataOpen != 0 || f.DataClose != 0 {
		t.Errorf("DataOpen/DataClose = %d/%d, want 0/0", f.DataOpen, f.DataClose)
	}
	tag := string(src[f.DataTagStart:f.DataTagEnd])
	if tag != `<script id="island-data" src="./profile.json"></script>` {
		t.Errorf("DataTag = %q", tag)
	}

	dataSrc := []byte(`{
		"name": "Ada",
		"age": 36 // user age
	}`)
	if err := f.SetData(dataSrc); err != nil {
		t.Fatalf("SetData: %v", err)
	}
	if f.Schema == nil || f.Schema.Type != "object" {
		t.Fatalf("Schema after SetData = %+v", f.Schema)
	}
	if f.Schema.Properties["name"].Type != "string" {
		t.Errorf("name type = %q, want string", f.Schema.Properties["name"].Type)
	}
	if f.Schema.Properties["age"].Type != "integer" || f.Schema.Properties["age"].Comment != "user age" {
		t.Errorf("age = %+v", f.Schema.Properties["age"])
	}
}

func TestParse_externalDataWithWhitespaceBody(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body>
<script id="island-data" src="./user_stats.json">

</script>
</body></html>`)
	f, err := Parse("card.island.html", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.DataSrc != "./user_stats.json" {
		t.Errorf("DataSrc = %q", f.DataSrc)
	}
	if f.DataTypeName != "UserStatsData" {
		t.Errorf("DataTypeName = %q, want UserStatsData", f.DataTypeName)
	}
	tag := string(src[f.DataTagStart:f.DataTagEnd])
	if !strings.HasPrefix(tag, `<script id="island-data" src="./user_stats.json">`) || !strings.HasSuffix(tag, "</script>") {
		t.Errorf("tag = %q", tag)
	}
}

func TestParse_externalDataInvalidSrc(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"empty", `src=""`},
		{"bare-src", `src`},
		{"https", `src="https://example.com/data.json"`},
		{"http", `src="http://example.com/data.json"`},
		{"abs", `src="/data.json"`},
		{"proto-relative", `src="//example.com/data.json"`},
		{"data-uri", `src="data:application/json,{}"`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			html := `<!DOCTYPE html><html><body><script id="island-data" ` + c.src + `></script></body></html>`
			_, err := Parse("x.island.html", []byte(html))
			if err == nil {
				t.Fatalf("expected error for %s, got nil", c.name)
			}
			if !strings.Contains(err.Error(), "must be a local relative path") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestParse_externalDataNonEmptyBodyIsError(t *testing.T) {
	bodies := []string{
		`const islandData = {};`,
		`{}`,
		`123`,
		`// comment`,
		`/* comment */`,
		`hello`,
	}
	for _, body := range bodies {
		html := `<!DOCTYPE html><html><body><script id="island-data" src="./x.json">` + body + `</script></body></html>`
		_, err := Parse("x.island.html", []byte(html))
		if err == nil {
			t.Fatalf("expected error for non-empty body %q, got nil", body)
		}
		if !strings.Contains(err.Error(), "empty body") {
			t.Errorf("unexpected error for %q: %v", body, err)
		}
	}
}

func TestParse_externalDataTypeAttributeIsError(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><body><script id="island-data" src="./x.json" type="application/json"></script></body></html>`)
	_, err := Parse("x.island.html", src)
	if err == nil {
		t.Fatal("expected error for type attribute on external island-data, got nil")
	}
	if !strings.Contains(err.Error(), "must not have a type attribute") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSetData_errors(t *testing.T) {
	f := &File{Path: "x.island.html"}
	cases := []struct {
		name string
		json string
	}{
		{"invalid-jwcc", `{not json}`},
		{"non-object-array", `[1, 2, 3]`},
		{"non-object-string", `"hello"`},
		{"non-object-int", `42`},
		{"empty-object", `{}`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			err := f.SetData([]byte(c.json))
			if err == nil {
				t.Fatalf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestParse_inlineDataTypeName(t *testing.T) {
	f, err := Parse("user_card.island.html", wrap(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.DataTypeName != "UserCardData" {
		t.Errorf("DataTypeName = %q, want %q", f.DataTypeName, "UserCardData")
	}
	if f.DataSrc != "" {
		t.Errorf("DataSrc = %q, want empty", f.DataSrc)
	}
	if f.DataTagStart != 0 || f.DataTagEnd != 0 {
		t.Errorf("DataTagStart/End = %d/%d, want 0/0", f.DataTagStart, f.DataTagEnd)
	}
}
