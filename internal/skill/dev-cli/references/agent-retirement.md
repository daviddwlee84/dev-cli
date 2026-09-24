# Agent-safe worktree retirement

Recursive submodule disposal requires explicit `--recursive`. Initialized child
clones need task/artifact/runtime checks and fresh remote recovery proof before
inside-out staging. For linked-worktree removal only, an absent/truly empty child
path with no retained Git store can instead use local empty proof; do not
initialize or download it merely for cleanup. Path-based claims and incomplete
observations still block. Only exact reviewed empty modules admin scaffolding
may be pruned under locks with native directory-only identity/emptiness guards;
empty checkout directories stay for non-force Git removal. Canonical/shared Git
and the outer branch remain. Partial pruning failure is not RETIRED; real-store
journals/rollback remain, including mixed workspaces. Recover retained journals
with `dev git submodule recover`, never delete them using old proof. See `submodules.md`.

## The rule

A feature agent may prepare and integrate work, but it must not destroy the
runtime or checkout containing its own process. Completion has three milestones:

```text
READY     exact transcript preserved in its chosen destination after writer exit
MERGED    branch integrated; runtime/worktree may still exist
RETIRED   runtime absent, worktree removed, optional branch deleted, task reaped
```

`hot/warm/cold/done` remain the persisted task states. `done` now means MERGED,
not "everything has already been deleted".

## Normal local flow

Default product-first preparation requires committed product changes and an empty
index. From the feature worktree:

```bash
# Commit product changes first. Do not stage the moving transcript.
dev agent artifact prepare --session claude:<uuid> --plan .claude/plans/task.md
# Exit the agent normally so SpecStory can write its final Markdown.
```

The outer `specstory run` wrapper calls:

```bash
dev agent artifact finalize --run-id "$DEV_AGENT_RUN_ID" --if-pending --writer-stopped
```

Then an external main/integration workspace runs:

```bash
dev work done <task> --ff
dev work retire <task> --delete-branch
```

`done` only integrates and records MERGED. `retire` re-resolves every runtime
pane, closes eligible sessions, waits for them to disappear, revalidates Git,
and removes the worktree without force.

`dev repo flow [repo]` offers Retire only for an exact DONE task. Enter first shows a
revision-bound plan with conditions, ordered effects, retained resources, and a
CLI fallback; branch deletion needs the displayed typed token. Apply locks and
reloads task/repository/worktree/ref/runtime/artifact authority, repeats safety
checks after closure and before removal, and deletes the task last. If a later
effect fails, its ledger reports completed work and recovery without claiming a
rollback.

For periodic cleanup from the canonical main checkout:

```bash
dev work sweep --merged-worktrees
# Present the exact candidates/blockers to the user and ask for approval.
dev work sweep --merged-worktrees --apply --yes
```

This includes unmanaged linked worktrees whose named branches are already
contained in main. It never treats containment alone as permission: dirty Git,
pending artifacts and runtime blockers still stop cleanup. Branches remain by
default; add `--delete-branches` only when the user approved that separately.

`dev work sweep --base <ref>` also forwards the selected base to DONE task retirement;
`--merged-worktrees` uses its verified base for managed tasks as well as unmanaged
checkouts. Reviewed retire/remove plans bind worktree-list authority only to the
same branch or paths equal to, containing, or nested under the target. Removing
an unrelated sibling no longer invalidates the rest of an approved
`--apply --yes` batch. A same-branch checkout or target lock/HEAD change still
makes the plan stale; containment and all other guards remain required.

Claude Workflow ephemeral worktrees use a separate, stricter path:

```bash
dev work sweep --ephemeral-worktrees --stale-days 14
dev work sweep --ephemeral-worktrees --json
dev work sweep --ephemeral-worktrees --apply
dev work sweep --ephemeral-worktrees --apply --delete-branches --base main
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

## Exact transcript and co-commit handoffs (v0.2.40)

`prepare --specstory-path PATH` pins one provider/UUID/capture-path selection;
otherwise ambiguous exports still block. Finalization rechecks source/writer
identity and exact native intent revisions under locks. An optional
`artifact finalize --revision HASH` binds the reviewed revision.

Co-commit instead queues a reviewed feature-only index, an exact `claude:UUID`
transcript, one `--plan` or `--no-plan`, and a base `--message-file`. Read
[AI artifacts](ai-artifacts.md) first: the separately installed canonical v2
wrapper must be real and launched **without `--allow-commit`**. Dev neither
launches nor closes agents. Retain the real `queue_ack` output in the recorder;
report "finalization queued" and exit without further repository/index work,
even for `queued_but_not_bound`. Repair only that exact binding with `--run-id`,
never generate another request or fabricate an ACK.

An external `artifact finalize --intent ID --allow-commit` performs canonical
prepare-only, fresh native Guard/CAS checks and one exact-revision commit-capable
helper call. Unknown outcomes reconcile only. `--preview-review --json` is
read-only and cannot apply findings or approve a commit/rotation. Noncredential
review never restores secret bytes or bypasses hooks. `artifact list --json`
(schema 1, `artifact_handoffs`) and readiness keep persisted native intent separate
from helper observations, without reconciling. Complete parent/tree/full-message/
request proof and receipt reachability still precede integration or retirement;
queued or runtime-done is not READY. Native Windows co-commit remains unsupported.

## Choose the containment base

`dev work retire --base <ref>` overrides the containment target for a DONE task or
linked worktree without changing recorded task intent. Resolution checks a local
branch (`refs/heads/X`), then a remote-tracking branch (`refs/remotes/X`, such as
`origin/main`), then a commit. Fully qualified branch refs keep their named kind.
Apply re-resolves the same input and requires the same kind, ref and commit OID;
a moved base or a newly created same-name branch makes the reviewed plan stale.
Remote-tracking refs are local observations, not an implicit fetch.

Without an override, task retirement keeps its recorded base. A recorded
fork-point commit is never silently replaced by the default branch: if it cannot
prove integration, retirement stays blocked with `pass --base <branch>` guidance.
After `dev work done --merged --base-ref X`, cleanup hints, the cleanup wizard and the
external coordinator carry X forward as `dev work retire --base X <task>`. If a shell
handoff cannot carry the override, dev prints the external command instead.

Optional `--delete-branch` still runs ordinary `git branch -d`, whose own merged
check uses the branch's upstream or HEAD, not `--base`. Against a non-HEAD base,
branch deletion can therefore fail after the worktree was removed; the branch
and DONE task remain, and the result reports partial completion.

## Pull-request flow

```bash
dev work done <task> --pr
# after CI/review and a commit-preserving merge
git fetch origin
dev work done <task> --merged --base-ref origin/main
dev work retire --base origin/main <task> --delete-branch
```

A squash merge is not ancestry-equivalent. It requires an explicit operator
attestation:

```bash
dev work done <task> --merged --base-ref origin/main --confirm-squash <merge-commit>
```

This proves only that the named squash commit is in the base; the operator is
asserting that it represents the feature. Equal trees after a squash are not
ancestry proof and do not waive retirement's containment checks.

## Safety policy

Retirement always refuses when:

- the caller cwd is the target or below it;
- the caller Herdr workspace/pane or tmux pane covers the target;
- a workspace contains panes outside the target;
- an agent is `working`, `blocked`, or `waiting`;
- runtime enumeration fails;
- the checkout is dirty, on the wrong branch, or not contained in its base;
- relevant artifact proof is pending, failed, unknown or no longer reachable.

`unknown` or empty agent status requires `--close-unknown` from outside the
target. Runtime enumeration failure requires external `--assume-no-runtime`.
Neither flag bypasses caller containment or active-agent states.

Raw `git worktree remove --force` and configured external tools can bypass dev.
Never run them from an agent that occupies the target. dev's guarantee covers
only dev-mediated paths. Existing CLI expert acknowledgements remain compatible,
but flow intentionally omits them. Task-backed and exact unmanaged path
retirement use taskflow, with contained removal required for the latter. Some
record-only/salvage `sweep` actions remain isolated compatibility paths.

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

## Product-first artifact rules

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

Worktree-mode `dev work start --focus -t <name>` creates a linked checkout; `--focus`
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

Interactive `dev work done` cleanup and standalone `dev work retire` share a preview of
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

## External archive policy

`dev agent artifact setup` can select a separate Git archive. New prepared intents bind
that destination and policy; old intents keep the commit workflow. Off/check
finalization needs no compatibility redactor script. For redact copies, review
`dev agent artifact archive` and supply `--archive-plan` when finalizing. Commit reviewed
plans with product changes before preparing one exact SpecStory transcript.
Ignored capture bytes participate in lifecycle readiness; a new recorder write or
missing archive receipt blocks cleanup. See [AI artifacts](ai-artifacts.md).
