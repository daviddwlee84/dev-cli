# TUI navigation

Bare `dev` opens the dashboard when stdin/stdout are terminals; `dev tui`
opens it explicitly. When piped, bare `dev` prints `dev ls` instead, so shell
composition stays predictable.

## Six views

Switch with `tab`, `l`/`h`, or right/left:

| View | Answers | Data source |
|---|---|---|
| TASKS | What am I working on? | task registry + live Git/runtime state |
| REPOS | What durable repositories exist here? | configured scan roots + asset catalog |
| FLEET | What exists and is active on my other machines? | remote `dev` snapshots over SSH |
| TRY | Which experiments can I resume, archive or graduate? | experiment catalog + live Git/runtime state |
| REMOTE | What can I open or clone? | authenticated forge CLI inventories |
| SKILLS | Which agent skills are active here and globally? | upstream `skills list --json` + lock metadata |

The initial TASKS frame is built before runtime auto-detection, project-root
lookup, cache decoding, shell tool probes or the optional release refresh can
finish. TASKS, REPOS and TRY then publish independently from one shared local
load cycle; optional REMOTE, FLEET and SKILLS live work remains lazy. Each view
has its own generation: `r` supersedes the old read, late results are ignored,
failed refreshes keep usable rows, and a successful empty result clears old
rows. Cached rows and current live results are separate readiness stages; there
is no all-tabs-ready state for views that may never be opened.

## Repository lifecycle preview

`dev flow [repo]` is a separate preview-labelled, full-screen TUI; it does not
share the dashboard model above and requires terminal input/output. In any
canonical or linked checkout, bare `dev flow` opens the canonical repository and
focuses that exact surface. Outside Git it opens a filterable repository picker;
an explicit repo overrides cwd.

Its rows are every Git-registered worktree plus task-only records, including
COLD/DONE tasks with no checkout: `canonical`, `managed`, `unmanaged`, `harness`,
`task-only`, and `conflict`. The center keeps persisted HOT/WARM/COLD/DONE intent
separate from observed Git/runtime/artifact evidence. The right side offers exact
managed lifecycle plans, or metadata-only Adopt and clean branch-preserving
Remove for eligible unmanaged linked checkouts. Canonical checkout removal,
harness cleanup, and destructive conflict resolution fail closed.

```text
j/k or arrows row    h/l or arrows action    Tab focus    Enter plan
r local facts only   R fetch/query/both       ? help       Esc back
```

Enter never applies. A READY plan needs `y`, or its displayed exact typed token
plus Enter. Apply revalidates the task revision and repository/checkout/ref/
runtime/artifact identities, then retains an ordered result ledger even after a
partial failure. The preview has no dirty commit/discard, WIP, takeover, shared-
writer, or unknown-runtime override; blocked plans show remediation and a CLI
fallback. Startup and `r` never use the network. Confirmed `R` evidence is
run-local and limited to refs plus review existence/state/draft/URL/provider/time,
not review decisions or checks.

For one run, set `DEV_TUI_TRACE` to an absolute file that does not already exist:

```bash
DEV_TUI_TRACE=/tmp/dev-tui-trace.json dev
```

The bounded versioned JSON is written with private permissions after the
alternate screen is restored. It contains only relative timings, aggregate row
counts and categorical view/generation/outcome fields—never project/host/tool
names, paths, commands,
key values, URLs, runtime handles or raw errors—and never enters `stats.db` or
the network. `tui.initial_view_returned` means the view string was built, not
that a terminal rasterized it.

REPOS shows branch, dirty state, owned logical size, linked-worktree count and
HOT/WARM/COLD task tallies. Press `space` to expand a repo into its linked
worktrees; each child has its own Git, runtime and task state, and enter opens
that checkout. Git-backed Tries are shown in TRY rather than appearing twice;
REMOTE still labels their local checkout as `try`. Repos with active work sort
first. On a first run with no tasks, switch to REPOS and press `s`, or use TRY
`n` for a low-cost experiment.

REMOTE loads lazily, so dashboard startup never waits on the network. Its
private XDG cache is decoded after the first view and holds the complete
paginated inventory. Fresh rows avoid a network query; stale rows remain
searchable while refresh runs in the background. Oversized/malformed cache
payloads and caches fingerprinted for another configured GH/GL host or Azure
target are ignored. GitLab uses explicit `GITLAB_HOST`/`GLAB_HOST` (default
`gitlab.com`) rather than inferring a host from cwd; a successful empty refresh
clears obsolete rows. `r`
refreshes explicitly. It marks remotes already cloned under
`scan_roots`. Enter opens a local clone; `c` asks before cloning an absent repo
into `project_root`. Filters include visibility, for example `vis:private`.

SKILLS also loads lazily, but local listing never contacts the network or
downloads the provider. It keeps project/global copies of the same skill as
separate rows. After the local snapshot finishes loading, press `c` for the
explicit read-only source check; pressing it earlier waits rather than canceling
the inventory. `r` only reloads local state.

GitHub and GitLab are discovered from authenticated `gh` and `glab` CLIs.
Azure DevOps Services inventory is opt-in because each query needs an explicit
organization and team project:

```toml
[[forge.azure_devops]]
organization = "https://dev.azure.com/acme"
project = "Platform"
```

Repeat the table for additional projects. Azure CLI and its `azure-devops`
extension must already be installed and authenticated; dev does not install the
extension, change Azure defaults, or store credentials.

FLEET is also lazy. It shows cached rows immediately when fresh, waits for and
reuses the accepted REPOS snapshot for this machine, then queries the machines
in `$XDG_CONFIG_HOME/dev/remotes.toml`. It hides this machine by default because
REPOS already provides the richer local view; press `a` to include local rows.
Cache endpoint identity includes the SSH port. Enter opens an explicitly
revealed local row in the normal runtime; a remote row prefers native
`herdr --remote` after focusing the checkout's workspace and falls back to
`ssh -t` at that repository. Git synchronization is deliberately CLI-only
through `dev fleet sync`; the CLI `dev fleet list` continues to include this
machine.

## Vim-style movement

```
j / k                next / previous
ctrl+d / ctrl+u      half page down / up
g / G                top / bottom
h / l, shift-tab/tab previous / next view
/                    filter as you type
?                    full keyboard help overlay
esc                  close prompt/filter/overlay; when clear, quit
q                    quit (or close help/action menu)
```

The `/` query applies to the current view and matches whitespace-separated
terms independently. Structured local filters include `tag:important`,
`remote:none`, `size:>1GiB`, `phase:deprecated` and `where:archived` where
applicable. `gitlab auth` still finds a GitLab repo whose name/description also
contains auth, regardless of word order.

## Actions

TASKS:

```
enter / o  open in the selected runtime
n          add a quick repository note
N          browse/search/edit/delete repository notes
p          park warm, prompting for the next action
c          edit the next action
1 / 2 / 3  show HOT / WARM / COLD
0          clear filters
a          include DONE
```

Generic open never repairs lifecycle drift. A normal COLD worktree task points
to `dev resume`; a missing or unregistered checkout points to `dev sweep`, which
reports any artifact salvage requirement before offering resume/reap cleanup.

REPOS:

```
n / N      quick-add / browse repository notes
enter / o  ad-hoc open: no task, branch or worktree
space      expand/collapse linked worktrees
m          edit repository tags and note
d          track direct work on the current branch (usually main)
s          isolated task: branch + worktree + provisioning + runtime + entry
O / R      cycle / reverse activity/latest/name/git/size/tasks sort
y          copy menu; follow with y/p/b/s/w
```

FLEET:

```
enter / o  open the selected checkout
a          include/hide this machine
r          refresh configured hosts
```

In TRY, `n` remains “new Try”; quick notes intentionally do not attach to Try
assets.

The LIVE column makes runtime state explicit (`herdr:working`, `herdr:idle`). A
collapsed repo with several sessions shows `herdr:N live`; expanded children
show their individual status or `closed`. Ephemeral agent-harness worktrees and
other untracked worktrees stay visible as `(ephemeral)` / `(external)`, so the
children always explain the WT count. LATEST is the newest dirty-file mtime,
commit time, or task update. SIZE is `checkout + private Git` logical bytes;
shared Git objects are shown in detail and never charged to every worktree. The
detail pane also calls out `no remote`, local-only branches and multiple
branch-upstream remotes.

`y` opens a second-key copy menu:

```
yy  agent-ready Markdown context (whole repo on parent; one checkout on child)
yp  selected checkout's absolute path
yb  selected branch
ys  runtime handles and agent session IDs (parent or child scope)
yw  every linked-worktree path in the repo, one per line
```

The same full Markdown is available without a clipboard or TUI through
`dev repo context [repo]`. With no argument it resolves the current repository;
inside a linked worktree it still reports the whole repo.

TRY:

```
enter / o  open a present Try
n          create/clone a Try (name, optional clone ref, git yes/no)
space      mark, deprecate/reactivate, archive/restore, or graduate
a          include deprecated, archived, evicted and graduated history
O / R      cycle / reverse activity/name/phase/size sort
```

Archive is a reversible same-filesystem move under `tries_root/.dev`; it does
not reclaim disk space. Phase 1 has no evict/delete action. The catalog keeps a
stable ID, per-host location, tags/note, last-opened time and graduation
history; Git and size facts remain live/derived.

REMOTE:

```
n / N      notes, only when a local clone exists
enter / o  open an existing local clone
c          confirm and clone an absent repo into project_root
r          refresh configured forge CLIs, replacing the cache
```

SKILLS:

```
a          open the interactive installer (default personal skill catalog)
c          check lock-managed Git sources without installing updates
u          confirm and update only the selected lock-managed skill
r          reload local project/global state without network access
```

The detail pane shows the scope root, installed path, complete agent list,
source URL, manager and update reason. Filters include `scope:global`,
`agent:Codex` and `update:update_available`.

## Heatmap and live config

```
H   open the selected repository's one-year activity heatmap
    b backfills only that repo; r rereads stats; H / esc returns

e   edit the effective config in VISUAL/EDITOR
r   reparse config and reload local/remote data and tool bindings
```

An empty heatmap is actionable: press `b` to derive only the selected repo's
history into stats.db and automatically redraw. `r` does not manufacture data;
it rereads what collectors/backfill have stored.

Returning from `e` live-reloads the config. Changes to scan roots, worktree
policy, forge cache settings and `[[tui.tools]]` take effect immediately. A
runtime backend change needs a restart because existing callbacks and sessions
belong to the backend the TUI opened with; the status line says so rather than
pretending to switch underneath a live workspace.

## External tools are explicit configuration

```bash
dev tui tools
```

shows every binding, the exact command, its source, and whether it can run on
this machine. `dev config init` writes the defaults out in full:

```toml
[[tui.tools]]
key  = "L"
name = "lazygit"
run  = "lazygit"

[[tui.tools]]
key  = "Y"
name = "yazi"
run  = "yazi"

[[tui.tools]]
key  = "V"
name = "nvim"
run  = "nvim ."

[[tui.tools]]
key  = "B"
name = "vibe"
run  = "vibe"
interactive = true

[[tui.tools]]
key  = "P"
name = "plans here"
run  = "claude-plans-here"
interactive = true
```

A configured list replaces the defaults entirely. Commands run through
`$SHELL` in the selected row's checkout, so arguments, environment variables
and executable scripts work. `interactive = true` runs through `$SHELL -lic`
and deliberately evaluates the command *after* rc loading, so this machine's
`vibe` alias and `claude-plans-here` function work. Prefer a real script on PATH
when the binding should be portable to machines with different shell configs.
The dashboard resolves availability in a bounded background load after the first
view; rendering itself never launches the shell. Unknown or missing bindings are
hidden and fail closed until the current config generation has been checked.

Keys are case-sensitive. A tool cannot take one the dashboard owns; config
loading reports the collision instead of silently shadowing movement or quit.
