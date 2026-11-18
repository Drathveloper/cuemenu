# cuemenu

A `menuconfig`-style editor for configurations written in [CUE](https://cuelang.org). The CUE schema defines what can be configured; `cuemenu` turns it into menus, validates every value as you type with the official CUE evaluator, and writes the final configuration as JSON or YAML.

```
go install github.com/Drathveloper/cuemenu@latest
```

## Usage

```sh
cuemenu [DIR]                                 # edit (DIR: a CUE package, default ".")
cuemenu tree DIR                              # print the menu tree, no UI
cuemenu check DIR                             # exit code 1 if anything is missing (for CI)
cuemenu export [-f json|yaml] [-o FILE] DIR   # write the final configuration
cuemenu version                               # version, commit and build date
```

- **Where changes go:** `DIR/.config.cue` holds only what you changed; everything else comes from the schema defaults, like the kernel's `.config`. CUE ignores files starting with `.`, so it never interferes with the package.
- **`export` format:** taken from the extension of `-o` (`.yaml`/`.yml`), JSON otherwise. The file is written atomically, with mode 0600 if it contains a secret.
- **Stale values:** values that no longer fit the schema (e.g. a field of a variant that is no longer chosen) are dropped on load, with a warning.

## Keys

| Key | Action |
|---|---|
| `↑↓` `⏎` `esc` | move, open or edit, go back |
| `space` | toggle a boolean |
| `e` | edit any field as a CUE expression |
| `r` | reset to the default value |
| `a` `d` `K`/`J` | in lists and maps: add, delete, move |
| `/` | search by name, path or documentation |
| `s` `q` | save, quit (asks if there are unsaved changes) |

## How each construct is shown

| In the schema | In the menu |
|---|---|
| `bool` | `[*]` / `[ ]` |
| `*"a" \| "b"` | list of options |
| `int & >=1 & <=65535`, `string`, validators | field validated as you type |
| struct | submenu |
| `[...T]` | editable list |
| `[string]: T`, `[=~"^X-"]: T` | map: keys are checked against the pattern |
| `#A \| #B` | variant: choosing one changes the fields |
| computed or fixed field | read-only |
| `if`, `for` | fields appear and disappear as values change |
| anything else | CUE expression editor |

A field's documentation comes from its `//` comments. `@menu(secret)` hides the value on screen.

## Writing schemas

- **Undeclared fields:** a regular struct only takes the fields it declares. To accept arbitrary fields, declare it with `...` or as `_`.
- **Variants without a discriminator field:** use closed definitions (`#A | #B`). With open structs, CUE cannot tell which variant was chosen.
- **Imports:** do not name a field like an import (`tls: tls.#Settings` hides the import); give the import an alias (`tlscfg "…/tls"`).

## Development

Build, test and lint tasks come from [go-build-tools](https://github.com/Drathveloper/go-build-tools), included as a submodule in `.parent`:

```sh
git clone --recursive <repo>            # or, in an existing clone: git submodule update --init
make build                              # bin/cuemenu, with version, commit and date (cuemenu version)
make test                               # tests with coverage in reports/
make lint                               # golangci-lint with the shared strict configuration
```

The code passes `make lint` with no findings. Every `//nolint` states its reason next to it.

## Releases

Every push to `master` runs lint, tests and the build (the go-build-tools workflow) and, if they pass, publishes a release with notes listing the commits since the previous one. The patch version goes up, or the minor or major one when a pushed commit message has a line that is just `#minor` or `#major` (a mention inside a sentence does not count). Each release has binaries for Linux and macOS (amd64, arm64) and Windows (amd64), with their `checksums.txt`; `make dist` builds the same files locally.

## Status

The conformance matrix (`model/conformance_test.go`) checks 75 constructs of the CUE specification. `PLAN.md` describes the design, the coverage, the deliberate deviations from CUE and what was learned about the CUE API.
