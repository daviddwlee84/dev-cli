# TODO

Priority `P1` (next) … `P4` (someday); effort `S` / `M` / `L`.

## Active

### P2 · M — Incremental local repo snapshot
The TUI now renders immediately and probes repositories in the background with
bounded concurrency, reducing perceived startup latency. A 56-repo refresh
still takes roughly three seconds because status + latest commit remain live Git
queries. If inventories grow into the hundreds, persist a short-lived snapshot
and stream changed rows into the model; keep `r` as the explicit live refresh.

### P2 · M — Multi-host aggregation
`dev ls --all` fanning out over ssh to the machines in a configured host list,
merging their `dev ls --json`. The JSON contract is already stable and the
`owner` field already identifies which machine holds a task, so this is
plumbing rather than design. Today the same thing works by hand:

```bash
ssh jingle-235 dev ls --json | jq '.[] | select(.state=="hot")'
```

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
Anything derivable from git is derived live, every run. The registry holds
state, owner, next action and a note — nothing else. The moment it caches a
branch name or an ahead/behind count, it can disagree with git, and a tool that
disagrees with git about git is worse than no tool.

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

- Task lifecycle: `start` / `park` / `resume` / `done` / `sweep`.
- Worktree ownership rule, path templates, provisioning, per-repo `.dev.toml`.
- Runtime adapters: herdr, tmux, none, behind one contract suite.
- Repo discovery and gh/glab-backed clone, create, sync.
- `try` and `graduate`.
- Activity stats: sampler, git backfill, WakaTime import, heatmap.
- Interactive TASKS / REPOS / REMOTE dashboard with vim navigation, lazy
  gh/glab inventory, private XDG cache, local-clone matching and confirmed clone.
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
- Latest repo activity from dirty-file mtime, commit, or task update; configurable
  REPOS columns and activity/latest/name/git/tasks sorting.
- Immediate TUI render with bounded-parallel repo probes (serial path measured
  4.2s); background rows replace the initial loading state.
- Explicit Herdr/tmux LIVE repo status and selected-repo heatmap overlay in TUI,
  including `b` single-repo backfill.
- XDG `stats path/clear` and regenerable `cache list/path/clear` with stats data
  deliberately kept out of cache semantics.
- Bundled agent skill with a generated, drift-checked command reference.
