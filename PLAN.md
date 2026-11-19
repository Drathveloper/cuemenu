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
   - CUE expression editor (`e`) on any editable field. It is the default control for mixed kinds (other than `null | T`, edited as `T`), and for now also the way to edit whole maps.
3. ✅ Lists: submenu with one element per line; `a` adds (with the control of the element type: options or text; a struct, variant or map element is added empty and opened as a menu of its own), `⏎` edits or opens, `d` deletes and `K`/`J` move. Maps: `a` asks for the key, checks it against the patterns (regexps included) and then asks for the value, or opens the entry if it is a struct; `d` deletes the entry. Variants without a discriminator: a "variant" chooser, and if the variant is told apart by a field of its own, that field is asked for.
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
- **Lists:** the element type comes from `LookupPath(AnyIndex)` on the arguments of `Expr()` when the list has a default. Lists are always edited whole (`SetList`), so there are never edits on an index: setting a field inside an element (`routes[0].matchers[1].name`) rebuilds that element from what the user wrote in it, then sets the list (see "Struct elements" below).
- **Variants without a discriminator:** each variant is named after its own fields (e.g. `memory` or `redis`). It is chosen by filling in one of its own fields (required ones first), and pruning drops the fields of the other one. This requires the variants to be **closed definitions**: with open structs CUE cannot tell them apart and the choice stays on the default.
- **Flaky test found:** pruning iterates a Go map in random order, and between two edits that exclude each other it could drop the new one (it failed 27 times out of 30). Edits just made now take priority. Tests are run with `-count=30`.
- **Known limits:** expressions cannot reference other fields.

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

## Real-world schemas (Grafana, Timoni, KubeVela)

Tried on Grafana panel options and dashboard v2beta1, the Timoni redis example (`#Config`) and the `parameter` block of the 48 KubeVela definitions that use no `vela/*` builtins. Two bugs, both fixed:

- **References into another package:** for `x: pkg.Name`, and for a plain reference inside an imported package (Grafana's `displayMode?: LegendDisplayMode` in `common`), `Expr()` returns a `SelectorOp` instead of the disjunction behind it, so enums showed as text and variants as an empty menu. The `Expr()` base of an intra-package reference has no fields and cannot be looked up; `ReferencePath()` resolves both cases. Every `Expr()` call in `model` goes through `expr()`.
- **`if` on an unset required field** (Timoni's `#Image`: `if digest != "" && tag != ""`): the struct's `IncompleteKind()` is bottom, so it fell back to the expression editor. A bottom value that lists at least one field is a struct. `Fields()` alone does not fail on bottom scalars such as `n + 1`, hence the extra check.

Also fixed after that run:

- **Fields closed out by another definition:** Timoni's `resources` is `corev1.#ResourceRequirements` and the closed `timoniv1.#ResourceRequirements`, so `claims?` is listed by `Fields()` but is "field not allowed": it showed as read-only with the raw CUE of `[...#ResourceClaim]` (with its `let`) spread over several lines. Such optional fields are now left out of the menu. `Allows()` returns true for them; `Validate()` (without `Concrete`) reports only permanent errors, so a required field still missing is kept.
- **`*null | [...T]`** (KubeVela's `command`, `args`): a list that may be left null. It was the expression editor; now it is a list showing `(null)`, and `r` puts the null back.
- **One line per entry:** values are compacted to one line in the menu and in `tree`, and constraints are clipped at 100 characters (`tui.Hint`): the element type of the dashboard's `variables` list is about 4000 characters, which also flooded the details under the TUI menu.
- **The top level of the package** need not be a struct with fields. KubeVela's `parameter` can be a map (`labels`: `[string]: string | null`), a variant (`container-image`: `#PatchParams | close({containers: [...#PatchParams]})`) or `...` (`json-merge-patch`); the tree was empty. `Config.Root()` classifies the root like any field (its path is empty, which `find` now accepts) and the TUI uses it as the container of the top menu, so a map, a variant or a struct with `...` work there as anywhere else. `tree` prints a `(root)` line when the root is not a plain menu.
- **A file's top-level disjunction is not an `OrOp`:** "#A: …, #B: …, #A | #B" evaluates as `{#A, #B} & (#A | #B)` (a `let` gives the same shape). `expr()` returns the disjunction when the other conjuncts declare no fields.
- **Structs with `...` take new fields with `a`** (`Node.Open`, `Config.Entry` falls back to `declaredOpen`), at the root or below; before, only `e` could add them.
- **Variants without a default were shown as chosen** (the first alternative, since the unresolved value unifies with it) and, with a discriminator, had no entry to choose from at all. While several alternatives still fit, nothing is chosen, and the discriminator is offered as a choice even before it exists in the value.
- **Maps with default entries** (Timoni's `nodeSelector: *{"kubernetes.io/os": "linux"} | {[string]: string}`) were a variant named "variant 1" with a read-only entry. A disjunction with one map alternative, where every other is a concrete struct that fits in it, is now a map: its hint is the map's pattern, the default's entries are edited with that pattern, and `a` adds keys. A default that does not fit keeps it a variant. CUE semantics, kept as is: new keys add to the default entries rather than replace them, so a default entry cannot be removed, only changed (which drops the default); and the default is an open struct, so `y: 2` is accepted through it even though the map says `string` (the TUI types new entries as text from the pattern; only `e` can enter a number).

- **Computed fields waiting for their inputs** (Timoni's `clusterVersion.major`, computed from `kubeVersion` with `strconv.Atoi`, or a label that is `name & …`) were editable expressions until the input was set; a value typed there would make the later input be rejected. While a required input is missing such a field has no kind at all (`IncompleteKind()` is bottom), whereas a type or a validator keeps one (`a: Mode`, `string & strings.MaxRunes(3)`), so a bottom field that is not a struct is now read-only, shown as `(computed)` with its formula as hint.
- **Values on one line, also in the editor:** strings are unquoted with `literal.Unquote` (`"""…"""` and `#"raw"#` were shown, and edited, with their quotes) and values are clipped at 40 characters in the menu. The editor is one line and `textinput` turns line breaks into spaces: a multi-line text was silently flattened on Enter, and a struct edited with `e` became invalid CUE (`{ a: 1 b: 2 }`). Multi-line text is now edited as an escaped CUE string, and multi-line values reach the editor as JSON, which is CUE on one line.

## Manual TUI pass and more schemas (Holos, Grafana app kinds)

The TUI was driven by hand (pty + `pyte`) on Timoni `redis`, KubeVela `webservice` and Grafana's `DashboardSpec` v2beta1, and `tree` was run on Holos v1beta1 (`author.#Helm`, `#Kustomize`, `#Kubernetes`, `#Platform`; `core.#Task`, `#TaskSet`, `#Platform`, `#Helm`) and on the 41 `*Spec` kinds of Grafana's apps (alerting, iam, secret, correlations, dashboard). All of them load, and none falls back to the expression editor any more. Fixed:

- **The screen grew past the terminal:** the menu got the terminal height minus a fixed 9 rows, but the details under it vary (documentation, constraint, editor, errors). In a 30-row terminal the error of a rejected value was below the screen. The footer is now wrapped to the terminal width and measured, the menu gets the rows left, and if the footer alone does not fit its first lines (documentation) are dropped, so that the editor, its errors and the help stay visible. Choosers and search results scroll too (a choice with 30 options overflowed).
- **Attributes in values:** a list element from the Kubernetes types showed as `{ key: "a" @go(Key) @protobuf(…) }`. `Syntax` now leaves attributes out.
- **New map entries that are variants or maps** (the dashboard's `elements: [string]: Element | *{}`) opened an empty CUE editor. Like struct entries, they are now created and opened, so the variant is chosen from its menu.
- **Incomplete is not an error at load time:** Holos' `core.#Task` has `if kind == "Helm" {helm!: …}` for each kind. While `kind` is not chosen the comprehensions cannot be evaluated, and `Load` failed with "unresolved disjunction". `Load` now uses `Validate()`, which reports only permanent errors, and a conjunction (`p.#Task` embedded at the root) whose kind is bottom but lists fields is a struct. Choosing `kind` then shows only the fields of that kind. `check` printed the same error once per `if`; each message is now shown once.
- **Go-generated enums** (`kind: string & ("Resources" | "Helm" | …)`) were text: the disjunction is now found when the other conjuncts are types that every alternative has.
- **Go pointers, `null | T`** (`cue get go`; about 270 fields in the corpus: `null | int`, `null | bool`, `null | string`, `null | #Probe`…) were all expression editors. They now get the widget of `T`, and while not set, a `null | #Struct` menu lists the fields of `#Struct`. As with `*null | [...T]`, `r` puts the null back; `e` can still type `null`.
- **Boolean disjunctions** (`*false | true`, also as `bool & (*false | true)`) are a toggle instead of a two-option choice.
- **Small TUI fixes:** the prompt for a new key showed an empty `[]` details line.

Notes:

- `pyte` 0.8.2 mishandles `insert_lines`/`delete_lines` (a target row is not cleared when its source row was never written) and lacks `CSI S`/`CSI T`, which bubbletea's renderer uses. Without patching it, the manual tests show stale lines that a real terminal does not.
- Variants that cannot be told apart, such as `Element | *{}` (where `{}` has no `kind`), are named `variant 1…n`.

## Struct elements of lists

The gap most real schemas hit (Grafana's routes, matchers, permissions, layout items; Kubernetes tolerations, ports): a struct element was typed whole as CUE, and saved with every default copied in.

- **A struct element is a menu** of its fields, built like any struct, recursively: a list inside an element (matchers of a route) is a list again, a variant element has its chooser or discriminator, a map element takes keys. `a` adds `{}` and opens it; `e` still edits the whole element as CUE.
- **Lists are still one edit.** `Set` and `Unset` on a path inside an element split it at the last index (`inList`) and rebuild that element from its leaves as the user wrote them (`written`: the list edit, or the one of an enclosing list), plus the change, then set the list. Nested lists recurse through `Set`. Only what was set is saved: no defaults, no placeholders.
- **Variants in elements:** like `prune` for edits, the fields kept in an element that no longer exist once the new ones are set (those of a variant no longer chosen) are dropped (`pruneElem`).
- **The widget of a field inside an element** comes from the element constraint (`field` follows index selectors through `elemAt`), so a field of an element from a schema default list is editable, not read-only.
- **Recursive schemas** (`#Route: {routes?: [...#Route]}`): the element type of a nested list is a structural cycle when looked up on its own; unified with a `{}` element it is the right struct. Its hint, which prints as `[..._|_]`, is not shown.
- The summary of an element shows what the user wrote, not the schema merged in (`continue: bool`).
- **Errors inside elements:** in CUE an element with a field in error is bottom, and so is its list: `List()` fails and the elements vanished from the menu, with the error shown nowhere (only `check` saw it, e.g. after editing `.config.cue` by hand). Elements are now looked up by index, which still works, and their fields by iteration, which does too; errors reported as `routes.0.receiver` are matched to `routes[0].receiver`. A field in error shows the value written (`1`), not `_|_ // …`, anywhere in the tree.
- Struct variants in lists without a discriminator are named by their own fields, as elsewhere; `variant 1…n` is only for alternatives with no field of their own (`Element | *{}`).
- Tried by hand on Grafana's `RouteTreeSpec` (three levels of routes, matchers, toggles) and a list of discriminated layout items; the 134 corpus schemas load with identical trees.

## JSON Schema and the CUE registry

Tried on the curated modules of the CUE Central Registry (`cue.dev/x`: GitHub Actions, GitLab CI, Buildkite, Azure Pipelines, the 71 Kubernetes `api/*/v1` kinds and 45 CRDs of cert-manager, Prometheus Operator, Flux and Kyverno: 123 schemas) and on 14 SchemaStore schemas imported with `cue import jsonschema:` (docker-compose, Dependabot, golangci-lint, GoReleaser, Helm charts, helmfile, Kustomize, Prometheus, Traefik, tsconfig, codecov…). Registry modules load through `load.Instances`, which uses `CUE_REGISTRY` by itself. Kubernetes and the CRDs needed nothing: their exports came out right on the first try. What JSON Schema brings in did:

- **`matchN`.** `cue import` writes `oneOf` as `matchN(1, [A, B])`, `anyOf` as `matchN(>=1, […])` and `allOf` as `matchN(N, […])` with all N. `expr` now reads the first two as `A | B`, so struct alternatives are variants (GitHub Actions' jobs: `runs-on` or `uses`) and alternatives of one kind get its widget. Unlike `A | B`, `matchN` only checks a value: the value does not take the fields of the alternative it matches. So a chosen `matchN` variant builds its menu from the value unified with the alternative (`validatesOnly`), and `allOf` parts add their structs to the menu (`allOfStruct`) and are searched by `find` (`alternatives`).
- **`Err()` vs `Validate()` again.** A struct that misses a required field fails `matchN` with `Err()` until it is complete, so `find` dropped every `matchN` alternative and `prune` then deleted fields just set (`runs-on` vanished when adding `steps`). Alternatives are now kept unless `Validate()` finds a permanent conflict (`fits`).
- **Required containers.** `LookupPath` finds nothing for a required field without a value: adding a key to `jobs!: {[K]: V}` failed with "key matches no pattern", and a variant whose distinguishing field is required asked for a read-only node. `Entry` and `askFor` use `find`/`field`. The "⚠ required" marker is now `Node.Missing` (required and absent from the value): before, a required map or struct kept it even when filled in.
- **Choosing the first variant** while none is decided (`{}` fits all) did nothing: it was taken as already chosen.
- **A validator is not `_`.** `matchN(…)` has kind `_`, so `declaredOpen` took it for "anything" and gave its own declaration to every field below it.
- **Validators next to a disjunction** (`matchN(2, …) & (A | B)`) are skipped to find it, unless they declare fields: `matchN(1, [{uses!: _, ...}, …]) & close({…})`, GitHub Actions' step, is a menu of the fields in `close`.
- **Untyped structs.** A JSON Schema with properties but no `type` becomes `null | bool | number | string | [...] | {…}` (docker-compose's services). With at least three of those kinds next to one struct, it is edited as the struct, like `null | T`; `bool | {…}` stays a real choice (codecov's `status`).
- **The root** can be "structs or an empty document" (Azure Pipelines: `matchN(1, [#pipeline, =~"^$"])`, `#pipeline` a `oneOf` of seven structs): it is a menu or a variant of the structs, instead of an expression editor that cannot even be used at the root.
- **Mixed kinds** stay in the CUE editor, but text that is no valid CUE is tried as a string: `ubuntu-latest` needs no quotes in GitHub Actions' `runs-on` (a string, a list or a struct).
- **Constants that are required** (`version!: 2`, JSON Schema's `const`) are read-only, yet must be given: ⏎ gives them their value.
- **Variants are named** by their required own fields when they have some (`runs-on` / `uses`, not the list of twenty optional fields), and `askFor` asks for that field.
- **CUE problems worked around:** successive `FillPath` calls reject valid values when the schema has `matchIf` next to a closed struct (Dependabot: "field not allowed", while `cue vet` accepts them), so the edits are written as CUE text and unified once. CUE's exporter panics on some values of GitHub Actions' schema (quoted definitions, `#."working-directory"`) and `Unify` panics on some values from `Expr()` (docker-compose's `allOf` parts): printing falls back to JSON, `fits` treats a panic as no fit, and `Set` undoes an edit whose tree cannot be built instead of taking the editor down with unsaved changes.
- **Field order.** A struct unified with values lists the fields it was given first, and fields built with `FillPath` come sorted by name: in v0.2.1 already, a schema of optional fields (all of JSON Schema) had its menu reordered by every edit and its export sorted by name. Menus now sort their fields as the declaration does (`inOrder`), and the export declares first, as `x?: _`, the fields of each struct on the way in the schema's order (`exportValue`). Those declarations add nothing to a concrete value, but they change how a field is classified, so they are only used to export. `cue export` keeps the same order.
- **Speed.** A docker-compose service (about a hundred fields from several `allOf` parts) took 1.2 s per edit at first. While a tree is built, `without` and `Value` are computed once per set of edits left out; the empty struct that creates a map entry no longer makes every field below it "edited"; and the tree `Set` builds to check an edit is kept for the next `Root`. An edit there now takes about 125 ms; Kubernetes and GitLab trees are faster than in v0.2.1.
- **Results.** All 123 registry schemas load; expression fields went from 111 to 99, and none got worse. On SchemaStore they went from 48 to 38 (golangci-lint 10 → 1). Flows checked end to end, export included: a GitHub Actions job with steps, a Kubernetes Deployment with containers and ports, a cert-manager Certificate, a GitLab job, an Azure pipeline, a docker-compose service, a Dependabot update. The 134 earlier schemas give the same trees as v0.2.1, except Redis' `image`, which lost a false "⚠ required", and Grafana heatmap's `reverse`, now where the schema declares it.
- **Left as they are:** most remaining expression fields are mixed kinds by design (`string | [...string]`, `int | string`), typed as CUE or as plain text. Three SchemaStore schemas do not load because of the conversion, not cuemenu: `package.json` and pre-commit import other SchemaStore schemas that `cue import` does not bring, and Renovate's has a structural cycle that `cue vet` reports too. The saved `.config.cue` lists its values by name.

## Random interaction (monkey) over every schema

`TestMonkey` drives the TUI with random keys (moves, ⏎, esc, space, typed values, `e r a d K J / s q`) over each schema: nothing may panic, every state must render, the list open must show the elements its keys edit, and what is saved must load back, with nothing dropped, to the same tree, without `_|_` in it. By default it runs on the example and the import test package; `CUEMENU_MONKEY=FILE` lists more directories (the 235 of the real-world and registry corpora above), and `CUEMENU_MONKEY_LONG=1` does 8 walks of 400 keys per schema. It found, with pty checks of the same flows:

- **Search:** ↓ with no results, then ⏎, indexed -1.
- **Lists that must have elements** (`[_, ...]`, JSON Schema's `minItems`, Dependabot's `assignees`) showed an element `[0]` before any was written, but `Elems` returned none, so editing, deleting or moving it panicked. `Elems` now gives one placeholder per element shown (its value, else `_` or `{}`); the lookup uses `find`, which sees optional fields.
- **New element editor:** looking up an element not added yet gives an error, and an error is "concrete", so the editor of the second element of a list opened with `_|_ // field not found: 1` in it, and that text was saved as the value.
- **Element field order:** a list element rebuilt with `FillPath` prints its fields sorted by name under `Syntax(Final)`, and the kept fields went before the new one, so an element read differently while editing, in the file and after loading it. Its leaves are now sorted as the element declares them and compiled from text.
- **Element out of the value:** an element added empty to docker-compose's `develop.watch` (`path!` and `action!` missing) leaves the service's `matchN` undecided, and the evaluated list then has no elements: `a` added an element that never showed up. An element written but missing from the value is now shown as written, unified with its constraint, so its required fields can be filled in.
- **Empty input:** ⏎ on an empty text field wrote `""` (a required `cert_file!` counted as given), although the editor says "empty = default"; it now resets the field, as for other kinds (`""` is typed with `e`). An empty map key adds nothing instead of failing.
- **Unsaved:** the mark and the question on quitting came from any action; now they mean that the edits differ from those last saved, so an action that changes nothing, or a change undone, leaves nothing to save.
- **Variant told apart by a list** (KubeVela's `container-image`: `#P | close({containers: [...#P]})`): choosing it asked for the whole list as CUE, and ⏎ chose nothing. It is now chosen with the list empty, which opens to add elements. The enums of those elements showed as text: with the variant undecided, their declaration is the same disjunction twice (`D & D`, one per alternative), which `embeddedOr` now accepts.
- **Not cuemenu:** Timoni's redis example declares `imagePullSecrets?: [...corev1.LocalObjectReference]`, a name its k8s package does not have (only `#LocalObjectReference`), so its elements cannot be filled in; `cue eval` leaves the reference unresolved too.

## cuegen: Go code from the schema

`cuegen` (`cmd/cuegen`, package `gen`) generates the Go types that receive a configuration: structs with `json` and `validate` tags, `NewX` with the defaults, `UnmarshalJSON` that decodes over them, `ParseJSON`, `ParseYAML` (through JSON, so there is one set of tags) and `Validate`, which runs go-playground/validator and then the generated checks.

**How the schema is read.** The structure (fields, `?` and `!`, patterns, list elements, disjunctions of structs) comes from the value API; the constraints of a scalar come from its evaluated text parsed as CUE (`*3000 | uint & >=1 & <=65535`), read as alternatives of conjuncts. `Syntax()` is no use there: it keeps references to definitions as `let` clauses, and `Expr()` hides the disjunction of a field with a default (`*"localhost" | strings.MinRunes(3)` is a `NoOp`). Disjunctions, `oneOf`/`anyOf` and `allOf` are read with the model's own logic (`model.Disjuncts`, `model.AllOf`).

**Decisions.**

- A field that may be left out without a default (`!`, `?`, or a regular field with no default) is a pointer: validator's `required` rejects zero values, but `port!: int` accepts `0`.
- `if` branches: the enum and bool fields of a struct with a comprehension are filled in with each value in turn (and with the literals its `if` conditions compare them with: `if kind == "Resources"` when `kind` is any string), and the fields that come and go become pointers. Code then requires them, forbids them (`helm?: _|_`) or sets their default after decoding, from the value decoded. The comprehensions are found in the `Raw` syntax, following references, since that of a reference is just the reference.
- Variants are interfaces. With a field that every alternative fixes to a different constant, that field picks the variant; else each variant is decoded and validated in turn, and the value is the one it fits, or the default if it fits several, as CUE resolves a disjunction (Grafana's heatmap legend fits both its variants: the default wins). Identical alternatives are one.
- Keys a struct with fields does not declare but its pattern allows (Helm's `annotations`: the `artifacthub.io/…` ones and any other) are kept in a `Rest` map, decoded and written back.
- A field the schema computes is decoded into a pointer and not checked: an interpolation, alone or in a conjunction (`#Path & "\(Name).yaml"`), a `[for …]` list, or a reference to another field of the configuration (Holos' `name: Name`). A reference is to a field of the configuration if its path, which is relative to the struct it is in, exists in the root generated; one through a definition (`#."relative-path-modes"`) or out of the root (Grafana's `type: ScaleDistribution` next to the `FieldConfig` generated) is a type.
- A struct is named after its definition, or its path. Structs that are equal are one type: by the position of their definition, else by a fingerprint of their fields with `?`/`!`, list elements and alternatives a few levels deep. The fingerprint has to include optional fields, which `%v` leaves out (two structs with only optional fields printed `{}` and became one type), and `%#v` panics on some schemas (golangci's `let` names). Without it, Grafana's recursive dashboard layouts grew to 92,000 lines; with it, 4,000.
- A struct is closed if `Allows` rejects an unknown field. Next to a validator such as `matchN`, among the parts of its conjunction however nested, `Allows` is wrong either way (kyverno's `match` is open, Dependabot's root closed), and there an unknown field is filled in and the result validated. That is no rule for every struct: filling a field into a part of a closed definition is accepted, and only unification of the whole document rejects it.
- An optional field that is `_|_` (Holos: `helm?: _|_` in a task of another kind) is left out of the struct.
- What the code cannot check is warned about on stderr, never accepted silently.

**Differential test.** `examples/webapp/config` checks 70 hand-written inputs; `TestCorpus` (`CUEGEN_CORPUS=FILE`, `CUEGEN_INPUTS=N`) generates the code of each schema, builds it with a small `main`, and feeds it random inputs made from the schema (typical values, the schema's own literals, wrong kinds, `null`, unknown keys) next to CUE: both must accept the same inputs and decode them to the same configuration. Over the 235 schemas it found, in order:

- A schema too deep or recursive with no definition names never ended: generation is cut at 32 levels, and equal structs are one type.
- `null` decoded into Go's zero value or `nil` where CUE rejects it: `checkKeys` rejects it per key (and in the elements of lists and maps of scalars), and `UnmarshalJSON` of a struct rejects `null` itself; nullable structs in lists and maps are pointers.
- `IncompleteKind` is bottom for a value with an error not resolved yet (`matchN(…) & {…}`, an `if` on a field not given), and `_` for a `matchN`: the kind comes from the parts, the alternatives, or the fields.
- `oneOf` with required keys next to a struct (`matchN(1, [{a!: _}, {b!: _}])`), common in CRDs, is generated as a count of the alternatives given.
- Disjunctions of several kinds (`string | {…}`, `int | string`) are `any` with a check of the JSON kind.
- Patterns of an open struct do not restrict its keys: `[=~"^X-"]: string` allows any other key with any value. Only closed structs check their keys.
- A field the schema computes with a comprehension (Holos' `spec.components: [for …]`) is decoded, not computed, and warned about.

With 150 random inputs per schema, 15 of the 235 schemas still accept inputs CUE rejects, never the reverse, all at places a warning names: alternatives of several kinds whose contents are not checked (`int | string`, `string | [...string]`), JSON Schema's `not`, a `oneOf` whose alternatives are told apart by more than keys. Three decode differently: the fields of an open struct that it does not declare are not kept (the default of the heatmap legend keeps `width` in CUE), and fields an `if` fills in from other fields (Holos' `metadata.labels`) are not computed.

## Tooling

- Build, test and lint come from [go-build-tools](https://github.com/Drathveloper/go-build-tools) as a submodule in `.parent`. `make lint` passes with no findings under its strict golangci-lint configuration.
- What the linter found that mattered: `cue.Value.Context` is deprecated, so `Config` owns a `*cue.Context`; map keys are checked with `FillPath` and `...` is detected with `LookupPath(AnyString)`, neither needing a context.
- `golangci-lint run --fix` is not safe here: its `fieldalignment` fix reorders struct fields, which breaks positional literals and drops the field comments.
- The remaining `//nolint` directives each state why the finding does not apply.
