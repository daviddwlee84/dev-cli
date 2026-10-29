# Shared repository navigation and task lifecycle trial

Status: research, prototype and repeatable trial; no new task backend or task
command deprecation. Prototype reviewed 2026-09-25 against Sidecar 1.14.0 and
td 0.65.0. User feedback recorded 2026-10-29; the formal two-route trial has not
been performed.

## Intent

The primary workflow remains Herdr. The original reason for dev task tracking
was easier cleanup after work, but requiring start/park/done state management
adds friction when the useful endpoint is simply a tidy local worktree/branch
list. Evaluate whether Git evidence and explicit external cleanup suffice.

REPOS remains useful independently: supply one local candidate source to
Sidecar, Neovim and optional terminal pickers, while each tool keeps its own
preferences, sessions and genuine recent-use history.

## User feedback — 2026-10-29

The maintainer reports that dev tasks are rarely used, and td/Sidecar is rarely
opened in projects where it has been tried. Storing more work items or adding
another board therefore does not by itself make the work easier to revisit.
This is feedback about current usage, not a completed comparison of the two
isolated worktree routes below.

The target is personal work with multiple agents across devboxes. Prefer a
lightweight entry point where a person can capture a short thought and return
to the few items needing attention. Keep agent suggestions separate from
accepted work, and keep detailed execution steps subordinate to the durable
work item. An issue may span several agent sessions; a session is not its
identity or completion criterion.

The [human–agent work-item design note](../docs/notes/human-agent-work-items.md)
develops this distinction between capture, accepted intent, execution and
human attention. It also compares the roles of Herdr, td/Sidecar, Beads and
hosted trackers without selecting a new dev backend.

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

## Next evaluation sequence

1. Document the work-item model and routing first: preserve short human
   captures, distinguish suggestions from accepted work, and link execution
   context to its authoritative work item without duplicating its backlog.
2. Try native UI entry points and explicit agent recipes in the existing
   workflow. Record whether the maintainer actually revisits them, can find
   the next decision, and can resume the same intent from another session or
   devbox. Keep this work-item trial distinct from the two-route worktree
   lifecycle trial.
3. Propose the smallest dev capability only for friction observed in those
   trials. A backend adapter, another dashboard or a task-model change is not
   a prerequisite. Preserve existing task records, lifecycle safety and
   structured-output compatibility.

This sequence does not introduce a new tracker backend, automatically sync
work items, or deprecate task commands. td/Sidecar and Beads remain optional
tools to evaluate, not required dependencies.

## Acceptance and decision

An open PR remains active. After exact branch commits are contained in main,
the user may approve local worktree and branch cleanup. Squash merge requires
separate evidence. Review remaining transcript diffs after writer exit; neither
their path nor an agent/td completion status is discard authority. Dirty
products, incomplete observation and changed plan authority remain blockers.

The worktree trial records navigation/setup effort, repeated metadata,
confirmation steps, ability to resume, branch-list clarity and reclaimed disk
space. Keep its formal feedback checklist blank until a person actually uses
both workflows; the 2026-10-29 usage report is not a passing trial result.

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
