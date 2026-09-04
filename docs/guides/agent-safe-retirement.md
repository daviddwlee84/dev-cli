---
description: Retire an integrated dev-cli worktree and runtime safely, from outside the workspace being removed.
authority: project
status: stable
verified_on: 2026-09-03
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
| `dev flow [repo]` | Preview UI for an exact DONE task: Enter builds a revision-bound Retire plan, then approval applies it. Branch deletion requires the displayed typed token. |
| `dev sweep --merged-worktrees [--base <ref>] [--apply] [--yes] [--close-unknown] [--assume-no-runtime] [--delete-branches]` | From the canonical checkout, reports (and, with `--apply`, retires) both task-tracked and unmanaged linked worktrees whose branches are already contained in the base. |
| `dev sweep --ephemeral-worktrees [--stale-days <n>] [--json]` | From a canonical non-bare checkout, emits a strict Claude Workflow V1 report; JSON schema 1 is report-only. |
| `dev sweep --ephemeral-worktrees --apply [--delete-branches --base <ref>]` | Requires a TTY and per-item confirmation, then revalidates each approved fingerprint under the common-dir cleanup lock before plain non-force removal. |

A dirty checkout does not fail here: `dev done` classifies it against the base
first, offering commit or discard interactively, or taking an explicit
`--dirty` policy from a script. See
[Change-stream workflow](change-stream-workflow.md) for that wizard. The
`--keep-worktree` and `--delete-branch` flags remain accepted on `dev done`
only to fail loudly and point at `dev retire`.

After a bare interactive `dev done` reaches MERGED, its cleanup step performs
a read-only retirement preview and asks whether to keep, retire, or retire and
delete the contained branch. Covering Herdr panes and recognized agent states
are shown before approval. Caller-owned Herdr workspaces use a fresh exact-pane
external coordinator; working/blocked/waiting agents and mixed workspaces are
never overridden.

For a read-only explanation before acting, render or open the generic
`workspace-closeout` recipe from the exact checkout:

```bash
dev prompt render workspace-closeout .
dev prompt open workspace-closeout . --agent my-agent
```

The recipe includes the full retirement audit, but its result is advisory.
`eligible` and a merged PR never authorize cleanup; an external `dev retire`
still recollects and revalidates every gate. If `dev done --ff` or
`dev git pull-rebase` stops at a conflict, use `prompt open` only to discuss the
semantic resolution, then explicitly continue/abort Git and rerun the lifecycle
command. See [Prompt handoffs](prompt-handoffs.md).

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

In `dev flow`, Retire shows conditions, effects, retained resources, and the CLI
fallback before approval. Apply locks and reloads the task revision plus exact
repository/worktree/ref/runtime/artifact authority, repeats safety checks after
runtime closure and before removal, and deletes the task record last. A later
failure preserves a step ledger and recovery; completed effects are not called
rolled back.

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

`--close-unknown` and `--assume-no-runtime` only relax fail-closed *observations* (an unreadable status, a runtime list that could not be enumerated). Neither flag ever bypasses caller containment or an active-agent state. `retirement.Service.Retire` also revalidates the target's identity, Git ancestry, in-progress Git operations (`gitx.InProgress`, checking `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `BISECT_LOG`, `rebase-merge`, `rebase-apply`, `sequencer` — not `REBASE_HEAD`, which Git leaves behind after a rebase completes), and worktree cleanliness after runtime sessions close — reality may have changed while a runtime was draining, so no earlier proof carries across that boundary.

## Periodic cleanup with sweep

```bash
dev sweep --merged-worktrees
# Present the exact candidates/blockers to the user and ask for approval.
dev sweep --merged-worktrees --apply --yes
```

This surfaces both task-tracked DONE worktrees and unmanaged linked worktrees whose named branches are already contained in the base — run it from the canonical checkout. It reports before applying: containment alone is never permission. Dirty Git state, a pending or unreachable artifact, a locked or prunable worktree registration, an in-progress Git operation, and the same runtime refusal conditions as `dev retire` all still block cleanup. Branches remain after retirement by default; pass `--delete-branches` only when the user separately approved deleting them.

### Verified Claude Workflow ephemeral cleanup

Claude Workflow turn-scoped worktrees have a separate strict V1 path:

```bash
dev sweep --ephemeral-worktrees --stale-days 14
dev sweep --ephemeral-worktrees --json
dev sweep --ephemeral-worktrees --apply
dev sweep --ephemeral-worktrees --apply --delete-branches --base main
```

The command runs only from a canonical non-bare checkout. Its bounded,
fixed-depth adapter reads private metadata under `~/.claude/projects`, verifies
validated workflow/agent IDs, exact canonical worktree mapping,
`spawnedWithWorktree`, `isolation=worktree`, and matching journal linkage, but
never decodes or emits prompts, scripts, logs, result bodies, or transcript
content. Unknown add-only fields are tolerated; wrong required types, path
mismatch, duplicate claims, unsafe/symlink/reparse/group-or-world-writable
metadata, source mutation, bound exhaustion, and future/conflicting/unparseable
time fail closed.

V1 terminal liveness requires workflow `completed|killed`, matching agent `done`,
one journal `started` and `result`, and no same-ID resumed transcript. A killed
workflow with no result, progress, or a resume is `unknown` regardless of age;
there is no attestation bypass. `--stale-days` measures provider inactivity,
defaults to 14, and has a minimum of 1.

Current-provider ownership is not proved by path continuity. Apply additionally
requires provider-observed branch, HEAD, common-dir, and an opaque non-replayable
registration generation matching the live registry. Claude Code 2.1.259 records
none of those Git identity facts, so current Claude claims report
`provider-git-identity: unknown` and remain report-only even when `--apply` is
requested. This prevents stale terminal metadata from attaching to a replacement
checkout at the same path. A path, branch convention, or reusable GitDir pathname
must never be treated as the missing generation.

The independent safety audit also requires a present, registered, non-main,
named, unlocked, non-prunable linked worktree with exact common-dir, live branch,
registry HEAD, and live HEAD agreement. Staged, unstaged, conflicted, untracked,
ignored, or recursively inspected submodule content blocks cleanup, as do Git
operations, task claims, unsafe artifact intents, caller containment, unknown
runtime inventory, or any covering runtime. Missing, prunable, unregistered, and
orphan paths remain report only.

JSON schema 1 contains only normalized identity/state/time, Git/path/branch/HEAD
facts, checks, actions, diagnostics, fingerprints, and counts. It is report-only.
Apply rejects `--yes`, `--close-unknown`, `--assume-no-runtime`, `--no-runtime`,
and JSON; it requires a terminal and confirms each item. Under a common-dir
cleanup lock it rediscovers the repository and recollects provider, Git, task,
artifact, runtime, and caller proof immediately before each removal. A changed
fingerprint is `skipped-changed`.

Removal uses plain `git worktree remove` without force and verifies both path and
registration are gone. It never closes sessions, prunes, deletes Claude metadata,
or rescues/stashes/commits dirty work. The named branch survives by default, so
unique commits remain recoverable. Optional deletion separately requires
`--delete-branches --base <ref>`, unchanged base/branch tips, containment, zero
unique commits, and ordinary `git branch -d`; any failure after removal keeps the
branch and reports partial completion.

## Safety boundaries

Raw `git worktree remove --force` and configured external tools bypass dev entirely — never run them from an agent that occupies the target. dev's guarantee is only for dev-mediated paths; it cannot stop an operator or script from calling Git directly.

Normal task-backed retirement uses `internal/taskflow`. The explicit unmanaged
path form of `dev retire` remains an isolated compatibility implementation, and
some `sweep` record-only/orphan-salvage actions remain outside taskflow. Existing
CLI acknowledgement flags remain compatible; the flow preview intentionally
offers none of them.

A real Codex session once deleted its own registered worktree and branch from another checkout. Herdr kept the workspace and terminal alive because a Unix process can hold an open cwd inode after the path is unlinked, and SpecStory then recreated the same path containing only `.specstory/` content. The shell looked alive but was no longer a Git checkout:

```text
failed to reload config: No such file or directory
fatal: not a git repository (or any of the parent directories): .git
```

Treat `runtime alive + Git registration absent + artifact-only path` as an orphan that needs transcript salvage and external reconciliation — **never** as RETIRED.

`dev sweep` now recognises that shape rather than leaving it to be spotted by eye. When a task records a checkout that exists but Git does not register, and the directory holds nothing but agent artifact folders, sweep reports it as an abandoned agent workspace. It offers to remove it only when every file inside is byte-identical to one the repository already has; anything that differs is listed as salvage work and left untouched, including by `--apply`.

Byte equality rather than mere presence is the point. A transcript writer that outlives its worktree usually flushes a longer final version than the copy committed earlier, so a same-named file in the repository is not evidence that the tail was saved.

## Sources

- [`internal/skill/dev-cli/references/agent-retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/agent-retirement.md)
- [`internal/help/topics/retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/retirement.md)
- [`internal/cli/retire.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/retire.go)
- [`internal/taskflow/retire.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/taskflow/retire.go)
- [`internal/cli/artifact.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/artifact.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
- [`internal/ephemeral`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/ephemeral)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/retire/safety.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/safety.go)
- [`internal/retire/service.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/service.go)
- [`internal/retire/audit.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/audit.go)
- [`internal/cli/prompt_command.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/prompt_command.go)
- [`internal/gitx/transactions.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/gitx/transactions.go)
