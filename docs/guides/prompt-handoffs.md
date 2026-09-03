---
description: Render deterministic operational context or hand it to a configured local agent without creating a second lifecycle authority.
authority: project
status: stable
verified_on: 2026-09-02
tested_with: OpenCode 1.18.25
---

# Prompt handoffs

`dev prompt` collects a read-only snapshot, places it in a built-in prompt, and
either prints it or gives it to a local command you configured. It is a context
handoff, not an agent loop, scheduler, permission manager, or lifecycle engine.

!!! info "Freshness"
    **Authority:** `internal/cli/prompt_command.go`, `internal/promptkit`,
    `internal/handoff`, `internal/closeout`, and `internal/retire/audit.go` ·
    **Status:** stable · **Verified:** 2026-09-02.

## Escalate only as far as needed

```text
dev status / dev sweep / dev repo context
    |
    +-- facts are sufficient --> use done, park, sweep, or retire
    |
    +-- dev prompt render <recipe> --> inspect or copy the exact prompt
    +-- dev prompt run <recipe>    --> bounded one-shot analysis, no user stdin
    +-- dev prompt open <recipe>   --> foreground conversation in this terminal
```

Prefer the deterministic lifecycle command when the next step is already clear.
Render when a person or another tool only needs the context. Use `run` for a
finite unattended answer. Use `open` only when a semantic question needs a
conversation, such as resolving what a conflicted change should mean.

All three modes collect the same recipe context. The receiving agent may explain
or prioritize it, but `dev` does not parse that reply, run a follow-up turn, or
convert it into approval for a mutation.

## The three recipes

```bash
dev prompt list
dev prompt list --json
dev prompt render pr-triage
dev prompt render session-close
dev prompt render workspace-closeout [repo-or-checkout]
```

| Recipe | Scope | Read-only result |
|---|---|---|
| `pr-triage` | account/local forge inbox | Orders requests you authored or were asked to review under merge/review/fix/wait/inspect. It accepts the same query and `--scope`, `--role`, `--state`, `--repo`, `--all-repos`, `--linked`, and `--limit` filters as `dev pr list`. |
| `session-close` | selected runtime on this machine | Classifies sessions for runtime closure, checkpoint/park, keeping open, or inspection. It includes session/pane identity, recognized activity, matching task status, and artifact status where available. |
| `workspace-closeout` | one repository | Joins tasks, every known checkout, live Git status, runtime coverage, artifacts, and pull requests, then includes a full read-only retirement audit for each checkout. `--base` supplies the audit base for an unmanaged linked worktree. |

Each rendered prompt carries a JSON envelope with `schema_version: 1`, the recipe
and context versions, generation time, host, scope, target where applicable,
capabilities, warnings, and recipe-specific context. Collected repository text,
branch names, request titles, and notes are data rather than instructions.
Missing or failed evidence remains an explicit capability gap or warning; it is
never changed into a reassuring clean/empty value.

## Choose render, run, or open

| Mode | Agent required? | Prompt transport | User stdin | Default timeout | Runtime behavior |
|---|---:|---|---|---|---|
| `render` | no | stdout | untouched | none | starts no process and touches no runtime |
| `run` | yes | `stdin`, private `file`, or one `argv` element | never passed through | 10 minutes | waits for one batch process in the recipe working directory |
| `open` | yes | private `file` or one `argv` element | attached to the child | none | runs one foreground process in the current terminal/TTY |

Examples:

```bash
dev prompt render pr-triage --role reviewer --repo owner/api
dev prompt run session-close --agent my-agent
dev prompt run pr-triage --dry-run
dev prompt open workspace-closeout . --agent my-agent
dev prompt open workspace-closeout . --dry-run
```

`--dry-run` resolves the agent, working directory, transport, timeout, and safe
command preview, then prints the complete rendered prompt without starting the
process. It therefore makes no writer claim. A real `run` or `open` is treated as
a writer claim when its working directory is a checkout, even though the recipe
asks for read-only analysis; the shared-checkout occupancy guard still applies.
Unlike a command the current agent runs to continue its own work, a handoff is a
new agent claim, so the invoking agent's pane is **not** excluded. Use
`--allow-shared-checkout` only after coordinating disjoint ownership.

`open` requires an interactive terminal unless it is a dry run and does not
accept a timeout. Only `run` accepts a timeout and gains a 10-minute default when
the value is absent or zero. When a batch timeout expires, `dev` terminates the
launcher process tree so a descendant agent cannot continue modifying the
checkout after the handoff returns.

## Host-only agent configuration

There is no built-in agent, vendor, or default launcher. Configure one or more
local commands in the user config returned by `dev config path`:

```toml
[[agent]]
name = "my-agent"
default = true

[agent.run]
command = ["my-agent", "--batch"]
input = "stdin"
timeout = "10m"

[agent.open]
command = ["my-agent", "{{prompt_file}}"]
input = "file"
```

### Concrete OpenCode example

OpenCode 1.18.25 exposes `opencode run [message..]`; adding `--interactive`
keeps a direct split-footer conversation. File input works well for a large
workspace snapshot because `-f/--file` attaches the private prompt file while a
short message tells OpenCode how to treat it:

```toml
[[agent]]
name = "opencode"
default = true

[agent.run]
command = ["opencode", "run", "--file", "{{prompt_file}}", "Read the attached dev prompt and follow its instructions."]
input = "file"
timeout = "10m"

[agent.open]
command = ["opencode", "run", "--interactive", "--file", "{{prompt_file}}", "Read the attached dev prompt, explain the evidence, and ask before changing anything."]
input = "file"
```

This is documentation, not a built-in default. Re-check `opencode run --help`
after upgrading; do not add `--auto`, because that would change the configured
agent's approval policy.

Selection is deterministic:

1. `--agent NAME` selects that name (matching is case-insensitive).
2. Without it, the one entry with `default = true` is selected.
3. Without a default, the sole configured `[[agent]]` is selected.
4. Multiple entries with no default are ambiguous and fail with their names.

Names are required, cannot have surrounding whitespace, and must be unique
case-insensitively. At most one entry may be the default. Each agent must define
at least one of `[agent.run]` or `[agent.open]`; asking for an undefined mode
fails rather than borrowing the other launcher.

A launcher has these fields:

| Field | Constraint |
|---|---|
| `command = ["program", "arg", ...]` | Direct argv. The first element must be non-empty. Set exactly one of `command` and `shell`. Preferred because it does not invoke a shell. |
| `shell = "static command"` | Runs through `$SHELL -c`. It must be static and cannot contain prompt placeholders. |
| `input = "stdin"` | `run` only. The prompt is the child's finite stdin; no placeholder is allowed. |
| `input = "file"` | For `command`, exactly one whole argv element must be `{{prompt_file}}`. For `shell`, reference `$DEV_PROMPT_FILE` or `${DEV_PROMPT_FILE}`. |
| `input = "argv"` | `command` only, with exactly one whole argv element `{{prompt}}`. The rendered prompt is limited to 100 KiB. |
| `timeout = "10m"` | `run` only; optional non-negative duration, with 10 minutes when omitted/zero. `open` rejects a timeout because safe interactive process-tree cancellation needs terminal job control. |
| `load_shell_rc = true` | Optional and valid only with `shell`; uses `$SHELL -lic` instead of `-c`. |

Placeholders must be complete command elements; embedded forms such as
`--prompt={{prompt}}` are rejected. For file transport, `dev` creates a temporary
0700 directory and 0600 `prompt.md`, then removes it after the child exits.
Prompt contents are never interpolated into shell text.

`[[agent]]` is machine-owned executable policy. A repository's
`.dev-cli/config.toml` is not allowed to define it, so checking out a project
cannot choose a command for `dev` to execute.

## Permissions and mutation boundary

The built-in recipes instruct the receiver to analyze and quote possible next
commands, not to approve, merge, rebase, close, delete, or retire anything.
However, `dev prompt` is not a sandbox: the configured child retains whatever
filesystem, network, tool, and approval policy its own command gives it. `dev`
does not add a permissive mode, reduce permissions, answer an approval prompt,
or edit configuration.

No agent answer is authoritative. Review it, then invoke `dev done`, `dev park`,
`dev sweep`, or `dev retire` yourself. Those commands recollect and revalidate
fresh state at their mutation boundaries.

## Current-terminal and Herdr boundary

`dev prompt open` does **not** create, focus, reuse, or inject into a Herdr, tmux,
or Zellij surface. It starts the configured process in the foreground of the
terminal that invoked it. Inside Herdr, that naturally means the current pane.
If a separate Herdr pane is wanted, create or focus that pane manually, enter the
exact checkout there, and run `dev prompt open ...` from that terminal.

This is separate from `dev start --run '<shell command>'`. That command may
dispatch shell text only to the exact root pane returned for a newly created
first-class Herdr worktree; reused, fallback, unverified, and non-Herdr surfaces
fail closed. `prompt open` does not widen or reuse that exact-pane contract.

## What the closeout classifications mean

`session-close` reports **runtime closure only**. A recognized covering agent in
`idle` or `done` may satisfy the activity gate, but that only says its current
turn settled. It does not prove that changes were committed, artifacts were
finalized, review completed, or task intent is done. Caller-contained,
mixed-purpose, active, unrecognized, and insufficiently observed sessions do not
become close candidates.

`workspace-closeout` is broader, but still advisory. Its retirement audit checks
target kind, worktree registration and path, status availability, cleanliness,
in-progress Git operations, known base and branch containment, task state,
artifact reachability/finalization, and runtime eligibility. Only a deterministic
`retirement.status` of `eligible` may be suggested for retirement, and even that
is not authorization: `dev retire` must recollect and revalidate before changing
anything. A merged pull request is evidence only and cannot substitute for any
of those gates.

## Rebase conflicts

Start with the deterministic transaction rather than an agent:

```bash
dev done <task> --ff
# or, for an update of the current branch
dev git pull-rebase
```

If Git stops at a conflict, stay in that exact checkout and open the full
workspace context there:

```bash
dev prompt open workspace-closeout . --agent my-agent
```

Use the conversation to decide the semantic resolution. Only after the operator
chooses should the Git rebase be continued or aborted. Then rerun the original
lifecycle command so it derives fresh state. The prompt launcher never resolves
the conflict, chooses continue versus abort, force-pushes, or grants cleanup
permission on its own.

## Scheduling and statelessness

`dev` has no daemon, queue, or scheduler. Every `render`, `run`, or `open`
invocation collects a fresh snapshot and forgets the agent's answer when the
process exits. A scheduler may invoke a non-interactive `run`, but must choose a
configured batch command and accept its timeout/error behavior. `open` is a
foreground TTY operation and is not a scheduled-job surface.

## Related pages

- [Pull request inbox](pull-request-inbox.md)
- [Agent-safe retirement](agent-safe-retirement.md)
- [Parallel agents and runtimes](parallel-agents-runtimes.md)
- [Commands and configuration](../reference/commands-config.md)
- [Compatibility and known limitations](../reference/compatibility.md)
