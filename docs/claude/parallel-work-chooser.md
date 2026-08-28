---
description: Choose among one session, subagents, Agent view, agent teams, Dynamic Workflows, and worktrees for parallel work.
authority: anthropic-docs-and-project-policy
status: evolving
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
---

# Parallel work decision guide

Choose the smallest coordination surface that safely separates mutation. More agents increase context capacity and throughput, but also multiply tokens, integration cost, and failure surfaces.

!!! info "Freshness"
    Agent view is a **research preview**. Agent teams are **experimental and disabled by default**. Dynamic Workflows require Claude Code v2.1.154 or later. Recheck official documentation before standardizing these surfaces for a team.

## Start with four questions

1. **Who should coordinate?** You, the main Claude session, a team lead, or a script?
2. **Do workers need to communicate with one another?** Returning independent results is cheaper than maintaining peer coordination.
3. **Can they mutate the same files or external state?** If yes or unknown, split branches/worktrees or serialize the work.
4. **Is the job one-off or repeatable at scale?** A handful of tasks rarely needs a scripted workflow.

## Choose the surface

| Need | Use | Why |
|---|---|---|
| one coherent task with frequent feedback | main conversation | lowest coordination overhead |
| focused research/test/review whose details should not fill main context | subagent | independent context, concise result returns |
| several independent sessions you want to dispatch and monitor | Agent view | human-managed background sessions in one UI; research preview |
| peers must share findings, tasks, and messages | agent team | lead plus communicating teammates; experimental |
| repeatable fan-out/join or adversarial verification at large scale | Dynamic Workflow | JavaScript script owns orchestration and intermediate results |
| independent writers need file-state separation | worktrees with any appropriate surface | support layer for files/index/HEAD; not a coordination style |
| one long build/log watcher | background `Bash` or `Monitor` | no extra reasoning context is needed |

## Mutation decision

```text
read-only worker?
  yes ─► same checkout is usually safe
  no
   │
files and generated/external state provably disjoint?
  yes ─► one branch/worktree can work with explicit ownership
  no or unknown ─► separate branch + worktree per change stream
```

Even separate worktrees can collide through ports, databases, caches, hooks, shared refs, cloud accounts, queues, and deployment targets. Assign those resources explicitly.

## Recommended patterns

### Research before implementation

Run independent read-only subagents, collect conclusions, then let one owner implement. This increases perspective without creating merge work.

### One feature, disjoint modules

Use one `dev` worktree/branch and several panes or subagents. Give every writer a path/symbol contract and one agent responsibility for shared interfaces and final tests.

### Competing hypotheses or designs

Use separate contexts. For read-only debugging, subagents or a team can challenge hypotheses without extra worktrees. For competing implementations, use a branch/worktree per approach and keep only the selected result after review.

### Independent backlog items

Use Agent view when a human wants to dispatch and revisit several sessions. Each item should have its own branch/worktree and acceptance test.

### Cross-checked audit or migration

Use a Dynamic Workflow when the script should fan out many items, collect structured results, verify findings with independent agents, and resume/re-run the same graph. Trial a small slice before expanding.

## Team-specific guardrails

Agent-team teammates do not automatically receive worktree isolation. Partition files and shared generated artifacts before launch. Start with 3–5 teammates only when peer communication is valuable; otherwise use subagents. The lead remains the integration owner and must wait for results, inspect diffs, run combined verification, and shut down teammates.

## Workflow-specific guardrails

- Require explicit human opt-in before a workflow run.
- Keep the script readable and its phases visible.
- Bound concurrency, total agents, turns, and cost.
- Treat stopped/failed agent results as missing, not successful.
- Verify each dimension as soon as its review completes.
- Keep destructive or outward-facing actions outside unsupervised fan-out.

## Worktree ownership with dev-cli

Use `dev` for a durable stream a human may review or resume tomorrow. Use Claude Code's own temporary worktree when the harness should own the isolated experiment. Do not nest a harness worktree inside a `dev` worktree merely because another agent is launched.

## Sources

- [Claude Code: run agents in parallel](https://code.claude.com/docs/en/agents)
- [Subagents](https://code.claude.com/docs/en/sub-agents)
- [Agent view](https://code.claude.com/docs/en/agent-view)
- [Agent teams](https://code.claude.com/docs/en/agent-teams)
- [Dynamic Workflows](https://code.claude.com/docs/en/workflows)
- [`internal/help/topics/agents.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/agents.md)
