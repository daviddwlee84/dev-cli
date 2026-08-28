---
description: Compare delegated subagents inside one conversation with human-managed background sessions in Agent view.
authority: anthropic-docs
status: research-preview-partial
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
---

# Subagents and Agent view

Subagents let the main conversation delegate a focused task and receive a result. Agent view lets a human dispatch, monitor, and attach to several independent background sessions.

!!! warning "Agent view is a research preview"
    Its commands, UI, lifecycle, and defaults can change. Keep a manual sessions/worktrees fallback and verify behavior against the installed Claude Code version.

## Compare the models

| Dimension | Subagent | Agent view background session |
|---|---|---|
| owner/coordinator | parent conversation | human using `claude agents` |
| context | independent child context; fork may inherit parent conversation | independent full session |
| result path | summary/result returns to parent | session remains available for human review/attachment |
| communication | parent collects; named agents may message supported peers | human or cross-session messaging |
| lifetime | scoped to delegated work/session | supervised background process and persisted session state |
| file isolation | shared by default; optional `isolation: worktree` | Git repositories use isolated background worktrees by default unless disabled/already isolated |
| best use | research, logs, tests, specialist review | independent backlog tasks or long-running investigations |

## Subagents

A normal subagent starts with its own context, system prompt, tool/model/permission configuration, delegation prompt, and project context. It does not receive the parent's full conversation or previously read tool outputs. A **forked subagent** starts from a copy of the parent conversation when the task depends on that context.

Use subagents to:

- keep high-volume search/log/file output out of the main context;
- run independent research or review in parallel;
- define reusable specialist roles;
- constrain tools, model, or permissions;
- isolate an independent writer in a worktree.

A minimal read-only reviewer:

```markdown
---
name: code-reviewer
description: Reviews a completed change for actionable correctness issues.
tools: Read, Grep, Glob
---

Review the requested diff. Return only evidence-backed findings.
```

Add `isolation: worktree` only when the agent will independently mutate files. A worktree with no changes can be removed automatically; one with work remains until safe cleanup rules allow removal.

Foreground subagents block the parent and pass permission prompts through. Background subagents run concurrently and surface their permission requests to the main session. A parent denial cannot be bypassed through another agent.

## Agent view

Open the research-preview manager:

```bash
claude agents
```

Or dispatch directly:

```bash
claude --bg --name flaky-test-fix "investigate and fix the flaky test"
```

Agent view groups sessions by whether they need input, are working, are ready for review, or are completed. You can peek at output, reply, attach to the full conversation, detach while it continues, stop/respawn, or reopen a previous repository session.

A per-user supervisor hosts background sessions so they can outlive the view or shell. Persistence is not the same as successful work: inspect the transcript/diff and verification before accepting a completed state.

### File isolation

In a Git repository, a dispatched background session normally gets a worktree under `.claude/worktrees/`. Isolation may be skipped when the session is already in a linked worktree, the directory is not Git and no VCS hook exists, or background isolation is explicitly disabled.

Do not disable isolation for several writing sessions unless file ownership is provably disjoint. Non-file resources still need unique ownership even with worktrees.

## Operational checklist

- Give each worker a self-contained prompt, paths, acceptance criteria, and test command.
- State whether it is read-only or may write/commit/push.
- For Agent view, name sessions and use one branch/worktree per independent task.
- Review sessions that need input instead of passively polling every transcript.
- Before cleanup, preserve useful commits and confirm no dirty/untracked work.
- Stop/close background runtimes after review; “completed” is not resource cleanup.

## Sources

- [Create custom subagents](https://code.claude.com/docs/en/sub-agents)
- [Manage multiple agents with Agent view](https://code.claude.com/docs/en/agent-view)
- [Run agents in parallel](https://code.claude.com/docs/en/agents)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
