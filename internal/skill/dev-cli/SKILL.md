---
name: dev-cli
description: Use when coordinating repository setup, task and worktree lifecycle, agent handoffs, SSH hosts, fleet operations, notes, skill or MCP inventory, or feedback for unexpected dev failures.
---

# dev-cli

Git owns durable code; worktrees are local checkouts; runtimes are host-local
sessions; dev records task intent. Choose the boundary the work needs.

## Discover only what you need

Use `dev <command> --help` for current syntax and flags, and `dev help <topic>`
for workflow explanations. `dev --help` lists commands; `dev help` lists topics.
Prefer supported `--json` output for automation. Read the relevant command before
mutation: some actions execute immediately, while others require preview/apply.
Do not preload manuals, repeat `dev --skill`, or run diagnostics for every task.

## Preserve these boundaries

- Opening a repository needs no task. Choose direct, branch, or worktree
  deliberately; pass an explicit `--base` for branch/worktree creation. Keep
  managed worktrees outside repositories.
- `dev start` normally prepares a checkout/runtime. Agent launch is explicit.
  Parallel writers need coordinated ownership and verified launch targets.
- Completion, artifact finalization, retirement, and branch deletion are separate.
  Finalize agent artifacts after their writer stops; retire an agent checkout
  externally. A runtime's `done` state does not mean cleanup is safe.
- Unknown or cached observations, merged PRs, and command refusals do not grant
  cleanup authority. Resolve the reported conditions; do not improvise force
  removal or bypass trust checks.

## Read a matching reference for advanced operations

Load only the guide needed for the requested operation:

- Worktree provisioning or cross-host lifecycle: [ownership](references/worktree-ownership.md), [lifecycle](references/task-lifecycle.md).
- Parallel agent launches or prompt transports: [agents](references/parallel-agents.md), [handoffs](references/prompt-handoffs.md).
- Agent completion or cleanup batches: [retirement](references/agent-retirement.md), [triage](references/local-triage.md).
- Repository hooks, publication, or physical organization: [setup](references/repository-bootstrap.md), [bootstrap](references/bootstrap.md).
- SSH diagnostics, Tailscale/LAN/fleet discovery, routes, machines, keys, permissions, passwords, configuration, registration, or file export: [SSH](references/ssh-hosts.md).
- Skill/MCP/instruction transfers or managed updates: [transfers](references/agent-interop.md), [maintenance](references/skills-management.md).
- Recursive Git children: [submodules](references/submodules.md).

For ordinary listing, opening, notes, dashboard navigation, and configuration,
use command help and its suggested workflow topic.

## Unexpected dev failures

Read [feedback and repair](references/feedback-and-repair.md). Preserve the
original task; extra investigation, repair, subagents and publication need
explicit consent covering scope and cost.
