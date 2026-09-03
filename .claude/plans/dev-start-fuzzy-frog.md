# Context

`dev` already presents tasks as `HOT / WARM / COLD / DONE`, but the legal edges and their preconditions are distributed across `park`, `resume`, `done`, `retire`, `adopt`, `wt rm`, `sweep`, and a separate dashboard Park callback. Completion filters suggest the intended graph, yet explicitly named tasks can currently traverse undocumented paths such as `DONE -> HOT` or `DONE -> WARM`. At the same time, checkout dirtiness, publication, worktree registration, runtime/agent occupancy, artifact readiness, merge evidence, and PR observability are not lifecycle states; they are live facts with independent freshness and failure modes.

Build an experimental, independent `dev flow [repo]` TUI that makes this model visible and actionable. The selected product shape is:

- context-aware repository startup;
- every registered worktree plus managed tasks with no checkout;
- full guarded apply for the normal managed lifecycle;
- separate Adopt / Remove Checkout actions for unmanaged worktrees;
- no coupling to the existing six-view dashboard model;
- local-only startup and `r` refresh, with remote work only after an explicit action;
- `HOT / WARM / COLD / DONE` remain the only persisted task states.

The implementation must first advance this no-ahead feature branch from v0.2.2 to the v0.2.3 `main` baseline, preserving the existing `.specstory` working-tree files. The v0.2.3 generation/cancellation/readiness infrastructure should be reused rather than reimplemented.

## Product and interaction model

### Hybrid state machine

Model three layers explicitly:

1. **Recorded intent** — task mode, `HOT / WARM / COLD / DONE`, owner, next action, branch/base, and expected checkout.
2. **Observed facts** — checkout identity/registration, dirty/conflicted/Git-operation state, upstream and ahead/behind with freshness, local containment in an explicit base, runtime sessions, recognized agents, artifact readiness, and remote/review capability. Every fact is `known`, `unknown`, `error`, `skipped`, or `loading`; failed probes must never become false/clean/closed.
3. **Guarded actions** — legal source/target edge, required inputs, conditions, blockers/remediation, ordered effects, retained/removed resources, confirmation class, and exact fallback CLI command.

`READY` is action readiness, `REVIEW` is a state-preserving handoff/result, `MERGED` is DONE plus named ancestry evidence, and `RETIRED` is successful task removal. They are overlays/milestones, not new TOML states.

Encode one mode-aware transition table and enforce it in the shared service and every CLI/TUI caller:

- worktree/branch: `HOT|WARM -> WARM` (park warm), `HOT|WARM -> COLD` (park cold), `WARM|COLD -> HOT` (resume), `HOT|WARM -> DONE` (directly integrated or verified merged), `DONE -> RETIRED`;
- direct: `HOT|WARM -> WARM` (warm park), `WARM -> HOT` (resume), `HOT|WARM -> DONE` (direct completion), `DONE -> RETIRED`; direct never becomes COLD;
- review handoff from HOT/WARM pushes/opens review but preserves the current state;
- COLD must resume before completion; DONE cannot park/resume/complete;
- reconciliation is a named action, not an undocumented backward transition.

### Repository rows

Identify repositories by canonical Git common directory and checkouts by canonical registered path. The repository screen is the exact union of authoritative `git worktree list --porcelain` records and unresolved/task-only records:

- **canonical** — the worktree Git marks Main, regardless of branch name; never removable;
- **managed checkout** — exactly one valid task binding; receives lifecycle actions;
- **unmanaged checkout** — registered non-main, non-harness worktree; receives Inspect/Open, Adopt, and guarded Remove Checkout (branch always retained);
- **harness checkout** — strict canonical containment under `.claude/worktrees`; visible and openable, never adoptable/removable from this UI;
- **cold task / done task** — no checkout is expected; still visible for Resume or Retire;
- **drift/conflict** — stale path, branch/mode mismatch, duplicate claims, incomplete inventory, missing registration, or harness overlap; visible but fail-closed except for a narrowly proven reconciliation.

Bind a task exactly once: exact canonical worktree path first, then a unique branch match only when the recorded path is absent/stale; branch/direct tasks normally bind only to the canonical row on the live branch. Preserve ambiguity instead of guessing.

### Screen and keys

- In a canonical or linked checkout, `dev flow` opens that canonical repository and focuses the exact current surface. Outside Git, it asynchronously opens a repository picker. `dev flow <repo>` overrides cwd. An unconfigured current repository is still inspectable.
- Layout: worktree/task rows on the left; mode-aware lifecycle rail and evidence in the center; candidate actions and selected condition details on the right, stacking on narrow terminals.
- Up/down or `j/k` changes rows; left/right or `h/l` changes actions; Tab changes pane; Enter opens a plan and never mutates immediately; a second action-specific confirmation applies; Esc backs out; `r` reloads local facts; `R` opens a remote menu for **Fetch refs**, **Refresh PR/MR**, or **Both**; `?` shows evidence/key semantics; `q` exits.
- Render text labels as well as color: READY, NEEDS INPUT, BLOCKED, UNKNOWN, ERROR, STALE, DRIFT.
- “Close” is not a lifecycle node. A DONE managed task offers Retire; an unmanaged row offers Remove Checkout; runtime closure is an effect of those plans. Branch deletion is a separately confirmed managed-retirement variant only.
- Startup/local refresh never fetches or contacts a forge. Network work may be a declared effect of Resume, cold push, review handoff, or a confirmed `R` choice. A manual provider query records only a portable minimum—PR/MR existence, open/draft/merged/closed state, URL, provider, and observation time. Unsupported, unauthenticated, failed, and not-yet-refreshed review evidence remains explicitly UNKNOWN; review/check details not consistently available across providers are not inferred.

## Implementation

### 0. Reconcile the v0.2.3 baseline

- Record and preserve the current `.specstory/statistics.json` and `.specstory/history/...` changes; do not clean, overwrite, or use an ambiguous shared stash.
- Fast-forward the no-ahead feature branch to `main`/v0.2.3 with `git merge --ff-only main`; stop rather than resolve any unexpected local-file collision automatically.
- Run the v0.2.3 TUI loader/readiness/trace race tests before feature edits.

### 1. Add revision-aware task writes and pure safety observations

Extend `internal/task/store.go` without changing task TOML:

- return `Record{Task, Revision}` where Revision hashes the exact record bytes and ID;
- expose list diagnostics instead of printing/skipping corrupt records invisibly;
- add cross-process locked create-only, compare-and-update, and compare-and-delete transactions using `internal/lockx`;
- make existing Save/Delete compatibility methods participate in the same lock, while new lifecycle code uses revision-aware transactions.

Make planning side-effect-free and preserve observation failures:

- split pure artifact readiness/receipt inspection from artifact finalization/reconciliation;
- extend runtime inspection to retain runtime-list/agent-list capability and errors and to use live recognized-agent observations;
- add exact worktree path/branch/HEAD/Main/locked/prunable lookup helpers;
- keep existing no-data-loss behavior: normal TUI removal is non-force, dirty/uncommitted and unresolved artifact state block, and caller/mixed-runtime protections remain absolute. Writer claims and adoption fail closed on any other recognized agent regardless of displayed status; removal/retirement keeps the existing action-specific policy—active or unrecognized agents block, idle/done agents may be externally closed after structural checks, and unknown status requires an explicit external CLI acknowledgement.

### 2. Make repository topology authoritative

Refactor `internal/inventory/repo_context.go` around a topology skeleton plus bounded enrichment:

- always use Git’s worktree records for the selected repository, including bare worktree hubs; retain Git’s real Main record instead of synthesizing one from a navigation path;
- add stable RepoKey/RowKey values, typed observation completeness/errors, task binding, ownership evidence, drift/conflict reasons, and task-only rows;
- use canonical common-directory identity for repository/task joins and preserve unresolved task groups;
- use a strict path-based harness detector; a `worktree-` branch prefix may warn but is not ownership proof;
- preserve runtime unavailable/failed separately from “closed.”

Reuse `gitx.Discover`, `pathx.Canonical`, `gitx.Worktrees`, `inventory.RepoContext`, and the v0.2.3 shared limiter. Add a dedicated context resolver because existing `FindByWorktree` can choose the wrong canonical task and existing repo helpers lose linked-worktree focus.

### 3. Create the single `internal/taskflow` authority

Add a UI-agnostic domain package with value-only, copy-safe types:

- `Locator`, `Action`, action-specific options, `Observation`, `Condition`, `Effect`, `Plan`, `Approval`, `StepResult`, `Handoff`, and `Result`;
- `Plan` contains the exact task revision, source/target state, exact repo/path/branch/base/upstream identities, blockers, inputs, ordered effects, retained resources, confirmation requirement, fallback command, and an authority fingerprint/PlanID;
- `Result` records every attempted/completed effect, partial success, warnings/recovery, milestone, optional post-alt-screen handoff, and a fresh after-snapshot.

The service exposes context/repository inspection plus `Plan(Request)` and `Apply(Plan, Approval)`. `Apply` must:

1. validate the approved PlanID/typed confirmation;
2. lock by canonical repo and task in a fixed order;
3. reload the task revision and freshly re-resolve Git/worktree/runtime/agent/artifact/remote identity;
4. reject a changed authority fingerprint before mutation;
5. execute in the existing no-data-loss order, repeating safety-critical checks after runtime closure and immediately before removal;
6. persist task state/path last where applicable;
7. return a step ledger and fresh snapshot on success or error.

Reuse rather than duplicate:

- `gitx.AnalyzeFinish` and its fingerprint for completion;
- `retire.Inspect` / `CloseAndWait` and `retire.Service` ordering, refactoring them into mechanisms whose caller-supplied identity is re-derived by taskflow;
- `wt.Manager.Create` for COLD resume reconstruction;
- `experiment.TransitionPlan/Result` and lock/revalidation patterns;
- existing runtime-handle coverage, artifact, Git-operation, and branch-containment checks after moving them out of CLI-local policy.

Implement the selected action sets:

- managed: Park Warm/Cold (including explicit WIP/push options), Resume, Direct/FF/Review/Verify-Merged completion, and Retire with optional contained-branch deletion;
- unmanaged: metadata-only Adopt and exact branch-preserving Remove Checkout;
- drift: only typed, proven reconciliation plans; prunable/missing registrations remain inspect/recovery-only because Git prune is repository-wide;
- remote: an explicit Refresh action with independent `FetchRefs` and `QueryReview` options, so the `R` menu can run either or both and return separate outcomes/freshness;
- no generic TUI force. Existing CLI compatibility flags (`--force`, `--close-unknown`, `--assume-no-runtime`, fetch defaults, etc.) remain supported and map to explicit typed options/confirmations; this feature does not silently change command syntax or defaults.

Extend `internal/forge` with a query-oriented, capability-aware review boundary shared by GitHub, GitLab, and Azure adapters. Resolve by canonical remote identity plus exact head/base branch, normalize only the portable status fields (existence, state, draft, URL, provider, observed time), and preserve ambiguous/multiple matches and CLI/auth/provider failures as typed UNKNOWN/ERROR evidence. Keep review observations in the flow model for the current run; do not add a persistent cache or infer a PR from a pushed branch.

### 4. Cut existing mutation paths over before exposing the TUI

Refactor these callers into parse/render/confirm adapters over taskflow, preserving documented output, flags, direct-vs-report behavior, and non-interactive compatibility:

- `internal/cli/park.go`, `resume.go`, `done_flow.go`, `retire.go`, `adopt.go`, and `worktree.go` (`wt rm` only);
- overlapping state/removal suggestions in `internal/cli/sweep.go`, replacing captured mutable closures with frozen plans revalidated at apply time and removing synthetic DONE authorization for unmanaged worktrees;
- the existing dashboard’s Park callback and lifecycle-relevant SetNext write in `internal/cli/tui.go`.

Do not pull `dev start`, `wt create`, repo sync, configurable tools, notes, stats, or dashboard layout into this feature. Add an architecture/boundary test that prevents CLI/TUI handlers from directly assigning lifecycle state, deleting lifecycle tasks, closing runtimes, or removing worktrees on these migrated paths.

### 5. Build the independent `internal/flowtui`

Create a small Bubble Tea model with injected callbacks for launch resolution, repository load/enrichment, Plan, and Apply. Do not embed or add a mode to `internal/tui.Model`.

Reuse the v0.2.3 protocols as patterns:

- return the first View before repository/runtime/Git work starts;
- monotonically tagged request generations plus cancellation and stale-result rejection;
- valid-empty replacement and stale snapshot retention;
- stable RowKey/TaskID selection so a COLD task follows its rebuilt checkout;
- one shared runtime snapshot and bounded per-surface enrichment;
- expensive finish/removal/remote probes only for the selected action.

Read generations never authorize Apply. A mutation is bound to exact identities, cannot be canceled/discarded by refresh or row switching, disables conflicting actions while running, renders its partial-result ledger, then starts a fresh local generation. Directory/runtime activation occurs only after Bubble Tea leaves the alternate screen.

Add `internal/cli/flow.go` and register visible, preview-labelled `dev flow [repo]` through the v0.2.3 `newRootCommand(app)` seam. Treat `flow` as a full-screen command for deferred release checks and privacy-safe TUI tracing. Reject non-TTY invocation before emitting control sequences, with a pointer to non-interactive inventory commands.

### 6. Synchronize user-facing contracts

- Add the feature to `[Unreleased]` in `CHANGELOG.md`; do not commit, push, tag, or publish a release unless separately requested.
- Update README, root/embedded help, `internal/skill/dev-cli/SKILL.md`, authored task-lifecycle/worktree-ownership/runtime-retirement references, and generated command help via `make skill-sync`.
- Add paired English/zh-TW repository-flow documentation and update mental-model, workflow, TUI, compatibility, and source-freshness pages where they describe lifecycle/cleanup authority.
- Document row taxonomy, local/remote freshness, the manual Git/forge remote menu, portable PR/MR fields and UNKNOWN/error semantics, branch-preserving removal, partial results, confirmation rules, and that raw Git/configured external tools remain outside dev-mediated safety.

## Critical files

- `internal/taskflow/{types,service,transitions,park,resume,complete,adopt,remove,retire}.go` — one graph/plan/apply authority.
- `internal/task/store.go` — locked raw-revision create/update/delete.
- `internal/inventory/repo_context.go` — authoritative all-worktree plus task-only projection.
- `internal/retire/{safety,service}.go`, `internal/artifact/service.go`, `internal/gitx/worktree.go` — reusable safety mechanisms and pure observations.
- `internal/forge/{forge,github,gitlab,azure}.go` — capability-aware manual PR/MR lookup and normalized review evidence.
- `internal/flowtui/{model,load,actions,overlay,view}.go` — independent interaction/rendering only.
- `internal/cli/{flow,park,resume,done_flow,retire,adopt,worktree,sweep,tui}.go` — thin adapters and command registration.

## Verification

1. **Transition and condition matrices**
   - Exhaustively test every checkout mode × recorded state × action; reject direct+COLD, DONE park/resume/complete, COLD complete, and other undocumented explicit-task paths before any effect.
   - Test READY/NEEDS INPUT/BLOCKED/UNKNOWN/ERROR aggregation; required unknown/error can never enable Apply.

2. **Identity, topology, and binding**
   - Cover canonical/linked/nested cwd, symlink aliases, same-name clones, unconfigured repos, separate-git-dir and bare hubs, detached/locked/prunable/missing rows, exact-once Git records, task-only COLD/DONE rows, moved paths, wrong branches, duplicate task claims, and harness conflicts.
   - Assert canonical Main is never removable and incomplete inventory can never prove “unmanaged.”

3. **Plan/apply race and no-data-loss tests**
   - Mutate task revision/state/path, worktree registration/path/branch/HEAD, dirty/conflict/Git-operation state, artifact receipts, runtime sessions/current pane/agent activity, and base/upstream refs between Plan and Apply and again after runtime closure.
   - Assert stale plans perform zero effects; unmanaged removal preserves branch ref/OID; completion never removes runtime/worktree/branch; retirement deletes task last; partial failures report completed stages and yield a safe retry plan.
   - Cover runtime none/list failure, caller/mixed sessions, and every recognized-agent status under both policies: all statuses block writer/adopt claims; active/unrecognized block cleanup, idle/done may be safely closed, and unknown requires the existing external acknowledgement. Also cover close failure, timeout, and session reappearance.

4. **CLI parity and architecture boundary**
   - Preserve focused tests for park/resume/done/retire/adopt/wt rm/sweep and dashboard Park while asserting they call the same taskflow plans.
   - Add a source/AST boundary test for migrated handlers and compatibility tests for existing flags/output/structured contracts.

5. **Flow TUI behavior**
   - Model tests for picker/filter/focus, row/action navigation, blocked-plan inspection, action-specific confirmations, stale generation rejection, valid-empty and stale retention, stable selection after COLD resume, in-flight result retention, narrow/monochrome rendering, and deferred handoff.
   - PTY test: immediate first frame and clean quit while context/runtime/topology/enrichment are blocked.
   - Instrument network fakes: startup and `r` make zero fetch/forge calls; only a confirmed network-bearing transition or `R` does. Test Fetch-only, PR/MR-only, and Both; normalize GitHub/GitLab/Azure results; retain separate success/error/freshness when one half fails; and treat zero, ambiguous, unsupported, unauthenticated, and stale results truthfully.

6. **End-to-end and repository gates**
   - In an isolated HOME, display canonical/managed/unmanaged/harness/COLD rows; adopt an eligible external checkout; run HOT → WARM → HOT → DONE → Retire; verify branch retention; exercise manual Git/PR refresh; and prove canonical/harness/active-agent/stale-plan attempts make no unauthorized change.
   - Run focused race tests, `go test -race ./...`, formatting check, `go vet ./...`, build, `make e2e`, `make skill-sync`, `make skill-check`, and strict bilingual MkDocs/source/site checks.
   - Run an independent safety/code review after implementation and address confirmed findings before declaring the feature complete.
