package tui_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"cuelang.org/go/cue"

	"github.com/Drathveloper/cuemenu/model"
	"github.com/Drathveloper/cuemenu/tui"
)

// Random key presses over each schema: no panic, every state renders, the
// list open shows the elements edited, and what is saved loads back to the
// same tree. CUEMENU_MONKEY names a file listing more schema directories
// (separated by blanks); CUEMENU_MONKEY_LONG runs more and longer walks.
func TestMonkey(t *testing.T) {
	t.Parallel()
	dirs := []string{"../examples/webapp", "../model/testdata/imports"}
	if list := os.Getenv("CUEMENU_MONKEY"); list != "" {
		dirs = strings.Fields(readFile(t, list))
	}
	seeds, steps := uint64(4), 150
	if os.Getenv("CUEMENU_MONKEY_LONG") != "" {
		seeds, steps = 8, 400
	} else if testing.Short() {
		seeds, steps = 1, 60
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			schema, err := model.Load(dir)
			if err != nil {
				t.Skip(err)
			}
			for seed := range seeds {
				monkey(t, schema, seed, steps)
			}
		})
	}
}

// What the monkey types, separated by |.
const monkeyTexts = `1|0|-1|true|abc|"x"|null|{}|[]|a b|x: 1|8080|1.5|*|\n`

func monkey(t *testing.T, schema cue.Value, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // reproducible, not secret
	texts := strings.Split(monkeyTexts, "|")
	cfg := model.New(schema)
	file := filepath.Join(t.TempDir(), ".config.cue")
	m := tui.New(cfg, file, "monkey")
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	trail := make([]string, 0, steps)
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d: %s\nkeys: %s", seed, fmt.Sprintf(format, args...), strings.Join(trail, " "))
	}
	defer func() {
		if r := recover(); r != nil {
			fail("panic: %v\n%s", r, debug.Stack())
		}
	}()
	for range steps {
		msgs, name := randomKey(rng, texts)
		trail = append(trail, name)
		for _, msg := range msgs {
			m.Update(msg)
		}
		m.View()
		if bad := listMismatch(cfg, m.Container()); bad != "" {
			fail("%s", bad)
		}
		if name == "s" && m.Mode() == modeBrowse && strings.HasPrefix(m.Status(), "Saved") {
			roundTrip(t, schema, cfg, file, fail)
		}
	}
	if err := cfg.Save(file); err != nil {
		fail("save: %v", err)
	}
	roundTrip(t, schema, cfg, file, fail)
	if testing.Verbose() {
		t.Logf("seed %d saved:\n%s", seed, readFile(t, file))
	}
}

func roundTrip(t *testing.T, schema cue.Value, cfg *model.Config, file string, fail func(string, ...any)) {
	t.Helper()
	data := readFile(t, file)
	if strings.Contains(data, "_|_") {
		fail("saved an error as a value:\n%s", data)
	}
	again := model.New(schema)
	if dropped, err := again.Load(file); err != nil || len(dropped) > 0 {
		fail("reload: %v, dropped %v\n%s", err, dropped, data)
	}
	if a, b := dump(cfg.Root()), dump(again.Root()); a != b {
		fail("reload differs:\n--- edited\n%s\n--- reloaded\n%s\n--- file\n%s", a, b, data)
	}
}

// listMismatch reports the open list if the elements it shows are not
// those its keys edit.
func listMismatch(cfg *model.Config, node *model.Node) string {
	if node == nil || node.Widget != model.List {
		return ""
	}
	shown := 0
	for _, c := range node.Children {
		if strings.HasPrefix(c.Label, "[") {
			shown++
		}
	}
	if elems := cfg.Elems(node.Path); len(elems) != shown {
		return fmt.Sprintf("list %s shows %d elements, edits %d %q", node.Path, shown, len(elems), elems)
	}
	return ""
}

func dump(node *model.Node) string {
	var b strings.Builder
	var rec func(*model.Node, int)
	rec = func(n *model.Node, depth int) {
		fmt.Fprintf(&b, "%s%s %v %s\n", strings.Repeat("  ", depth), n.Path, n.Widget, n.Value)
		for _, c := range n.Children {
			rec(c, depth+1)
		}
	}
	rec(node, 0)
	return b.String()
}

func randomKey(rng *rand.Rand, texts []string) ([]tea.KeyPressMsg, string) {
	key := func(code rune) []tea.KeyPressMsg { return []tea.KeyPressMsg{{Code: code}} }
	char := func(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }
	switch n := rng.IntN(100); {
	case n < 25:
		return key(tea.KeyDown), "down"
	case n < 30:
		return key(tea.KeyUp), "up"
	case n < 50:
		return key(tea.KeyEnter), "enter"
	case n < 58:
		return key(tea.KeyEscape), "esc"
	case n < 64:
		return key(tea.KeySpace), "space"
	case n < 76:
		text := texts[rng.IntN(len(texts))]
		var msgs []tea.KeyPressMsg
		for _, r := range text {
			msgs = append(msgs, char(r))
		}
		return msgs, "'" + text
	case n < 80:
		return key(tea.KeyBackspace), "backspace"
	case n < 82:
		return []tea.KeyPressMsg{{Code: 'u', Mod: tea.ModCtrl}}, "ctrl+u"
	default:
		r := []rune("eradKJ/sq")[rng.IntN(9)]
		return []tea.KeyPressMsg{char(r)}, string(r)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a test file, or a list the developer chose
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
