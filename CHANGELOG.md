# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `dev summary` machine-wide Markdown/JSON snapshots with adaptive detail,
  attention filtering, recent commits, runtime controls and optional sizes.
- Agent-safe retirement: `dev retire` closes covering runtime sessions and removes a linked worktree
  only from outside it, refusing active agents, mixed-purpose workspaces, dirty state, and in-progress
  Git operations; `dev sweep --merged-worktrees` reports and retires task-tracked and unmanaged
  worktrees whose branches are contained in the base.
- `dev prepare` and `dev artifact` arm and finalize exact agent transcripts after their writer exits,
  and `dev git uncommit/recommit/pull-rebase/amend-all/setup` add guarded Git transactions.
- `dev done --merged --base-ref <ref>` verifies a branch merged outside dev, with `--confirm-squash`
  as the explicit operator attestation for squash merges.
- An agent skill manager: `dev skill list/add/update` inventories project and global agent skills
  with their agents, sources, and update state, backed by a SKILLS dashboard view and a `dev doctor`
  check for the skill provider.
- A remote repository fleet: `dev fleet list/status/sync/open` and `dev fleet config` inventory
  repositories, tasks, and live runtime state across SSH-reachable machines running their own `dev`,
  with a FLEET dashboard view, per-host degradation states, and a regenerable `fleet` cache.
- An interactive `dev done` finish wizard that analyzes a dirty checkout against the base and offers
  commit, discard, or cancel, plus a `--dirty auto|fail|commit|discard` policy with `-m/--message`
  and `-y/--yes` for non-interactive callers.
- Semantic color for human-readable output, controlled by a global `--color auto|always|never` and
  disabled automatically for `NO_COLOR`, `TERM=dumb`, non-TTY writers, and `--json`.
- Catalog-backed repository quick notes with durable Markdown storage, rebuildable full-text search, CLI commands, and TUI add/browse workflows.
- `dev journal` Markdown/JSON reports over calendar-day ranges, with author,
  repository, granularity, truncation and optional Git diff metrics controls.
- Fully paginated forge inventory with visibility filtering and a versioned,
  cache-first stale-while-revalidate REMOTE experience.
- Azure DevOps Services repository inventory, search, cloning, and opt-in forge configuration.
- An interactive, context-aware `dev start` wizard for managed task creation.
- Runtime session activation so opened or reused task surfaces can be focused or attached.
- Exact-pane, fail-closed safeguards for launching parallel agents in newly created Herdr worktrees.
- Agent-ready repository context output, TUI copy actions, and an expanded worktree tree with per-checkout state.
- A bilingual English/Traditional Chinese MkDocs knowledge site with strict source/site checks and GitHub Pages deployment.
- Shared ASCII workflow orientation in `dev --help` and `dev help`, plus bilingual Mermaid diagrams for the full change-stream loop and lifecycle states.

### Fixed

- `dev done` no longer closes the runtime, removes the worktree, or deletes the branch; it records
  MERGED and hands cleanup to `dev retire`, so a process can never delete the worktree it is running in.
- Activity sampling now attributes sessions running in linked worktrees outside
  the canonical repository path.
- Focused starts now activate their runtime session after the interactive wizard completes.
- Corrected command/config documentation to remove the unsupported `dev wt plan --json` example, include Azure forge configuration, and describe shell navigation without implying ordinary command output is evaluated.
- Corrected bundled skill reference coverage and agent-rule numbering.

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

[Unreleased]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/daviddwlee84/dev-cli/releases/tag/v0.1.0
