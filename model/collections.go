package model

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/literal"
)

// Entry describes a new key for the map at mapPath, so the caller can ask
// for its value. It fails if the key exists or no pattern accepts it.
func (c *Config) Entry(mapPath cue.Path, key string) (*Node, error) {
	mapVal := c.Value().LookupPath(mapPath)
	if mapVal.LookupPath(cue.MakePath(cue.Str(key))).Exists() {
		return nil, fmt.Errorf("%w: %q", ErrKeyExists, key)
	}
	constraint, ok := patternFor(mapVal, key)
	if !ok {
		return nil, fmt.Errorf("%w: %q %v", ErrKeyPattern, key, patterns(mapVal))
	}
	node := &Node{Path: child(mapPath, cue.Str(key)), Label: key, Doc: doc(constraint)}
	classify(node, constraint)
	return node, nil
}

// SetList replaces the list at path with elems, each a CUE expression. An
// empty element (one not filled in yet in a closed list) is written as _.
func (c *Config) SetList(path cue.Path, elems []string) error {
	parts := make([]string, len(elems))
	for idx, elem := range elems {
		parts[idx] = cmp.Or(elem, "_")
	}
	return c.SetExpr(path, "["+strings.Join(parts, ", ")+"]")
}

// Choose selects variant idx of the struct disjunction at path. With a
// discriminator field it sets it. Otherwise a variant is selected by
// giving a value to a field only it has: that field is returned for the
// caller to edit, and setting it completes the choice (edits of the other
// variant are then pruned). A variant with no fields of its own is chosen
// by dropping the edits it does not declare.
//
//nolint:nilnil // a nil node means the choice is done; a node, that it needs that field
func (c *Config) Choose(path cue.Path, idx int) (*Node, error) {
	alts, err := c.variants(path, idx)
	if err != nil {
		return nil, err
	}
	names, discr := variantNames(alts)
	if discr.String() != "" {
		discrPath := cue.MakePath(append(slices.Clone(path.Selectors()), discr.Selectors()...)...)
		return nil, c.SetExpr(discrPath, literal.String.Quote(names[idx]))
	}
	current := c.Value().LookupPath(path)
	if def, ok := current.Default(); ok {
		current = def
	}
	if chosenVariant(current, alts) == idx {
		return nil, nil
	}
	if ask := askFor(alts, idx, path); ask != nil {
		return ask, nil
	}
	c.dropUndeclared(path, alts[idx])
	return nil, nil
}

// variants returns the alternatives of the variant at path, checking that
// idx is one of them.
func (c *Config) variants(path cue.Path, idx int) ([]cue.Value, error) {
	decl, _, found := find(c.without(path), path)
	op, alts := decl.Expr()
	if !found || op != cue.OrOp || idx < 0 || idx >= len(alts) {
		return nil, fmt.Errorf("%s: %w", path, ErrNotVariant)
	}
	return alts, nil
}

// dropUndeclared removes the edits below path that alt does not declare.
func (c *Config) dropUndeclared(path cue.Path, alt cue.Value) {
	depth := len(path.Selectors())
	for key, ed := range c.edits {
		if !under(ed.path, path) || len(ed.path.Selectors()) == depth {
			continue
		}
		rel := cue.MakePath(ed.path.Selectors()[depth:]...)
		if _, _, found := find(alt, rel); !found {
			delete(c.edits, key)
		}
	}
	c.prune()
}

// listItems returns a node per element of the list fld, plus a template
// for new elements taken from the element constraint of the unedited list
// decl. Lists are edited as a whole (see SetList), so elements carry no
// edits.
func listItems(fld, decl cue.Value, path cue.Path) ([]*Node, *Node) {
	elem := elemType(decl)
	var tmpl *Node
	if elem.Exists() {
		tmpl = &Node{Label: "new element", Doc: doc(elem)}
		classify(tmpl, elem)
	}
	if def, ok := fld.Default(); ok && !fld.IsConcrete() {
		fld = def
	}
	var items []*Node
	iter, err := fld.List()
	for idx := 0; err == nil && iter.Next(); idx++ {
		constraint := elem
		if !constraint.Exists() { // closed list: per-position constraint
			constraint = decl.LookupPath(cue.MakePath(cue.Index(idx)))
		}
		item := &Node{Path: index(path, idx), Label: fmt.Sprintf("[%d]", idx)}
		classify(item, constraint)
		item.Value = leafValue(iter.Value())
		items = append(items, item)
	}
	if tmpl != nil {
		tmpl.Path = index(path, len(items))
	}
	return items, tmpl
}

func index(list cue.Path, idx int) cue.Path {
	return cue.MakePath(append(slices.Clone(list.Selectors()), cue.Index(idx))...)
}

// elemType is T in [...T], also when the list has a default
// (*["a"] | [...T]): then it is found in the expression's arguments.
func elemType(decl cue.Value) cue.Value {
	anyIndex := cue.MakePath(cue.AnyIndex)
	if elem := decl.LookupPath(anyIndex); elem.Exists() {
		return elem
	}
	_, args := decl.Expr()
	for _, arg := range args {
		if elem := arg.LookupPath(anyIndex); elem.Exists() {
			return elem
		}
	}
	return cue.Value{}
}

// askFor returns the field that tells variant idx apart, to be filled in.
func askFor(alts []cue.Value, idx int, path cue.Path) *Node {
	own := ownFields(alts, idx)
	if len(own) == 0 {
		return nil
	}
	fld := alts[idx].LookupPath(cue.MakePath(cue.Str(own[0])))
	return firstLeaf(fld, child(path, cue.Str(own[0])), own[0])
}

// ownFields lists the fields of alternative idx that no other has.
func ownFields(alts []cue.Value, idx int) []string {
	var own []string
	iter, err := alts[idx].Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		name := iter.Selector().Unquoted()
		shared := false
		for other, alt := range alts {
			if _, _, found := field(alt, cue.Str(name)); found && other != idx {
				shared = true
			}
		}
		if !shared {
			own = append(own, name)
		}
	}
	return own
}

// chosenVariant is the first alternative val is still compatible with.
func chosenVariant(val cue.Value, alts []cue.Value) int {
	for idx, alt := range alts {
		if val.Unify(alt).Err() == nil {
			return idx
		}
	}
	return -1
}

// firstLeaf returns the first scalar field at or below val, preferring
// required fields: the one to ask for when choosing a variant.
func firstLeaf(val cue.Value, path cue.Path, label string) *Node {
	if val.IncompleteKind() != cue.StructKind {
		node := &Node{Path: path, Label: label, Doc: doc(val)}
		classify(node, val)
		return node
	}
	type fieldVal struct {
		val cue.Value
		sel cue.Selector
	}
	var required, rest []fieldVal
	iter, err := val.Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		fv := fieldVal{iter.Value(), iter.Selector()}
		if fv.sel.ConstraintType() == cue.RequiredConstraint {
			required = append(required, fv)
		} else {
			rest = append(rest, fv)
		}
	}
	for _, fv := range append(required, rest...) {
		if node := firstLeaf(fv.val, child(path, fv.sel), fv.sel.Unquoted()); node != nil {
			node.Required = node.Required || fv.sel.ConstraintType() == cue.RequiredConstraint
			return node
		}
	}
	return nil
}
