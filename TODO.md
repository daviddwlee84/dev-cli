# TODO

Priority `P1` (next) … `P4` (someday); effort `S` / `M` / `L`.

## Active

### P1 · L — Verified backup receipts and safe local eviction
Phase 1 intentionally stops at reversible archive. Before adding `dev reclaim`
or any action that deletes a local checkout, build a fresh preflight that checks
all local heads/tags/notes/stash against actual remote refs, plus dirty,
untracked, ignored, LFS, submodule/nested-repo, live-runtime, task, cwd and
linked-worktree state. `no remote` or any local-only ref must block safe
removal; multiple remotes/upstreams require an explicit recovery target.

Add an explicit `backup --push` receipt, report-only `reclaim`, typed/batch
confirmation, separately named data-loss acknowledgements (never a generic
`--force`), linked-worktree removal that preserves branch/common-dir, and
remote/shared-Git restore. Non-Git experiments need a real archive/export
format before they can be called recoverable. Do not infer safety merely from
"origin exists" or the current branch being synced.

### P2 · L — Versioned orthogonal task integration phases
The backward-compatible retirement MVP derives READY/MERGED/RETIRED while
persisting the existing hot/warm/cold/done state. A future task schema should
separate execution disposition from integration phase, adopt strict versioned
decoding plus cross-process update locks, and explicitly migrate ambiguous
legacy done records. Old binaries currently ignore and later erase unknown TOML
fields, so this requires a deliberate minimum-version/downgrade policy.

### P3 · M — Query forge merge status and squash identity
Persist PR/MR identity and extend forge adapters to report open/draft/merged
state, merge commit and method. Until then `done --merged` proves ordinary
ancestry and squash completion requires explicit operator attestation; never
infer a squash solely because the source branch disappeared.

### P2 · M — Incremental local repo snapshot
The TUI now renders immediately and probes repositories in the background with
bounded concurrency, reducing perceived startup latency. A 56-repo refresh
still takes roughly three seconds because status + latest commit remain live Git
queries. If inventories grow into the hundreds, persist a short-lived snapshot
and stream changed rows into the model; keep `r` as the explicit live refresh.

### P3 · M — Optional repo-local task backend adapters (td / beads)
Research and detect repo-local task stores without making either a core
dependency:

- td: <https://sidecar.haplab.com/docs/td>
- beads: <https://github.com/gastownhall/beads>

Both may create dot-folders inside a repo. An adapter should report which
backend is present, delegate through its CLI, and avoid copying its task data
into dev's own notes/tasks. Decide only after comparing lifecycle, multi-agent
locking, git noise, cross-machine sync, archival and uninstall behavior. Until
then, record the integration point rather than prematurely selecting a tool.

### P3 · S — Television / fzf channel
A `tv` channel over `dev ls --json` for fuzzy-picking a task, and an `fzf`
fallback. Navigation by metadata rather than by remembering a path is the point
of the tool; the TUI covers it, but a picker composes better with other tools.

### P3 · S — `dev stats` per-repo heatmap
`--repo X` currently filters the totals and the combined grid. A dedicated
per-repo grid, several stacked, would answer "which project did I move to in
March" better than the aggregate does.

### P4 · M — Raycast extension
`dev ls` and `dev resume` from the launcher. Wants the JSON contract and a
stable binary path; nothing else new.

## Deliberately not doing

### Installing dependencies
`dev doctor` reports what is missing and what degrades; it will not install
anything. This is the one place where being glue matters most: git, herdr,
tmux, gh, glab, lazygit and yazi are all installed by a package manager the
user already has opinions about (brew, apt, mise, nix, chezmoi), and a tool
that shells out to one of them is guessing. `doctor` naming the missing binary
is enough — the user knows how they install things on that machine.

Reconsider only if `doctor` grows a case where the *right* command is
unambiguous and the user has asked for it.

### A second git database
Anything derivable from git is derived live, every run. Durable stores hold
human intent and identity: task state/owner/next action, asset tags/notes,
experiment lifecycle and per-host location. Recovery receipts may record one
past verification, but apply must verify again. Live branches, remotes,
ahead/behind, dirty state and sizes remain derived/cache-only; a durable tool
that disagrees with git about git is worse than no tool.

### Syncing runtime state between machines
Each host runs its own multiplexer. What crosses machines is branches, through
the remote, plus the task registry if `state_dir` is a git repo. Syncing
sessions would mean reconciling two live terminal states, which is a much
harder problem than the one this tool has.

### Owning the worktree path on machines with herdr
`herdr worktree create` would be less code, but the path policy has to hold
where herdr is not installed. dev creates the checkout with git and asks herdr
only to open it. See `internal/skill/dev-cli/references/worktree-ownership.md`.

## Done

- Repository quick notes: multiple timestamped sidecar Markdown files keyed by
  catalog ID, rebuildable SQLite FTS, complete CLI CRUD/search, `n` quick add and `N` TUI
  overlay with browse/search/expand/editor/confirmed-delete.

- Task lifecycle: `start` / `park` / `resume` / `done` / `sweep`.
- Worktree ownership rule, path templates, provisioning, per-repo `.dev.toml`.
- Runtime adapters: herdr, tmux, zellij, none, behind one contract suite.
- Repo discovery and forge-backed clone, create, sync.
- Durable Try catalog and lifecycle: create/clone/open, tags/notes, explicit
  deprecate/reactivate, reversible archive/restore, and identity-preserving
  graduate; no permanent local eviction in Phase 1.
- Activity stats: sampler, git backfill, WakaTime import, heatmap.
- Development journal generation with author/range/granularity controls,
  AI-friendly Markdown/JSON, optional diff metrics and current WIP context.
- Machine-wide project summaries with adaptive Markdown, complete JSON,
  repository/Try context, attention filtering and recent commit hints.
- Interactive TASKS / REPOS / TRY / REMOTE dashboard with vim navigation,
  full-screen help/forms, lifecycle actions, lazy forge inventory, private XDG
  caches, local-kind matching and confirmed clone.
- Explicit configurable TUI tools, including interactive shell aliases/functions,
  with lazygit / yazi / editor / shell defaults.
- `dev gitignore`, from GitHub's templates plus the sections no template has.
- `dev adopt`, to import existing worktrees, sessions and unmerged branches.
- `dev bootstrap`, recursively classifying existing repos/worktrees/bare hubs,
  building a non-destructive symlink index, or planning guarded atomic moves.
- Symlink catalogs as first-class scan roots, deduplicated with physical paths.
- `dev config init` / `dev edit`, generating and opening config from the
  machine's detected layout; TUI edit + live config/data/tool reload.
- Explicit direct / branch-only / worktree task modes, including direct-main
  lifecycle and ad-hoc repo open with no task.
- Rich starship-like Git status counts plus unique-path/type breakdown.
- Latest repo activity plus configurable REPOS columns and
  activity/latest/name/git/size/tasks sorting; repository-wide remote/upstream
  topology with no-remote/local-only/multi-upstream filters.
- Portable logical disk usage split into checkout/private/shared Git, streamed
  into the TUI behind a versioned 10-minute XDG cache.
- Immediate TUI render with bounded-parallel repo probes (serial path measured
  4.2s); background rows replace the initial loading state.
- Explicit Herdr/tmux LIVE repo status and selected-repo heatmap overlay in TUI,
  including `b` single-repo backfill.
- XDG-configured multi-host fleet snapshots over SSH, stale-cache fallback,
  Herdr/SSH remote navigation and strict clean fast-forward synchronization.
- XDG `stats path/clear` and regenerable `cache list/path/clear` with stats data
  deliberately kept out of cache semantics.
- Bundled agent skill with a generated, drift-checked command reference.
