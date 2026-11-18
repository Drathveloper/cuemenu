package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"cuelang.org/go/cue"

	"github.com/Drathveloper/cuemenu/model"
)

const (
	reservedRows = 9  // header, details and help around the menu
	maxSummary   = 40 // longer list summaries show the count instead
)

func selStyle() lipgloss.Style  { return lipgloss.NewStyle().Reverse(true) }
func dimStyle() lipgloss.Style  { return lipgloss.NewStyle().Faint(true) }
func errStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(lipgloss.Red) }
func headStyle() lipgloss.Style { return lipgloss.NewStyle().Bold(true) }

func (m *Model) header(out *strings.Builder) {
	title := "cuemenu — " + m.title
	if m.dirty {
		title += " (unsaved)"
	}
	out.WriteString(headStyle().Render(title) + "\n")
	crumb := m.top().path
	if crumb == "" {
		crumb = "(root)"
	}
	out.WriteString(dimStyle().Render(crumb) + "\n\n")
}

func (m *Model) body(out *strings.Builder) {
	switch m.mode {
	case choosing:
		for idx, opt := range m.target.Options {
			mark := "( )"
			if opt == m.target.Value {
				mark = "(X)"
			}
			out.WriteString(row(fmt.Sprintf(" %s %s", mark, unquote(opt)), idx == m.opt) + "\n")
		}
	case searching:
		out.WriteString(m.input.View() + "\n\n")
		for idx, node := range m.results {
			out.WriteString(row(" "+node.Path.String(), idx == m.opt) + "\n")
		}
	case browse, editing, confirmQuit:
		m.menu(out)
	}
}

func (m *Model) menu(out *strings.Builder) {
	items := m.items()
	cursor := m.top().cursor
	if len(items) == 0 {
		out.WriteString(dimStyle().Render(" (empty)") + "\n")
	}
	lo, hi := window(cursor, len(items), m.height-reservedRows)
	for idx := lo; idx < hi; idx++ {
		out.WriteString(row(" "+Line(items[idx]), idx == cursor) + "\n")
	}
}

func (m *Model) footer(out *strings.Builder) {
	out.WriteString("\n")
	if node := m.focused(); node != nil {
		out.WriteString(details(node))
	}
	switch m.mode {
	case editing:
		out.WriteString(m.input.View() + "\n")
	case confirmQuit:
		out.WriteString(headStyle().Render("Save changes before quitting? (y/n, esc to go back)") + "\n")
	case browse, choosing, searching:
	}
	if m.status != "" {
		out.WriteString(errStyle().Render(m.status) + "\n")
	}
	out.WriteString(dimStyle().Render(m.help()))
}

// focused is the node whose details are shown under the menu.
func (m *Model) focused() *model.Node {
	switch m.mode {
	case choosing, editing:
		return m.target
	case searching:
		return nil
	case browse, confirmQuit:
	}
	return m.selected()
}

func (m *Model) help() string {
	switch m.mode {
	case browse:
		return m.browseHelp()
	case editing:
		return "⏎ apply (empty = default)  esc cancel"
	case choosing:
		return "↑↓ move  ⏎ choose  esc cancel"
	case searching:
		return "type to filter  ↑↓ move  ⏎ go  esc cancel"
	case confirmQuit:
	}
	return ""
}

func (m *Model) browseHelp() string {
	cont := m.container()
	switch {
	case isList(cont):
		return "a add  ⏎ edit  e edit as CUE  d delete  K/J move  s save  esc back"
	case isMap(cont):
		return "a add key  ⏎ open/edit  d delete entry  e edit as CUE  s save  esc back"
	}
	return "↑↓ ⏎ open/edit  space toggle  e edit as CUE  r reset  / search  s save  esc back  q quit"
}

func details(node *model.Node) string {
	var out strings.Builder
	info := node.Path.String() + "  [" + string(node.Widget) + "]"
	if node.Required {
		info += "  required"
	}
	if node.Optional {
		info += "  optional"
	}
	out.WriteString(dimStyle().Render(info) + "\n")
	if node.Doc != "" {
		out.WriteString(node.Doc + "\n")
	}
	if node.Hint != "" {
		out.WriteString(dimStyle().Render("Constraint: "+compact(node.Hint)) + "\n")
	}
	for _, msg := range node.Errors {
		out.WriteString(errStyle().Render("⚠ "+msg) + "\n")
	}
	return out.String()
}

// Line renders a node like a menuconfig entry.
func Line(node *model.Node) string { return entry(node) + markers(node) }

func entry(node *model.Node) string {
	val := shown(node)
	switch node.Widget {
	case model.Toggle:
		mark := " "
		if node.Value == cueTrue {
			mark = "*"
		}
		return fmt.Sprintf("[%s] %s", mark, node.Label)
	case model.Menu, model.Map:
		if isElem(node) { // struct element of a list
			return node.Label + " " + compact(val)
		}
		return fmt.Sprintf("    %s  --->", node.Label)
	case model.List:
		return fmt.Sprintf("    %s (%s)  --->", node.Label, summary(node))
	case model.Choice, model.Variant:
		return fmt.Sprintf("    %s (%s)  --->", node.Label, val)
	case model.ReadOnly:
		return fmt.Sprintf("-*- %s = %s", node.Label, val)
	case model.Number, model.Text, model.Expr:
	}
	if isElem(node) {
		return node.Label + " " + val
	}
	return fmt.Sprintf("(%s) %s", val, node.Label)
}

// shown is the value as displayed: unquoted, and hidden if secret.
func shown(node *model.Node) string {
	val := unquote(node.Value)
	if node.Secret && val != "" {
		return "****"
	}
	return val
}

func markers(node *model.Node) string {
	var out string
	if node.Set {
		out += " *"
	}
	if node.Required && node.Value == "" {
		out += " ⚠ required"
	}
	if len(node.Errors) > 0 {
		out += " ⚠"
	}
	return out
}

// summary lists the elements of a list, or counts them if too long.
func summary(list *model.Node) string {
	parts := make([]string, len(list.Children))
	for idx, elem := range list.Children {
		parts[idx] = compact(unquote(elem.Value))
	}
	joined := strings.Join(parts, ", ")
	if len(joined) > maxSummary {
		return fmt.Sprintf("%d elements", len(parts))
	}
	return joined
}

// isElem reports whether node is an element of a list.
func isElem(node *model.Node) bool {
	sels := node.Path.Selectors()
	return len(sels) > 0 && sels[len(sels)-1].Type() == cue.IndexLabel
}

func compact(text string) string { return strings.Join(strings.Fields(text), " ") }

func row(text string, selected bool) string {
	if selected {
		return selStyle().Render(text)
	}
	return text
}

// window returns the visible range [lo, hi) of count rows around cursor.
func window(cursor, count, size int) (int, int) {
	if size <= 0 || count <= size {
		return 0, count
	}
	lo := max(0, min(cursor-size/2, count-size))
	return lo, lo + size
}

func unquote(text string) string {
	if plain, err := strconv.Unquote(text); err == nil {
		return plain
	}
	return text
}
