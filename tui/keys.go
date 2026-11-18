package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/literal"

	"github.com/Drathveloper/cuemenu/model"
)

const (
	keyUp    = "up"
	keyDown  = "down"
	keyEnter = "enter"
	keyEsc   = "esc"
	keySpace = "space"
)

const (
	msgFixed = "Fixed or computed by the schema."
	msgNoAdd = "Nothing to add here: only lists and maps take new items."
	cueTrue  = "true"
)

// Search ranks, best first.
const (
	rankExact = iota // exact name or path
	rankName         // name contains the query
	rankOther        // path or doc contain the query
)

// browseActions maps each key to what it does while browsing a menu.
func browseActions() map[string]func(*Model) tea.Cmd {
	return map[string]func(*Model) tea.Cmd{
		keyUp: (*Model).cursorUp, "k": (*Model).cursorUp,
		keyDown: (*Model).cursorDown, "j": (*Model).cursorDown,
		keyEnter: (*Model).openSelected, "right": (*Model).openSelected, "l": (*Model).openSelected,
		keySpace: (*Model).toggleOrOpen,
		"e":      (*Model).editAsCUE,
		"r":      (*Model).remove, "d": (*Model).remove,
		"a": (*Model).add,
		"K": (*Model).elemUp, "shift+up": (*Model).elemUp,
		"J": (*Model).elemDown, "shift+down": (*Model).elemDown,
		keyEsc: (*Model).back, "left": (*Model).back, "h": (*Model).back, "backspace": (*Model).back,
		"/": (*Model).startSearch,
		"s": func(m *Model) tea.Cmd { m.save(); return nil },
		"q": (*Model).quit,
	}
}

func (m *Model) browseKey(msg tea.KeyPressMsg) tea.Cmd {
	m.status = ""
	var cmd tea.Cmd
	if action, ok := browseActions()[msg.String()]; ok {
		cmd = action(m)
	}
	m.items() // clamp the cursor
	return cmd
}

func (m *Model) cursorUp() tea.Cmd   { m.top().cursor--; return nil }
func (m *Model) cursorDown() tea.Cmd { m.top().cursor++; return nil }

func (m *Model) openSelected() tea.Cmd { m.open(m.selected()); return nil }

func (m *Model) toggleOrOpen() tea.Cmd {
	if node := m.selected(); node != nil && node.Widget == model.Toggle {
		m.toggle(node)
	} else {
		m.open(node)
	}
	return nil
}

func (m *Model) editAsCUE() tea.Cmd {
	node, cont := m.selected(), m.container()
	switch {
	case node == nil:
	case isList(cont):
		m.editWith(node, true, m.setElem(cont, index(node)))
	case node.Widget != model.ReadOnly && !node.Chooser:
		m.edit(node, true)
	}
	return nil
}

// remove deletes a list element or a map entry, or resets a field.
func (m *Model) remove() tea.Cmd {
	node, cont := m.selected(), m.container()
	switch {
	case node == nil:
	case isList(cont):
		elems, idx := m.elems(cont), index(node)
		m.apply(m.cfg.SetList(cont.Path, slices.Delete(elems, idx, idx+1)))
	case node.Set || isMap(cont):
		m.cfg.Unset(node.Path) // in a map, removes the whole entry
		m.changed()
	}
	return nil
}

// add creates a list element or a map entry in the current menu.
func (m *Model) add() tea.Cmd {
	cont := m.container()
	switch {
	case isList(cont) && cont.Elem != nil:
		m.addElem(cont)
	case isMap(cont):
		m.addEntry(cont)
	default:
		m.status = msgNoAdd
	}
	return nil
}

func (m *Model) addElem(list *model.Node) {
	m.editOrChoose(list.Elem, func(val string) error {
		return m.cfg.SetList(list.Path, append(m.elems(list), val))
	})
	if m.mode == editing && list.Elem.Widget == model.Menu {
		m.input.SetValue("{}")
	}
}

// addEntry asks for a key, then creates the entry: a struct entry is
// created and opened; any other asks for its value next.
func (m *Model) addEntry(mapNode *model.Node) {
	m.editWith(&model.Node{Label: "new key", Hint: mapNode.Hint}, false, func(key string) error {
		entry, err := m.cfg.Entry(mapNode.Path, key)
		if err != nil {
			return fmt.Errorf("new entry: %w", err)
		}
		if entry.Widget != model.Menu {
			m.editOrChoose(entry, nil)
			return nil
		}
		if err := m.cfg.Set(entry.Path, map[string]any{}); err != nil {
			return fmt.Errorf("creating %s: %w", entry.Path, err)
		}
		m.changed()
		m.stack = append(m.stack, frame{path: entry.Path.String()})
		return nil
	})
}

func (m *Model) elemUp() tea.Cmd   { m.moveElem(-1); return nil }
func (m *Model) elemDown() tea.Cmd { m.moveElem(1); return nil }

func (m *Model) moveElem(delta int) {
	node, cont := m.selected(), m.container()
	if node == nil || !isList(cont) {
		return
	}
	from := index(node)
	dest := from + delta
	elems := m.elems(cont)
	if dest < 0 || dest >= len(elems) {
		return
	}
	elems[from], elems[dest] = elems[dest], elems[from]
	m.apply(m.cfg.SetList(cont.Path, elems))
	m.top().cursor = dest
}

func (m *Model) back() tea.Cmd {
	if len(m.stack) > 1 {
		m.stack = m.stack[:len(m.stack)-1]
		return nil
	}
	return m.quit()
}

func (m *Model) startSearch() tea.Cmd {
	m.mode, m.opt, m.results = searching, 0, nil
	m.input.Reset()
	m.input.Prompt = "Search: "
	m.input.EchoMode = textinput.EchoNormal
	return m.input.Focus()
}

// open performs the default action on node, like Enter in menuconfig.
func (m *Model) open(node *model.Node) {
	if node == nil {
		return
	}
	if cont := m.container(); isList(cont) {
		m.editOrChoose(node, m.setElem(cont, index(node)))
		return
	}
	switch node.Widget {
	case model.Menu, model.Map, model.Variant, model.List:
		m.stack = append(m.stack, frame{path: node.Path.String()})
	case model.Toggle:
		m.toggle(node)
	case model.ReadOnly:
		m.status = msgFixed
	case model.Choice, model.Number, model.Text, model.Expr:
		m.editOrChoose(node, nil)
	}
}

// editOrChoose opens the chooser or the editor for node. set receives the
// new value as CUE text; nil means "set the field node itself".
func (m *Model) editOrChoose(node *model.Node, set func(string) error) {
	switch {
	case node.Chooser:
		m.choose(node, m.chooseVariant(node))
	case node.Widget == model.Choice && set == nil:
		m.choose(node, func(val string) error { return m.cfg.SetExpr(node.Path, val) })
	case node.Widget == model.Choice:
		m.choose(node, set)
	case node.Widget == model.ReadOnly:
		m.status = msgFixed
	default:
		m.editWith(node, node.Widget != model.Text, m.valueCommit(node, set))
	}
}

// valueCommit applies typed text, to the field itself (set == nil) or
// through set (a list element). Empty text resets a field to its default
// and adds nothing to a list; a string is typed without quotes.
func (m *Model) valueCommit(node *model.Node, set func(string) error) func(string) error {
	asExpr := node.Widget != model.Text
	return func(text string) error {
		switch {
		case text == "" && set == nil && asExpr:
			m.cfg.Unset(node.Path)
			return nil
		case text == "" && set != nil:
			return nil
		case set == nil && !asExpr:
			return m.cfg.Set(node.Path, text)
		case set == nil:
			return m.cfg.SetExpr(node.Path, text)
		case !asExpr:
			return set(literal.String.Quote(text))
		}
		return set(text)
	}
}

// chooseVariant picks a variant; if it is told apart by a field, the
// editor for that field opens next.
func (m *Model) chooseVariant(node *model.Node) func(string) error {
	return func(opt string) error {
		ask, err := m.cfg.Choose(node.Path, slices.Index(node.Options, opt))
		if err != nil {
			return fmt.Errorf("choosing %q: %w", opt, err)
		}
		if ask != nil {
			m.editOrChoose(ask, nil)
			m.status = fmt.Sprintf("To choose %q, fill in %s", opt, ask.Path)
		}
		return nil
	}
}

// setElem replaces element idx of list with a CUE value.
func (m *Model) setElem(list *model.Node, idx int) func(string) error {
	return func(val string) error {
		elems := m.elems(list)
		elems[idx] = val
		return m.cfg.SetList(list.Path, elems)
	}
}

// elems is the current list as CUE text, one string per element.
func (m *Model) elems(list *model.Node) []string {
	out := make([]string, 0, len(list.Children))
	for _, elem := range list.Children {
		out = append(out, elem.Value)
	}
	return out
}

func (m *Model) toggle(node *model.Node) {
	m.apply(m.cfg.Set(node.Path, node.Value != cueTrue))
}

// edit opens the editor on the field node itself (e: as a CUE expression).
func (m *Model) edit(node *model.Node, asExpr bool) {
	m.editWith(node, asExpr, func(text string) error {
		switch {
		case text == "":
			m.cfg.Unset(node.Path) // empty: back to the schema value
			return nil
		case asExpr:
			return m.cfg.SetExpr(node.Path, text)
		}
		return m.cfg.Set(node.Path, text)
	})
}

func (m *Model) editWith(node *model.Node, asExpr bool, commit func(string) error) {
	m.mode, m.target, m.asExpr, m.commit = editing, node, asExpr, commit
	val := node.Value
	if val == "" && asExpr && len(node.Path.Selectors()) > 0 {
		if current := m.cfg.Value().LookupPath(node.Path); current.IsConcrete() {
			val = fmt.Sprint(current)
		}
	}
	if !asExpr {
		val = unquote(val)
	}
	m.input.Reset()
	m.input.SetValue(val)
	m.input.Prompt = node.Label + ": "
	if asExpr {
		m.input.Prompt = node.Label + " (CUE): "
	}
	m.input.EchoMode = textinput.EchoNormal
	if node.Secret {
		m.input.EchoMode = textinput.EchoPassword
	}
	m.input.Focus()
}

func (m *Model) choose(node *model.Node, commit func(string) error) {
	m.mode, m.target, m.commit = choosing, node, commit
	m.opt = max(0, slices.Index(node.Options, node.Value))
}

func (m *Model) editKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case keyEsc:
		m.mode, m.status = browse, ""
		return nil
	case keyEnter:
		m.run(m.commit, m.input.Value(), editing)
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m *Model) chooseKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case keyUp, "k":
		m.opt = max(0, m.opt-1)
	case keyDown, "j":
		m.opt = min(len(m.target.Options)-1, m.opt+1)
	case keyEsc:
		m.mode = browse
	case keyEnter, keySpace:
		m.run(m.commit, m.target.Options[m.opt], browse)
	}
}

func (m *Model) searchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case keyEsc:
		m.mode = browse
		return nil
	case keyUp:
		m.opt = max(0, m.opt-1)
		return nil
	case keyDown:
		m.opt = min(len(m.results)-1, m.opt+1)
		return nil
	case keyEnter:
		m.mode = browse
		if m.opt < len(m.results) {
			m.jump(m.results[m.opt])
		}
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.results, m.opt = search(m.root, strings.ToLower(m.input.Value())), 0
	return cmd
}

// search finds nodes whose path or doc contain query: exact names first,
// then names containing it, then the rest.
func search(root []*model.Node, query string) []*model.Node {
	if query == "" {
		return nil
	}
	var found []*model.Node
	walk(root, func(node *model.Node) {
		if strings.Contains(strings.ToLower(node.Path.String()+" "+node.Doc), query) {
			found = append(found, node)
		}
	})
	slices.SortStableFunc(found, func(a, b *model.Node) int { return rank(a, query) - rank(b, query) })
	return found
}

func rank(node *model.Node, query string) int {
	label := strings.ToLower(node.Label)
	switch {
	case label == query || strings.ToLower(node.Path.String()) == query:
		return rankExact
	case strings.Contains(label, query):
		return rankName
	}
	return rankOther
}

// jump opens the menu that holds node and puts the cursor on it.
func (m *Model) jump(node *model.Node) {
	m.stack = []frame{{}}
	nodes := m.root
	sels := node.Path.Selectors()
	for depth := range sels {
		path := cue.MakePath(sels[:depth+1]...).String()
		idx := slices.IndexFunc(nodes, func(n *model.Node) bool { return n.Path.String() == path })
		if idx < 0 {
			return
		}
		m.top().cursor = idx
		if depth < len(sels)-1 {
			m.stack = append(m.stack, frame{path: path})
			nodes = nodes[idx].Children
		}
	}
}

func (m *Model) confirmKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "y", "s": // yes, save
		if m.save() {
			return tea.Quit
		}
		m.mode = browse
	case "n":
		return tea.Quit
	case keyEsc:
		m.mode, m.status = browse, ""
	}
	return nil
}

// index is the position of a list element.
func index(node *model.Node) int {
	sels := node.Path.Selectors()
	return sels[len(sels)-1].Index()
}
