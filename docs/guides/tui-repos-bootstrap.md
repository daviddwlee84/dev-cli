---
description: Navigate tasks, repositories, fleet hosts, experiments, remotes, agent skills, and static MCP declarations in the TUI; capture repository quick notes; inventory or adopt existing work safely.
authority: project
status: evolving
verified_on: 2026-09-11
tested_with: skills 1.5.23; Claude Code 2.1.252; Codex/Cursor/Gemini CLI/OpenCode docs 2026-09-01
---

# TUI, repositories, quick notes, and bootstrap

Bare `dev` opens an interactive dashboard when standard input/output are terminals. When piped, it prints the plain task listing so shell composition remains predictable.

There are three independent full-screen models. Bare `dev` / `dev tui` is the
seven-view inventory dashboard below. Preview-labelled `dev flow [repo]` is a
TTY-only, plan-first lifecycle view for one canonical repository; it is not a
dashboard tab or mode. `dev triage` is the cross-repository organizer.

## Seven views

REPOS and REMOTE support `y u` (also row action **copy clone URL**). REPOS reads
the selected checkout's fetch URLs locally, preferring origin and asking when
multiple choices remain. REMOTE copies CloneURL, falling back only to SSHURL,
not the browser URL. Missing/unsafe URLs, stale selections and clipboard errors
do not trigger clone/fetch or an empty copy. To add the selected repository as a
dependency, use the CLI [submodule addition wizard](submodule-workspaces.md#add-a-repository-as-a-submodule);
the dashboard does not add submodules itself.

| View | Question | Source |
|---|---|---|
| TASKS | What am I working on? | task registry plus live Git/runtime facts |
| REPOS | What durable repositories exist here? | configured scan roots and local catalog |
| FLEET | What exists and is active on configured machines? | accepted local REPOS snapshot plus remote `dev` snapshots over SSH |
| TRY | Which experiments can I resume, archive, or graduate? | experiment catalog plus live facts |
| REMOTE | What can I open or clone? | authenticated `gh`/`glab` inventories and cache |
| SKILLS | Which skills would an agent launched from this context read? | context-first targets plus an `A` all-repositories toggle |
| MCP | Which MCP declarations would that context expose? | sanitized static configuration with the same shared scope |

The initial TASKS frame is built before runtime auto-detection, project-root
lookup, cache decoding, shell tool probes, or the optional release refresh can
finish. TASKS, REPOS, and TRY then publish independently from one shared local
cycle. REMOTE, SKILLS, and MCP stay lazy; FLEET uses delayed host-by-host warming. Each requested view has a generation:
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

Switch with `tab`, `h`/`l`, arrows, or left-click a visible tab. A left-button
press selects a visible row; clicking the selected row opens its actions, even
when selection came from the keyboard or startup. No double-click timing is needed.
The wheel moves three rows, and a right-button press selects a row then opens its
actions. Modified row clicks, motion and releases do not activate rows. Opening the menu does
not execute an option. Enter/`o` opens the row. Some terminals require Shift/Option for native
text selection while mouse tracking is enabled.

## Contextual Help (v0.2.24)

Press `?` or click **Help** in the footer. Help starts on **Keys** for the current
dashboard view, keeping that view's TL;DR at the top. It has three tabs:

| Tab | Content |
|---|---|
| Keys | This view's actions, common navigation and configured tools, including availability and conditions |
| Guide | Workflow, columns, colors and symbols; a read-only selected-row snapshot captured when Help opened |
| Manual | All embedded `dev help` topics, with related topics first, plus the shared workflow TL;DR |

Keys and Guide share a search over their entries, with shortcut matches before
guide matches. Search is limited to the selected help view; choose **All views**
to search all seven. Changing help scope does not change the actual dashboard
tab, selection, filter or order. The selected-row explanation appears only for
the view where Help opened and retains unknown, loading or cached observations.
It is an explanation of that captured display, never fresh action authority.

In REPOS, Guide explains why cyan/blue selection overrides the row's usual
color, orange reports dirty paths, and gray alone does not prove Git clean.
It covers `⇡N`/`⇣N`/`⇕`, `=N`, `+N`, `!N`, `?N`, `clean`/`local`, unknown `?`,
cached `~`, loading/truncated `…`, and each column's `—`. LIVE, WT, task counts,
logical owned SIZE and LATEST all have separate explanations. Other views have
their own legends: for example, green MCP means enabled configuration, not a
running or healthy server. Color appearance follows the terminal palette and
`--color` settings.

Manual search covers topic names, titles, headings and body text, with excerpts.
Open a result to read it at the matching passage. Paragraphs wrap; code, ASCII
diagrams and tables preserve their structure and can pan horizontally. `/`
finds text within an article and `n`/`N` moves between matches. Back preserves the
previous query and reading position. Each article names its CLI counterpart.

Use `1–3` or click tabs, `v` or click **View** for scope, `/` or click the search
field, `j/k` to choose or scroll, and Enter to read an entry. PgUp/PgDn, the wheel
and the clickable/draggable scrollbar move through long content. Article pan
controls and left/right arrows reveal wide code or diagrams. Esc stops search
editing, then clears the query, returns a level and finally closes Help. `q`
closes outside text input; letters such as `q`, `v` and `f` stay text while typing.

Help and Ctrl+O share a floating popup with **Expand** and **Close**. Its normal
maximum is 104 columns × 32 rows, leaving at least two cells around it; below
80 columns or 22 rows it fills the screen. `f` expands Help. Click outside to
close without activating a background row. Popup scrollbar dragging is supported
even though dashboard row activation ignores mouse motion.

Opening and searching Help uses only embedded documents, existing tool
availability and the captured dashboard state. It starts no Git, forge, runtime,
provider or MCP probes. Flow and triage retain their independent help interfaces.

## Startup repository and discovery (v0.2.24)

TASKS remains the startup view. The first REPOS visit selects the repository
containing the startup directory without changing ordering, scrolling it into
view when needed. Subdirectories, aliases and linked worktrees resolve through
Git common-directory identity; linked worktrees select the repo parent row.
Manual selection, filtering and sorting take precedence over delayed results.

An outside startup Git repo gets clickable **Add this repo…** and **Scan parent
directory…** entries, also in Ctrl+O. The first appends the main repo root to
`paths.repo_paths`; the second appends that root's parent to `paths.scan_roots`
and includes sibling projects under the normal depth-3 discovery rules. Neither
uses a nested working directory as the candidate root. Prefer exact repo entries
for isolated locations and scan roots for directories dedicated to projects.

Both actions preview the actual configuration file (including `--config`), path,
scope and field change, with clickable confirm/cancel. Scroll long preview details
with the wheel over the text or PgUp/PgDn. Only the selected field is extended;
existing comments, entry order and inherited defaults are retained. Already covered
repos are not added again, and old entries are not automatically consolidated.
Saving reloads local discovery and selects the added repo. Failed/incomplete scans
remain unknown rather than becoming an enrollment suggestion.

Automatic writes use the guarded macOS/Linux file backend, retaining metadata and
private recovery in `$XDG_DATA_HOME/dev/config-recovery`. A concurrent edit or
changed repository identity rejects the preview. Symlink configurations, unsupported
TOML layouts or platforms offer a manual change and config editor instead.
If saving succeeds but reload fails, the dashboard reports both outcomes.

## Common actions

### TASKS

```text
enter/o   open the selected task
p         park warm and enter the next action
c         edit the next action
n/N       quick-add / browse repository notes
ctrl+o    task state filters
```

A COLD worktree task must be rebuilt with `dev resume`; the dashboard does not silently recreate it through a generic open action. A missing or unregistered worktree points to `dev sweep` first, so unique agent artifacts are reported for salvage before the task is resumed or reaped. Enter never opens an abandoned artifact-only directory. At 97 or more terminal cells the TASKS table includes a display-width-aware `REPO` column; narrower layouts retain the previous columns and show repo/path in detail.

### Repository flow

```bash
dev flow              # exact current surface, or picker outside Git
dev flow api          # explicit repository
```

Flow lists every registered worktree plus task-only COLD/DONE rows and labels
canonical/managed/unmanaged/harness/task-only/conflict topology. Use `j/k` for
rows, `h/l` for actions, Tab for panel focus, Enter to build a plan, then `y` or
the displayed typed token to approve only a READY plan. `r` reloads local facts;
`R` offers explicit fetch, review query, or both. Startup and `r` never use the
network.

The plan separates persisted HOT/WARM/COLD/DONE intent from live evidence,
shows remediation/fallback for blocked actions, and is revalidated against the
exact task revision and Git/worktree/runtime/artifact identity before Apply.
Partial effects remain in a result ledger. Eligible unmanaged rows can be
metadata-only Adopted or cleanly Removed while preserving their branch;
canonical/harness/conflict destruction fails closed. See [Repository lifecycle
flow](repository-flow.md) for action and compatibility boundaries.

### REPOS

```text
enter/o   ad-hoc open without creating a task
n         new repository through the clone-aware `repo new` wizard
a/N       quick-add / browse repository notes
space     expand linked worktrees
m         edit repository tags/summary
d         track direct work on the current branch
s         start isolated work: branch + worktree + provisioning + runtime
H         open the repository activity heatmap
y         open copy/context actions
```

Expanded rows explain every linked worktree, including harness-owned `(ephemeral)` and otherwise unmanaged `(external)` checkouts. The LIVE column shows runtime activity separately from task state.

`n` also works when REPOS is empty. The dashboard suspends into
`dev repo new --handoff stay`, preserving config/scaffold overrides, and reloads
local TASKS/REPOS/TRY state after success. It does not introduce a second,
reduced repository-creation implementation.

`dev repo context [repo]` emits the same agent-ready Markdown context available from the TUI copy menu, including paths, Git/worktree/runtime facts, and tasks. `--json` adds the schema-v1 evidence/readiness contract; `--refresh` is the only form that live-probes optional forge and configured fleet sources.

### TRY and REMOTE

TRY handles low-cost experiments, reversible archive/restore, marking, and graduation. Archive is organization, not deletion or disk reclamation.

REMOTE loads lazily so startup does not wait for the network. Its private XDG
cache is decoded after the first view and holds the complete paginated inventory.
Fresh rows require no network; stale rows stay searchable while background
refresh runs. Oversized/malformed payloads and caches fingerprinted for another
configured GH/GL host or Azure target are ignored. GitLab uses explicit
`GITLAB_HOST`/`GLAB_HOST` (default `gitlab.com`) instead of inferring a host from
cwd; successful empty inventories clear old rows. Enter opens an existing local
clone. For an absent repository, `c` confirms a path-confined `project_root`
destination; `enter` clones and stays in the dashboard, while `o` clones and
opens afterward. A `project_root` outside the configured REPOS discovery
roots/depth is rejected before mutation. An animated row/status marker remains
visible through Git clone and the local-only, generation-guarded REPOS refresh;
`q`/Ctrl-C requests cancellation without abandoning the in-flight result. Once
accepted, REMOTE marks the checkout as `repo` and REPOS can search it
immediately. If Git leaves a destination after failure, REMOTE marks its exact
path `inspect` instead of deleting it or offering a misleading retry. `r` forces a forge refresh.
Use `/vis:private` for an exact visibility filter. Notes are enabled only after
a REMOTE row resolves to a local clone. TRY keeps lowercase `n` for creating a
new Try rather than a repository note.

### CLI repository pickers

Bare `dev repo clone` opens a picker over the existing private forge cache. It
uses each provider's exact clone URL, never refreshes the network implicitly,
and keeps a manual URL/path/`owner/name` choice. A stale or incomplete cache is
still selectable with a warning; populate or replace it explicitly:

```bash
dev repo remote --refresh
dev repo clone
```

Outside a checkout, bare `dev start` uses the same picker UI over fast live
local discovery, then fully resolves the selected repository before planning a
task. Inside a repository it retains the immediate current-repository default
without scanning every configured root. The default
external selector is `fzf`; a missing executable falls back to the built-in
Bubble Tea list, and `[picker] command = []` forces that list. Compatible
external commands receive candidates on stdin and must return one unchanged
line on stdout. Non-TTY callers retain line prompts and never receive picker UI.

The repository's `contrib/television/dev-remote-repos.toml` and
`contrib/fzf/dev-repo-clone.bash` compose over the same public source instead of
creating another inventory:

```bash
dev repo remote --refresh                 # populate once or refresh deliberately
ref="$(tv dev-remote-repos)" && [ -n "$ref" ] && dev repo clone "$ref"
source contrib/fzf/dev-repo-clone.bash
dev-repo-clone-fzf
```

Both external recipes require `jq`, read `dev repo remote --cached --json`, and
pass one quoted clone URL to `dev repo clone`. They are examples to copy or
symlink; dev does not modify Television, shell, or chezmoi configuration.

### FLEET

FLEET is a remote host tree. Local is hidden by default; `a` or the action menu
reveals it collapsed at the end, reusing REPOS. Search and coverage exclude local
while hidden. Host rows and actions are available while repositories load.
Space expands/collapses hosts; Enter/`o` navigates to a host or repo.
Ctrl+O/right-click offers host actions.
The HERDR column shows saved registration/enabled state from one shared local
catalog read, independently of SSH snapshots. Enable/disable/remove act on an
exact profile and keep remote sessions running; multiple profiles use a picker.
r refreshes the selected host and catalog metadata. Cached repositories remain
searchable with explicit age and coverage.

Background warming starts after five seconds or an earlier FLEET visit, once per
missing/expired host without password prompts. Set [tui.fleet]
background_refresh = false to keep updates explicit. Search includes collapsed
known repos, temporarily shows matching children, and never triggers extra SSH.
See [the host tree and Herdr actions](remote-fleet.md#dashboard-host-tree).

## Repository quick notes

On TASKS, lowercase `n` opens a one-line quick-add prompt. REPOS reserves `n`
for new repositories and uses `a` for quick-add. Uppercase `N` opens the selected
repository's notes overlay in both views. A child worktree resolves to the same
canonical repository through catalog identity.

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
Inside Git they scan only the exact startup checkout plus global/user sources,
matching what an agent launched there can read and excluding unrelated projects.
Outside Git their startup context retains the cross-repository inventory,
scanning every accepted REPOS target plus the ordinary startup directory.
Uppercase `A` switches both views between that context and all accepted
repositories for this TUI session; old-scope rows are cleared before a guarded
reload, and the other capability view remains lazy. Refreshes keep usable rows
visible, warning-only partial inventories stay fresh, and a visible capability
view resumes automatically after REPOS recovers.

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
`presence:`, and `integrity:`. Press `e` to open the row's primary installed `SKILL.md` (or
lock file for a missing row). The `y` menu copies the file path (`p`), safe
summary (`s`), sanitized source URL (`u`), or whole raw file (`f`).

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
static files. Press `e` to open the selected declaration's `ConfigPath`; the `y`
menu copies its path (`p`), sanitized declaration (`s`), or the entire raw file
(`f`). Raw copy is a local regular-file read capped at 1 MiB and performs no
network access, but it can put credentials and other declarations from that file
into the system clipboard. Rows and structured output remain sanitized. `e`
edits a private working copy, revalidates the observed source immediately before
atomic replacement, and preserves the working copy when it detects a conflict.

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

Keys are case-sensitive and cannot shadow globally owned dashboard bindings. `A` remains configurable for compatibility, but the SKILLS/MCP scope toggle takes precedence on those views. Returning from an editor can reload most config; switching runtime backend requires restarting the TUI.

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
- [`internal/flowtui`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/flowtui)
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/help/topics/notes.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/notes.md)
- [`internal/cli/note.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/note.go)
- [`internal/help/topics/bootstrap.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/bootstrap.md)
- [`internal/cli/adopt.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/adopt.go)
- [`internal/cli/bootstrap.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/bootstrap.go)

## Dashboard lifecycle additions

Use [dashboard lifecycle actions](dashboard-actions.md) for task finish/resume/retire/recovery, full start wizards, Trash disposal and repository browser actions. `Ctrl+O` opens the selected row menu; TASKS `a` shows completed tasks.

## Dashboard navigation and organizer entry

Enter opens a repository/task row or starts FLEET host navigation. Space expands or collapses REPOS/FLEET trees; flat lists leave Space unused. REPOS/TRY `Ctrl+O` offers organization of the
current item, filtered results, or all local work; multi-selection belongs to the
independent triage screen. `1–7` selects TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS,
and MCP respectively. TASKS state filters live in its action menu (`a` still
shows done tasks). Click a data column for ascending → descending → default
ordering; FLEET HOST groups machines. Sorting is local to each view/session and
uses the current snapshot, with unknown values last. The footer keeps two lines
of primary actions and navigation; tools, state filters and sorting are in
`Ctrl+O`, and `?` opens contextual Keys/Guide/Manual help. Existing custom tool bindings for `4–7`
need reassignment; `x`/Ctrl+A are no longer reserved dashboard selection keys.

Triage uses grouped repo/Try checkboxes, mouse selection, and Ctrl+A all/none
within the filtered scope. Results preserve failures when returning to the
dashboard. Git synchronization diagnostics retain a category, exit code, bounded
redacted output and a next step; old receipts cannot recover discarded reasons.
Retries require a new preview. Authentication, fetch and rebase are never
silently performed as error recovery. See [local triage](local-triage.md).


## Progressive loading, searchable actions and yearly activity

REPOS first reads a dated presentation cache, then publishes discovery and Git
observations incrementally. A slow runtime does not hold back repository rows.
Pending/cached rows grant no action authority. Only complete discovery removes
missing rows; failures retain stale/unknown evidence. `r` remains local refresh;
`dev cache clear repos` removes the snapshot. SIZE retains its separate cache.

Ctrl+O menus and submenus support `/` filtering, arrows and Enter; Escape clears
search before closing. `H` displays saved stats, then automatically backfills the
selected repository's full local Git history. Unchanged refs reuse a checkpoint.
Calendar years run oldest first, with wheel/arrows, PgUp/PgDn and Home/End
scrolling; narrow screens split into week segments. `r` refreshes and `b` forces
backfill without fetching. Git activity is still a 20-minute estimate per
non-merge commit.

The REPOS/SKILLS action menu opens the independent
[Skills management wizard](skills-management.md) for scopes, selection, checks
and reviewed mutations; it is not another dashboard tab.


### Local loading measurement

A 60-repository isolated fixture, three runs per case on macOS with Go 1.26.4,
measured the following medians. These are trace acceptance times, not terminal
rasterization; all live Git reads still finish in the background.

| Case | First repository data | Complete local snapshot |
|---|---:|---:|
| Previous dashboard | 3,896 ms | 3,896 ms |
| New, no presentation cache | 14 ms | 3,911 ms |
| New, presentation cache | 15 ms | 2,819 ms |

The first display no longer waits for a full scan. Full-scan duration remains
sensitive to filesystem/process load; this change does not claim cached Git
facts are current. Recovery details are loaded for the focused repository, and
completed rows become available while other repositories are still loading.
