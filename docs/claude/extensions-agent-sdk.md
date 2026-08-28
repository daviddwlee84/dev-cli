---
description: Choose hooks, skills, plugins, MCP, or the Agent SDK according to whether you need automation, knowledge, packaging, integration, or an embedded harness.
authority: anthropic-docs
status: evolving
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
---

# Hooks, skills, plugins, and Agent SDK

Claude Code extension mechanisms solve different problems. Put reusable instructions in skills, deterministic lifecycle reactions in hooks, external capabilities behind MCP, distributable components in plugins, and a programmatic agent loop in the Agent SDK.

## Choose the mechanism

| Need | Mechanism | Runs where |
|---|---|---|
| reusable procedure or domain instructions | skill | loaded into the current agent context on demand |
| deterministic reaction before/after lifecycle events | hook | command/HTTP/MCP/model handler outside or alongside the model turn |
| database, browser, API, or custom tool | MCP server/custom tool | external capability exposed through a tool schema |
| versioned distribution of skills/agents/hooks/MCP/workflows | plugin | namespaced package installed at a scope |
| embed Claude Code's autonomous loop in an application | Agent SDK | application-controlled session/process |
| one project-specific experiment | standalone `.claude/` configuration | current project without packaging overhead |

## Skills

A skill is a `SKILL.md` procedure with a discoverable description and optional supporting files. Claude can select a relevant skill or a user can invoke `/skill-name`. The description is visible early; the body and references load only when used, keeping persistent procedure text out of every turn.

Custom commands have been merged into skills: existing `.claude/commands/*.md` files still work, while directory-based skills add references, scripts, invocation controls, and progressive disclosure.

`allowed-tools` pre-approves listed tools while the skill runs; it does not invent a tool or bypass parent/organization restrictions. A skill should orchestrate existing capabilities and keep destructive/outward actions behind explicit consent.

Use a skill when people repeatedly paste the same checklist, deploy recipe, review standard, or multi-step procedure. Keep stable project facts in `CLAUDE.md` instead.

## Hooks

Hooks run at events such as session setup, prompt submission, pre/post tool use, permission request, agent/task transitions, compaction, stop, and worktree creation/removal. Handler types include command, HTTP, MCP tool, prompt evaluation, and experimental agent handlers.

Use hooks for deterministic actions:

- format or validate changed files;
- reject a prohibited command before execution;
- attach context or audit records;
- enforce task-completion/teammate-idle checks;
- implement non-Git worktree create/remove behavior.

Hooks complement permissions; they do not replace them. A successful hook with no decision does not approve a tool call, best-effort matchers are not a hard sandbox, and many hook failures/timeouts fail open. Review committed hook code like executable project code because it inherits local environment authority.

## Plugins

A plugin packages and versions reusable components: skills, custom agents, hooks, MCP/LSP configuration, workflows, monitors, binaries, and supported default settings. Plugin skill names are namespaced (`/plugin-name:skill`) to avoid collision and can be distributed through a marketplace.

Use standalone `.claude/` configuration for a project-specific workflow or fast experiment. Convert to a plugin when the component should be versioned, installed, shared across projects, or released to others. A `plugin.json` manifest provides identity/version/metadata; component directories live at the plugin root, not inside `.claude-plugin/`.

## MCP and custom tools

MCP servers expose external systems through typed tools/resources. Their definitions, permissions, network/credential access, and output size become part of the harness's security and context budget. Defer or scope tool schemas when possible, mark genuinely read-only custom tools correctly, and never infer that “read” in a tool name makes its backend safe.

## Agent SDK

The Claude Agent SDK embeds the same execution loop that powers Claude Code:

1. initialize a session with prompt, system context, and tool definitions;
2. receive an assistant response with text/tool calls;
3. execute approved tools and return results;
4. repeat until a text-only completion or a turn/budget/error limit;
5. emit a result carrying status, usage/cost, and session ID.

The SDK provides Claude Code built-in tools plus permissions, hooks, subagents, MCP/custom tools, context compaction, session resume/fork, model/effort control, and turn/budget limits. It is more than a bare API tool-calling loop because the harness manages these coding-agent concerns and execution semantics.

For production agents:

- set `maxTurns` and `maxBudgetUsd`;
- use least-privilege tools and explicit allow/deny rules;
- expose an approval callback for interactive high-trust actions;
- persist/inspect session IDs and result subtypes;
- handle failures and missing subagent results explicitly;
- keep the execution environment isolated;
- record verification evidence, not only final prose.

## Security boundary summary

- Skills are instructions, not authority.
- Hooks are executable automation, not a substitute for permissions.
- Plugins are supply/distribution units and must be reviewed before install.
- MCP/custom tools carry their backend credentials and side effects.
- Agent SDK applications own permission callbacks, limits, storage, and environment isolation.

## Sources

- [Skills](https://code.claude.com/docs/en/skills)
- [Hooks reference](https://code.claude.com/docs/en/hooks)
- [Create plugins](https://code.claude.com/docs/en/plugins)
- [MCP](https://code.claude.com/docs/en/mcp)
- [Agent SDK loop](https://code.claude.com/docs/en/agent-sdk/agent-loop)
