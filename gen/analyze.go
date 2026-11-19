// Package gen turns a CUE schema into the Go types that receive the
// configuration it describes: JSON and YAML decoding with the schema's
// defaults, and validation with go-playground/validator tags where a tag
// can say it, generated code where not. What it cannot translate is
// reported, never silently accepted as valid.
package gen

import (
	"fmt"
	"slices"
	"strings"

	"cuelang.org/go/cue"

	"github.com/Drathveloper/cuemenu/model"
)

// kind is what a Go type is built from.
type kind int

const (
	kAny kind = iota
	kBool
	kInt
	kFloat
	kString
	kBytes
	kStruct
	kUnion
	kList
	kMap
)

// Names of JSON kinds and Go types, of the helpers the generated code may
// need (generator.uses), and tags used more than once.
const (
	typeBool   = "bool"
	typeInt    = "int"
	typeString = "string"
	jsonNull   = "null"
	jsonNumber = "number"
	jsonObject = "object"
	jsonArray  = "array"
	litTrue    = "true"
	litFalse   = "false"

	helperPtr       = "ptr"
	helperFromJSON  = "fromJSON"
	helperCheckKeys = "checkKeys"
	helperPick      = "pickVariant"
	helperChecker   = "checker"
	helperDuration  = "duration"
	helperTime      = "time"
	helperUnionList = "unionList"
	helperIsKind    = "isKind"
	helperCount     = "count"
	helperURL       = "url"
	helperRest      = "rest"
	pkgStrings      = "strings"

	tagUnsigned = "gte=0"
)

// ref is the type of a value: a scalar, a named struct or union, or a
// list or map of another ref, with what validates it.
type ref struct {
	named    *named
	elem     *ref     // list and map elements
	key      *ref     // map keys (only their checks and tags)
	tags     []string // validator tags of this value
	checks   []check  // generated checks of this value
	values   []string // enum: its literals
	kind     kind
	min      int // list: elements required by the schema
	nullable bool
}

// check is generated code: cond, with %[1]s standing for the value,
// is true when the value is invalid; msg says why.
type check struct{ cond, msg string }

// named is a generated struct, or a union of structs (a Go interface).
type named struct {
	name     string
	doc      string
	discr    string // union: JSON key whose value picks the variant
	fields   []*field
	variants []*named // union
	tells    []string // union: per variant, its discriminator value (JSON)
	oneOfs   []oneOf  // keys of which one must be given
	def      int      // union: index of the default variant, -1 none
	closed   bool     // unknown keys are an error
	isUnion  bool
}

type field struct {
	ref      *ref
	cond     *cond // exists only for some values of another field
	goName   string
	json     string
	doc      string
	def      string // Go expression of the default, "" if none
	ptr      bool   // *T: may be absent
	required bool
	computed bool
	rest     bool // the keys the struct does not declare, by its pattern
}

type generator struct {
	defs    map[string]*named // definitions already generated, by position
	names   map[string]bool
	regexps map[string]string // pattern -> Go variable
	uses    map[string]bool   // helpers the code needs
	opts    Options
	root    cue.Value // what the code is generated for
	types   []*named
	warns   []string
}

// maxDepth is how deep types are generated: a recursive schema that is
// not a definition referenced by name would never end.
const maxDepth = 32

// Options of the generated code.
type Options struct {
	Package string // package clause
	Type    string // root type
	Koanf   bool   // also Load, from a github.com/knadh/koanf/v2 instance
}

func (g *generator) warnf(path cue.Path, format string, args ...any) {
	where := path.String()
	if where == "" {
		where = "(root)"
	}
	g.warns = append(g.warns, where+": "+fmt.Sprintf(format, args...))
}

// typeOf returns the type of val, found at path; name is the Go name a
// struct gets if it is no definition.
func (g *generator) typeOf(val cue.Value, path cue.Path, name string) *ref {
	out := g.typeOfValue(val, path, name)
	out.nullable = kindOf(val)&cue.NullKind != 0
	return out
}

func (g *generator) typeOfValue(val cue.Value, path cue.Path, name string) *ref {
	if len(path.Selectors()) > maxDepth {
		g.warnf(path, "too deep (a recursive schema?): any value is accepted")
		return &ref{kind: kAny}
	}
	if all, ok := model.AllOf(val); ok {
		return g.allOf(val, all, path, name)
	}
	if alts, ok := structAlts(val); ok {
		if len(alts) == 1 { // null | {…}
			return g.typeOf(alts[0], path, name)
		}
		return &ref{kind: kUnion, named: g.union(val, alts, path, name)}
	}
	if alts, ok := model.Disjuncts(val); ok && mixed(alts) {
		return g.mixedKinds(alts, path)
	}
	switch kindOf(val) &^ cue.NullKind {
	case cue.StructKind:
		return g.structRef(val, path, name)
	case cue.ListKind:
		return g.listRef(val, path, name)
	default:
		return g.leaf(val, path)
	}
}

// mixedKinds is a value of the kinds of alternatives of several kinds.
func (g *generator) mixedKinds(alts []cue.Value, path cue.Path) *ref {
	names := make([]string, 0, len(alts))
	for _, alt := range alts {
		names = append(names, cueKinds(kindOf(alt))...)
	}
	return g.ofKinds(names, path)
}

// allOf is the struct JSON Schema's allOf requires of val:
// matchN(2, [#container_spec, #workload_spec]). It is closed as val is:
// the parts unified lose the close() around them.
func (g *generator) allOf(val, all cue.Value, path cue.Path, name string) *ref {
	out := g.typeOfValue(all, path, name)
	if out.kind == kStruct && !isOpen(val) {
		out.named.closed = true
	}
	return out
}

// structAlts returns the alternatives of a disjunction of structs, null
// aside.
func structAlts(val cue.Value) ([]cue.Value, bool) {
	alts, ok := model.Disjuncts(val)
	if !ok {
		return nil, false
	}
	alts = slices.DeleteFunc(slices.Clone(alts), func(alt cue.Value) bool { return kindOf(alt) == cue.NullKind })
	for _, alt := range alts {
		if kindOf(alt) != cue.StructKind {
			return nil, false
		}
	}
	return alts, len(alts) > 0
}

// mixed: the alternatives are structs or lists, and something else.
func mixed(alts []cue.Value) bool {
	var all cue.Kind
	for _, alt := range alts {
		all |= kindOf(alt) &^ cue.NullKind
	}
	composite := all & (cue.StructKind | cue.ListKind)
	return composite != 0 && all != composite && all != cue.StructKind && all != cue.ListKind ||
		composite == cue.StructKind|cue.ListKind
}

// kindOf is the kind of val, also when it is an error not complete yet
// (a matchN next to a struct, an if on a field not given): the kind of
// its parts, or a struct if it has fields.
func kindOf(val cue.Value) cue.Kind {
	kind := val.IncompleteKind()
	if kind == cue.TopKind { // matchN(…) is _ to CUE: its alternatives say more
		if alts, ok := model.Disjuncts(val); ok {
			kind = anyKind(alts)
		}
	}
	if kind != cue.BottomKind {
		return kind
	}
	operator, args := exprOf(val)
	switch operator {
	case cue.AndOp:
		kind = cue.TopKind
		for _, arg := range args {
			kind &= kindOf(arg)
		}
		return kind
	case cue.OrOp:
		return anyKind(args)
	default:
		if iter, err := val.Fields(cue.Optional(true), cue.Patterns(true)); err == nil && iter.Next() {
			return cue.StructKind
		}
		return cue.BottomKind
	}
}

// anyKind is the kinds any of alts may have.
func anyKind(alts []cue.Value) cue.Kind {
	kind := cue.BottomKind
	for _, alt := range alts {
		kind |= kindOf(alt)
	}
	return kind
}

// exprOf is val.Expr(), through a reference: Expr of #registry is the
// reference, not what it names.
func exprOf(val cue.Value) (cue.Op, []cue.Value) {
	operator, args := val.Expr()
	if operator == cue.SelectorOp {
		if root, path := val.ReferencePath(); len(path.Selectors()) > 0 {
			return root.LookupPath(path).Expr()
		}
	}
	return operator, args
}

func (g *generator) structRef(val cue.Value, path cue.Path, name string) *ref {
	if elem, ok := mapElem(val); ok {
		return g.mapRef(val, elem, path, name)
	}
	return &ref{kind: kStruct, named: g.structType(val, path, name)}
}

// structType returns the named struct of val, the same one for every use
// of a definition.
func (g *generator) structType(val cue.Value, path cue.Path, name string) *named {
	key, defName := defKey(val)
	if key == "" {
		key = "text:" + fullText(val) // the same struct again: a recursive one, or repeated
	}
	if found, ok := g.defs[key]; ok {
		return found
	}
	if defName != "" {
		name = defName
	}
	out := &named{name: g.unique(name), doc: doc(val), closed: !isOpen(val)}
	g.defs[key] = out
	g.types = append(g.types, out)
	prefix := out.name
	if len(path.Selectors()) == 0 {
		prefix = "" // top-level types are named after their field alone
	}
	goNames := map[string]bool{}
	iter, err := val.Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		sel := iter.Selector()
		fld := g.field(iter.Value(), sel, child(path, sel), prefix)
		fld.goName = uniqueIn(goNames, fld.goName)
		out.fields = append(out.fields, fld)
	}
	g.conditionals(val, path, out, prefix, goNames)
	g.oneOfs(val, path, out)
	g.restField(val, path, out, goNames)
	return out
}

// restField keeps the keys a struct with fields does not declare but its
// pattern allows (Helm's annotations: artifacthub.io/… and any other) in
// a map, decoded and written back with the struct.
func (g *generator) restField(val cue.Value, path cue.Path, out *named, goNames map[string]bool) {
	var keys, values []cue.Value
	iter, err := val.Fields(cue.Patterns(true))
	for err == nil && iter.Next() {
		if sel := iter.Selector(); sel.ConstraintType() == cue.PatternConstraint {
			keys, values = append(keys, sel.Pattern()), append(values, iter.Value())
		}
	}
	switch {
	case len(keys) == 0:
		return
	case len(keys) > 1:
		g.warnf(path, "several patterns next to fields: the keys they allow are not kept")
		return
	}
	elem := g.typeOf(values[0], cue.MakePath(append(path.Selectors(), cue.AnyString)...), out.name+"Value")
	if elem.kind == kUnion {
		g.warnf(path, "a pattern of variants next to fields: the keys it allows are not kept")
		return
	}
	rest := &ref{kind: kMap, elem: elem}
	if out.closed {
		rest.key = g.leaf(keys[0], path)
	} else if fmt.Sprint(keys[0]) != typeString {
		g.warnf(path, "values of keys that do not match the pattern are checked too")
	}
	out.fields = append(out.fields, &field{
		ref: rest, goName: uniqueIn(goNames, "Rest"), rest: true,
		doc: "Rest holds the fields the schema allows by the pattern [" + fmt.Sprint(keys[0]) + "] and does not declare.",
	})
}

// defKey identifies the definition val is, if it is one: by where it is
// declared, so two packages with a #Settings each stay apart.
func defKey(val cue.Value) (string, string) {
	root, path := val.ReferencePath()
	sels := path.Selectors()
	if len(sels) == 0 || !sels[len(sels)-1].IsDefinition() {
		return "", ""
	}
	decl := root.LookupPath(path)
	if decl.Pos().String() == "-" {
		return "", ""
	}
	return decl.Pos().String(), goName(strings.TrimPrefix(sels[len(sels)-1].String(), "#"))
}

func (g *generator) field(val cue.Value, sel cue.Selector, path cue.Path, owner string) *field {
	label := sel.Unquoted()
	out := &field{json: label, goName: goName(label), doc: doc(val)}
	if g.opts.Koanf && strings.Contains(label, ".") {
		g.warnf(path, "koanf splits keys at \".\": load with another delimiter")
	}
	out.ref = g.typeOf(val, path, owner+out.goName)
	operator, _ := val.Expr()
	switch {
	case g.isComputed(val, operator): // a pointer: what is not given stays out
		out.computed, out.ptr = true, needsPtr(out.ref)
		g.warnf(path, "computed by the schema: decoded, not checked")
	case sel.ConstraintType() == cue.OptionalConstraint:
		out.ptr = needsPtr(out.ref)
	case sel.ConstraintType() == cue.RequiredConstraint:
		out.ptr, out.required = needsPtr(out.ref), true
	default:
		g.regular(out, val, path)
	}
	return out
}

// regular sets up a field with no ! or ?: with a default it may be
// absent; without one it must be given, unless it is a struct or a list,
// which exist already (empty).
func (g *generator) regular(out *field, val cue.Value, path cue.Path) {
	switch out.ref.kind {
	case kUnion:
		g.regularUnion(out)
	case kStruct:
	case kList, kMap:
		if def, hasDef := val.Default(); hasDef && def.IsConcrete() {
			out.def = g.literal(def, out.ref, path)
		}
	default:
		g.regularScalar(out, val, path)
	}
}

// regularUnion defaults to the default variant, else must be given.
func (g *generator) regularUnion(out *field) {
	variant := out.ref.named.def
	if variant < 0 {
		out.required = true
		return
	}
	g.uses[helperPtr] = true
	out.def = "ptr(New" + out.ref.named.variants[variant].name + "())"
}

func (g *generator) regularScalar(out *field, val cue.Value, path cue.Path) {
	nullable := val.IncompleteKind()&cue.NullKind != 0
	def, hasDef := val.Default()
	if !hasDef && val.IsConcrete() {
		def, hasDef = val, true
	}
	if !hasDef || !def.IsConcrete() || def.Kind() == cue.NullKind {
		out.ptr, out.required = needsPtr(out.ref), !nullable && !hasDef
		return
	}
	out.def = g.literal(def, out.ref, path)
	if nullable {
		out.ptr = true
		out.def = "ptr(" + out.def + ")"
		g.uses[helperPtr] = true
	}
}

// isComputed: the value is an expression over other fields, such as
// "\(protocol)://\(domain)/", not a type or a constant.
func (g *generator) isComputed(val cue.Value, operator cue.Op) bool {
	if operator == cue.InterpolationOp || forList(val) || g.isFieldRef(val, operator) {
		return true
	}
	scalar := val.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0
	return scalar && val.IsConcrete() && operator != cue.NoOp && operator != cue.OrOp && operator != cue.AndOp
}

// isFieldRef: val is another field of the configuration (Holos' name:
// Name in #Helm), so the schema computes it. A reference out of it is a
// type (Grafana's type: ScaleDistribution next to the FieldConfig the
// code is for), as one to a definition is. The path of a reference is
// relative to the struct it is in, which is the root here.
func (g *generator) isFieldRef(val cue.Value, operator cue.Op) bool {
	if operator == cue.AndOp { // string & Name, #Path & "\(Name).yaml"
		_, args := val.Expr()
		return slices.ContainsFunc(args, func(arg cue.Value) bool {
			op, _ := arg.Expr()
			return op == cue.InterpolationOp || op == cue.SelectorOp && g.isFieldRef(arg, op)
		})
	}
	if operator != cue.SelectorOp {
		return false
	}
	_, path := val.ReferencePath()
	sels := path.Selectors()
	if len(sels) == 0 || slices.ContainsFunc(sels, func(sel cue.Selector) bool { // #."relative-path-modes"
		return sel.LabelType() != cue.StringLabel
	}) {
		return false
	}
	return g.root.LookupPath(path).Exists()
}

// needsPtr: a value that may be absent needs a pointer to tell absent
// from zero, except lists and maps (nil) and interfaces.
func needsPtr(r *ref) bool {
	return r.kind != kList && r.kind != kMap && r.kind != kUnion && r.kind != kAny
}

// mapElem returns the value of the only pattern of a struct with no
// fields: {[string]: T} is a map[string]T.
func mapElem(val cue.Value) (cue.Value, bool) {
	iter, err := val.Fields(cue.Optional(true))
	if err == nil && iter.Next() {
		return cue.Value{}, false
	}
	var found []cue.Value
	iter, err = val.Fields(cue.Patterns(true))
	for err == nil && iter.Next() {
		if iter.Selector().ConstraintType() == cue.PatternConstraint {
			found = append(found, iter.Value())
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	if len(found) == 0 && isOpen(val) {
		return cue.Value{}, true // {...}: any keys, any values
	}
	return cue.Value{}, false
}

// conjuncts are the parts of val's conjunction, nested ones included.
func conjuncts(val cue.Value) []cue.Value {
	operator, args := val.Expr()
	if operator != cue.AndOp {
		return []cue.Value{val}
	}
	var out []cue.Value
	for _, arg := range args {
		out = append(out, conjuncts(arg)...)
	}
	return out
}

// isOpen: val allows fields it does not declare. Next to a validator
// such as matchN, Allows is wrong either way (kyverno's match is open,
// Dependabot's root closed): there a field is filled in to find out.
func isOpen(val cue.Value) bool {
	const unknown = "\x00unknown"
	if !slices.ContainsFunc(conjuncts(val), func(part cue.Value) bool {
		op, _ := part.Expr()
		return op == cue.CallOp
	}) {
		return val.Allows(cue.Str(unknown))
	}
	// CUE v0.17.1 panics validating the fill in some schemas (compose-spec:
	// index out of range in its typo check); Allows is the best left there.
	err, panicked := error(nil), true
	func() {
		defer func() { _ = recover() }()
		err = val.FillPath(cue.MakePath(cue.Str(unknown)), struct{}{}).Validate()
		panicked = false
	}()
	if panicked {
		return val.Allows(cue.Str(unknown))
	}
	return err == nil || !strings.Contains(err.Error(), "not allowed")
}

func (g *generator) mapRef(val, elem cue.Value, path cue.Path, name string) *ref {
	out := &ref{kind: kMap, elem: &ref{kind: kAny}}
	g.listBuiltins(val, out, path)
	if !elem.Exists() {
		if hasFieldsOrPatterns(val) {
			g.warnf(path, "several patterns: values are not checked")
		}
		return out
	}
	out.elem = g.typeOf(elem, cue.MakePath(append(path.Selectors(), cue.AnyString)...), name)
	if isOpen(val) {
		if keyed(val) {
			g.warnf(path, "values of keys that do not match the pattern are checked too")
		}
		return out // open: keys outside the pattern are allowed
	}
	iter, err := val.Fields(cue.Patterns(true))
	for err == nil && iter.Next() {
		if sel := iter.Selector(); sel.ConstraintType() == cue.PatternConstraint {
			out.key = g.leaf(sel.Pattern(), path)
		}
	}
	return out
}

// keyed: the pattern of a map is more than any string.
func keyed(val cue.Value) bool {
	iter, err := val.Fields(cue.Patterns(true))
	for err == nil && iter.Next() {
		if sel := iter.Selector(); sel.ConstraintType() == cue.PatternConstraint &&
			fmt.Sprint(sel.Pattern()) != typeString {
			return true
		}
	}
	return false
}

func hasFieldsOrPatterns(val cue.Value) bool {
	iter, err := val.Fields(cue.Optional(true), cue.Patterns(true))
	return err == nil && iter.Next()
}

func (g *generator) listRef(val cue.Value, path cue.Path, name string) *ref {
	list := val
	if elem := list.LookupPath(cue.MakePath(cue.AnyIndex)); !elem.Exists() {
		list = listPart(val)
	}
	out := &ref{kind: kList, elem: &ref{kind: kAny}}
	if elem := list.LookupPath(cue.MakePath(cue.AnyIndex)); elem.Exists() {
		out.elem = g.typeOf(elem, cue.MakePath(append(path.Selectors(), cue.AnyIndex)...), name)
	} else if list.Exists() {
		g.warnf(path, "a list of fixed length: elements are not checked")
	}
	out.min = fixedLen(list)
	g.listBuiltins(val, out, path)
	if out.min > 0 && !slices.ContainsFunc(out.tags, func(t string) bool { return strings.HasPrefix(t, "min=") }) {
		out.tags = append(out.tags, fmt.Sprintf("min=%d", out.min))
	}
	return out
}

// fixedLen is how many elements list has before its ellipsis.
func fixedLen(list cue.Value) int {
	n := 0
	for list.LookupPath(cue.MakePath(cue.Index(n))).Exists() {
		n++
	}
	return n
}

// listPart is the list type in val's expression: the one without the
// default in *[a] | [...T], or the one next to list.MinItems(n).
func listPart(val cue.Value) cue.Value {
	operator, args := val.Expr()
	if operator == cue.NoOp && len(args) == 1 && !args[0].Equals(val) {
		return listPart(args[0])
	}
	for _, arg := range args {
		if arg.LookupPath(cue.MakePath(cue.AnyIndex)).Exists() {
			return arg
		}
	}
	for _, arg := range args {
		if operator == cue.AndOp || operator == cue.OrOp {
			if found := listPart(arg); found.Exists() {
				return found
			}
		}
	}
	return cue.Value{}
}

// listBuiltins turns list.MinItems(n) and list.MaxItems(n) into tags,
// and struct.MinFields(n) and struct.MaxFields(n) of a map.
func (g *generator) listBuiltins(val cue.Value, out *ref, path cue.Path) {
	operator, args := exprOf(val)
	if operator != cue.AndOp {
		return
	}
	for _, arg := range args {
		if op, _ := arg.Expr(); op != cue.CallOp {
			continue
		}
		text := fmt.Sprint(arg)
		var num int
		switch {
		case scan(text, "list.MinItems(%d)", &num), scan(text, "struct.MinFields(%d)", &num):
			out.tags = append(out.tags, fmt.Sprintf("min=%d", num))
		case scan(text, "list.MaxItems(%d)", &num), scan(text, "struct.MaxFields(%d)", &num):
			out.tags = append(out.tags, fmt.Sprintf("max=%d", num))
		case strings.HasPrefix(text, "list.UniqueItems("):
			out.tags = append(out.tags, "unique")
		default:
			g.warnf(path, "%s is not checked", text)
		}
	}
}

func scan(text, format string, num *int) bool {
	n, err := fmt.Sscanf(text, format, num)
	return err == nil && n == 1
}

// union is the Go interface for a disjunction of structs, told apart by
// a field with a different constant in each, or else by a field only one
// of them has.
func (g *generator) union(val cue.Value, alts []cue.Value, path cue.Path, name string) *named {
	out := &named{name: g.unique(name), doc: doc(val), isUnion: true, def: -1}
	g.types = append(g.types, out)
	alts = g.variants(out, val, alts, path, name)
	if discr, vals, ok := discriminator(alts); ok {
		out.discr, out.tells = discr, vals
		return out
	}
	return out
}

// variants sets the variants of out, one per alternative but the same
// one again, and its default; it returns the alternatives kept.
func (g *generator) variants(out *named, val cue.Value, alts []cue.Value, path cue.Path, name string) []cue.Value {
	def, hasDef := val.Default()
	kept := make([]cue.Value, 0, len(alts))
	for idx, alt := range alts {
		variant := g.structType(alt, path, fmt.Sprintf("%sVariant%d", name, idx+1))
		if slices.Contains(out.variants, variant) {
			continue // the same alternative again
		}
		if hasDef && out.def < 0 && def.Subsume(alt) == nil && alt.Subsume(def) == nil {
			out.def = len(kept)
		}
		out.variants, kept = append(out.variants, variant), append(kept, alt)
	}
	return kept
}

// discriminator finds a field that every alternative fixes to a
// different constant.
func discriminator(alts []cue.Value) (string, []string, bool) {
	iter, err := alts[0].Fields()
	for err == nil && iter.Next() {
		label := iter.Selector().Unquoted()
		vals := make([]string, 0, len(alts))
		for _, alt := range alts {
			fld := alt.LookupPath(cue.MakePath(cue.Str(label)))
			data, jerr := fld.MarshalJSON()
			if !fld.IsConcrete() || jerr != nil || slices.Contains(vals, string(data)) {
				break
			}
			vals = append(vals, string(data))
		}
		if len(vals) == len(alts) {
			return label, vals, true
		}
	}
	return "", nil, false
}

func (g *generator) unique(name string) string {
	if name == "" {
		name = "Value"
	}
	out := name
	for n := 2; g.names[out]; n++ {
		out = fmt.Sprintf("%s%d", name, n)
	}
	g.names[out] = true
	return out
}

func uniqueIn(seen map[string]bool, name string) string {
	out := name
	for n := 2; seen[out]; n++ {
		out = fmt.Sprintf("%s%d", name, n)
	}
	seen[out] = true
	return out
}

// fullText identifies val by its text, with its fields and patterns, a
// few levels deep: %v leaves optional fields out, and %#v panics on some
// schemas (let clauses with names that are no identifiers).
func fullText(val cue.Value) string {
	var out strings.Builder
	fingerprint(&out, val, 0)
	return out.String()
}

func fingerprint(out *strings.Builder, val cue.Value, depth int) {
	const levels = 4
	fmt.Fprintf(out, "%v", val)
	if depth == levels {
		return
	}
	indent := "\n" + strings.Repeat("\t", depth)
	if alts, ok := model.Disjuncts(val); ok {
		for _, alt := range alts {
			out.WriteString(indent + "| ")
			fingerprint(out, alt, depth+1)
		}
		return
	}
	kind := kindOf(val)
	if kind&cue.ListKind != 0 {
		out.WriteString(indent + "[_]: ")
		fingerprint(out, val.LookupPath(cue.MakePath(cue.AnyIndex)), depth+1)
	}
	if kind&cue.StructKind == 0 {
		return
	}
	iter, err := val.Fields(cue.Optional(true), cue.Patterns(true))
	for err == nil && iter.Next() {
		fmt.Fprintf(out, "%s%v: ", indent, iter.Selector())
		fingerprint(out, iter.Value(), depth+1)
	}
}

func child(parent cue.Path, sel cue.Selector) cue.Path {
	return cue.MakePath(append(parent.Selectors(), sel)...)
}

func doc(val cue.Value) string {
	groups := val.Doc()
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		parts = append(parts, strings.TrimSpace(group.Text()))
	}
	return strings.Join(parts, "\n")
}
