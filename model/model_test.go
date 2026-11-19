package model_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/Drathveloper/cuemenu/model"
)

// Paths and values used in several tests.
const (
	pathDomain    = "server.domain"
	pathGzip      = "server.enable_gzip"
	pathOutputs   = "log.outputs"
	pathMaxSize   = "log.max_size_mb"
	cueTrue       = "true"
	cueX          = `"x"`
	variantMemory = "memory"
)

func flatten(nodes []*model.Node, out map[string]*model.Node) map[string]*model.Node {
	for _, n := range nodes {
		if _, dup := out[n.Path.String()]; !dup { // a variant's chooser shares its path
			out[n.Path.String()] = n
		}
		flatten(n.Children, out)
	}
	return out
}

func webapp(t *testing.T) *model.Config {
	t.Helper()
	v, err := model.Load("../examples/webapp")
	if err != nil {
		t.Fatal(err)
	}
	return model.New(v)
}

func tree(c *model.Config) map[string]*model.Node { return flatten(c.Tree(), map[string]*model.Node{}) }

func set(t *testing.T, c *model.Config, path string, x any) {
	t.Helper()
	if err := c.Set(cue.ParsePath(path), x); err != nil {
		t.Fatalf("set %s=%v: %v", path, x, err)
	}
}

// Seed of the conformance matrix: one CUE construct per row.
func TestWidgets(t *testing.T) {
	got := tree(webapp(t))
	want := map[string]model.Widget{
		"app_name":                 model.Text,     // string | *"x"
		"run_mode":                 model.Choice,   // *"a" | "b"
		"schema_version":           model.ReadOnly, // fixed value
		"server":                   model.Menu,     // struct
		"server.http_port":         model.Number,   // #Port (bounds via definition) | *3000
		pathDomain:                 model.Text,     // stdlib validator
		"server.root_url":          model.ReadOnly, // interpolation, computed
		pathGzip:                   model.Toggle,   // bool | *false
		"database":                 model.Variant,  // *#A | #B | #C
		"database.type":            model.Choice,   // discriminator: chooses the variant
		"auth.session_lifetime":    model.Text,     // time.Duration
		"auth.min_password_length": model.Number,   // int & >=8 | *12
		"auth.oauth2":              model.Map,      // [Name=string]: {...}
		"auth.admins":              model.List,     // [...string]
		pathOutputs:                model.List,     // [...("a"|"b")] | *["a"]
		"log.file_path":            model.Text,     // optional field
		pathMaxSize:                model.Number,   // int | null: edited as int
		"log.filters":              model.List,     // [...{struct}]
		"server.headers":           model.Map,      // [=~"^X-"]: string (regexp keys)
		"cache":                    model.Variant,  // *#A | #B without discriminator
	}
	for p, w := range want {
		if n := got[p]; n == nil || n.Widget != w {
			t.Errorf("%s: got %+v, want %s", p, n, w)
		}
	}
	if n := got["database"]; n.Value != "sqlite3" || len(n.Options) != 3 {
		t.Errorf("database variant: value %q options %v", n.Value, n.Options)
	}
	if !got["log.file_path"].Optional {
		t.Error("log.file_path should be optional")
	}
}

// An edited field keeps its widget: classification ignores its own edit.
func TestEditedFieldKeepsWidget(t *testing.T) {
	c := webapp(t)
	set(t, c, pathGzip, true)
	set(t, c, "server.protocol", "https")
	set(t, c, "database.type", "postgres")
	got := tree(c)
	for p, w := range map[string]model.Widget{pathGzip: model.Toggle, "server.protocol": model.Choice, "database": model.Variant} {
		if n := got[p]; n.Widget != w {
			t.Errorf("%s: widget %s, want %s", p, n.Widget, w)
		}
	}
	if n := got[pathGzip]; n.Value != cueTrue || !n.Set {
		t.Errorf("enable_gzip: %+v", n)
	}
	if n := got["database"]; n.Value != "postgres" {
		t.Errorf("database: value %q", n.Value)
	}
	if n := got["database.type"]; n.Widget != model.Choice || n.Value != `"postgres"` || len(n.Options) != 3 {
		t.Errorf("edited discriminator: %+v", n)
	}
	if n := got["server.root_url"]; n.Value != `"https://localhost/"` {
		t.Errorf("root_url not recomputed: %q", n.Value)
	}
}

// Dependencies come from re-unification; edits of fields that disappear
// are dropped.
func TestDependencies(t *testing.T) {
	c := webapp(t)
	set(t, c, "server.protocol", "https")
	if n := tree(c)["server.cert_file"]; n == nil || !n.Required {
		t.Fatalf("https: cert_file missing or not required: %+v", n)
	}
	set(t, c, "server.cert_file", "/etc/cert.pem")
	if n := tree(c)["server.cert_file"]; n.Widget != model.Text || !n.Required || n.Value != `"/etc/cert.pem"` {
		t.Errorf("edited required field: %+v", n)
	}
	// Fields from a definition imported from another package of the module.
	if n := tree(c)["server.tls.min_version"]; n == nil || n.Widget != model.Choice || n.Doc != "Minimum TLS version." {
		t.Errorf("imported definition: %+v", n)
	}
	set(t, c, "server.tls.min_version", "1.3")
	set(t, c, "server.protocol", "unix")
	got := tree(c)
	if got["server.cert_file"] != nil || got["server.http_port"] != nil || got["server.socket_path"] == nil {
		t.Error("unix: expected only socket_path")
	}
	if slices.Contains(c.EditKeys(), "server.cert_file") {
		t.Error("cert_file edit should be pruned")
	}

	set(t, c, "database.type", "postgres")
	set(t, c, "database.user", "app")
	if n := tree(c)["database.password"]; n == nil || !n.Secret {
		t.Errorf("postgres: password should be secret: %+v", n)
	}
	set(t, c, "database.type", "sqlite3")
	if slices.Contains(c.EditKeys(), "database.user") {
		t.Error("database.user should be pruned with the variant")
	}

	set(t, c, "log.file_path", "/var/log/app.log") // optional fields survive pruning
	if tree(c)["log.file_path"].Value != `"/var/log/app.log"` {
		t.Error("optional field edit lost")
	}
}

func TestInvalidEditRejected(t *testing.T) {
	c := webapp(t)
	for path, x := range map[string]any{
		"server.http_port":         70000,   // out of bounds
		"server.protocol":          "ftp",   // not an option
		pathDomain:                 "ab",    // strings.MinRunes(3)
		"auth.min_password_length": 4,       // >=8
		"auth.session_lifetime":    "mucho", // time.Duration
		pathGzip:                   "yes",   // wrong kind
		"server.cert_file":         "/c",    // only exists with https
		"server.no_such_field":     "x",     // not declared (open struct)
	} {
		if err := c.Set(cue.ParsePath(path), x); err == nil {
			t.Errorf("%s=%v accepted", path, x)
		}
	}
	if keys := c.EditKeys(); len(keys) != 0 {
		t.Errorf("rejected edits were kept: %v", keys)
	}
	if err := c.Set(cue.ParsePath("server.http_port"), 70000); err == nil || !strings.Contains(err.Error(), "out of bound <=65535") {
		t.Errorf("rejection should explain the cause, got: %v", err)
	}
}

func TestSaveLoadExport(t *testing.T) {
	c := webapp(t)
	set(t, c, "server.protocol", "https")
	if _, err := c.Export("yaml"); err == nil {
		t.Error("export should fail: cert_file and key_file are required")
	}
	set(t, c, "server.cert_file", "/c.pem")
	set(t, c, "server.key_file", "/k.pem")
	set(t, c, pathMaxSize, 100)
	set(t, c, "auth.admins", []string{"alberto"})

	file := t.TempDir() + "/.config.cue"
	if err := c.Save(file); err != nil {
		t.Fatal(err)
	}
	// Saving over a file readable by others leaves it 0600, with no temp
	// files left behind.
	if err := os.Chmod(file, 0o644); err != nil { //nolint:gosec // on purpose: Save must restrict it
		t.Fatal(err)
	}
	if err := c.Save(file); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf(".config.cue should be 0600, is %v", st.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".cuemenu-*")); len(left) > 0 {
		t.Errorf("temp files left: %v", left)
	}
	r := webapp(t)
	if dropped, err := r.Load(file); err != nil || len(dropped) != 0 {
		t.Fatalf("load: %v, dropped %v", err, dropped)
	}
	a, err := c.Export("json")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Export("json")
	if string(a) != string(b) {
		t.Errorf("round trip differs:\n%s\n---\n%s", a, b)
	}
	y, err := r.Export("yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cert_file: /c.pem", "root_url: https://localhost/", "max_size_mb: 100", "- alberto"} {
		if !strings.Contains(string(y), want) {
			t.Errorf("yaml lacks %q:\n%s", want, y)
		}
	}
}

// Expressions typed by the user, including whole lists and map entries.
func TestSetExpr(t *testing.T) {
	c := webapp(t)
	for path, expr := range map[string]string{
		pathOutputs:     `["console", "file"]`,
		pathMaxSize:     `250`,
		"database.type": `"mysql"`,
		"auth.oauth2":   `{github: {client_id: "a", client_secret: "b"}}`,
	} {
		if err := c.SetExpr(cue.ParsePath(path), expr); err != nil {
			t.Fatalf("%s=%s: %v", path, expr, err)
		}
	}
	for path, expr := range map[string]string{
		pathOutputs: `["syslog"]`, // not an allowed element
		pathMaxSize: `"big"`,      // int | null
		pathDomain:  `"unterminated`,
	} {
		if err := c.SetExpr(cue.ParsePath(path), expr); err == nil {
			t.Errorf("%s=%s accepted", path, expr)
		}
	}
	got := tree(c)
	if n := got["auth.oauth2.github.client_secret"]; n == nil || !n.Secret {
		t.Errorf("map entry not shown or not secret: %+v", n)
	}
	if n := got["database.port"]; n.Value != "3306" {
		t.Errorf("mysql default port: %+v", n)
	}

	// Map entries survive a save/load cycle, even with a single field.
	file := t.TempDir() + "/.config.cue"
	if err := c.Save(file); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(file) //nolint:gosec // a file in t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	saved = append(saved, `auth: oauth2: gitlab: client_id: "x"`+"\n"...)
	if err := os.WriteFile(file, saved, 0o600); err != nil { //nolint:gosec // a file in t.TempDir()
		t.Fatal(err)
	}
	r := webapp(t)
	if dropped, err := r.Load(file); err != nil || len(dropped) != 0 {
		t.Fatalf("load: %v, dropped %v", err, dropped)
	}
	got = tree(r)
	if got["auth.oauth2.github.client_id"] == nil || got["auth.oauth2.gitlab.client_id"] == nil {
		t.Error("map entries lost on reload")
	}
	if got[pathOutputs].Value != `["console", "file"]` {
		t.Errorf("list lost on reload: %q", got[pathOutputs].Value)
	}
}

func TestLists(t *testing.T) {
	c := webapp(t)
	n := tree(c)[pathOutputs]
	if n.Elem == nil || n.Elem.Widget != model.Choice || len(n.Children) != 1 || n.Children[0].Value != `"console"` {
		t.Fatalf("outputs: elem %+v children %+v", n.Elem, n.Children)
	}
	if err := c.SetList(n.Path, []string{`"console"`, `"file"`}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetList(n.Path, []string{`"syslog"`}); err == nil {
		t.Error("element outside the enum accepted")
	}
	if got := tree(c)[pathOutputs]; len(got.Children) != 2 || got.Children[1].Path.String() != "log.outputs[1]" {
		t.Errorf("outputs after set: %+v", got.Children)
	}

	// List of structs: the element template is a struct (edited as CUE).
	f := tree(c)["log.filters"]
	if f.Elem == nil || f.Elem.Widget != model.Menu {
		t.Fatalf("filters elem: %+v", f.Elem)
	}
	if err := c.SetList(f.Path, []string{`{module: "db"}`}); err != nil {
		t.Fatal(err)
	}
	// Closed list with elements not filled in yet: "" is written as _.
	cl := model.New(cuecontext.New().CompileString(`a: [int, string]`))
	if err := cl.SetList(cue.ParsePath("a"), []string{"", cueX}); err != nil {
		t.Errorf("partially filled closed list: %v", err)
	}
	if err := c.SetList(f.Path, []string{`{level: "info"}`}); err != nil {
		t.Error("an incomplete element is accepted (it is reported on export, not on edit)")
	}
}

func TestMaps(t *testing.T) {
	c := webapp(t)
	h := cue.ParsePath("server.headers")
	if n := tree(c)["server.headers"]; n.Widget != model.Map {
		t.Fatalf("regexp map: %+v", n)
	}
	if _, err := c.Entry(h, "Foo"); err == nil {
		t.Error("key not matching ^X- accepted")
	}
	e, err := c.Entry(h, "X-Frame-Options")
	if err != nil || e.Widget != model.Text {
		t.Fatalf("entry: %+v %v", e, err)
	}
	set(t, c, e.Path.String(), "DENY")
	if _, err := c.Entry(h, "X-Frame-Options"); err == nil {
		t.Error("duplicate key accepted")
	}

	// Struct entries: created empty, filled, removed independently.
	o := cue.ParsePath("auth.oauth2")
	for _, k := range []string{"github", "gitlab"} {
		e, err := c.Entry(o, k)
		if err != nil || e.Widget != model.Menu {
			t.Fatalf("%s: %+v %v", k, e, err)
		}
		if err := c.Set(e.Path, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	set(t, c, "auth.oauth2.github.client_id", "abc")
	c.Unset(cue.ParsePath("auth.oauth2.gitlab"))
	got := tree(c)
	if got["auth.oauth2.gitlab"] != nil || got["auth.oauth2.github.client_id"] == nil {
		t.Error("unset of one entry should keep the other")
	}

	// Empty and regexp entries survive save/load.
	set(t, c, "auth.oauth2.bitbucket", map[string]any{})
	file := t.TempDir() + "/.config.cue"
	if err := c.Save(file); err != nil {
		t.Fatal(err)
	}
	r := webapp(t)
	if dropped, err := r.Load(file); err != nil || len(dropped) != 0 {
		t.Fatalf("load: %v dropped %v", err, dropped)
	}
	got = tree(r)
	for _, p := range []string{`server.headers."X-Frame-Options"`, "auth.oauth2.bitbucket", "auth.oauth2.github.client_id"} {
		if got[p] == nil {
			t.Errorf("%s lost on reload", p)
		}
	}
}

// A variant without a discriminator field is chosen by filling a field
// only it has; switching back drops that variant's edits.
func TestVariantWithoutDiscriminator(t *testing.T) {
	c := webapp(t)
	n := tree(c)["cache"]
	if n.Widget != model.Variant || n.Value != variantMemory || !n.Children[0].Chooser {
		t.Fatalf("cache: %+v", n)
	}
	set(t, c, "cache.memory.size_mb", 128)
	ask, err := c.Choose(n.Path, 1)
	if err != nil || ask == nil || ask.Path.String() != "cache.redis.addr" || !ask.Required {
		t.Fatalf("choose redis should ask for cache.redis.addr: %+v %v", ask, err)
	}
	if tree(c)["cache"].Value != variantMemory {
		t.Error("choosing must not change anything until the field is set")
	}
	set(t, c, "cache.redis.addr", "localhost:6379")
	got := tree(c)
	if got["cache"].Value != "redis" || got["cache.memory.size_mb"] != nil {
		t.Errorf("after setting addr: cache=%q memory still there=%v", got["cache"].Value, got["cache.memory.size_mb"] != nil)
	}
	if ask, err := c.Choose(n.Path, 0); err != nil || ask == nil {
		t.Fatalf("back to memory: %+v %v", ask, err)
	} else {
		set(t, c, ask.Path.String(), 32)
	}
	if got := tree(c); got["cache"].Value != variantMemory || got["cache.redis.addr"] != nil {
		t.Errorf("back to memory: %q", got["cache"].Value)
	}
	if _, err := c.Export("json"); err != nil {
		t.Errorf("export after switching: %v", err)
	}

	// The discriminated variant goes through the same entry point.
	if _, err := c.Choose(cue.ParsePath("database"), 2); err != nil || tree(c)["database"].Value != "mysql" {
		t.Errorf("Choose with discriminator: %v", err)
	}
}

func TestImportedReferences(t *testing.T) {
	v, err := model.Load("testdata/imports")
	if err != nil {
		t.Fatal(err)
	}
	c := model.New(v)
	got := tree(c)
	if n := got["mode"]; n == nil || n.Widget != model.Choice || len(n.Options) != 3 {
		t.Errorf("imported enum: %+v", n)
	}
	if n := got["legend.display"]; n == nil || n.Widget != model.Choice {
		t.Errorf("enum referenced inside the imported package: %+v", n)
	}
	if n := got["variant"]; n == nil || n.Widget != model.Variant {
		t.Fatalf("imported variant: %+v", n)
	}
	if _, err := c.Choose(cue.ParsePath("variant"), 1); err != nil || tree(c)["variant"].Value != "b" {
		t.Errorf("choose imported variant: %v", err)
	}
}

func compile(t *testing.T, src string) *model.Config {
	t.Helper()
	v := cuecontext.New().CompileString(src)
	if err := v.Err(); err != nil {
		t.Fatal(err)
	}
	return model.New(v)
}

func exportJSON(t *testing.T, c *model.Config) string {
	t.Helper()
	out, err := c.Export("json")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(out)), "")
}

// The top level of a package need not be a struct with fields: KubeVela's
// parameters are maps, variants or open structs.
func TestRootMap(t *testing.T) {
	c := compile(t, `[string]: string | null`)
	if root := c.Root(); root.Widget != model.Map || root.Path.String() != "" {
		t.Fatalf("root: %+v", root)
	}
	entry, err := c.Entry(cue.Path{}, "app")
	if err != nil || entry.Path.String() != "app" {
		t.Fatalf("entry: %+v %v", entry, err)
	}
	if err := c.SetExpr(entry.Path, `"web"`); err != nil {
		t.Fatal(err)
	}
	if got := exportJSON(t, c); got != `{"app":"web"}` {
		t.Errorf("export: %s", got)
	}
	file := t.TempDir() + "/.config.cue"
	if err := c.Save(file); err != nil {
		t.Fatal(err)
	}
	again := compile(t, `[string]: string | null`)
	if _, err := again.Load(file); err != nil || tree(again)["app"] == nil {
		t.Errorf("reload: %v %v", err, again.EditKeys())
	}
	c.Unset(cue.Path{}) // reset everything
	if len(c.EditKeys()) != 0 {
		t.Errorf("unset root: %v", c.EditKeys())
	}
}

func TestRootVariant(t *testing.T) {
	// Definitions next to the embedded disjunction: {#A, #B} & (#A | #B).
	c := compile(t, "#A: {kind: \"a\", x: int | *1}\n#B: {kind: \"b\", y: int | *2}\n#A | #B")
	if root := c.Root(); root.Widget != model.Variant || len(root.Options) != 2 {
		t.Fatalf("root: %+v", root)
	}
	if _, err := c.Choose(cue.Path{}, 1); err != nil {
		t.Fatal(err)
	}
	if got := exportJSON(t, c); got != `{"kind":"b","y":2}` {
		t.Errorf("export: %s", got)
	}

	// No discriminator (KubeVela's container-image): a chooser comes first,
	// and choosing asks for the field only that variant has.
	c = compile(t, "#P: {image: string, name: *\"\" | string}\n#P | close({containers: [...#P]})")
	root := c.Root()
	if root.Widget != model.Variant || len(root.Children) == 0 || !root.Children[0].Chooser {
		t.Fatalf("root: %+v", root)
	}
	ask, err := c.Choose(cue.Path{}, 1)
	if err != nil || ask == nil || ask.Path.String() != "containers" {
		t.Fatalf("choose: %+v %v", ask, err)
	}
	if err := c.SetExpr(ask.Path, `[{image: "nginx"}]`); err != nil {
		t.Fatal(err)
	}
	if got := exportJSON(t, c); got != `{"containers":[{"image":"nginx","name":""}]}` {
		t.Errorf("export: %s", got)
	}
}

// A struct with ... takes fields it does not declare, at the root or below.
func TestOpenStruct(t *testing.T) {
	c := compile(t, `...`)
	if root := c.Root(); root.Widget != model.Menu || !root.Open {
		t.Fatalf("root: %+v", root)
	}
	entry, err := c.Entry(cue.Path{}, "x")
	if err != nil || entry.Widget != model.Expr {
		t.Fatalf("entry: %+v %v", entry, err)
	}
	if err := c.SetExpr(entry.Path, `{y: 1}`); err != nil {
		t.Fatal(err)
	}
	if got := exportJSON(t, c); got != `{"x":{"y":1}}` {
		t.Errorf("export: %s", got)
	}

	c = compile(t, "a: {b: int | *1, ...}\nclosed: {b: int | *1}")
	got := tree(c)
	if !got["a"].Open || got["closed"].Open || c.Root().Open {
		t.Errorf("open: a=%v closed=%v root=%v", got["a"].Open, got["closed"].Open, c.Root().Open)
	}
	if _, err := c.Entry(cue.ParsePath("closed"), "x"); err == nil {
		t.Error("a struct without ... takes no new fields")
	}
}

// A variant without default is not chosen yet: nothing is shown as chosen,
// and with a discriminator that field is where it is chosen.
func TestVariantWithoutDefault(t *testing.T) {
	c := compile(t, "#P: {image: string}\na: #P | close({containers: [...#P]})\nb: {kind: \"x\", x: int | *1} | {kind: \"y\", y: int | *2}")
	got := tree(c)
	if got["a"].Value != "" || got["a"].Children[0].Value != "" {
		t.Errorf("undiscriminated: %q / chooser %q", got["a"].Value, got["a"].Children[0].Value)
	}
	kind := got["b.kind"]
	if kind == nil || kind.Widget != model.Choice || len(kind.Options) != 2 || kind.Value != "" {
		t.Fatalf("discriminator to choose with: %+v", kind)
	}
	if err := c.SetExpr(kind.Path, `"y"`); err != nil {
		t.Fatal(err)
	}
	if got := tree(c); got["b"].Value != "y" || got["b.y"] == nil {
		t.Errorf("after choosing: %q %v", got["b"].Value, got["b.y"])
	}

	// Same at the top of the package.
	c = compile(t, "#A: {kind: \"a\", x: int | *1}\n#B: {kind: \"b\", y: int | *2}\n#A | #B")
	if kind := tree(c)["kind"]; kind == nil || kind.Widget != model.Choice || kind.Value != "" {
		t.Errorf("root discriminator: %+v", kind)
	}
}

// Timoni's nodeSelector: a map whose default has entries.
func TestMapWithDefault(t *testing.T) {
	c := compile(t, `nodeSelector: *{"kubernetes.io/os": "linux"} | {[string]: string}`)
	n := tree(c)["nodeSelector"]
	if n.Widget != model.Map || n.Hint != "[string]: string" || len(n.Children) != 1 {
		t.Fatalf("map with default: %+v", n)
	}
	entry, err := c.Entry(n.Path, "disk")
	if err != nil || entry.Widget != model.Text {
		t.Fatalf("entry: %+v %v", entry, err)
	}
}

// A computed field waiting for its inputs shows the formula as its hint.
func TestComputedHint(t *testing.T) {
	n := tree(compile(t, "n!: int\nsum: n + 1"))["sum"]
	if n.Widget != model.ReadOnly || n.Value != "" || n.Hint != "n + 1" {
		t.Errorf("computed: %+v", n)
	}
}

// Values are shown without the attributes of the schema (@go, @protobuf).
func TestValueWithoutAttributes(t *testing.T) {
	n := tree(compile(t, `tol: [...{key: string @go(Key)}] & [{key: "a"}]`))["tol[0]"]
	if n == nil || strings.Contains(n.Value, "@") {
		t.Errorf("element: %+v", n)
	}
}

// A definition whose fields depend on a choice not made yet (Holos' #Task:
// if kind == "Helm" {helm!: ...}) is still a menu, also at the root.
func TestFieldsAwaitingAChoice(t *testing.T) {
	dir := t.TempDir()
	src := "package s\n\n#T: {\n\tkind: \"a\" | \"b\"\n\tif kind == \"a\" {x!: int}\n\tif kind == \"b\" {y!: int}\n}\n\n#T\n"
	if err := os.WriteFile(dir+"/s.cue", []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := model.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := model.New(v)
	if root := c.Root(); root.Widget != model.Menu {
		t.Fatalf("root: %+v", root)
	}
	if n := tree(c)["kind"]; n == nil || n.Widget != model.Choice {
		t.Fatalf("kind: %+v", n)
	}
	// Each if fails the same way: the error is reported once.
	if err := c.Check(); err == nil || strings.Count(err.Error(), "unresolved disjunction") != 1 {
		t.Errorf("check: %v", err)
	}
}

// Go-generated enums are a type and a disjunction: string & ("a" | "b").
func TestTypedEnum(t *testing.T) {
	n := tree(compile(t, `kind: string & ("a" | "b")`))["kind"]
	if n.Widget != model.Choice || len(n.Options) != 2 {
		t.Errorf("typed enum: %+v", n)
	}
}

// Go pointers to scalars are null | T: edited as T, null when not set.
func TestNullableScalars(t *testing.T) {
	c := compile(t, "n?: null | int\nb?: null | bool\ns?: null | string")
	nodes := tree(c)
	for path, want := range map[string]model.Widget{"n": model.Number, "b": model.Toggle, "s": model.Text} {
		if n := nodes[path]; n.Widget != want {
			t.Errorf("%s: %+v", path, n)
		}
	}
	set(t, c, "n", 3)
	if got := exportJSON(t, c); got != `{"n":3}` {
		t.Errorf("export: %s", got)
	}
}

// A Go pointer to a struct, null | #T, is a menu with the fields of #T.
func TestNullableStruct(t *testing.T) {
	c := compile(t, "#P: {path?: string}\np?: null | #P")
	if n := tree(c)["p.path"]; n == nil || n.Widget != model.Text {
		t.Fatalf("p.path: %+v", n)
	}
	set(t, c, "p.path", "/x")
	if got := exportJSON(t, c); got != `{"p":{"path":"/x"}}` {
		t.Errorf("export: %s", got)
	}
}

// A disjunction of booleans is a toggle, also with a type: bool & (*false | true).
func TestBoolDisjunction(t *testing.T) {
	nodes := tree(compile(t, "a: *false | true\nb: bool & (*false | true)"))
	for _, path := range []string{"a", "b"} {
		if n := nodes[path]; n.Widget != model.Toggle || n.Value != "false" {
			t.Errorf("%s: %+v", path, n)
		}
	}
}

// The fields of struct elements are edited one by one, also in a list
// inside an element; the list is saved with only what was set.
func TestElementFields(t *testing.T) {
	c := compile(t, `#R: {
	receiver!: string
	continue:  bool | *false
	matchers: [...{name!: string, value: string | *""}]
	kind?:     "a" | "b"
}
routes: [...#R]
fixed: *[{receiver: "x"}] | [...#R]`)
	if err := c.SetList(cue.ParsePath("routes"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	nodes := tree(c)
	if n := nodes["routes[0].receiver"]; n == nil || n.Widget != model.Text || !n.Required {
		t.Fatalf("element field: %+v", n)
	}
	if n := nodes["routes[0].kind"]; n == nil || n.Widget != model.Choice {
		t.Fatalf("element enum: %+v", n)
	}
	set(t, c, "routes[0].receiver", "team")
	if err := c.SetList(cue.ParsePath("routes[0].matchers"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	set(t, c, "routes[0].matchers[0].name", "sev")
	if got := c.Elems(cue.ParsePath("routes")); len(got) != 1 || strings.Contains(got[0], "continue") ||
		!strings.Contains(got[0], `name: "sev"`) || strings.Contains(got[0], "value") {
		t.Fatalf("written element: %q", got)
	}
	if n := tree(c)["routes[0].matchers[0].name"]; n == nil || !n.Set || n.Value != `"sev"` {
		t.Fatalf("nested element field: %+v", n)
	}
	if err := c.Set(cue.ParsePath("routes[0].nope"), 1); err == nil {
		t.Error("undeclared field in an element accepted")
	}
	c.Unset(cue.ParsePath("routes[0].receiver"))
	if n := tree(c)["routes[0].receiver"]; n.Set || len(n.Errors) != 0 {
		t.Errorf("unset element field: %+v", n)
	}

	// An unedited default list: its elements are editable, not fixed.
	if n := tree(c)["fixed[0].receiver"]; n == nil || n.Widget != model.Text {
		t.Fatalf("default list element field: %+v", n)
	}
	set(t, c, "fixed[0].receiver", "y")
	if got := c.Elems(cue.ParsePath("fixed")); len(got) != 1 || !strings.Contains(got[0], `"y"`) {
		t.Fatalf("default list edited: %q", got)
	}
	dir := t.TempDir()
	if err := c.Save(dir + "/c.cue"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dir + "/c.cue") //nolint:gosec // a file in t.TempDir()
	if err != nil || strings.Contains(string(data), "continue") || strings.Contains(string(data), "_") {
		t.Fatalf("saved with defaults or placeholders (%v):\n%s", err, data)
	}
	loaded := compile(t, `#R: {
	receiver!: string
	continue:  bool | *false
	matchers: [...{name!: string, value: string | *""}]
	kind?:     "a" | "b"
}
routes: [...#R]
fixed: *[{receiver: "x"}] | [...#R]`)
	if dropped, err := loaded.Load(dir + "/c.cue"); err != nil || len(dropped) > 0 {
		t.Fatalf("load: %v, dropped %v", err, dropped)
	}
	if n := tree(loaded)["routes[0].matchers[0].name"]; n == nil || !n.Set {
		t.Errorf("loaded element field: %+v", n)
	}
}

// Struct variants as elements: chosen per element, by a discriminator.
func TestVariantElements(t *testing.T) {
	c := compile(t, "#A: {kind: \"a\", x?: int}\n#B: {kind: \"b\", y?: string}\nitems: [...(#A | #B)]")
	if err := c.SetList(cue.ParsePath("items"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	if n := tree(c)["items[0]"]; n == nil || n.Widget != model.Variant {
		t.Fatalf("variant element: %+v", n)
	}
	if _, err := c.Choose(cue.ParsePath("items[0]"), 1); err != nil {
		t.Fatal(err)
	}
	set(t, c, "items[0].y", "v")
	if n := tree(c)["items[0]"]; n.Value != "b" {
		t.Fatalf("chosen variant: %+v", n)
	}
	if _, err := c.Choose(cue.ParsePath("items[0]"), 0); err != nil {
		t.Fatalf("switching the variant of an element: %v", err)
	}
	if got := c.Elems(cue.ParsePath("items")); len(got) != 1 || strings.Contains(got[0], "y:") {
		t.Errorf("fields of the old variant kept: %q", got)
	}
}

// A recursive schema (Grafana's notification routes) nests lists of the
// same struct: each level is a menu too.
func TestRecursiveElements(t *testing.T) {
	c := compile(t, `#Route: {
	receiver?: string
	routes?: [...#Route]
}
routes: [...#Route]`)
	if err := c.SetList(cue.ParsePath("routes"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	if n := tree(c)["routes[0].routes"]; n == nil || n.Elem == nil || n.Elem.Widget != model.Menu || strings.Contains(n.Hint, "_|_") {
		t.Fatalf("nested recursive list: %+v", n)
	}
	if err := c.SetList(cue.ParsePath("routes[0].routes"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	set(t, c, "routes[0].routes[0].receiver", "b")
	if n := tree(c)["routes[0].routes[0].receiver"]; n == nil || n.Widget != model.Text || !n.Set {
		t.Fatalf("field of a nested recursive element: %+v", n)
	}
	if n := tree(c)["routes[0].routes[0].routes"]; n == nil || n.Elem == nil || n.Elem.Widget != model.Menu {
		t.Fatalf("third level: %+v", n)
	}
}

// A loaded value that breaks an element (a file edited by hand) makes the
// whole list bottom: the elements must still be listed, with the error on
// the field, so it can be fixed.
func TestElementErrors(t *testing.T) {
	c := compile(t, `#R: {receiver?: string, port?: int & <=65535}
routes: [...#R]
ports: [...int]`)
	file := t.TempDir() + "/c.cue"
	if err := os.WriteFile(file, []byte("routes: [{receiver: 1}, {receiver: \"ok\"}]\nports: [\"x\", 2]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(file); err != nil {
		t.Fatal(err)
	}
	nodes := tree(c)
	if n := nodes["routes[0].receiver"]; n == nil || len(n.Errors) == 0 || n.Value != "1" {
		t.Fatalf("error on the element field, with the value written: %+v", n)
	}
	if n := nodes["routes[1].receiver"]; n == nil || n.Value != `"ok"` {
		t.Errorf("valid element next to it: %+v", n)
	}
	if n := nodes["ports[0]"]; n == nil || len(n.Errors) == 0 || n.Value != cueX {
		t.Errorf("error on a scalar element: %+v", n)
	}
	set(t, c, "routes[0].receiver", "fixed")
	if n := tree(c)["routes[0].receiver"]; len(n.Errors) != 0 || n.Value != `"fixed"` {
		t.Errorf("after fixing it: %+v", n)
	}
}

// A required map not filled in yet (GitHub Actions' jobs!) takes keys:
// its patterns are in the declaration, not in a value.
func TestRequiredMapEntry(t *testing.T) {
	c := compile(t, "import \"struct\"\njobs!: struct.MinFields(1) & close({[=~\"^[a-z]+$\"]: {runs: string}})\nname!: string")
	if nodes := tree(c); !nodes["jobs"].Missing || !nodes["name"].Missing {
		t.Errorf("required and not given: %+v %+v", nodes["jobs"], nodes["name"])
	}
	entry, err := c.Entry(cue.ParsePath("jobs"), "build")
	if err != nil || entry.Widget != model.Menu {
		t.Fatalf("entry of a required map: %+v, %v", entry, err)
	}
	if _, err := c.Entry(cue.ParsePath("jobs"), "Bad"); err == nil {
		t.Error("a key outside the pattern was accepted")
	}
	set(t, c, "jobs.build.runs", "x")
	set(t, c, "name", "n")
	nodes := tree(c)
	if n := nodes["jobs.build.runs"]; n == nil || n.Value != cueX {
		t.Errorf("field of the new entry: %+v", n)
	}
	if nodes["jobs"].Missing || nodes["name"].Missing {
		t.Errorf("given, no longer missing: %+v %+v", nodes["jobs"], nodes["name"])
	}
}

// JSON Schema's oneOf/anyOf, imported by cue as matchN(1, […]) and
// matchN(>=1, […]), as in GitHub Actions: edited like a disjunction.
func TestMatchN(t *testing.T) {
	c := compile(t, `
#step: matchN(1, [{uses!: _, ...}, {run!: _, ...}]) & close({
	name?: string
	uses?: string
	run?:  string
})
#normalJob: close({snapshot?: string, "runs-on"!: string, steps?: [...#step]})
#callJob: close({uses!: string, with?: [string]: string})
jobs!: [=~"^[a-z]+$"]: matchN(1, [#normalJob, #callJob])
shell?: matchN(>=1, [string, "bash" | "sh"])
names?: matchN(1, [string, [...string]])
not?: matchN(0, [string])`)
	nodes := tree(c)
	if n := nodes["shell"]; n == nil || n.Widget != model.Text {
		t.Errorf("anyOf of strings: %+v", n)
	}
	if n := nodes["names"]; n == nil || n.Widget != model.Expr {
		t.Errorf("string or list: %+v", n)
	}
	entry, err := c.Entry(cue.ParsePath("jobs"), "build")
	if err != nil || entry.Widget != model.Variant || strings.Join(entry.Options, ",") != "runs-on,uses" {
		t.Fatalf("oneOf of structs: %+v, %v", entry, err)
	}
	set(t, c, "jobs.build", map[string]any{})
	ask, err := c.Choose(cue.ParsePath("jobs.build"), 0)
	if err != nil || ask == nil || ask.Path.String() != `jobs.build."runs-on"` || ask.Widget != model.Text || !ask.Required {
		t.Fatalf("choosing a oneOf variant asks for its field: %+v, %v", ask, err)
	}
	set(t, c, `jobs.build."runs-on"`, "ubuntu")
	if n := tree(c)[`jobs.build."runs-on"`]; n == nil || n.Widget != model.Text {
		t.Fatalf("a field set in a oneOf variant keeps its own widget: %+v", n)
	}
	if err := c.SetList(cue.ParsePath("jobs.build.steps"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	nodes = tree(c)
	if n := nodes[`jobs.build."runs-on"`]; n == nil || !n.Set {
		t.Fatalf("a field of the chosen oneOf variant was dropped when setting another: %+v", n)
	}
	if n := nodes["jobs.build.steps[0]"]; n == nil || n.Widget != model.Menu {
		t.Fatalf("a step, matchN(1, [{uses!: _, ...}, …]) & close({…}), is a menu of its fields: %+v", n)
	}
	if n := nodes["jobs.build.steps[0].run"]; n == nil || n.Widget != model.Text {
		t.Fatalf("fields of an element inside a oneOf variant: %+v", n)
	}
	set(t, c, "jobs.build.steps[0].run", "make")
	if got := exportJSON(t, c); got != `{"jobs":{"build":{"runs-on":"ubuntu","steps":[{"run":"make"}]}}}` {
		t.Errorf("export: %s", got)
	}
}

// Choosing the first variant while none is decided yet ({} fits both)
// must ask for its field, not take it as already chosen.
func TestChooseFirstUndecided(t *testing.T) {
	c := compile(t, "#A: close({a!: int})\n#B: close({b!: string})\nx: #A | #B\nx: {}")
	ask, err := c.Choose(cue.ParsePath("x"), 0)
	if err != nil || ask == nil || ask.Path.String() != "x.a" {
		t.Fatalf("choose the first variant: %+v, %v", ask, err)
	}
}

// The fields of a variant chosen by default keep their widget: an enum
// with a default stays a choice (KubeVela's startup-probe).
func TestVariantFieldsKeepWidgets(t *testing.T) {
	c := compile(t, "#x: {httpGet?: {scheme?: *\"HTTP\" | \"HTTPS\"}}\n*#x | close({probes: [...#x]})")
	if n := tree(c)["httpGet.scheme"]; n == nil || n.Widget != model.Choice {
		t.Errorf("enum inside the default variant: %+v", n)
	}
}

// A package that is a struct or a scalar (Azure Pipelines: matchN(1,
// [#pipeline, =~"^$"])) is a menu of the struct's fields.
func TestRootStructOrScalar(t *testing.T) {
	c := compile(t, "#pipeline: close({trigger?: string, pool?: {vmImage?: string}})\nmatchN(1, [#pipeline, =~\"^$\"])")
	root := c.Root()
	if root.Widget != model.Menu || len(root.Children) != 2 {
		t.Fatalf("root: %+v", root)
	}
	set(t, c, "pool.vmImage", "ubuntu-latest")
	if got := exportJSON(t, c); got != `{"pool":{"vmImage":"ubuntu-latest"}}` {
		t.Errorf("export: %s", got)
	}
}

// matchIf (JSON Schema's if/then/else) next to a closed struct, as in
// Dependabot's schema: valid edits must not be rejected.
func TestMatchIfClosed(t *testing.T) {
	c := compile(t, "matchIf({beta!: true, ...}, {...}, {...}) & close({version?: int, beta?: bool, updates!: [...{eco?: string}]})")
	set(t, c, "version", 2)
	if err := c.SetList(cue.ParsePath("updates"), []string{"{}"}); err != nil {
		t.Fatalf("valid edit rejected: %v", err)
	}
	set(t, c, "updates[0].eco", "gomod")
	if got := exportJSON(t, c); !strings.Contains(got, `"version":2`) || !strings.Contains(got, `"updates":[{"eco":"gomod"}]`) {
		t.Errorf("export: %s", got)
	}
}

// A JSON Schema with properties but no type, as cue import writes it
// (docker-compose's services): any JSON, a struct if it is one. Edited
// as the struct, with the fields of its allOf parts (matchN(2, […])).
func TestUntypedStruct(t *testing.T) {
	c := compile(t, `#untyped: null | bool | number | string | [...]
#container: #untyped | {image?: string, ports?: [...string], ...}
#workload: #untyped | {scale?: int, ...}
#service: matchN(2, [#container, #workload]) & (#untyped | {restart?: string, ...})
services?: [=~"^[a-z]+$"]: #service`)
	entry, err := c.Entry(cue.ParsePath("services"), "web")
	if err != nil || entry.Widget != model.Menu {
		t.Fatalf("untyped struct entry: %+v, %v", entry, err)
	}
	set(t, c, "services.web", map[string]any{})
	for range 2 { // before and after an edit
		nodes := tree(c)
		for path, widget := range map[string]model.Widget{
			"services.web.image": model.Text, "services.web.ports": model.List,
			"services.web.scale": model.Number, "services.web.restart": model.Text,
		} {
			if n := nodes[path]; n == nil || n.Widget != widget {
				t.Fatalf("%s, with the allOf parts: %+v", path, n)
			}
		}
		set(t, c, "services.web.image", "nginx")
	}
	if got := exportJSON(t, c); got != `{"services":{"web":{"image":"nginx"}}}` {
		t.Errorf("export: %s", got)
	}
}

// bool | {…} is a real choice, not an untyped struct (codecov's status).
func TestBoolOrStruct(t *testing.T) {
	c := compile(t, `status?: matchN(>=1, [bool, close({project?: string})])`)
	if n := tree(c)["status"]; n == nil || n.Widget != model.Expr {
		t.Errorf("bool or struct: %+v", n)
	}
}

// Fields keep the schema's order once set, in the menu and in the export,
// also when they are all optional (JSON Schema): a struct otherwise lists
// the fields it was given first.
func TestFieldOrder(t *testing.T) {
	c := compile(t, "s?: {z?: int, m?: int, a?: int}\nname?: string\nport?: int\nkeys?: [string]: int")
	set(t, c, "keys.b", 1)
	set(t, c, "port", 1)
	set(t, c, "s.a", 1)
	set(t, c, "s.z", 2)
	set(t, c, "name", "x")
	set(t, c, "keys.a", 2)
	labels := func(nodes []*model.Node) string {
		out := make([]string, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, n.Label)
		}
		return strings.Join(out, " ")
	}
	if got := labels(c.Tree()); got != "s name port keys" {
		t.Errorf("root menu: %s", got)
	}
	if got := labels(tree(c)["s"].Children); got != "z m a" {
		t.Errorf("struct menu: %s", got)
	}
	if got := exportJSON(t, c); got != `{"s":{"z":2,"a":1},"name":"x","port":1,"keys":{"a":2,"b":1}}` {
		t.Errorf("export: %s", got)
	}
}

// A list element reads in the schema's order however its fields were
// edited, and the same after saving and loading it again.
func TestElementOrder(t *testing.T) {
	const schema = `l: [...{z: string, m?: string, a?: *"x" | "y"}]`
	c := compile(t, schema)
	if err := c.SetList(cue.ParsePath("l"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	set(t, c, "l[0].a", "y")
	set(t, c, "l[0].m", "m")
	set(t, c, "l[0].z", "z")
	want := "{\n\tz: \"z\"\n\tm: \"m\"\n\ta: \"y\"\n}"
	if got := c.Elems(cue.ParsePath("l")); len(got) != 1 || got[0] != want {
		t.Fatalf("edited: %q", got)
	}
	file := filepath.Join(t.TempDir(), ".config.cue")
	if err := c.Save(file); err != nil {
		t.Fatal(err)
	}
	again := compile(t, schema)
	if _, err := again.Load(file); err != nil {
		t.Fatal(err)
	}
	if got := again.Elems(cue.ParsePath("l")); len(got) != 1 || got[0] != want {
		t.Fatalf("reloaded: %q", got)
	}
}

// A list that must have elements ([_, ...], JSON Schema's minItems) shows
// them before any is written, and Elems has one per element shown, so
// that they can be edited.
func TestMinItemsList(t *testing.T) {
	c := compile(t, `assignees?: [...string] & [_, ...]`)
	if n := tree(c)["assignees[0]"]; n == nil {
		t.Fatal("element 0 not shown")
	}
	elems := c.Elems(cue.ParsePath("assignees"))
	if len(elems) != 1 {
		t.Fatalf("elems: %q", elems)
	}
	elems[0] = cueX
	if err := c.SetList(cue.ParsePath("assignees"), elems); err != nil {
		t.Fatal(err)
	}
	if got := exportJSON(t, c); got != `{"assignees":["x"]}` {
		t.Errorf("export: %s", got)
	}
}

// An element whose required fields are missing can leave out of the value
// the list it is in (docker-compose's develop.watch, under a matchN that
// cannot be decided yet). It is still shown as written, to be filled in.
func TestElementOutOfValue(t *testing.T) {
	c := compile(t, "#d: null | close({w?: [...close({p!: string})]})\ns: [string]: matchN(1, [{...}]) & (null | {d?: #d, ...})")
	if err := c.SetList(cue.ParsePath("s.x.d.w"), []string{"{}"}); err != nil {
		t.Fatal(err)
	}
	if n := tree(c)["s.x.d.w[0].p"]; n == nil || !n.Required {
		t.Fatalf("element field not shown: %+v", n)
	}
	set(t, c, "s.x.d.w[0].p", "a")
	if got := exportJSON(t, c); got != `{"s":{"x":{"d":{"w":[{"p":"a"}]}}}}` {
		t.Errorf("export: %s", got)
	}
}
