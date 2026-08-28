---
description: Retire an integrated dev-cli worktree and runtime safely, from outside the workspace being removed.
authority: project
status: stable
verified_on: 2026-08-28
---

# Agent-safe retirement

A `dev` change stream reaches full retirement only after an external caller closes its runtime and removes its worktree — a feature agent must never destroy the checkout or runtime containing its own process.

## Three milestones, not one

Completion is three separate milestones, and the persisted task state `done` now means the middle one, not the last:

```text
READY     exact final transcript committed after the writer exits
MERGED    branch integrated; runtime/worktree may still exist
RETIRED   runtime absent, worktree removed, optional branch deleted, task reaped
```

`hot`, `warm`, `cold`, and `done` remain the only persisted task states — `done` means MERGED, with cleanup possibly still pending. It does **not** mean the runtime or worktree has already been deleted.

## Commands

| Command | What it does |
|---|---|
| `dev prepare --session <provider:uuid> --plan <path>` | Arms post-writer artifact finalization without closing the running agent. Product changes must already be committed; the transcript itself is deliberately not staged yet. |
| `dev artifact finalize --run-id "$DEV_AGENT_RUN_ID" --if-pending --writer-stopped` | Commits the one exact, stable transcript after its writer has stopped. `--if-pending` no-ops silently when no armed intent matches the run id; `--writer-stopped` confirms the outer wrapper has returned. |
| `dev done --ff` | Rebases the task branch onto its base and fast-forwards it locally. Records MERGED; never closes the runtime, removes a worktree, or deletes a branch. |
| `dev done --pr` | Pushes the branch and opens a pull/merge request through an available forge CLI. The task stays under review, not MERGED. |
| `dev done --merged --base-ref <ref>` | Verifies an externally merged branch is contained in `<ref>` and records MERGED. |
| `dev done --merged --base-ref <ref> --confirm-squash <merge-commit>` | Same, but for a squash merge: attests that the named commit, already proven contained in `<ref>`, represents the feature branch. This is an operator assertion dev cannot verify on its own. |
| `dev artifact discard <intent> --yes` | Records that an intent can never be finalized — its transcript was never written, or its HEAD is gone after a rebase — so it stops blocking integration and retirement. It commits and recovers nothing, prints exactly what is being abandoned, and refuses an intent that is still `armed`, because finalization is the path that preserves a transcript. |
| `dev retire [task-or-worktree] [--close-unknown] [--assume-no-runtime] [--delete-branch] [--timeout <duration>]` | Re-resolves every covering runtime session, refuses active agents and mixed-purpose workspaces, waits for closure, revalidates Git state, and only then removes the linked worktree without force. Deletes the task record only after every requested step succeeds. |
| `dev sweep --merged-worktrees [--base <ref>] [--apply] [--yes] [--close-unknown] [--assume-no-runtime] [--delete-branches]` | From the canonical checkout, reports (and, with `--apply`, retires) both task-tracked and unmanaged linked worktrees whose branches are already contained in the base. |

A dirty checkout does not fail here: `dev done` classifies it against the base
first, offering commit or discard interactively, or taking an explicit
`--dirty` policy from a script. See
[Change-stream workflow](change-stream-workflow.md) for that wizard. The
`--keep-worktree` and `--delete-branch` flags remain accepted on `dev done`
only to fail loudly and point at `dev retire`.

## Normal local flow

From the feature worktree:

```bash
# Commit product changes first. Do not stage the moving transcript.
dev prepare --session claude:<uuid> --plan .claude/plans/task.md
# Exit the agent normally so SpecStory can write its final Markdown.
```

The outer `specstory run` wrapper then calls:

```bash
dev artifact finalize --run-id "$DEV_AGENT_RUN_ID" --if-pending --writer-stopped
```

An external main/integration workspace finishes the job:

```bash
dev done <task> --ff
dev retire <task> --delete-branch
```

`dev done` only integrates and records MERGED. `dev retire` re-resolves every runtime pane, closes eligible sessions, waits for them to disappear, revalidates Git, and removes the worktree without force.

## Pull-request flow

```bash
dev done <task> --pr
# after CI/review and a commit-preserving merge
git fetch origin
dev done <task> --merged --base-ref origin/main
dev retire <task> --delete-branch
```

A squash merge is not ancestry-equivalent, so it needs an explicit operator attestation:

```bash
dev done <task> --merged --base-ref origin/main --confirm-squash <merge-commit>
```

This proves only that the named squash commit is contained in the base; the operator is asserting that it actually represents the feature branch.

## Refusal conditions

`retire.Inspect` refuses to proceed whenever any of the following hold, and it re-checks after closing sessions rather than trusting a stale view:

- the caller's current directory is the target checkout or below it;
- the caller's `HERDR_WORKSPACE_ID`, `HERDR_PANE_ID`, or `TMUX_PANE` identifies a runtime session that covers the target;
- a covering runtime workspace also contains panes outside the target (mixed-purpose workspace);
- any covering agent status is `working`, `running`, `busy`, `blocked`, or `waiting` — **never overridable, by any flag**;
- agent status is empty or `unknown`, **unless** the caller passes `--close-unknown` from outside the target;
- agent status is unrecognized (anything outside the known set) — always blocks;
- runtime enumeration itself fails, **unless** the caller passes `--assume-no-runtime`.

`--close-unknown` and `--assume-no-runtime` only relax fail-closed *observations* (an unreadable status, a runtime list that could not be enumerated). Neither flag ever bypasses caller containment or an active-agent state. `retirement.Service.Retire` also revalidates the target's identity, Git ancestry, in-progress Git operations (`gitx.InProgress`, checking `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `rebase-merge`, `rebase-apply`, `sequencer` — not `REBASE_HEAD`, which Git leaves behind after a rebase completes), and worktree cleanliness after runtime sessions close — reality may have changed while a runtime was draining, so no earlier proof carries across that boundary.

## Periodic cleanup with sweep

```bash
dev sweep --merged-worktrees
# Present the exact candidates/blockers to the user and ask for approval.
dev sweep --merged-worktrees --apply --yes
```

This surfaces both task-tracked DONE worktrees and unmanaged linked worktrees whose named branches are already contained in the base — run it from the canonical checkout. It reports before applying: containment alone is never permission. Dirty Git state, a pending or unreachable artifact, a locked or prunable worktree registration, an in-progress Git operation, and the same runtime refusal conditions as `dev retire` all still block cleanup. Branches remain after retirement by default; pass `--delete-branches` only when the user separately approved deleting them.

## Safety boundaries

Raw `git worktree remove --force` bypasses dev entirely — never run it from an agent that occupies the target. dev's guarantee is only that no dev-mediated path performs a forced removal; it cannot stop an operator or script from calling Git directly.

A real Codex session once deleted its own registered worktree and branch from another checkout. Herdr kept the workspace and terminal alive because a Unix process can hold an open cwd inode after the path is unlinked, and SpecStory then recreated the same path containing only `.specstory/` content. The shell looked alive but was no longer a Git checkout:

```text
failed to reload config: No such file or directory
fatal: not a git repository (or any of the parent directories): .git
```

Treat `runtime alive + Git registration absent + artifact-only path` as an orphan that needs transcript salvage and external reconciliation — **never** as RETIRED.

## Sources

- [`internal/skill/dev-cli/references/agent-retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/agent-retirement.md)
- [`internal/help/topics/retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/retirement.md)
- [`internal/cli/retire.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/retire.go)
- [`internal/cli/artifact.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/artifact.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/retire/safety.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/safety.go)
- [`internal/retire/service.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/service.go)
- [`internal/gitx/transactions.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/gitx/transactions.go)
