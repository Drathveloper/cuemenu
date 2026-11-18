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
	Open     bool     // Menu declared with ...: Config.Entry adds fields
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
	op, args := expr(decl)
	if op != cue.OrOp || !classifyDisjunction(node, flattenOr(args)) {
		node.Widget = kindWidget(decl, kindOf(decl, op, args))
	}
	node.Hint = hint(node.Widget, decl)
	if node.Widget == Menu {
		_, node.Open = declaredOpen(decl)
	}
}

// classifyDisjunction handles | of structs (variants), of concrete scalars
// (choices) and of lists with null (*null | [...T]: a list that may be
// left null). Other disjunctions are classified by their kind.
func classifyDisjunction(node *Node, alts []cue.Value) bool {
	switch {
	case hasMapAlt(alts):
		node.Widget = Map
	case all(alts, isStruct):
		node.Widget = Variant
		node.Options, node.discr = variantNames(alts)
	case all(alts, isConstScalar):
		node.Widget = Choice
		for _, alt := range alts {
			node.Options = append(node.Options, format(alt))
		}
	case all(alts, isNullOrList):
		node.Widget = List
	default:
		return false
	}
	return true
}

// mapAlt returns the map of a map with default entries, such as Timoni's
// nodeSelector: *{"kubernetes.io/os": "linux"} | {[string]: string}. It
// is the one alternative with patterns; every other is a concrete struct
// that fits in it. A default that does not fit keeps it a variant.
func mapAlt(alts []cue.Value) (cue.Value, bool) {
	idx := slices.IndexFunc(alts, func(alt cue.Value) bool { return len(patterns(alt)) > 0 })
	if idx < 0 {
		return cue.Value{}, false
	}
	for other, alt := range alts {
		fits := isStruct(alt) && alt.IsConcrete() && len(patterns(alt)) == 0 && alt.Unify(alts[idx]).Err() == nil
		if other != idx && !fits {
			return cue.Value{}, false
		}
	}
	return alts[idx], true
}

func hasMapAlt(alts []cue.Value) bool {
	_, ok := mapAlt(alts)
	return ok
}

// defaultedMap is the map alternative of decl, if decl is a map with
// default entries.
func defaultedMap(decl cue.Value) (cue.Value, bool) {
	if op, args := expr(decl); op == cue.OrOp {
		return mapAlt(flattenOr(args))
	}
	return cue.Value{}, false
}

func isNullOrList(val cue.Value) bool {
	kind := val.IncompleteKind()
	return kind == cue.NullKind || kind == cue.ListKind
}

func isStruct(val cue.Value) bool { return val.IncompleteKind() == cue.StructKind }

func isConstScalar(val cue.Value) bool {
	return val.IsConcrete() && val.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0
}

// expr is val.Expr(), seeing through a reference into another package
// (pkg.Name), which Expr reports as a selector instead of what it names.
func expr(val cue.Value) (cue.Op, []cue.Value) {
	operator, args := val.Expr()
	if operator == cue.SelectorOp {
		if root, path := val.ReferencePath(); len(path.Selectors()) > 0 {
			return root.LookupPath(path).Expr()
		}
	}
	if operator == cue.AndOp {
		if alts, ok := embeddedOr(args); ok {
			return cue.OrOp, alts
		}
	}
	return operator, args
}

// embeddedOr finds the disjunction of a struct that only embeds one next
// to definitions or lets, which declare no fields: at the top of a file,
// "#A: {…}, #B: {…}, #A | #B" is {#A, #B} & (#A | #B).
func embeddedOr(args []cue.Value) ([]cue.Value, bool) {
	var alts []cue.Value
	for _, arg := range args {
		operator, sub := expr(arg)
		switch {
		case operator == cue.OrOp && alts == nil:
			alts = sub
		case operator != cue.OrOp && arg.IncompleteKind() == cue.StructKind && !declaresFields(arg):
		default:
			return nil, false
		}
	}
	return alts, alts != nil
}

// declaresFields reports whether val has regular fields or patterns;
// definitions, hidden fields and lets do not count.
func declaresFields(val cue.Value) bool {
	iter, err := val.Fields(cue.Optional(true), cue.Patterns(true))
	return err == nil && iter.Next()
}

// kindOf is the kind of val. Two cases hide it (bottom): a validator such
// as list.MinItems(1), then it is the intersection of the kinds of the
// conjuncts; and a struct whose if tests a required field not yet set,
// which still has fields.
func kindOf(val cue.Value, op cue.Op, args []cue.Value) cue.Kind {
	kind := val.IncompleteKind()
	switch {
	case kind != cue.BottomKind:
	case op == cue.AndOp:
		kind = cue.TopKind
		for _, arg := range args {
			kind &= arg.IncompleteKind()
		}
	case hasFields(val):
		kind = cue.StructKind
	}
	return kind
}

// hasFields reports whether val lists at least one field. Fields alone
// does not fail on a bottom scalar such as n + 1 with n unset.
func hasFields(val cue.Value) bool {
	iter, err := val.Fields(cue.All())
	return err == nil && iter.Next()
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
	}
	return scalarWidget(kind)
}

func scalarWidget(kind cue.Kind) Widget {
	switch {
	case kind == cue.BoolKind:
		return Toggle
	case kind != 0 && kind&^cue.NumberKind == 0:
		return Number
	case kind == cue.StringKind:
		return Text
	case kind == cue.BottomKind:
		// Computed from a required field not set yet: n + 1 with n!: int.
		// Types and validators keep a kind while incomplete.
		return ReadOnly
	}
	return Expr
}

// hint is the constraint shown next to a field that can still change.
func hint(widget Widget, decl cue.Value) string {
	switch widget {
	case Map:
		if mapVal, ok := defaultedMap(decl); ok {
			decl = mapVal
		}
		return strings.Join(patterns(decl), "\n")
	case List:
		if _, hasDefault := decl.Default(); decl.IsConcrete() && !hasDefault {
			return ""
		}
	case ReadOnly:
		if !decl.IsConcrete() { // computed, waiting: show the formula
			return format(decl)
		}
		return ""
	case Menu, Variant:
		return ""
	case Toggle, Choice, Number, Text, Expr:
	}
	return format(decl)
}

// flattenOr expands nested disjunctions: *("x" | "y") | "z".
func flattenOr(args []cue.Value) []cue.Value {
	var out []cue.Value
	for _, arg := range args {
		if op, sub := expr(arg); op == cue.OrOp {
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
