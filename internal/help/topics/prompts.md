# Prompt handoffs

How to give deterministic dev context to a configured local agent without
making `dev` an agent harness or a second lifecycle authority.

## Escalate only as far as needed

```text
dev status / dev sweep / dev repo context
    |
    +-- enough facts? use done, park, sweep, or retire directly
    |
    +-- dev prompt render <recipe>   inspect or copy the exact prompt
    +-- dev prompt run <recipe>      one-shot analysis; no user stdin
    +-- dev prompt open <recipe>     foreground conversation in this terminal
```

All modes collect the same read-only deterministic recipe context. The receiver
may explain and prioritize it; `dev` never parses the reply, loops, mutates
state, or turns advice into lifecycle authorization.

## Built-in recipes

```bash
dev prompt list
dev prompt list --json
dev prompt render pr-triage
dev prompt render session-close
dev prompt render workspace-closeout [repo-or-checkout]
```

- `pr-triage` uses the `dev pr list` query/filter surface and prioritizes
  requests authored by or awaiting review from the user.
- `session-close` classifies **runtime closure only**. `idle`/`done` says the
  current turn settled, not that work, artifacts, review, or task intent is
  complete.
- `workspace-closeout` joins one repository's tasks, all known checkouts, Git,
  runtime evidence, artifacts, and PRs. It includes a full deterministic
  retirement audit; a merged PR and even `eligible` remain advisory.

Missing capabilities and failed evidence stay explicit warnings/unknowns. Every
prompt embeds envelope `schema_version: 1`, recipe/context versions, time, host,
scope, optional target, capabilities, warnings, and recipe context. Collected
strings are untrusted data, not instructions.

## Render, run, and open

| Mode | Behavior |
|---|---|
| `render` | Print the exact prompt. No agent configuration or process is needed. |
| `run` | Start one batch process, never pass user stdin, wait with a default 10-minute timeout. Prompt input may be stdin/file/argv. |
| `open` | Start one foreground process attached to the current TTY. Prompt input must be file/argv so stdin stays conversational. No default timeout. |

```bash
dev prompt run session-close --agent my-agent
dev prompt open workspace-closeout . --agent my-agent
dev prompt open workspace-closeout . --dry-run
```

Dry-run prints the resolved agent, mode, cwd, transport, timeout, safe command
markers, and complete prompt without starting anything. `open` otherwise needs
an interactive terminal. A real run/open in a checkout is a new writer claim
and uses the shared-checkout occupancy guard even though the recipe requests
read-only analysis. The invoking agent's pane is not excluded; sharing remains
an explicit coordinated override.

## Host-only configuration

There is no built-in vendor, agent, or launcher:

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

`--agent NAME` selects explicitly; otherwise the unique `default = true` entry
wins; otherwise a sole configured agent wins. Multiple agents with no default
fail as ambiguous. Names are required, have no surrounding whitespace, compare
case-insensitively, and must be unique. At most one is default. An agent must
configure run or open; one mode never silently borrows the other.

Each launcher sets exactly one direct `command` argv or static `shell`, plus
`input` and optional shell-only `load_shell_rc`. `run` may set a non-negative
`timeout`; `open` rejects one. Direct argv is preferred. Static shell uses `$SHELL -c`, or
`-lic` when shell RC loading is explicit. Prompt text is never interpolated into
shell text.

Transport rules:

- `stdin`: run only; no placeholder.
- `file`: direct command needs exactly one whole `{{prompt_file}}` element;
  shell must reference `$DEV_PROMPT_FILE`/`${DEV_PROMPT_FILE}`. The temporary
  prompt is private (0700 directory, 0600 file) and removed afterward.
- `argv`: direct command only, exactly one whole `{{prompt}}` element, maximum
  prompt size 100 KiB.

Embedded placeholders such as `--prompt={{prompt}}` are rejected. `[[agent]]`
is host executable policy and is denied in repository `.dev-cli/config.toml`.

## Terminal, runtime, and permissions

`dev prompt open` stays in the terminal that invoked it. It does not create,
focus, reuse, or inject into Herdr/tmux/Zellij. Inside Herdr it naturally stays
in the current pane. For a separate Herdr pane, create/focus one manually, enter
the exact checkout, then run `prompt open` there.

`dev start --run` is separate: it dispatches shell text only to the exact root
pane returned for a newly created first-class Herdr worktree. Prompt handoff does
not weaken that proof.

The child retains the permissions of its configured command. `dev` provides no
sandbox, adds/removes no permission mode, answers no approval prompt, and never
runs a command quoted by the reply. Review advice, then invoke `done`, `park`,
`sweep`, or `retire`; those commands recollect and revalidate fresh state.

## Rebase conflicts

Use a deterministic path first:

```bash
dev done <task> --ff
# or
dev git pull-rebase
```

If Git stops at a conflict, stay in that exact checkout and run:

```bash
dev prompt open workspace-closeout . --agent my-agent
```

Discuss the semantic resolution, then explicitly continue or abort the Git
operation and rerun the original lifecycle command. The prompt launcher never
chooses, resolves, force-pushes, or grants cleanup permission.

## Scheduling and statelessness

There is no daemon, queue, or scheduler. Each invocation collects a fresh
snapshot and forgets the reply after process exit. External schedulers may call
a configured non-interactive `run`; `open` is a foreground TTY operation.
