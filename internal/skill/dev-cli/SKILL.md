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

- Opening needs no task. Select direct, branch or worktree deliberately; use
  explicit `--base`. Managed worktrees stay outside repositories.
- Task-free worktrees use `dev git worktree create/open`; cleanup stays reviewed.
  `dev repo list --json` supplies navigation candidates, not external preferences/history.
- `dev work start` prepares checkout/runtime; agent launch is explicit. Parallel
  writers require disjoint ownership and verified targets.
- Completion, artifacts, retirement and branch deletion are separate. Finalize
  after writer exit; retire externally. Use `retire --base` when integration is unproven.
- Cached/unknown observations, runtime `done` and merged PRs never authorize
  cleanup. Resolve guards; never improvise force removal or bypass checks.
- `dev pr checkout <URL>` is task-free; `--try` creates an independent clone.
  Provisioning is explicit; merge never cleans up locally.

## Read a reference for advanced operations

- Worktrees/lifecycle: [ownership](references/worktree-ownership.md), [lifecycle](references/task-lifecycle.md).
- Agents/prompts: [agents](references/parallel-agents.md), [handoffs](references/prompt-handoffs.md).
- History retention, exact sessions and co-commit: [artifacts](references/ai-artifacts.md).
- Cleanup: [retirement](references/agent-retirement.md), [triage](references/local-triage.md).
- Hygiene/scans/redaction: [hygiene](references/hygiene.md).
- Forks/setup/organization: [setup](references/repository-bootstrap.md), [bootstrap](references/bootstrap.md).
- SSH/fleet: [SSH](references/ssh-hosts.md).
- Dotfile setup, native chezmoi operations, or FLEET host actions: [dotfiles](references/dotfiles.md).
- Skill/MCP/instruction transfers, updates or removal: [transfers](references/agent-interop.md), [maintenance](references/skills-management.md).
- [Snippets](references/snippets.md).
- PR/MR pages, diffnav, gh-dash, checkout and merge: [pull requests](references/pull-requests.md).
- Recursive Git children: [submodules](references/submodules.md).

## Unexpected dev failures

Read [feedback and repair](references/feedback-and-repair.md). Preserve the
original task; extra investigation, repair, subagents and publication need
explicit consent covering scope and cost.
