// Package model turns an evaluated CUE value into a menu tree.
// All CUE semantics are delegated to cuelang.org/go; this package only
// decides which widget each field gets.
package model

import (
	"fmt"
	"slices"
	"strconv"
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
	Missing  bool     // required and not given yet
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
	// Validate, not Err: a value still incomplete is no error, it is what
	// the user fills in (e.g. if kind == "a" {…} before kind is chosen).
	if err := schema.Validate(); err != nil {
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
	case all(alts, func(alt cue.Value) bool { return alt.IncompleteKind() == cue.BoolKind }):
		node.Widget = Toggle // *false | true
	case all(alts, isConstScalar):
		node.Widget = Choice
		for _, alt := range alts {
			node.Options = append(node.Options, format(alt))
		}
	case all(alts, isNullOrList):
		node.Widget = List
	case untypedStruct(alts):
		node.Widget = Menu
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

// untypedStruct: any JSON value, but a struct with these fields if it is
// one. cue import makes it of a JSON Schema with properties but no
// "type": null | bool | number | string | [...] | {…} (docker-compose's
// services). It is edited as the struct, like null | T as T.
func untypedStruct(alts []cue.Value) bool {
	structs, kinds := 0, 0
	for _, alt := range alts {
		switch {
		case isStruct(alt):
			structs++
		case anyOfKind(alt):
			kinds++
		case alt.IncompleteKind() != cue.NullKind:
			return false
		}
	}
	return structs == 1 && kinds >= minUntyped
}

// minUntyped: as many of bool, number, string and [...] next to a struct
// make it untyped; fewer are a real choice (codecov's status: bool | {…}).
const minUntyped = 3

// anyOfKind: any value of a kind, unconstrained: bool, number, string,
// [...].
func anyOfKind(val cue.Value) bool {
	return slices.Contains([]string{"bool", "number", "int", "float", "string", "[...]", "[..._]"}, format(val))
}

func isStruct(val cue.Value) bool { return val.IncompleteKind() == cue.StructKind }

func isConstScalar(val cue.Value) bool {
	return val.IsConcrete() && val.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0
}

// expr is val.Expr(), seeing through a reference into another package
// (pkg.Name), which Expr reports as a selector instead of what it names.
func expr(val cue.Value) (cue.Op, []cue.Value) {
	operator, args := rawExpr(val)
	if operator == cue.AndOp {
		if alts, ok := embeddedOr(args); ok {
			return cue.OrOp, alts
		}
	}
	if operator == cue.CallOp {
		if alts, ok := matchAny(args); ok {
			return cue.OrOp, alts
		}
	}
	return operator, args
}

// matchAll returns the parts of matchN(N, [A, B]) with N of them all:
// JSON Schema's allOf (docker-compose's service is a container spec and
// a workload spec).
func matchAll(val cue.Value) ([]cue.Value, bool) {
	const matchArgs = 3
	operator, args := rawExpr(val)
	if operator != cue.CallOp || len(args) != matchArgs || format(args[0]) != "matchN" {
		return nil, false
	}
	var parts []cue.Value
	iter, err := args[2].List()
	for err == nil && iter.Next() {
		parts = append(parts, iter.Value())
	}
	return parts, format(args[1]) == strconv.Itoa(len(parts))
}

// allOfStruct is the struct that the allOf parts of decl require:
// matchN(N, [A, B]) only checks them, so their fields are not in the
// value (docker-compose's service: its container and workload specs).
func allOfStruct(decl cue.Value) (cue.Value, bool) {
	var out cue.Value
	for _, part := range allOfParts(decl) {
		if part = nonNull(part); isStruct(part) {
			out = unifyAll(out, part)
		}
	}
	return out, out.Exists()
}

func allOfParts(decl cue.Value) []cue.Value {
	operator, args := rawExpr(decl)
	if operator == cue.AndOp {
		out := make([]cue.Value, 0, len(args))
		for _, arg := range args {
			out = append(out, allOfParts(arg)...)
		}
		return out
	}
	if parts, ok := matchAll(decl); ok {
		return parts
	}
	return nil
}

func unifyAll(acc, val cue.Value) cue.Value {
	if !acc.Exists() {
		return val
	}
	return acc.Unify(val)
}

// rawExpr is val.Expr(), through a reference to another package.
func rawExpr(val cue.Value) (cue.Op, []cue.Value) {
	operator, args := val.Expr()
	if operator == cue.SelectorOp {
		if root, path := val.ReferencePath(); len(path.Selectors()) > 0 {
			return root.LookupPath(path).Expr()
		}
	}
	return operator, args
}

// validatesOnly: decl is a matchN, which checks a value against its
// alternatives without unifying it with them.
func validatesOnly(decl cue.Value) bool {
	operator, args := rawExpr(decl)
	if operator == cue.AndOp { // embedded: {@jsonschema(…), matchN(…)}
		return slices.ContainsFunc(args, validatesOnly)
	}
	_, ok := matchAny(args)
	return operator == cue.CallOp && ok
}

// matchAny returns the alternatives of matchN(1, [A, B]) or matchN(>=1,
// [A, B]), JSON Schema's oneOf and anyOf as imported by cue: a value that
// matches one of them, edited like A | B. args are those of the call:
// the function, n and the list.
func matchAny(args []cue.Value) ([]cue.Value, bool) {
	const matchArgs = 3
	if len(args) != matchArgs || format(args[0]) != "matchN" {
		return nil, false
	}
	count := format(args[1])
	if count != "1" && count != ">=1" {
		return nil, false
	}
	var alts []cue.Value
	iter, err := args[2].List()
	for err == nil && iter.Next() {
		alts = append(alts, iter.Value())
	}
	return alts, len(alts) > 0
}

// neutral: a part of a conjunction that adds no fields or kind to a
// disjunction next to it: a definition with no fields, or a validator
// that declares none (matchN(2, …), not close({…}), which has the fields
// of GitHub Actions' steps).
func neutral(arg cue.Value, operator cue.Op) bool {
	return (isStruct(arg) || operator == cue.CallOp) && !declaresFields(arg)
}

// embeddedOr finds the one disjunction of a conjunction whose other parts
// add nothing to it:
//   - definitions or lets next to it, which declare no fields: at the top
//     of a file, "#A: {…}, #B: {…}, #A | #B" is {#A, #B} & (#A | #B);
//   - a type every alternative has, as in Go-generated enums:
//     string & ("a" | "b");
//   - the same disjunction again: a list element's field, when every
//     alternative of an undecided variant declares the list.
func embeddedOr(args []cue.Value) ([]cue.Value, bool) {
	var alts, types []cue.Value
	for _, arg := range args {
		operator, sub := expr(arg)
		switch {
		case operator == cue.OrOp:
			if alts != nil && !sameValues(alts, sub) {
				return nil, false
			}
			alts = sub // the same one again: two alternatives of the root declare it
		case neutral(arg, operator):
		case isScalarType(arg):
			types = append(types, arg)
		default:
			return nil, false
		}
	}
	if !haveTypes(flattenOr(alts), types) {
		return nil, false
	}
	return alts, alts != nil
}

func sameValues(a, b []cue.Value) bool {
	return slices.EqualFunc(a, b, func(x, y cue.Value) bool { return format(x) == format(y) })
}

// isScalarType reports whether val is a type such as string or int.
func isScalarType(val cue.Value) bool {
	return !val.IsConcrete() && val.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0
}

// haveTypes reports whether every alternative has all the types.
func haveTypes(alts, types []cue.Value) bool {
	for _, typ := range types {
		if !all(alts, func(alt cue.Value) bool { return alt.Unify(typ).Err() == nil }) {
			return false
		}
	}
	return true
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
func kindOf(val cue.Value, operator cue.Op, args []cue.Value) cue.Kind {
	kind := val.IncompleteKind()
	if kind == cue.TopKind && operator == cue.OrOp { // matchN: any value, but checked against these
		kind = cue.BottomKind
		for _, arg := range flattenOr(args) {
			kind |= arg.IncompleteKind()
		}
	}
	if kind == cue.BottomKind && operator == cue.AndOp {
		kind = cue.TopKind
		for _, arg := range args {
			kind &= arg.IncompleteKind()
		}
	}
	if kind == cue.BottomKind && hasFields(val) {
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
	if kind != cue.NullKind { // null | T, a Go pointer: edited as T
		kind &^= cue.NullKind
	}
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
// listHint: the type of a list is worth showing, unless the list is fixed
// or recursive (it prints as [..._|_]).
func listHint(decl cue.Value) bool {
	_, hasDefault := decl.Default()
	fixed := decl.IsConcrete() && !hasDefault
	return !fixed && !strings.Contains(format(decl), "_|_")
}

func hint(widget Widget, decl cue.Value) string {
	switch widget {
	case Map:
		if mapVal, ok := defaultedMap(decl); ok {
			decl = mapVal
		}
		return strings.Join(patterns(decl), "\n")
	case List:
		if !listHint(decl) {
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
// Final drops what is not data, e.g. an open list prints [] not [...string],
// and the attributes of the schema (@go) are left out.
func leafValue(val cue.Value) string {
	if def, ok := val.Default(); ok && !val.IsConcrete() {
		val = def
	}
	if !val.IsConcrete() {
		return ""
	}
	var text []byte
	var err error
	if !safe(func() { text, err = cueformat.Node(val.Syntax(cue.Final(), cue.Attributes(false))) }) || err != nil {
		if data, jerr := val.MarshalJSON(); jerr == nil { // JSON is CUE too
			return string(data)
		}
		return format(val)
	}
	return string(text)
}

// safe runs export and reports whether it finished: CUE's exporter panics
// on some values of GitHub Actions' schema (quoted definitions, #."x").
func safe(export func()) bool {
	done := false
	func() {
		defer func() { _ = recover() }()
		export()
		done = true
	}()
	return done
}

// variantNames labels each struct variant by a field holding a distinct
// concrete string in every variant (a discriminator such as "type").
// Without one, each variant is named by the fields only it has.
// requiredOf keeps the names of fields that val requires (x!).
func requiredOf(val cue.Value, names []string) []string {
	var out []string
	iter, err := val.Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		if sel := iter.Selector(); sel.ConstraintType() == cue.RequiredConstraint && slices.Contains(names, sel.Unquoted()) {
			out = append(out, sel.Unquoted())
		}
	}
	return out
}

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
		own := ownFields(alts, idx)
		if required := requiredOf(alts[idx], own); len(required) > 0 {
			own = required // GitHub Actions' jobs: "runs-on" or "uses"
		}
		names[idx] = strings.Join(own, ", ")
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

// format prints val, or nothing where CUE's exporter panics (see safe).
func format(val cue.Value) string {
	var text string
	safe(func() { text = fmt.Sprint(val) })
	return text
}

func all(vals []cue.Value, pred func(cue.Value) bool) bool {
	for _, val := range vals {
		if !pred(val) {
			return false
		}
	}
	return len(vals) > 0
}
