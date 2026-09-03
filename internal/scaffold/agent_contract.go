package scaffold

const starterAgentContract = `# Project agent guidance

> Bootstrap status: incomplete. This is a safe starter, not verified project
> documentation. Replace each TODO with evidence from the repository as the
> implementation takes shape; do not invent commands, architecture, or policy.

## Working rules

- These instructions apply repository-wide.
- Preserve user work and unrelated changes; keep edits focused on the request.
- Inspect existing code, manifests, scripts, CI, and maintained documentation
  before choosing an implementation or command.
- Do not run destructive Git or filesystem operations, publish, deploy, or
  release without explicit authorization.
- Never commit secrets, credentials, or real local environment values.
- Never claim a check passed unless it was actually run; report skipped and
  failing checks plainly.

## Project purpose

TODO: Describe what this repository builds or operates, its users, and important
non-goals.

## Toolchain and verified commands

No project commands were verified by this scaffold. Replace these placeholders
with commands copied from authoritative repository configuration or docs.

- Prerequisites/setup: TODO — not documented yet.
- Build: TODO — not documented yet.
- Focused test: TODO — not documented yet.
- Full test: TODO — not documented yet.
- Format/lint: TODO — identify which commands modify files.
- Run/deploy: TODO — not documented yet.

Until a command is verified, report it as undocumented rather than guessing.

## Architecture

- Entrypoints: TODO.
- Major directories and subsystem ownership: TODO.
- Generated, vendored, or externally managed paths: TODO.
- Important data and control-flow boundaries: TODO.

## Behavioral contracts

TODO: Record project-specific invariants that implementations must preserve. Do
not present assumptions as established contracts.

## Handoff requirements

- Summarize changed files and user-visible behavior.
- List the exact checks run and their results.
- State which relevant checks were not run and why.
- Report remaining TODOs, uncertainty, assumptions, and preserved user changes.
`

// StarterAgentContract is the canonical built-in scaffold body. Custom presets
// may replace it by providing their own AGENTS.md file.
func StarterAgentContract() string { return starterAgentContract }
