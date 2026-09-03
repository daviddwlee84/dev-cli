# Parallel agents

How to run several coding agents without them stepping on each other.

## The one rule

> **Worktree per change stream. Exact pane per agent.**

Not one worktree per agent. Isolation follows the *git mutation boundary*, not
who is typing.

```
one independent change stream
        → one worktree
        → one Herdr workspace
        → one exact launch pane
        → one or more coordinated agents
```

## Choose the operation

| Intent | Action |
|---|---|
| Observe existing work | `herdr agent get/read/wait`; do not send probe keys or focus it |
| Spawn independent work | `dev start <repo> --task <name> --base <committed-ref> --json`, validate the exact new pane, then launch there |
| Handoff the same task | Settle the old session, checkpoint dirty code, get explicit user agreement, then resume with a new forked session ID |

A new independent task does not need to wait for an unrelated agent. It does
need a committed base: dirty state in another checkout is not inherited.

## Shared-checkout collision guard

Herdr-aware writer claims (`start --direct`, `start --branch-only`, and
`resume`) compare recognized agents by canonical Git worktree root and reject a
checkout occupied by another pane. `idle`, `done`, `blocked`, `working`, and
`unknown` are all occupied states. Herdr resolves the current pane before
excluding it, so an inherited ID made stale by a pane move is not trusted.

Pure `repo open`, `wt open`, and TUI Enter/focus navigate to the live owner and
do not authorize another writer. `--allow-shared-checkout` is an explicit
escape hatch only for coordinated disjoint writer ownership. Default `dev
start` worktree creation remains allowed because it creates a new boundary.

Use `dev status` in a checkout to see recognized activities.

## When agents can share a checkout

They can when file ownership is explicit and disjoint:

| Parallel work | Same checkout? |
|---|---|
| One researching, one coding | yes, if only one writes |
| Frontend and backend with owned paths | yes, with explicit coordination |
| Both editing one manifest/API surface | no |
| Either changing HEAD, rebasing, formatting globally, or running codegen | no |
| Unknown file ranges | no |

## Ownership boundaries

```
dev         durable task, branch and checkout lifecycle
Herdr       workspace, pane, process detection and live state
SpecStory   rendered history rooted at the launch checkout
Git         durable code and review artifacts
```

A pane move changes layout, not process cwd. `EnterWorktree` does not prove an
already-running SpecStory writer rebound to a new path. Start the new process
from the target worktree root instead.

## Launching a background agent

`dev start` creates the task/worktree/runtime target; it does not start an
agent. The bundled `dev-cli` skill owns the fail-closed cross-tool workflow and
supports standard Claude/Codex plus the local one-shot Copilot launchers.

Do not infer a pane from focus or sidebar order. Missing, reused, fallback,
non-Herdr, or unverified panes are not launch targets.

## Cleanup is explicit

Herdr `done` means a turn settled; it does not mean history is synced, review is
complete, code is committed, or the workspace may close.

- `dev park` records WARM and keeps the checkout; a contained caller runtime stays alive.
- `dev park --cold --push` closes externally and removes a reconstructible checkout.
- `dev done --ff` integrates and records DONE; runtime/worktree/branch remain.
- `dev done --pr` leaves the task and checkout for review.
- `dev flow [repo]` can plan DONE retirement, but Apply still requires an external safe caller.
- `dev retire` closes/waits/removes/reaps; `dev sweep` reports first.

`--cold --keep-session` is rejected because a live session must not point at a
removed checkout.

## Bringing environment into a worktree

Every worktree is a clean checkout. `dev` provisions gitignored files listed in
`worktree.include` and dependencies configured for the repo.

`.claude/settings.local.json` is not a universal default. The Claude one-shot
wrapper preserves an existing Copilot pin and creates/removes one only when
absent; Codex injects its backend. Add the exact file only for an explicit
sticky/plain-Claude profile. `dev` rejects source swaps and destination-parent
symlinks, reports an existing target as skipped, and never logs contents.
