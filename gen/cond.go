package gen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/token"

	"github.com/Drathveloper/cuemenu/model"
)

// cond says when a field exists: for some values of another field of
// its struct (if protocol == "https" {cert_file!: string}).
type cond struct {
	ctrl      *field
	required  []string      // values of ctrl that require the field
	forbidden []string      // values of ctrl that do not allow it
	defaults  []condDefault // its defaults, by the values that give them
}

type condDefault struct {
	expr   string
	values []string
}

// occurrence is a conditional field as the values of ctrl declare it.
type occurrence struct {
	val    cue.Value      // as the first value declares it
	sel    cue.Selector   // idem
	decls  []cue.Value    // per value, its declaration
	sels   []cue.Selector // per value, its label (a!, a?, a)
	values []string       // the values of ctrl it exists for
}

// conditionals finds the fields of t that exist only for some values of
// an enum or bool field: they are filled in with each value in turn.
func (g *generator) conditionals(val cue.Value, path cue.Path, owner *named, prefix string, goNames map[string]bool) {
	if !hasComprehension(val) {
		return
	}
	base := texts(val)
	literals := compared(val)
	done := map[string]bool{}
	for _, ctrl := range slices.Clone(owner.fields) {
		domain := domainOf(ctrl, literals[ctrl.json])
		if len(domain) == 0 || ctrl.computed || ctrl.cond != nil {
			continue
		}
		keys, found := g.fillDomain(val, ctrl, domain)
		for _, key := range keys {
			if occ := found[key]; !done[key] && key != ctrl.json && varying(occ, len(domain), base[key]) {
				done[key] = true
				g.conditional(owner, ctrl, occ, path, prefix, goNames)
			}
		}
	}
}

// varying: the field occ describes is not there for every value of the
// domain, or not as base (the struct as it is) declares it.
func varying(occ *occurrence, domain int, base string) bool {
	return len(occ.values) != domain || base != text2(occ.val, occ.sel)
}

// fillDomain fills ctrl in val with each value of its domain, and returns
// the keys of the fields found, in order, and how each value declares them.
func (g *generator) fillDomain(val cue.Value, ctrl *field, domain []string) ([]string, map[string]*occurrence) {
	var keys []string
	found := map[string]*occurrence{}
	for _, lit := range domain {
		filled := val.FillPath(cue.MakePath(cue.Str(ctrl.json)), goValue(lit))
		g.collect(filled, lit, found, &keys)
	}
	return keys, found
}

func (g *generator) collect(filled cue.Value, lit string, found map[string]*occurrence, keys *[]string) {
	iter, err := filled.Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		key := iter.Selector().Unquoted()
		occ, ok := found[key]
		if !ok {
			occ = &occurrence{val: iter.Value(), sel: iter.Selector()}
			found[key] = occ
			*keys = append(*keys, key)
		}
		occ.values = append(occ.values, lit)
		occ.decls = append(occ.decls, iter.Value())
		occ.sels = append(occ.sels, iter.Selector())
	}
}

// conditional makes the field occ describes exist only when ctrl has
// one of occ.values: a pointer, with its requirement and default
// checked and set by generated code.
func (g *generator) conditional(owner *named, ctrl *field, occ *occurrence, path cue.Path, prefix string,
	goNames map[string]bool,
) {
	key := occ.sel.Unquoted()
	idx := slices.IndexFunc(owner.fields, func(f *field) bool { return f.json == key })
	if idx >= 0 && owner.fields[idx].computed {
		return
	}
	shown := slices.IndexFunc(occ.decls, func(decl cue.Value) bool { return kindOf(decl) != cue.BottomKind })
	if shown < 0 {
		return // forbidden for every value
	}
	fld := g.field(occ.decls[shown], occ.sels[shown], child(path, occ.sel), prefix)
	if idx < 0 {
		fld.goName = uniqueIn(goNames, fld.goName)
		owner.fields = append(owner.fields, fld)
	} else {
		fld.goName = owner.fields[idx].goName
		owner.fields[idx] = fld
	}
	fld.ptr, fld.required, fld.def = needsPtr(fld.ref), false, ""
	fld.cond = &cond{ctrl: ctrl}
	if varies(occ.decls) {
		g.warnf(child(path, occ.sel), "its type depends on %s: checked as when %s is %s",
			ctrl.json, ctrl.json, occ.values[shown])
	}
	for idx, lit := range occ.values {
		if kindOf(occ.decls[idx]) == cue.BottomKind { // field?: _|_
			fld.cond.forbidden = append(fld.cond.forbidden, lit)
			continue
		}
		required, def := g.presence(occ.decls[idx], occ.sels[idx], fld.ref, path)
		if required {
			fld.cond.required = append(fld.cond.required, lit)
		}
		if def != "" {
			fld.cond.addDefault(def, lit)
		}
	}
}

func (c *cond) addDefault(expr, lit string) {
	for idx := range c.defaults {
		if c.defaults[idx].expr == expr {
			c.defaults[idx].values = append(c.defaults[idx].values, lit)
			return
		}
	}
	c.defaults = append(c.defaults, condDefault{expr: expr, values: []string{lit}})
}

// varies: the declarations, those not forbidden, have different types.
func varies(decls []cue.Value) bool {
	first := ""
	for _, decl := range decls {
		if kindOf(decl) == cue.BottomKind {
			continue
		}
		if text := fullText(decl); first == "" {
			first = text
		} else if text != first {
			return true
		}
	}
	return false
}

// presence tells whether a field must be given, and its default.
func (g *generator) presence(val cue.Value, sel cue.Selector, target *ref, path cue.Path) (bool, string) {
	switch sel.ConstraintType() {
	case cue.RequiredConstraint:
		return true, ""
	case cue.OptionalConstraint:
		return false, ""
	default:
		return g.regularPresence(val, target, path)
	}
}

// regularPresence is presence of a field with no ! or ?.
func (g *generator) regularPresence(val cue.Value, target *ref, path cue.Path) (bool, string) {
	switch target.kind {
	case kStruct:
		return false, "New" + target.named.name + "()"
	case kUnion:
		if target.named.def < 0 {
			return true, ""
		}
		return false, "ptr(New" + target.named.variants[target.named.def].name + "())"
	case kList, kMap:
		if def, hasDef := val.Default(); hasDef && def.IsConcrete() {
			return false, g.literal(def, target, path)
		}
		return false, ""
	default:
		return g.scalarPresence(val, target, path)
	}
}

func (g *generator) scalarPresence(val cue.Value, target *ref, path cue.Path) (bool, string) {
	def, hasDef := val.Default()
	if !hasDef && val.IsConcrete() {
		def, hasDef = val, true
	}
	if hasDef && def.IsConcrete() && def.Kind() != cue.NullKind {
		return false, g.literal(def, target, path)
	}
	return !hasDef && val.IncompleteKind()&cue.NullKind == 0, ""
}

// domainOf lists the values of an enum or bool field, as literals, and
// then those its comprehensions compare it with.
func domainOf(fld *field, compared []string) []string {
	out := slices.Clone(fld.ref.values)
	if fld.ref.kind == kBool && len(out) == 0 {
		out = []string{litTrue, litFalse}
	}
	for _, lit := range compared {
		if !slices.Contains(out, lit) {
			out = append(out, lit)
		}
	}
	return out
}

// compared returns, per field, the literals the conditions of the
// comprehensions in val compare it with: "Resources" for kind in
// if kind == "Resources" {…}, where kind can be any string.
func compared(val cue.Value) map[string][]string {
	out := map[string][]string{}
	for _, src := range sources(val, 0) {
		ast.Walk(src, func(node ast.Node) bool {
			if clause, ok := node.(*ast.IfClause); ok {
				comparisons(clause.Condition, out)
			}
			return true
		}, nil)
	}
	return out
}

// sources are the unevaluated syntax of val: its own, and that of the
// definitions it refers to and of the parts of its conjunction, where
// its comprehensions are (Raw syntax of a reference is the reference).
func sources(val cue.Value, depth int) []ast.Node {
	const maxRefs = 4
	out := []ast.Node{val.Syntax(cue.Raw())}
	if depth >= maxRefs {
		return out
	}
	if root, path := val.ReferencePath(); len(path.Selectors()) > 0 {
		out = append(out, sources(root.LookupPath(path), depth+1)...)
	}
	if operator, args := val.Expr(); operator == cue.AndOp {
		for _, arg := range args {
			out = append(out, sources(arg, depth+1)...)
		}
	}
	return out
}

func comparisons(node ast.Expr, out map[string][]string) {
	switch expr := node.(type) {
	case *ast.ParenExpr:
		comparisons(expr.X, out)
	case *ast.UnaryExpr:
		comparisons(expr.X, out)
	case *ast.BinaryExpr:
		if expr.Op == token.LAND || expr.Op == token.LOR {
			comparisons(expr.X, out)
			comparisons(expr.Y, out)
			return
		}
		comparison(expr, out)
	}
}

// comparison adds the literal of name == "lit" (or !=) to out[name].
func comparison(expr *ast.BinaryExpr, out map[string][]string) {
	name, lit := expr.X, expr.Y
	if isScalarLit(name) {
		name, lit = lit, name
	}
	ident, isIdent := name.(*ast.Ident)
	if !isIdent || expr.Op != token.EQL && expr.Op != token.NEQ || !isScalarLit(lit) {
		return
	}
	_, value := litKind(lit)
	if !slices.Contains(out[ident.Name], value) {
		out[ident.Name] = append(out[ident.Name], value)
	}
}

// goValue is the Go value of a literal, to fill into CUE.
func goValue(lit string) any {
	if str, err := strconv.Unquote(lit); err == nil {
		return str
	}
	if num, err := strconv.ParseInt(lit, 10, 64); err == nil {
		return num
	}
	if b, err := strconv.ParseBool(lit); err == nil {
		return b
	}
	num, _ := strconv.ParseFloat(lit, 64)
	return num
}

// texts maps each field of val to its declaration as text.
func texts(val cue.Value) map[string]string {
	out := map[string]string{}
	iter, err := val.Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		out[iter.Selector().Unquoted()] = text2(iter.Value(), iter.Selector())
	}
	return out
}

func text2(val cue.Value, sel cue.Selector) string {
	return fmt.Sprintf("%v %s", sel.ConstraintType(), fullText(val))
}

// forList: val is a list made by a comprehension over other fields,
// [for k, v in components {v}].
func forList(val cue.Value) bool {
	if kindOf(val) != cue.ListKind {
		return false
	}
	return slices.ContainsFunc(sources(val, 0), func(src ast.Node) bool {
		list, ok := src.(*ast.ListLit)
		return ok && slices.ContainsFunc(list.Elts, func(elt ast.Expr) bool {
			comp, ok := elt.(*ast.Comprehension)
			return ok && slices.ContainsFunc(comp.Clauses, func(clause ast.Clause) bool {
				_, isFor := clause.(*ast.ForClause)
				return isFor
			})
		})
	})
}

func hasComprehension(val cue.Value) bool {
	found := false
	for _, src := range sources(val, 0) {
		ast.Walk(src, func(node ast.Node) bool {
			if _, ok := node.(*ast.Comprehension); ok {
				found = true
			}
			return !found
		}, nil)
	}
	return found
}

// oneOf is JSON Schema's oneOf (exact) or anyOf of required keys next to
// a struct: matchN(1, [{a!: _}, {b!: _, c!: _}]) & {a?: …, b?: …, c?: …}.
type oneOf struct {
	keys  [][]string // per alternative, the keys it requires
	exact bool
}

// oneOfs finds those constraints among the parts of val.
func (g *generator) oneOfs(val cue.Value, path cue.Path, owner *named) {
	operator, args := val.Expr()
	if operator != cue.AndOp {
		return
	}
	for _, arg := range args {
		head := fmt.Sprint(arg)
		if !strings.HasPrefix(head, "matchN(") {
			continue
		}
		rule, found := keyRule(arg, owner)
		if !found {
			g.warnf(path, "%.80s is not checked", head)
			continue
		}
		rule.exact = strings.HasPrefix(head, "matchN(1,")
		owner.oneOfs = append(owner.oneOfs, rule)
	}
}

// keyRule reads a matchN of alternatives that only require keys of owner.
func keyRule(arg cue.Value, owner *named) (oneOf, bool) {
	var rule oneOf
	alts, isOr := model.Disjuncts(arg)
	for _, alt := range alts {
		keys := requiredKeys(alt)
		if len(keys) == 0 || !declared(owner, keys) {
			return rule, false
		}
		rule.keys = append(rule.keys, keys)
	}
	return rule, isOr && len(alts) > 0
}

// requiredKeys are the keys alt requires, if that is all it says.
func requiredKeys(alt cue.Value) []string {
	var out []string
	iter, err := alt.Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		if iter.Selector().ConstraintType() != cue.RequiredConstraint || iter.Value().IncompleteKind() != cue.TopKind {
			return nil
		}
		out = append(out, iter.Selector().Unquoted())
	}
	return out
}

func declared(owner *named, keys []string) bool {
	for _, key := range keys {
		if !slices.ContainsFunc(owner.fields, func(f *field) bool { return f.json == key && f.cond == nil }) {
			return false
		}
	}
	return true
}
