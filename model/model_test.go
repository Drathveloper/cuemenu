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
		pathMaxSize:                model.Expr,     // int | null: mixed kinds
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
