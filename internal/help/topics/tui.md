# TUI navigation

Bare `dev` opens the dashboard when stdin/stdout are terminals; `dev tui`
opens it explicitly. When piped, bare `dev` prints `dev ls` instead, so shell
composition stays predictable.

## Three views

Switch with `tab`, `l`/`h`, or right/left:

| View | Answers | Data source |
|---|---|---|
| TASKS | What am I working on? | task registry + live Git/runtime state |
| REPOS | What repositories exist here? | recursive configured scan roots |
| REMOTE | What can I open or clone? | authenticated `gh` + `glab` inventories |

REPOS shows branch, dirty state, linked-worktree count and HOT/WARM/COLD task
tallies. Repos with active work sort first; untracked clean repos remain
visible below them. On a first run with no tasks, switch to REPOS and press `s`
to start one, or run `dev adopt` to import work already in flight.

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
esc                  clear prompt/filter; when clear, quit
q                    quit
```

The `/` query applies to the current view and matches whitespace-separated
terms independently. `gitlab auth` finds a GitLab repo whose name/description
also contains auth, regardless of word order.

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
enter / o  open the repository
s          start a task: branch + worktree + provisioning + runtime + entry
```

REMOTE:

```
enter / o  open an existing local clone
c          confirm and clone an absent repo into project_root
r          refresh gh + glab, replacing the cache
```

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
