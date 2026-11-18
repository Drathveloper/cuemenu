package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/literal"

	"github.com/Drathveloper/cuemenu/model"
)

const (
	maxSummary = 40  // longer list summaries show the count instead
	maxHint    = 100 // longer constraints are clipped
	maxValue   = 40  // and longer values
	searchRows = 2   // the search box and a blank line under it
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

// body writes the menu, the options or the search results in rows lines
// (0: no limit), scrolled to keep the cursor visible.
func (m *Model) body(out *strings.Builder, rows int) {
	switch m.mode {
	case choosing:
		first, end := window(m.opt, len(m.target.Options), rows)
		for idx, opt := range m.target.Options[first:end] {
			mark := "( )"
			if opt == m.target.Value {
				mark = "(X)"
			}
			out.WriteString(row(fmt.Sprintf(" %s %s", mark, unquote(opt)), first+idx == m.opt) + "\n")
		}
	case searching:
		out.WriteString(m.input.View() + "\n\n")
		if rows > 0 {
			rows = max(1, rows-searchRows)
		}
		lo, hi := window(m.opt, len(m.results), rows)
		for idx, node := range m.results[lo:hi] {
			out.WriteString(row(" "+node.Path.String(), lo+idx == m.opt) + "\n")
		}
	case browse, editing, confirmQuit:
		m.menu(out, rows)
	}
}

func (m *Model) menu(out *strings.Builder, rows int) {
	items := m.items()
	cursor := m.top().cursor
	if len(items) == 0 {
		out.WriteString(dimStyle().Render(" (empty)") + "\n")
	}
	lo, hi := window(cursor, len(items), rows)
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
	case takesFields(cont):
		return "a add key  ⏎ open/edit  d delete entry  e edit as CUE  s save  esc back"
	}
	return "↑↓ ⏎ open/edit  space toggle  e edit as CUE  r reset  / search  s save  esc back  q quit"
}

func details(node *model.Node) string {
	var out strings.Builder
	if node.Widget != "" { // else a prompt, e.g. for a new key: no field yet
		info := node.Path.String() + "  [" + string(node.Widget) + "]"
		if node.Required {
			info += "  required"
		}
		if node.Optional {
			info += "  optional"
		}
		out.WriteString(dimStyle().Render(info) + "\n")
	}
	if node.Doc != "" {
		out.WriteString(node.Doc + "\n")
	}
	if node.Hint != "" {
		out.WriteString(dimStyle().Render("Constraint: "+Hint(node)) + "\n")
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
			return fmt.Sprintf("    %s %s  --->", node.Label, compact(val))
		}
		return fmt.Sprintf("    %s  --->", node.Label)
	case model.List:
		return fmt.Sprintf("    %s (%s)  --->", node.Label, summary(node))
	case model.Choice, model.Variant:
		return fmt.Sprintf("    %s (%s)  --->", node.Label, val)
	case model.ReadOnly:
		return readOnlyEntry(node.Label, val)
	case model.Number, model.Text, model.Expr:
	}
	if isElem(node) {
		return node.Label + " " + val
	}
	return fmt.Sprintf("(%s) %s", val, node.Label)
}

// Hint is the constraint of node on one line, clipped if too long: the
// type of a list of structs can take thousands of characters.
func Hint(node *model.Node) string { return clip(compact(node.Hint), maxHint) }

func clip(text string, size int) string {
	if runes := []rune(text); len(runes) > size {
		return string(runes[:size-1]) + "…"
	}
	return text
}

func readOnlyEntry(label, val string) string {
	if val == "" { // computed from fields not set yet
		return fmt.Sprintf("-*- %s (computed)", label)
	}
	return fmt.Sprintf("-*- %s = %s", label, val)
}

// shown is the value as displayed: on one line, unquoted, and hidden if
// secret.
func shown(node *model.Node) string {
	val := clip(compact(unquote(node.Value)), maxValue)
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
	if list.Value == "null" { // *null | [...T] left null
		return list.Value
	}
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

// unquote is the text of a CUE string literal: "x", """multi-line""",
// #"raw"#. Anything else is returned as is.
func unquote(text string) string {
	if plain, err := literal.Unquote(text); err == nil {
		return plain
	}
	return text
}

// oneLine writes CUE text on one line for the editor, which would turn line
// breaks into spaces: a multi-line string escaped ("l1\nl2"), a struct or
// a list as JSON, which is CUE too. Values reaching the editor are concrete.
func oneLine(text string) string {
	if !strings.Contains(text, "\n") {
		return text
	}
	if data, err := cuecontext.New().CompileString(text).MarshalJSON(); err == nil {
		return string(data)
	}
	return text
}
