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
| task lifecycle | `start`, `park`, `resume`, `done`, `sweep`, `ls`, `status` |
| linked worktrees | `wt list`, `wt create`, `wt open`, `wt rm`, `wt plan`, `wt provision` |
| repositories/remotes | `repo list`, `repo context`, `repo clone`, `repo open`, `repo new`, `repo sync`, `repo remote`, `repo mark` |
| machine inventory | `bootstrap`, `adopt`, `doctor` |
| experiments | `try`, `tries …`, `graduate` |
| terminal UI | `tui`, `tui tools` |
| configuration/shell | `config init/show/path`, `shell-init`, completion |
| generated policy/assets | `gitignore`, `skill install/sync` |
| activity/data | `stats …`, `cache …` |
| help | `help [topic]` |

Run `dev <command> --help` for the installed binary; this site describes the repository version identified in its freshness metadata.

## High-value structured interfaces

```bash
dev ls --json
dev repo list --json
dev repo context [repo]
dev repo remote --json
dev bootstrap --json
```

Prefer JSON or the agent-ready Markdown context over parsing human tables. Tables are optimized for terminals and may change columns/width without changing the structured contract.

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
| `[forge]` / `[[forge.azure_devops]]` | remote result limit, cache TTL, and opt-in Azure organization/project targets |
| `[bootstrap]` | recursion, symlink handling, index/layout policy |
| `[tui]` / `[[tui.tools]]` | columns, sorting, and external-tool bindings |
| `[stats]` | sampler and optional WakaTime import |

A repository may commit `.dev.toml` for worktree provisioning overrides that should travel with the project. Keep host-specific paths and credentials in the user config or ignored environment files, not in the repository override.

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
