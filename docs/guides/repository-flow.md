---
description: Use the preview dev flow interface to inspect one repository and apply revision-bound lifecycle, adoption, removal, retirement, and remote-evidence plans.
authority: project
status: evolving
verified_on: 2026-09-01
---

# Repository lifecycle flow

`dev flow [repo]` is a preview-labelled, full-screen, TTY-only interface for one
repository's lifecycle. It is independent of the six-view `dev tui` dashboard:
its only job is to show repository surfaces, observed evidence, and exact guarded
actions.

```bash
dev flow              # current repository, or a picker outside Git
dev flow api          # explicit repository overrides cwd
```

From either the canonical checkout or a linked worktree, bare `dev flow` resolves
the canonical Git common-directory identity and focuses the exact current
surface. Outside Git it loads a filterable repository picker asynchronously. A
repository that is referenced by task metadata but temporarily unavailable
remains inspectable as unavailable task-only rows.

## Repository surfaces

The left panel starts from every record in `git worktree list --porcelain`, then
adds task records that have no checkout. Normal COLD and DONE tasks therefore do
not disappear merely because no directory exists.

| Row kind | Meaning | Guarded actions |
|---|---|---|
| `canonical` | the worktree Git marks Main, regardless of branch name | legal task actions when exactly bound; never checkout removal |
| `managed` | a non-main checkout with one exact task binding | mode/state lifecycle actions |
| `unmanaged` | a registered linked checkout with no task or harness claim | Adopt; clean branch-preserving Remove Checkout |
| `harness` | strict path evidence places it under `.claude/worktrees` | inspect and eligible remote evidence only; harness owns cleanup |
| `task-only` | task metadata has no registered checkout, normally COLD or DONE | Resume or Retire only when exact task identity permits it |
| `conflict` | duplicate claims, mismatched identity, incomplete inventory, or other ambiguity | inspect/remediate; destructive actions fail closed |

A branch name prefix is not harness ownership proof. Canonical, harness-owned,
locked/prunable, detached, ambiguously claimed, or incompletely observed rows are
never treated as removable merely because they look inactive.

For an eligible unmanaged row:

- **Adopt** creates one worktree-mode task record and does not change checkout
  content, refs, runtime layout, or path. A dirty checkout may be adopted because
  its bytes remain untouched. Fresh strict runtime evidence may derive HOT only
  for one stable shell-only covering session with no recognized agent; otherwise
  adoption is WARM. `runtime=none` is unobserved, so it derives WARM.
- **Remove Checkout** requires an exact named clean linked checkout, complete task
  inventory with no claims, ready artifacts, safe external runtime cleanup, and
  no harness evidence. It uses non-force removal and verifies that the local
  branch remains at the same OID.

Repository-wide prune and drift reconciliation are not hidden Remove variants.
The flow reports those conditions and leaves them for explicit recovery.

## Intent, facts, and plans

The three panels keep different kinds of truth separate:

1. **Persisted intent** — task mode, HOT/WARM/COLD/DONE, owner, next action,
   branch/base, expected checkout, and runtime hint.
2. **Observed facts** — repository/worktree identity, HEAD and refs, Git status and
   in-progress operations, artifact readiness, runtime sessions, and recognized
   agents. A fact remains known, unknown, error, skipped, loading, or stale; a
   failed observation never becomes false, clean, or closed.
3. **Guarded plan** — legal source/target edge, READY/NEEDS INPUT/BLOCKED/UNKNOWN/
   ERROR availability, ordered conditions and effects, remediation, resources
   retained, confirmation class, PlanID, and CLI fallback.

`runtime=none` cannot observe session or agent occupancy. That does not turn the
observation into “closed,” but a complete local Git/task snapshot can still be
fresh and useful for non-runtime facts.

## Managed lifecycle choices

Actions are concrete variants, not a generic force menu:

| Current intent | Flow choices |
|---|---|
| HOT/WARM worktree or branch task | Park Warm; Park Cold; Park Cold + Push; Complete FF; Review Handoff; Verify Merged |
| HOT/WARM direct task | Park Warm; Complete Direct |
| WARM task | Resume (with an explicit fetch effect) |
| COLD worktree/branch task | Resume and reconstruct/reopen; completion remains unavailable until HOT |
| DONE task | Retire (Keep Branch); for eligible non-direct tasks, Retire + Delete Contained Branch |

Review Handoff publishes the branch and creates a pull/merge request when the
available provider supports it, but preserves HOT/WARM. Direct/FF/verified
completion writes DONE last and keeps branch, checkout, and runtime resources.
Retire is the separate externally safe close/wait/remove/task-reap operation.

The preview always uses a fail-on-dirty completion policy. It intentionally does
not expose dirty commit/discard, WIP checkpoint, shared-writer, ownership
takeover, force removal, `--close-unknown`, or `--assume-no-runtime` choices.
Blocked plans show the precise evidence, remediation, and compatible CLI
fallback instead of manufacturing an override.

## Keys and approval

```text
j/k or up/down       select a surface or picker/menu row
h/l or left/right    select an already-concrete action
Tab / Shift-Tab      move panel focus
/                    filter the repository picker
Enter                build and inspect a plan; never apply immediately
y                    approve a READY non-typed plan
r                    reload local facts only
R                    choose Fetch Refs, Query Review, or Both
?                    show evidence and key semantics
Esc                  back out
q                    quit; while Apply runs, queue quit until its ledger returns
```

Typed retirement displays the exact `DELETE <branch>` token; type it and press
Enter. A mismatch remains non-mutating. Plans that are not READY have no Apply
path and remain open for inspection.

## Local and manual remote evidence

Startup and `r` do not fetch or contact a forge. Network work happens only in a
confirmed action that declares it: Resume fetch, Park Cold + Push, Review
Handoff, or one of the `R` choices.

`R` supplies three independent plan variants:

- **Fetch Refs** runs `git fetch --prune` for the exact configured remote.
- **Query Review** asks the supported GitHub, GitLab, or Azure CLI for the exact
  head/base relationship without fetching.
- **Both** fetches first, then queries; if fetch fails, review is not attempted.

Remote results live only for the current flow run. Ref evidence contains exact
named-ref OIDs or explicit absence/error. Portable review evidence contains only
existence, provider state (open/draft/merged/closed), draft flag, URL, provider,
and observation time. Unsupported providers, missing CLIs/extensions,
authentication/provider failures, malformed or multiple matches, and
not-yet-requested evidence stay UNKNOWN or ERROR. The flow does not query or
infer review decisions, approvals, comments, or checks.

## Plan and Apply revalidation

Planning is side-effect-free. A plan seals the task record revision and exact
repository, Git common directory, checkout path/branch/HEAD, refs, status,
runtime/agent occupancy, artifacts, remote identity, concrete options, and
ordered effects into an authority fingerprint and PlanID.

Apply accepts only a plan produced by the current flow run and an approval bound
to that PlanID. It locks repository then task state, reloads the task revision,
re-observes authority, and rejects any difference before a new effect. Safety-
critical checks repeat after runtime closure and immediately before checkout or
branch removal. Task state is written last; retirement deletes the DONE record
last.

Apply is not canceled by refresh, row switching, or quit. Conflicting navigation
is disabled, and quit/refresh waits for the result. The result ledger preserves
every attempted/completed/failed effect plus warnings and recovery. Partial
success means completed effects remain completed; no rollback is implied. A new
local generation loads afterward, and directory/runtime/URL handoff occurs only
after the alternate screen exits.

## Compatibility boundary

The same `internal/taskflow` authority backs normal task-managed park, resume,
completion, and retirement commands plus exact unmanaged linked-checkout Adopt
and Remove paths. Existing CLI flags and structured output remain compatible,
including expert acknowledgements that the preview does not offer.

This is not a claim that every historical cleanup path has migrated. Explicit
unmanaged path retirement remains an isolated compatibility implementation, and
`sweep` retains record-only, orphan-salvage, and other narrowly scoped
reconciliation paths outside taskflow. `sweep` still reports before applying.

Raw Git and configured external TUI tools do not participate in dev's task locks,
PlanID approval, identity revalidation, or result ledger. They remain available,
but they are outside dev-mediated safety guarantees.

## Sources

- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/flowtui`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/flowtui)
- [`internal/taskflow`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/taskflow)
- [`internal/inventory/repo_context.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/inventory/repo_context.go)
- [`internal/task/store.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/task/store.go)
- [`internal/forge/review.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/review.go)
