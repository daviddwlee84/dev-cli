# Agent-safe retirement

A process must not delete the worktree that contains its own current directory.
The terminal can remain visible after Unix unlinks the path, but config reloads,
relative files and new subprocesses then fail. Herdr still showing a pane does
not mean the Git checkout is healthy.

Use three separate milestones:

1. **READY** — by default, commit product work and leave an empty index, run
   `dev agent artifact prepare`, then exit normally so the post-SpecStory finalizer can preserve
   the exact final transcript in its selected source-commit or external-archive
   destination. A manual product-first finalizer must pass `--writer-stopped`;
   a Claude SessionEnd observer can provide the same durable proof without
   staging during teardown. Explicit co-commit has separate proof below.
2. **MERGED** — `dev work done --ff`, or external-merge verification with
   `dev work done --merged --base-ref <ref>`, records DONE. Explicit and
   non-interactive completion keeps worktree/branch; selected interactive task
   pane closures are reported separately. Bare interactive
   completion may continue into a separately confirmed cleanup preview.
3. **RETIRED** — from outside the target workspace run `dev work retire`. It closes
   eligible runtime sessions, waits until they disappear, revalidates Git, and
   removes the worktree without force.

`dev repo flow [repo]` exposes Retire only for an exact DONE task. It first shows
conditions, ordered effects, resources retained, and a fallback command; branch
deletion is a separate typed option. Apply reloads the exact task revision and
Git/worktree/ref/runtime/artifact identity under locks, repeats safety checks
after runtime closure and before removal, and deletes the task record last.
Completed steps remain visible if a later step fails; no rollback is implied.

## Recursive submodule removal

`--recursive` is required even when every gitlink is empty. For linked-worktree
removal only, a child path that is absent or truly empty with no retained Git
store may use local empty proof; no initialization, download, remote proof or
push is needed merely for removal. Deinitialized retained data, orphan stores,
local/ignored files, unexpected `.git`, symlinks/reparse points, external claims
and incomplete observations block. Empty-child task/artifact claims use paths,
not parent-inherited Git discovery; any non-discarded matching artifact intent
blocks, even finalized, and discarded records still bind the reviewed authority.

Initialized child clones retain full remote recovery requirements and inside-out
journaled staging/rollback, including mixed workspaces. Only exact reviewed empty
modules admin directories may be pruned under locks after immediate native
directory identity/emptiness revalidation. Empty checkout directories stay for
ordinary non-force `git worktree remove`; no recursive file-deletion workaround
is used. Layout changes, new initialization or claims invalidate plans. Partial
admin pruning failure is reported, never task-retired success. Canonical/shared
Git and the outer branch remain; initialization/publication policy is unchanged.
`objects/info/alternates` remains an intentional blocker.

## Explicit co-commit closeout (v0.2.40)

`prepare --specstory-path PATH` pins the exact provider/UUID/capture-path selection;
ambiguity is never resolved by newest mtime. The separate `--closeout co-commit`
lane requires a real canonical v2 wrapper launched **without `--allow-commit`**,
a reviewed feature-only index, exact `claude:UUID` transcript, one `--plan` or
`--no-plan`, and a base `--message-file`. Dev never launches or closes the agent.
Preserve the actual queue ACK in the recorder, report "finalization queued", and
exit without further repository operations, even for `queued_but_not_bound`.
`--run-id` only repairs that exact existing binding; it creates no request or ACK.

After the wrapper finishes, an external `artifact finalize --intent ID
--allow-commit` delegates prepare-only, then rechecks native writer/policy and
CAS guards before one commit-capable call with the exact helper journal revision.
`--writer-stopped` alone does not supply co-commit lifecycle proof. Unknown commit
outcomes reconcile only, never retry. Read-only `artifact list --json` and
readiness keep persisted intent separate from helper observations and never
reconcile records. Retirement still requires complete, reachable artifact proof
and all its ordinary Git/runtime guards. See `dev help ai-artifacts` for canonical
v2 source maintained separately in `agent-skills`, compatible installed-helper
requirements (never bundled or auto-installed), read-only `--preview-review --json`,
private review/rotation requirements and native Windows limits.

## Choose the containment base

`dev work retire --base <ref>` overrides the containment target for a DONE task or
linked worktree without changing recorded task intent. Resolution checks a local
branch (`refs/heads/X`), then a remote-tracking branch (`refs/remotes/X`, such as
`origin/main`), then a commit. Fully qualified branch refs keep their named kind.
Apply re-resolves the same input and requires the same kind, ref and commit OID;
a moved base or a newly created same-name branch makes the reviewed plan stale.
Remote-tracking refs are local observations, not an implicit fetch. Equal trees
after a squash are not ancestry proof and do not waive containment checks.

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

Retirement never overrides `working`, `blocked`, or `waiting` agents. Unknown
status needs external `--close-unknown`. A workspace containing panes outside
the target is mixed-purpose and must be reorganized or closed manually.

The bare interactive `dev work done` cleanup preview shows those workspace panes and
agent states before offering keep, retire, or retire plus branch deletion. If
the target is the caller's Herdr workspace, dev creates a fresh exact-root-pane
workspace in the canonical checkout and hands a short-lived, single-use intent
to that external coordinator. It revalidates task, checkout HEAD, runtime
fingerprint, and the full ordinary retirement plan before closing anything.

Before MERGED, parent/canonical agents are preserved and block FF: recheck,
choose PR, or cancel. Exact idle/done task-worktree panes can be selected for
closure, but only final Apply closes them. Other active agents remain blockers.
General foreground programs require independent FF file-change consent while
they keep running. Interactive done/retire cleanup lists workspace/tab/pane IDs
and program names/PIDs/directories. Known programs require CLOSE <workspace-id>
and final retirement approval; --close-unknown does not grant this consent.
A foreground shell is not proof of no background jobs: those are not inspected.
Parent/mixed workspaces remain open. Cancellation before Apply closes nothing;
partial failures list completed closures. Old coordinator handoffs must be
recreated; version 2 verifies the launching PID has returned to a shell. If the canonical
integration checkout is dirty, dev lists its paths and offers PR handoff,
typed-`DROP` guarded discard, or cancel. It never silently commits unrelated
canonical bytes.

Raw `git worktree remove --force` and configured external tools bypass these
protections. Do not use them on an agent-owned checkout. Existing expert CLI
acknowledgements remain available, but the flow preview deliberately omits dirty
discard, shared-writer/takeover, and unknown-runtime overrides.

Task-backed `dev work retire` and exact unmanaged path retirement use taskflow
(the latter requires contained removal). Some record-only/orphan-salvage `sweep`
reconciliation paths remain separate; not every cleanup uses the same planner.

From the canonical main checkout, `dev work sweep --merged-worktrees` reports both
tracked and unmanaged linked worktrees whose named branches are contained in
main. Review the exact candidates and blockers first; apply only after user
confirmation. Worktree retirement keeps branches unless `--delete-branches`
was separately requested.

`dev work sweep --base <ref>` also forwards the selected base to DONE task retirement;
`--merged-worktrees` uses its verified base for managed tasks as well as unmanaged
checkouts. Reviewed retire/remove plans bind worktree-list authority only to the
same branch or paths equal to, containing, or nested under the target. Removing
an unrelated sibling no longer invalidates the rest of an approved
`--apply --yes` batch. A same-branch checkout or target lock/HEAD change still
makes the plan stale; containment and all other guards remain required.

Claude Workflow turn-scoped worktrees have a separate strict V1 audit:

```bash
dev work sweep --ephemeral-worktrees [--stale-days 14] [--json]
dev work sweep --ephemeral-worktrees --apply
dev work sweep --ephemeral-worktrees --apply --delete-branches --base main
```

Run it only from the canonical non-bare checkout. The report verifies one exact
bounded mapping under `~/.claude/projects`, terminal workflow plus done agent,
matching journal start/result, no same-ID resumed transcript, provider inactivity,
and every live Git/task/artifact/runtime/caller fact. `killed` without a child
result, progress, resume, missing/prunable/unregistered/orphan paths, dirty or
ignored content, and unknown evidence are never apply-eligible. JSON is
schema-version-1 report-only; apply rejects `--yes`, `--close-unknown`,
`--assume-no-runtime`, and `--no-runtime`, requires a TTY, and confirms each
candidate.

Provider ownership also needs provider-observed branch, HEAD, common-dir, and an
opaque non-replayable registration generation matching live state. Claude Code
2.1.259 does not record that identity, so its claims report
`provider-git-identity: unknown` and stay report-only even under `--apply`. Never
replace this proof with a reusable path, branch convention, or GitDir pathname.

Apply locks the Git common directory, recollects all proof, and requires the same
candidate fingerprint before plain non-force removal. It never closes a runtime,
prunes registrations, changes Claude metadata, or rescues/stashes/commits dirty
work. The named branch survives by default, so unique commits remain recoverable.
Deleting it is separate: `--delete-branches` requires explicit `--base`, unchanged
tips, containment, zero unique commits, and ordinary `git branch -d`.

For history retention, reviewed knowledge and source-package boundaries, see
`dev help ai-artifacts`.
