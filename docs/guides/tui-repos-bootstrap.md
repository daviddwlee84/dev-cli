---
description: Navigate tasks, repositories, fleet hosts, experiments, remotes, agent skills, and static MCP declarations in the TUI; capture repository quick notes; inventory or adopt existing work safely.
authority: project
status: evolving
verified_on: 2026-09-01
tested_with: skills 1.5.23; Claude Code 2.1.252; Codex/Cursor/Gemini CLI/OpenCode docs 2026-09-01
---

# TUI, repositories, quick notes, and bootstrap

Bare `dev` opens an interactive dashboard when standard input/output are terminals. When piped, it prints the plain task listing so shell composition remains predictable.

## Seven views

| View | Question | Source |
|---|---|---|
| TASKS | What am I working on? | task registry plus live Git/runtime facts |
| REPOS | What durable repositories exist here? | configured scan roots and local catalog |
| FLEET | What exists and is active on configured machines? | accepted local REPOS snapshot plus remote `dev` snapshots over SSH |
| TRY | Which experiments can I resume, archive, or graduate? | experiment catalog plus live facts |
| REMOTE | What can I open or clone? | authenticated `gh`/`glab` inventories and cache |
| SKILLS | Which agent skills are installed across repositories and globally? | native versioned 77-agent path registry plus project/global locks |
| MCP | Which MCP servers are declared for supported agents? | sanitized static configuration for Claude Code, Codex, Cursor, Gemini CLI, and OpenCode |

The initial TASKS frame is built before runtime auto-detection, project-root
lookup, cache decoding, shell tool probes, or the optional release refresh can
finish. TASKS, REPOS, and TRY then publish independently from one shared local
cycle. REMOTE, FLEET, SKILLS, and MCP stay lazy. Each requested view has a generation:
`r` supersedes the previous read, late results are ignored, failed refreshes keep
usable rows visible, and a successful empty result removes obsolete rows. Cache
acceptance and current live completion are distinct; there is no all-tabs-ready
state for optional views that may never be opened.

Use an absolute path that does not exist for a one-run diagnostic trace:

```bash
DEV_TUI_TRACE=/tmp/dev-tui-trace.json dev
```

The private, bounded JSON is written after the alternate screen is restored. It
contains relative timings, aggregate row counts, and categorical
view/generation/outcome fields, never repository/task/host/tool names, paths,
commands, key values, URLs, handles, or
raw errors. It is not `stats.db` and is never sent anywhere.
`tui.initial_view_returned` means the Bubble Tea view string was built, not that
the terminal rasterized it.

Switch with `tab`, `h`/`l`, or arrows. Use `j`/`k`, `g`/`G`, `ctrl+d`/`ctrl+u`, `/` to filter, `?` for help, and `esc`/`q` to leave the current mode.

## Common actions

### TASKS

```text
enter/o   open the selected task
p         park warm and enter the next action
c         edit the next action
n/N       quick-add / browse repository notes
1/2/3     HOT/WARM/COLD filters
```

A COLD worktree task must be rebuilt with `dev resume`; the TUI does not silently recreate it through a generic open action. A missing or unregistered worktree points to `dev sweep` first, so unique agent artifacts are reported for salvage before the task is resumed or reaped. Enter never opens an abandoned artifact-only directory. At 97 or more terminal cells the TASKS table includes a display-width-aware `REPO` column; narrower layouts retain the previous columns and show repo/path in detail.

### REPOS

```text
enter/o   ad-hoc open without creating a task
space     expand linked worktrees
m         edit repository tags/summary
n/N       quick-add / browse repository notes
d         track direct work on the current branch
s         start isolated work: branch + worktree + provisioning + runtime
H         open the repository activity heatmap
y         open copy/context actions
```

Expanded rows explain every linked worktree, including harness-owned `(ephemeral)` and otherwise unmanaged `(external)` checkouts. The LIVE column shows runtime activity separately from task state.

`dev repo context [repo]` emits the same agent-ready Markdown context available from the TUI copy menu, including paths, Git/worktree/runtime facts, and tasks.

### TRY and REMOTE

TRY handles low-cost experiments, reversible archive/restore, marking, and graduation. Archive is organization, not deletion or disk reclamation.

REMOTE loads lazily so startup does not wait for the network. Its private XDG
cache is decoded after the first view and holds the complete paginated inventory.
Fresh rows require no network; stale rows stay searchable while background
refresh runs. Oversized/malformed payloads and caches fingerprinted for another
configured GH/GL host or Azure target are ignored. GitLab uses explicit
`GITLAB_HOST`/`GLAB_HOST` (default `gitlab.com`) instead of inferring a host from
cwd; successful empty inventories clear old rows. Enter opens a local clone; `c` confirms before
cloning an absent repository into a path-confined `project_root`; `r` forces a
refresh.
Use `/vis:private` for an exact visibility filter. Notes are enabled only after
a REMOTE row resolves to a local clone. TRY keeps lowercase `n` for creating a
new Try rather than a repository note.

### FLEET

FLEET also loads lazily. It keeps valid cached rows usable while waiting for the
current REPOS generation to be accepted, reuses that snapshot for the local host
instead of scanning repositories again, and then fans out to configured SSH
hosts. This machine is hidden by default because REPOS already provides the
richer local inventory; press `a` to include/hide local rows. Press `r` to
supersede an older request and force a live reload. Changing any endpoint field,
including SSH port, invalidates that host's cache. None of this changes
`dev fleet list`, whose non-interactive output continues to include this machine.

## Repository quick notes

On TASKS and REPOS, lowercase `n` opens a one-line quick-add prompt and uppercase `N` opens the selected repository's notes overlay. A child worktree resolves to the same canonical repository through catalog identity.

```text
j/k       move
/         search body, tags, and repository
Enter     expand or collapse the Markdown body
a or n    add another note
e         edit the body in VISUAL/EDITOR
d         enter confirmation; y deletes
Esc       return without changing data
```

The optional REPOS column `notes` shows a count. It is off by default because the table is width-constrained. Repository detail shows the count and latest preview when notes exist; task detail does so when the task resolves to a loaded repository row.

The same source-of-truth workflow is available without the TUI:

```bash
dev note add "try event subscription" --repo api --tag idea
dev note list api
dev note search "event subscription" --repo api
dev note show <id-or-prefix>
dev note edit <id-or-prefix>
dev note delete <id-or-prefix>       # confirms
dev note path api
dev note reindex
```

A note ID prefix must be unique and at least eight characters.

Markdown under configured `paths.state_dir/notes` is durable; `$XDG_CACHE_HOME/dev/notes.db` is only a rebuildable search index. See the [complete generated command reference](../reference/commands-config.md#complete-generated-command-reference) for exact flags.

SKILLS and MCP both load lazily after the current REPOS generation is accepted.
They scan each canonical repository and add the exact startup checkout when it is
a distinct linked worktree; global/user sources are read once. Refreshes keep
usable rows visible, warning-only partial inventories stay fresh, and a visible
capability view resumes automatically after REPOS recovers.

SKILLS reads the versioned `skills@1.5.23` 77-agent path registry and lock files
natively—no Node, `skills`, npm, `npx`, agent detector, or project code runs.
Same-named project/global/repository rows remain distinct. Presence and embedded
`dev-cli` integrity are local facts; update state is a separate lock-recorded
upstream comparison. `c` is the explicit grouped Git source check, `a` opens the
upstream interactive installer, and `u` confirms before updating only the
selected lock-managed skill in that row's checkout. The check hashes Git object
bytes without populating a checkout; locale-dependent non-ASCII folder hashes stay
unverifiable. Mutations require a directly installed `skills` executable, skip
repository-local npm shims, reject source-less locks, and serialize cooperating
`dev` processes. Filters include `repo:`, `scope:`, `agent:`, `update:`,
`presence:`, and `integrity:`.

MCP reads static declarations for Claude Code, Codex, Cursor, Gemini CLI, and
OpenCode. It preserves file/scope rows and exact Claude local project keys instead
of guessing a generally effective configuration; only Claude's documented
user/project/local/managed project approvals are resolved. An absolute
`CLAUDE_CONFIG_DIR` relocates Claude user sources. Configured/enabled/disabled
never means connected or healthy. Provider-specific environment reference names
and finite OAuth facts remain, while values, raw arguments, URL
credentials/path/query/fragment, and indirect file content are discarded before
rows enter the model. The scanner never runs a server, helper, URL, or agent MCP
command. Filters include `repo:`,
`agent:`, `scope:`, `transport:`, `managed:`, and `state:`; `r` only rereads
static files.

## External tools

```bash
dev tui tools
```

Configured tools run through `$SHELL` in the selected checkout while the alternate screen is suspended. `interactive = true` uses `$SHELL -lic` so local aliases/functions can resolve; use a real executable on `PATH` when the binding must be portable. Availability probes run in a bounded background load after the first view; rendering never starts a login shell, and unresolved bindings fail closed.

```toml
[[tui.tools]]
key = "L"
name = "lazygit"
run = "lazygit"
```

Keys are case-sensitive and cannot shadow dashboard-owned bindings. Returning from an editor can reload most config; switching runtime backend requires restarting the TUI.

## Inventory an existing machine

Start with a report:

```bash
dev bootstrap ~/code /mnt/work
dev bootstrap ~/code --json
```

The scanner identifies canonical checkouts, linked worktrees, bare repositories, and symlink aliases, then deduplicates them by Git identity.

The recommended organization layer is a non-destructive symlink index:

```bash
dev bootstrap ~/code --index ~/Projects --layout flat
dev bootstrap ~/code --index ~/Projects --layout flat --apply
```

Physical moves are a separate, stricter mode. A move plan blocks dirty repositories, linked worktrees, live sessions/current working directories, aliases that would break, occupied destinations, and cross-filesystem renames. If any row is blocked, apply moves none.

## Adopt work already in flight

Bootstrap answers **where repositories are**. Adoption answers **which existing branches, worktrees, and sessions are active work**:

```bash
dev adopt
dev adopt --apply
```

Adopt reports by default and only writes task entries after `--apply` plus confirmation. It does not move, rename, or delete checkouts, and it excludes recognized harness-ephemeral worktrees.

## Sources

- [`skills@1.5.23` agent path registry](https://github.com/vercel-labs/skills/blob/v1.5.23/src/agents.ts)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
- [Cursor MCP configuration](https://cursor.com/docs/mcp)
- [Gemini CLI MCP configuration](https://google-gemini.github.io/gemini-cli/docs/tools/mcp-server.html)
- [OpenCode MCP configuration](https://opencode.ai/docs/mcp-servers/)
- [`internal/help/topics/tui.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/tui.md)
- [`internal/help/topics/notes.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/notes.md)
- [`internal/cli/note.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/note.go)
- [`internal/help/topics/bootstrap.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/bootstrap.md)
- [`internal/cli/adopt.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/adopt.go)
- [`internal/cli/bootstrap.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/bootstrap.go)
