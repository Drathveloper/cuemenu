// cuegen writes the Go types that receive the configuration a CUE schema
// describes: decoded from JSON or YAML with the schema's defaults, and
// validated with go-playground/validator tags, or generated code where a
// tag cannot say it. What it cannot translate is reported as a warning.
//
//	cuegen [-pkg NAME] [-type NAME] [-e PATH] [-o FILE] [-koanf] [DIR]
//	cuegen -version
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"cuelang.org/go/cue"

	"github.com/Drathveloper/cuemenu/gen"
	"github.com/Drathveloper/cuemenu/internal/buildinfo"
	"github.com/Drathveloper/cuemenu/model"
)

// Build information, set by `make build` with -ldflags -X. When it is not
// set, buildinfo falls back on what Go embeds in the binary.
//
//nolint:gochecknoglobals // -X can only set package-level variables
var (
	Version   = buildinfo.Dev
	Commit    = buildinfo.Unknown
	BuildTime = buildinfo.Unknown
)

const usage = `usage: cuegen [-pkg NAME] [-type NAME] [-e PATH] [-o FILE] [-koanf] [DIR]
       cuegen -version

Writes Go types for the configuration that the CUE package in DIR
(default ".") describes, with ParseJSON, ParseYAML and Validate. The
generated code needs github.com/go-playground/validator/v10 and
go.yaml.in/yaml/v3. With -koanf it also has Load, which takes the
configuration from github.com/knadh/koanf/v2 (files, environment
variables...). What the schema says that the code does not check is
reported as a warning.

`

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const modeSource = 0o644

var errNotFound = errors.New("not in the package")

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cuegen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		sayf(stderr, "%s", usage)
		flags.PrintDefaults()
	}
	pkg := flags.String("pkg", "config", "package `name` of the generated code")
	typ := flags.String("type", "Config", "`name` of the root type")
	expr := flags.String("e", "", "generate for this `path` of the package (such as #Config) instead of all of it")
	out := flags.String("o", "", "write to `FILE` instead of the standard output")
	koanf := flags.Bool("koanf", false, "also generate Load, from a github.com/knadh/koanf/v2 instance")
	version := flags.Bool("version", false, "print the version, commit and build date")
	switch err := flags.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case err != nil:
		return exitUsage
	case flags.NArg() > 1:
		flags.Usage()
		return exitUsage
	case *version:
		sayf(stdout, "%s\n", buildinfo.Describe("cuegen", Version, Commit, BuildTime))
		return exitOK
	}
	dir := "."
	if flags.NArg() == 1 {
		dir = flags.Arg(0)
	}
	src, err := generate(dir, *expr, gen.Options{Package: *pkg, Type: *typ, Koanf: *koanf}, stderr)
	if err == nil && *out != "" {
		err = model.WriteFile(*out, src, modeSource)
	} else if err == nil {
		_, err = stdout.Write(src)
	}
	if err != nil {
		sayf(stderr, "cuegen: %v\n", err)
		return exitFail
	}
	return exitOK
}

func generate(dir, expr string, opts gen.Options, stderr io.Writer) ([]byte, error) {
	schema, err := model.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("%w", err) // it says what it was loading
	}
	if expr != "" {
		if schema = schema.LookupPath(cue.ParsePath(expr)); !schema.Exists() {
			return nil, fmt.Errorf("%s: %w", expr, errNotFound)
		}
	}
	src, warns, err := gen.Generate(schema, opts)
	for _, warn := range warns {
		sayf(stderr, "cuegen: warning: %s\n", warn)
	}
	if err != nil {
		return nil, fmt.Errorf("%w", err) // it says what is wrong with the schema
	}
	return src, nil
}

// sayf writes to stderr: if that fails there is nowhere to report.
func sayf(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}
