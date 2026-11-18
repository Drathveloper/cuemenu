package model_test

import (
	"cmp"
	"encoding/json"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/Drathveloper/cuemenu/model"
)

// Conformance matrix: one row per construct of the CUE specification
// (https://cuelang.org/docs/reference/spec/). Each row checks the widget a
// field gets and, optionally, that an edit is accepted or rejected and
// what the field evaluates to afterwards.
type row struct {
	name     string
	src      string
	path     string       // field inspected
	widget   model.Widget // "" = must not appear in the menu
	setPath  string       // default: path
	set      string       // CUE expression to set; "" = no edit
	wantPath string       // default: path
	want     string       // JSON of wantPath after the edit; "" = not checked
	reject   bool         // the edit must be rejected
}

//nolint:goconst // a table: each row reads better with its CUE inline
var matrix = []row{
	// --- Basic types and literals ---
	{name: "null", src: `a: null`, path: "a", widget: model.ReadOnly, want: `null`},
	{name: "bool", src: `a: bool`, path: "a", widget: model.Toggle, set: `true`, want: `true`},
	{name: "bool with default", src: `a: *true | bool`, path: "a", widget: model.Toggle, want: `true`},
	{name: "int", src: `a: int`, path: "a", widget: model.Number, set: `42`, want: `42`},
	{name: "int rejects float", src: `a: int`, path: "a", widget: model.Number, set: `1.5`, reject: true},
	{name: "int hexadecimal", src: `a: int`, path: "a", widget: model.Number, set: `0x10`, want: `16`},
	{name: "int with _", src: `a: int`, path: "a", widget: model.Number, set: `1_000`, want: `1000`},
	{name: "int multiplier", src: `a: int`, path: "a", widget: model.Number, set: `1Ki`, want: `1024`},
	{name: "float", src: `a: float`, path: "a", widget: model.Number, set: `2.5`, want: `2.5`},
	{name: "float exponent", src: `a: float`, path: "a", widget: model.Number, set: `1e3`, want: `1E+3`}, // CUE's JSON for 1e3
	{name: "number", src: `a: number`, path: "a", widget: model.Number, set: `3`, want: `3`},
	{name: "bounds", src: `a: int & >0 & <10`, path: "a", widget: model.Number, set: `10`, reject: true},
	{name: "not equal", src: `a: int & !=0`, path: "a", widget: model.Number, set: `0`, reject: true},
	{name: "string", src: `a: string`, path: "a", widget: model.Text, set: `"tab\there"`, want: `"tab\there"`},
	{name: "multiline string", src: "a: string | *\"\"\"\n\tl1\n\tl2\n\t\"\"\"", path: "a", widget: model.Text, want: `"l1\nl2"`},
	{name: "raw string", src: `a: #"c:\dir"#`, path: "a", widget: model.ReadOnly, want: `"c:\\dir"`},
	{name: "regexp =~", src: `a: =~"^[a-z]+$"`, path: "a", widget: model.Text, set: `"ABC"`, reject: true},
	{name: "regexp !~", src: `a: string & !~"admin"`, path: "a", widget: model.Text, set: `"admin1"`, reject: true},
	{name: "bytes", src: `a: bytes`, path: "a", widget: model.Expr, set: `'abc'`, want: `"YWJj"`},
	{name: "top _", src: `a: _`, path: "a", widget: model.Expr, set: `{x: 1}`, want: `{"x":1}`},
	{name: "mixed kinds", src: `a: int | string`, path: "a", widget: model.Expr, set: `"s"`, want: `"s"`},
	{name: "mixed kinds rejects", src: `a: int | string`, path: "a", widget: model.Expr, set: `true`, reject: true},

	// --- Disjunctions and defaults ---
	{name: "enum", src: `a: "x" | "y"`, path: "a", widget: model.Choice, set: `"y"`, want: `"y"`},
	{name: "enum with default", src: `a: *"x" | "y"`, path: "a", widget: model.Choice, want: `"x"`},
	{name: "nested enum", src: `a: *("x" | "y") | "z"`, path: "a", widget: model.Choice, set: `"z"`, want: `"z"`},
	{name: "numeric enum", src: `a: 1 | 2 | *3`, path: "a", widget: model.Choice, set: `2`, want: `2`},
	{name: "null or int", src: `a: *null | int`, path: "a", widget: model.Expr, set: `5`, want: `5`},
	{name: "null or list", src: `a: *null | [...string]`, path: "a", widget: model.List, want: `null`},
	{name: "null or list set", src: `a: *null | [...string]`, path: "a", widget: model.List, set: `["x"]`, want: `["x"]`},
	{name: "null or list rejects kind", src: `a: *null | [...string]`, path: "a", widget: model.List, set: `[1]`, reject: true},
	{name: "map with default", src: `a: *{x: "1"} | {[string]: string}`, path: "a", widget: model.Map, want: `{"x":"1"}`},
	{name: "map with default adds key", src: `a: *{x: "1"} | {[string]: string}`, path: "a", widget: model.Map, setPath: "a.y", set: `"2"`, wantPath: "a", want: `{"x":"1","y":"2"}`},
	{name: "map with default: entry editable", src: `a: *{x: "1"} | {[string]: string}`, path: "a.x", widget: model.Text, set: `"3"`, wantPath: "a", want: `{"x":"3"}`},
	{name: "map with default: CUE accepts through the open default", src: `a: *{x: "1"} | {[string]: string}`, path: "a", widget: model.Map, setPath: "a.y", set: `2`, wantPath: "a", want: `{"x":"1","y":2}`},
	{name: "default outside the map: variant", src: `a: *{kind: "x"} | close({[=~"^y"]: int})`, path: "a", widget: model.Variant},
	{name: "variant with discriminator", src: `a: *{kind: "x", x: int | *1} | {kind: "y", y: int | *2}`, path: "a", widget: model.Variant, setPath: "a.kind", set: `"y"`, want: `{"kind":"y","y":2}`},
	{name: "embedded variant", src: "#A: {kind: \"a\", x: int | *1}\n#B: {kind: \"b\", y: int | *2}\na: {#A | #B}", path: "a", widget: model.Variant},

	// --- Structs ---
	{name: "struct", src: `a: {b: int | *1}`, path: "a", widget: model.Menu, wantPath: "a.b", want: `1`},
	{name: "open struct rejects undeclared fields", src: `a: {b: int | *1}`, path: "a", widget: model.Menu, setPath: "a.typo", set: `1`, reject: true},
	{name: "struct with ... accepts fields", src: `a: {b: int | *1, ...}`, path: "a", widget: model.Menu, setPath: "a.extra", set: `"x"`, want: `{"b":1,"extra":"x"}`},
	{name: "definition with ...", src: "#D: {b: int | *1, ...}\na: #D", path: "a", widget: model.Menu, setPath: "a.extra", set: `"x"`, want: `{"b":1,"extra":"x"}`},
	{name: "embedding with ...", src: "#B: {b: int | *1}\na: {#B, ...}", path: "a", widget: model.Menu, setPath: "a.extra", set: `2`, want: `{"b":1,"extra":2}`},
	{name: "_ in depth", src: `a: _`, path: "a", widget: model.Expr, setPath: "a.x.y", set: `1`, want: `{"x":{"y":1}}`},
	{name: "closed definition", src: "#D: {b: int | *1}\na: #D", path: "a", widget: model.Menu, setPath: "a.typo", set: `1`, reject: true},
	{name: "close()", src: `a: close({b: int | *1})`, path: "a.b", widget: model.Number, setPath: "a.typo", set: `1`, reject: true},
	{name: "embedding", src: "#Base: {x: int | *1}\na: {#Base, y: string | *\"s\"}", path: "a.x", widget: model.Number, wantPath: "a", want: `{"x":1,"y":"s"}`},
	{name: "empty struct", src: `a: {}`, path: "a", widget: model.Menu, want: `{}`},
	{name: "optional", src: `a?: int`, path: "a", widget: model.Number, set: `3`, want: `3`},
	{name: "required", src: `a!: int`, path: "a", widget: model.Number, set: `3`, want: `3`},

	{name: "closed out by another definition", src: "#A: {x?: int, y?: int}\n#B: {x?: int}\na: #A & #B", path: "a.y", widget: ""},
	{name: "kept by both definitions", src: "#A: {x?: int, y?: int}\n#B: {x?: int}\na: #A & #B", path: "a.x", widget: model.Number, set: `1`, want: `1`},
	{name: "split declarations", src: "a: {x: int | *1}\na: {y: int | *2}", path: "a.y", widget: model.Number, wantPath: "a", want: `{"x":1,"y":2}`},
	{name: "optional with default", src: `a?: int | *1`, path: "a", widget: model.Number, set: `4`, want: `4`},

	// --- Identifiers and labels ---
	{name: "hidden definition", src: "#D: int\na: #D", path: "#D", widget: ""},
	{name: "hidden field", src: "_h: 1\na: _h + 1", path: "_h", widget: ""},
	{name: "hidden field used", src: "_h: 1\na: _h + 1", path: "a", widget: model.ReadOnly, want: `2`},
	{name: "let", src: "let x = 5\na: x", path: "a", widget: model.ReadOnly, want: `5`},
	{name: "quoted label", src: `"my-field": int | *1`, path: `"my-field"`, widget: model.Number, set: `2`, want: `2`},
	{name: "unicode label", src: `π: number | *3.14`, path: "π", widget: model.Number, set: `3.1416`, want: `3.1416`},
	{name: "field alias", src: "X=\"weird-name\": int | *1\nb: X + 1", path: "b", widget: model.ReadOnly, setPath: `"weird-name"`, set: `5`, wantPath: "b", want: `6`},
	{name: "dynamic field", src: "k: \"name\"\nobj: {(k): string | *\"x\"}", path: "obj.name", widget: model.Text, set: `"y"`, want: `"y"`},

	// --- References, expressions, comprehensions ---
	{name: "reference", src: "a: int | *1\nb: a", path: "b", widget: model.Number, setPath: "a", set: `5`, wantPath: "b", want: `5`},
	{name: "arithmetic", src: "a: int | *2\nb: a * 2", path: "b", widget: model.ReadOnly, setPath: "a", set: `3`, wantPath: "b", want: `6`},
	{name: "interpolation", src: "h: string | *\"x\"\nu: \"http://\\(h)/\"", path: "u", widget: model.ReadOnly, setPath: "h", set: `"y"`, wantPath: "u", want: `"http://y/"`},
	{name: "if", src: "on: bool | *false\nif on {extra: int | *1}", path: "extra", widget: "", setPath: "on", set: `true`, wantPath: "extra", want: `1`},
	{name: "if on a required field", src: "a: {r!: string, ref: string, if r != \"\" {ref: r}}", path: "a", widget: model.Menu, setPath: "a.r", set: `"x"`, wantPath: "a", want: `{"r":"x","ref":"x"}`},
	{name: "computed, input not set", src: "n!: int\nsum: n + 1", path: "sum", widget: model.ReadOnly},
	{name: "computed interpolation, input not set", src: "r!: string\nup: \"x\\(r)\"", path: "up", widget: model.ReadOnly},
	{name: "computed reference, input not set", src: "name!: string\nlabel: name", path: "label", widget: model.ReadOnly, setPath: "name", set: `"web"`, wantPath: "label", want: `"web"`},
	{name: "computed call, input not set", src: "import \"strings\"\nname!: string\nup: strings.ToUpper(name)", path: "up", widget: model.ReadOnly},
	{name: "type reference stays editable", src: "Mode: string\na: Mode", path: "a", widget: model.Text, set: `"x"`, want: `"x"`},
	{name: "for", src: "src: [...string] | *[\"a\"]\nout: {for x in src {(x): {enabled: bool | *false}}}", path: "out.a.enabled", widget: model.Toggle, setPath: "src", set: `["a", "b"]`, wantPath: "out.b", want: `{"enabled":false}`},

	// --- Lists ---
	{name: "open list", src: `a: [...int]`, path: "a", widget: model.List, set: `[1, 2]`, want: `[1,2]`},
	{name: "open list rejects kind", src: `a: [...int]`, path: "a", widget: model.List, set: `["x"]`, reject: true},
	{name: "closed list", src: `a: [int, string]`, path: "a", widget: model.List, set: `[1, "x"]`, want: `[1,"x"]`},
	{name: "closed list rejects length", src: `a: [int, string]`, path: "a", widget: model.List, set: `[1]`, reject: true},
	{name: "list prefix and rest", src: `a: [string, ...int]`, path: "a", widget: model.List, set: `["x", 1, 2]`, want: `["x",1,2]`},
	{name: "list.MinItems", src: "import \"list\"\na: [...string] & list.MinItems(1)", path: "a", widget: model.List, set: `[]`, reject: true},
	{name: "list.UniqueItems", src: "import \"list\"\na: [...int] & list.UniqueItems()", path: "a", widget: model.List, set: `[1, 1]`, reject: true},

	// --- Pattern constraints ---
	{name: "string pattern", src: `a: [string]: int`, path: "a", widget: model.Map, setPath: "a.x", set: `1`, wantPath: "a", want: `{"x":1}`},
	{name: "regexp pattern", src: `a: [=~"^X-"]: string`, path: "a", widget: model.Map, setPath: `a."X-A"`, set: `"v"`, wantPath: "a", want: `{"X-A":"v"}`},
	{name: "regexp pattern rejects", src: `a: [=~"^X-"]: string`, path: "a", widget: model.Map, setPath: "a.foo", set: `"v"`, reject: true},
	{name: "negated pattern", src: `a: [!~"^_"]: int`, path: "a", widget: model.Map, setPath: `a."_x"`, set: `1`, reject: true},
	{name: "negated pattern accepts", src: `a: [!~"^_"]: int`, path: "a", widget: model.Map, setPath: "a.x", set: `1`, wantPath: "a", want: `{"x":1}`},
	{name: "hidden field not editable", src: "_h: int | *1\na: _h", path: "a", widget: model.Number, setPath: "_h", set: `2`, reject: true},
	{name: "pattern with alias", src: `users: [N=string]: {name: N, age: int | *0}`, path: "users", widget: model.Map, setPath: "users.ana.age", set: `30`, wantPath: "users.ana", want: `{"name":"ana","age":30}`},

	// --- Standard library validators ---
	{name: "strings.MaxRunes", src: "import \"strings\"\na: string & strings.MaxRunes(3)", path: "a", widget: model.Text, set: `"abcd"`, reject: true},
	{name: "net.IPv4 (string or bytes)", src: "import \"net\"\na: net.IPv4", path: "a", widget: model.Expr, set: `"999.1.1.1"`, reject: true},
	{name: "net.IPv4 & string", src: "import \"net\"\na: net.IPv4 & string", path: "a", widget: model.Text, set: `"10.0.0.1"`, want: `"10.0.0.1"`},
	{name: "stdlib function", src: "import \"strings\"\na: string | *\"x\"\nb: strings.ToUpper(a)", path: "b", widget: model.ReadOnly, setPath: "a", set: `"hola"`, wantPath: "b", want: `"HOLA"`},
	{name: "time.Duration", src: "import \"time\"\na: time.Duration", path: "a", widget: model.Text, set: `"5m"`, want: `"5m"`},
}

func TestConformance(t *testing.T) {
	for _, r := range matrix {
		t.Run(r.name, func(t *testing.T) {
			schema := cuecontext.New().CompileString(r.src)
			if err := schema.Err(); err != nil {
				t.Fatalf("schema: %v", err)
			}
			cfg := model.New(schema)
			r.checkWidget(t, cfg)
			r.checkEdit(t, cfg)
			r.checkWant(t, cfg)
		})
	}
}

func (r row) checkWidget(t *testing.T, cfg *model.Config) {
	t.Helper()
	node := tree(cfg)[cue.ParsePath(r.path).String()]
	switch {
	case r.widget == "" && node != nil:
		t.Errorf("%s should not be in the menu, got %s", r.path, node.Widget)
	case r.widget != "" && node == nil:
		t.Fatalf("%s missing from the menu", r.path)
	case node != nil && node.Widget != r.widget:
		t.Errorf("%s: widget %s, want %s", r.path, node.Widget, r.widget)
	}
}

func (r row) checkEdit(t *testing.T, cfg *model.Config) {
	t.Helper()
	if r.set == "" {
		return
	}
	path := cmp.Or(r.setPath, r.path)
	err := cfg.SetExpr(cue.ParsePath(path), r.set)
	if r.reject != (err != nil) {
		t.Fatalf("set %s = %s: reject=%v, err=%v", path, r.set, r.reject, err)
	}
}

func (r row) checkWant(t *testing.T, cfg *model.Config) {
	t.Helper()
	if r.want == "" {
		return
	}
	path := cmp.Or(r.wantPath, r.path)
	got, err := json.Marshal(cfg.Value().LookupPath(cue.ParsePath(path)))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if string(got) != r.want {
		t.Errorf("%s = %s, want %s", path, got, r.want)
	}
}

// The matrix doubles as documentation: every widget is covered.
func TestMatrixCoversAllWidgets(t *testing.T) {
	seen := map[model.Widget]bool{}
	for _, r := range matrix {
		seen[r.widget] = true
	}
	for _, w := range []model.Widget{
		model.ReadOnly, model.Toggle, model.Choice, model.Number, model.Text,
		model.Menu, model.List, model.Map, model.Variant, model.Expr,
	} {
		if !seen[w] {
			t.Errorf("no row for widget %s", w)
		}
	}
}
