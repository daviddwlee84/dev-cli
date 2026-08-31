# dev and Herdr

Read this on a Herdr machine, when task state should appear in the sidebar, or
when a `dev start --json` result will be used to launch an agent.

## Division of responsibility

```
Herdr       per-host workspace, pane, process and live-agent state
dev         durable task, branch and checkout lifecycle
SpecStory   history writer rooted at the launched process checkout
Git         durable commits, transcript and plan
```

Herdr is not synced between machines. `dev` persists an advisory runtime handle
with its owning backend name, revalidates the handle against live checkout
coverage before close/resume, and reopens stale handles. This prevents a Herdr
handle being sent to Tmux/none after config changes. Legacy nameless handles are
used only after exact current-backend validation. Root-pane data stays transient.

## What dev asks Herdr to do

| dev operation | Herdr command |
|---|---|
| list live sessions | `workspace list` + `pane list` |
| list pane-level agent activity | `agent list` |
| open a checkout | `workspace create --cwd <dir> --no-focus --label <name>` |
| open a worktree | `worktree open --cwd <parent-root> --path <path> --no-focus --label <repo/branch>` |
| activate an opened checkout | `workspace focus <id>`; outside Herdr, attach the default client |
| dispatch an explicit start command | `pane run <exact-root-pane-id> <command>` |
| close a session | `workspace close <id>` |
| show task state | `workspace report-metadata <id> --source dev --token …` |

`dev` creates worktrees with Git and only asks Herdr to open them. It derives
the canonical parent checkout from Git and passes it with `--cwd`, so grouping
does not depend on whichever Herdr pane invoked `dev`. First-class `worktree
open` gives the child native repository/worktree grouping plus branch/ahead-
behind data. Worktree-mode `dev start` uses the same `repo/branch`
label as `dev wt create`; no special origin labels or metadata are needed.

A plain-workspace fallback reports `surface=workspace` honestly. A root pane is
automatically launchable only when the same successful `worktree open` response
proves a newly created layout. Reuse, fallback, missing/malformed responses,
Tmux, and the `none` runtime never supply a launchable pane. `--no-focus`
preserves the caller's context; never substitute the focused/current pane.

`dev start --run '<shell command>'` consumes that proof directly, dispatches
opaque shell text without persisting or echoing it, and returns after Herdr
accepts the command. It does not wait for completion or report the command's
exit status. `--focus` remains independent and runs only after successful
dispatch. `--json` stays a separate pure creation contract.

Read `parallel-agents.md` for exact machine validation, pane verification,
SpecStory profiles and permission-mode rules.

## Collision preflight

On Herdr, `dev` compares recognized agent cwd values by canonical Git worktree
root before writer-claiming direct/branch starts and resume. It resolves
`herdr pane current --current` before excluding the caller, because inherited
`HERDR_PANE_ID` may be stale after a pane move. Every recognized state—including
`idle`, `done`, and `unknown`—is occupied; malformed/missing activity data fails
closed.

Pure repo/worktree open and TUI Enter reuse the live owner's workspace and do
not claim a second writer. Inside Herdr they focus it in the current client;
outside Herdr the TUI first restores the terminal and then attaches, matching
`hhere`. The root `--allow-shared-checkout` escape hatch is
only for coordinated writer ownership. A default worktree start remains
separate and needs no override.

`dev status` shows recognized activities for the current worktree.

## Sidebar metadata

Task annotations use `$stage` and `$next`. Tokens render only if the local Herdr
row layout names them; reporting an unused token succeeds silently.

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace", "$stage"],
  ["branch", "git_status"],
  ["$next"],
]
```

## Cleanup semantics

Herdr agent `done` means the latest turn settled. It does not establish that
history is synced, review is complete, code is committed, or cleanup is safe.
Never auto-close based on agent state.

- `dev park` records WARM; inside its own workspace it leaves runtime alive for normal agent exit.
- `dev park --cold --push` closes/removes only from an external safe caller.
- `dev done --ff` integrates but keeps runtime/worktree/branch.
- `dev done --pr` leaves task/runtime/worktree for review.
- `dev retire` externally closes, waits, revalidates and removes.
- `dev sweep` reports first and routes DONE cleanup through retire.

`workspace close` itself touches no branch or checkout, but it can kill the
caller before later cleanup runs. Retirement therefore refuses caller
workspace/pane containment, mixed workspaces, and working/blocked/waiting
agents. Unknown status needs external `--close-unknown`; close failure or timeout
stops worktree removal. `--cold --keep-session` remains rejected.

## Agent-session handoff

Herdr exposes live agent session IDs and `Task.AgentSession` exists in the task
schema. `dev prepare` captures one exact `provider:uuid` from its flag, the task,
or a unique covering runtime session and persists it for artifact finalization.
Resume still reopens/rebuilds checkout and runtime only; launching/resuming an
agent conversation remains an explicit wrapper operation.

## Without Herdr

Runtime selection is Herdr → Tmux → Zellij → none unless pinned. Tmux follows
sesh semantics: switch the current client inside tmux, attach outside. Zellij
attaches outside and uses `action switch-session` inside on Zellij 0.44+;
older releases report the missing switch capability instead of nesting a
second client. Tmux and Zellij can report session creation/reuse but no pane.
`none` emits a shell `cd` directive in human mode;
`dev start --json` suppresses that directive and stays pure JSON.

On Windows there is no tmux, Zellij or Herdr, so the backend is always `none`
regardless of configuration. `dev shell-init powershell` still consumes the `cd`
directive (via a `DEV_SHELL_CD_FILE` temp file rather than file descriptor 3).
