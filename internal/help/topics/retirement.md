# Agent-safe retirement

A process must not delete the worktree that contains its own current directory.
The terminal can remain visible after Unix unlinks the path, but config reloads,
relative files and new subprocesses then fail. Herdr still showing a pane does
not mean the Git checkout is healthy.

Use three separate milestones:

1. **READY** — commit product work, run `dev prepare`, then exit normally so the
   post-SpecStory finalizer can commit the exact final transcript. A manual
   external finalizer must pass `--writer-stopped`; a Claude SessionEnd observer
   can provide the same durable proof without staging during teardown.
2. **MERGED** — an external coordinator runs `dev done --ff`, or verifies an
   external merge with `dev done --merged --base-ref <ref>`. Runtime and
   worktree remain intact.
3. **RETIRED** — from outside the target workspace run `dev retire`. It closes
   eligible runtime sessions, waits until they disappear, revalidates Git, and
   removes the worktree without force.

`dev flow [repo]` exposes Retire only for an exact DONE task. It first shows
conditions, ordered effects, resources retained, and a fallback command; branch
deletion is a separate typed option. Apply reloads the exact task revision and
Git/worktree/ref/runtime/artifact identity under locks, repeats safety checks
after runtime closure and before removal, and deletes the task record last.
Completed steps remain visible if a later step fails; no rollback is implied.

Retirement never overrides `working`, `blocked`, or `waiting` agents. Unknown
status needs external `--close-unknown`. A workspace containing panes outside
the target is mixed-purpose and must be reorganized or closed manually.

Raw `git worktree remove --force` and configured external tools bypass these
protections. Do not use them on an agent-owned checkout. Existing expert CLI
acknowledgements remain available, but the flow preview deliberately omits dirty
discard, shared-writer/takeover, and unknown-runtime overrides.

Task-backed `dev retire` uses the guarded lifecycle service. Explicit unmanaged
path retirement retains an isolated compatibility implementation; do not assume
every cleanup or `sweep` reconciliation path has moved to the same planner.

From the canonical main checkout, `dev sweep --merged-worktrees` reports both
tracked and unmanaged linked worktrees whose named branches are contained in
main. Review the exact candidates and blockers first; apply only after user
confirmation. Worktree retirement keeps branches unless `--delete-branches`
was separately requested.

Claude Workflow turn-scoped worktrees have a separate strict V1 audit:

```bash
dev sweep --ephemeral-worktrees [--stale-days 14] [--json]
dev sweep --ephemeral-worktrees --apply
dev sweep --ephemeral-worktrees --apply --delete-branches --base main
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
