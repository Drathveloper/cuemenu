// cuemenu: a menuconfig-style editor for CUE configurations.
//
//	cuemenu [DIR]                                    edit (default ".")
//	cuemenu tree DIR                                 print the menu tree
//	cuemenu check DIR                                exit 1 if incomplete
//	cuemenu export [-f json|yaml] [-o FILE] DIR      final configuration
//	cuemenu version                                  build information
//	cuemenu help                                     how to use it
//
// DIR is a CUE package. The user's edits live in DIR/.config.cue.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
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
  cuemenu help                                  this help (also -h, --help)

DIR is a CUE package (default "."). Changes are saved in DIR/.config.cue.
`

const help = `cuemenu: a menuconfig-style editor for CUE configurations. It turns a CUE
schema into menus, validates every value as you type and writes the final
configuration as JSON or YAML.

` + usage + `
Commands:
  (none)    Open the editor on DIR. Changes are kept in DIR/.config.cue,
            which holds only the values you changed.
  tree      Print the menu tree with the control of each field, without the UI.
  check     List what is missing and exit with code 1 if the configuration
            is incomplete. Meant for CI.
  export    Print the final configuration, or write it to a file with -o.
  version   Print the version, commit and build date.
  help      Print this help.

Export flags (before or after DIR):
  -f json|yaml   Output format. Default: from the extension of -o, else json.
  -o FILE        Write to FILE instead of the standard output. The file is
                 replaced atomically, with mode 0600 if it holds a secret.

Editor keys:
  ↑ ↓  k j       Move.
  ⏎  → l         Open a menu, edit a field, choose an option.
  esc  ← h       Go back; at the top, quit.
  space          Toggle a boolean.
  e              Edit any field as a CUE expression.
  r              Reset a field to its default.
  a  d  K J      In lists and maps: add, delete, move up and down.
  /              Search by name, path or documentation.
  s  q           Save, quit (asks if there are unsaved changes).

Exit codes: 0 ok, 1 error or incomplete configuration, 2 bad usage.

More: https://github.com/Drathveloper/cuemenu
`

// errUsage reports command-line arguments that make no sense.
var errUsage = errors.New("bad usage")

// Values of the build information when nothing sets them.
const (
	devVersion = "dev"
	unknown    = "unknown"
)

// Build information, set by `make build` with -ldflags -X. When it is not
// set, buildInfo falls back on what Go embeds in the binary.
//
//nolint:gochecknoglobals // -X can only set package-level variables
var (
	Version   = devVersion
	Commit    = unknown
	BuildTime = unknown
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
	if about(args, stdout) {
		return exitOK
	}
	opts, err := parseArgs(args)
	switch {
	case errors.Is(err, flag.ErrHelp): // -h, -help, --help
		sayf(stdout, "%s", help)
		return exitOK
	case err != nil:
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

// about runs the commands that only tell about cuemenu itself: version
// and help. It reports whether args was one of them.
func about(args []string, stdout io.Writer) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "version":
		info, ok := debug.ReadBuildInfo()
		build := buildInfo(info, ok)
		sayf(stdout, "cuemenu %s (commit %s, built %s)\n", build.version, build.commit, build.time)
		return true
	case "help":
		sayf(stdout, "%s", help)
		return true
	}
	return false
}

type options struct {
	cmd    string
	dir    string
	out    string // export -o
	format string // export -f
}

// parseArgs reads [SUBCOMMAND] [flags] [DIR] [flags]: flags may come
// before or after DIR, so `cuemenu export . -o app.yaml` works.
func parseArgs(args []string) (options, error) {
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
	if err != nil {
		return opts, fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() > 0 || opts.cmd != cmdExport && (opts.out != "" || opts.format != "") {
		return opts, errUsage
	}
	return opts, nil
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

type build struct {
	version, commit, time string
}

// buildInfo completes what -ldflags did not set with the data Go embeds:
// `go install module@version` records the module version, and `go build`
// in a git checkout records the commit and its time.
func buildInfo(info *debug.BuildInfo, ok bool) build {
	out := build{version: Version, commit: Commit, time: BuildTime}
	if !ok {
		return out
	}
	if out.version == devVersion && info.Main.Version != "" && info.Main.Version != "(devel)" {
		out.version = info.Main.Version
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && out.commit == unknown {
			out.commit = setting.Value
		}
		if setting.Key == "vcs.time" && out.time == unknown {
			out.time = setting.Value
		}
	}
	return out
}

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
