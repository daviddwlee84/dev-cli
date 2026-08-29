---
description: Record dev-cli dependencies, upstream preview status, documentation constraints, and behavior that is intentionally incomplete.
authority: project-and-upstream
status: evolving
verified_on: 2026-08-29
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
| terminal multiplexing on Windows | tmux/Zellij/Herdr (POSIX only) | Windows always uses the `none` backend; `dev shell-init powershell` still moves the shell |
| in-place self-update | standalone install (not Homebrew/Scoop/`go install`) | `dev upgrade` prints the package manager's upgrade command instead |

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

Older generated configs may contain `forge.remote_limit = 100`. The field is
still accepted, but complete forge inventories are now paginated and it no
longer caps synchronization. `dev config init` no longer writes it. The
`--limit` flag on `dev repo remote` limits rendered matches after the complete
inventory has been searched.

### Windows is a build target, not a full-feature platform

`dev` compiles and runs on `windows/amd64` and `windows/arm64`, and every release ships a `.zip` for each. Core repository, task and worktree operations work. What differs:

- There is no tmux, Zellij or Herdr, so the runtime backend is always `none`. Grouped runtime/agent activity and named sessions are unavailable; the `cd` directive and PowerShell wrapper still work.
- Shell integration is `dev shell-init powershell`. POSIX shells hand the directory back on file descriptor 3; PowerShell cannot inherit it, so the wrapper passes a temp-file path in `DEV_SHELL_CD_FILE` instead.
- `dev fleet open` starts a child shell (`%COMSPEC%`) rather than replacing the process, because Windows has no `exec(2)`.
- The domain test suites still assume a POSIX filesystem in places, so Windows CI runs the tests advisory-only while compilation, `go vet` and the build are enforced.

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
- `dev done` records MERGED only: it never closes the invoking runtime, removes a worktree, or deletes a branch. Cleanup moved to `dev retire`, which runs from outside the target workspace, refuses active agents and mixed-purpose workspaces, and revalidates Git state after every runtime closure. `dev done --delete-branch` is now an error pointing at `dev retire --delete-branch`, and `--keep-worktree` warns as a no-op.
- `dev sweep --merged-worktrees` enumerates linked worktrees from Git rather than from the task registry, so unmanaged worktrees whose branches are contained in the base become retirable. Containment alone is never permission; dirty state, unfinalized artifacts, in-progress Git operations, and runtime refusals all still block it, and branches survive unless `--delete-branches` is passed.
- `dev sweep` reports a branch-backed task whose branch Git no longer has as dead and offers to reap the record. Such a task cannot be finished, resumed, or retired, because every one of those paths resolves the branch first; the suggestion stays report-only until `--apply`.
- An unknown command is reported instead of discarded. `dev` silences cobra's own error printing and previously also skipped printing anything whose message began with `unknown command`, so a mistyped command produced no output at all on either stream. The message, cobra's "Did you mean this?" suggestions, and a pointer to `--help` are now printed to stderr with exit status 1.
- A stray argument to a command family is an error rather than a silent help render. `dev wt bogus` used to print `dev wt` help and exit 0 because a family has no `Run` of its own; every family node now reports the unknown subcommand and exits 1, while a bare family still prints its help and exits 0.
- Argument-count and flag errors print the failing command's usage block. `--color` still governs whether that block is colorized.
- Each command family's help carries an ASCII orientation diagram and a `See also: dev help <topic>` pointer, and `dev help <command>` resolves a command name or alias to its topic, so `dev help wt` reaches the worktrees page.
- Semantic color covers every human-readable surface, including the interactive dashboard: `dev --color never`, `NO_COLOR` and `TERM=dumb` now disable dashboard color too, which they previously did not.
- `dev sweep` reaps a task whose repository directory no longer exists. Such a record was unreachable by every command in the binary: `done`, `resume`, `park` and `retire` all resolve the repository first, the dead-branch rule excludes direct mode, and the stale-worktree rule requires a recorded worktree path. A live runtime session rules the suggestion out, and reaping removes only dev's record of intent.
- `dev sweep` reports a task-recorded checkout that exists but Git does not register and that holds nothing but agent artifact directories. Removal is offered only when every file inside is byte-identical to one already in the repository; anything else is reported as salvage work and is never removed, including under `--apply`.
- `dev sweep` acts on a cold task whose worktree is still on disk. Inventory has always computed that drift for `dev ls` and the dashboard, but sweep never consulted it, so it was displayed and not actionable.
- `dev retire <path>` reaps the matching task record. Only the by-task form set the task identity, so retiring the same checkout by path left the record behind; the DONE-state and identity checks are unchanged.
- `dev version` reports whether the running build is a published release, and `dev doctor` carries the same line. Nothing in the tool answered "am I current?" before, and `go install ...@latest` resolves to the newest tag, so an untagged feature was invisible to anyone installing it.
- A release publishes platform archives and `SHA256SUMS` and takes its notes from `CHANGELOG.md`. Earlier releases published a GitHub release object and nothing else, so their assets are absent by construction.
- A release publishes Windows `.zip` archives alongside the Unix `.tar.gz` set, and refreshes an in-repo Scoop manifest attached to the release (and pushed to the bucket when a token is configured).
- `dev upgrade` downloads the current release for this platform, verifies it against the release `SHA256SUMS`, and replaces the running binary with an atomic rename (Windows moves the live `.exe` aside and sweeps it on the next run). It defers to Homebrew, Scoop or `go install` when one of them owns the file.
- An interactive `dev` command prints one dim "newer release available" line at most once a day, read from the day-old release cache; it never blocks on the network. `[update] check = false` or `DEV_NO_UPDATE_CHECK` disables it.

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
- [`internal/cli/upgrade.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/upgrade.go)
- [`internal/cli/version.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/version.go)
- [`internal/cli/fleet_exec_windows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet_exec_windows.go)
- [`.github/workflows/release.yml`](https://github.com/daviddwlee84/dev-cli/blob/main/.github/workflows/release.yml)
- [Claude Code parallel agents](https://code.claude.com/docs/en/agents)
