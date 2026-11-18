package tui_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"cuelang.org/go/cue"

	"github.com/Drathveloper/cuemenu/model"
	"github.com/Drathveloper/cuemenu/tui"
)

func TestOneLinePerEntry(t *testing.T) {
	node := &model.Node{Label: "a", Widget: model.ReadOnly, Value: "{\n\tx: 1\n}", Path: cue.ParsePath("a")}
	if got := tui.Line(node); strings.Contains(got, "\n") || got != "-*- a = { x: 1 }" {
		t.Errorf("multi-line value: %q", got)
	}
}

func TestNullList(t *testing.T) {
	node := &model.Node{Label: "args", Widget: model.List, Value: "null", Path: cue.ParsePath("args")}
	if got := tui.Line(node); got != "    args (null)  --->" {
		t.Errorf("null list: %q", got)
	}
}

func TestLongHintClipped(t *testing.T) {
	hint := "[...{\n" + strings.Repeat("x: string ", 100) + "}]"
	got := tui.Hint(&model.Node{Hint: hint})
	if strings.Contains(got, "\n") || len([]rune(got)) > 100 || !strings.HasSuffix(got, "…") {
		t.Errorf("long hint: %d runes %q", len([]rune(got)), got)
	}
	if got := tui.Hint(&model.Node{Hint: "int &\n>0"}); got != "int & >0" {
		t.Errorf("short hint: %q", got)
	}
}

func TestNullListEditing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/s.cue", []byte("package s\n\nargs: *null | [...string]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := model.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(model.New(v), dir+"/.config.cue", "s")
	press(t, m, "enter", "'a", "'x", "enter") // open the list, add "x"
	if got := values(m.Container()); len(got) != 1 || got[0] != `"x"` {
		t.Fatalf("add to a null list: %v", got)
	}
	press(t, m, "esc", "'r")
	if n := m.Selected(); n.Set || n.Value != "null" {
		t.Fatalf("reset to null: %+v", n)
	}
}

func load(t *testing.T, src string) *tui.Model {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/s.cue", []byte("package s\n\n"+src+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := model.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tui.New(model.New(v), dir+"/.config.cue", "s")
}

func TestRootContainers(t *testing.T) {
	m := load(t, `[string]: string | null`)             // a map: a adds a key
	press(t, m, "'a", "'app", "enter", "'web", "enter") // string | null: typed as text
	if n := m.Selected(); n == nil || n.Path.String() != "app" || n.Value != `"web"` {
		t.Fatalf("root map entry: %+v (%s)", n, m.Status())
	}
	press(t, m, "'d")
	if m.Selected() != nil {
		t.Error("d should delete the entry")
	}

	m = load(t, `...`) // open: a adds a field
	press(t, m, "'a", "'x", "enter", "'1", "enter")
	if n := m.Selected(); n == nil || n.Path.String() != "x" || n.Value != "1" {
		t.Fatalf("open root field: %+v (%s)", n, m.Status())
	}

	m = load(t, "#P: {image: string}\n#P | close({containers: [...#P]})")
	if n := m.Selected(); n == nil || !n.Chooser {
		t.Fatalf("root variant should start on its chooser: %+v", n)
	}
	press(t, m, "enter", "down", "enter", `'[{image: "x"}]`, "enter")
	if got := m.Container().Value; got != "containers" {
		t.Errorf("root variant after choosing: %q (%s)", got, m.Status())
	}

	m = load(t, "#A: {kind: \"a\", x: int | *1}\n#B: {kind: \"b\", y: int | *2}\n#A | #B")
	press(t, m, "enter", "down", "enter") // choose kind "b" on the discriminator
	if got := m.Container(); got.Value != "b" || len(got.Children) != 2 {
		t.Errorf("root variant by discriminator: %+v (%s)", got, m.Status())
	}
}

func TestMapWithDefaultEditing(t *testing.T) {
	m := load(t, `nodeSelector: *{"kubernetes.io/os": "linux"} | {[string]: string}`)
	press(t, m, "enter", "enter", "ctrl+u", "'windows", "enter") // edit the default entry
	if n := m.Selected(); n == nil || n.Value != `"windows"` {
		t.Fatalf("default entry: %+v (%s)", n, m.Status())
	}
	press(t, m, "'a", "'disk", "enter", "'ssd", "enter")
	if got := values(m.Container()); len(got) != 2 {
		t.Errorf("after adding a key: %v (%s)", got, m.Status())
	}
}

func TestValuesShown(t *testing.T) {
	multi := &model.Node{Label: "a", Widget: model.Text, Value: "\"\"\"\n\tl1\n\tl2\n\t\"\"\"", Path: cue.ParsePath("a")}
	if got := tui.Line(multi); got != "(l1 l2) a" {
		t.Errorf("multi-line string: %q", got)
	}
	raw := &model.Node{Label: "p", Widget: model.Text, Value: `#"c:\dir"#`, Path: cue.ParsePath("p")}
	if got := tui.Line(raw); got != `(c:\dir) p` {
		t.Errorf("raw string: %q", got)
	}
	long := &model.Node{Label: "a", Widget: model.Text, Value: `"` + strings.Repeat("x", 200) + `"`, Path: cue.ParsePath("a")}
	if got := tui.Line(long); len([]rune(got)) > 60 || !strings.Contains(got, "…") {
		t.Errorf("long value: %d runes %q", len([]rune(got)), got)
	}
}

// The editor is one line: values that span several lines must survive it.
func TestMultiLineEditing(t *testing.T) {
	m := load(t, "content: string | *\"\"\"\n\tl1\n\tl2\n\t\"\"\"\nraw: string | *#\"c:\\dir\"#\ns: {a: int | *1, b: int | *2}")
	press(t, m, "enter")
	if !m.AsExpr() || m.Input() != `"l1\nl2"` {
		t.Fatalf("multi-line text should open escaped as CUE: expr=%v %q", m.AsExpr(), m.Input())
	}
	press(t, m, "enter")
	if n := m.Selected(); m.Status() != "" || !strings.Contains(n.Value, "l1") || !strings.Contains(n.Value, "\n") {
		t.Errorf("unchanged multi-line text: %q (%s)", n.Value, m.Status())
	}
	press(t, m, "down", "enter")
	if m.AsExpr() || m.Input() != `c:\dir` {
		t.Fatalf("raw string: expr=%v %q", m.AsExpr(), m.Input())
	}
	press(t, m, "enter")
	if n := m.Selected(); n.Value != `"c:\\dir"` && n.Value != `#"c:\dir"#` {
		t.Errorf("unchanged raw string: %q (%s)", n.Value, m.Status())
	}
	press(t, m, "down", "'e")
	if strings.Contains(m.Input(), "\n") || m.Input() == "" {
		t.Fatalf("struct as CUE on one line: %q", m.Input())
	}
	press(t, m, "enter")
	if m.Status() != "" {
		t.Errorf("unchanged struct rejected: %s", m.Status())
	}
}

func TestComputedPending(t *testing.T) {
	node := &model.Node{Label: "major", Widget: model.ReadOnly, Path: cue.ParsePath("major")}
	if got := tui.Line(node); got != "-*- major (computed)" {
		t.Errorf("computed field waiting for its inputs: %q", got)
	}
}

// The screen never grows past the terminal: the menu shrinks so that the
// details, the editor and its error stay visible, and long option lists
// scroll.
func TestFitsTheTerminal(t *testing.T) {
	var src strings.Builder
	src.WriteString("// one\n// two\n// three\n// four\n")
	for idx := range 30 {
		fmt.Fprintf(&src, "f%d: *512 | int & >=64\n", idx)
	}
	src.WriteString(`c: "o" | *"p" | "q"`)
	for idx := range 30 {
		fmt.Fprintf(&src, ` | "o%d"`, idx)
	}
	m := load(t, src.String())
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 15})
	press(t, m, "enter", "ctrl+u", "'10", "enter")
	screen := m.View().Content
	if !fits(screen, 60, 15) || !strings.Contains(screen, "out of bound") {
		t.Errorf("rejected value:\n%s", screen)
	}
	press(t, m, "esc")
	for range 30 {
		press(t, m, "down")
	}
	press(t, m, "enter")
	for range 25 {
		press(t, m, "down")
	}
	screen = m.View().Content
	if !fits(screen, 60, 15) || !strings.Contains(screen, "o23") {
		t.Errorf("long chooser:\n%s", screen)
	}
}

func fits(screen string, width, height int) bool {
	lines := strings.Split(screen, "\n")
	for _, line := range lines {
		if lipgloss.Width(line) > width {
			return false
		}
	}
	return len(lines) <= height
}

// The prompt for a new key has no field to describe yet.
func TestNewKeyPrompt(t *testing.T) {
	m := load(t, `[string]: string`)
	press(t, m, "'a")
	if screen := m.View().Content; strings.Contains(screen, "  []") || !strings.Contains(screen, "Constraint: [string]: string") {
		t.Errorf("new key prompt:\n%s", screen)
	}
}

// A new entry that is a struct of any kind (a variant, a map) is created
// and opened, as a plain struct entry is.
func TestNewVariantEntry(t *testing.T) {
	m := load(t, `els: [string]: {kind: "a", x?: int} | {kind: "b", y?: string}`)
	press(t, m, "enter", "'a", "'p1", "enter")
	if c := m.Container(); m.Mode() != "browse" || c == nil || c.Path.String() != "els.p1" || c.Widget != model.Variant {
		t.Fatalf("new variant entry: mode %s, container %+v (%s)", m.Mode(), c, m.Status())
	}
}
