---
description: Choose experimental agent teams for communicating peers or Dynamic Workflows for repeatable scripted orchestration.
authority: anthropic-docs
status: experimental-and-versioned
verified_on: 2026-08-28
minimum_version: Claude Code 2.1.154 for Dynamic Workflows
tested_with: Claude Code 2.1.250
---

# Agent teams and Dynamic Workflows

Agent teams put a lead in charge of communicating peer sessions. Dynamic Workflows move the orchestration itself into JavaScript so the same fan-out, joins, and verification stages can be inspected and rerun.

!!! warning "Version-sensitive features"
    Agent teams are experimental and disabled by default. Dynamic Workflows require v2.1.154+ and may require plan/config enablement. Confirm availability, limits, and permission behavior in the installed version before relying on either.

## Agent teams

An agent team contains:

| Component | Role |
|---|---|
| lead | decomposes work, assigns/coordinates, synthesizes and integrates |
| teammates | independent Claude Code sessions with separate contexts |
| task list | pending/in-progress/completed work and dependencies when Task tools are available |
| mailbox/messages | direct communication among lead and teammates |

Enable teams explicitly with `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`. In current versions, a named `Agent` call can launch a teammate while teams are enabled. Historical `TeamCreate` and `TeamDelete` tools were removed in v2.1.178 and should not appear in new automation.

### Use teams when

- peers must share discoveries and challenge one another;
- debugging benefits from competing hypotheses;
- frontend/backend/tests have independent ownership but shared decisions;
- a lead should reassign blocked tasks as work evolves.

Use a single session or subagents for sequential work, same-file changes, or tasks where every step depends on the previous result.

### File and lifecycle safety

Teammates do **not** automatically receive worktree isolation. Partition files/symbols and shared generated artifacts before launch. If independent writers need isolation, explicitly provide separate branches/worktrees or serialize mutation.

A teammate message cannot approve a permission or consent decision for the human. Permission prompts surface through the lead. Teammates should shut down gracefully after their findings/diffs have been reviewed; task completion alone is not process cleanup.

Current limitations include no nested teams, one lead/team per session, imperfect task-status updates, slow shutdown during active calls, and incomplete resumption for in-process teammates. Keep a subagent/manual-session fallback.

## Dynamic Workflows

A Dynamic Workflow is a JavaScript script executed by the workflow runtime. The script—not Claude's current turn—decides which agents run next and retains intermediate results in variables.

```javascript
export const meta = {
  name: 'review-by-dimension',
  description: 'Review changed files and independently verify findings',
}

const reviews = await pipeline(dimensions, dimension =>
  agent(`Review for ${dimension}`, { label: dimension })
)
return reviews.filter(Boolean)
```

Use a workflow when:

- a job needs more than a handful of delegated tasks;
- the same orchestration should be rerun or audited;
- many files/items need the same transformation or review;
- independent agents should verify or challenge findings;
- intermediate output should stay out of the main context.

Do not use one for a small linear task, a conversation needing mid-run human sign-off, or unsupervised destructive/outward actions.

### Runtime constraints

Current official documentation describes a background isolated script runtime with no direct shell/filesystem access; agents perform the work. It allows up to 16 concurrent agents (subject to local resources) and 1,000 agents per run. These are version-sensitive ceilings, not recommended sizes. Start with a small slice and the default medium guideline rather than designing to the caps.

A stopped or unrecoverably failed `agent()` resolves to a missing result. Workflows must validate/deduplicate outputs and report unverified claims instead of treating absence as success. Same-session resume can reuse unchanged completed calls, while changed/failed earlier stages may cause later work to rerun.

## Compare coordination ownership

| Surface | Who holds the plan? | Best scale | Communication |
|---|---|---:|---|
| subagents | main Claude turn by turn | a few tasks | results return to caller |
| skill | Claude following reusable instructions | a few tasks | current context |
| agent team | lead agent turn by turn | handful of peers | direct messages plus tasks |
| Dynamic Workflow | JavaScript script | dozens or larger | structured intermediate results |

## Quality pattern

1. Discover the independent units.
2. Fan out bounded workers with structured output.
3. Verify each finding/result independently as soon as it arrives.
4. Deduplicate and rank surviving results.
5. Let one integration owner apply writes in dependency order.
6. Run the complete combined verification.
7. Stop workers and clean isolated resources.

## Sources

- [Agent teams](https://code.claude.com/docs/en/agent-teams)
- [Dynamic Workflows](https://code.claude.com/docs/en/workflows)
- [Run agents in parallel](https://code.claude.com/docs/en/agents)
- [Tools reference](https://code.claude.com/docs/en/tools-reference)
