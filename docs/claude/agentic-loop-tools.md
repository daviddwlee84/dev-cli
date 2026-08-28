---
description: Understand Claude Code as an agentic harness around a model, with tools, context, permissions, sessions, and verification loops.
authority: anthropic-docs
status: official
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
---

# Agentic harness and tools

Claude Code is the **agentic harness** around a Claude model: the model reasons, while the harness supplies tools, context management, execution environments, permissions, sessions, and orchestration.

!!! info "Public behavior, not proprietary internals"
    This page documents the architecture and contracts Anthropic publishes. It does not infer model internals or undocumented implementation details.

## The agentic loop

```text
your goal
   │
   ▼
gather context ──► take action ──► verify results
      ▲                                  │
      └──────── learn and repeat ◄───────┘
```

The phases blend together. Reading a question may need only context gathering; a bug fix usually runs tests, reads/searches, edits, and tests again. Every tool result becomes context for the next model decision. The user can interrupt and steer the loop.

## Model versus harness

| Component | Responsibility |
|---|---|
| model | understand the prompt/code, reason, choose the next response or tool call |
| harness | provide tool schemas, execute approved calls, return results, manage context/session/environment |
| project instructions | persistent conventions and boundaries such as `CLAUDE.md`, skills, and settings |
| user/permission system | approve, deny, interrupt, or redirect actions |

A text-only model cannot edit code or run a test. Agency appears when the harness executes tools and feeds their results back through the loop.

## Tool categories

| Category | Typical tools | Role |
|---|---|---|
| files | `Read`, `Edit`, `Write`, `NotebookEdit` | inspect or change artifacts |
| search/intelligence | `Glob`, `Grep`, `LSP` | discover code and relationships |
| execution | `Bash`, `PowerShell`, `Monitor` | run builds, tests, Git, and long-lived observation |
| web/resources | `WebSearch`, `WebFetch`, MCP resource tools | acquire external context |
| orchestration | `Agent`, `Workflow`, `SendMessage` | delegate or coordinate independent contexts |
| work tracking | `TaskCreate`, `TaskGet`, `TaskList`, `TaskUpdate` | metadata and dependencies; creating a task does not start a worker |
| session/control | `AskUserQuestion`, plan/worktree/scheduling tools | change harness state or request a human decision |
| reusable procedures | `Skill` | load instructions and supporting material on demand |

Availability varies by surface, model, organization policy, plugin, and session configuration. Use the live tools reference as the authority for exact schemas.

## Names that are easy to confuse

- **`Agent`:** starts a subagent; the older tool name `Task` remains in historical material.
- **Task-list tools:** create coordination records only.
- **`/tasks` and `TaskStop`:** inspect or stop background work, which may be a command or agent.
- **`Workflow`:** runs a Dynamic Workflow script that orchestrates many agents.
- **`Skill`:** loads a reusable prompt/procedure into the current conversation.
- **`EnterWorktree`:** changes checkout isolation; it does not itself create another agent.

## Context and sessions

A session combines the system prompt, conversation, tool definitions/results, project instructions, loaded skills, and configured extensions. Tool output consumes context, so subagents are useful when only a concise result should return to the main conversation. Automatic compaction summarizes older context; durable rules belong in project instructions, not only in an early prompt.

Sessions are independent and tied to directories. Resume continues one session ID; fork creates a new session/history branch. A Git worktree can give each parallel session an independent checkout while repository history remains shared.

## Safety controls

- **Permissions** decide whether a tool call may run. Allow/deny rules and permission modes are the hard policy boundary.
- **Checkpoints** can rewind file edits, but not remote APIs, deployments, or database effects.
- **Hooks** observe or automate lifecycle events and can block selected calls, but do not replace permissions or sandboxing.
- **Worktree enforcement** can prevent an isolated session from editing or redirecting Git into the protected main checkout.
- **Verification** is part of the loop: a changed file without relevant tests or inspection is not a completed engineering result.

## A reliable harness contract

When embedding or operating a coding agent, define:

1. goal and acceptance criteria;
2. trusted context and freshness;
3. available/read-only/mutating tools;
4. permission and outward-action gates;
5. filesystem, branch, and external-resource isolation;
6. turn, cost, and concurrency limits;
7. verification commands and evidence;
8. session persistence, handoff, and cleanup ownership.

## Sources

- [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works)
- [Tools reference](https://code.claude.com/docs/en/tools-reference)
- [Permissions](https://code.claude.com/docs/en/permissions)
- [How the Agent SDK loop works](https://code.claude.com/docs/en/agent-sdk/agent-loop)
