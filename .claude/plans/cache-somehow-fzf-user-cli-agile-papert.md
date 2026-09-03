# Context

`dev` already has robust domain-owned caches (forge remotes, fleet snapshots, disk sizes, note FTS, templates), but it has no generic cache of selectable CLI values and no picker integration. Repository discovery/inventory remains deliberately live. The first useful integration is therefore narrow: reuse the existing forge cache to choose a remote for `dev repo clone`, use fast live discovery to choose a local repository in the `dev start` wizard, and expose the existing `dev repo remote --json` contract to Television/fzf recipes. This removes repetitive path/name typing without making fzf mandatory, changing scripted behavior, or adding a second inventory/cache.

The user selected an internal configurable picker plus composable external recipes, with a built-in Bubble Tea fallback. This slice intentionally does not add pickers to `dev done` (which already has task completion) or to every finite-choice command.

## 1. Add a reusable, safe selector primitive

- Add `internal/textmatch` with an exported term matcher equivalent to the current `internal/tui/rows.go:matches`: trim/lowercase, split whitespace, require every term as a substring. Have the TUI delegate to it so the built-in picker and `/` keep one tested matching contract; do not introduce a fuzzy-scoring dependency.
- Add `internal/picker` with small caller-neutral types such as:
  - `Item { Value, Label, Description string }`
  - `Request { Prompt, InitialQuery string; Items []Item }`
  - `Result { Item Item; Selected, Canceled bool }`
  - a selector object constructed with input/output/error streams and configured external argv, exposing `Select(context.Context, Request) (Result, error)`.
- Keep backend selection internal: use the configured external executable when `exec.LookPath` succeeds, otherwise use the built-in Bubble Tea backend. An explicitly empty argv forces the built-in backend. A configured executable that starts and then fails is an error rather than silently replaying the interaction in a second UI.
- Execute external argv directly with `exec.CommandContext`, never via a shell. Feed normalized, uniquely rendered candidate lines on child stdin, capture only the selected line from stdout, and leave stderr attached to the caller terminal for fzf-style UI. Map the exact returned line back to the source `Item`; decorate otherwise-identical rendered lines with a deterministic ordinal so duplicate labels/descriptions cannot select the wrong value. Reject unexpected/multiple output instead of guessing. Treat exit codes 1 and 130 as cancellation, propagate context cancellation, and report other exits.
- Support a literal `{prompt}` substitution in argv elements so the per-call label can reach fzf without shell interpolation. Newlines/NULs in item display fields are normalized before rendering; the opaque `Value` is never rendered or trusted back from the child.
- Implement the fallback as a compact Bubble Tea list with `bubbles/textinput`: live term filtering, up/down plus Ctrl-N/Ctrl-P, Enter to select, Esc/Ctrl-C to cancel, and a bounded viewport. Test the model directly rather than requiring a real terminal.
- Add an `App` adapter/test seam in `internal/cli` that passes `App.In/Out/Err` and `App.Cfg.Picker` into `internal/picker`. It should only open a picker when input and output are a terminal pair; non-TTY readers keep the existing deterministic line prompts.

Critical files: `internal/picker/*.go`, `internal/textmatch/*.go`, `internal/tui/rows.go`, `internal/cli/app.go`, and a small `internal/cli/picker.go` adapter.

## 2. Add minimal global picker configuration

- Extend `internal/config/config.go` with `Config.Picker` and a global-only `[picker]` section containing one `command []string` argv vector. Default it to fzf with conservative presentation args and `{prompt}` (for example `fzf`, reverse layout, bounded height/border, and a prompt). `command = []` is the documented way to force the built-in picker.
- Keeping executable and arguments in one replacement-valued slice avoids a partial TOML overlay accidentally retaining fzf arguments when the executable is changed to `tv`, `sk`, or `gum`.
- Validation permits an empty vector, but rejects an empty first element and NUL/newline-bearing argv elements. It does not require the binary to be installed: absence is a supported fallback state.
- Add the annotated section to `internal/cli/config.go`'s starter config. `dev config show` will include it automatically. Keep picker policy out of repo-owned `.dev-cli/config.toml`.
- Extend `dev doctor` to identify the configured external selector as optional and state that the built-in picker will be used when it is unavailable; do not install anything.

Critical files: `internal/config/config.go`, `internal/config/config_test.go`, `internal/cli/config.go`, `internal/cli/doctor.go` and focused CLI tests.

## 3. Wire only the two approved interactive entry points

### `dev repo clone`

- In `internal/cli/repo_create_wizard.go:runRepoCloneWizard`, before the existing free-form source prompt, read the raw forge cache through `cachedRemoteRows(app)` / `forge.LoadCacheAny`. This is disk-only and must not trigger provider network calls or the expensive `matchRemoteLocals` repository scan.
- Build candidates from cache rows with non-empty `CloneURL`, displaying forge-qualified full name plus visibility/archived/description metadata. Preserve cache ordering (or the existing deterministic updated/name ordering). Use `RemoteRepo.CloneURL` as `Item.Value`, not `FullName`, so GitHub, GitLab (including self-hosted), and Azure DevOps keep the exact provider URL.
- Append a synthetic “Enter a URL, path, or owner/name…” item. Selecting it, having no usable cache/candidates, or running through non-TTY input goes through the current `prompter.line` path unchanged. A stale but source-compatible cache remains useful and is visibly warned as stale; a missing/source-mismatched cache simply falls back to manual entry. Never refresh the network merely to open the picker.
- Picker cancellation returns `errPromptCanceled`, preserving the command’s existing “Canceled; nothing was cloned” behavior. Explicit positional refs and non-interactive/`--json` behavior bypass the picker exactly as today.
- Do not add a costly local scan merely to badge/exclude already-cloned remotes. Existing destination/nested-repository checks remain authoritative and prevent unsafe duplicate acquisition.

### `dev start`

- Replace `internal/cli/start_wizard.go:promptStartRepository`’s numbered listing with the shared picker.
- Obtain candidates with `repo.Discover(..., repo.CompletionOptions())`, filter to non-bare Git repositories, and render `Repo.Display()` plus `config.Contract(Repo.Path)`. Put the current repository first when it resolves, while retaining a synthetic manual/name/path item for values not discovered under configured roots.
- Return the selected repo directly; the manual path continues through `repo.Resolve`. Preserve explicit `dev start <repo>`, non-TTY prompts, empty-inventory errors, and cancellation before any mutation.

Tests should inject selector results through the `App` seam, so command/wizard tests do not depend on fzf, PTYs, or a developer’s PATH.

## 4. Ship composable Television/fzf usage without another command

- Do not add `dev pick`: `dev repo remote --cached --json` already supplies the cache-backed, pipe-friendly candidate contract, while `--refresh` explicitly populates it. Reusing this command avoids duplicate filtering, freshness, JSON, and cache semantics.
- Add `contrib/television/dev-remote-repos.toml`. Its source invokes `dev repo remote --cached --json` and `jq` to emit TSV containing exact `repo.clone_url`, display/full name, forge, visibility, local path, and description. Enter outputs the exact clone URL; an action can run `dev repo clone` on it. Metadata declares `dev` and `jq` requirements. The repository only ships the file; it does not modify the user’s Television/chezmoi configuration.
- Document both compositions near repository bootstrap:
  - initialize/refresh once with `dev repo remote --refresh`;
  - copy or symlink the channel and use `dev repo clone "$(tv dev-remote-repos)"`;
  - provide an fzf shell function that transforms the same JSON with `jq`, displays descriptive columns, extracts the exact clone URL, handles cancellation/empty output, and invokes `dev repo clone`.
- Explain that the internal no-argument clone picker needs neither `tv` nor `jq`, uses no network, and falls back to the built-in UI when its configured external command is absent.

## 5. Record intentionally deferred picker surfaces

Replace the completed `TODO.md` “Television / fzf channel” item with a scoped backlog item for extending the shared selector only where it improves over completion. List:

- `tries *`, `graduate`, note show/edit/delete, fleet open/host, artifact discard, and skill update;
- interactive disambiguation for structured repo/experiment/catalog ambiguity errors;
- branch/base selection after adding one canonical `gitx` branch enumerator;
- larger enum sets currently handled by `prompter.choice`.

Explicitly note that task lifecycle commands such as `dev done` already expose shell completion and should not automatically gain a picker merely because the primitive exists.

## 6. Keep product surfaces synchronized

- Add the user-visible feature to `[Unreleased]` in `CHANGELOG.md`.
- Update `README.md`, `docs/reference/commands-config.md` and its zh-TW pair for `[picker]`, and `docs/reference/compatibility.md` plus zh-TW for external-missing/built-in fallback and non-TTY behavior.
- Update the English/zh-TW repository/TUI workflow guide where clone and external-tool composition are described (principally `docs/guides/tui-repos-bootstrap*.md`).
- Update `internal/skill/dev-cli/SKILL.md` and `internal/skill/dev-cli/references/repository-bootstrap.md` with picker/cache/manual-fallback semantics. The Cobra syntax need not change, but run `make skill-sync` and inspect the generated command reference because help text may be clarified.
- Regenerate `docs/llms*.txt` only if the source check reports drift, per repository guidance.

## 7. Verification

1. Unit tests:
   - `go test ./internal/textmatch ./internal/picker ./internal/config`
   - picker duplicate mapping, filtering/navigation/cancel, unavailable executable fallback, context cancellation, exit 1/130 cancellation, malformed child output, and non-zero failure. Use a Go helper-process invocation of the current test binary rather than platform-specific shell scripts.
2. Focused CLI tests:
   - cached clone selection returns the exact `CloneURL` without a provider call;
   - stale/no/source-mismatched cache and manual-item fallback;
   - picker cancellation performs no clone;
   - explicit/non-interactive/JSON clone paths remain unchanged;
   - start selects current/other/manual repos and fast discovery excludes bare/linked worktrees.
3. Repository gates: `make skill-sync`, inspect its diff, `make skill-check`, format-preserving check (`files="$(gofmt -l .)" && test -z "$files"`), `make vet`, `go test -race ./...`, and `make build` (or `make all` plus race test).
4. Documentation gates: `uv sync --frozen --extra docs`, source check, `mkdocs build --strict`, and site check; regenerate llms files only when instructed by the checker.
5. Manual smoke test with an existing remote cache: run no-arg `dev repo clone`, verify configured fzf and forced built-in modes show the same candidates, select manual entry, and cancel before confirmation; run `dev start`, switch repository, and cancel before creation. Also validate the shipped TV channel and documented fzf function return an exact clone URL without refreshing the network.
