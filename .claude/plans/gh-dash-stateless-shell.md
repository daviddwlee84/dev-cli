# Generalize prompt handoff beyond pull requests

## Context

Commit `694cb70` added a useful first slice: `dev pr list`, three PR-specific
prompt assets, and a configurable `[[agent]]` command. The next use cases expose
that the handoff is the reusable product, not the PR command:

- decide which Herdr agent sessions can close;
- decide which tasks/worktrees in one repository should finish, park, retire,
  or be inspected;
- escalate a hard integration/rebase/conflict case to an interactive agent,
  while ordinary cases continue to use deterministic `done`, `sweep`, and
  `retire` commands.

The current `dev pr prompt --agent` is only partly suitable. It always starts a
foreground child and waits. With the default `input = "stdin"`, stdin is the
finite prompt, so the user cannot continue a conversation. Its
`interactive = true` flag only loads a login shell; it does not create a TTY,
open Herdr, or make an agent conversational. File/argv input happens to preserve
`app.In`, but this is not an explicit contract. The child also inherits whatever
directory `dev` was invoked from, rather than the selected checkout.

The intended result is therefore a small, generic escalation ladder:

```text
existing deterministic command/report
    -> dev prompt render   # inspect/copy the exact prompt
    -> dev prompt run      # one-shot, unattended advice
    -> dev prompt open     # foreground conversation in the current terminal
    -> user explicitly runs/approves done, park, sweep, retire, rebase, ...
```

`dev` remains a context and lifecycle tool, not an embedded agentic workflow.
It will not parse replies, iterate, change an agent's permission mode, or turn a
model's conclusion into cleanup authorization.

## Recommended command surface

Create one generic command family. Generate each recipe as a Cobra child below
each mode so recipe-specific flags, usage, and completions remain discoverable.

```text
dev prompt list [--json]

dev prompt render pr-triage [query] [PR flags]
dev prompt render session-close
dev prompt render workspace-closeout [repo-or-checkout] [--base REF]

dev prompt run  <same recipes> [--agent NAME] [--dry-run]
dev prompt open <same recipes> [--agent NAME] [--dry-run]
```

Mode semantics are deliberately different:

- **`render`** performs read-only context collection and writes only the final
  Markdown prompt to stdout. No configured agent is needed.
- **`run`** is batch/non-interactive. It selects `--agent`, then the configured
  default, then the sole configured agent. It never inherits user stdin:
  stdin transport receives the finite prompt; file/argv transport receives EOF.
  It streams stdout/stderr, waits, and has a default 10-minute timeout. This is
  the mode for `opencode run ...`, cron, or another scheduler.
- **`open`** is a foreground interactive child in the **current terminal**. It
  requires a TTY, inherits stdin/stdout/stderr, waits, and has no default
  timeout. Prompt transport must be file or direct argv so stdin remains
  available for the conversation. The recipe determines `Cmd.Dir`; for a
  repository recipe this is the exact resolved checkout. If invoked inside a
  Herdr pane, the agent naturally stays in that pane.
- **`--dry-run`** on `run`/`open` still performs read-only collection and
  rendering, but creates no temporary file and starts no process. It prints
  the mode, selected agent, resolved cwd, transport, timeout, a redacted
  command preview, and the exact prompt.

Remove `dev pr prompt` before release rather than keep two public models: the
published baseline is `v0.2.4`, and that subcommand exists only in the
unreleased feature commit. Keep `dev pr list`; update `dev pr` wording so it no
longer claims to launch agents. Replace all branch docs/examples rather than
adding a compatibility layer for an unpublished command.

## Agent configuration: separate batch from interactive launch

Replace the current flat pre-release schema (`command`/`run`/`input`/
`interactive`/`timeout`) with nested launchers:

```toml
[[agent]]
name = "opencode"
default = true

[agent.run]
command = ["opencode", "run", "{{prompt_file}}"]
input = "file"
timeout = "10m"

[agent.open]
command = ["opencode", "{{prompt_file}}"]
input = "file"
```

The exact OpenCode/Claude/Codex examples must be checked against each installed
CLI's current help before documentation is written; examples are not built-in
defaults.

Schema:

```go
type Agent struct {
    Name    string   `toml:"name"`
    Default bool     `toml:"default"`
    Run     Launcher `toml:"run"`
    Open    Launcher `toml:"open"`
}

type Launcher struct {
    Command     []string `toml:"command"` // direct argv, preferred
    Shell       string   `toml:"shell"`   // static host-owned shell text
    Input       string   `toml:"input"`   // stdin | file | argv
    LoadShellRC bool     `toml:"load_shell_rc"`
    Timeout     Duration `toml:"timeout"`
}
```

Rules enforced by `config.Validate` and by the process package:

- An agent needs at least one of `[agent.run]` or `[agent.open]`; at most one
  agent is default; names are trimmed, non-empty, and case-insensitively unique.
- A launcher has exactly one of non-empty `command` or `shell`; reject blank
  `command[0]`.
- `command` bypasses the shell. `shell` is static text; reject every `{{...}}`
  placeholder in it. `load_shell_rc` is valid only with `shell` and means just
  that — it is not interactivity.
- `stdin` is valid only for `run` and contains no placeholder.
- Direct `file` transport requires exactly one whole argv element
  `{{prompt_file}}`. For shell/file transport, inject `DEV_PROMPT_FILE` and
  require the shell text to reference `$DEV_PROMPT_FILE` or
  `${DEV_PROMPT_FILE}`; never splice the path or prompt into shell text.
- `argv` is direct-command only and requires exactly one whole argv element
  `{{prompt}}`; reject prompts above a conservative argument-size bound.
  Embedded forms such as `--prompt={{prompt}}` are rejected.
- `open` rejects stdin transport and requires terminal stdin/stdout. It inherits
  the process environment and terminal streams; `run` gets EOF unless stdin is
  the prompt transport.
- `run` defaults to a 10-minute timeout. `open` has no timeout and rejects one:
  safely cancelling an interactive descendant tree requires terminal job
  control, unlike the isolated process group used by batch mode.
- Prompt files live in a private `0700` temporary directory, mode `0600`, for
  the child lifetime only. Errors redact prompt contents and temporary paths.
- No process replacement (`syscall.Exec`): a child preserves cleanup, error
  reporting, Windows behavior, and return to the shell wrapper.
- No configurable environment/secret map in this version. Normal inherited
  environment or a reviewed wrapper executable covers that without making
  `dev` a credential store.

`[[agent]]` remains host-only. Keep `agent` in
`internal/projectconfig/load.go:deniedTopLevelSections` and strengthen the test
to assert a `DiagnosticDenied`, not merely that the command did not run.

## Architecture

### 1. `internal/handoff`: process and transport only

Extract the generic parts of `runAgent`/`agentProcess` from
`internal/cli/pr_prompt.go` into a package with no PR, forge, runtime, Cobra, or
`App` knowledge:

- normalized `Launcher`/`Mode`/`Spec`;
- validation of command versus shell and transport placeholders;
- private prompt-file lifecycle;
- explicit cwd and injected IO;
- batch EOF versus inherited TTY behavior;
- timeout/cancellation and a dry-run `Preview` with redactions.

The CLI converts `config.Launcher` into `handoff.Spec`; cross-agent uniqueness
and default selection stay in `internal/config`.

### 2. `internal/templatex`: one-pass scalar rendering

Move the pure `scaffold.RenderTemplate` implementation
(`internal/scaffold/template.go:93-190`) into a lower-level package and keep
`scaffold.RenderTemplate` as a compatibility wrapper. Prompt templates use one
placeholder, `{{context_json}}`.

This fixes the current renderer's correctness bug: it substitutes JSON and then
rescans provider-controlled content, so a PR title containing `{{value}}` is
mistaken for an unknown template variable. The shared renderer validates only
the template and never reparses inserted values.

### 3. `internal/promptkit`: registry, envelope, templates

This package owns no collection policy. It provides:

```go
type Recipe struct {
    Name, Summary, TargetUsage string
    ContextVersion int
    Template string
}

type Snapshot struct {
    Scope string
    Target Target
    WorkingDirectory string
    Capabilities []Capability
    Warnings []Warning
    Context any
}
```

A sorted registry rejects duplicate names and supports `prompt list`. Cobra
recipe factories bind typed options to a provider callback and return a
`Snapshot`.

Every template receives this versioned JSON envelope:

```json
{
  "schema_version": 1,
  "recipe": "workspace-closeout",
  "context_version": 1,
  "generated_at": "2026-09-02T12:00:00Z",
  "host": "host",
  "scope": "repository",
  "target": {
    "kind": "checkout",
    "name": "demo/feat-x",
    "path": "/absolute/worktree",
    "working_directory": "/absolute/worktree"
  },
  "capabilities": [
    {"name": "runtime-agent-activity", "available": true, "detail": "herdr"}
  ],
  "warnings": [
    {"source": "gitlab", "code": "unauthenticated", "message": "signed out", "action": "..."}
  ],
  "context": {}
}
```

Rules:

- one timestamp is created per invocation and passed through every builder;
- `schema_version` changes only for a breaking common-envelope change;
  each recipe owns its `context_version`;
- fields are add-only within a version;
- partial forge/runtime/artifact failures become capabilities/warnings, not
  silent absences; a failure that prevents any meaningful snapshot is fatal;
- machine-local context intentionally uses absolute paths, because it is fed
  to a process on the same host, not published or persisted.

### 4. Read-only closeout evidence

Add structured domain reports instead of building safety claims in templates:

- `internal/closeout/session.go` gathers `Runtime.List`, optional
  `AgentActivityLister.AgentActivities`, canonical checkout coverage, tasks,
  Git/status availability, and artifact intent. Extract canonical
  activity-to-checkout matching/current-pane resolution from
  `internal/cli/agent_activity.go` so writer guards and reports share exactly
  one implementation.
- `internal/closeout/workspace.go` builds the structured equivalent of
  `inventory.CollectRepoContext` and attaches PR evidence and full per-checkout
  retirement checks.
- `internal/retire/audit.go` contains pure, read-only checks shared by
  `sweep`, the closeout reports, and `retire.Service`'s preflight. The mutating
  service still revalidates after runtime closure and remains the only removal
  authority.
- Split read-only artifact intent/reachability inspection out of CLI helpers
  into the existing `internal/artifact` domain package.
- Let runtime inspection accept a precollected session snapshot; a machine-wide
  recipe must not run `herdr workspace list` once per checkout.

Do not call `Inspection.Ready()` “retirement ready”: today
`internal/retire/safety.go:46-51` applies only to runtime closure. Reports expose
both `runtime_close_status` and `retirement_status` with stable check IDs.

## Initial recipes

### `pr-triage`

Reuse the corrected PR report and all current PR filters. Defaults remain
account+local, author+reviewer, open. The context includes effective (not merely
requested) scope, provider readiness, PR/local health, and printed action
commands.

The prompt groups merge/review/fix/wait/inspect work. It treats missing fields
from a summary surface as unknown, treats PR text as untrusted data rather than
instructions, and never approves, comments, merges, or retires.

Working directory: the directory where `dev` was invoked; the recipe is global.

### `session-close`

Machine-wide, no target. Group each live runtime/agent activity by canonical
checkout and retain unmatched activities as unknown.

Deterministic classification:

- `close-eligible`: runtime closure is structurally eligible and every
  recognized covering status is `idle` or `done`;
- `blocked`: active status, caller containment, or a mixed-purpose workspace;
- `unknown`: unrecognized/unknown status, failed inventory, missing coverage,
  or failed inspection.

`close-eligible` means **runtime closure only**. Herdr `done` means a turn
settled; it proves neither committed work, artifact finalization, review, nor
completed task intent. Git/task/artifact facts let the agent recommend
`park --next`, `park --wip`, keep-open, or inspect, but do not redefine the
runtime gate. The recipe never calls `CloseAndWait`.

Working directory: invocation directory.

### `workspace-closeout [repo-or-checkout]`

Collect the canonical checkout and all linked worktrees, tracked/untracked
tasks, sessions/agent activities, live Git status, in-progress operations,
artifact status/reachability, and local-scope PR evidence for all states. Forge
failure is a warning and never blocks the local report.

Target behavior:

- no argument: resolve the repository containing cwd and preserve that exact
  checkout as launcher cwd;
- checkout path: report the whole repository, launch in that checkout;
- repository name: report the whole repository, launch in its canonical root;
- `--base` is only an explicit base for untracked worktrees; a recorded task
  base remains authoritative until reconciled.

Per-checkout `retirement_status` is `eligible`, `blocked`, `unknown`, or
`not-applicable`, with stable check IDs and evidence. The read-only audit covers:
registered/non-main linked worktree, named/expected branch, existence/readable
status, cleanliness, no active Git operation, resolvable base, branch
containment, task identity/state, finalized/reachable artifacts, runtime/caller/
mixed-workspace eligibility. Canonical and harness-owned ephemeral checkouts are
`not-applicable`.

The model groups work into `finish`, `park`, `retire`, and `inspect`, quoting
existing deterministic commands only. A forge-reported merged PR never turns a
failed ancestry/artifact/runtime check into eligibility.

This recipe also covers the rebase/conflict escalation:

1. inspect with `workspace-closeout`;
2. use existing `dev done --ff` or `dev git pull-rebase` for the ordinary path;
3. if Git stops at a conflict, run `dev prompt open workspace-closeout` from the
   exact checkout;
4. the prompt requires the agent to explain the state and ask before
   rebase/abort/reset/force/retire actions;
5. rerun `done`/`sweep`/`retire`, which re-read and revalidate state.

There is no separate rebase workflow in this version.

## Runtime/Herdr decision

Ship only foreground current-terminal `open`.

`dev start --run` is not reusable as a generic interactive launcher: it accepts
opaque shell text only for the exact root pane returned by the same newly
created first-class Herdr worktree response. It deliberately rejects reuse,
fallback, malformed/missing root data, tmux, Zellij, and `none`; it has no stdio,
wait, or exit-status channel. A persisted handle or focused/current pane is not
an equivalent proof.

Therefore `dev prompt open` does **not** call `Runtime.Open`, `Activator`, or
`PaneRunner`, and has no `--herdr` flag. Inside Herdr it runs in the current
pane; outside it runs in the current terminal. A new runtime surface is deferred
until an optional backend-neutral capability can model require-new versus
reuse, topology, creation-correlated transient target, argv versus shell text,
detached versus foreground/wait semantics, and stdio/exit status. Never infer a
launch target from a saved handle, focused pane, label, directory search, or
reused surface.

For a separate Herdr surface today, the documented safe manual workflow is:
create/focus a fresh pane or workspace with Herdr, change to the target checkout,
then run `dev prompt open ...` there. Do not disguise that manual step as a
cross-backend feature.

## Fix existing PR correctness before extraction

The generic layer must not fossilize these current bugs:

1. Per-repository GitHub/GitLab queries must honor requested roles and populate
   `roles`; add an explicit any-role mode used only by workspace closeout.
2. Normalize each `--repo` selector once. A URL becomes canonical forge plus
   `owner/name`; a bare `owner/name` applies to matching ready providers.
   Restrict both account rows and local targets.
3. Return normalized/effective collection options, so an account-to-local
   fallback cannot report the wrong scope in JSON/prompt context.
4. Local joins expose `checkout_exists`, registration and status availability,
   and status error. Prefer a readable live `Status.Branch`; a recorded branch
   fallback is marked as such. Missing/unreadable state is never represented as
   clean zero-values.
5. Render missing/unreadable checkout state distinctly in tables and JSON.
6. Replace the PR-only retire prompt with `workspace-closeout`; do not retain
   its broken default of open PRs or contradictory ahead rules.
7. Replace `strings.NewReplacer` with the one-pass strict renderer.
8. Correct docs that say `dev pr` emits a reviewer trigger phrase: current
   actions contain a generic body placeholder only.
9. Add `schema_version` and document the complete PR JSON/config contract in
   the paired commands/config pages.

## Critical files and sequence

1. **Normalize PR reports**
   - `internal/forge/{pr,github,gitlab}.go`
   - `internal/cli/{pr,pr_collect}.go`
   - focused forge/CLI regression tests for roles, repo selectors, effective
     scope, missing/cold/wrong-branch joins, and hostile brace content.

2. **Create reusable evidence**
   - add `internal/retire/audit.go`, `internal/closeout/{session,workspace}.go`,
     and read-only artifact inspection;
   - extract activity matching from `internal/cli/agent_activity.go`;
   - have `internal/cli/sweep.go` and `internal/retire/service.go` reuse the same
     check helpers without changing report-before-apply or revalidation order.

3. **Create generic prompt/handoff core**
   - add `internal/templatex`, retaining a wrapper in
     `internal/scaffold/template.go`;
   - add `internal/promptkit` registry/envelope/templates;
   - add `internal/handoff` process/transport implementation;
   - replace the flat agent schema in `internal/config/config.go` and the
     commented starter config; retain project-config denial.

4. **Wire CLI**
   - add `internal/cli/prompt_command.go` and recipe-specific factories;
   - register in `internal/cli/root.go`, TLDR/help topics and completions;
   - remove `newPRPromptCmd`, PR-only launcher helpers, and old prompt assets;
   - leave `dev pr list` as the PR inventory surface.

5. **Tests**
   - template one-pass/malformed/unknown tests, including provider strings with
     `{{...}}`;
   - registry order/duplicate/version/envelope tests;
   - nested agent config validation and default/singleton selection;
   - `run`: stdin/file/argv, EOF for non-stdin transport, timeout, cwd,
     permissions/cleanup, stderr separation, and no-temp dry run;
   - `open`: TTY requirement, file/argv, inherited IO, cwd, no default timeout,
     and writer-collision guard (`guardSharedCheckout`);
   - every Herdr status plus caller/mixed/failed-inventory session cases;
   - workspace dirty/conflict/in-progress/missing/containment/task/artifact/
     ephemeral/canonical cases, including “merged PR is not authorization.”

6. **Product/docs synchronization**
   - update `README.md`, `[Unreleased]` in `CHANGELOG.md`, and `TODO.md`;
   - replace the PR-only handoff section in paired
     `docs/guides/pull-request-inbox*.md`;
   - add paired `docs/guides/prompt-handoffs.md` pages with the escalation table,
     batch versus TTY behavior, cwd, permissions, and Herdr boundary;
   - update paired commands/config, compatibility, retirement,
     parallel-runtime, and sources/freshness pages;
   - add/update bundled help and
     `internal/skill/dev-cli/references/{pull-requests,prompt-handoffs}.md`;
   - run `make skill-sync`, inspect generated commands, and `make skill-check`;
   - regenerate `docs/llms*.txt` and run strict bilingual docs checks.

## Out of scope

- custom/project-authored recipes or executable prompt workflows;
- a `machine-attention` recipe (existing `dev summary --attention --json` is
  already the right deterministic surface and can join the registry later);
- a dedicated integration/rebase agent workflow;
- parsing, storing, or iterating on agent replies;
- automatic PR comments/approvals/merges, session close, worktree removal,
  branch deletion, rebase, reset, or force push;
- built-in agent vendors/default launchers or secret/environment injection;
- new Herdr/tmux/Zellij surfaces, pane injection, or persisted pane IDs;
- treating forge merge state as retirement proof.

## Verification

```bash
files="$(gofmt -l .)" && test -z "$files"
make vet
go test -race ./...
make build
make e2e

make skill-sync
make skill-check

uv sync --frozen --extra docs
uv run python scripts/check-docs.py --source --generate-llms
uv run python scripts/check-docs.py --source
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

End-to-end manual checks:

1. `dev prompt render session-close` produces a prompt whose unknown/missing
   capabilities are explicit and performs no mutation.
2. A fake batch agent confirms `run` receives the prompt, cannot read user
   stdin, runs in the expected cwd, and times out.
3. A PTY-backed fake interactive agent confirms `open` receives the initial
   prompt by file/argv and can continue reading user input without a default
   timeout.
4. From inside a Herdr pane, `open workspace-closeout` stays in that pane; from
   outside, it uses the current terminal. Neither creates/reuses/focuses a
   runtime surface.
5. `session-close` never calls `CloseAndWait`; `workspace-closeout` never calls
   removal; a merged/squashed PR with failed local checks remains blocked.
6. Existing `dev sweep`, `dev done`, `dev retire`, and `dev start --run`
   behavior and safety tests remain unchanged.
