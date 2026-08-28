# TUI navigation

Bare `dev` opens the dashboard when stdin/stdout are terminals; `dev tui`
opens it explicitly. When piped, bare `dev` prints `dev ls` instead, so shell
composition stays predictable.

## Four views

Switch with `tab`, `l`/`h`, or right/left:

| View | Answers | Data source |
|---|---|---|
| TASKS | What am I working on? | task registry + live Git/runtime state |
| REPOS | What durable repositories exist here? | configured scan roots + asset catalog |
| TRY | Which experiments can I resume, archive or graduate? | experiment catalog + live Git/runtime state |
| REMOTE | What can I open or clone? | authenticated `gh` + `glab` inventories |

REPOS shows branch, dirty state, owned logical size, linked-worktree count and
HOT/WARM/COLD task tallies. Git-backed Tries are shown in TRY rather than
appearing twice; REMOTE still labels their local checkout as `try`. Repos with
active work sort first. On a first run with no tasks, switch to REPOS and press
`s`, or use TRY `n` for a low-cost experiment.

REMOTE loads lazily, so dashboard startup never waits on the network. A
private XDG cache makes later switches instant; `r` refreshes explicitly. It
marks remotes already cloned under `scan_roots`. Enter opens a local clone; `c`
asks before cloning an absent repo into `project_root`.

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
p          park warm, prompting for the next action
c          edit the next action
1 / 2 / 3  show HOT / WARM / COLD
0          clear filters
a          include DONE
```

REPOS:

```
enter / o  ad-hoc open: no task, branch or worktree
d          track direct work on the current branch (usually main)
s          isolated task: branch + worktree + provisioning + runtime + entry
space      edit repository tags and note
O / R      cycle / reverse activity/latest/name/git/size/tasks sort
```

The LIVE column makes runtime state explicit (`herdr:working`, `herdr:idle`).
SIZE is `checkout + private Git` logical bytes; shared Git objects are shown in
detail and never charged to every worktree. The detail pane also calls out
`no remote`, local-only branches and multiple branch-upstream remotes.

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
enter / o  open an existing local clone
c          confirm and clone an absent repo into project_root
r          refresh gh + glab, replacing the cache
```

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

Keys are case-sensitive. A tool cannot take one the dashboard owns; config
loading reports the collision instead of silently shadowing movement or quit.
