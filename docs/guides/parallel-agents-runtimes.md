---
description: Coordinate several coding agents around change-stream ownership, explicit mutation boundaries, and replaceable runtimes.
authority: project-policy
status: stable
verified_on: 2026-08-28
---

# Parallel agents and runtimes

> **Worktree per change stream. Pane per cooperating agent.**

Do not create one worktree merely because another agent exists. Create one when another independent writer can mutate overlapping or unknown state.

## Decide whether agents can share

| Parallel work | Same checkout? | Condition |
|---|---|---|
| researcher plus implementer | yes | researcher is read-only |
| frontend plus backend | usually | ownership of shared schemas/manifests is explicit |
| tests plus implementation | usually | one integration owner resolves interface changes |
| two known disjoint directories | yes | no shared codegen/formatter output |
| both edit one manifest/API | risky | serialize or split streams |
| competing implementations | no | separate branch/worktree per approach |
| large refactors or unknown ranges | no | overlap cannot be bounded |
| any worker switches/reset/rebases `HEAD` | no | Git state mutation affects the checkout |

Worktrees isolate files/index/HEAD. They do not isolate ports, databases, caches, hooks, credentials, queues, generated artifacts, or deployment targets; assign those resources separately.

## Topology for one feature

```text
one change stream / branch / dev worktree
                    │
             one runtime workspace
          ┌─────────┼─────────┐
        pane A     pane B     pane C
        agent      agent      tests/review
```

This topology keeps one integration history and avoids merging execution artifacts. Give each writer a path/symbol contract and name the person or agent responsible for the final combined test.

Inside a `dev` worktree, launch agents in the existing checkout. Do not automatically ask Claude Code to create another nested worktree; the outer worktree already provides the mutation boundary.

## Topology for competing or independent work

```bash
dev wt create exp/jwt --base main
dev wt create exp/session --base main
dev wt create exp/oauth --base main
```

Each approach gets its own branch and worktree. Compare results, integrate one deliberately, and remove the others only after their useful commits or notes are recoverable.

## Runtime selection

`runtime.Select("auto")` chooses:

1. **Herdr** when its binary and server are available. It can present linked worktrees as first-class grouped workspaces and report agent activity/session IDs.
2. **tmux** when installed. It opens a named session rooted at the checkout and stores display metadata in user options.
3. **Zellij** when a compatible installation is available. It opens or focuses a session rooted at the checkout.
4. **none** everywhere else. Core Git/task/worktree behavior remains available, while shell integration changes directories.

A runtime opens and displays a checkout; `dev`, not Herdr or tmux, owns branch/worktree lifecycle. Closing a runtime never deletes the task or checkout.

## Coordination contract

Before launching parallel writers, record:

- the shared goal and acceptance test;
- each worker's files/symbols and forbidden shared surfaces;
- dependencies and when blocked work may start;
- whether a worker may run Git operations that change `HEAD`;
- merge/integration order;
- the integration owner and full verification command;
- cleanup ownership for worktrees, runtimes, ports, containers, and temporary data.

Use a read-only researcher or reviewer when independent context is useful but another writer would add merge cost.

## Safe finish

1. Stop new mutation and collect each worker's summary/tests.
2. Inspect every diff and preserve useful commits.
3. Integrate in dependency order.
4. Run formatting/codegen once under one owner.
5. Run the complete relevant suite in the integrated checkout.
6. Park or close runtime sessions.
7. Remove only clean, recoverable worktrees; keep branches until merge status is verified.

For Claude-specific primitive selection, continue with the [parallel work decision guide](../claude/parallel-work-chooser.md).

## Sources

- [`internal/help/topics/agents.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/agents.md)
- [`internal/runtime/runtime.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/runtime.go)
- [`internal/runtime/herdr.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/herdr.go)
- [Claude Code: run agents in parallel](https://code.claude.com/docs/en/agents)
