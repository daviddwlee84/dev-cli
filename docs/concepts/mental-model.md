---
description: Understand dev-cli as durable Git history, scoped intent and catalog state, repository quick-note sidecars, disposable worktrees, and replaceable runtimes.
authority: project
status: stable
verified_on: 2026-09-01
---

# Mental model and lifecycle

`dev` treats a unit of work as a **change stream** whose durable identity is its Git branch. The checkout and runtime are replaceable projections of that stream.

## Four responsibilities

```text
Git remote + branch  durable history and cross-machine handoff
        ↓
worktree             local files, index, and checked-out HEAD
        ↓
runtime              Herdr workspace, tmux session, or current shell
        ↓
dev registry         human intent and reconstruction metadata
```

Sidecar state has deliberately separate scopes:

- each task stores checkout mode, repository/branch/base, lifecycle state, owner host, next executable action, task context, runtime hint, and timestamps;
- the catalog stores stable repository/Try identity, tags, one metadata summary, experiment lifecycle, and host-local locations;
- configured `paths.state_dir/notes` stores multiple durable Markdown observations keyed by catalog repository ID;
- `$XDG_CACHE_HOME/dev/notes.db` is only the rebuildable full-text index for those Markdown files.

The catalog ID keeps a quick note attached across linked worktrees, symlink indexes, path moves, and synchronized host state. `dev` does not itself synchronize notes or catalog files. Live Git status, ahead/behind, and runtime availability are derived again instead of treated as authoritative cached truth.

## The four lifecycle states

| State | Git and checkout | Runtime | Meaning |
|---|---|---|---|
| 🔥 `hot` | branch plus checkout | open | active now |
| 🌤 `warm` | branch plus checkout kept | closed | likely to return within days |
| ❄️ `cold` | committed and pushed branch; worktree absent | none | paused and reconstructible elsewhere |
| ✅ `done` | integrated; branch/checkout may remain | may remain open | MERGED, pending explicit retirement |

```mermaid
flowchart TD
    accTitle: dev-cli lifecycle states
    accDescr: Tasks move between HOT, WARM, and eligible COLD states; direct or verified integrated work reaches DONE while resources remain; review leaves the current state unchanged; and external retirement reaps only an already completed entry.

    Start["dev start"] --> Hot["HOT"]
    Hot -->|dev park --next| Warm["WARM"]
    Warm -->|dev resume| Hot
    Hot -->|branch/worktree: dev park --cold --push| Cold["COLD"]
    Warm -->|branch/worktree: dev park --cold --push| Cold
    Cold -->|dev resume --fetch| Hot

    Hot -->|direct: dev done| Done["DONE"]
    Warm -->|direct: dev done| Done
    Hot -->|branch/worktree: dev done --ff| Done
    Warm -->|branch/worktree: dev done --ff| Done

    Hot -.->|branch/worktree: dev done --pr; state unchanged| Review["push / review handoff"]
    Warm -.->|branch/worktree: dev done --pr; state unchanged| Review
    Review -.->|feedback: dev resume if WARM| Hot
    Review -->|dev done --merged --base-ref REF| Done

    Done -->|dev retire from outside target| Reaped["RETIRED: resources cleaned; entry reaped"]
    Done -.->|dev sweep: report candidate| Report["cleanup report"]
    Report -.->|approved apply| Reaped
```

For branch/worktree tasks, `dev done --pr` is intentionally not a HOT/WARM → DONE transition. It hands off a pushed branch (opening review when supported) and leaves state and cleanup unchanged. `dev flow` can manually query portable review existence/state/draft/URL evidence, but never infers DONE. After an external merge, use `dev done --merged --base-ref <ref>` or Flow's Verify Merged action to prove named ancestry explicitly before a separate Retire plan performs cleanup.

`dev flow [repo]` makes the underlying hybrid model visible: persisted intent,
typed live observations, and a revision-bound guarded plan are distinct. Errors,
unknowns, and stale facts do not become false; Apply revalidates exact task/Git/
worktree/runtime/artifact identity before mutation. See [Repository lifecycle
flow](../guides/repository-flow.md).

## Checkout modes

A task records intent; it does not always require a linked worktree.

| Mode | Use | Lifecycle limit |
|---|---|---|
| direct | one small change in the canonical checkout | HOT ↔ WARM; canonical checkout cannot be removed |
| branch-only | isolated history without a second directory | can go COLD after push and switching the canonical checkout away |
| worktree | independent mutation, parallel streams, durable feature work | full lifecycle |

The mode is selected by collision and recovery needs, not by ceremony.

## Runtime is deliberately lossy

`runtime.Runtime` exposes `Open`, `Close`, `List`, and `Annotate`. `auto` selects Herdr when available, then tmux, then Zellij, then the always-available `none` backend. Closing any backend must not remove the checkout, branch, or task entry.

`none` means no observable multiplexer backend; it does not prove that no session or agent exists. Guarded actions that require occupancy or absence proof retain this as unobserved evidence. `dev flow` never presents it as known closed and does not offer the expert `--assume-no-runtime` override.

This separation is why rebooting, closing a multiplexer, or changing runtime backend does not abandon work. `dev sweep` can compare the registry with live Git/runtime facts and report drift.

## Cross-machine handoff

The branch is the transport boundary:

```bash
# machine A
dev park --cold --push

# machine B
dev resume <task> --fetch
```

One writer owns a branch at a time. If two machines or agents need to mutate the same feature concurrently, split the change stream and integrate afterwards.

## Sources

- [`internal/task/task.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/task/task.go)
- [`internal/catalog/catalog.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/catalog/catalog.go)
- [`internal/note/note.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/note.go)
- [`internal/runtime/runtime.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/runtime.go)
- [`internal/help/topics/parking.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/parking.md)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/taskflow`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/taskflow)
- [`internal/flowtui`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/flowtui)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
