package tui

import "github.com/Drathveloper/cuemenu/model"

// Hooks for the black-box tests in package tui_test.

var modeNames = [...]string{"browse", "editing", "choosing", "searching", "confirmQuit"}

func (m *Model) Mode() string           { return modeNames[m.mode] }
func (m *Model) AsExpr() bool           { return m.asExpr }
func (m *Model) Status() string         { return m.status }
func (m *Model) Selected() *model.Node  { return m.selected() }
func (m *Model) Container() *model.Node { return m.container() }
func (m *Model) Target() *model.Node    { return m.target }
func (m *Model) Input() string          { return m.input.Value() }
func (m *Model) SetInput(text string)   { m.input.SetValue(text) }
func (m *Model) Cursor() int            { return m.top().cursor }
