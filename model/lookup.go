package model

import (
	"fmt"

	"cuelang.org/go/cue"
)

// find locates the declaration of the field at path in val, returning its
// value and the selector it was declared with (so x! and x? keep their
// marker). Unlike LookupPath it finds optional and required fields without
// a value, and fields that only exist in some alternatives of an
// unresolved disjunction, e.g. the fields of a struct variant. An
// alternative only counts if it still unifies with val.
func find(val cue.Value, path cue.Path) (cue.Value, cue.Selector, bool) {
	if len(path.Selectors()) == 0 { // the root
		return val, cue.Selector{}, true
	}
	return findIn(val, path.Selectors(), true)
}

func findIn(val cue.Value, sels []cue.Selector, expand bool) (cue.Value, cue.Selector, bool) {
	// Declared fields first, in val or in an alternative; only then any
	// field of a struct declared with ...: one that an alternative (an
	// allOf part) declares has a better type than _.
	for _, lookup := range []lookupFunc{declared, field} {
		if fld, sel, found := descend(val, sels, lookup); found {
			return fld, sel, true
		}
		if !expand {
			continue
		}
		for _, alt := range alternatives(val) {
			if !fits(val, alt) {
				continue
			}
			if fld, sel, found := descend(val.Unify(alt), sels, lookup); found {
				return fld, sel, true
			}
		}
	}
	return cue.Value{}, cue.Selector{}, false
}

type lookupFunc func(cue.Value, cue.Selector) (cue.Value, cue.Selector, bool)

// descend looks up sels[0] in val with lookup, then the rest below it.
func descend(val cue.Value, sels []cue.Selector, lookup lookupFunc) (cue.Value, cue.Selector, bool) {
	fld, sel, found := lookup(val, sels[0])
	if !found || len(sels) == 1 {
		return fld, sel, found
	}
	return findIn(fld, sels[1:], true)
}

// field looks up a direct field of val by name, including x? and x!.
// The field iterator yields nothing on a disjunction, so its default is
// searched too.
func declared(val cue.Value, sel cue.Selector) (cue.Value, cue.Selector, bool) {
	if sel.Type() == cue.IndexLabel { // an element of a list
		elem := elemAt(val, sel.Index())
		return elem, sel, elem.Exists()
	}
	if !sel.IsString() { // hidden fields and definitions are not editable
		return cue.Value{}, cue.Selector{}, false
	}
	name := sel.Unquoted()
	candidates := []cue.Value{val}
	if def, ok := val.Default(); ok {
		candidates = append(candidates, def)
	}
	for _, cand := range candidates {
		iter, err := cand.Fields(cue.Optional(true))
		for err == nil && iter.Next() {
			if decl := iter.Selector(); decl.Unquoted() == name {
				return iter.Value(), decl, true
			}
		}
	}
	// A key not written yet is still declared by a pattern ([K]: T).
	if constraint, ok := patternFor(val, name); ok {
		return constraint, sel, true
	}
	return cue.Value{}, cue.Selector{}, false
}

// field is declared, or any field of a struct declared with ....
func field(val cue.Value, sel cue.Selector) (cue.Value, cue.Selector, bool) {
	if fld, decl, found := declared(val, sel); found {
		return fld, decl, true
	}
	if anything, ok := declaredOpen(val); ok && sel.IsString() {
		return anything, sel, true
	}
	return cue.Value{}, cue.Selector{}, false
}

// declaredOpen reports whether val accepts fields it does not declare
// because its author said so: val is _, or its struct has "..." (then
// LookupPath(AnyString) yields _). Regular structs are open too in CUE,
// but treating them so would keep fields of an `if` that no longer holds,
// so they only take declared fields.
func declaredOpen(val cue.Value) (cue.Value, bool) {
	if operator, _ := rawExpr(val); val.IncompleteKind() == cue.TopKind && operator != cue.CallOp { // matchN(…) is no _
		return val, true
	}
	if rest := val.LookupPath(cue.MakePath(cue.AnyString)); rest.Exists() {
		return rest, true
	}
	return cue.Value{}, false
}

// alternatives lists the disjuncts in val's expression, default first.
func alternatives(val cue.Value) []cue.Value {
	if operator, args := rawExpr(val); operator == cue.AndOp {
		out := make([]cue.Value, 0, len(args))
		for _, arg := range args {
			out = append(out, alternatives(arg)...)
		}
		return out
	}
	if operator, args := expr(val); operator == cue.OrOp {
		if def, ok := val.Default(); ok {
			return append([]cue.Value{def}, args...)
		}
		return args
	}
	parts, ok := matchAll(val)
	if !ok {
		return nil
	}
	for idx, part := range parts {
		parts[idx] = nonNull(part) // the struct of an untyped part: any JSON, or {…}
	}
	return parts
}

// patterns describes the pattern constraints of val as "[K]: T".
func patterns(val cue.Value) []string {
	var out []string
	iter, err := val.Fields(cue.Patterns(true))
	for err == nil && iter.Next() {
		if sel := iter.Selector(); isPattern(sel) {
			out = append(out, fmt.Sprintf("[%v]: %v", sel.Pattern(), iter.Value()))
		}
	}
	return out
}

// isPattern: Pattern() of any other selector is an unusable error value.
func isPattern(sel cue.Selector) bool { return sel.ConstraintType() == cue.PatternConstraint }

// patternFor returns the value constraint for a new key of val: the first
// pattern the key unifies with (any string, a regexp, a disjunction...).
func patternFor(val cue.Value, key string) (cue.Value, bool) {
	iter, err := val.Fields(cue.Patterns(true))
	for err == nil && iter.Next() {
		if sel := iter.Selector(); isPattern(sel) && sel.Pattern().FillPath(cue.Path{}, key).Err() == nil {
			return iter.Value(), true
		}
	}
	return cue.Value{}, false
}

// fits reports whether val can still become alt: no permanent conflict.
// Not Err(), which also fails on values not complete yet: a struct that
// misses a required field fails matchN (JSON Schema's oneOf) until then.
// CUE can panic unifying with a value from Expr() (docker-compose's
// allOf parts): then it does not fit.
func fits(val, alt cue.Value) bool {
	var err error
	return safe(func() { err = val.Unify(alt).Validate() }) && err == nil
}
