package tui_test

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"cuelang.org/go/cue"

	"github.com/Drathveloper/cuemenu/model"
	"github.com/Drathveloper/cuemenu/tui"
)

const (
	modeEditing = "editing"
	modeBrowse  = "browse"
	cueX        = `"x"`
)

var named = map[string]rune{
	"up": tea.KeyUp, "down": tea.KeyDown, "enter": tea.KeyEnter,
	"esc": tea.KeyEscape, "space": tea.KeySpace, "backspace": tea.KeyBackspace,
}

// press sends keys: named keys ("enter") or literal text ("'abc").
func press(t *testing.T, m *tui.Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		var msgs []tea.KeyPressMsg
		if text, ok := strings.CutPrefix(k, "'"); ok {
			for _, r := range text {
				msgs = append(msgs, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		} else if k == "ctrl+u" {
			msgs = append(msgs, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		} else if code, ok := named[k]; ok {
			msgs = append(msgs, tea.KeyPressMsg{Code: code})
		} else {
			t.Fatalf("unknown key %q", k)
		}
		for _, msg := range msgs {
			m.Update(msg)
			m.View() // every state must render
		}
	}
}

func newModel(t *testing.T) (*tui.Model, string) {
	t.Helper()
	v, err := model.Load("../examples/webapp")
	if err != nil {
		t.Fatal(err)
	}
	file := t.TempDir() + "/.config.cue"
	return tui.New(model.New(v), file, "webapp"), file
}

// A full session: search, toggle, choose, edit, a rejected value, a
// variant change, save and quit.
func TestSession(t *testing.T) {
	m, file := newModel(t)

	press(t, m, "'/", "'enable_gzip", "enter", "space") // search + toggle
	press(t, m, "'/", "'protocol", "enter", "enter", "down", "enter")
	if n := m.Selected(); n.Label != "protocol" || n.Value != `"https"` {
		t.Fatalf("protocol: %+v", n)
	}

	press(t, m, "'/", "'cert_file", "enter", "enter", "'/etc/c.pem", "enter")
	press(t, m, "'/", "'http_port", "enter", "enter")
	press(t, m, "backspace", "backspace", "backspace", "backspace", "'70000", "enter")
	if m.Mode() != modeEditing || m.Status() == "" {
		t.Fatalf("invalid port should keep the editor open with an error, mode=%s status=%q", m.Mode(), m.Status())
	}
	press(t, m, "esc")

	// Variant: enter database, choose postgres on the discriminator.
	press(t, m, "'/", "'database.type", "enter", "enter", "down", "enter")
	if n := m.Selected(); n.Value != `"postgres"` {
		t.Fatalf("variant not chosen: %+v", n)
	}
	if !strings.Contains(m.View().Content, "user") {
		t.Error("postgres fields not shown in the database menu")
	}

	press(t, m, "'q")
	if m.Mode() != "confirmQuit" {
		t.Fatal("quit with unsaved changes should ask")
	}
	press(t, m, "'s")

	v, _ := model.Load("../examples/webapp")
	c := model.New(v)
	if _, err := c.Load(file); err != nil {
		t.Fatal(err)
	}
	got := c.Value()
	for path, want := range map[string]string{
		"server.enable_gzip": "true",
		"server.protocol":    `"https"`,
		"server.cert_file":   `"/etc/c.pem"`,
		"server.http_port":   "3000",
		"database.type":      `"postgres"`,
	} {
		if s := strings.TrimSpace(fmtValue(got.LookupPath(cue.ParsePath(path)))); s != want {
			t.Errorf("%s = %s, want %s", path, s, want)
		}
	}
}

// Editing as a CUE expression (the universal fallback) and resetting.
func TestExprAndReset(t *testing.T) {
	m, _ := newModel(t)
	press(t, m, "'/", "'outputs", "enter", "'e") // e: the whole list as CUE
	if m.Mode() != modeEditing || !m.AsExpr() {
		t.Fatal("e should open the CUE expression editor")
	}
	m.SetInput(`["console", "file"]`)
	press(t, m, "enter")
	if n := m.Selected(); n.Value != `["console", "file"]` || !n.Set {
		t.Fatalf("outputs: %+v", n)
	}
	press(t, m, "'r")
	if n := m.Selected(); n.Set || n.Value != `["console"]` {
		t.Fatalf("reset: %+v", n)
	}
}

func fmtValue(v cue.Value) string {
	if d, ok := v.Default(); ok {
		v = d
	}
	b, _ := v.MarshalJSON()
	return string(b)
}

func values(n *model.Node) []string {
	out := make([]string, 0, len(n.Children))
	for _, e := range n.Children {
		out = append(out, e.Value)
	}
	return out
}

func TestListEditing(t *testing.T) {
	m, file := newModel(t)
	press(t, m, "'/", "'outputs", "enter", "enter") // into the list
	if c := m.Container(); c == nil || c.Widget != model.List {
		t.Fatal("enter on a list should open it")
	}
	press(t, m, "'a", "down", "enter") // add "file" from the enum chooser
	press(t, m, "'a", "enter")         // add "console" again
	if got := values(m.Container()); strings.Join(got, ",") != `"console","file","console"` {
		t.Fatalf("after adds: %v", got)
	}
	press(t, m, "'d") // cursor stays on the element it was on: [0]
	press(t, m, "'J") // move the first element down
	if got := values(m.Container()); strings.Join(got, ",") != `"console","file"` || m.Cursor() != 1 {
		t.Fatalf("after delete+move: %v cursor %d", got, m.Cursor())
	}
	press(t, m, "enter", "up", "enter") // change [1] to "console"... then back
	if got := values(m.Container()); got[1] != `"console"` {
		t.Fatalf("edit element via chooser: %v", got)
	}

	// List of structs: a new element is added empty and opened as a menu
	// of its fields. Only what is set is saved, not the defaults.
	press(t, m, "esc", "'/", "'filters", "enter", "enter", "'a")
	if c := m.Container(); m.Mode() != modeBrowse || c == nil || c.Path.String() != "log.filters[0]" || len(c.Children) != 2 {
		t.Fatalf("struct element should open as a menu: mode %s %+v (%s)", m.Mode(), c, m.Status())
	}
	press(t, m, "enter", "'db", "enter") // module
	if n := m.Selected(); n.Value != `"db"` || !n.Set {
		t.Fatalf("element field: %+v (%s)", n, m.Status())
	}
	if got := saved(t, m, file); !strings.Contains(got, `module: "db"`) || strings.Contains(got, "level") {
		t.Fatalf("element saved with defaults:\n%s", got)
	}
	press(t, m, "down", "enter", "down", "enter") // level: info
	if got := saved(t, m, file); !strings.Contains(got, `level:  "info"`) {
		t.Fatalf("element level:\n%s", got)
	}
	press(t, m, "'r") // back to the default
	if got := saved(t, m, file); strings.Contains(got, "level") || m.Selected().Value != `"debug"` {
		t.Fatalf("element field reset:\n%s", got)
	}
	press(t, m, "esc")
	if n := m.Selected(); n.Set {
		t.Errorf("a struct element is a menu, not a set value: %+v", n)
	}
	if got := values(m.Container()); len(got) != 1 || !strings.Contains(got[0], `module: "db"`) {
		t.Fatalf("filters: %v", got)
	}
}

func saved(t *testing.T, m *tui.Model, file string) string {
	t.Helper()
	press(t, m, "'s")
	data, err := os.ReadFile(file) //nolint:gosec // a file in t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMapEditing(t *testing.T) {
	m, _ := newModel(t)
	press(t, m, "'/", "'headers", "enter", "enter", "'a", "'Foo", "enter")
	if m.Mode() != modeEditing || !strings.Contains(m.Status(), "pattern") {
		t.Fatalf("key not matching the pattern should be rejected: %s %q", m.Mode(), m.Status())
	}
	press(t, m, "ctrl+u", "'X-Frame", "enter") // key ok: now its value
	if m.Mode() != modeEditing || m.Target().Label != "X-Frame" {
		t.Fatalf("should ask for the value: %+v", m.Target())
	}
	press(t, m, "'DENY", "enter")
	if m.Status() != "" {
		t.Errorf("stale status after success: %q", m.Status())
	}
	if n := m.Selected(); n == nil || n.Value != `"DENY"` {
		t.Fatalf("header: %+v", n)
	}

	// Struct entry: created and entered directly.
	press(t, m, "esc", "esc", "'/", "'oauth2", "enter", "enter", "'a", "'github", "enter")
	if c := m.Container(); c == nil || c.Path.String() != "auth.oauth2.github" {
		t.Fatalf("should be inside the new entry, got %v", c)
	}
	press(t, m, "esc", "'d")
	if len(m.Container().Children) != 0 {
		t.Error("d should remove the entry")
	}
}

func TestVariantChooser(t *testing.T) {
	m, _ := newModel(t)
	press(t, m, "'/", "'cache", "enter", "enter") // into cache
	if n := m.Selected(); n == nil || !n.Chooser {
		t.Fatalf("first entry should be the variant chooser: %+v", n)
	}
	press(t, m, "enter", "down", "enter", "esc") // choose redis, then cancel
	if strings.Contains(m.View().Content, "(unsaved)") {
		t.Error("cancelling the field a variant asks for changes nothing")
	}
	press(t, m, "enter", "down", "enter") // choose redis: asks for addr
	if m.Mode() != modeEditing || m.Target().Path.String() != "cache.redis.addr" {
		t.Fatalf("should ask for cache.redis.addr: mode %s %+v", m.Mode(), m.Target())
	}
	press(t, m, "'localhost:6379", "enter")
	if c := m.Container(); c.Value != "redis" {
		t.Fatalf("cache should be redis now: %+v", c)
	}
}

// A field of mixed kinds is typed as CUE, but text that is no valid CUE
// is taken as a string where a string fits (GitHub Actions' runs-on).
func TestMixedKindPlainString(t *testing.T) {
	m := load(t, `runs: string | [...string]`+"\n"+`n: int | [...int]`)
	press(t, m, "enter", "'ubuntu-latest", "enter")
	if n := m.Selected(); n.Value != `"ubuntu-latest"` || m.Status() != "" {
		t.Fatalf("plain string: %+v (%s)", n, m.Status())
	}
	press(t, m, "enter", "ctrl+u", `'["a", "b"]`, "enter")
	if n := m.Selected(); !strings.Contains(n.Value, `"b"`) {
		t.Fatalf("list as CUE: %+v (%s)", n, m.Status())
	}
	press(t, m, "down", "enter", "'x-y", "enter")
	if m.Mode() != modeEditing || m.Status() == "" {
		t.Errorf("no string fits: the CUE error stays: %s %q", m.Mode(), m.Status())
	}
}

// A required field with a single value (JSON Schema's const, as
// Dependabot's version!: 2) is read-only, but must be given: ⏎ does.
func TestFixedRequired(t *testing.T) {
	m := load(t, "version!: 2\nname?: string")
	press(t, m, "enter")
	if n := m.Selected(); !n.Set || n.Missing || m.Status() != "" {
		t.Fatalf("required constant: %+v (%s)", n, m.Status())
	}
	press(t, m, "enter")
	if m.Status() == "" {
		t.Error("a given constant is just fixed")
	}
}

// The editor of an element not added yet starts empty: looking it up gives
// an error, which is "concrete" too, and was typed into the editor.
func TestAddSecondExprElement(t *testing.T) {
	m := load(t, `ports?: [...(int | string)]`)
	press(t, m, "enter", "'a", "'8080:80", "enter", "'a")
	if got := m.Input(); got != "" {
		t.Fatalf("new element editor starts with %q", got)
	}
	press(t, m, "'3000", "enter")
	if got := values(m.Container()); strings.Join(got, " ") != `"8080:80" 3000` {
		t.Fatalf("elements: %q (%s)", got, m.Status())
	}
}

// ⏎ on an empty text field puts it back to the schema's value, as the
// editor says, and an empty key adds no map entry.
func TestEmptyText(t *testing.T) {
	m := load(t, "name: *\"x\" | string\ncert!: string\nm: [string]: string")
	press(t, m, "enter", "ctrl+u", "enter")
	if n := m.Selected(); n.Set || n.Value != cueX {
		t.Fatalf("empty text: %+v", n)
	}
	press(t, m, "down", "enter", "enter")
	if n := m.Selected(); n.Set || !n.Missing {
		t.Fatalf("empty required text: %+v", n)
	}
	press(t, m, "down", "enter", "'a", "enter")
	if m.Mode() != modeBrowse || len(m.Container().Children) != 0 {
		t.Fatalf("empty key: mode %s, %d entries (%s)", m.Mode(), len(m.Container().Children), m.Status())
	}
}

// Unsaved means different from what was saved: a change undone, or one
// that changes nothing, asks for no save.
func TestUnsavedOnlyIfChanged(t *testing.T) {
	m := load(t, "name: *\"x\" | string\nm: [string]: string")
	press(t, m, "enter", "ctrl+u", "enter") // back to the default it has
	press(t, m, "down", "enter", "'a", "enter")
	if strings.Contains(m.View().Content, "unsaved") {
		t.Fatal("nothing changed, yet unsaved")
	}
	press(t, m, "esc", "up", "enter", "'y", "enter")
	if !strings.Contains(m.View().Content, "unsaved") {
		t.Fatal("a change not marked unsaved")
	}
	press(t, m, "'r")
	if strings.Contains(m.View().Content, "unsaved") {
		t.Fatal("a change undone, yet unsaved")
	}
}

// A variant told apart by a list (KubeVela's container-image) is chosen
// with the list empty, opened to add elements; their enums stay choices.
func TestVariantByList(t *testing.T) {
	m := load(t, `#x: {p: *"" | "x" | "y", q: string}`+"\n"+`#x | close({c: [...#x]})`)
	press(t, m, "enter", "down", "enter")
	if c := m.Container(); m.Mode() != modeBrowse || c == nil || c.Path.String() != "c" {
		t.Fatalf("list not opened: mode %s, %+v (%s)", m.Mode(), c, m.Status())
	}
	press(t, m, "'a")
	if n := m.Selected(); n == nil || n.Label != "p" || n.Widget != model.Choice {
		t.Fatalf("element enum: %+v", n)
	}
}
