---
description: Define authority levels, freshness metadata, and the source matrix behind dev-cli, Git, GitHub, and Claude Code claims.
authority: project-policy
status: maintained
verified_on: 2026-09-11
---

# Sources and freshness

Submodule graph/initialization, selective task branches, recursive remote proof and quarantine recovery are defined by `internal/gitx/submodule*`, `internal/submodule`, taskflow/CLI tests and [Submodule workspaces](../guides/submodule-workspaces.md), checked against Git 2.55.0 on 2026-09-06.

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
verified_on: 2026-09-11
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
| parent-preserving task completion, foreground program consent and coordinator v2 | [Agent-safe retirement](../guides/agent-safe-retirement.md#task-worktree-scope) | runtime process/occupancy, taskflow completion/retire and CLI scope regression tests | Unreleased |
| HOT/WARM/COLD/DONE, checkout modes, and legal transitions | [Mental model](../concepts/mental-model.md) | `internal/task/task.go`, `internal/taskflow/transitions.go`, lifecycle tests | repository snapshot 2026-09-01 |
| repository flow topology, local/manual-remote observations, revision-bound Plan/Apply, and partial ledgers | [Repository lifecycle flow](../guides/repository-flow.md) | `internal/cli/flow.go`, `internal/flowtui`, `internal/taskflow`, focused flow tests | preview implemented 2026-09-01 |
| `done --pr` leaves task active and DONE keeps resources until Retire | [Change-stream workflow](../guides/change-stream-workflow.md) | `internal/taskflow/{complete,retire}.go`, CLI lifecycle tests | implemented |
| worktree provisioning safety | [Worktrees and provisioning](../guides/worktrees-provisioning.md) | `internal/wt/plan.go`, `ecosystem.go`, `provision.go` | implemented |
| repository new/clone routing, cached/local picker behavior, snapshot templates/confinement, check-in policy, project trust, skill batching, TTY editor, upstream publication, and handoff | [Commands and configuration](commands-config.md#repository-bootstrap) | `internal/repo/{acquire,ref_security}.go`, `internal/picker`, `internal/scaffold`, `internal/repotemplate`, `internal/projectconfig`, `internal/cli/{picker,repo_create,start_wizard}.go`, focused picker/repo-bootstrap tests | implemented |
| lazygit lowercase `c` pending-message integration | [Compatibility](compatibility.md#lazygit-staged-message-prefill-is-best-effort) | [lazygit v0.59.0 working-tree helper](https://github.com/jesseduffield/lazygit/blob/v0.59.0/pkg/gui/controllers/helpers/working_tree_helper.go#L191-L216) | version-sensitive, checked 2026-08-29 |
| runtime fallback and exact-pane `start --run` dispatch | [Parallel agents and runtimes](../guides/parallel-agents-runtimes.md) | `internal/runtime/runtime.go`, `internal/runtime/herdr.go`, focused start/runtime tests | implemented |
| OpenSSH alias discovery/provenance, dev-owned Include/fragments, exact flags/JSON/TSV, public-key bootstrap, ProxyJump/Windows admin handling, partial outcomes, and removal limits | [SSH host onboarding](../guides/ssh-hosts.md) | `internal/sshhost`, `internal/cli/ssh.go`, SSH help, focused domain/CLI tests | implemented; repository snapshot 2026-09-01 |
| schema-v1 repository context, scoped readiness, sanitized remotes, and cache/live provenance | [Commands and configuration](commands-config.md#high-value-structured-interfaces) | `internal/repocontext`, `internal/cli/repo_context.go`, focused context tests | implemented |
| fleet primary/generated-fragment ownership, `remote_os`, snapshots, machine UUID pinning, sync safety, explicit bounded `fleet files`, and POSIX/Windows launchers | [Remote repository fleet](../guides/remote-fleet.md) | `internal/fleet`, `internal/localfiles`, `internal/machineid`, `internal/cli/{fleet,fleet_files}.go`, focused fake-SSH/fault-injection tests, required Windows SSH CI job | implemented; SSH/fleet snapshot 2026-09-01 |
| READY/MERGED/RETIRED milestones, task-backed retirement revalidation, compatibility boundaries, and merged-worktree sweep | [Agent-safe retirement](../guides/agent-safe-retirement.md) | `internal/taskflow/retire.go`, `internal/retire`, `internal/cli/{retire,artifact,sweep}.go`, focused retirement tests | implemented |
| `dev summary` machine-wide snapshot and `dev journal` calendar-day reports | [Machine summary](../guides/machine-summary.md), [Development journal](../guides/dev-journal.md) | `internal/summary`, `internal/journal`, focused summary/journal tests | implemented |
| native cross-repository agent skill inventory, versioned path registry, local status, object-byte upstream checks, and serialized mutations | [TUI, repositories, quick notes, and bootstrap](../guides/tui-repos-bootstrap.md) | `internal/agenttarget`, `internal/agentskill`, `internal/inventory/agent_skills.go`, `internal/cli/skill.go`, focused CLI/TUI tests | implemented; path registry snapshot `skills@1.5.23`; checked 2026-09-02 |
| declaration-only MCP inventory, Claude approval annotation, and secret-redaction boundary | [TUI, repositories, quick notes, and bootstrap](../guides/tui-repos-bootstrap.md) | current official agent config docs; `internal/agentmcp`, `internal/cli/mcp.go`, fixture/security tests | implemented for Claude Code, Codex, Cursor, Gemini CLI, and OpenCode; checked 2026-09-02 |
| dashboard startup/readiness stages, generation handling, cache/live provenance, and private trace semantics | [TUI, repositories, quick notes, and bootstrap](../guides/tui-repos-bootstrap.md) | `internal/perftrace`, `internal/tui/{readiness,local}.go`, `internal/cli/tui*.go`, focused race tests | implemented |
| quick-note storage, catalog identity, search, JSON, and TUI workflow | [TUI, repositories, quick notes, and bootstrap](../guides/tui-repos-bootstrap.md) | `internal/note`, `internal/cli/note.go`, focused CLI/TUI tests | implemented |
| Bundled skill drift, post-upgrade refresh and guarded uninstall | [Skills management](../guides/skills-management.md#bundled-dev-cli-skill-lifecycle) | `internal/skill`, CLI upgrade and isolated lifecycle tests | v0.2.23 |
| release archives, package-manager ownership, and Homebrew tap publication | [Compatibility](compatibility.md) | `internal/cli/upgrade.go`, `.github/workflows/release.yml`, `scripts/update-homebrew-formula.sh`, packaging helper test | implemented |
| current GitHub Flow has six branch/PR steps and no deployment step | [GitHub Flow](../git/github-flow.md) | [GitHub Docs](https://docs.github.com/en/get-started/using-github/github-flow) | official, checked 2026-08-28 |
| linked worktrees share repository data but own files/index/HEAD | [Worktree semantics](../git/worktree-semantics-recovery.md) | [`git-worktree`](https://git-scm.com/docs/git-worktree) | official, checked 2026-08-28 |
| Conventional Commits structure | [Branches and commits](../git/branches-commits-prs.md) | [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/) | versioned standard |
| Claude Code is an agentic harness | [Agentic harness](../claude/agentic-loop-tools.md) | [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works) | official, checked 2026-08-28 |
| parallel primitive selection/status | [Parallel chooser](../claude/parallel-work-chooser.md) | [Run agents in parallel](https://code.claude.com/docs/en/agents) | version-sensitive |
| Claude worktree path/base/cleanup | [Worktree isolation](../claude/worktree-isolation.md) | [Claude Code worktrees](https://code.claude.com/docs/en/worktrees) | version-sensitive, tested 2.1.250 |
| teams and Dynamic Workflows | [Teams and workflows](../claude/teams-dynamic-workflows.md) | [agent teams](https://code.claude.com/docs/en/agent-teams), [workflows](https://code.claude.com/docs/en/workflows) | experimental/versioned |
| hooks/skills/plugins/SDK roles | [Extensions](../claude/extensions-agent-sdk.md) | Anthropic feature references | evolving |
| generic prompt recipes, run/open transport, current-terminal boundary, session runtime-close meaning, and workspace retirement audit | [Prompt handoffs](../guides/prompt-handoffs.md) | `internal/cli/{prompt_command,prompt_providers}.go`, `internal/config/config.go`, `internal/promptkit`, `internal/handoff`, `internal/closeout`, `internal/retire/audit.go`, focused tests | implemented |
| `dev pr` inbox, provider surfaces, effective filtering/scope, and local checkout-health join | [Pull request inbox](../guides/pull-request-inbox.md) | `internal/forge/pr.go`, `internal/forge/{github,gitlab}.go`, `internal/cli/pr*.go`, focused forge/CLI tests | implemented |
| AI reviewer trigger phrases, workflow requirement and plan tiers | [AI pull-request review options](../notes/ai-pr-review-options.md) | [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions), [Claude Code Review setup](https://support.claude.com/en/articles/14233555-set-up-code-review-for-claude-code), [Codex GitHub](https://developers.openai.com/codex/integrations/github), [CodeRabbit commands](https://docs.coderabbit.ai/guides/commands) | vendor docs, checked 2026-09-01 |

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

### Agent interoperability profile (2026-09-07)

[Agent interoperability](../guides/agent-interop.md) is grounded in
`internal/agentinterop`, native inventory/CLI tests, and synthetic MCP servers.
The skills profile is fixed at 1.5.23 with project-v1/global-v3 locks; it does
not follow latest. The MCP initialization probe uses specification 2025-06-18.
Current Codex canonical user discovery is additive to the pinned registry's
legacy paths. Schema/fixture verification does not prove that a running native
client has loaded the generated configuration. Windows transfer writes remain
unavailable pending a separately verified privacy/identity adapter.

## Dashboard lifecycle additions

Repository homepage resolution reads local Git only. Try removal journals beside assets are durable operation history; they are neither a disposable cache nor a verified remote-backup receipt. Unknown removal outcomes remain unknown. See [dashboard actions](../guides/dashboard-actions.md).

## Local triage authority

[Local triage](../guides/local-triage.md) is defined by `internal/triage`, `internal/triagetui`, taskflow synchronization/protected cleanup and their Git-backed tests. Branch comparisons use local cached refs; interrupted batch receipts retain uncertainty. Reviewed 2026-09-09.

## Dashboard navigation and organizer entry

Enter always opens a dashboard row. REPOS/TRY `Ctrl+O` offers organization of the
current item, filtered results, or all local work; multi-selection belongs to the
independent triage screen. `1–7` selects TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS,
and MCP respectively. TASKS state filters live in its action menu (`a` still
shows done tasks). Click a data column for ascending → descending → default
ordering; FLEET HOST groups machines. Sorting is local to each view/session and
uses the current snapshot, with unknown values last. The footer keeps two lines
of primary actions and navigation; tools, state filters and sorting are in
`Ctrl+O`, and `?` lists the full key map. Existing custom tool bindings for `4–7`
need reassignment; `x`/Ctrl+A are no longer reserved dashboard selection keys.

Triage uses grouped repo/Try checkboxes, mouse selection, and Ctrl+A all/none
within the filtered scope. Results preserve failures when returning to the
dashboard. Git synchronization diagnostics retain a category, exit code, bounded
redacted output and a next step; old receipts cannot recover discarded reasons.
Retries require a new preview. Authentication, fetch and rebase are never
silently performed as error recovery. See [local triage](../guides/local-triage.md).


Calendar heatmaps/automatic Git backfill, searchable action menus and progressive
REPOS loading are verified through stats/tui/repo/cli tests and isolated PTY
traces. [Skills management](../guides/skills-management.md) provider semantics
are checked against upstream skills v1.5.23/v1.5.25 source contracts and isolated
fixtures. Listing/checking do not execute the provider; native mutation does not
claim frozen-content reproduction.

## Machine connections and discovery (2026-09-11)

[SSH onboarding](../guides/ssh-hosts.md#discovery-and-canonical-machines) is defined
by `internal/machineregistry`, `internal/sshdiscovery`, `internal/sshflow`, the SSH
CLI/dashboard adapters and their hermetic tests. Registry SQLite is durable;
discovery reports have a five-minute cache window and are never authentication
proof. Canonical IDs do not replace remote dev UUID pins. Tailscale CLI absence,
provider failures, source changes and partial scans retain explicit uncertainty.

Upstream distinctions were checked against [Tailscale SSH](https://tailscale.com/kb/1193/tailscale-ssh)
and the [Tailscale CLI](https://tailscale.com/kb/1080/cli#ssh). Native status JSON
is version-sensitive; dev consumes a bounded subset and reports unsupported or
unusable observations rather than enabling/configuring the provider.

## SSH picker and permission review (2026-09-11)

Key-list and wizard behavior is grounded in `internal/sshhost` key catalogs and
permission plans, `internal/cli/ssh_keys.go`, `ssh_permissions.go`,
`ssh_registration.go` and their fixture tests. `internal/picker` owns the backend
choice: single selections can use fzf, all multi-selections use Bubble Tea.
Key metadata/agent presence is not remote authentication proof. Permission repair
is a separately approved, retained local operation; default catalog listing does
not run `ssh -G`, chmod or remote login.

## SSH key doctor scope (2026-09-11)

The standalone `ssh key doctor` scan and selected-path mode are defined by
`internal/sshhost` permission scanning/planning and the key-doctor CLI adapter.
Fixtures verify metadata-only discovery, blocked/incomplete no-write behavior,
retained partial repairs and post-repair checks. Default scan scope differs from
setup's baseline-plus-selected-key preflight. Key inventory diagnostics preserve
stable codes and include specific safe reasons and appropriate remediation hints; they do not
convert missing public companions or unsafe paths into proof of key availability.
