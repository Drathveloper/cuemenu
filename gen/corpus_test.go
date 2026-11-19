package gen_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"

	"github.com/Drathveloper/cuemenu/gen"
)

// TestCorpus generates the code of every schema CUEGEN_CORPUS lists
// (a file of directories, separated by blanks), builds it, and checks
// that it agrees with CUE on random inputs: on what is valid, and on the
// configuration decoded. CUEGEN_INPUTS sets how many (default 40).
func TestCorpus(t *testing.T) {
	list := os.Getenv("CUEGEN_CORPUS")
	if list == "" {
		t.Skip("CUEGEN_CORPUS lists the schema directories")
	}
	data, err := os.ReadFile(list) //nolint:gosec // a list the developer chose
	if err != nil {
		t.Fatal(err)
	}
	inputs := 40
	if n, err := fmt.Sscan(os.Getenv("CUEGEN_INPUTS"), &inputs); n != 1 || err != nil {
		inputs = 40
	}
	modDir := module(t)
	for idx, dir := range strings.Fields(string(data)) {
		t.Run(fmt.Sprintf("%d-%s", idx, filepath.Base(dir)), func(t *testing.T) {
			agree(t, dir, filepath.Join(modDir, fmt.Sprintf("s%d", idx)), inputs)
		})
	}
}

// module is a Go module the generated packages are built in, with the
// versions of this one.
func module(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gomod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	gomod = []byte(strings.Replace(string(gomod), "module github.com/Drathveloper/cuemenu", "module corpus", 1))
	gosum, err := os.ReadFile("../go.sum")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"go.mod": gomod, "go.sum": gosum} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const harness = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	for _, path := range os.Args[1:] {
		data, _ := os.ReadFile(path)
		cfg, err := ParseJSON(data)
		if err != nil {
			fmt.Println("ERR", strings.ReplaceAll(err.Error(), "\n", "; "))
			continue
		}
		out, err := json.Marshal(cfg)
		fmt.Println("OK", string(out), err)
	}
}
`

func agree(t *testing.T, dir, pkgDir string, inputs int) {
	t.Helper()
	ctx := cuecontext.New()
	insts := load.Instances([]string{"."}, &load.Config{Dir: dir})
	if insts[0].Err != nil {
		t.Skip(insts[0].Err)
	}
	schema := ctx.BuildInstance(insts[0])
	if schema.Validate() != nil {
		t.Skip("the schema has errors")
	}
	src, warns, err := gen.Generate(schema, gen.Options{Package: "main", Type: rootType})
	if err != nil {
		t.Skip(err)
	}
	if err = os.MkdirAll(pkgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(pkgDir, "config_gen.go"), src)
	write(t, filepath.Join(pkgDir, "main.go"), []byte(harness))
	bin := filepath.Join(pkgDir, "bin")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // a temporary dir
	build.Dir = pkgDir
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("the generated code does not build:\n%s", out)
	}
	computed := computedKeys(warns)
	fuzz := &fuzzer{rng: rand.New(rand.NewPCG(1, uint64(len(dir)))), skip: computed} //nolint:gosec // reproducible
	cases := make([]string, 0, 1+inputs)
	cases = append(cases, "{}")
	for range inputs {
		data, jsonErr := json.Marshal(fuzz.random(schema, 0))
		if jsonErr != nil {
			t.Fatal(jsonErr)
		}
		cases = append(cases, string(data))
	}
	files := make([]string, len(cases))
	for idx, input := range cases {
		files[idx] = filepath.Join(pkgDir, fmt.Sprintf("in%d.json", idx))
		write(t, files[idx], []byte(input))
	}
	out, err := exec.CommandContext(t.Context(), bin, files...).Output() //nolint:gosec // the binary just built
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	failed := 0
	for idx, input := range cases {
		if failed < maxReported && !compare(t, schema.Unify(ctx.CompileString(input)), input, lines[idx], computed) {
			failed++
		}
	}
}

// maxReported is how many disagreements are reported per schema.
const maxReported = 3

func compare(t *testing.T, val cue.Value, input, goSays string, computed map[string]bool) bool {
	t.Helper()
	cueErr := val.Validate(cue.Concrete(true))
	goOK := strings.HasPrefix(goSays, "OK ")
	if (cueErr == nil) != goOK {
		t.Errorf("input %s\nCUE: %v\nGo:  %s", short(input), short(fmt.Sprint(cueErr)), short(goSays))
		return false
	}
	if !goOK {
		return true
	}
	want, err := val.MarshalJSON()
	if err != nil {
		return true
	}
	got := strings.TrimSuffix(strings.TrimPrefix(goSays, "OK "), " <nil>")
	if w, g := normalize(want, computed), normalize([]byte(got), computed); !reflect.DeepEqual(w, g) {
		t.Errorf("input %s decoded differently at %s\nCUE %s\nGo  %s", short(input), firstDiff(w, g, ""),
			short(string(want)), short(got))
		return false
	}
	return true
}

// firstDiff is where a and b first differ, and how.
func firstDiff(a, b any, path string) string {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := make([]string, 0, len(am)+len(bm))
		for key := range am {
			keys = append(keys, key)
		}
		for key := range bm {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range slices.Compact(keys) {
			if !reflect.DeepEqual(am[key], bm[key]) {
				return firstDiff(am[key], bm[key], path+"."+key)
			}
		}
	}
	return fmt.Sprintf("%s: CUE %v, Go %v", path, a, b)
}

func short(text string) string {
	const maxLen = 400
	if len(text) > maxLen {
		return text[:maxLen] + "…"
	}
	return text
}

// computedKeys are the fields the warnings say are computed by the
// schema: Go decodes them, but does not compute them.
func computedKeys(warns []string) map[string]bool {
	out := map[string]bool{}
	for _, warn := range warns {
		if path, ok := strings.CutSuffix(warn, ": computed by the schema: decoded, not checked"); ok {
			out[path[strings.LastIndex(path, ".")+1:]] = true
		}
	}
	return out
}

func normalize(data []byte, computed map[string]bool) any {
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return err.Error()
	}
	return prune(out, computed)
}

// prune drops what one side leaves out and the other writes empty.
func prune(val any, computed map[string]bool) any {
	switch x := val.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range x {
			if item = prune(item, computed); item != nil && !computed[key] && key != "bogus" { // Go drops unknown keys
				out[key] = item
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(x) == 0 {
			return nil
		}
		for idx := range x {
			x[idx] = prune(x[idx], computed)
		}
	}
	return val
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Values the random inputs take: typical ones, and ones that break
// typical constraints.
var (
	someStrings = []string{"", "x", "abc", "https://a.b", "http://a", "1h", "forever", "a@b.c", "X-a", "10.0.0.1",
		"2024-01-01T00:00:00Z", "/tmp/x", "a b"}
	someNumbers = []any{0, 1, -1, 3, 8, 80, 443, 65536, 1.5}
	someKeys    = []string{"a", "X-a", "b-c", "0"}
)

// fuzzer makes random inputs; it leaves out the computed fields, which
// Go does not compute (a known gap, warned).
type fuzzer struct {
	rng  *rand.Rand
	skip map[string]bool
}

// random is a JSON value for val: mostly what the schema allows, with
// mistakes now and then.
func (f *fuzzer) random(val cue.Value, depth int) any {
	rng := f.rng
	if depth > 5 {
		return nil
	}
	if def, ok := val.Default(); ok && rng.IntN(3) == 0 {
		if out, ok := export(def); ok {
			return out
		}
	}
	if val.IsConcrete() && val.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0 && rng.IntN(10) > 0 {
		if out, ok := export(val); ok {
			return out
		}
	}
	if operator, args := val.Expr(); operator == cue.OrOp && len(args) > 0 {
		val = args[rng.IntN(len(args))]
		if out, ok := export(val); ok && val.IsConcrete() && val.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0 {
			return out
		}
	}
	return f.ofKind(val, pickKind(rng, val.IncompleteKind()), depth)
}

func export(val cue.Value) (any, bool) {
	data, err := val.MarshalJSON()
	if err != nil {
		return nil, false
	}
	var out any
	return out, json.Unmarshal(data, &out) == nil
}

func pickKind(rng *rand.Rand, kinds cue.Kind) cue.Kind {
	var all []cue.Kind
	for _, kind := range []cue.Kind{cue.StructKind, cue.ListKind, cue.StringKind, cue.IntKind, cue.FloatKind,
		cue.BoolKind, cue.NullKind} {
		if kinds&kind != 0 {
			all = append(all, kind)
		}
	}
	if len(all) == 0 || rng.IntN(25) == 0 { // a mistake
		return []cue.Kind{cue.StringKind, cue.IntKind, cue.BoolKind}[rng.IntN(3)]
	}
	return all[rng.IntN(len(all))]
}

func (f *fuzzer) ofKind(val cue.Value, kind cue.Kind, depth int) any {
	rng := f.rng
	switch kind {
	case cue.StructKind:
		return f.structure(val, depth)
	case cue.ListKind:
		elem := val.LookupPath(cue.MakePath(cue.AnyIndex))
		out := []any{}
		for range rng.IntN(3) {
			out = append(out, f.random(elem, depth+1))
		}
		return out
	case cue.StringKind:
		return someStrings[rng.IntN(len(someStrings))]
	case cue.IntKind, cue.FloatKind:
		return someNumbers[rng.IntN(len(someNumbers))]
	case cue.BoolKind:
		return rng.IntN(2) == 0
	default:
		return nil
	}
}

func (f *fuzzer) structure(val cue.Value, depth int) map[string]any {
	rng := f.rng
	out := map[string]any{}
	iter, err := val.Fields(cue.Optional(true))
	for err == nil && iter.Next() {
		sel := iter.Selector()
		keep := 9
		if sel.ConstraintType() == cue.OptionalConstraint {
			keep = 4
		}
		if rng.IntN(10) < keep && !f.skip[sel.Unquoted()] {
			out[sel.Unquoted()] = f.random(iter.Value(), depth+1)
		}
	}
	patterns := false
	iter, err = val.Fields(cue.Patterns(true))
	for err == nil && iter.Next() {
		if sel := iter.Selector(); sel.ConstraintType() == cue.PatternConstraint {
			patterns = true
			for range rng.IntN(3) {
				// keys outside the pattern of an open map are a known gap (warned)
				if key := someKeys[rng.IntN(len(someKeys))]; sel.Pattern().Unify(val.Context().CompileString(strconv.Quote(key))).Err() == nil { //nolint:staticcheck // the test owns no context
					out[key] = f.random(iter.Value(), depth+1)
				}
			}
		}
	}
	if !patterns && rng.IntN(20) == 0 { // open maps with a key pattern are a known gap (warned)
		out["bogus"] = 1
	}
	return out
}
