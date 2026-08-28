---
description: Distinguish branches, worktrees, runtimes, agent contexts, tasks, and the shared state each leaves exposed.
authority: project
status: stable
verified_on: 2026-08-28
---

# Isolation boundaries and glossary

Isolation is layered. A separate worktree does not create a separate repository, process namespace, service stack, or coordination protocol.

## Boundary matrix

| Primitive | Separates | Still shares |
|---|---|---|
| Git branch | named commit history | repository object database, refs namespace, remotes |
| linked worktree | working files, index, checked-out `HEAD` | objects, most refs/config, hooks, external services |
| runtime session | terminal process tree and presentation | checkout, Git state, ports, databases, credentials |
| subagent context | model conversation/context window | checkout unless isolation is requested, process environment |
| agent team | task/message coordination | files by default; teammates are not automatically worktree-isolated |
| Dynamic Workflow | repeatable orchestration and joins | mutation boundaries chosen by the workflow author |
| `dev` task | lifecycle intent and reconstruction metadata | actual Git/runtime facts, which are derived live |

## Core terms

### Change stream

One independent, reviewable line of change. In `dev`, it normally maps to one branch and optionally one worktree. This is the unit that should own durable history.

### Branch

A movable Git ref naming a commit. It isolates history, not working files. Two checkouts must not normally have the same branch checked out simultaneously.

### Worktree

A working directory registered with one Git repository. A linked worktree has its own files, index, and `HEAD`, while sharing repository storage and much administrative state.

### Runtime

The live host environment that presents a checkout: Herdr, tmux, or the current shell. A runtime can disappear without changing task durability.

### Agent, subagent, and teammate

- An **agent** is a coding-agent session or, in Claude Code tool terminology, a worker launched by the `Agent` tool.
- A **subagent** runs delegated work with an independent context inside a parent session.
- A **teammate** is a peer Claude Code session in an experimental agent team, coordinated through a lead, task list, and messages.

These describe context and coordination, not automatic file isolation.

### Task

This word is overloaded:

- `dev task`: a durable project change-stream record.
- `TaskCreate`, `TaskGet`, `TaskList`, `TaskUpdate`: structured coordination records used by a session or team.
- `/tasks` and `TaskStop`: background-work inspection and control.
- old Claude Code `Task` tool: renamed to `Agent`; old references may still use the previous name.

### Workflow

Also overloaded:

- **GitHub Flow:** a branch-and-pull-request collaboration model.
- **common workflow:** an informal repeatable prompt or operating recipe.
- **Dynamic Workflow:** Claude Code's JavaScript orchestration runtime for repeatable multi-agent graphs.

## What worktrees do not isolate

Plan unique values or explicit ownership for resources outside the working directory:

- TCP ports and Unix sockets;
- local databases and test containers;
- caches and generated artifact directories;
- cloud accounts, queues, and deployment environments;
- repository hooks and shared refs;
- formatter/codegen output that several writers can regenerate.

If two workers can mutate one of these, a second worktree alone is not enough.

## Related pages

- [Mental model and lifecycle](mental-model.md)
- [Worktree semantics and recovery](../git/worktree-semantics-recovery.md)
- [Parallel work decision guide](../claude/parallel-work-chooser.md)

## Sources

- [Git worktree documentation](https://git-scm.com/docs/git-worktree)
- [Claude Code tools reference](https://code.claude.com/docs/en/tools-reference)
- [Claude Code agent teams](https://code.claude.com/docs/en/agent-teams)
- [Claude Code Dynamic Workflows](https://code.claude.com/docs/en/workflows)
