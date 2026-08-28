---
description: Find the dev-cli command groups, generated exact flags, configuration layers, and stable automation surfaces.
authority: project
status: generated-plus-authored
verified_on: 2026-08-28
---

# Commands and configuration

Use the authored map for intent and the embedded generated reference for exact flags. The generated block comes from the binary's Cobra command tree and is checked by `dev skill sync --check`.

## Command map

| Goal | Commands |
|---|---|
| task lifecycle | `start`, `park`, `resume`, `done`, `retire`, `sweep`, `ls`, `status` |
| agent artifacts | `prepare`, `artifact finalize`, `artifact list` |
| guarded Git transactions | `git uncommit`, `git recommit`, `git pull-rebase`, `git amend-all`, `git setup` |
| linked worktrees | `wt list`, `wt create`, `wt open`, `wt rm`, `wt plan`, `wt provision` |
| repositories/remotes | `repo list`, `repo context`, `repo clone`, `repo open`, `repo new`, `repo sync`, `repo remote`, `repo mark` |
| repository quick notes | `note add`, `note list`, `note show`, `note search`, `note edit`, `note delete`, `note path`, `note reindex` |
| machine inventory | `bootstrap`, `adopt`, `doctor` |
| experiments | `try`, `tries …`, `graduate` |
| terminal UI | `tui`, `tui tools` |
| configuration/shell | `config init/show/path`, `shell-init`, completion |
| remote fleet | `fleet list`, `fleet status`, `fleet sync`, `fleet open`, `fleet config …` |
| agent skills | `skill list`, `skill add`, `skill update`, `skill install`, `skill sync`, `skill print` |
| generated policy/assets | `gitignore`, `skill install/sync` |
| activity/data | `summary`, `journal`, `stats …`, `cache …` |
| help | `help [topic]` |

Run `dev <command> --help` for the installed binary; this site describes the repository version identified in its freshness metadata.

## High-value structured interfaces

```bash
dev ls --json
dev repo list --json
dev repo context [repo]
dev repo remote --json
dev note list [repo] --json
dev note search <query> --json
dev note show <note-id> --json
dev bootstrap --json
```

Prefer JSON or the agent-ready Markdown context over parsing human tables. Tables are optimized for terminals and may change columns/width without changing the structured contract.

Every `dev repo list --json` row includes `notes.count`. When a latest note exists, the same object adds `notes.latest_id`, `notes.latest_preview`, and `notes.latest_updated`; these optional fields are omitted when the count is zero. `dev note list --json` and `dev note search --json` return arrays of complete note records, while `dev note show --json` returns one complete record.

## `dev done` finish flags

`dev done` for branch/worktree tasks integrates through exactly one of `--ff` (rebase onto the base, then fast-forward it) or `--pr` (push and open a pull/merge request). Omitting both opens the interactive finish wizard on a TTY — see [Change-stream workflow](../guides/change-stream-workflow.md) for the prompts.

A dirty checkout is handled by `--dirty <auto|fail|commit|discard>` (default `auto`):

| Value | Behavior |
|---|---|
| `auto` | interactive: prompts to commit or discard; non-interactive: fails, same as `fail` |
| `fail` | refuses to finish with a dirty checkout |
| `commit` | commits everything with `--message`/`-m` (prompted interactively if omitted) |
| `discard` | resets tracked changes and removes untracked files; destructive, requires `--yes` outside a TTY |

`--yes`/`-y` confirms the selected finish plan; it is mandatory for a non-interactive `--dirty discard` and otherwise skips the interactive confirmation step. `--keep-worktree` keeps the worktree after `--ff` integration (default: removed once merged), `--push` also pushes the resulting branch, and `--delete-branch` deletes the branch once its commits are contained in the base — never a branch with unpushed commits.

## Configuration

```bash
dev config init
dev config show
dev config path
```

`config init` detects local roots and writes explicit defaults. A missing config file is allowed; the built-in defaults keep core Git behavior usable, but generated config is recommended because it makes machine policy reviewable.

Key sections:

| Section | Controls |
|---|---|
| `[paths]` | scan roots, project/tries/worktree roots, worktree template, state path |
| `[runtime]` | `auto`, Herdr, tmux, Zellij, or none plus metadata settings |
| `[worktree]` | ignored includes, linked dirs, setup commands, strategies, timeout |
| `[forge]` / `[[forge.azure_devops]]` | complete remote inventory cache TTL and opt-in Azure organization/project targets |
| `[bootstrap]` | recursion, symlink handling, index/layout policy |
| `[tui]` / `[[tui.tools]]` | columns, sorting, and external-tool bindings |
| `[stats]` | sampler and optional WakaTime import |

Repository quick-note Markdown is durable under configured `paths.state_dir/notes`, which defaults to `$XDG_DATA_HOME/dev/notes`. The full-text index at `$XDG_CACHE_HOME/dev/notes.db` is disposable and rebuilds from those files; changing `paths.state_dir` does not move the cache.

A repository may commit `.dev.toml` for worktree provisioning overrides that should travel with the project. Keep host-specific paths and credentials in the user config or ignored environment files, not in the repository override.

## Colored output

Human-readable output (tables, `dev status`, the `dev done` finish wizard, warnings, and cobra help) applies semantic ANSI color through a small set of roles: `title`/`header`/`prompt` (bold cyan), `label`/`dim` (dim), `success` (green), `warning` (yellow), `danger` (bold red), and `review` (magenta, for a PR/review handoff). Git-status and task-state strings are colored by their own meaning instead of a fixed role — `clean` is green, `dirty`/`ahead`/`behind`/`conflict` are yellow or red as appropriate.

Control it with the global `--color <auto|always|never>` flag (default `auto`). `auto` disables color when output is not attached to a terminal, when `NO_COLOR` is set to any non-empty value, or when `TERM=dumb`. `--json` output is never colored regardless of mode. There is no config-file field for color — `--color` and the environment are the only controls, so piping `dev` never requires `--color never` to stay clean.

## Shell integration

```bash
eval "$(dev shell-init zsh)"
dev shell-init fish | source
```

The trusted `shell-init` output defines a wrapper because a child process cannot change its parent's working directory. For navigation commands, that wrapper reads a NUL-terminated path from a private child-only file descriptor and calls `builtin cd`; it does not evaluate ordinary `dev` command output as shell code.

## Complete generated command reference

The following content is included from `internal/skill/dev-cli/references/commands.md`, beginning after its generated-file preamble:

--8<-- "internal/skill/dev-cli/references/commands.md:7"

## Keeping it current

```bash
go run ./cmd/dev skill sync --check
```

If command help changes, regenerate through `dev skill sync`; do not hand-edit the generated block.

## Sources

- [`internal/cli/root.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/root.go)
- [`internal/config/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/config/config.go)
- [`internal/skill/dev-cli/references/commands.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/commands.md)
- [`internal/cli/color.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/color.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
