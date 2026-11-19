package gen_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/Drathveloper/cuemenu/gen"
	"github.com/Drathveloper/cuemenu/model"
)

// rootType is the name the generated root type gets in these tests.
const rootType = "Config"

// The example's generated package is what cuegen generates now: its test
// is what checks the generated code against CUE.
func TestExampleUpToDate(t *testing.T) {
	t.Parallel()
	schema, err := model.Load("../examples/webapp")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := gen.Generate(schema, gen.Options{Package: "config", Type: rootType})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../examples/webapp/config/config_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, want) {
		t.Fatal("examples/webapp/config is out of date: run go generate ./examples/webapp/config")
	}
}

func TestNotAStruct(t *testing.T) {
	t.Parallel()
	schema, err := model.Load("testdata/scalar")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := gen.Generate(schema, gen.Options{Package: "config", Type: rootType}); err == nil {
		t.Fatal("a schema that is no struct generated code")
	}
}
