# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- The dashboard's FLEET view now hides this machine by default because REPOS
  already provides its richer local inventory. Press `a` to include local rows;
  the non-interactive `dev fleet list` contract still includes this machine.

### Fixed

- REMOTE keeps the top navigation bar visible while cached repositories refresh;
  its extra refresh banner is now included in the terminal-height budget.
- TASKS no longer opens a stale artifact directory or the canonical checkout for
  an unavailable worktree task. Missing/unregistered worktrees point to
  `dev sweep`, while a normal COLD worktree task points to explicit `dev resume`.

## [0.2.1] - 2026-08-30

### Added

- `dev repo new` now has a confirmed interactive repository-bootstrap wizard, with
  `dev repo create` as an alias; `dev repo clone` can optionally apply the same setup,
  and `dev repo setup` safely merges preset files into an existing clean checkout.
  A clear Git URL, local Git path, or owner/repository argument to `new`/`create` is
  acquired as a clone, preserving its history and remote. The no-argument wizard
  detects the same references. Its default-no “Customize preset and template options?”
  gate keeps the normal `agent-ready` path short while still exposing every detailed
  question on request.
  Presets live in versioned `scaffolds.toml`, support typed inputs, safe templates,
  phased hooks, project skills with setup entrypoints, optional GitHub/GitLab
  publication, and explicit stay/cd/runtime/start handoffs. The two recommended
  agent skills use reviewed built-in project initializers instead of executing
  newly downloaded setup code. Skills with the same source and agent targets are
  installed in one provider invocation; the history initializer also adds
  `.specstory/.gitignore` rules for machine-local identity/statistics while keeping
  session history trackable; existing custom content is preserved and missing managed
  rules are appended.
- `dev repo new` can seed a new Git history from a safe snapshot of a local directory,
  local repository, Git URL, or owner/repository reference. `--template-ref` selects a
  branch, tag, or commit and `--template-subdir` selects one confined folder; preset
  `template`, `template_ref`, and `template_subdir` fields support catalog repositories
  containing several starters. Source `.git` metadata is excluded, and symlinks,
  special files, and path traversal are rejected. Plans redact URL userinfo, preview
  selected paths, and warn when a local live snapshot is not pinned to a commit.
- Repository bootstrap now has `--check-in <auto|commit|stage|none>`. `stage` leaves
  generated changes staged for review and seeds lazygit's worktree-local pending
  message as a best-effort convenience for lowercase `c`; staged setup cannot be
  published, and the `start` handoff still requires committed setup. Presets can set
  `initial_check_in`, and project setup defaults can set `repo.setup.check_in`. A draft
  write failure is a warning after staging succeeds; it never rolls the index back.
- TTY text prompts in the repository, task-start, and finish wizards now use an inline
  editor with cursor movement, Home/End, Delete/Backspace, and Esc/Ctrl-C cancellation
  instead of inserting raw arrow-key escape sequences. Buffered non-TTY input retains
  its script/test behavior.
- Repositories may commit allowlisted `.dev-cli/config.toml` and
  `.dev-cli/scaffolds.toml` overrides. Host paths/runtime/state remain global, while
  executable project configuration is content-hash trusted locally before it can run;
  legacy `.dev.toml` worktree settings remain compatible.

- Releases now publish `windows/amd64` and `windows/arm64` `.zip` archives alongside the
  Unix `.tar.gz` set, all covered by `SHA256SUMS`. `dev` builds and runs on Windows; core
  repository, task and worktree commands work. There is no tmux, Zellij or Herdr there, so
  the runtime backend is always `none`, and `dev fleet open` starts a child shell rather than
  replacing the process. Shell integration is `dev shell-init powershell`, which passes the
  directory back through a `DEV_SHELL_CD_FILE` temp file since Windows cannot inherit
  file descriptor 3. CI now includes `windows-latest` (build, `go vet` and `skill sync --check`
  are enforced; the domain test suites run advisory-only until each POSIX assumption is guarded).
- `dev upgrade` downloads the newest release for this platform, verifies it against the
  release `SHA256SUMS`, and replaces the running binary with an atomic rename (Windows moves
  the live `.exe` aside and sweeps it on the next run). It defers to Homebrew, Scoop or
  `go install` when one of them owns the file. `dev upgrade --check` reports without changing
  anything.
- An interactive `dev` command prints one dim "newer release available" line at most once a
  day, read only from the day-old release cache — it never blocks on the network, and a
  best-effort background refresh keeps that cache warm. `[update] check = false` in
  `config.toml` (or `DEV_NO_UPDATE_CHECK`) disables it. `dev version --check` gained a
  `dev upgrade` hint and a `scoop update` hint on Windows.
- A Scoop manifest (`packaging/scoop/dev-cli.json`) with `checkver`/`autoupdate`. The release
  workflow refreshes its version and hashes, attaches it to the GitHub release, and pushes it
  to the bucket repo when `SCOOP_BUCKET_TOKEN` is configured.

## [0.2.0] - 2026-08-29

### Added

- Each command family's help carries an ASCII orientation diagram, and every command whose
  workflow has a quick-reference page ends its help with `See also: dev help <topic>`.
  `dev help <command>` now resolves a command name or alias to its topic, so `dev help wt`
  reaches the worktrees page instead of failing.
- `dev help tries` and `dev help skills` quick-reference pages.
- `dev version`, reporting whether this build is a published release or some number of commits
  past one, with an opt-in `--check` that asks GitHub for the newest release and caches the answer
  for a day. `dev doctor` reports the running version too; neither `dev --version` nor `dev doctor`
  touches the network.
- Releases publish `darwin/arm64`, `darwin/amd64`, `linux/amd64` and `linux/arm64` archives with a
  `SHA256SUMS` file, and take their notes from the matching `CHANGELOG.md` section. Previously a
  release published nothing but a GitHub release object with generated notes, so the only way to
  install `dev` was to build it.
- `e` in the dashboard's FLEET view edits `remotes.toml` — the file that view is about — instead of
  dev's own `config.toml`, reparsing it on return and refreshing the fleet only when it is valid.
- Semantic color now covers every human-readable surface. `dev fleet`, `dev skill`, `dev note`,
  `dev artifact`, `dev retire`, `dev park`, `dev resume`, `dev wt` and the `dev sweep` report were
  rendered entirely without it; fleet host state, skill update state and artifact intent state each
  gained a colorizer alongside the existing Git-status and task-state ones.
- `dev journal` and `dev summary` style their Markdown the way `dev help <topic>` does, and command
  help colors command names and flag specs rather than only its section headings.
- `--color` reaches the interactive dashboard, so `dev --color never` (and `NO_COLOR`, and
  `TERM=dumb`) render it without color.
- `dev sweep` reaps a task whose repository directory is gone. Such a record was unreachable by
  every command in the binary, because `done`, `resume`, `park` and `retire` all resolve the
  repository first, and neither existing reap rule covered it. A live runtime session rules the
  suggestion out, and reaping drops only dev's record of intent.
- `dev sweep` reports a task-recorded checkout that Git does not register and that holds nothing but
  agent artifact directories — the residue of a worktree removed while its transcript writer was
  still running. It offers removal only once every file inside is byte-identical to one the
  repository already has; anything else is reported as salvage work and never removed.
- `dev sweep` acts on a cold task whose worktree is still on disk, drift inventory has always
  computed for `dev ls` and the dashboard but sweep never consulted.

### Fixed

- An unknown command is reported instead of discarded. `dev` sets cobra's `SilenceErrors` and
  then also skipped printing any error beginning with `unknown command`, so `dev bogus` wrote
  nothing to either stream and exited 1. The message, cobra's suggestions, and a pointer to
  `--help` are now printed.
- A stray argument to a command family is an error. `dev wt bogus` printed `dev wt` help and
  exited 0, silently dropping the argument, because a family node has no `Run` of its own.
- Argument-count and flag errors print the failing command's usage block instead of one bare line.
- Removed the unparsed YAML frontmatter from the `retirement` help topic; `dev help` has no
  frontmatter handling, so those keys never affected anything.
- `dev retire <path>` reaps the matching task record. Only the by-task form set the task identity,
  so retiring the same checkout by path left the record behind.

## [0.1.11] - 2026-08-29

### Added

- `dev sweep` detects a branch-backed task whose branch Git no longer has — unfinishable by `done`,
  `resume`, or `retire` — and offers to reap the record.
- `dev artifact discard <intent> --yes` records that an armed handoff can never be finalized, so an
  intent whose transcript was never written, or whose HEAD is gone after a rebase, stops blocking
  integration and retirement. It refuses an intent that is still armed.

### Fixed

- Retirement no longer treats the `REBASE_HEAD` file Git leaves behind after a *completed* rebase as
  an in-progress operation, which had permanently blocked `dev retire` and
  `dev sweep --merged-worktrees` for any worktree that had ever been rebased.
- Development builds derive `--version` only from `vMAJOR.MINOR.PATCH` tags, so an unrelated tag in
  the repository can no longer be reported as the CLI version.

## [0.1.10] - 2026-08-29

### Added

- `dev journal` Markdown/JSON reports over calendar-day ranges, with author,
  repository, granularity, truncation and optional Git diff metrics controls.
- `dev summary` machine-wide Markdown/JSON snapshots with adaptive detail,
  attention filtering, recent commits, runtime controls and optional sizes.
- Fully paginated forge inventory with visibility filtering and a versioned,
  cache-first stale-while-revalidate REMOTE experience.

### Fixed

- Activity sampling now attributes sessions running in linked worktrees outside
  the canonical repository path.

## [0.1.9] - 2026-08-29

### Added

- Agent-safe retirement: `dev retire` closes covering runtime sessions and removes a linked worktree
  only from outside it, refusing active agents, mixed-purpose workspaces, dirty state, and in-progress
  Git operations; `dev sweep --merged-worktrees` reports and retires task-tracked and unmanaged
  worktrees whose branches are contained in the base.
- `dev prepare` and `dev artifact` arm and finalize exact agent transcripts after their writer exits,
  and `dev git uncommit/recommit/pull-rebase/amend-all/setup` add guarded Git transactions.
- `dev done --merged --base-ref <ref>` verifies a branch merged outside dev, with `--confirm-squash`
  as the explicit operator attestation for squash merges.

### Fixed

- `dev done` no longer closes the runtime, removes the worktree, or deletes the branch; it records
  MERGED and hands cleanup to `dev retire`, so a process can never delete the worktree it is running in.

## [0.1.8] - 2026-08-29

### Added

- An agent skill manager: `dev skill list/add/update` inventories project and global agent skills
  with their agents, sources, and update state, backed by a SKILLS dashboard view and a `dev doctor`
  check for the skill provider.

## [0.1.7] - 2026-08-29

### Added

- A remote repository fleet: `dev fleet list/status/sync/open` and `dev fleet config` inventory
  repositories, tasks, and live runtime state across SSH-reachable machines running their own `dev`,
  with a FLEET dashboard view, per-host degradation states, and a regenerable `fleet` cache.

## [0.1.6] - 2026-08-29

### Added

- An interactive `dev done` finish wizard that analyzes a dirty checkout against the base and offers
  commit, discard, or cancel, plus a `--dirty auto|fail|commit|discard` policy with `-m/--message`
  and `-y/--yes` for non-interactive callers.
- Semantic color for human-readable output, controlled by a global `--color auto|always|never` and
  disabled automatically for `NO_COLOR`, `TERM=dumb`, non-TTY writers, and `--json`.

## [0.1.5] - 2026-08-28

### Added

- Catalog-backed repository quick notes with durable Markdown storage, rebuildable full-text search, CLI commands, and TUI add/browse workflows.

## [0.1.4] - 2026-08-28

### Added

- Shared ASCII workflow orientation in `dev --help` and `dev help`, plus bilingual Mermaid diagrams for the full change-stream loop and lifecycle states.

## [0.1.3] - 2026-08-28

### Added

- Azure DevOps Services repository inventory, search, cloning, and opt-in forge configuration.

### Fixed

- Corrected command/config documentation to remove the unsupported `dev wt plan --json` example, include Azure forge configuration, and describe shell navigation without implying ordinary command output is evaluated.
- Corrected bundled skill reference coverage and agent-rule numbering.

## [0.1.2] - 2026-08-28

### Added

- A bilingual English/Traditional Chinese MkDocs knowledge site with strict source/site checks and GitHub Pages deployment.

## [0.1.1] - 2026-08-28

### Added

- Agent-ready repository context output, TUI copy actions, and an expanded worktree tree with per-checkout state.
- Exact-pane, fail-closed safeguards for launching parallel agents in newly created Herdr worktrees.
- An interactive, context-aware `dev start` wizard for managed task creation.
- Runtime session activation so opened or reused task surfaces can be focused or attached.

### Fixed

- Focused starts now activate their runtime session after the interactive wizard completes.

## [0.1.0] - 2026-08-28

### Added

- The HOT/WARM/COLD/DONE task lifecycle with direct, branch-only, and managed-worktree checkout modes.
- A TASKS/REPOS/TRY/REMOTE terminal dashboard backed by the same services as the non-interactive CLI.
- Try catalog identity, personal metadata, archive/restore, graduation, and guarded filesystem transitions.
- Rich Git inventory for upstream divergence, conflicts, staged, unstaged, and untracked paths.
- Managed worktree placement and provisioning with explicit ignored-file handling and per-ecosystem dependency strategies.
- Repository discovery, remote-risk inspection, logical size accounting, and durable activity statistics.
- Recursive machine bootstrap with non-destructive symlink indexing and guarded physical move plans.
- Adoption of existing worktrees, runtime sessions, and unmerged branches into the task registry.
- Managed `.gitignore` generation from GitHub templates plus project-local sections.
- XDG-based configurable repository, worktree, task, catalog, cache, and runtime layouts.
- An embedded and installable `dev-cli` agent skill with a generated command reference checked in CI.
- Homebrew, Go, and source installation paths, shell completion, and directory-changing shell integration.

<!-- 0.1.1 through 0.1.11 were tagged retroactively on 2026-08-29 from an already-linear history:
     the features had landed on main one branch at a time but were never released. Each tag sits on
     that feature's last commit, so the CHANGELOG at those commits still lists everything under
     [Unreleased]; this file at HEAD is the accurate record. -->

[Unreleased]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.11...v0.2.0
[0.1.11]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.10...v0.1.11
[0.1.10]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.9...v0.1.10
[0.1.9]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.8...v0.1.9
[0.1.8]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.7...v0.1.8
[0.1.7]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/daviddwlee84/dev-cli/releases/tag/v0.1.0
