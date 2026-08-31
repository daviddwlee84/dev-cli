# Context

The TUI currently renders a loading screen before local inventory finishes, but it still has unmeasured synchronous work before that screen: `runTUI` resolves the runtime and skill project root and reads REMOTE/FLEET caches before `tea.Program.Run`, while `View` reaches `renderFooter` -> `Model.Tools` and may run a three-second interactive login-shell probe. Once Bubble Tea starts, TASKS, REPOS, and TRY are loaded serially and published in one `reloadMsg`, so an early TASKS result waits for the slowest local source. REPOS takes roughly three seconds for the documented 56-repository case, reads tasks/runtime data again, and FLEET later repeats the local repository collection.

There is also no precise readiness contract: cache-seeded usability, a current live snapshot, failed-but-usable stale data, and SIZE enrichment completion are represented by unrelated booleans. There are no startup/tab timing events or benchmarks, and most asynchronous loads lack the generation guard already used by notes and disk-size loads.

The intended outcome is to make startup and requested-tab readiness observable and structurally non-blocking, then remove measured duplicate work without letting cache data become Git/runtime authority. CI will guarantee ordering, stale-result rejection, and responsiveness while producers are blocked; real wall-clock values will be observed as reference-profile SLOs rather than promised across arbitrary Git repositories, shells, SSH hosts, and forge services.

## Scope and non-goals

- Implement tracing, readiness/generation correctness, first-frame deblocking, independent local-tab publication, shared local inputs, and reuse of accepted REPOS data for REMOTE/FLEET.
- Preserve lazy live loading for REMOTE, FLEET, and SKILLS and preserve `r` as an explicit current-generation refresh.
- Keep `stats.db` unchanged; performance traces are opt-in disposable diagnostics, not activity data.
- Do **not** add a persistent TASKS/REPOS/TRY display cache yet. First measure the optimized live pipeline. If it still misses the agreed reference budget, a later change may add a minimal, versioned, display-only repository identity cache with stale/unknown rendering and no action authority.
- Do not fold release-cache registration, general cache unification, per-row/per-host streaming, or SIZE cache pruning into this change.
- Preserve the unrelated existing `.specstory` working-tree changes.

## 1. Add a truthful, private performance trace and capture a baseline

Create a small `internal/perftrace` package with an injected clock, a no-op implementation, a concurrency-safe bounded recorder, and a versioned JSON allowlist schema. Activate it only when `DEV_TUI_TRACE` names an absolute, non-existing output file; write that file once with private permissions after Bubble Tea restores the terminal and before any `cd`/runtime activation. Freeze the recorder before writing so late Bubble Tea commands become no-ops, and report trace-write failure only after terminal restoration without changing command success.

Record only relative monotonic offsets/durations, sequence, fixed event/view/stage/source/freshness/outcome enums, generation, and non-sensitive counts. Never record paths, repository/task/host/tool names, command arguments, key values, URLs, runtime handles, raw errors, or provider output. Cap events (with `dropped_events`) so repeated refreshes cannot grow memory indefinitely. Do not write to stdout, `stats.db`, XDG cache automatically, or the network.

Instrument these defensible boundaries through `cmd/dev/main.go`, `internal/cli/root.go`, `internal/cli/app.go`, `internal/cli/tui.go`, and an observer injected into `internal/tui.Model`:

- `cli.execute_begin`, root construction, `app.load`, and named TUI setup/producer spans.
- `tui.program_run_begin` and `tui.initial_view_returned` (explicitly not called “paint”).
- `tui.first_post_view_output_write` only if an `Fd`-preserving output wrapper passes Unix PTY and Windows terminal tests; otherwise omit it rather than changing terminal behavior.
- `tui.first_key_received` and key-update duration, without recording the key or treating user think time as latency.
- Per-view request, cache/live snapshot accepted, optional first enrichment, result discarded, and load finished events.

First land this instrumentation without changing loading order, then collect at least 30 cold/warm samples for runtime `none` and `auto`, the documented ~56-repository profile, and a larger stress fixture. Preserve this trace schema while applying the following optimizations so before/after median, p95, and max remain comparable. Use an external PTY harness for process-spawn-to-frame-bytes; an in-process trace starts at `cli.Execute`, not OS process creation or terminal rasterization.

## 2. Give every requested view generation-safe readiness semantics

Add a focused `internal/tui/readiness.go` and replace the overlapping local/remote/fleet/skills loading booleans with a fixed six-element value array (never a mutable map in the value-copied Bubble Tea model). Each view state tracks current generation, request cause (`initial`, `visit`, `refresh`, `config`, `action`), loading, whether a valid snapshot exists (so successful empty differs from failure), source (`cache`/`live`), freshness, terminal outcome, and whether the snapshot is actionable.

Use a common event envelope but view-specific milestones:

- TASKS/REPOS: live snapshot accepted, optional first enrichment when one is actually emitted, load finished.
- TRY: live snapshot accepted and load finished in this first implementation.
- REMOTE/FLEET: cache snapshot accepted, live/provider snapshot accepted, load finished.
- SKILLS: local snapshot accepted and load finished; explicit network update checks remain a separate operation.
- SIZE and note loading retain their own completion semantics and never delay a tab’s base readiness.

There is intentionally no “all tabs ready”: lazy tabs that were never visited have no request.

Tag every cache/local/REMOTE/FLEET/SKILLS read message with its generation and explicit validity/outcome. Increment before every initial load, first visit, `r`, config reload, and action-triggered refresh. Accept only the current generation; cancellation is an optimization, while generation comparison remains the correctness guard when a callback ignores cancellation. A valid empty result replaces old rows; a failed refresh retains prior usable rows, marks them stale/failed, and scopes its status/error to that view. Clone top-level slices and changed nested values before applying patches so candidate Bubble Tea models do not share mutation.

Create one run-scoped cancellable context in `runTUI` for read/probe commands, cancel superseded reads and cancel the run when `Program.Run` returns. Do not cancel or discard the completion of mutating actions; task mutations should reload the latest task by stable ID and save a copy instead of mutating a pointer owned by a model snapshot.

Reuse the existing patterns in `internal/tui/size.go` (`diskusage.Load.ID`, cancellation, channel completion) and `internal/tui/model.go` (`noteRequest`/`NoteTarget.Key`) rather than inventing a generic scheduler.

## 3. Remove synchronous first-view blockers

Refactor `internal/cli/tui.go`, `internal/tui/model.go`, and `internal/tui/view.go` so model construction and `View()` perform no filesystem, cache, process, runtime, or provider work:

- Replace `Tool.Available func() bool` with tri-state resolved data. Probe configured tools after the first view through a small bounded background command; hide/fail closed while unknown, and discard probe results from old config generations. `renderFooter` and `Model.Tools` become pure.
- Add an invocation-local, context-aware `sync.Once` runtime resolver shared by local loaders and actions. Remove the model’s synchronous backend query; each open callback returns either a directory handoff or runtime handle in `OpenResult`. Reuse a successful Herdr resolution/list result when possible instead of probing twice.
- Resolve the project root inside the lazy SKILLS load/action path rather than before `Program.Run`.
- Decode REMOTE and FLEET caches in generation-tagged Init/visit commands. A late startup cache result must not overwrite a newer explicit refresh. Network/forge/SSH/Node work remains visit- or action-triggered.
- Split passive release checking into a cache-only nudge and a detached refresh command, scheduling the optional network refresh only after the initial view has returned. Preserve its existing opt-out and non-blocking behavior.
- Keep `App.Load` synchronous for config/service construction unless the baseline identifies it as material; do not create a partially initialized `App` pre-emptively.

## 4. Publish TASKS, REPOS, and TRY independently without doubling process pressure

Add a TUI-specific local load-cycle coordinator in `internal/cli/tui_load.go` and a channel-backed load adapter in `internal/tui/local.go`, modeled after the SIZE stream. One generation should acquire immutable shared inputs once, then emit one terminal result per local view rather than one composite `reloadMsg`:

- Read `Tasks.List` once.
- Resolve/list runtime sessions once and supply the snapshot to every adapter.
- Discover repositories once and supply that result to the REPOS adapter and later consumers.
- Run TASKS and REPOS Git enrichment concurrently behind one context-aware shared limiter, retaining the current aggregate baseline cap of eight rather than starting two independent eight-worker pools.
- Pass supplied task/session/repository snapshots through new optional fields in `inventory.Options` and `repoCollectOptions`; retain their existing behavior for non-TUI callers.
- Continue treating `experiment.Service.List` as one bounded TRY producer in this pass; reuse its existing `ListOptions.SkipEnrichment`, `ServiceConfig.MaxEnrichment`, and supplied-session seam without prematurely streaming individual Try rows.
- Emit complete per-view snapshots initially. Do not stream individual REPOS rows until selection is preserved by canonical repo identity and traces prove that a two-stage skeleton/enrichment design is needed.

A blocked REPOS or TRY producer must not delay TASKS publication, and completion/failure of one view must not change another view’s generation or readiness.

## 5. Reuse accepted local data in lazy REMOTE/FLEET flows

Refactor the TUI-specific adapters while preserving existing non-interactive command behavior:

- Separate forge-provider collection from local matching. In the TUI, match provider/cache rows against the model’s accepted REPOS snapshot with the existing `Model.matchRemoteLocals` logic (made clone-on-write), rather than calling `repo.Discover` again after every provider refresh. Preserve catalog-based Try matching and duplicate-identity ambiguity handling.
- Extract a pure `fleet.Snapshot` conversion from `[]tui.RepoRow`. Let the TUI FLEET load consume the accepted live REPOS generation instead of calling `localFleetSnapshot`/`collectReposWithOptions` a second time. If FLEET is visited first, show any cache plus an explicit waiting/loading state and start its live refresh when the matching REPOS generation arrives; keep host fan-out under the existing fleet limit.
- Gate cache persistence by the current accepted generation (not merely command completion) so a superseded slower refresh cannot overwrite a newer cache snapshot.

## 6. Fix cache correctness directly exposed by this work

Include focused fixes and regression tests for:

1. In `collectRemotes`, persist a complete successful aggregate even when every successful provider returns zero rows, so old repositories cannot reappear on restart. Preserve the prior cache when no provider succeeded.
2. Include decimal `Host.Port` in `fleet.EndpointID`, intentionally causing a one-time miss when upgrading and preventing a cache from a different SSH endpoint from being reused.
3. Treat decoded REMOTE/FLEET cache data as untrusted presentation input: reject malformed/oversized payloads, validate fixed enum/time/string bounds, and use `pathx.JoinChild`/component validation for clone destinations so a cached name such as `../outside` cannot escape `project_root`. Preserve existing confirmation and live revalidation in the actual command path.

Do not add a new `dev cache` target in this change because no new persistent cache is introduced; therefore no Cobra command regeneration is expected.

## 7. Verification and UX acceptance

### Deterministic gates

Extend `internal/tui/tui_test.go` with fake clocks and channel barriers (not sleep-based latency assertions):

- With runtime, project-root, cache, tool, local, and optional provider fakes blocked, `View()` returns a loading frame, no probe/provider is called from render, navigation/quit remains processable, and lazy live providers remain untouched.
- Deliver generation N+1 before N for every six-view read family; only N+1 may alter rows/readiness/error. Repeat with a blocked startup cache seed followed by `r`.
- Verify valid-empty replacement, failed-refresh retention, view-scoped errors, and no endless loading state.
- Block REPOS/TRY, release TASKS, and verify independent publication; repeat for each local view.
- Counters prove one task read, runtime selection/list, and repo discovery per local generation; a concurrency tracker proves the shared TASKS/REPOS enrichment cap is never exceeded.
- Accepted REPOS data feeds FLEET and REMOTE matching without another local discovery/task/runtime pass.
- Model copies do not share row mutation, while canceled or late read commands cannot append to a frozen trace.

Add focused tests in `internal/perftrace`, `internal/forge`/`internal/cli`, and `internal/fleet` for schema privacy/caps/concurrency, nonfatal trace write failure, successful-empty REMOTE persistence, all-provider-failure retention, port-only endpoint invalidation, malicious cache payload rejection, and clone destination containment.

Add a Unix build-tagged PTY smoke test (using existing `golang.org/x/sys` facilities where practical) that observes an application header/loading frame and can send `q` while producers are blocked. Use a generous timeout only as a deadlock guard; add Windows-focused compile/terminal-wrapper tests if a production output wrapper is retained.

### Measurements and budgets

Add non-gating benchmarks for pure first `Model.View`, cache decode by payload size, TASKS/REPOS/TRY collection with injected probes, local-to-FLEET conversion, and stale-generation rejection. Compare the instrumentation-only and optimized builds on identical 0/10/56/large repository fixtures and cold/warm cache modes.

Use these provisional reference-profile SLOs, then record the actual profile with the results:

- process spawn -> first observed application frame bytes: p95 <= 200 ms, max <= 500 ms;
- injected key -> resulting frame bytes: p95 <= 100 ms;
- warm cached tab visit -> usable cached rows/frame: p95 <= 100 ms;
- no wall-clock completion promise for Git/REMOTE/FLEET/SKILLS providers; instead require immediate loading/cached usability, bounded provider timeouts, current-generation completion events, and uninterrupted interaction.

CI gates the structural properties above, not noisy real-machine millisecond thresholds. A future local display cache is considered only if the optimized live REPOS/TRY measurements still miss an agreed reference-profile readiness target.

### Repository checks

Run focused tests first, then:

```bash
files="$(gofmt -l .)" && test -z "$files"
go vet ./...
go test ./...
go test -race ./...
go test -run '^$' -bench . -benchmem ./internal/perftrace ./internal/tui ./internal/cli
make build
make skill-check
uv sync --frozen --extra docs
uv run python scripts/check-docs.py --source
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

Run the built TUI once with an isolated explicit trace path, inspect that each requested view has request/accepted/finished or failure events, verify stdout remains unchanged, and compare the recorded baseline/optimized distributions.

## Documentation and release surface

Update `[Unreleased]` in `CHANGELOG.md`; README; `internal/help/topics/tui.md`; `internal/skill/dev-cli/SKILL.md`; and the paired English/zh-TW TUI and commands/config/freshness pages. Document exact readiness meanings, trace opt-in/privacy/retention, cached-versus-live behavior, `r` semantics, and the fact that observed frame bytes are not terminal rasterization. Correct the existing “five views” drift to six wherever touched. Do not run `make skill-sync` unless implementation changes Cobra syntax; still run `make skill-check`.

## Critical files

- `internal/cli/root.go`, `internal/cli/app.go`, `internal/cli/tui.go`, new `internal/cli/tui_load.go`
- new `internal/perftrace/*`
- `internal/tui/model.go`, `internal/tui/view.go`, `internal/tui/size.go`, new `internal/tui/readiness.go` and `internal/tui/local.go`
- `internal/inventory/inventory.go`, `internal/cli/fleet.go`, `internal/fleet/cache.go`, `internal/forge/cache.go`
- `internal/tui/tui_test.go` and focused CLI/cache tests
- `CHANGELOG.md`, `README.md`, embedded help/skill, and paired English/zh-TW docs
