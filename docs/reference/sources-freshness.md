---
description: Define authority levels, freshness metadata, and the source matrix behind dev-cli, Git, GitHub, and Claude Code claims.
authority: project-policy
status: maintained
verified_on: 2026-08-31
---

# Sources and freshness

This site mixes product documentation, external specifications, fast-moving harness behavior, project policy, and history. Every page identifies which kind of statement it contains and when it was checked.

## Authority order

1. **dev-cli behavior:** repository code, tests, E2E, and generated command reference.
2. **Git semantics:** current `git-scm.com` manuals for the relevant installed Git version.
3. **GitHub collaboration:** current GitHub Docs.
4. **Claude Code behavior:** current Anthropic documentation, with preview/experimental/version labels.
5. **Standards:** named versioned specifications such as Conventional Commits 1.0.0 and SemVer 2.0.0.
6. **Historical context:** dated archives and older project essays, never a current operating rule.
7. **Project policy:** explicitly labeled recommendations; not presented as an upstream guarantee.

A lower item may explain motivation but cannot override a higher authority's implementation claim.

## Required page metadata

```yaml
---
description: One sentence used by navigation and llms.txt.
authority: one value from the authority table below
status: one value from the status table below
verified_on: YYYY-MM-DD
minimum_version: optional
tested_with: optional
---
```

| `authority` value | Meaning |
|---|---|
| `project` | code/test-defined dev-cli behavior |
| `project-policy` | this project's recommendation |
| `git-scm`, `github-docs`, `anthropic-docs` | one current upstream authority |
| `git-and-project-policy`, `anthropic-docs-and-project-policy` | upstream semantics plus a labeled local recommendation |
| `project-and-upstream` | compatibility page spanning implementation and upstream status |

| `status` value | Meaning |
|---|---|
| `stable`, `maintained` | current project content with a maintenance expectation |
| `evolving` | current behavior likely to change |
| `official` | current upstream normative/documented behavior |
| `research-preview-partial` | page includes a research-preview surface |
| `experimental-and-versioned` | experimental feature with explicit version bounds |
| `generated-plus-authored` | generated reference embedded in authored guidance |

`verified_on` must be a real ISO date no later than the current UTC calendar date. English/zh-TW siblings must match on authority, status, date, minimum version, and tested version. Pages backed by Anthropic docs or preview/experimental status must carry `minimum_version` or `tested_with`. The docs checker enforces these rules, nav membership, and bilingual file parity.

## Claim/source matrix

| Topic or claim | Owning page | Primary authority | Status checked |
|---|---|---|---|
| HOT/WARM/COLD/DONE and checkout modes | [Mental model](../concepts/mental-model.md) | `internal/task/task.go`, lifecycle CLI/tests | repository snapshot 2026-08-28 |
| `done --pr` leaves task active | [Change-stream workflow](../guides/change-stream-workflow.md) | `internal/cli/done.go` | implemented |
| worktree provisioning safety | [Worktrees and provisioning](../guides/worktrees-provisioning.md) | `internal/wt/plan.go`, `ecosystem.go`, `provision.go` | implemented |
| repository new/clone routing, snapshot templates/confinement, check-in policy, project trust, skill batching, TTY editor, upstream publication, and handoff | [Commands and configuration](commands-config.md#repository-bootstrap) | `internal/repo/{acquire,ref_security}.go`, `internal/scaffold`, `internal/repotemplate`, `internal/projectconfig`, `internal/cli/repo_{create,checkin,skills}*.go`, `internal/cli/prompt.go`, focused repo-bootstrap tests | implemented |
| lazygit lowercase `c` pending-message integration | [Compatibility](compatibility.md#lazygit-staged-message-prefill-is-best-effort) | [lazygit v0.59.0 working-tree helper](https://github.com/jesseduffield/lazygit/blob/v0.59.0/pkg/gui/controllers/helpers/working_tree_helper.go#L191-L216) | version-sensitive, checked 2026-08-29 |
| runtime fallback and exact-pane `start --run` dispatch | [Parallel agents and runtimes](../guides/parallel-agents-runtimes.md) | `internal/runtime/runtime.go`, `internal/runtime/herdr.go`, focused start/runtime tests | implemented |
| SSH fleet snapshots, per-host states, and `fleet sync` fast-forward safety | [Remote repository fleet](../guides/remote-fleet.md) | `internal/fleet`, `internal/cli/fleet.go`, focused fleet tests | implemented |
| READY/MERGED/RETIRED milestones, retirement refusal conditions, and merged-worktree sweep | [Agent-safe retirement](../guides/agent-safe-retirement.md) | `internal/retire`, `internal/cli/{retire,artifact,sweep}.go`, focused retirement tests | implemented |
| `dev summary` machine-wide snapshot and `dev journal` calendar-day reports | [Machine summary](../guides/machine-summary.md), [Development journal](../guides/dev-journal.md) | `internal/summary`, `internal/journal`, focused summary/journal tests | implemented |
| agent skill inventory, scopes, and explicit update actions | [TUI, repositories, quick notes, and bootstrap](../guides/tui-repos-bootstrap.md) | `internal/agentskill`, `internal/cli/skill.go`, focused TUI tests | implemented |
| TUI startup/readiness stages, generation handling, cache/live provenance, and private trace semantics | [TUI, repositories, quick notes, and bootstrap](../guides/tui-repos-bootstrap.md) | `internal/perftrace`, `internal/tui/{readiness,local}.go`, `internal/cli/tui*.go`, focused race tests | implemented |
| quick-note storage, catalog identity, search, JSON, and TUI workflow | [TUI, repositories, quick notes, and bootstrap](../guides/tui-repos-bootstrap.md) | `internal/note`, `internal/cli/note.go`, focused CLI/TUI tests | implemented |
| current GitHub Flow has six branch/PR steps and no deployment step | [GitHub Flow](../git/github-flow.md) | [GitHub Docs](https://docs.github.com/en/get-started/using-github/github-flow) | official, checked 2026-08-28 |
| linked worktrees share repository data but own files/index/HEAD | [Worktree semantics](../git/worktree-semantics-recovery.md) | [`git-worktree`](https://git-scm.com/docs/git-worktree) | official, checked 2026-08-28 |
| Conventional Commits structure | [Branches and commits](../git/branches-commits-prs.md) | [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/) | versioned standard |
| Claude Code is an agentic harness | [Agentic harness](../claude/agentic-loop-tools.md) | [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works) | official, checked 2026-08-28 |
| parallel primitive selection/status | [Parallel chooser](../claude/parallel-work-chooser.md) | [Run agents in parallel](https://code.claude.com/docs/en/agents) | version-sensitive |
| Claude worktree path/base/cleanup | [Worktree isolation](../claude/worktree-isolation.md) | [Claude Code worktrees](https://code.claude.com/docs/en/worktrees) | version-sensitive, tested 2.1.250 |
| teams and Dynamic Workflows | [Teams and workflows](../claude/teams-dynamic-workflows.md) | [agent teams](https://code.claude.com/docs/en/agent-teams), [workflows](https://code.claude.com/docs/en/workflows) | experimental/versioned |
| hooks/skills/plugins/SDK roles | [Extensions](../claude/extensions-agent-sdk.md) | Anthropic feature references | evolving |

## Historical sources

- [`githubflow.github.io`](https://githubflow.github.io/) preserves the early “default branch is always deployable” model.
- The [2019 Wayback snapshot](https://web.archive.org/web/20191104103724/https://guides.github.com/introduction/flow/) preserves an older deploy-before-merge guide.

They disagree about deploy/merge order and use older `master` terminology. They are cited only in historical sections; the current GitHub Docs page owns present-day GitHub Flow claims.

## Adapted local material

The local `agent-skills/skills/local/git-workflow/` collection informed topic discovery for branch hygiene, Conventional Commits, releases, and worktree recovery. Its content mixes upstream rules with house policy, contains known stale claims, and has no clear repository-level license grant. This site independently rewrites verified ideas and cites public upstream specifications instead of copying that skill.

## Refresh procedure

When code or an upstream feature changes:

1. Re-read the implementation/test or current official page.
2. Update the English claim and its zh-TW sibling in the same change.
3. Change `verified_on`, `tested_with`, status, and this matrix when applicable.
4. Run the source checker and strict site build.
5. Review any historical wording to ensure it did not become normative.
6. Record unresolved uncertainty as a limitation instead of guessing.

## Version-sensitive source set

- [Tools reference](https://code.claude.com/docs/en/tools-reference)
- [Subagents](https://code.claude.com/docs/en/sub-agents)
- [Agent view](https://code.claude.com/docs/en/agent-view)
- [Agent teams](https://code.claude.com/docs/en/agent-teams)
- [Dynamic Workflows](https://code.claude.com/docs/en/workflows)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
- [Hooks](https://code.claude.com/docs/en/hooks)
- [Skills](https://code.claude.com/docs/en/skills)
- [Plugins](https://code.claude.com/docs/en/plugins)
- [Agent SDK loop](https://code.claude.com/docs/en/agent-sdk/agent-loop)
