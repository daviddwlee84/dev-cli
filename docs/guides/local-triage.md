---
description: Find forgotten local work across ordinary repositories and Tries, review synchronization batches, and preserve ignored data during checkout cleanup.
authority: project
status: evolving
verified_on: 2026-09-09
---

# Local work triage

`dev triage` opens an independent cross-repository interface. Its default view
groups forgotten work by repository/Try before action selection.

```bash
dev triage
dev triage --report
dev triage --json
dev triage --kind try
dev triage --root ~/other-projects --stale-days 30
dev triage --all                 # include retained catalog history
```

Redirected output is a read-only text report. `--json` returns schema version 1
with source completeness, exact local identities, findings, action candidates,
runtime links, and recorded triage intent. Existing `dev ls --json` is unchanged.

## What is discovered

The scan combines configured discovery roots, additional roots, the current
checkout, local catalog and task paths, and paths observed in supported available
runtimes. Every registered worktree and local branch is inspected, including
branches that are not checked out. Common Git directories deduplicate physical
repositories; cataloged Try checkouts retain their own identity. Immediate Try
directories are observed without automatically enrolling them in the catalog.

Ordinary repositories and Tries can be filtered separately. Active and
deprecated local Tries appear by default; `--all` includes retained history.
Git Tries can participate in synchronization. Non-Git Tries remain visible for
individual preservation, archive, graduation, or disposal.

Findings retain uncommitted changes, missing upstream configuration, unavailable
upstream refs, ahead/behind/divergence, stash/tags/notes, ignored paths, task drift,
and runtime occupancy. A missing upstream does not prove that commits have never
been published. Runtime errors and incomplete observations are visible rather
than interpreted as clean or closed. `--no-runtime` disables multiplexer probes
and leaves checkout mutation blocked where runtime evidence is required.

Startup and `r` do not fetch, query a forge or fleet, refresh release information,
write task/catalog records, or start runtime sessions. Remote comparisons use
locally cached tracking refs. An explicit fetch is a separate approved action.

## Views and keys

The independent organizer starts at **Select items**. It shows one summary row
per repository/Try, with short badges for unsaved work, synchronization, occupancy
and incomplete observations. Expand a row to inspect branches and checkouts.
Selecting its checkbox includes all underlying local work; child checkboxes can
refine the selection. A partial parent uses `[-]`. Color supplements symbols and
labels; `--color never` and `TERM=dumb` retain understandable output.

| Input | Action |
|---|---|
| Space / checkbox click | Toggle the focused item or group |
| Ctrl+A (or a) | Select all filtered targets; when all are selected, cancel them; hidden selection stays |
| Left/right / disclosure click | Collapse or expand a repository |
| Enter / row click | Enter shows details; a row click only focuses |
| Ctrl+O / A / Choose action button | Choose an action for the explicitly labelled current item or selection |
| Tab | Focus the primary action button |
| /, g, d | Text filter; repo/Try filter; show deferred work |
| n | Clear selection |
| o, e, v | Individual flow; shell; runtime |
| L, s, U, R | Keep local; snooze; clear intent; disposable-directory policy |
| r, b, ?, q | Local refresh; last results; complete help; return |

Right-click a row opens its action scope. The wheel scrolls the active list or
details. Action selection leads to an exact preview; only explicit approval
executes it. The four stages are **Select → Action → Preview → Results**.
Results show counts and short per-target reasons. Enter expands diagnostic,
remediation, exact effect and receipt information. `e` opens a shell for the
result's target; `Ctrl+O` chooses a new action and always builds a fresh plan.
Old receipts without diagnostics explicitly say that the reason was not captured.

## Approved batches

Each round chooses one action: select, preview, approve, execute, refresh. After
fetching, select a new push, fast-forward, or cleanup round. Newly eligible rows
do not inherit an old approval. Candidate rows still need fresh guarded plans;
a candidate label is not a READY result.

- Fetch updates the selected remote-tracking branch namespace, including pruning
  deleted remote branches, without fetching tags or recursing into submodules.
- Push sends the exact previewed commit and branch to one unambiguous endpoint.
  It never force-pushes or includes extra tags/branches. Triangular or multiple
  destinations, first publication, and divergent branches require individual
  review. Uncommitted changes remain visible after a successful push.
- Fast-forward requires an exact clean checked-out branch and runtime evidence.
  It uses `--ff-only --no-overwrite-ignore`; an ignored-file collision aborts.
  It does not switch branches, stash changes, or rebase.
- Task actions retain the existing mode/state graph. Cleanup preserves branches
  and Git common directories. Whole-repository eviction is not provided.

Plans lock and revalidate repository, refs, checkout, task revision, runtime and
relevant content evidence before effects. Cleanup also checks contents and the
disposable policy immediately before removing a checkout. Active agents,
including idle/done agents, are not closed by triage batches. Canonical,
harness-owned, conflicted, locked/prunable or incompletely observed checkouts
cannot be removed through this interface.

Checkout removal batches require the displayed `CLEAN N` token in addition to reviewing
the exact paths and effects. Independent items continue after an item fails;
same-repository operations are serialized. Esc during Apply stops the remaining
queue after the current operation returns; quitting waits for its ledger.
Receipts under `<state_dir>/triage/runs/` retain completed, skipped, canceled,
failed, stale and partial outcomes. An interrupted `running` entry is unknown
and requires fresh inspection, not an assumed rollback or automatic replay.

## Intent and ignored files

Triage intent is durable under `<state_dir>/triage/`; observed Git state is not
stored as truth. Keep-local and snooze bind to the observed item fingerprint.
New commits, edits or findings bring the item back. Serious observation errors
remain visible. Deferral never authorizes cleanup.

Disposable directories default to an empty list. `R` accepts exact
checkout-relative directories such as `node_modules` or `.venv`, separated by
commas. This is an explicit decision that their ignored contents may be lost
during an approved linked-checkout removal. It applies only to this clone and
its worktrees. It does not follow a replacement clone at the same path.

For linked-checkout cleanup, all other ignored files remain blockers. Previews list the affected ignored paths
and sizes. Traversal, globs, Git administrative paths, symlink roots, nested repos
and submodules cannot be authorized by a disposable-directory rule. Symlink
targets outside the checkout are never traversed. Platforms without stable
filesystem identity cannot enroll such policies or perform protected cleanup.

Verified whole-repository backup/restore, external-reference analysis, permanent
repo deletion, and automatic commit/rebase remain separate work. Being synced
on the current branch is insufficient evidence to remove a whole clone.

## Enter from REPOS or TRY

The dashboard remains navigation-only: Enter/`o` opens the current item and
Space retains its existing worktree/action behavior. There are no organizer
checkboxes or hidden selections on these pages. `Ctrl+O` offers **organize this
item**, **organize current filtered results**, and **organize all local work**.
The triage header names the chosen scope; scoped entry starts with items, not
an action chooser.

The handoff still reuses dashboard metadata and task/worktree observations.
Only scoped repositories receive deeper inspection, and returning refreshes only
affected rows/SIZE entries. The existing 10-minute SIZE cache remains; no new
persistent inventory cache or watcher is added. Standalone `dev triage` still
collects the full inventory. Refresh never fetches or enrolls Tries.

## Trash or forget a Try

`A` offers `trash-try` for existing independent Tries and `forget-try` for a
confirmed missing directory. The action chooser labels each operation explicitly. Unknown activity displays as `—`;
`missing` and `unavailable` describe separate presence observations. An access
failure or an unavailable root cannot authorize forgetting.

Trash batches require the displayed `TRASH N` token and preserve the **whole**
directory, including ignored and untracked contents. Trash still occupies disk
space until emptied. Task/runtime claims, shared or external Git storage, unsafe
paths, and changed source identity/content block removal. A missing Trash backend
never falls back to permanent deletion. For an uncataloged Try, the preview
explicitly includes selected-only registration during Apply. Cancellation writes
nothing; if registration succeeds and Trash fails, the record and remaining
contents are retained and the result reports partial progress. Other host
locations and ordinary Trash recovery history remain intact.

A missing accidental Try can be completely forgotten with a `FORGET N` batch,
or through the same guarded service:

```bash
dev tries forget <ref> --dry-run --json
dev tries forget <id> --confirm-forget <id> --json
```

Forgetting requires one local location, an accessible expected parent, confirmed
absence, and no task, runtime, agent-artifact, note-source, other catalog or
recovery reference. Personal catalog notes/tags must be reviewed and cleared
first. A second host location blocks forgetting even when its path text matches.
Apply locks and rereads the exact catalog record; a reappeared directory or
changed authority rejects the plan. It removes only that catalog record and
writes an audit result; it never deletes project files, note Markdown, or stats.
Permanent batch deletion and ordinary canonical-repository deletion are excluded.
