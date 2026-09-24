# Shared repository navigation and task lifecycle trial

Status: prototype and repeatable trial; no task command deprecation. Reviewed
2026-09-25 against Sidecar 1.14.0 and td 0.65.0. Human preference remains untested.

## Intent

The primary workflow remains Herdr. The original reason for dev task tracking
was easier cleanup after work, but requiring start/park/done state management
adds friction when the useful endpoint is simply a tidy local worktree/branch
list. Evaluate whether Git evidence and explicit external cleanup suffice.

REPOS remains useful independently: supply one local candidate source to
Sidecar, Neovim and optional terminal pickers, while each tool keeps its own
preferences, sessions and genuine recent-use history.

## Implemented boundary

- Public source remains `dev repo list --json`; no new Go API, catalog database
  or automatic task/td synchronization.
- The optional dotfiles `dev-projects` helper supplies candidates, validated
  launches and explicit add-only Sidecar imports. `:DevRepositories` and
  `tv dev-repos` are separate views, leaving native Projects intact.
- Sidecar native Projects has no dynamic external provider. It can open a path
  without registration; registration only adds selected missing entries and
  does not mirror removals, names, paths or runtime state.
- The [repeatable trial](../contrib/worktree-trial/README.md) generates two
  independent small Go projects, a local bare origin, isolated settings and a
  feedback checklist. One route is Herdr/dev without task records; the other
  is Sidecar-owned workspace lifecycle. Each checkout has one mutation owner.

## Acceptance and decision

An open PR remains active. After exact branch commits are contained in main,
the user may approve local worktree and branch cleanup. Squash merge requires
separate evidence. Review remaining transcript diffs after writer exit; neither
their path nor an agent/td completion status is discard authority. Dirty
products, incomplete observation and changed plan authority remain blockers.

The trial records navigation/setup effort, repeated metadata, confirmation
steps, ability to resume, branch-list clarity and reclaimed disk space. Keep
human feedback blank until a person actually uses both workflows.

If task-free work covers the real workflow, move managed task lifecycle out of
the recommended entry path before considering formal deprecation. Preserve
legacy task ownership, safe cleanup services, feedback-repair dependencies and
public JSON compatibility. Do not delete `taskflow` merely because its name
includes task: unmanaged removal and triage still use it.

## Deferred

- A lightweight public candidate projection only if measured full-inventory
  latency warrants it; keep the existing discovery authority.
  One local prototype run through the installed dev v0.3.0 returned 83 candidates
  in 9.17 seconds. The asynchronous picker stays cancellable; this single sample
  is a latency follow-up signal, not a benchmark or justification for a new cache.
- Optional sesh generated import fragment; no zoxide/history DB synchronization.
- Sidecar rename/move/remove synchronization would require ownership proof and
  a stronger native write API; current CLI lacks cross-process CAS.
- Worktrunk and Workmux are already configured in the dotfiles and overlap
  with worktree lifecycle. Avoid adding a third full trial before comparing the
  two selected routes.
- Precise reviewed transcript-tail disposal and smooth post-writer cleanup
  remain a product gap. The prototype does not turn all-dirty discard into a
  selective per-file operation.

## Research corrections

Td 0.65 supports remote issue/handoff sync and worktree-scoped session state;
trusted review policy is not universally different-session-only. Its reads can
migrate the DB or auto-sync, so do not invoke td during ordinary local REPOS
refresh. Sidecar has real guarded worktree CLI operations; the reason not to
replace dev blindly is its distinct ownership/runtime semantics, not absence
of a headless API. See the updated dotfiles td/Sidecar and shared-navigation
guides for pinned upstream sources.
