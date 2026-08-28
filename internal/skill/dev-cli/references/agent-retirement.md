# Agent-safe worktree retirement

## The rule

A feature agent may prepare and integrate work, but it must not destroy the
runtime or checkout containing its own process. Completion has three milestones:

```text
READY     exact final transcript committed after the writer exits
MERGED    branch integrated; runtime/worktree may still exist
RETIRED   runtime absent, worktree removed, optional branch deleted, task reaped
```

`hot/warm/cold/done` remain the persisted task states. `done` now means MERGED,
not "everything has already been deleted".

## Normal local flow

From the feature worktree:

```bash
# Commit product changes first. Do not stage the moving transcript.
dev prepare --session claude:<uuid> --plan .claude/plans/task.md
# Exit the agent normally so SpecStory can write its final Markdown.
```

The outer `specstory run` wrapper calls:

```bash
dev artifact finalize --run-id "$DEV_AGENT_RUN_ID" --if-pending --writer-stopped
```

Then an external main/integration workspace runs:

```bash
dev done <task> --ff
dev retire <task> --delete-branch
```

`done` only integrates and records MERGED. `retire` re-resolves every runtime
pane, closes eligible sessions, waits for them to disappear, revalidates Git,
and removes the worktree without force.

## Pull-request flow

```bash
dev done <task> --pr
# after CI/review and a commit-preserving merge
git fetch origin
dev done <task> --merged --base-ref origin/main
dev retire <task> --delete-branch
```

A squash merge is not ancestry-equivalent. It requires an explicit operator
attestation:

```bash
dev done <task> --merged --base-ref origin/main --confirm-squash <merge-commit>
```

This proves only that the named squash commit is in the base; the operator is
asserting that it represents the feature.

## Safety policy

Retirement always refuses when:

- the caller cwd is the target or below it;
- the caller Herdr workspace/pane or tmux pane covers the target;
- a workspace contains panes outside the target;
- an agent is `working`, `blocked`, or `waiting`;
- runtime enumeration fails;
- the checkout is dirty, on the wrong branch, or not contained in its base;
- artifact finalization is armed/finalizing/failed.

`unknown` or empty agent status requires `--close-unknown` from outside the
target. Runtime enumeration failure requires external `--assume-no-runtime`.
Neither flag bypasses caller containment or active-agent states.

Raw `git worktree remove --force` can bypass dev. Never run it from an agent that
occupies the target. dev's guarantee is that no dev-mediated path does so.

## What self-deletion looks like

A real Codex session deleted its own registered worktree and branch from another
checkout. Herdr kept the workspace and terminal alive because Unix processes can
hold an open cwd inode after the path is unlinked. SpecStory then recreated the
same path with only `.specstory/` content.

The resulting shell looked alive but was no longer a Git checkout:

```text
failed to reload config: No such file or directory
fatal: not a git repository (or any of the parent directories): .git
```

Config/skill reloads and new subprocesses failed while Herdr still displayed an
idle agent. Treat `runtime alive + Git registration absent + artifact-only path`
as an orphan requiring transcript salvage and external reconciliation, never as
RETIRED.

## Artifact rules

- Match SpecStory Markdown by the exact UUID in its fixed preamble, not filename
  or newest mtime.
- Finalize only after SessionEnd was observed or the outer wrapper supplies
  `--writer-stopped`; byte stability remains a second, independent check.
- Stage only that transcript and explicitly named plans.
- Never stage another provider/session implicitly.
- `.specstory/statistics.json` is derived and ignored.
- New untracked transcripts above 2 MiB need `--allow-large`.
- Scanner/redactor failure commits nothing and leaves a retryable intent.
- Artifact commits are on the same feature branch, not necessarily the same
  product commit; they carry `Agent-Artifact-Session` and
  `Dev-Artifact-Intent` trailers.
