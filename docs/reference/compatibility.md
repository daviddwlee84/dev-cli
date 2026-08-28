---
description: Record dev-cli dependencies, upstream preview status, documentation constraints, and behavior that is intentionally incomplete.
authority: project-and-upstream
status: evolving
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
---

# Compatibility and known limitations

This page separates graceful degradation from real limitations. Reverify it whenever command/runtime code or version-sensitive Claude Code documentation changes.

## dev-cli capability matrix

| Capability | Dependency | Without it |
|---|---|---|
| core repository/task/worktree operations | Git | unavailable; Git is the only hard runtime dependency |
| rich grouped runtime and agent activity | Herdr server + CLI | `auto` tries tmux, then Zellij, then none |
| named terminal session | tmux or compatible Zellij | `none` preserves core behavior and shell navigation |
| GitHub pull requests/remotes | `gh` authenticated | branch still pushes when Git works; browser/manual flow may be needed |
| GitLab merge requests/remotes | `glab` authenticated | same graceful fallback |
| worktree dependency setup | ecosystem manager (`uv`, npm, Cargo, etc.) | plan reports the missing tool and keeps the checkout |
| interactive dashboard | terminal input/output | bare `dev` prints the plain task list when piped |
| repository-note search | linked `modernc.org/sqlite` with FTS5 | no external `sqlite3` executable is required |

## Confirmed project limitations

### Note search and filesystem durability vary by text and platform

Latin note queries use term-wise prefix FTS and SQLite ranking. Non-ASCII queries use literal term-wise substring matching because SQLite's `unicode61` tokenizer does not segment arbitrary CJK substrings; those results do not use the same FTS ranking.

Note writes sync the file and atomically rename it on every supported platform. Unix also syncs the containing directory after rename/delete. The Windows implementation cannot provide that directory-fsync step, so a sudden power loss has a narrower durability guarantee there. Concurrent mutations by cooperating `dev` processes are serialized and each Markdown replacement is atomic; arbitrary external writers do not participate in that lock.

### Pull-request completion is not tracked automatically

`dev done --pr` pushes and opens a pull/merge request, then leaves the task, runtime, and worktree unchanged because review owns integration. Current `dev sweep` does not query the forge to infer that a request later merged. Verify integration and finish/reconcile deliberately; do not assume remote merge implies local DONE.

### Agent session capture is reserved, not wired

The task schema has an `AgentSession` field and Herdr inventory can expose live agent session IDs. The production start/park/resume path does not yet capture or attach that ID. Treat the field and live inventory as observability/future integration, not a promise that `dev resume` restores the coding-agent conversation.

### Built-in forge cache TTL differs from generated config

`dev config init` writes `forge.cache_ttl = "15m"`. With no config file, the current built-in `Forge.CacheTTL` zero value means an existing valid cache is not rejected by age; explicit `r` refresh still replaces it. Run `config init` or set the TTL when freshness matters.

### Direct mode has a smaller lifecycle

A direct task uses the canonical checkout and cannot go COLD, because cold cleanup would remove a directory the repository needs. Use branch-only or worktree mode for cross-machine reconstruction.

## Current behaviors that are implemented

These were historical gaps and should not be reintroduced as limitations:

- `dev start --focus` activates the runtime after non-JSON creation.
- TUI navigation refuses to open a missing COLD checkout and directs the user to `dev resume`.
- Runtime handles now record backend provenance and are revalidated before cleanup.
- `auto` runtime selection includes Zellij between tmux and none.

- `dev done` opens an interactive finish wizard on a TTY when `--ff`/`--pr` are both omitted, analyzing a dirty checkout against the base (commit, discard, or cancel) instead of rejecting any uncommitted change outright; a non-interactive caller still passes an explicit `--dirty` policy and `--yes` for a destructive discard.
- Human-readable output now carries semantic color (`--color auto|always|never`), automatically disabled when `NO_COLOR` is set, `TERM=dumb`, or stdout/stderr is not a terminal.

## Claude Code status matrix

| Surface | Status on 2026-08-28 | Compatibility note |
|---|---|---|
| core agentic loop/tools | official | exact tools depend on surface/model/policy |
| worktree isolation | official, rapidly evolving | cleanup, baseRef, resume, and safety details are version-sensitive |
| subagents | official | fork/background defaults and naming changed across 2.1.x |
| Agent view | research preview | retain manual sessions/worktrees fallback |
| agent teams | experimental, disabled by default | no automatic worktree isolation; resumption/task/shutdown limitations |
| Dynamic Workflows | versioned feature | requires v2.1.154+; availability/config and limits vary by release |
| Agent SDK | official SDK | language SDK parity and event availability can differ |

`TeamCreate` and `TeamDelete` are historical tools removed in v2.1.178. The old `Task` worker tool name was replaced by `Agent`; do not confuse either with Task-list metadata tools.

## Documentation-stack limitations

- `mkdocs-static-i18n` reports that `zh-TW` is not a Lunr language, so Chinese search does not receive a dedicated Lunr stemmer/segmenter. Navigation and pages still build.
- `mkdocs-llmstxt` skips pages after static-i18n remaps them. This project replaces that empty output with a deterministic local generator and runs strict builds again.
- The copy-to-LLM plugin's button labels remain English on zh-TW pages; this is cosmetic.
- MkDocs and Material are pinned below their next major versions because the selected plugin stack targets MkDocs 1.x/Material 9.x.

## Reporting a compatibility change

Update the owning guide, both languages, this matrix, and [Sources and freshness](sources-freshness.md). Include the tested binary/version and a code/test or official-source link. Never preserve a known false statement for backward documentation compatibility.

## Sources

- [`internal/runtime/runtime.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/runtime.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
- [`internal/task/task.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/task/task.go)
- [`internal/forge/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/cache.go)
- [`internal/note/index.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/index.go)
- [`internal/note/store.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/store.go)
- [`internal/note/sync_windows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/sync_windows.go)
- [Claude Code parallel agents](https://code.claude.com/docs/en/agents)
