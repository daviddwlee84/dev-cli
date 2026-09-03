# Recover stale workflow work, add agent profiles, and sweep verified ephemeral worktrees

## Context

The generic prompt handoff shipped locally in commit
`b1e74e81b902f5af67957cfdc580d85440ae6807`. Two follow-up needs are now
separate and explicit:

1. Reconcile two dirty Claude Workflow worktrees left by a killed workflow,
   without deleting uncommitted bytes merely because their branches are
   contained in `main`.
2. Improve the agent-profile UX, then add a provider-verified,
   report-before-apply cleanup path for future ephemeral worktrees.

A semantic audit found that the two stale trees contain **no useful hunk absent
from `feat/ssh-hosts`**: 16 files are byte-identical, 19 are superseded by
stronger implementations/tests, and three `noreplace_*` files are unused
alternatives. The remaining product integration is between current `main` and
the already-complete SSH feature, chiefly the additive fleet cache/config/
transport changes.

The user also asked for execution from a clean context after planning. Do not
rely on context compaction for correctness: automatic compaction is
harness-controlled, and `/compact` is a manual user command. Instead, persist
this plan and use one fresh **non-fork** execution agent per phase. Each agent
reads only `AGENTS.md`, this plan, its phase ID, and the current tree; the main
session receives a compact completion report and independently verifies the
result.

## User-authorized policy

- Each phase may create its specified **local** commits/checkpoints after all
  phase gates pass. Every agent-generated commit carries the repository's
  `Co-Authored-By` trailer.
- No push is authorized. Rescue tags must never be pushed.
- Phase A may create two rescue commits, annotated rescue tags, a verified
  bundle, two `git merge --no-ff -s ours` ancestry commits, the normal `main`
  merge into `feat/ssh-hosts`, and one separate non-ff merge of exact commit
  `4167d17df4a38359d6451c223f4d755e17383093` to carry the already-reviewed
  exited-Zellij fix required by the host's full-suite gate. It may also create
  one normal follow-up docs-fix commit limited to correcting the verified zh-TW
  `structured-interfaces` anchor and regenerated `docs/llms*.txt` files after
  strict MkDocs exposed the merge-resolution error.
- After the observed competing-writer race, Phase A may perform exactly one
  clean local `git reset --hard 0786a336870b69d8516ef90f1e9646d1cf1ad7c5`
  from redundant merge `58cb94575a0875421f0640fedd91e423efe3df73`, whose
  tree is byte-identical, then amend only that commit message to add the required
  co-author trailer. The reconstructed commit must retain tree
  `b480f97225a387d2952ed84224004a6fa855f587` and exact parents
  `aa9dcfea55de70e319d9451719d06c1a975be75f`
  `7b0ec57925f80dc61c6d197ebf5213f7ea39919b`; no other
  reset/amend/update-ref is authorized.
- Ephemeral V1 is strict: a killed workflow without a matching child result, or
  any same-ID resumed transcript, is `unknown` and never apply-eligible. There
  is no operator-attestation bypass in V1.

## Clean-context execution contract

Each phase is executed sequentially by a new non-fork agent, with exactly one
writable worktree and no concurrent editing agent.

Before each agent starts, the coordinator verifies:

- exact branch and base SHA;
- exact pre-existing dirty paths;
- the worktree's current writer/runtime owner;
- allowed/generated/forbidden paths from that phase.

The fresh agent must:

1. Read `AGENTS.md` and this plan's assigned phase before editing.
2. Stop on a base mismatch, unexpected dirty path, live competing writer, or
   need for an unlisted file; never silently expand scope.
3. Never stash, reset, clean, force-remove, force-delete, or push.
4. Format only owned Go files; use a read-only repository-wide `gofmt -l`
   check rather than `make fmt` while foreign dirty files exist.
5. Return:

```text
phase: A|B|C
status: complete|blocked
start_head:
end_head:
commits_and_tags:
changed_files:
unexpected_files: []
protected_paths_untouched: true|false
invariants:
tests: exact command + exit status + result
external_effects:
remaining_issues:
next_phase_base:
```

The main coordinator reviews status/diff/tests before accepting the phase and
starting the next fresh agent.

### Protected paths in `feat/pr-ci-scan`

Execution agents must never edit, format, stage, reset, or clean these recorder
paths:

- `.specstory/history/2026-09-01_12-41-38Z-gh-dash-user-pr.md`
- `.specstory/history/2026-09-01_16-46-23Z-make-gofmt-w-go.md`
- `.specstory/statistics.json`

At an authorized feature commit, only the main coordinator may invoke
`agent-history-hygiene` and stage the current phase's plan/session artifacts.
The unrelated `make-gofmt-w-go` transcript and statistics remain excluded.

---

## Phase A — rescue, record semantic ancestry, and reconcile `feat/ssh-hosts`

### Worktrees and expected state

**Writable, one at a time:**

1. Claude source 1:
   `/Users/zhouhanru/Documents/David/Program/dev-cli/.claude/worktrees/wf_4602a254-247-1`
2. Claude source 2:
   `/Users/zhouhanru/Documents/David/Program/dev-cli/.claude/worktrees/wf_4602a254-247-2`
3. Integration target:
   `/Users/zhouhanru/Worktrees/dev-cli/feat-ssh-hosts`

**Read-only:** canonical repo
`/Users/zhouhanru/Documents/David/Program/dev-cli`.

**Forbidden:** `feat/pr-ci-scan` and every unrelated worktree.

Expected identities:

- source branches `worktree-wf_4602a254-247-{1,2}` at
  `437716db46d5cefe526dcb19835786459ee0d07e`;
- target product branch `feat/ssh-hosts` at
  `4ba2d0b21dfb3525f9336bf38ccd2300bd6c3171`;
- exact current main/v0.2.7 snapshot
  `7b0ec57925f80dc61c6d197ebf5213f7ea39919b`;
- unique merge base `437716db46d5cefe526dcb19835786459ee0d07e`.

The target is expected to have exactly two post-commit artifact tails and no
product changes:

- `.specstory/history/2026-09-01_10-07-35Z-ssh-host-fleet-chezmoi.md`
- `.specstory/statistics.json`

The owning `add-ssh-host-lifecycle` session confirmed it is read-only and no
longer depends on orphan sibling `9228bc5`; ignore that unreferenced object and
let Git GC it naturally. Any other mismatch stops Phase A for plan revision.

### Semantic disposition

| Source | Dirty | Exact target | Superseded | Unused alternatives | Missing useful hunks |
|---|---:|---:|---:|---:|---:|
| workflow 1 / `internal/sshhost` | 27 | 13 | 11 | 3 | 0 |
| workflow 2 / `internal/fleet` | 11 | 3 | 8 | 0 | 0 |
| total | 38 | 16 | 19 | 3 | 0 |

No source hunk is copied into the target. The three unused alternatives are
`internal/sshhost/noreplace_{darwin,linux,other_unix}.go`; they remain protected
by the rescue checkpoint but are not integrated as product code.

### A1. Fail-closed preflight

For each source and target:

- revalidate branch, HEAD, registered worktree path/common-dir, lock/prunable
  state, cwd/runtime/process ownership, Git operation, full status, and artifact
  state;
- confirm source 1 has exactly 27 dirty files under `internal/sshhost` and
  source 2 has exactly the 11 audited `internal/fleet` paths;
- require `git clean -ndX` to remain empty in both removable source trees; any
  ignored/environment/transcript path there stops the phase;
- the non-removable SSH target may retain only its audited ignored roots
  `.claude/settings.local.json`, `.venv/`, `dev`, `scripts/__pycache__/`, and
  `site/`; record their top-level inventory, never stage/clean/remove them, and
  stop if any additional ignored root appears;
- hash the audited dirty files, wait for a quiet interval, and require the
  second status/hash snapshot to match;
- re-read Claude Workflow metadata: exact path mapping must remain; no current
  writer may appear. Historical `killed` alone is not proof.

### A2. Checkpoint target artifact tails

The main coordinator—not the fresh integration agent—requires the owning
SpecStory session to release the worktree, then waits for the two artifact files
to remain quiet across two status/hash snapshots before staging and scanning
exactly those paths. Create a separate local checkpoint atop `4ba2d0b`; do not
amend `4ba2d0b`, use `9228bc5`, stash, reset, or touch product or ignored files.

Require:

- checkpoint parent exactly `4ba2d0b`;
- checkpoint diff contains exactly the two artifact paths;
- staged redaction/gitleaks and cached whitespace checks pass;
- target becomes clean;
- record `<A0_SHA>` and `<A0_TREE>`.

The SSH transcript is absent from current main, and main's statistics blob is
identical to the merge-base blob, so this checkpoint should not add a textual
main-merge conflict. If either claim changes, stop and re-audit.

### A3. Rescue exact source bytes

One source worktree at a time:

- stage only the audited paths with explicit pathspecs—never `git add .`, `-A`,
  stash, restore, or clean;
- inspect the cached diff and run `git diff --cached --check` plus staged
  gitleaks/agent-history scanning; a leak stops for rotation/remediation;
- commit the exact snapshot locally;
- create annotated local tags:
  - `rescue/claude-wf-4602a254-247-1-20260903`
  - `rescue/claude-wf-4602a254-247-2-20260903`
- create one bundle outside the repository under
  `$XDG_DATA_HOME/dev/rescues/dev-cli/` containing both tag refs and verify it;
- record source commit/tag/bundle hashes and require each source worktree clean.

Never push the tags or bundle.

### A4. Record semantic ancestry

In clean `feat/ssh-hosts`:

1. Use `<A0_TREE>` from the target artifact checkpoint as the immutable tree
   baseline; keep `4ba2d0b^{tree}` only to prove A0 changed artifacts and no
   product path.
2. Merge each rescued source branch separately with
   `git merge --no-ff -s ours` and a message stating that the target already
   contains reviewed exact/superseding implementations.
3. After each merge, require `HEAD^{tree} == <A0_TREE>` and the corresponding
   rescue commit to be an ancestor.

These are history-only decisions; any tree change is a failure.

### A5. Merge current main semantically

Immediately before merging, require main still exactly
`7b0ec57925f80dc61c6d197ebf5213f7ea39919b`, target clean, and the unique merge
base still `437716d`. Merge the pinned main SHA with `--no-ff --no-commit` so the
full resolution is tested before creating the authorized merge commit. Never
resolve an entire file with blanket ours/theirs.

The read-only merge-tree predicts 27 overlaps: 19 textual conflicts and eight
clean auto-merges. Inspect every auto-merge; manually resolve all conflicts.

Core code unions:

- `internal/fleet/cache.go`: `EndpointID` retains both main's `MachineID` and
  target's `EffectiveRemoteOS()`.
- `internal/fleet/config.go`: retain main's `machineid` import/field/validation
  plus target's `RemoteOS`, managed-fragment provenance/loading, target-OS path
  semantics, collision policy, and platform private-file validation.
- `internal/fleet/transport.go`: retain main's retry policy, bounded IO/capture,
  attempts/errors, no-PTY/forwarding lockdown, and guarded transfers; retain
  target's checked POSIX/Windows dispatch, UTF-16LE PowerShell allowlist/stdin/
  no-dev behavior, platform askpass carriers, and allow Windows `_capability`
  for fleet machine ID while continuing to deny native-Windows file payload
  protocols.
- `internal/lockx/lockx.go`: preserve main's `Lease`, idempotent `Close`, and
  `AcquireDir` API plus target's canonical-parent `WithFile`; `WithDir` keeps
  nil-operation validation, cancellation before callback, directory lease
  semantics, and release-error propagation.
- `internal/cli/fleet.go`: retain main machine-ID/files/protocol helpers and
  target generated-origin/config-edit/open helpers, locked/private writes, and
  redaction.
- `internal/cli/{doctor,root,tldr}.go`: inspect auto-merges and retain main
  flow/MCP/native-skill wiring plus target SSH registration/diagnostics/help.

Hand-authored conflicts also include `CHANGELOG.md`, `README.md`, paired
remote-fleet/commands-config/compatibility/sources-freshness pages,
`internal/help/topics/fleet.md`, and `internal/skill/dev-cli/SKILL.md`. Preserve
main's released 0.2.5–0.2.7, guarded lifecycle, repository-aware skill/MCP, and
machine-ID material while placing SSH/fleet onboarding under current
`[Unreleased]`. Preserve bilingual parity and target OpenSSH/RemoteOS/Windows
security claims.

Inspect clean auto-merges in `AGENTS.md`, paired mental-model pages,
`internal/cli/{doctor,root,tldr}.go`, `mkdocs.yml`, and generated commands.
Regenerate `docs/llms*.txt` and generated command references only after code and
hand-authored docs are resolved; do not hand-select a generated conflict side.

Preserve target RemoteOS/cache/no-dev tests and main machine-ID, localfiles,
protocol, transport-hardening, agent-skill/MCP, and guarded-sync suites. The
main merge commit must have the pre-main-merge Phase A target as first parent
and exact `7b0ec579...` as second parent.

### A5b. Merge the already-reviewed exited-Zellij fix

The host retains old `EXITED` Zellij entries that make the pinned `v0.2.7`
`runtime.TestBackendContract/zellij` fail even though no live session exists.
Do not delete or mutate that external state and do not waive or mask the full
suite. After committing the resolved pinned-main merge, merge exact standalone
commit `4167d17df4a38359d6451c223f4d755e17383093` with `--no-ff`; its parent must
remain the shared `437716d` base. Preserve both sides of any changelog or skill
reference conflict, then require the resulting commit to have the completed
main merge as first parent and exact `4167d17...` as second parent. Do not
cherry-pick it and do not merge later `feat/pr-ci-scan` commits `694cb70` or
`b1e74e8`. The target must pass the previously failing focused Zellij contract
and every A6 gate on this final tree.

### A6. Verify target

- `git diff --check` and read-only `gofmt -l`.
- `go test -count=1 ./internal/sshhost ./internal/fleet ./internal/localfiles ./internal/lockx ./internal/agentskill ./internal/cli`
- `go test -race -count=1 ./internal/sshhost ./internal/fleet ./internal/localfiles ./internal/lockx`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- build and E2E.
- `make skill-sync`, inspect generated commands, `make skill-check`.
- strict source/site bilingual docs checks and `docs/llms*.txt` generation if
  drifted.
- cross-compile the Windows/amd64 and Windows/ARM64 SSH/fleet/CLI test packages
  to temporary outputs outside the repository. Native Windows/amd64 execution
  remains a landing/CI acceptance gate after a future authorized push; because
  this plan forbids push, it is recorded but does not block local source-tree
  retirement once every runnable gate passes and the rescue refs/bundle remain.

### A7. Retire source worktrees

Only after all target gates pass and both rescue commits are ancestors:

1. Recheck each source is clean, has no ignored/artifact/runtime/writer state,
   and is outside caller cwd.
2. Remove each with plain non-force `git worktree remove -- <path>`.
3. Verify path and registry removal.
4. Delete only the now-merged source branches with `git branch -d`; never `-D`.
5. Run `git worktree prune` only after successful removals, then inspect the
   resulting complete worktree list.
6. Retain rescue tags and verified bundle through SSH feature acceptance.

Phase A creates authorized local rescue/ours/main-merge commits and tags, but
never pushes.

---

## Phase B — discoverable, mode-aware agent profiles

**Writable:** `/Users/zhouhanru/Worktrees/dev-cli/feat-pr-ci-scan` only.  
**Base:** `b1e74e81b902f5af67957cfdc580d85440ae6807`.  
**Forbidden:** both workflow sources, `feat/ssh-hosts`, unrelated worktrees, and
protected recorder paths.

### Implementation

1. Add optional `Description string` to `config.Agent`. Keep repeated
   `[[agent]]`, one global default, explicit `--agent`, singleton selection, and
   independent run/open launchers. Add no backend/model schema, inheritance,
   built-in vendor/default, environment map, or secret store.

2. Add `dev prompt agents [--json]`.
   - Human table: `PROFILE DEFAULT RUN OPEN DESCRIPTION`, one row per sorted
     profile.
   - Direct launcher displays only `filepath.Base(command[0])`; shell displays
     `shell`; unavailable displays `—`.
   - Never display argv, shell source, executable directories, environment,
     prompt, or config path.
   - Stable JSON array; every object has `name`, `description`, `default`, and
     nested `run`/`open`, each with `configured`, `kind`
     (`command|shell|none`), and executable basename or empty string.

3. Resolve profile and selected launcher **before** recipe collection.
   - Preserve explicit/default/sole global selection.
   - Never fallback to another profile when the selected one lacks the mode.
   - Missing/unknown/ambiguous diagnostics list mode-capable profiles and point
     to `dev prompt agents`.
   - For non-dry-run open, check TTY before collection.
   - Writer collision remains after collection when cwd is known.

4. Fix `--agent` completion.
   - Use `app.loadCompletion()` because root intentionally skips eager load for
     `__complete`.
   - Reuse `addCompletion` for prefix filtering/sanitized descriptions.
   - `prompt run` lists run-capable profiles; `prompt open` lists open-capable
     profiles; descriptions may say `default · run/open · <description>`.
   - Invalid config degrades to no dynamic candidates and `NoFileComp`.

5. Tests cover config description, sorted/redacted table+JSON, empty inventory,
   no secret argv/shell leakage, explicit/default/sole/ambiguous selection,
   missing-mode no-fallback, early failure before fake forge/runtime collection,
   mode-specific `__complete`/`__completeNoDesc`, parsed `--config`, prefixes,
   descriptions, and invalid config.

### Files and synchronization

Representative implementation paths:

- `internal/config/config.go`, `internal/config/agent_test.go`
- `internal/cli/prompt_command.go`, prompt/completion tests, starter config
- README, changelog, prompt help/skill, paired prompt/commands-config docs
- generated command reference and `docs/llms*.txt`

Run focused tests, read-only format check, full vet/test/race/build/E2E,
skill-sync/check, and strict bilingual docs checks. Gates green authorize the
local Phase B commit; the coordinator stages current plan/session artifacts via
hygiene, excludes protected unrelated artifacts, commits, and records the SHA
as Phase C's base. No push.

---

## Phase C — verified Claude Workflow ephemeral sweep

**Writable:** `feat/pr-ci-scan` only.  
**Base:** exact accepted Phase B commit SHA.  
**Forbidden:** source workflow trees, `feat/ssh-hosts`, unrelated worktrees,
protected recorder paths.

### Architecture

Add provider-neutral `internal/ephemeral`:

- `types.go`: targets, claims, liveness, stable checks, versioned report/apply
  results, `OwnershipSource` interface;
- `audit.go`: pure aggregation; zero facts are unknown and blocked outranks
  unknown;
- `service.go`: read-only collection plus a separately locked/revalidated apply.

Add `internal/ephemeral/claudeworkflow` for the private Claude metadata layout.
`inventory.IsEphemeralWorktree` remains a display/candidate hint only; rename
its implementation to `LooksEphemeralWorktree` and retain the old function as a
compatibility wrapper. A path/name pattern never authorizes deletion.

### Metadata adapter

Starting from a Git candidate, inspect only bounded, fixed-depth paths under
`~/.claude/projects`:

- workflow `<session>/workflows/wf_*.json`;
- agent mappings
  `<session>/subagents/workflows/<wf>/agent-*.meta.json`;
- bounded `journal.jsonl` for matching started/result records;
- same-ID resumed transcript existence at
  `<session>/subagents/agent-<id>.jsonl`.

Requirements:

- validated single-component IDs and fixed containment;
- regular, non-symlink files/directories; reject traversal, NUL, reparse,
  group/world-writable metadata, source mutation during read, duplicate claims,
  path mismatch, and bound exhaustion;
- exact canonical `worktreePath`, `spawnedWithWorktree=true`, matching run/agent
  IDs, `isolation=worktree`, and journal `started`/`result` records sharing one
  required opaque linkage key;
- destructive eligibility additionally requires provider-observed branch, HEAD,
  common-dir, and an opaque non-replayable registration generation that all
  match live Git evidence. Claude Code 2.1.259 records none of these identity
  facts, so this adapter leaves `provider-git-identity` unknown and all current
  Claude claims report-only even under `--apply`; path/name conventions and a
  reusable GitDir pathname are never substitutes;
- tolerate unknown add-only JSON fields because upstream has no schema version,
  but require exact types/presence for every fact used;
- never decode/emit prompts, scripts, logs, result bodies, filenames from model
  output, or transcript content;
- last activity is the maximum trustworthy provider/journal timestamp and
  relevant regular-file mtime; future/conflicting/unparseable time is unknown.

V1 terminal status requires all of:

- workflow status observed as `completed` or `killed`;
- matching agent state `done`;
- matching journal `started` and `result`;
- no same-ID resumed transcript.

A killed/progress/no-result or resumed agent is `unknown` and cannot be cleaned,
regardless of age. No typed-attestation bypass exists in V1.

### CLI

```text
dev sweep --ephemeral-worktrees [--base REF] [--stale-days N] [--json]
dev sweep --ephemeral-worktrees --apply [--delete-branches --base REF]
```

- canonical non-bare checkout only;
- mutually exclusive with `--merged-worktrees`;
- `--stale-days` means provider inactivity, defaults to 14, minimum 1;
- JSON is report-only; reject `--json --apply`;
- apply requires an interactive terminal and per-item confirmation;
- reject `--yes`, `--close-unknown`, `--assume-no-runtime`, and apply with
  `--no-runtime`;
- branch is retained by default; deleting branches requires explicit `--base`.

Stable report schema version 1 includes repository/common-dir identity,
capabilities/diagnostics, sorted candidates, provider/run/agent IDs, normalized
states/times, Git/path/branch/HEAD facts, stable checks/classification, planned
actions, and summary counts. It never persists or prints private metadata
content.

### Eligibility

Checks include:

- verified unique provider ownership/mapping/terminal/result/no-resume/inactivity;
- matching provider-observed branch/HEAD/common-dir/non-replayable registration
  identity; absent identity is unknown and cannot be replaced by current Git
  state, a path/branch pattern, or reusable administrative pathname;
- registered present non-main named worktree, exact common-dir, branch, worktree
  HEAD and live HEAD agreement, unlocked and non-prunable;
- clean staged/unstaged/conflicted/untracked/submodule state;
- zero ignored files in V1 (`git ls-files --others --ignored
  --exclude-standard -z`); ignored content is blocked, never deleted;
- no merge/rebase/cherry-pick/revert/sequencer/bisect operation;
- no task claim;
- artifact inventory known and every intent discarded or finalized/reachable;
- caller outside target, runtime inventory known, no covering/mixed/active
  session.

Classification is `eligible`, `blocked`, `unknown`, or `not-applicable`.
Missing/prunable/unregistered/orphaned paths are report-only; V1 does not prune,
repair, rescue, stash, commit, or remove them. The two historical fixtures must
remain blocked/unknown: both were dirty and younger than 14 days; source 1 also
had resumed/no-result liveness ambiguity.

A clean checkout with unique commits may be removed because its named branch is
retained. Branch deletion is a separate action and additionally requires an
unchanged tip, explicit base, containment, and zero unique commits.

### Apply and revalidation

Report construction is side-effect-free and uses `artifact.InspectWorktrees`,
never `ensureArtifactsFinalized` or a lock that writes state.

For apply:

1. Acquire a common-dir advisory cleanup lock.
2. Re-discover repo/common-dir and re-scan unique provider ownership/liveness/
   inactivity.
3. Re-list worktrees and require unchanged canonical path, branch, HEAD,
   registration, lock/prunable state.
4. Reload tasks, artifact inspection, every runtime, caller containment, full
   status, ignored files, submodules, and Git operation.
5. Compare a stable candidate fingerprint; any change becomes skipped-changed.
6. Remove only with non-force `gitx.RemoveWorktree`.
7. Verify path/registration gone; never delete Claude metadata and never prune
   on normal success.
8. If separately requested, re-resolve unchanged branch/base, use
   `gitx.CompareBranches`, require containment/zero unique, and call only
   `git branch -d`; failure leaves the branch retained and reports partial
   completion.

### Tests and docs

- Audit precedence and every classification.
- Adapter fixtures for completed/killed, progress/no-result, resume, malformed
  types, unknown fields, duplicate/path mismatch, shared/mismatched journal keys,
  symlink/traversal, oversized/over-count inputs including nonmatching directory
  entries, directory/file mutation, future/conflicting timestamps, privacy/redaction.
- Service races mutating every proof between report/apply, same-path metadata
  replay, missing live/provider registration generation, unknown-path runtime
  sessions, stale-day overflow, and local-branch base aliases.
- CLI canonical-only and flag conflicts, age minimum, JSON purity/schema,
  report immutability, dirty/untracked/ignored/submodule/operation, task,
  artifacts, runtime/caller, locked/prunable/missing/orphan, branch-retained
  unique commits, safe branch-d, no force/prune/metadata deletion.
- Update changelog, README/TODO, retirement help, paired retirement/
  compatibility/commands docs, bundled agent-retirement/worktree-ownership
  references, generated commands and llms files.
- Full format/vet/test/race/build/E2E, skill and bilingual docs checks, Unix
  native plus Windows compile/native filesystem-adapter gates.

Gates green authorize the local Phase C commit with hygiene-staged current phase
artifacts and no push.

## Out of scope

- Backend/model inheritance or built-in agent profiles.
- Secret/environment injection into agent profiles.
- Operator-attested cleanup of unknown Claude workflows.
- Automatic dirty rescue/commit/stash or ignored-file disposal.
- Other agent-harness metadata adapters.
- Automatic missing/prunable/orphan repair or repository-wide prune.
- Force worktree removal, `branch -D`, metadata deletion, or rescue-tag push.
- Automatic context compaction as a correctness mechanism.
