# Local work triage

`dev triage` opens an independent cross-repository interface. Its default view
finds forgotten work; Tab switches to batches grouped by the selected action.

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

Forgotten work sorts uncommitted work before unsynchronized branches and other
remediation, followed by idle candidates. Older observed activity sorts first
within a class. The default idle threshold is 14 days. Age is a sorting signal,
not backup proof or removal permission. The four-cell summary separates work
needing preservation/synchronization from other work, and batch candidates from
items requiring individual review; it does not infer personal importance.

| Key | Action |
|---|---|
| Tab | Forgotten work / quick batches |
| g, / | Filter ordinary repositories/Tries; text filter |
| j/k, arrows | Select a row |
| Space, a, n | Toggle selection; select visible candidates; clear selection |
| f, p, u | Select fetch, push, or fast-forward batches |
| w, c, t | Select Park Warm, Park Cold, or Retire |
| A, x | Available action chooser; Remove Checkout / Trash or forget Try |
| Enter | Build exact plans and show effects and blockers |
| y | Approve a non-removal batch after preview |
| PgUp/PgDn | Scroll the full preview |
| ?, b | Full row details; last batch results |
| o, e, v | Individual flow/Try actions; shell; exact runtime activation |
| L, s, U | Keep local; snooze seven days; clear row intent |
| R | Replace this clone's disposable-directory list; blank clears it |
| d, r, q | Show deferred rows; refresh local facts; quit |

The individual shell starts at the selected path and does not switch branches.
Use existing Git/editor tools there, then exit to refresh the triage view.
Trash can enroll an uncataloged Try during its reviewed Apply. Other catalog
lifecycle operations can use explicit `dev tries list` reconciliation.

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

## Select from REPOS or TRY

In the dashboard, `x` toggles a repository/Try selection and `Ctrl+A` selects
visible items. Filtering keeps hidden selections, with their count shown in the
footer. `Ctrl+O` offers clear selection and a single-item triage entry. With a
selection, Enter opens this independent interface for that scope; without one,
Enter keeps its normal open behavior. `o` always opens the current row and REPOS
Space still expands worktrees. Selecting a repository includes all its local
branches and registered worktrees. `A` lists available actions and candidate
counts; choose an action, adjust the preselected candidates, then Enter to preview.

The handoff reuses accepted dashboard metadata and task/worktree observations.
Only selected repositories receive deeper Git/content inspection; no other roots
are discovered. The first round can reuse the in-memory metadata snapshot; later
refreshes inspect the selected scope again. Returning after an action updates
only affected rows and invalidates their SIZE entries. SIZE retains its existing
10-minute cache. There is no new persistent repository snapshot or watcher:
standalone `dev triage` still collects the full inventory on startup/refresh.
Cached observations never authorize Apply. Local refresh does not fetch.
The dashboard TRY read path observes directories without enrolling them or
reconciling moves; explicit CLI lifecycle commands retain their reconciliation.

## Trash or forget a Try

`A` offers `trash-try` for existing independent Tries and `forget-try` for a
confirmed missing directory. `x` chooses the corresponding Try action, or
Remove Checkout for an ordinary checkout. Unknown activity displays as `—`;
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
