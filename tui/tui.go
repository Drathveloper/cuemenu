// Package tui is the menuconfig-style interface over a model.Config.
package tui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Drathveloper/cuemenu/model"
)

type mode int

const (
	browse mode = iota
	editing
	choosing
	searching
	confirmQuit
)

type frame struct {
	path   string // path of the menu node; "" for the root
	cursor int
}

// Model is the bubbletea model of the editor: the menu being browsed and
// the prompt (editor, chooser, search) open on top of it, if any.
type Model struct {
	target  *model.Node        // node being edited or chosen
	cfg     *model.Config      // the configuration being edited
	commit  func(string) error // editing / choosing: applies the value
	file    string             // where Save writes
	title   string             // shown in the header
	status  string             // last message or error
	root    *model.Node        // the menu tree: the whole configuration
	stack   []frame            // open menus, root first
	results []*model.Node      // searching: matches
	input   textinput.Model    // editing / searching: the text typed
	mode    mode               // what keys do now
	opt     int                // choosing / searching: cursor
	height  int                // terminal rows
	width   int                // terminal columns
	asExpr  bool               // editing: input is a CUE expression
	dirty   bool               // changes not saved yet
}

// New returns the editor for cfg, saving to file.
func New(cfg *model.Config, file, title string) *Model {
	return &Model{
		cfg: cfg, file: file, title: title,
		root: cfg.Root(), stack: []frame{{}}, input: textinput.New(),
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model: keys go to the handler of the current mode.
//
//nolint:ireturn // the signature is fixed by tea.Model
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height, m.width = msg.Height, msg.Width
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		var cmd tea.Cmd
		switch m.mode {
		case browse:
			cmd = m.browseKey(msg)
		case editing:
			cmd = m.editKey(msg)
		case choosing:
			m.chooseKey(msg)
		case searching:
			cmd = m.searchKey(msg)
		case confirmQuit:
			cmd = m.confirmKey(msg)
		}
		return m, cmd
	}
	return m, nil
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	var head, body, foot strings.Builder
	m.header(&head)
	m.footer(&foot)
	footer := foot.String()
	if m.width > 0 { // wrapped here, so that its height is known
		footer = lipgloss.NewStyle().Width(m.width).Render(footer)
	}
	rows := 0 // unknown: no limit
	if m.height > 0 {
		room := m.height - strings.Count(head.String(), "\n") - 1 // one menu row at least
		if lines := strings.Split(footer, "\n"); len(lines) > room {
			footer = strings.Join(lines[len(lines)-max(1, room):], "\n") // the editor and its errors are last
		}
		rows = max(1, m.height-strings.Count(head.String(), "\n")-lipgloss.Height(footer))
	}
	m.body(&body, rows)
	view := tea.NewView(head.String() + body.String() + footer)
	view.AltScreen = true
	view.WindowTitle = "cuemenu"
	return view
}

// top is the menu being shown.
func (m *Model) top() *frame { return &m.stack[len(m.stack)-1] }

// items returns the current menu, dropping menus from the stack that no
// longer exist (e.g. after choosing another variant).
func (m *Model) items() []*model.Node {
	nodes := m.root.Children
	for depth, open := range m.stack[1:] {
		node := byPath(nodes, open.path)
		if node == nil {
			m.stack = m.stack[:depth+1]
			break
		}
		nodes = node.Children
	}
	m.top().cursor = max(0, min(m.top().cursor, len(nodes)-1))
	return nodes
}

func (m *Model) selected() *model.Node {
	items := m.items()
	if len(items) == 0 {
		return nil
	}
	return items[m.top().cursor]
}

// container is the node whose children are the current menu. At the top
// it is the root, which can be a map or a variant too.
func (m *Model) container() *model.Node {
	cont := m.root
	nodes := cont.Children
	for _, open := range m.stack[1:] {
		if cont = byPath(nodes, open.path); cont == nil {
			return nil
		}
		nodes = cont.Children
	}
	return cont
}

// run applies a commit. The mode is reset first so a commit can chain into
// another prompt (map key, then its value).
func (m *Model) run(commit func(string) error, value string, back mode) {
	m.mode, m.status = browse, "" // a chained prompt may set a new status
	if err := commit(value); err != nil {
		m.mode, m.status = back, err.Error() // stay so it can be fixed
		return
	}
	if m.mode == browse { // a chained prompt has changed nothing yet
		m.changed()
	}
}

func (m *Model) apply(err error) {
	if err != nil {
		m.status = err.Error()
		return
	}
	m.changed()
}

// changed rebuilds the tree, keeping the cursor on the same field: a change
// can add or remove fields above it (an `if` in the schema).
func (m *Model) changed() {
	var path string
	if node := m.selected(); node != nil {
		path = node.Path.String()
	}
	m.dirty = true
	m.root = m.cfg.Root()
	items := m.items()
	if idx := slices.IndexFunc(items, func(n *model.Node) bool { return n.Path.String() == path }); idx >= 0 {
		m.top().cursor = idx
	}
}

func (m *Model) save() bool {
	if err := m.cfg.Save(m.file); err != nil {
		m.status = "Could not save: " + err.Error()
		return false
	}
	m.dirty = false
	m.status = "Saved to " + m.file
	return true
}

func (m *Model) quit() tea.Cmd {
	if !m.dirty {
		return tea.Quit
	}
	m.mode = confirmQuit
	return nil
}

func byPath(nodes []*model.Node, path string) *model.Node {
	for _, node := range nodes {
		if node.Path.String() == path {
			return node
		}
	}
	return nil
}

func isList(node *model.Node) bool { return node != nil && node.Widget == model.List }

func isMap(node *model.Node) bool { return node != nil && node.Widget == model.Map }

// takesFields: a map, or a struct declared with ..., gets new keys with a.
func takesFields(node *model.Node) bool { return isMap(node) || node != nil && node.Open }

func walk(nodes []*model.Node, visit func(*model.Node)) {
	for _, node := range nodes {
		visit(node)
		walk(node.Children, visit)
	}
}
