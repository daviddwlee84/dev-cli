# Context

Three related bootstrap/dashboard behaviors currently create misleading project state. First, REMOTE drops back to ordinary list mode as soon as clone confirmation is submitted, falsely rendering `not cloned — press c` while `git clone` runs; its immediate runtime handoff also bypasses local discovery, so the clone is absent from REPOS. Second, the built-in `agent-ready` preset commits an operationally empty three-line `AGENTS.md` that tells agents to run “documented checks” even when the generated README contains no commands. Third, the common generated `.gitignore` omits SpecStory's derived `statistics.json`; this repository already has the correct hand-written rule, but its legacy tracked file remains noisy because ignore rules do not affect indexed files. The intended outcome is observable, ordered clone reconciliation; one honest canonical starter agent contract; and an exact statistics ignore that preserves durable SpecStory histories.

## Implementation

1. **Separate cloning from opening in the CLI adapter** (`internal/cli/tui.go`).
   - Change `Actions.CloneRemote` to return only the acquired local path and error; keep `Actions.OpenRemote` as the existing runtime handoff boundary.
   - Extract a small testable TUI clone helper and implement it through `repo.Acquire` (`internal/repo/acquire.go`) with `AcquireClone`, the path-confined `project_root/<repo-name>` destination from `tuiRemoteCloneDestination`, and the forge clone URL. This reuses existing destination-exists, nested-repository, ref normalization, and credential-redaction checks instead of the current bare `gitx.Run` call.
   - Do not clear or rewrite `$XDG_CACHE_HOME/dev/remotes.json`: it contains forge inventory only. The required cache update is the local REPOS snapshot described below.

2. **Add an explicit clone lifecycle to the Bubble Tea model** (`internal/tui/model.go`, `internal/tui/local.go`, `internal/tui/readiness.go`).
   - Add value-copied clone state with a request ID, exact forge/full-name identity (never the movable cursor), known path, requested handoff (`stay` or `open`), cancel function, phase (`cloning`, `refreshing`, `opening`), and the minimum REPOS generation that may complete it. Add dedicated clone-result/open-result messages rather than overloading `actionMsg`.
   - Require fresh, actionable REPOS and requested TRY snapshots before offering clone, and reject a `project_root` that the configured discovery roots/depth/skip policy cannot reach. Preserve direct symlink-index reachability.
   - Add a throttled `bubbles/spinner` `MiniDot` model. Start its tick command when cloning begins, update it only while a clone lifecycle is active, and stop re-arming ticks when the lifecycle completes or fails.
   - In clone confirmation, keep `esc` as cancel, make `enter` start clone-and-stay, and make `o` start clone-and-open. Snapshot the exact confirmed row so a background REMOTE reorder cannot switch targets. `q`/Ctrl-C cancels the currently active clone/refresh/open context but waits for its result rather than abandoning mutation state.
   - On clone success, patch the exact REMOTE row, start a generation-guarded REPOS-only refresh, and keep a short-lived unconfirmed marker only across partial/unrelated observations. A complete REPOS snapshot or positive conflicting identity remains authoritative; markers never enter the forge cache.
   - Finalize from any accepted current-or-newer REPOS result. `enter` stays in the TUI; `o` opens after refresh, and still honors the requested exact-path open with a warning when refresh fails. Open failures retain the successful clone. Failed Git clones that leave a destination render it as `inspect` with its exact path instead of deleting it or offering a misleading retry.
   - Preserve completed handoffs from commands launched just before clone, and do not let their late results tear down the TUI mid-mutation.

3. **Render progress and updated affordances** (`internal/tui/view.go`, `internal/tui/overlay.go`).
   - During `cloning`/`refreshing`/`opening`, render the spinner plus phase text in the REMOTE row's LOCAL column/detail and in the footer status, never `not cloned — press c`.
   - Update confirmation/help text to `enter clone · o clone and open · esc cancel`; suppress normal clone/open bindings while the operation is active.
   - Once the accepted local snapshot lands, render the existing `repo` local-kind marker and local path, so REMOTE accurately identifies the local repository.

4. **Lock behavior with focused tests** (`internal/tui/tui_test.go`, `internal/cli/tui_internal_test.go`, and existing repo acquisition tests where useful).
   - Drive the TUI step-by-step rather than eagerly consuming the whole command chain, and assert the intermediate spinner/status/`cloning…` state appears immediately after both `enter` and `o`.
   - Cover cancellation, duplicate/action/quit blocking, clone failure, clone success followed by a generation-guarded local reload, and no forge reload.
   - Assert the accepted REPOS result changes REMOTE to `repo` with the path and makes the new repository findable through the REPOS filter in the same TUI run.
   - Assert `enter` stays in the dashboard, while `o` invokes `OpenRemote` only after REPOS acceptance; an open failure must not erase the clone.
   - Cover refresh error/missing-discovery behavior so the known path remains visible and no false refresh success is reported.
   - Test the production helper against a local Git source to verify it clones through `repo.Acquire` into `project_root` and preserves its safety/error behavior.

5. **Replace the misleading `agent-ready` contract with one canonical starter** (`internal/scaffold/agent_contract.go`, `internal/scaffold/builtin.go`, `internal/cli/repo_initializers.go`).
   - Define one shared fixed-body starter helper in `internal/scaffold` and use it from both the built-in preset and native fallback, eliminating the two divergent AGENTS bodies without interpolating untrusted repository names into Markdown.
   - Make the generated file explicitly say `Bootstrap status: incomplete` and that it is a safe starter, not verified project documentation. Include a useful repository-wide baseline (preserve user work, no destructive/publish/secret actions without authority, no fabricated facts, never claim unrun checks), then clearly marked TODO sections for purpose, prerequisites/build/test/format/run commands, architecture, behavioral contracts, and concrete handoff reporting.
   - Preserve `planHasDestination("AGENTS.md")` and existing-file behavior: custom preset content remains authoritative, and an existing `AGENTS.md` is never overwritten. Do not auto-migrate legacy generated files and do not add a `CLAUDE.md` symlink in this change.
   - Add content-level tests in scaffold planning plus `repo new`/wizard/setup/native initializer paths: assert incomplete/TODO and handoff sections, absence of the misleading unconditional documented-checks sentence, equality between native fallback and canonical output, and preservation of existing/custom files.

6. **Make SpecStory statistics an exact common ignore, without hiding history** (`internal/ignore/compose.go`, ignore/CLI contract tests).
   - Add only `.specstory/statistics.json` to `agentsSection`; do not ignore `.specstory/`, `.specstory/history/`, `.specstory/.project.json`, or `.specstory/cli/`. Keep the optional history-hygiene nested ignore independently valid.
   - Rely on the existing managed-block merge: new `agent-ready` projects and explicit `dev gitignore` / `dev repo setup --preset agent-ready` reruns receive the rule idempotently while preserving hand-written content. Do not scan or mutate unrelated existing repositories.
   - Remove this repository's legacy `.specstory/statistics.json` index entry while preserving the local generated file; the already-present root ignore then suppresses future rewrites. Keep all history Markdown trackable and do not modify the RPi sample repository.
   - Extend pure compose/merge tests, `git check-ignore --no-index` contracts, and agent-ready end-to-end tests to assert statistics is ignored exactly once while histories, project identity, and SpecStory config remain visible.

7. **Synchronize user-facing behavior** (`CHANGELOG.md`, `README.md`, `internal/help/topics/{tui,repositories}.md`, `internal/skill/dev-cli/SKILL.md` and `references/repository-bootstrap.md`, paired English/zh-TW TUI/getting-started/reference pages).
   - Add separate `[Unreleased]` fix bullets for REMOTE reconciliation, honest `agent-ready` guidance, and the exact SpecStory statistics policy.
   - Document `enter` versus `o`, spinner-backed progress, local-only inventory refresh, and immediate REMOTE/REPOS reconciliation.
   - Describe the starter as explicitly incomplete/non-fabricating and distinguish the common top-level statistics ignore from durable trackable histories and optional history-hygiene metadata handling.
   - No Cobra syntax changes are involved, so generated command reference regeneration is not expected; regenerate generated docs text only through the documented source checker if it reports drift.

## Verification

- Run focused tests for the REMOTE clone lifecycle/production helper, canonical agent-contract rendering and repository workflows, and ignore merge/Git contracts.
- Run `gofmt` on changed Go files; then `go test ./internal/tui ./internal/cli ./internal/repo ./internal/scaffold ./internal/ignore`, `go test -race ./...`, `go vet ./...`, and `make skill-check`.
- Confirm `.specstory/statistics.json` remains on disk but no longer appears as a tracked modification, while `.specstory/history/*.md` remains visible to Git.
- Run documentation validation: `uv run python scripts/check-docs.py --source`, `uv run mkdocs build --strict`, and `uv run python scripts/check-docs.py --site site` (regenerate `docs/llms*.txt` only if the source check reports drift).
- Exercise the real dashboard with a disposable authenticated/private-safe test remote or local forge fixture: verify the spinner persists until clone completion, `enter` stays, `o` opens only after refresh, REMOTE shows `repo` + path, and `/` in REPOS finds the clone without a forge network refresh.
- Generate a disposable `agent-ready` repository and inspect the committed `AGENTS.md` and `.gitignore`: guidance must be useful yet explicitly incomplete, statistics ignored, histories/config not broadly hidden, and rerunning setup/gitignore must be idempotent.
