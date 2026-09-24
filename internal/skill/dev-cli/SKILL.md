---
name: dev-cli
description: Use dev for repository discovery and setup, task and worktree lifecycle, agent handoffs, SSH hosts, fleet operations, dotfile setup, repository notes, skill or MCP inventory, secret/privacy hygiene, and feedback for unexpected dev failures.
---

# dev-cli

Git owns durable code; worktrees are local checkouts; runtimes are host-local
sessions; dev records task intent.

## Discover what you need

Use leaf `--help` for syntax, `dev help <topic>` for workflows, and
`dev help --tree` for families. See [navigation](references/command-navigation.md)
for shortcuts and Try graduation/demotion; dashboard details: `dev help tui`.
Prefer supported JSON. Read help before mutation: some actions apply immediately,
others require preview/apply. Do not preload manuals or run routine diagnostics.

## Preserve these boundaries

- Opening a repository needs no task. Choose direct, branch, or worktree
  deliberately; pass an explicit `--base` for branch/worktree creation. Keep
  managed worktrees outside repositories.
- `dev git worktree create/open` and reviewed merged-worktree sweep can be used
  without task registration. `dev repo list --json` supplies editor/launcher
  candidates; keep native project preferences and usage histories separate.
- `dev work start` normally prepares a checkout/runtime. Agent launch is explicit.
  Parallel writers need coordinated ownership and verified launch targets.
- Completion, artifact finalization, retirement and branch deletion are separate.
  Finalize after writer exit; retire externally. Use `retire --base` when a fork
  point cannot prove integration. Runtime `done` is not cleanup-ready.
- Unknown or cached observations, merged PRs, and command refusals do not grant
  cleanup authority. Resolve the reported conditions; do not improvise force
  removal or bypass trust checks.

## Read a reference for advanced operations

- Worktree provisioning or cross-host lifecycle: [ownership](references/worktree-ownership.md), [lifecycle](references/task-lifecycle.md).
- Parallel agent launches or prompt transports: [agents](references/parallel-agents.md), [handoffs](references/prompt-handoffs.md).
- History retention, exact sessions and co-commit: [artifacts](references/ai-artifacts.md).
- Agent completion or cleanup batches: [retirement](references/agent-retirement.md), [triage](references/local-triage.md).
- Repository hygiene batches, `report --json` summaries, scans or redaction: [hygiene](references/hygiene.md).
- Repository forks, hooks, publication, or physical organization: [setup](references/repository-bootstrap.md), [bootstrap](references/bootstrap.md).
- SSH dashboard, diagnosis, discovery, tests/activity, keys, credentials and registration: [SSH](references/ssh-hosts.md).
- Dotfile setup, native chezmoi operations, or FLEET host actions: [dotfiles](references/dotfiles.md).
- Skill/MCP/instruction transfers, updates or removal: [transfers](references/agent-interop.md), [maintenance](references/skills-management.md).
- [Snippets](references/snippets.md).
- Recursive Git children: [submodules](references/submodules.md).

## Unexpected dev failures

Read [feedback and repair](references/feedback-and-repair.md). Preserve the
original task; extra investigation, repair, subagents and publication need
explicit consent covering scope and cost.
