# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`AGENTS.md` is the canonical repository guidance file. `CLAUDE.md` is a symlink to it; edit this file rather than replacing the symlink.

## Toolchain and commands

- Go 1.26.4 (from `go.mod`) builds the CLI. Python 3.11+ and `uv` are used only for the documentation site.
- `make` / `make all` runs `fmt -> vet -> test -> build`. `make fmt` runs `gofmt -w .` and therefore modifies files.
- There is no working `make lint` recipe even though `lint` appears in `.PHONY`. The repository's static gates are formatting and `go vet`.

```bash
make build                         # build ./dev with git-derived version metadata
make test                          # go test ./...
go test -race ./...                # CI test command
go test ./internal/gitx -run '^TestAddWorktreeUsesExplicitBase$'  # one test
go test ./internal/cli -run '^TestName/Subtest$'                  # one subtest
make vet                           # go vet ./...
files="$(gofmt -l .)" && test -z "$files"  # format check that preserves gofmt failures
make e2e                           # real CLI lifecycle in an isolated temporary HOME
make skill-sync                    # regenerate command docs, then rebuild embedded skill
make skill-check                   # fail when command tree and generated skill reference drift
make install                       # binary under PREFIX; skill in user agent directories
```

Documentation uses the same sequence as CI:

```bash
uv sync --frozen --extra docs
uv run python scripts/check-docs.py --source
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

If generated `docs/llms*.txt` files drift, regenerate them with `uv run python scripts/check-docs.py --source --generate-llms` and then rerun the documentation checks.

## Architecture

The executable path is deliberately shallow:

```text
cmd/dev/main.go
  -> cli.Execute()
  -> cli.NewRootCommandWithIO()
  -> App.Load()
  -> Cobra command handler
  -> domain package(s)
```

`internal/cli` assembles commands, parses flags, invokes services, and renders output. Keep policy out of command handlers; it belongs in independently testable domain packages:

- `config` owns defaults, TOML overlays, validation, and XDG paths.
- `gitx` is the Git porcelain boundary and canonicalizes repository identity through the shared Git common directory.
- `task` persists only human intent Git cannot derive: task mode/state, owner, next action, and runtime hints.
- `wt` owns managed linked-worktree creation, placement, provisioning plans, and safe removal. Runtimes never create or manage Git worktrees; they surface checkout or experiment paths selected and validated by the calling `dev` flow, including managed, external, or adopted worktrees and non-Git Try directories.
- `runtime` abstracts Herdr, tmux, Zellij, and the no-multiplexer backend. Handles are backend-qualified hints and must be checked against live checkout coverage before reuse.
- `repo` discovers and deduplicates repositories; `inventory` joins repositories, Git state, tasks, worktrees, and live runtime data. CLI listings and the TUI consume this joined view.
- `catalog` stores stable repository/Try identity and personal metadata. `experiment` owns Try creation, archive/restore, and graduation, including intent records and rollback/reconciliation around moves.
- `note` stores multiple repository thoughts as durable Markdown keyed by catalog ID and maintains a rebuildable SQLite FTS index. It is distinct from task context and the catalog's single metadata summary.
- `tui` owns Bubble Tea state/rendering only. `internal/cli/tui.go` injects callbacks to the same services used by non-interactive commands.
- `stats` is durable SQLite data; `diskusage`, note FTS, and forge/gitignore data are regenerable caches. Do not treat stats as cache.
- `forge` wraps optional `gh`, `glab`, and Azure CLI integrations and must degrade to local Git behavior when they are unavailable.

The principal task flow spans `internal/cli/start.go`, `start_flow.go`, `gitx`/`wt`, `runtime`, and `task`: resolve the canonical repository and explicit base, create or select the checkout, provision/open it, then persist and annotate the task. Lifecycle behavior lives in `park.go`, `resume.go`, `done.go`, and `sweep.go`; preserve their report-before-apply and no-data-loss ordering.

State is split intentionally:

- Git remotes/branches are durable code truth.
- Worktrees are disposable local checkouts.
- Runtime sessions are per-host and re-derivable.
- Task TOML and catalog assets hold only intent/identity that Git cannot answer.
- Repository quick-note Markdown is durable sidecar data; `notes.db` is only its disposable search index.
- `stats.db` is durable observation data; `$XDG_CACHE_HOME/dev/*` is disposable.

## Behavioral contracts

- Task modes are `worktree`, `branch`, and `direct`; lifecycle states are `hot`, `warm`, `cold`, and `done`.
- Use an explicit base for branch/worktree creation. Do not infer a safe base from whichever branch happens to be checked out.
- Never nest a managed worktree inside a repository. Removing a worktree does not remove its branch, and dirty removal stays opt-in.
- Cold parking requires committed, pushed, reconstructible work before runtime/worktree cleanup.
- Reconciliation flows such as `sweep`, `adopt`, and bootstrap organization plans report before applying changes. Direct actions such as `wt rm`, `tries archive`, and `cache clear` mutate immediately after their own safety checks; do not treat them as previews.
- A recognized agent in a canonical checkout occupies it regardless of whether its status is working, idle, done, or unknown. Shared-writer overrides require coordinated disjoint ownership.
- A parallel launch target is only the exact root pane returned for a newly created first-class Herdr worktree; reused/fallback/unverified surfaces fail closed.
- `dev ls --json` is an external automation contract. Add fields rather than renaming/removing existing fields; apply the same compatibility care to other documented structured output.
- Quick notes attach to the canonical repository through catalog identity. Markdown is durable, note deletion requires confirmation, and clearing/rebuilding note FTS must never delete source files.
- Filesystem transitions must retain path traversal/symlink, same-filesystem, source revalidation, and rollback/reconciliation checks.

## Versioning and changelog

The current published baseline is `v0.2.4` (2026-09-01). The CLI version authority is an immutable `vMAJOR.MINOR.PATCH` Git tag:

- `Makefile` derives development builds with `git describe --tags --match 'v[0-9]*' --always --dirty` and injects `internal/cli.Version` through `-ldflags`. The `--match` filter is load-bearing: any other tag in the repository (a `backup/` or `rescue/` marker, say) must never become `--version`.
- `go install ...@version` recovers the module version from Go build information.
- `pyproject.toml` version `0.0.0` belongs only to the MkDocs environment and is not the CLI version.

Every user-visible feature must be added to `[Unreleased]` in `CHANGELOG.md` as part of the feature change. Do not publish or describe a feature build under an older release's version; a release containing new compatible behavior must receive a newer version. Decide from public compatibility impact rather than blindly from the commit type.

**When to release.** A tag is not an occasional ceremony; it is how a downstream binary learns it is stale. `go install ...@latest` resolves to the newest tag, so anything merged but untagged is invisible outside this repository.

- A landed branch that stands alone is released immediately: bump the patch during `0.x` (`v0.1.x`) and tag it as the last step of landing.
- A landed branch belonging to an announced milestone accumulates in `[Unreleased]`; the milestone closes with a minor bump (`v0.2.0`).
- Never let `[Unreleased]` grow past one milestone's worth of work. `v0.1.0` to `v0.1.11` were tagged retroactively on 2026-08-29 precisely because 38 commits accumulated with no release.

**What downstream gets.** Every release from `v0.2.0` forward publishes platform binaries and `SHA256SUMS` alongside the GitHub release, and `dev doctor` reports the running version so a user can compare without leaving the terminal. A release that ships neither leaves `dev --version` as the only signal, which is what the freshness work exists to fix.

For a release:

1. Move the relevant `[Unreleased]` entries into `## [x.y.z] - YYYY-MM-DD` and update comparison links.
2. Update version-pinned install examples when the documented recommended release changes.
3. Push `main` first. `release.yml` asserts the tagged commit is an ancestor of `origin/main`, so a tag pushed ahead of its branch fails validation.
4. Tag the exact commit on `main` as `vMAJOR.MINOR.PATCH`; never move or reuse a published tag. Never push `backup/*` or `rescue/*` tags — a tag reachable from a commit that predates the `--match` filter breaks the workflow's `./dev --version` assertion.
5. Let `.github/workflows/release.yml` run CI, verify `./dev --version`, build the platform matrix, create the GitHub release, and publish the matching Homebrew formula. The repository secret `HOMEBREW_TAP_TOKEN` must be a fine-grained token with Contents write access to `daviddwlee84/homebrew-tap`; a tap publication failure fails the release job so it can be retried instead of leaving Homebrew users stale.

If the Homebrew step needs a retry or an older stable release needs a one-time backfill, run `gh workflow run publish-homebrew.yml -f version=vMAJOR.MINOR.PATCH`. The manual workflow verifies that the release exists and republishes only the formula; it does not create or move a tag or GitHub release.

Stable release tags only are accepted by the current workflow; prerelease tags are not supported.

## Keep implementation, skill, and docs aligned

`internal/skill/dev-cli/` is shipped product surface, not incidental prose: `internal/skill/skill.go` embeds the entire tree in the binary. A code change is incomplete when the bundled skill or public documentation still describes old behavior.

| Change | Required synchronization |
|---|---|
| Cobra command name, `Use`, `Short`, flag, or global option | Run `make skill-sync`, inspect `internal/skill/dev-cli/references/commands.md`, then run `make skill-check`. Never edit the generated block by hand. |
| Task modes/states/defaults or lifecycle preconditions | Update `SKILL.md`, `references/task-lifecycle.md`, README, embedded help, relevant English and zh-TW workflow/mental-model/compatibility pages, and transition tests. |
| Worktree placement, provisioning, include rules, strategies, or safety checks | Update `references/worktree-ownership.md`, README/public worktree docs, config examples, and focused `wt` tests. |
| Runtime protocol, fallback order, activation, agent collision, exact-pane launch, or cleanup behavior | Update `references/runtime-herdr.md`, `references/parallel-agents.md`, compatibility docs, and runtime/lifecycle tests. Keep the boundary clear: `dev start` prepares a checkout/runtime surface; it does not launch an agent. |
| Bootstrap scan/layout/index/move behavior | Update `references/bootstrap.md`, public bootstrap docs, and bootstrap tests. |
| Config fields/defaults or structured output | Update generated command help where applicable, authored reference pages and both locales, examples, and compatibility tests. |
| Quick-note storage, identity, search, structured output, or TUI behavior | Update README, embedded help, `SKILL.md`, `references/notes.md`, and paired English/zh-TW mental-model, workflow, TUI, commands/config, compatibility, and freshness pages. |
| Any user-visible feature or fix | Update `CHANGELOG.md`; update README/help/MkDocs/skill wherever they make the affected claim. |

`make skill-check` proves only that generated command syntax matches the Cobra tree. It cannot detect drift in lifecycle semantics, JSON schemas, safety claims, examples, README, embedded help, or authored skill references; review those manually. Because the skill is compiled with `go:embed`, rebuild after any skill edit before testing the bundled output.

English and zh-TW MkDocs pages are maintained as a pair. Run the strict documentation checks above after changing either locale, navigation, snippets, or freshness metadata.
