---
description: Generate a current machine-wide snapshot of repositories, Tries, tasks, worktrees, runtimes, and recovery risks.
authority: project
status: stable
verified_on: 2026-08-28
---

# Machine summary

`dev summary` answers “what is on this machine right now?” It emits Markdown by
default, does not invoke an AI agent, and does not persist a second copy of Git
or runtime state.

```bash
dev summary
dev summary --attention
dev summary --detail compact --no-runtime
dev summary --recent-commits 3 --sizes
dev summary --json
```

The default `auto` detail expands projects with dirty/conflicted checkouts,
live sessions, or HOT/WARM tasks. Quiet repositories and Tries remain in a
compact index with branch, Git status, recent activity, recovery risk, and the
latest commit subject.

`--attention` selects active work plus topology errors, missing/prunable
checkouts, repositories without remotes, and repositories with local-only
branches. Auto detail expands every selected project. `--detail full` expands
all projects; explicit `--detail compact` keeps all projects to one line.

Present active/deprecated Tries are included alongside durable repositories,
including non-Git experiments with the smaller set of facts they support.
`--include-history` adds archived, evicted, and graduated records. Remote-only
repositories that have not been cloned are deliberately outside this local
machine snapshot.

## Structured output and performance

`--json` always emits the complete selected snapshot regardless of Markdown
detail. Schema version 1 includes collection capabilities, aggregate counts,
project identity, current Git/recovery facts, checkouts, tasks, sessions,
recent commits, optional size, attention reasons, and warnings.

Runtime sessions are queried once and shared by repository and Try collection.
`--no-runtime` avoids that query and marks `runtime_collected` false, so an
empty session list is not mistaken for a completed observation. Size scanning
is opt-in and uses the existing disk-usage cache.

Use the three context surfaces according to the question:

```text
dev summary       current machine-wide snapshot
dev journal       activity within a calendar-day range
dev repo context  full context for one repository
```
