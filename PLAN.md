# cuemenu — plan

A menuconfig-style editor (Go + bubbletea) for configurations written in CUE.

## Decisions (2026-11-18)

- **Input:** a CUE package (several related files, with `cue.mod` and imports) describing the whole configuration of a service. Loaded with `load.Instances`.
- **State:** `DIR/.config.cue` keeps only the values the user changed, like the kernel's `.config`. The CUE loader ignores files starting with `.`, so it never mixes with the schema. It is written with mode 0600 because it may hold secrets.
- **Save and export:** saving always works, even with an incomplete configuration. Exporting requires it to be complete: every required field filled in and no abstract value left.
- **Output:** configurable; for now `-f json|yaml` and `-o FILE`.
- **CUE support:** all evaluation is done by `cuelang.org/go` (v0.17.1, pinned). Each field gets a specific control or, if none fits, the CUE expression editor.

## Phases

0. ✅ Proof of concept: `cuemenu tree DIR` prints the tree with the control assigned to each field.
1. ✅ Model: `Config.Set/Unset/Tree/Save/Load/Export`, errors per path and dropping of edits that stop applying. CLI: `cuemenu tree DIR` and `cuemenu export DIR json|yaml`.
2. ✅ TUI (bubbletea v2): menuconfig navigation, inline errors, `/` search ranked by relevance, save with `s` and quit with `q` (asks if there are unsaved changes). Instead of a `?` help window, a panel under the list always shows the path, documentation, constraint and errors of the selected field. Brought forward from phases 3 and 4:
   - A variant is chosen on its discriminator (`database --->` › `type (postgres)`).
   - CUE expression editor (`e`) on any editable field. It is the default control for lists, mixed kinds and `null`, and for now also the way to edit whole maps.
3. ✅ Lists: submenu with one element per line; `a` adds (with the control of the element type: options, text, or CUE for structs), `⏎` edits, `d` deletes and `K`/`J` move. Maps: `a` asks for the key, checks it against the patterns (regexps included) and then asks for the value, or opens the entry if it is a struct; `d` deletes the entry. Variants without a discriminator: a "variant" chooser, and if the variant is told apart by a field of its own, that field is asked for.
4. ✅ Conformance matrix: `model/conformance_test.go`, with 75 rows, one per construct of the specification.
5. ✅ `cuemenu check DIR` (exit code 1 if incomplete, with the position in the schema of each problem) and `cuemenu export [-f json|yaml] [-o FILE] DIR` (format taken from the extension; flags go before or after DIR). Files are written atomically (0600 temp file + rename), and the export is 0600 if it contains secrets. Exit codes: 0 ok, 1 error or incomplete configuration, 2 bad usage.

## Phase 0 findings (CUE API v0.17.1)

- `string | *"x"` and `bool | *false` are **not** seen as disjunctions: `Expr()` returns `NoOp` because the default is absorbed. The kind comes from `IncompleteKind()` and the value from `Default()`.
- Enums and struct disjunctions do return `OrOp` with each alternative.
- Computed fields (interpolations, references) and fixed ones are concrete and have no default, so they are shown read-only.
- Pattern constraints (`[K]: T`) are found by iterating `Fields(cue.Patterns(true))` (see phase 3).
- Variants are named after the first field holding a concrete string that differs in each one (e.g. `type`).
- Dependencies come for free: a `FillPath` followed by rebuilding the tree makes the fields of an `if` appear or disappear.

## Phase 1 findings

- **Each field is classified without its own edit** (schema plus the other edits). Otherwise an edited field would be concrete and look read-only. The value shown does come from the configuration with every edit.
- **`Set` drops first, then validates.** It first removes the edits that stop applying (e.g. the fields of a variant no longer chosen), then rejects the change if any error appears that did not exist before. An undeclared field (CUE would accept it in an open struct) is rejected.
- **API pitfalls (v0.17.1)** handled by `find` in `lookup.go`:
  - After a `FillPath` the value becomes an `AndOp`, and `Expr()` reflects the expression, not the evaluated result.
  - `LookupPath` does not find `x?` or `x!` without a value (the selector needs its marker), and `x?` also finds `x!` fields. The real marker comes from iterating the fields of the parent struct.
  - `LookupPath` does not look inside unresolved disjunctions. Alternatives are searched **by unifying them with the value** and discarding those that fail; searching them by syntax alone found fields of variants already ruled out.
  - On a disjunction the field iterator does list the fields (those of the default), but its selectors do not work with `LookupPath` and it sometimes loses attributes.
- **Cost:** every edited field is re-evaluated without its edit (O(edits × evaluation)). Fine while there are few edits; if it gets slow, `without(p)` can be cached per version of the edits.

## Phase 2 findings

- **Bugs found testing in a real terminal and with the session tests:**
  - `child()` shared the selector array between siblings (`append` aliasing), so from the third level down fields overwrote each other.
  - An edit on an ancestor (a whole map) made its leaves read-only. `edited` and `without` now consider edits above and below the field.
  - After a change that adds fields above (an `if`), the cursor moved to another field. It is now restored by path.
  - A rejected value only showed the header "N errors in empty disjunction:". It now shows the causes.
- **Testing:** the `tui/` tests send key presses straight to `Update`, with no terminal or teatest. Manual testing used a pty and `pyte` (no tmux available).
- **Known limit:** CUE expressions typed by the user cannot reference other fields of the configuration. If needed, they could be compiled with `cue.Scope`.

## Phase 3 findings

- **`Set` stores structs as their leaves.** No edit sits inside another, so deleting a map entry does not affect the others. An empty `{}` is stored as is, because it creates an entry with no fields yet.
- **Patterns:** `LookupPath(AnyString)` does not see regexp patterns. They are iterated with `Fields(cue.Patterns(true))` and a key is checked by unifying it with `Pattern()`. `Pattern()` may only be called on a `PatternConstraint` selector; on any other selector the returned value panics when used.
- **Lists:** the element type comes from `LookupPath(AnyIndex)` on the arguments of `Expr()` when the list has a default. Lists are always edited whole (`SetList`), so there are never edits on an index.
- **Variants without a discriminator:** each variant is named after its own fields (e.g. `memory` or `redis`). It is chosen by filling in one of its own fields (required ones first), and pruning drops the fields of the other one. This requires the variants to be **closed definitions**: with open structs CUE cannot tell them apart and the choice stays on the default.
- **Flaky test found:** pruning iterates a Go map in random order, and between two edits that exclude each other it could drop the new one (it failed 27 times out of 30). Edits just made now take priority. Tests are run with `-count=30`.
- **Known limits:** struct elements of a list are edited as CUE text, with no per-field submenu. Expressions cannot reference other fields.

## Phase 4 findings: coverage of the CUE specification

Each row of `model/conformance_test.go` checks which control the field gets and, where it applies, that an edit is accepted or rejected and the resulting value.

| Area of the specification | Constructs covered | Control |
|---|---|---|
| Basic types | `null`, `bool`, `int` (hex, `_`, `Ki`), `float`, `number`, `string` (multiline, raw, escapes), `bytes`, `_` | read-only / toggle / number / text / CUE |
| Constraints | `<` `<=` `>` `>=` `!=`, `=~`, `!~`, standard library validators (`strings`, `list`, `net`, `time`) | that of the base type; CUE validates |
| Disjunctions | enums (nested, numeric), `*` defaults, mixed kinds, `null \| T`, struct variants with and without discriminator, embedded variant `{#A \| #B}` | options / CUE / variant |
| Structs | open, `...`, closed definitions, `close()`, embedding, declarations split across files, empty, `x?` (with default), `x!` | menu |
| Identifiers | `#Def` and `_hidden` (neither shown nor editable), `let`, quoted and unicode labels, field aliases, dynamic fields `(k)` | — |
| Expressions | references, arithmetic, interpolation, standard library functions, `if`, `for` | read-only, or appear and disappear |
| Lists | `[...T]`, closed `[A, B]`, prefix and rest `[A, ...T]`, with validators | list |
| Patterns | `[string]: T`, `[=~re]: T`, `[!~re]: T`, alias `[N=string]: {name: N}` | map |
| Modules | package split across files, import of another package of the module, standard library | — |

**Bugs found by the matrix:**
- Nested enums (`*("x"|"y") | "z"`) were not flattened.
- With a validator (`[...string] & list.MinItems(1)`), `IncompleteKind()` returns `_|_`. The kind is now the intersection of the kinds of the parts of the `&`.
- `_` and `{...}` values rejected fields.
- A path with a hidden label caused a panic.

**Deliberate deviations from CUE semantics:**
- **A regular struct only takes the fields it declares**, even though CUE considers it open. Otherwise dependency pruning would not work: the field of an `if` that no longer holds would still be "allowed" by the root struct and end up in the export. To accept arbitrary fields, the schema declares it with `_` or `...` (detected because `LookupPath(AnyString)` then yields `_`).
- **Ambiguous kind, CUE editor.** A validator whose kind is ambiguous (`net.IPv4` accepts a string or bytes) is edited with the CUE editor. With `net.IPv4 & string` it becomes text.

**CUE pitfall for schema authors** (hit in the example): a field named like an import, as in `tls: tls.#Settings`, hides the import. Give the import an alias (`tlscfg "…/tls"`).

## Phase 5 findings

- **Atomic writes:** `os.WriteFile` keeps the mode of the file it overwrites, so a secret could be written for a moment with 0644 before the `chmod`. `model.WriteFile` now writes a 0600 temp file and renames it; both `Save` and `export -o` use it.
- **Testable CLI:** the CLI is tested by calling `run(args, stdout, stderr)` directly, without building the binary (`main_test.go`).
- **Warnings:** a stale edit in `.config.cue` produces a warning but does not make `check` fail. If CI should fail on it, a `-strict` flag can be added.

## Tooling

- Build, test and lint come from [go-build-tools](https://github.com/Drathveloper/go-build-tools) as a submodule in `.parent`. `make lint` passes with no findings under its strict golangci-lint configuration.
- What the linter found that mattered: `cue.Value.Context` is deprecated, so `Config` owns a `*cue.Context`; map keys are checked with `FillPath` and `...` is detected with `LookupPath(AnyString)`, neither needing a context.
- `golangci-lint run --fix` is not safe here: its `fieldalignment` fix reorders struct fields, which breaks positional literals and drops the field comments.
- The remaining `//nolint` directives each state why the finding does not apply.
