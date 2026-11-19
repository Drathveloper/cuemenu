package gen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"

	"github.com/Drathveloper/cuemenu/model"
)

// leaf returns the type of a scalar: its evaluated expression, such as
// *3000 | uint & >=1 & <=65535, read as alternatives of conjuncts. It is
// read from the text fmt prints, which has references resolved (Syntax
// keeps them as let clauses).
func (g *generator) leaf(val cue.Value, path cue.Path) *ref {
	node, err := parser.ParseExpr("", fmt.Sprint(val))
	if err != nil {
		g.warnf(path, "%v: any value is accepted", val)
		return &ref{kind: kAny}
	}
	if call, ok := node.(*ast.CallExpr); ok && text(call.Fun) == "matchN" {
		return g.matchN(val, path)
	}
	lits, rest := alternatives(node)
	switch {
	case len(rest) == 1: // the literals are defaults (assumed valid)
		return g.conjuncts(rest[0], path)
	case len(rest) == 0 && len(lits) > 0:
		return g.enum(lits, path)
	case len(rest) == 0:
		return &ref{kind: kAny}
	default:
		return g.severalKinds(lits, rest, path)
	}
}

// alternatives splits node into its literals and its other alternatives,
// null aside.
func alternatives(node ast.Expr) ([]ast.Expr, []ast.Expr) {
	var lits, rest []ast.Expr
	for _, alt := range split(node, token.OR) {
		switch {
		case isLit(alt, token.NULL):
		case isScalarLit(alt):
			lits = append(lits, alt)
		default:
			rest = append(rest, alt)
		}
	}
	return lits, rest
}

// severalKinds is a value of the kinds of several alternatives.
func (g *generator) severalKinds(lits, rest []ast.Expr, path cue.Path) *ref {
	var names []string
	for _, alt := range rest {
		names = append(names, jsonKinds(g.conjuncts(alt, path).kind)...)
	}
	if len(lits) > 0 {
		litk, _ := litKind(lits[0])
		names = append(names, jsonKinds(litk)...)
	}
	return g.ofKinds(names, path)
}

// matchN is JSON Schema's oneOf or anyOf of values: the literals of an
// enum, or else values of several kinds, of which the kind is checked.
func (g *generator) matchN(val cue.Value, path cue.Path) *ref {
	alts, ok := model.Disjuncts(val)
	if !ok {
		g.warnf(path, "%v is not checked", val)
		return &ref{kind: kAny}
	}
	var lits []ast.Expr
	var names []string
	for _, alt := range alts {
		if node, err := parser.ParseExpr("", fmt.Sprint(alt)); err == nil && isScalarLit(node) {
			lits = append(lits, node)
		}
		names = append(names, cueKinds(kindOf(alt))...)
	}
	if len(lits) == len(alts) {
		return g.enum(lits, path)
	}
	return g.ofKinds(names, path)
}

// ofKinds is a value of one of the JSON kinds in names: decoded as such
// if there is one, else as any, and its kind checked. What else the
// alternatives say is not.
func (g *generator) ofKinds(names []string, path cue.Path) *ref {
	slices.Sort(names)
	names = slices.Compact(names)
	if names = slices.DeleteFunc(names, func(name string) bool { return name == jsonNull }); slices.Contains(names, "") {
		return &ref{kind: kAny}
	}
	if len(names) == 2 && names[0] == typeInt && names[1] == jsonNumber {
		names = []string{jsonNumber}
	}
	all := []string{jsonArray, typeBool, jsonNumber, jsonObject, typeString}
	if !slices.ContainsFunc(all, func(name string) bool { return !slices.Contains(names, name) }) {
		return &ref{kind: kAny} // any JSON value
	}
	g.warnf(path, "alternatives of kinds %s: only the kind is checked", strings.Join(names, ", "))
	if len(names) == 1 {
		return &ref{kind: map[string]kind{typeBool: kBool, typeInt: kInt, jsonNumber: kFloat, typeString: kString}[names[0]]}
	}
	g.uses[helperIsKind] = true
	quoted := make([]string, len(names))
	for idx, name := range names {
		quoted[idx] = strconv.Quote(name)
	}
	return &ref{kind: kAny, checks: []check{{
		"!isKind(%[1]s, " + strings.Join(quoted, ", ") + ")",
		"must be of kind " + strings.Join(names, " or "),
	}}}
}

// jsonKinds names the JSON kinds of a Go kind ("" for any).
func jsonKinds(goKind kind) []string {
	switch goKind {
	case kBool:
		return []string{typeBool}
	case kInt:
		return []string{typeInt}
	case kFloat:
		return []string{jsonNumber}
	case kString, kBytes:
		return []string{typeString}
	case kStruct, kMap, kUnion:
		return []string{jsonObject}
	case kList:
		return []string{jsonArray}
	default:
		return []string{""}
	}
}

// cueKinds names the JSON kinds of a CUE kind.
func cueKinds(kinds cue.Kind) []string {
	if kinds == cue.TopKind || kinds == cue.BottomKind {
		return []string{""}
	}
	var out []string
	for _, pair := range []struct {
		name string
		kind cue.Kind
	}{
		{jsonNull, cue.NullKind}, {typeBool, cue.BoolKind}, {typeInt, cue.IntKind}, {jsonNumber, cue.FloatKind},
		{typeString, cue.StringKind}, {typeString, cue.BytesKind}, {jsonObject, cue.StructKind}, {jsonArray, cue.ListKind},
	} {
		if kinds&pair.kind != 0 {
			out = append(out, pair.name)
		}
	}
	return out
}

// split flattens a chain of operator, dropping the default marks (*x).
func split(node ast.Expr, operator token.Token) []ast.Expr {
	switch expr := node.(type) {
	case *ast.BinaryExpr:
		if expr.Op == operator {
			return append(split(expr.X, operator), split(expr.Y, operator)...)
		}
	case *ast.UnaryExpr:
		if expr.Op == token.MUL {
			return split(expr.X, operator)
		}
	case *ast.ParenExpr:
		return split(expr.X, operator)
	}
	return []ast.Expr{node}
}

func isLit(node ast.Expr, kind token.Token) bool {
	lit, ok := node.(*ast.BasicLit)
	return ok && lit.Kind == kind
}

func isScalarLit(node ast.Expr) bool {
	if unary, ok := node.(*ast.UnaryExpr); ok && unary.Op == token.SUB {
		node = unary.X
	}
	lit, ok := node.(*ast.BasicLit)
	return ok && lit.Kind != token.NULL
}

// litKind is the kind of a literal and its Go form.
func litKind(node ast.Expr) (kind, string) {
	sign := ""
	if unary, ok := node.(*ast.UnaryExpr); ok {
		sign, node = "-", unary.X
	}
	lit, _ := node.(*ast.BasicLit)
	switch lit.Kind {
	case token.STRING:
		str, err := literal.Unquote(lit.Value)
		if err != nil || strings.HasPrefix(lit.Value, "'") {
			return kBytes, ""
		}
		return kString, strconv.Quote(str)
	case token.INT:
		return kInt, sign + lit.Value
	case token.FLOAT:
		return kFloat, sign + lit.Value
	case token.TRUE, token.FALSE:
		return kBool, lit.Value
	default:
		return kAny, ""
	}
}

// enum is a choice among literals: oneof when the values fit in a tag.
func (g *generator) enum(lits []ast.Expr, path cue.Path) *ref {
	out, ok := enumValues(lits)
	if !ok {
		g.warnf(path, "literals of several kinds: any value is accepted")
		return &ref{kind: kAny}
	}
	values := out.values
	switch {
	case out.kind == kBool && len(values) == 2 || out.kind == kBytes:
	case out.kind == kString && tagSafe(values) || out.kind == kInt:
		out.tags = []string{oneOfTag(values)}
	default:
		conds := make([]string, len(values))
		for idx, value := range values {
			conds[idx] = "%[1]s != " + value
		}
		out.checks = []check{{strings.Join(conds, " && "), "must be one of " + strings.Join(values, ", ")}}
	}
	return out
}

// oneOfTag is oneof=a b, or eq=a for one value.
func oneOfTag(values []string) string {
	plain := make([]string, len(values))
	for idx, value := range values {
		plain[idx] = strings.Trim(value, `"`)
	}
	if len(plain) == 1 {
		return "eq=" + plain[0]
	}
	return "oneof=" + strings.Join(plain, " ")
}

// enumValues reads the literals of an enum, all of one kind (ints and
// floats are numbers).
func enumValues(lits []ast.Expr) (*ref, bool) {
	out := &ref{values: make([]string, 0, len(lits))}
	for idx, lit := range lits {
		litk, value := litKind(lit)
		if idx > 0 && litk != out.kind {
			if !isNumber(litk) || !isNumber(out.kind) {
				return nil, false
			}
			litk = kFloat
		}
		out.kind = litk
		out.values = append(out.values, value)
	}
	return out, true
}

func isNumber(k kind) bool { return k == kInt || k == kFloat }

// tagSafe: the strings can be written in a tag as they are.
func tagSafe(quoted []string) bool {
	for _, value := range quoted {
		plain, err := strconv.Unquote(value)
		if err != nil || plain == "" || strings.ContainsAny(plain, " ,|'\"\\`=") || strconv.Quote(plain) != value {
			return false
		}
	}
	return true
}

// predeclared returns the Go kind of a CUE type and the bounds it implies.
func predeclared(name string) (kind, []string, bool) {
	type decl struct {
		tags []string
		kind kind
	}
	table := map[string]decl{
		typeInt: {kind: kInt}, "int64": {kind: kInt},
		"uint": {[]string{tagUnsigned}, kInt}, "uint64": {[]string{tagUnsigned}, kInt},
		"int8": {[]string{"gte=-128", "lte=127"}, kInt}, "int16": {[]string{"gte=-32768", "lte=32767"}, kInt},
		"int32": {[]string{"gte=-2147483648", "lte=2147483647"}, kInt},
		"rune":  {[]string{tagUnsigned, "lte=1114111"}, kInt},
		"uint8": {[]string{tagUnsigned, "lte=255"}, kInt}, "byte": {[]string{tagUnsigned, "lte=255"}, kInt},
		"uint16": {[]string{tagUnsigned, "lte=65535"}, kInt}, "uint32": {[]string{tagUnsigned, "lte=4294967295"}, kInt},
		"float": {kind: kFloat}, "float32": {kind: kFloat}, "float64": {kind: kFloat}, jsonNumber: {kind: kFloat},
		typeString: {kind: kString}, "bytes": {kind: kBytes}, typeBool: {kind: kBool}, "_": {kind: kAny},
	}
	found, ok := table[name]
	return found.kind, found.tags, ok
}

// conjuncts reads a type and its constraints: uint & >=1 & <=65535.
func (g *generator) conjuncts(node ast.Expr, path cue.Path) *ref {
	out := &ref{kind: kAny}
	var bounds []ast.Expr
	for _, part := range split(node, token.AND) {
		if g.conjunct(out, part, path) {
			bounds = append(bounds, part)
		}
	}
	for _, bound := range bounds {
		g.bound(out, bound, path)
	}
	if len(out.tags) > 1 && out.tags[0] == tagUnsigned && slices.ContainsFunc(out.tags[1:], isLowerBound) {
		out.tags = out.tags[1:] // uint & >=1
	}
	return out
}

// conjunct reads a type or a builtin into out; it tells whether part is
// a bound, read once the type is known.
func (g *generator) conjunct(out *ref, part ast.Expr, path cue.Path) bool {
	switch expr := part.(type) {
	case *ast.Ident:
		if declKind, tags, ok := predeclared(expr.Name); ok {
			out.kind = declKind
			out.tags = append(out.tags, tags...)
			return false
		}
		g.warnf(path, "%s is not checked", expr.Name)
	case *ast.UnaryExpr:
		return true
	case *ast.CallExpr:
		g.call(out, expr, path)
	default:
		if isScalarLit(part) {
			out.kind, _ = litKind(part)
			return true
		}
		g.warnf(path, "%s is not checked", text(part))
	}
	return false
}

func isLowerBound(tag string) bool {
	return strings.HasPrefix(tag, "gte=") || strings.HasPrefix(tag, "gt=")
}

// bound turns >=1, =~"re", != "x" or a constant into a tag or a check.
func (g *generator) bound(out *ref, node ast.Expr, path cue.Path) {
	if isScalarLit(node) {
		_, value := litKind(node)
		out.checks = append(out.checks, check{"%[1]s != " + value, "must be " + value})
		return
	}
	unary, _ := node.(*ast.UnaryExpr)
	if !isScalarLit(unary.X) {
		g.warnf(path, "%s is not checked", text(node))
		return
	}
	argKind, value := litKind(unary.X)
	boundKind(out, unary.Op, argKind)
	switch unary.Op {
	case token.MAT, token.NMAT:
		g.match(out, unary.Op, value)
	case token.GEQ, token.GTR, token.LEQ, token.LSS, token.NEQ:
		g.compare(out, unary.Op, argKind, value)
	default:
		g.warnf(path, "%s is not checked", text(node))
	}
}

// boundKind is the kind a bound gives a value of no type yet: a string
// for a pattern or a string, a number for a number.
func boundKind(out *ref, operator token.Token, argKind kind) {
	switch {
	case operator == token.MAT || operator == token.NMAT || out.kind == kAny && argKind == kString:
		out.kind = kString
	case out.kind == kAny && isNumber(argKind):
		out.kind = kFloat
	}
}

// match checks =~"re" or !~"re" (operator) with the quoted pattern value.
func (g *generator) match(out *ref, operator token.Token, value string) {
	pattern, _ := strconv.Unquote(value)
	re := g.regexp(pattern)
	cond, msg := "!"+re+".MatchString(%[1]s)", "must match "+value
	if operator == token.NMAT {
		cond, msg = re+".MatchString(%[1]s)", "must not match "+value
	}
	out.checks = append(out.checks, check{cond, msg})
}

// tagOf is the validator tag of a bound; negated is the Go operator that
// tells a value breaks it.
func tagOf(operator token.Token) (string, string) {
	switch operator {
	case token.GEQ:
		return "gte", "<"
	case token.GTR:
		return "gt", "<="
	case token.LEQ:
		return "lte", ">"
	case token.LSS:
		return "lt", ">="
	default: // token.NEQ
		return "ne", "=="
	}
}

func (g *generator) compare(out *ref, operator token.Token, argKind kind, value string) {
	tag, negated := tagOf(operator)
	numeric := out.kind == kInt && argKind == kInt || out.kind == kFloat && isNumber(argKind)
	if numeric || operator == token.NEQ && out.kind == kString && tagSafe([]string{value}) {
		out.tags = append(out.tags, tag+"="+strings.Trim(value, `"`))
		return
	}
	out.checks = append(out.checks, check{"%[1]s " + negated + " " + value, "must be " + operator.String() + " " + value})
}

// builtin is a validator of CUE's standard library: a validator tag, or
// a check (cond: invalid when true; %[2]s is the argument) and the
// helper it uses.
type builtin struct{ tag, cond, msg, uses string }

func builtinOf(name string) (builtin, bool) {
	table := map[string]builtin{
		"strings.MinRunes":  {tag: "min=%s"},
		"strings.MaxRunes":  {tag: "max=%s"},
		"strings.HasPrefix": {"startswith=%s", "!strings.HasPrefix(%[1]s, %[2]s)", "must start with", pkgStrings},
		"strings.HasSuffix": {"endswith=%s", "!strings.HasSuffix(%[1]s, %[2]s)", "must end with", pkgStrings},
		"strings.Contains":  {"contains=%s", "!strings.Contains(%[1]s, %[2]s)", "must contain", pkgStrings},
		"time.Duration":     {cond: "!validDuration(%[1]s)", msg: "must be a duration", uses: helperDuration},
		"time.Time":         {cond: "!validTime(%[1]s)", msg: "must be an RFC 3339 time", uses: helperTime},
		"net.IP":            {tag: "ip"},
		"net.IPv4":          {tag: "ipv4"},
		"net.IPv6":          {tag: "ipv6"},
		"net.IPCIDR":        {tag: "cidr"},
		"net.URL":           {cond: "!validURL(%[1]s, false)", msg: "must be a URL", uses: helperURL},
		"net.AbsURL":        {cond: "!validURL(%[1]s, true)", msg: "must be an absolute URL", uses: helperURL},
	}
	found, ok := table[name]
	return found, ok
}

func (g *generator) call(out *ref, node *ast.CallExpr, path cue.Path) {
	name := text(node.Fun)
	found, known := builtinOf(name)
	if !known || len(node.Args) > 1 {
		g.warnf(path, "%s is not checked", text(node))
		return
	}
	if strings.HasPrefix(name, "strings.") || strings.HasPrefix(name, "time.") || strings.HasPrefix(name, "net.") {
		out.kind = kString
	}
	arg, ok := callArg(node)
	if !ok {
		g.warnf(path, "%s is not checked", text(node))
		return
	}
	g.builtin(out, found, arg)
}

// callArg is the only argument of a call, a literal, or "" if it has none.
func callArg(node *ast.CallExpr) (string, bool) {
	if len(node.Args) == 0 {
		return "", true
	}
	if !isScalarLit(node.Args[0]) {
		return "", false
	}
	_, arg := litKind(node.Args[0])
	return arg, true
}

// builtin adds the tag of a builtin, or else its check.
func (g *generator) builtin(out *ref, rule builtin, arg string) {
	if rule.tag != "" && (arg == "" || !strings.HasPrefix(arg, `"`) || tagSafe([]string{arg})) {
		out.tags = append(out.tags, fmt.Sprintf(rule.tag, strings.Trim(arg, `"`)))
		return
	}
	g.uses[rule.uses] = true
	out.checks = append(out.checks, check{
		strings.ReplaceAll(rule.cond, "%[2]s", arg),
		strings.TrimSpace(rule.msg + " " + arg),
	})
}

func (g *generator) regexp(pattern string) string {
	if name, ok := g.regexps[pattern]; ok {
		return name
	}
	name := fmt.Sprintf("pattern%d", len(g.regexps)+1)
	g.regexps[pattern] = name
	return name
}

// text is the CUE source of node.
func text(node ast.Node) string {
	data, err := format.Node(node)
	if err != nil {
		return fmt.Sprint(node)
	}
	return string(data)
}

// literal is the Go expression of the default def of a value of type
// target.
func (g *generator) literal(def cue.Value, target *ref, path cue.Path) string {
	if lit, ok := g.literalOf(def, target); ok {
		return lit
	}
	g.warnf(path, "default %v is not set", def)
	return ""
}

func (g *generator) literalOf(def cue.Value, target *ref) (string, bool) {
	switch target.kind {
	case kString:
		str, err := def.String()
		return strconv.Quote(str), err == nil
	case kInt, kFloat, kBool:
		data, err := def.MarshalJSON()
		return string(data), err == nil
	case kBytes:
		data, err := def.Bytes()
		return "[]byte(" + strconv.Quote(string(data)) + ")", err == nil
	default:
		data, err := def.MarshalJSON()
		if err != nil {
			return "", false
		}
		if string(data) == "[]" || string(data) == "{}" {
			return "", true
		}
		g.uses[helperFromJSON] = true
		return "fromJSON[" + g.goType(target) + "](" + strconv.Quote(string(data)) + ")", true
	}
}
