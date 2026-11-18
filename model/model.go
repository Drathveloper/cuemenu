// Package model turns an evaluated CUE value into a menu tree.
// All CUE semantics are delegated to cuelang.org/go; this package only
// decides which widget each field gets.
package model

import (
	"fmt"
	"slices"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueformat "cuelang.org/go/cue/format"
	"cuelang.org/go/cue/load"
)

// Widget is the control a field is edited with.
type Widget string

// Widgets, from the most specific to the universal fallback (Expr).
const (
	ReadOnly Widget = "readonly" // fixed or computed value
	Toggle   Widget = "toggle"   // bool
	Choice   Widget = "choice"   // disjunction of concrete scalars
	Number   Widget = "number"   // int, float, number
	Text     Widget = "text"     // string
	Menu     Widget = "menu"     // struct
	List     Widget = "list"     // list
	Map      Widget = "map"      // struct with a pattern constraint
	Variant  Widget = "variant"  // disjunction of structs
	Expr     Widget = "expr"     // anything else: edited as a CUE expression
)

// Node is a field of the configuration as shown in the menu.
type Node struct {
	Elem     *Node    // List: template for a new element ([...T])
	Doc      string   // from the field's // comments
	Label    string   // field name
	Widget   Widget   // how it is edited
	Value    string   // current or default value as CUE text, "" if unset
	Hint     string   // constraint as CUE text, when not concrete
	Children []*Node  // Menu, Map, Variant: fields; List: elements
	Options  []string // Choice: allowed values; Variant: variant names
	Errors   []string // evaluation errors at this path
	Path     cue.Path // where the field is
	discr    cue.Path // Variant: field that names the variants
	Set      bool     // changed by the user
	Secret   bool     // @menu(secret)
	Optional bool     // label?
	Required bool     // label!
	Chooser  bool     // Choice that picks a variant: use Config.Choose
}

// Load evaluates the CUE package in dir, with its imports and module deps.
// Files starting with "." (such as the saved .config.cue) are ignored by
// the CUE loader, so they never leak into the schema.
func Load(dir string) (cue.Value, error) {
	inst := load.Instances([]string{"."}, &load.Config{Dir: dir})[0]
	if inst.Err != nil {
		return cue.Value{}, fmt.Errorf("loading %s: %w", dir, inst.Err)
	}
	schema := cuecontext.New().BuildInstance(inst)
	if err := schema.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("evaluating %s: %w", dir, err)
	}
	return schema, nil
}

// classify decides the widget of a field from its unedited value.
func classify(node *Node, decl cue.Value) {
	op, args := decl.Expr()
	if op != cue.OrOp || !classifyDisjunction(node, flattenOr(args)) {
		node.Widget = kindWidget(decl, kindOf(decl, op, args))
	}
	node.Hint = hint(node.Widget, decl)
}

// classifyDisjunction handles | of structs (variants) and of concrete
// scalars (choices). Other disjunctions are classified by their kind.
func classifyDisjunction(node *Node, alts []cue.Value) bool {
	switch {
	case all(alts, isStruct):
		node.Widget = Variant
		node.Options, node.discr = variantNames(alts)
	case all(alts, isConstScalar):
		node.Widget = Choice
		for _, alt := range alts {
			node.Options = append(node.Options, format(alt))
		}
	default:
		return false
	}
	return true
}

func isStruct(val cue.Value) bool { return val.IncompleteKind() == cue.StructKind }

func isConstScalar(val cue.Value) bool {
	return val.IsConcrete() && val.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0
}

// kindOf is the kind of val. A validator such as list.MinItems(1) hides
// it (bottom): then it is the intersection of the kinds of the conjuncts.
func kindOf(val cue.Value, op cue.Op, args []cue.Value) cue.Kind {
	kind := val.IncompleteKind()
	if kind == cue.BottomKind && op == cue.AndOp {
		kind = cue.TopKind
		for _, arg := range args {
			kind &= arg.IncompleteKind()
		}
	}
	return kind
}

func kindWidget(val cue.Value, kind cue.Kind) Widget {
	switch {
	case kind == cue.StructKind && len(patterns(val)) > 0:
		return Map
	case kind == cue.StructKind:
		return Menu
	case kind == cue.ListKind:
		return List
	case val.IsConcrete():
		return ReadOnly
	case kind == cue.BoolKind:
		return Toggle
	case kind != 0 && kind&^cue.NumberKind == 0:
		return Number
	case kind == cue.StringKind:
		return Text
	}
	return Expr
}

// hint is the constraint shown next to a field that can still change.
func hint(widget Widget, decl cue.Value) string {
	switch widget {
	case Map:
		return strings.Join(patterns(decl), "\n")
	case List:
		if _, hasDefault := decl.Default(); decl.IsConcrete() && !hasDefault {
			return ""
		}
	case Menu, Variant, ReadOnly:
		return ""
	case Toggle, Choice, Number, Text, Expr:
	}
	return format(decl)
}

// flattenOr expands nested disjunctions: *("x" | "y") | "z".
func flattenOr(args []cue.Value) []cue.Value {
	var out []cue.Value
	for _, arg := range args {
		if op, sub := arg.Expr(); op == cue.OrOp {
			out = append(out, flattenOr(sub)...)
		} else {
			out = append(out, arg)
		}
	}
	return out
}

// leafValue is the concrete value of val, or its default, as CUE text.
// Final drops what is not data, e.g. an open list prints [] not [...string].
func leafValue(val cue.Value) string {
	if def, ok := val.Default(); ok && !val.IsConcrete() {
		val = def
	}
	if !val.IsConcrete() {
		return ""
	}
	text, err := cueformat.Node(val.Syntax(cue.Final()))
	if err != nil {
		return format(val)
	}
	return string(text)
}

// variantNames labels each struct variant by a field holding a distinct
// concrete string in every variant (a discriminator such as "type").
// Without one, each variant is named by the fields only it has.
func variantNames(alts []cue.Value) ([]string, cue.Path) {
	iter, err := alts[0].Fields()
	for err == nil && iter.Next() {
		discr := cue.MakePath(iter.Selector())
		if names, ok := discriminate(alts, discr); ok {
			return names, discr
		}
	}
	names := make([]string, len(alts))
	for idx := range alts {
		names[idx] = strings.Join(ownFields(alts, idx), ", ")
		if names[idx] == "" {
			names[idx] = fmt.Sprintf("variant %d", idx+1)
		}
	}
	return names, cue.Path{}
}

// discriminate returns the value of discr in each alternative, if it is a
// concrete string that differs in every one.
func discriminate(alts []cue.Value, discr cue.Path) ([]string, bool) {
	names := make([]string, 0, len(alts))
	for _, alt := range alts {
		fld := alt.LookupPath(discr)
		name, err := fld.String()
		if err != nil || !fld.IsConcrete() || slices.Contains(names, name) {
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

func doc(val cue.Value) string {
	groups := val.Doc()
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		parts = append(parts, strings.TrimSpace(group.Text()))
	}
	return strings.Join(parts, "\n")
}

func child(parent cue.Path, sel cue.Selector) cue.Path {
	// Clone: appending to parent.Selectors() would share its array
	// between siblings.
	return cue.MakePath(append(slices.Clone(parent.Selectors()), cue.Str(sel.Unquoted()))...)
}

// under reports whether path is root or lies below it.
func under(path, root cue.Path) bool {
	sels, rootSels := path.Selectors(), root.Selectors()
	if len(rootSels) > len(sels) {
		return false
	}
	for idx, sel := range rootSels {
		if sels[idx].String() != sel.String() {
			return false
		}
	}
	return true
}

// touches reports whether one path contains the other.
func touches(a, b cue.Path) bool { return under(a, b) || under(b, a) }

func format(val cue.Value) string { return fmt.Sprint(val) }

func all(vals []cue.Value, pred func(cue.Value) bool) bool {
	for _, val := range vals {
		if !pred(val) {
			return false
		}
	}
	return len(vals) > 0
}
