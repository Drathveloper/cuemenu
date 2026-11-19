package model

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/literal"
)

// Entry describes a new key for the map (or open struct) at mapPath, so
// the caller can ask for its value. It fails if the key exists or no
// pattern accepts it.
func (c *Config) Entry(mapPath cue.Path, key string) (*Node, error) {
	// find, not LookupPath: a required map not filled in yet (m!: [K]: V)
	// has no value to look up, but its declaration has the patterns.
	mapVal, _, _ := find(c.Value(), mapPath)
	if mapVal.LookupPath(cue.MakePath(cue.Str(key))).Exists() {
		return nil, fmt.Errorf("%w: %q", ErrKeyExists, key)
	}
	constraint, accepted := c.entryConstraint(mapPath, mapVal, key)
	if !accepted {
		return nil, fmt.Errorf("%w: %q %v", ErrKeyPattern, key, patterns(mapVal))
	}
	node := &Node{Path: child(mapPath, cue.Str(key)), Label: key, Doc: doc(constraint)}
	classify(node, constraint)
	return node, nil
}

// entryConstraint is what a new key of the map at mapPath must hold: a
// pattern of the map, of the map behind a default (*{k: v} | {[K]: T}),
// or anything if the struct was declared with ....
func (c *Config) entryConstraint(mapPath cue.Path, mapVal cue.Value, key string) (cue.Value, bool) {
	if constraint, found := patternFor(mapVal, key); found {
		return constraint, true
	}
	if decl, _, found := find(c.without(mapPath), mapPath); found {
		if defaulted, isDefaulted := defaultedMap(decl); isDefaulted {
			if constraint, matched := patternFor(defaulted, key); matched {
				return constraint, true
			}
		}
	}
	return declaredOpen(mapVal)
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
	if compatible(current, alts) == 1 && chosenVariant(current, alts) == idx { // already chosen
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
	alts := variantAlts(decl)
	if !found || idx < 0 || idx >= len(alts) {
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
// decl. A struct element is a menu of its fields; the list is still saved
// whole (see setElem).
func (c *Config) listItems(fld, decl cue.Value, path cue.Path, errs map[string][]string) ([]*Node, *Node) {
	elem := elemType(decl)
	var tmpl *Node
	if elem.Exists() {
		tmpl = &Node{Label: "new element", Doc: doc(elem)}
		classify(tmpl, elem)
	}
	if def, ok := fld.Default(); ok && !fld.IsConcrete() {
		fld = def
	}
	elems := c.elems(path) // shown, and edited with e, as written
	var items []*Node
	for idx := 0; ; idx++ {
		constraint := elemAt(decl, idx)
		elemVal, ok := elemValue(fld, elems, constraint, idx)
		if !ok {
			break
		}
		item := &Node{Path: index(path, idx), Label: fmt.Sprintf("[%d]", idx)}
		item.Errors = errs[item.Path.String()]
		classify(item, constraint)
		c.fill(item, elemVal, constraint, errs)
		if item.Value == "" && idx < len(elems) {
			item.Value = leafValue(elems[idx])
		}
		items = append(items, item)
	}
	if tmpl != nil {
		tmpl.Path = index(path, len(items))
	}
	return items, tmpl
}

// elemValue is element idx of the list fld, looked up by index: an
// element with an error makes the list bottom and List fails, but its
// elements, and their fields, can still be looked up. An element left out
// of the value while something it is in cannot be decided (a matchN
// waiting for the element's required fields) is taken as written, so
// that they can be filled in.
func elemValue(fld cue.Value, written []cue.Value, constraint cue.Value, idx int) (cue.Value, bool) {
	val := fld.LookupPath(cue.MakePath(cue.Index(idx)))
	if !val.Exists() && idx < len(written) {
		val = constraint.Unify(written[idx])
	}
	return val, val.Exists()
}

// elemAt is the constraint of element idx of the list declared as list:
// T in [...T], else that of the position in a closed list.
func elemAt(list cue.Value, idx int) cue.Value {
	if elem := elemType(list); elem.Exists() {
		return elem
	}
	return list.LookupPath(cue.MakePath(cue.Index(idx)))
}

// Elems is the list at path as CUE text, one string per element, as the
// user wrote them: defaults are not copied in.
func (c *Config) Elems(path cue.Path) []string {
	elems := c.elems(path)
	out := make([]string, len(elems))
	for idx, elem := range elems {
		out[idx] = leafValue(elem)
	}
	return out
}

// elems are the elements of the list at path as the user wrote them (an
// edit holds a list whole, also one inside an element of another list),
// else as they are now: the schema's default.
func (c *Config) elems(path cue.Path) []cue.Value {
	list, ok := c.written(path)
	if !ok {
		cur, _, found := find(c.Value(), path) // also an optional field
		if found && cur.Validate(cue.Concrete(true)) != nil {
			return c.placeholders(cur)
		}
		list = c.ctx.CompileString(leafValue(cur))
	}
	var out []cue.Value
	iter, err := list.List()
	for err == nil && iter.Next() {
		out = append(out, iter.Value())
	}
	return out
}

// placeholders are the elements of a list not concrete yet, as listItems
// shows them: [_, ...] (JSON Schema's minItems) or [int, string] ask for
// elements before any is written. Each is its value if it has one, else
// what it still needs.
func (c *Config) placeholders(list cue.Value) []cue.Value {
	if def, ok := list.Default(); ok {
		list = def
	}
	var out []cue.Value
	for idx := 0; ; idx++ {
		elem := list.LookupPath(cue.MakePath(cue.Index(idx)))
		if !elem.Exists() {
			return out
		}
		text := leafValue(elem)
		switch {
		case text != "":
		case elem.IncompleteKind() == cue.StructKind:
			text = "{}"
		default:
			text = "_"
		}
		out = append(out, c.ctx.CompileString(text))
	}
}

// written is what the user wrote at path: an edit at path or above it (a
// list edit holds its elements and what is inside them).
func (c *Config) written(path cue.Path) (cue.Value, bool) {
	sels := path.Selectors()
	for size := len(sels); size > 0; size-- {
		if ed, ok := c.edits[cue.MakePath(sels[:size]...).String()]; ok {
			val := ed.val.LookupPath(cue.MakePath(sels[size:]...))
			return val, val.Exists()
		}
	}
	return cue.Value{}, false
}

// setElem sets the field at rest inside element idx of the list at list,
// or removes it (val nil). Lists are edited whole, so the element is
// rebuilt from what the user wrote in it plus the change.
func (c *Config) setElem(list cue.Path, idx int, rest cue.Path, val *cue.Value) error {
	elems := c.elems(list)
	if idx >= len(elems) {
		return fmt.Errorf("%s: %w", index(list, idx), ErrUndeclared)
	}
	var kept, fresh []edit
	if len(rest.Selectors()) > 0 {
		for _, ed := range leaves(elems[idx], cue.Path{}) {
			if !under(ed.path, rest) {
				kept = append(kept, ed)
			}
		}
	}
	if val != nil {
		fresh = leaves(*val, rest)
	}
	elems[idx] = c.rebuild(c.pruneElem(list, idx, kept, fresh), len(rest.Selectors()) == 0)
	return c.Set(list, c.ctx.NewList(elems...))
}

// pruneElem drops the fields kept in an element that no longer exist with
// the fresh ones, e.g. those of a variant no longer chosen: what prune
// does with the edits of a struct.
func (c *Config) pruneElem(list cue.Path, idx int, kept, fresh []edit) []edit {
	decl, _, found := find(c.without(list), list)
	if !found {
		return append(kept, fresh...)
	}
	constraint := elemAt(decl, idx)
	for changed := true; changed; {
		changed = false
		for pos, ed := range kept {
			others := slices.Delete(slices.Clone(kept), pos, pos+1)
			if _, _, ok := find(constraint.Unify(c.rebuild(append(others, fresh...), false)), ed.path); !ok {
				kept, changed = others, true
				break
			}
		}
	}
	return declOrder(append(kept, fresh...), constraint)
}

// declOrder sorts the leaves of an element as decl declares them, so that
// the element reads the same however it was edited, and after reloading.
func declOrder(parts []edit, decl cue.Value) []edit {
	rank := func(path cue.Path) []int {
		var out []int
		val := decl
		for _, sel := range path.Selectors() {
			pos, idx := -1, 0
			iter, err := val.Fields(cue.Optional(true))
			for ; err == nil && iter.Next(); idx++ {
				if iter.Selector().Unquoted() == sel.Unquoted() {
					pos = idx
					break
				}
			}
			if pos < 0 { // undeclared (a key, a field of ...): after, as they are
				return append(out, idx)
			}
			out = append(out, pos)
			val = iter.Value()
		}
		return out
	}
	slices.SortStableFunc(parts, func(a, b edit) int { return slices.Compare(rank(a.path), rank(b.path)) })
	return parts
}

// rebuild makes a struct of leaves, or any value if whole (the leaf is
// the element itself). It is compiled from text when it can: a value
// built with FillPath prints its fields sorted by name (Syntax(Final)).
func (c *Config) rebuild(parts []edit, whole bool) cue.Value {
	if !whole {
		var src strings.Builder
		for _, ed := range parts {
			if ed.text == "" {
				src.Reset()
				break
			}
			src.WriteString(labels(ed.path) + ": " + ed.text + "\n")
		}
		if val := c.ctx.CompileString(src.String()); src.Len() > 0 && val.Err() == nil {
			return val
		}
	}
	val := c.ctx.CompileString("{}")
	if whole {
		val = c.ctx.CompileString("_")
	}
	for _, ed := range parts {
		val = val.FillPath(ed.path, ed.val)
	}
	return val
}

// inList splits a path inside a list element (ports[0].name) into the
// list, the index and the path inside the element, at the last index.
func inList(path cue.Path) (cue.Path, int, cue.Path, bool) {
	sels := path.Selectors()
	for pos, sel := range slices.Backward(sels) {
		if sel.Type() == cue.IndexLabel {
			return cue.MakePath(sels[:pos]...), sel.Index(), cue.MakePath(sels[pos+1:]...), true
		}
	}
	return cue.Path{}, 0, cue.Path{}, false
}

func index(list cue.Path, idx int) cue.Path {
	return cue.MakePath(append(slices.Clone(list.Selectors()), cue.Index(idx))...)
}

// elemType is T in [...T], also when the list has a default
// (*["a"] | [...T]): then it is found in the expression's arguments.
func elemType(decl cue.Value) cue.Value {
	if elem, ok := elemOf(decl); ok {
		return elem
	}
	_, args := expr(decl)
	for _, arg := range args {
		if elem, ok := elemOf(arg); ok {
			return elem
		}
	}
	return cue.Value{}
}

// elemOf is T in the list [...T]. In a recursive schema (#R: {sub?:
// [...#R]}) the T of a nested list is a structural cycle until it is
// unified with an element, so it is read from one.
func elemOf(list cue.Value) (cue.Value, bool) {
	elem := list.LookupPath(cue.MakePath(cue.AnyIndex))
	if elem.Exists() && elem.Err() != nil {
		probe := list.Unify(cuecontext.New().CompileString("[{}]"))
		elem = probe.LookupPath(cue.MakePath(cue.Index(0)))
	}
	return elem, elem.Exists()
}

// askFor returns the field that tells variant idx apart, to be filled in.
func askFor(alts []cue.Value, idx int, path cue.Path) *Node {
	own := ownFields(alts, idx)
	if len(own) == 0 {
		return nil
	}
	if required := requiredOf(alts[idx], own); len(required) > 0 {
		own = required // the field the variant is named after
	}
	fld, sel, _ := field(alts[idx], cue.Str(own[0])) // not LookupPath: it misses x!
	node := firstLeaf(fld, child(path, sel), own[0])
	if node != nil && node.Path.String() == child(path, sel).String() {
		node.Required = sel.ConstraintType() == cue.RequiredConstraint
	}
	return node
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

// compatible counts the alternatives val still unifies with.
func compatible(val cue.Value, alts []cue.Value) int {
	count := 0
	for _, alt := range alts {
		if fits(val, alt) {
			count++
		}
	}
	return count
}

// chosenVariant is the first alternative val is still compatible with.
func chosenVariant(val cue.Value, alts []cue.Value) int {
	for idx, alt := range alts {
		if fits(val, alt) {
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
