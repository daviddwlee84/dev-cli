# Agent-safe worktree retirement

Recursive submodule disposal is an explicit extension: verify child tasks/artifacts/runtime, local data and fresh remote recovery first, then stage children inside-out and remove the outer checkout without force. Preserve canonical/shared Git and the outer branch. A retained journal is recovered with `dev submodule recover`, not deleted using an old remote proof. See `submodules.md`.

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

`dev flow [repo]` offers Retire only for an exact DONE task. Enter first shows a
revision-bound plan with conditions, ordered effects, retained resources, and a
CLI fallback; branch deletion needs the displayed typed token. Apply locks and
reloads task/repository/worktree/ref/runtime/artifact authority, repeats safety
checks after closure and before removal, and deletes the task last. If a later
effect fails, its ledger reports completed work and recovery without claiming a
rollback.

For periodic cleanup from the canonical main checkout:

```bash
dev sweep --merged-worktrees
# Present the exact candidates/blockers to the user and ask for approval.
dev sweep --merged-worktrees --apply --yes
```

This includes unmanaged linked worktrees whose named branches are already
contained in main. It never treats containment alone as permission: dirty Git,
pending artifacts and runtime blockers still stop cleanup. Branches remain by
default; add `--delete-branches` only when the user approved that separately.

Claude Workflow ephemeral worktrees use a separate, stricter path:

```bash
dev sweep --ephemeral-worktrees --stale-days 14
dev sweep --ephemeral-worktrees --json
dev sweep --ephemeral-worktrees --apply
dev sweep --ephemeral-worktrees --apply --delete-branches --base main
```

Run it from the canonical non-bare checkout. The schema-v1 report joins one
bounded, fixed-depth `~/.claude/projects` mapping to live Git state without
decoding or emitting prompts, scripts, logs, result bodies, or transcript
content. V1 requires workflow `completed|killed`, matching agent `done`, journal
`started` and `result`, no same-ID resumed transcript, and provider inactivity
older than `--stale-days` (default 14, minimum 1). Killed/no-result, progress,
resume, malformed/mutating metadata, or unknown time stays `unknown`; there is no
operator-attestation bypass.

Ownership must also bind provider-observed branch, HEAD, common-dir, and an opaque
non-replayable registration generation to the live target. Claude Code 2.1.259
records no such Git identity, so current Claude Workflow claims report
`provider-git-identity: unknown` and remain report-only even with `--apply`.
A path, derived branch name, or reusable GitDir pathname is never a substitute;
otherwise stale terminal metadata could authorize a replacement checkout at the
same path.

The worktree must remain registered, present, non-main, named, unlocked and
non-prunable, with exact common-dir/branch/registry-HEAD/live-HEAD agreement. It
must have no staged/unstaged/conflicted/untracked/ignored/recursive-submodule
content, no Git operation, task claim, unsafe artifact intent, caller
containment, or covering runtime. Missing, prunable, unregistered, and orphan
paths are report only. Apply rejects `--yes`, `--close-unknown`,
`--assume-no-runtime`, `--no-runtime`, and JSON; it requires a TTY and per-item
confirmation. Under a common-dir cleanup lock it recollects every fact and
compares the stable candidate fingerprint before plain non-force removal. It
never prunes, closes sessions, deletes provider metadata, or rescues/stashes/
commits dirty work.

A branch is retained by default, so a clean checkout with unique commits may be
removed safely. Optional branch deletion is a separate proof requiring an
explicit base, unchanged branch/base tips, containment, zero unique commits, and
ordinary `git branch -d`; any post-removal failure leaves the branch retained and
reports partial completion.

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

Raw `git worktree remove --force` and configured external tools can bypass dev.
Never run them from an agent that occupies the target. dev's guarantee covers
only dev-mediated paths. Existing CLI expert acknowledgements remain compatible,
but flow intentionally omits them. Task-backed retirement uses taskflow; explicit
unmanaged path retirement and some record-only/salvage `sweep` actions remain
isolated compatibility paths and must not be described as taskflow-managed.

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

## Task worktree scope

Worktree-mode `dev start --focus -t <name>` creates a linked checkout; `--focus`
only changes focus. Finishing that task preserves its parent/canonical agent
sessions and other tasks' tabs. A recognized parent agent in any state blocks
FF: recheck after handling it independently in Herdr, choose PR, or cancel.
When integration is already proven and only retirement remains, a parent agent
does not prevent cleanup of the separate child worktree.

The interactive finish plan may select exact idle/done **task-worktree** agent
panes for closure. Nothing closes until final Apply approval. Parent, other
checkout, mixed, or unverified Herdr workspaces are protected even when a pane
has changed into the target directory. If they still cover the worktree,
retirement stops and identifies the preserved panes. Working/blocked/waiting
agents remain blockers. General foreground programs (an editor, server, or
test command) require a separate FF confirmation: files in the displayed
checkout will change while those programs continue running. `--yes` does not
supply that confirmation; non-interactive calls requiring it stop.

Interactive `dev done` cleanup and standalone `dev retire` share a preview of
the target, retained resources, workspace/tab/pane IDs, agent states, and
foreground program names, PIDs and directories. Closing known non-agent
programs requires typing `CLOSE <workspace-id>` for each affected workspace,
then approving the final retirement. `--close-unknown` does not authorize
known program termination. Only a proven foreground shell is labeled as such;
unavailable process evidence is shown as unknown, never idle. Herdr provides
foreground evidence; other backends keep their existing explicitly qualified
unknown-runtime policy. Background jobs are **not inspected** and may also stop
when their terminal closes. Raw argv and environment values are not displayed.

Approvals bind exact task/checkout, terminal topology and program fingerprints.
A changed program, new tab or moved pane requires a refreshed preview. The
single-use version-2 coordinator handoff waits for the exact launching dev PID
to exit and verifies the original pane has returned to its foreground shell;
a replacement program is not exempt. Old handoffs must be recreated. Final
cancellation before Apply changes nothing; failures after effects begin list
completed closures and retained resources. Raw Git and Herdr actions remain
outside dev's locks and revalidation guarantees.
