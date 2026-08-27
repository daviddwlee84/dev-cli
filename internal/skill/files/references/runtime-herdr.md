# dev and herdr

Read this when working on a machine that runs herdr, or when a task's state
should be visible in the sidebar.

## The division

```
herdr    per-host runtime: what is running, where the panes are, which agent
         is working right now
dev      cross-host intent: which change streams exist, what state each is in,
         what to do next
git      durable truth: the commits
```

herdr is deliberately **not** synced between machines, and `dev` does not try
to. Each host runs its own herdr; `dev ls --json` is the aggregation point:

```bash
ssh jingle-235 dev ls --json
ssh jingle-247 dev ls --json
```

## What dev asks herdr to do

`dev` shells out to the herdr CLI, which emits JSON for everything it needs:

| dev operation | herdr command |
|---|---|
| list live sessions | `workspace list` + `pane list` (panes carry the cwd) |
| open a checkout | `workspace create --cwd <dir> --label <name>` |
| open a **worktree** | `worktree open --path <path> --label <name>` |
| close a session | `workspace close <id>` |
| show task state | `workspace report-metadata <id> --source dev --token …` |

`worktree open` rather than `workspace create` for a worktree is what gives the
checkout git provenance in herdr: it appears grouped under its parent repo's
space with its own branch and ahead/behind row, rather than as an unrelated
directory.

`dev` does **not** call `herdr worktree create` — see
`worktree-ownership.md` for why.

Note that `workspace close` only ends herdr's session state. It does not delete
the directory, the worktree or the branch. That separation is exactly the one
`dev park` relies on.

## Making task state visible in the sidebar

`dev` reports two metadata tokens on the workspace hosting a task:

- `$stage` — `HOT`, `WARM`, `COLD`, `DONE`
- `$next` — the task's next action

A token only renders if the sidebar row layout names it. Setting one with no
matching layout entry succeeds silently and shows nothing, so add this to
`~/.config/herdr/config.toml`:

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace", "$stage"],
  ["branch", "git_status"],
  ["$next"],
]
```

Which turns a row into:

```
● atp-sipui · HOT
  fix/gx-security-recovery ↑2
  finish the refresh regression test
```

That is far more useful than leaving four agents running in the hope that
seeing their names jogs your memory.

Because the layout is pinned by hand, re-check it against `herdr --default-config`
after a herdr upgrade — upstream changes to the default rows will not reach a
customised layout.

## Sidebar hygiene

The sidebar is working memory, not storage. Aim for three to seven hot spaces
per machine; everything else lives in `dev ls`.

`dev sweep` reports live sessions with no task recorded, which is usually where
the excess is. For each one, either `dev start` in that directory to track it,
or just close it — now that closing is not the same as forgetting.

## Agents are processes, not bookmarks

An agent pane with useful context in it is tempting to leave running forever.
Don't: herdr surfaces a resumable `agent_session` id, and `dev` stores it on
the task, so the conversation can be resumed after the process is gone.

Structure the isolation this way:

```
one independent change stream
        → one worktree
        → one workspace
        → N tabs / panes
        → N agents
```

Not one worktree per agent.

## Without herdr

`dev` selects `herdr` → `tmux` → `none`, in that order, and `runtime.backend`
pins it explicitly. The `none` backend prints a `cd` directive for the shell
wrapper instead of opening a session — so every core operation still works with
nothing but git installed.
