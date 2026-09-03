---
description: Choose the owner and location of each worktree, then provision ignored files and dependencies safely.
authority: project
status: stable
verified_on: 2026-08-29
---

# Worktrees and provisioning

A Git worktree begins as a clean checkout. `dev` owns durable change-stream worktrees and builds an inspectable plan for the ignored files and dependencies needed to make them usable.

## Choose one lifecycle owner

| Worktree kind | Owner | Typical location | Lifetime |
|---|---|---|---|
| feature, fix, experiment, cross-machine handoff | `dev` | configured `paths.worktree_path` | `dev done` records MERGED only; a later `dev retire`, explicit `dev wt rm`, or approved sweep removes it |
| harness-scoped isolation | Claude Code or another harness | harness-owned directory such as `.claude/worktrees/<name>/` | governed by that harness's retention and safe-cleanup rules; Flow never adopts or removes it |
| externally created linked worktree | external until adopted | tool-specific | visible to `dev`/`dev flow`; unmanaged until explicit adoption |

Use `dev` when code, history, or plans must remain reviewable or a human may return later. Do not nest a long-lived `dev` worktree inside a repository; file watchers, language servers, backup tools, and searches in the outer checkout would see a second copy of the tree.

`dev` creates the checkout with Git at its configured path. Herdr only opens that existing path, so worktree placement remains identical on machines without Herdr.

`dev flow [repo]` uses Git's authoritative worktree records to show canonical,
managed, unmanaged, and strict `.claude/worktrees/` harness rows, plus task-only
rows that have no checkout. It never guesses ownership from a `worktree-*`
branch prefix; ambiguous path/task binding is labelled CONFLICT and stops
lifecycle mutation. See [Repository lifecycle flow](repository-flow.md).

## Inspect before creating

```bash
dev wt plan
dev wt plan --write          # seed repository-owned .dev-cli/config.toml
dev wt create feat/auth --base main
dev wt list
```

`dev wt plan` reads lockfiles, tool availability, configured include/link rules, and Git ignore state without changing the checkout. The resulting plan shows every runnable or skipped step and any safety downgrade.

## Carry ignored files by allowlist

```toml
[worktree]
include = [".env", ".env.local", "config/local.json"]
link = []
post_create = "auto"
strategy = "reinstall"
```

Only paths that are both listed and confirmed by Git as ignored are copied. A tracked file already arrives through the branch; copying a different checkout's version over it would violate branch isolation.

Included files are copied as files, not logged. Current provisioning also rechecks the source and destination path shape while opening/copying so source swaps and symlinked destination parents are refused.

Do not add `.claude/settings.local.json` globally. Include that exact file only for a deliberately chosen launcher whose behavior depends on it, and verify the copy in the plan.

## Select dependency strategy by correctness

| Strategy | Effect | Guidance |
|---|---|---|
| `reinstall` | run the lockfile-derived install command | safe default |
| `copy` | duplicate an installed dependency directory | use only where paths are portable |
| `link` | share one dependency directory | fastest and usually unsafe for concurrent writers |
| `skip` | leave dependencies absent | container- or CI-driven development |

Per-ecosystem overrides live in the project or global config:

```toml
[worktree.strategies]
node = "copy"
```

New project-owned overrides belong in `.dev-cli/config.toml`; legacy
`.dev.toml` remains readable under its compatibility behavior. A `post_create`
command from `.dev-cli/config.toml` does not run until its exact
executable-config hash is approved with `dev config trust . --yes`. Changing
the command invalidates that approval.

Important built-in decisions:

- Python virtual environments cannot be copied or linked because they embed absolute paths; `uv sync` can reuse its global cache.
- Node `node_modules` may be copied, but sharing is refused because either checkout can mutate it.
- Go uses a global content-addressed module cache, so there is no checkout-local dependency tree to copy.
- Cargo `target/` may be copied, but sharing concurrent build output is refused.

An invalid or unsound request narrows to `reinstall` with a warning instead of silently creating a broken environment.

## Auto-detected setup

`post_create = "auto"` recognizes one manager per ecosystem, in priority order:

| Marker | Command |
|---|---|
| `uv.lock` | `uv sync` |
| `poetry.lock` | `poetry install` |
| `pnpm-lock.yaml` | `pnpm install --frozen-lockfile` |
| `package-lock.json` | `npm ci` |
| `yarn.lock` | `yarn install --immutable` |
| `go.mod` | `go mod download` |
| `Cargo.toml` | `cargo fetch` |
| `Gemfile.lock` | `bundle install` |

A missing tool is reported and skipped. A failed setup command leaves the worktree in place so the branch and checkout can be repaired rather than discarded.

## Reprovision or remove

```bash
dev wt provision /path/to/worktree --dry-run
dev wt provision /path/to/worktree
dev wt rm feat/auth
```

Removing a worktree and deleting a branch are separate decisions. `dev wt rm` preserves the branch and refuses a dirty checkout without explicit force. If the directory disappeared outside Git, it prunes the stale administrative entry.

## Sources

- [`internal/wt/plan.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/plan.go)
- [`internal/wt/ecosystem.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/ecosystem.go)
- [`internal/wt/provision.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/provision.go)
- [`internal/wt/manager.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/manager.go)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
