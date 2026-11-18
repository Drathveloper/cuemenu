// cuemenu: a menuconfig-style editor for CUE configurations.
//
//	cuemenu [DIR]                                    edit (default ".")
//	cuemenu tree DIR                                 print the menu tree
//	cuemenu check DIR                                exit 1 if incomplete
//	cuemenu export [-f json|yaml] [-o FILE] DIR      final configuration
//	cuemenu version                                  build information
//
// DIR is a CUE package. The user's edits live in DIR/.config.cue.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Drathveloper/cuemenu/model"
	"github.com/Drathveloper/cuemenu/tui"
)

const usage = `usage:
  cuemenu [DIR]                                 edit the configuration
  cuemenu tree DIR                              print the menu tree
  cuemenu check DIR                             check it is complete (exit code 1 if not)
  cuemenu export [-f json|yaml] [-o FILE] DIR   write the final configuration
  cuemenu version                               version, commit and build date

DIR is a CUE package (default "."). Changes are saved in DIR/.config.cue.
`

// Build information, set by `make build` with -ldflags -X.
//
//nolint:gochecknoglobals // -X can only set package-level variables
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1 // error, or configuration incomplete
	exitUsage = 2
)

// Subcommands.
const (
	cmdEdit   = "edit"
	cmdTree   = "tree"
	cmdCheck  = "check"
	cmdExport = "export"
)

// Modes of an exported file.
const (
	modePublic = 0o644
	modeSecret = 0o600 // when it carries a secret
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run executes the command line and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	app := cliApp{stdout: stdout, stderr: stderr}
	if len(args) == 1 && args[0] == "version" {
		sayf(stdout, "cuemenu %s (commit %s, built %s)\n", Version, Commit, BuildTime)
		return exitOK
	}
	opts, ok := parseArgs(args)
	if !ok {
		sayf(stderr, "%s", usage)
		return exitUsage
	}
	cfg, code := app.load(opts.dir)
	if code != exitOK {
		return code
	}
	switch opts.cmd {
	case cmdTree:
		printTree(stdout, cfg.Tree(), 0)
		return exitOK
	case cmdCheck:
		return app.check(cfg)
	case cmdExport:
		return app.export(cfg, opts)
	}
	return app.edit(cfg, opts.dir)
}

type options struct {
	cmd    string
	dir    string
	out    string // export -o
	format string // export -f
}

// parseArgs reads [SUBCOMMAND] [flags] [DIR] [flags]: flags may come
// before or after DIR, so `cuemenu export . -o app.yaml` works.
func parseArgs(args []string) (options, bool) {
	opts := options{cmd: cmdEdit, dir: "."}
	if len(args) > 0 && slices.Contains([]string{cmdTree, cmdCheck, cmdExport}, args[0]) {
		opts.cmd, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("cuemenu", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.out, "o", "", "")
	flags.StringVar(&opts.format, "f", "", "")
	err := flags.Parse(args)
	if err == nil && flags.NArg() > 0 {
		opts.dir = flags.Arg(0)
		err = flags.Parse(flags.Args()[1:])
	}
	onlyExport := opts.cmd == cmdExport || opts.out == "" && opts.format == ""
	return opts, err == nil && flags.NArg() == 0 && onlyExport
}

// cliApp runs the subcommands; each prints its own errors and returns
// the exit code.
type cliApp struct {
	stdout, stderr io.Writer
}

func (app cliApp) fail(err error) int {
	sayf(app.stderr, "%v\n", err)
	return exitFail
}

// load reads the schema in dir and the edits saved in dir/.config.cue.
func (app cliApp) load(dir string) (*model.Config, int) {
	schema, err := model.Load(dir)
	if err != nil {
		return nil, app.fail(err)
	}
	cfg := model.New(schema)
	dropped, err := cfg.Load(configFile(dir))
	if err != nil {
		return nil, app.fail(err)
	}
	for _, path := range dropped {
		sayf(app.stderr, "warning: %s no longer applies to the configuration, dropped\n", path)
	}
	return cfg, exitOK
}

func (app cliApp) check(cfg *model.Config) int {
	if err := cfg.Check(); err != nil {
		return app.fail(err)
	}
	sayf(app.stdout, "configuration complete\n")
	return exitOK
}

func (app cliApp) export(cfg *model.Config, opts options) int {
	format := opts.format
	if format == "" {
		format = "json"
		if ext := filepath.Ext(opts.out); ext == ".yaml" || ext == ".yml" {
			format = "yaml"
		}
	}
	data, err := cfg.Export(format)
	if err != nil {
		return app.fail(err)
	}
	if opts.out == "" {
		if _, err := app.stdout.Write(data); err != nil {
			return app.fail(err)
		}
		return exitOK
	}
	// Not readable by others if it carries a secret.
	mode := os.FileMode(modePublic)
	if hasSecret(cfg.Tree()) {
		mode = modeSecret
	}
	if err := model.WriteFile(opts.out, data, mode); err != nil {
		return app.fail(err)
	}
	return exitOK
}

func (app cliApp) edit(cfg *model.Config, dir string) int {
	abs, _ := filepath.Abs(dir)
	if _, err := tea.NewProgram(tui.New(cfg, configFile(dir), filepath.Base(abs))).Run(); err != nil {
		return app.fail(err)
	}
	return exitOK
}

func configFile(dir string) string { return filepath.Join(dir, ".config.cue") }

// sayf writes to stdout or stderr: if that fails there is nowhere to report.
func sayf(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

func hasSecret(nodes []*model.Node) bool {
	for _, node := range nodes {
		if node.Secret && node.Value != "" || hasSecret(node.Children) {
			return true
		}
	}
	return false
}

func printTree(out io.Writer, nodes []*model.Node, depth int) {
	for _, node := range nodes {
		sayf(out, "%s%-60s %-8s %s\n", strings.Repeat("    ", depth), tui.Line(node), node.Widget,
			strings.Join(strings.Fields(node.Hint), " "))
		printTree(out, node.Children, depth+1)
	}
}
