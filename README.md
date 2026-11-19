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
cuemenu help                                  # full help: commands, flags, editor keys (also -h)
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
| `a` `d` `K`/`J` | in lists, maps and structs with `...`: add, delete, move |
| `/` | search by name, path or documentation |
| `s` `q` | save, quit (asks if there are unsaved changes) |

## How each construct is shown

| In the schema | In the menu |
|---|---|
| `bool` | `[*]` / `[ ]` |
| `*"a" \| "b"` | list of options |
| `int & >=1 & <=65535`, `string`, validators | field validated as you type |
| struct | submenu |
| `[...T]` | editable list; a struct element is a submenu of its own |
| `[string]: T`, `[=~"^X-"]: T` | map: keys are checked against the pattern |
| `#A \| #B` | variant: choosing one changes the fields |
| `matchN(1, [A, B])`, `matchN(>=1, …)`, `matchN(N, …)` (JSON Schema's oneOf, anyOf, allOf as `cue import` writes them) | like `A \| B`; allOf parts add their fields |
| computed or fixed field | read-only |
| `if`, `for` | fields appear and disappear as values change |
| a package that is `[string]: T` or `#A \| #B` at its top level | the top menu is that map or variant |
| anything else | CUE expression editor |

A field's documentation comes from its `//` comments. `@menu(secret)` hides the value on screen.

## Writing schemas

- **Undeclared fields:** a regular struct only takes the fields it declares. To accept arbitrary fields, declare it with `...` (`a` then adds a field) or as `_`.
- **Variants without a discriminator field:** use closed definitions (`#A | #B`). With open structs, CUE cannot tell which variant was chosen.
- **Imports:** do not name a field like an import (`tls: tls.#Settings` hides the import); give the import an alias (`tlscfg "…/tls"`).

## Generating Go code: cuegen

`cuegen` writes the Go types that receive the configuration a schema describes, so the program that reads it takes its defaults and its validation from the same CUE package:

```sh
go install github.com/Drathveloper/cuemenu/cmd/cuegen@latest
cuegen [-pkg config] [-type Config] [-e '#Config'] [-o config_gen.go] [-koanf] DIR
cuegen -version
```

The generated code has `ParseJSON`, `ParseYAML` and `Validate`, and needs `github.com/go-playground/validator/v10` and `go.yaml.in/yaml/v3`. `examples/webapp/config` is what it generates for the example, with `-koanf` (`go generate` updates it); its test gives the same inputs to CUE and to the generated code and checks that they agree, on what is valid and on what is decoded.

- **Defaults:** decoding starts from `NewConfig()`, so what the input leaves out keeps the schema's default. Fields that only exist under an `if` get theirs after decoding, from the field they depend on.
- **Validator tags** where they can say it: kinds and bounds (`gte`, `lt`…), enums (`oneof`), required fields, rune and item counts (`strings.MinRunes`, `list.MinItems`, `[_, ...]`), `net.IP`, prefixes and suffixes. Required and optional fields are pointers, so that a `0` or `""` given is told from one left out.
- **Generated code** for the rest: regular expressions, `time.Duration`, `time.Time`, `net.URL`, what an `if` branch requires or forbids, JSON Schema's oneOf of required keys, unknown fields of closed structs, `null` where the schema does not allow it, map key patterns.
- **Variants** (`#A | #B`, JSON Schema's oneOf) are Go interfaces: decoding picks the variant by its discriminator field, or else, as CUE does, the one the value fits (the default if it fits several).
- **Patterns next to fields** (`{name: string, [string]: string}`): the keys the struct does not declare go to a `Rest` map, and are written back.
- **koanf:** with `-koanf` the code also has `Load(k *koanf.Koanf)`, to take the configuration from [koanf](https://github.com/knadh/koanf) (a file plus secrets in environment variables, say) with the same decoding and validation. Text where the schema expects a number or a boolean is converted, a list is split at commas, and keys match fields whatever their case, so environment variables can name camelCase fields. Keys with a `.` need another koanf delimiter (cuegen warns about declared ones).

  ```go
  k := koanf.New(".")
  k.Load(file.Provider("config.yaml"), yaml.Parser())
  k.Load(env.Provider(".", env.Opt{Prefix: "APP_", TransformFunc: func(key, value string) (string, any) {
  	// APP_DATABASE__PASSWORD -> database.password
  	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(key, "APP_")), "__", "."), value
  }}), nil)
  cfg, err := config.Load(k)
  ```
- **Warnings:** what the code does not check is printed as a warning, never accepted silently. Examples: fields the schema computes (`"\(a)/\(b)"`, `[for …]`), alternatives of several kinds (only the kind is checked), JSON Schema's `not`.

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

Every push to `master` runs lint, tests and the build (the go-build-tools workflow) and, if they pass, publishes a release with notes listing the commits since the previous one. The patch version goes up, or the minor or major one when a pushed commit message has a line that is just `#minor` or `#major` (a mention inside a sentence does not count). Each release has binaries of `cuemenu` and `cuegen` for Linux and macOS (amd64, arm64) and Windows (amd64), with their `checksums.txt`; `make dist` builds the same files locally.

## Status

The conformance matrix (`model/conformance_test.go`) checks 75 constructs of the CUE specification. `PLAN.md` describes the design, the coverage, the deliberate deviations from CUE and what was learned about the CUE API.
