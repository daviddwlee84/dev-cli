---
description: Find the dev-cli command groups, generated exact flags, configuration layers, and stable automation surfaces.
authority: project
status: generated-plus-authored
verified_on: 2026-08-29
---

# Commands and configuration

Use the authored map for intent and the embedded generated reference for exact flags. The generated block comes from the binary's Cobra command tree and is checked by `dev skill sync --check`.

## Command map

| Goal | Commands |
|---|---|
| task lifecycle | `start`, `park`, `resume`, `done`, `retire`, `sweep`, `ls`, `status` |
| agent artifacts | `prepare`, `artifact finalize`, `artifact list`, `artifact discard` |
| guarded Git transactions | `git uncommit`, `git recommit`, `git pull-rebase`, `git amend-all`, `git setup` |
| linked worktrees | `wt list`, `wt create`, `wt open`, `wt rm`, `wt plan`, `wt provision` |
| repositories/remotes | `repo list`, `repo context`, `repo new`/`repo create`, `repo clone`, `repo setup`, `repo open`, `repo sync`, `repo remote`, `repo mark` |
| repository quick notes | `note add`, `note list`, `note show`, `note search`, `note edit`, `note delete`, `note path`, `note reindex` |
| machine inventory | `bootstrap`, `adopt`, `doctor` |
| experiments | `try`, `tries …`, `graduate` |
| terminal UI | `tui`, `tui tools` |
| configuration/shell | `config init/show/path/edit/trust`, `config scaffolds init/show/path/edit`, `shell-init`, completion |
| remote fleet | `fleet list`, `fleet status`, `fleet sync`, `fleet open`, `fleet config …` |
| agent skills | `skill list`, `skill add`, `skill update`, `skill install`, `skill sync`, `skill print` |
| generated policy/assets | `gitignore`, `skill install/sync` |
| activity/data | `summary`, `journal`, `stats …`, `cache …` |
| help | `help [topic]` |

Run `dev <command> --help` for the installed binary; this site describes the repository version identified in its freshness metadata.

## High-value structured interfaces

```bash
dev ls --json
dev repo list --json
dev repo context [repo]
dev repo remote --json
dev repo new NAME --json
dev repo clone <ref> --json
dev repo setup [repo-or-path] --preset PRESET --json
dev note list [repo] --json
dev note search <query> --json
dev note show <note-id> --json
dev bootstrap --json
```

Prefer JSON or the agent-ready Markdown context over parsing human tables. Tables are optimized for terminals and may change columns/width without changing the structured contract.

Every `dev repo list --json` row includes `notes.count`. When a latest note exists, the same object adds `notes.latest_id`, `notes.latest_preview`, and `notes.latest_updated`; these optional fields are omitted when the count is zero. `dev note list --json` and `dev note search --json` return arrays of complete note records, while `dev note show --json` returns one complete record.

## Repository bootstrap

Repository acquisition and setup are separate operations so a Git URL never
silently changes the meaning of `new`:

| Command | Behavior |
|---|---|
| `dev repo new` | interactive new-repository wizard |
| `dev repo new NAME` / `dev repo create NAME` | create a new local repository; the explicit-name default remains the compatible `minimal` preset |
| `dev repo clone [owner/name\|url]` | clone into the configured destination, then optionally apply a preset; setup defaults off |
| `dev repo setup [repo-or-path]` | repeat-safely merge native initializers and preset files into an existing clean checkout; defaults to the current repository and does not commit unless requested |

Across these commands, controls include `--preset`, `--path`, typed input values through
`--set`, item selection through `--enable`/`--disable`, `--dry-run`, `--yes`,
`--json`, and `--handoff <stay|cd|open|start>`. JSON mode is non-interactive
and never changes directory or opens a runtime. Dry-run performs no target
repository mutation; clone setup can only be planned in detail after the clone
exists. Use the generated reference below for each command's exact flag
availability.

The wizard renders the selected scaffold and workflow summary before target
repository mutation. The built-in presets are:

- `minimal`: `main`, README, and an initial commit; this preserves existing
  scripted `repo new NAME` behavior.
- `agent-ready`: extends `minimal` with common ignores, starter `AGENTS.md`,
  and project-scoped `.claude/settings.json` plus `.claude/plans/`.
  `agent-history-hygiene` and `project-knowledge-harness` are offered but are
  not selected silently. When selected, dev installs them and runs reviewed
  built-in initializers for their project surfaces before the initial commit;
  downloaded skill scripts are not executed for these built-ins.

Presets can add typed `string`, `bool`, and `choice` inputs, text templates,
hooks, and project skills. Hooks run in fixed `before_commit`, `after_commit`,
and `after_remote` phases. The safe hook form is an argv `command`; a shell
`run` is explicit, and only `interactive = true` loads an interactive shell.
Required failures stop later commit/remote steps; optional failures are
reported as warnings. Produced local files are retained for recovery. Native
initializers and preset files are repeat-safe; custom hooks and skill setup are
responsible for their own idempotency.

### Upstream publishing

Before offering publication, dev probes `gh` and `glab` without changing
state. A provider is offered only when its CLI is installed and authenticated;
otherwise the wizard gives the relevant installation or login guidance.
Local-only is the default, and a newly published repository defaults private.

Publishing uses the local repository name and configured description, then asks
for the provider namespace/owner, visibility, and whether to push the
initial/current branch. Dev creates the empty GitHub or GitLab repository after
required local setup and commit steps succeed, then adds/verifies `origin` and
optionally pushes with upstream tracking. A
provider, name-conflict, or push failure never deletes the local checkout or
an upstream that was already created.

Publishing from `repo setup` requires `--commit`, so the newly created
upstream cannot omit the generated setup changes.

### Handoff

`stay` prints the result only. `cd` uses the trusted `shell-init` wrapper to
change the parent shell. `open` opens the configured Herdr/tmux/Zellij runtime
and falls back to `cd` when runtime is `none`. `start` continues into the
existing task wizard with the repository fixed, and is unavailable when setup
leaves uncommitted files that a new worktree would omit. Neither repository
bootstrap nor `dev start` launches a coding agent.

## `dev done` finish flags

`dev done` for branch/worktree tasks integrates through exactly one of `--ff` (rebase onto the base, then fast-forward it) or `--pr` (push and open a pull/merge request). Omitting both opens the interactive finish wizard on a TTY — see [Change-stream workflow](../guides/change-stream-workflow.md) for the prompts.

A dirty checkout is handled by `--dirty <auto|fail|commit|discard>` (default `auto`):

| Value | Behavior |
|---|---|
| `auto` | interactive: prompts to commit or discard; non-interactive: fails, same as `fail` |
| `fail` | refuses to finish with a dirty checkout |
| `commit` | commits everything with `--message`/`-m` (prompted interactively if omitted) |
| `discard` | resets tracked changes and removes untracked files; destructive, requires `--yes` outside a TTY |

`--yes`/`-y` confirms the selected finish plan; it is mandatory for a non-interactive `--dirty discard` and otherwise skips the interactive confirmation step. `--keep-worktree` keeps the worktree after `--ff` integration (default: removed once merged), `--push` also pushes the resulting branch, and `--delete-branch` deletes the branch once its commits are contained in the base — never a branch with unpushed commits.

## Configuration

```bash
dev config init
dev config show
dev config path
dev config scaffolds init
dev config scaffolds show
dev config scaffolds path
dev config scaffolds edit
```

`config init` detects local roots and writes explicit defaults. A missing config file is allowed; the built-in defaults keep core Git behavior usable, but generated config is recommended because it makes machine policy reviewable.

Key sections:

| Section | Controls |
|---|---|
| `[paths]` | scan roots, project/tries/worktree roots, worktree template, state path |
| `[runtime]` | `auto`, Herdr, tmux, Zellij, or none plus metadata settings |
| `[worktree]` | ignored includes, linked dirs, setup commands, strategies, timeout |
| `[forge]` / `[[forge.azure_devops]]` | complete remote inventory cache TTL and opt-in Azure organization/project targets |
| `[bootstrap]` | recursion, symlink handling, index/layout policy |
| `[tui]` / `[[tui.tools]]` | columns, sorting, and external-tool bindings |
| `[stats]` | sampler and optional WakaTime import |
| `[update]` | `check` (default `true`) — allow the once-a-day "newer release available" hint and its background cache refresh; `DEV_NO_UPDATE_CHECK` overrides it |

Repository quick-note Markdown is durable under configured `paths.state_dir/notes`, which defaults to `$XDG_DATA_HOME/dev/notes`. The full-text index at `$XDG_CACHE_HOME/dev/notes.db` is disposable and rebuilds from those files; changing `paths.state_dir` does not move the cache.

### Scaffold presets

Global repository recipes live at `$XDG_CONFIG_HOME/dev/scaffolds.toml` (or
the root `--scaffolds` override). Every authored file declares `version = 1`.
A compact preset looks like:

```toml
version = 1
default_preset = "team"
default_agents = ["claude-code", "codex"]

[presets.team]
extends = "agent-ready"
handoff = "cd"

[[presets.team.inputs]]
id = "deployment"
type = "choice"
choices = ["none", "docker"]
default = "none"

[[presets.team.files]]
id = "service-readme"
source = "service/README.md" # templates/service/README.md beside this file
destination = "docs/service.md"

[[presets.team.hooks]]
id = "verify"
phase = "before_commit"
command = ["make", "test"]
required = true

[[presets.team.skills]]
id = "knowledge"
source = "daviddwlee84/agent-skills/skills"
name = "project-knowledge-harness"
agents = ["claude-code", "codex"]
default = true

[presets.team.skills.setup]
phase = "before_commit"
interpreter = "bash"
script = "scripts/init.sh"
args = ["--target", "{{path}}", "--project-name", "{{name}}"]
required = true
```

Skill setup normally names a project-local script inside the installed skill.
The shipped recommendations instead use `builtin = "agent-history-hygiene"` or
`builtin = "project-knowledge-harness"`; these fixed reviewed initializers do
not execute downloaded skill code.

A preset may extend one parent. Scalars override; simple lists replace; files,
hooks, and skills merge by `id`, and an inherited item can be disabled with
`enabled = false`. Template sources must remain in the `templates/` tree next
to their config source, destinations must remain inside the repository, and a
skill setup script must remain inside the installed skill directory.

### Safe project overlays

A repository may commit these fixed files:

- `.dev-cli/config.toml`: allowlisted worktree provisioning and repository
  setup wizard defaults.
- `.dev-cli/scaffolds.toml`: project presets, templates, hooks, and skill
  setup using the same versioned schema.

```toml
# .dev-cli/config.toml
version = 1

[worktree]
include = [".env.example"]
strategy = "reinstall"

[repo.setup]
preset = "team"
handoff = "cd"
commit = false
```

Effective precedence, lowest to highest, is built-ins, global config/scaffolds,
legacy `.dev.toml`, the target repository's `.dev-cli/*`, then explicit CLI or
wizard choices. `.dev.toml` remains readable for compatibility; new project
configuration should use `.dev-cli/config.toml`. Global `default_preset` and the
project `[repo.setup]` preset, handoff, and commit fields seed interactive
wizard choices; they do not change scripted defaults, which are controlled by
the corresponding flags.

Project files cannot override host paths, state location, runtime backend,
forge inventory or credentials, stats, update, bootstrap, or TUI policy. They
also cannot silently publish a repository. Before a post-create command from
`.dev-cli/config.toml`, or a hook or skill setup from project
`.dev-cli/scaffolds.toml`, executes, dev asks to trust the canonical repository
plus an execution-content hash. Changed executable content requires new
consent; non-interactive use without a matching trust record fails closed.
Legacy `.dev.toml` retains its compatibility behavior. Keep credentials and
host-specific paths in user config or ignored environment files, never in a
committed project overlay.

Project-authored skill setup must use a local source so its bytes can be bound
to the trust hash. Remote project skills may still be installed, but cannot
declare executable setup; global presets remain host-owned policy.

## Colored output

Every human-readable surface applies semantic ANSI color through a small set of roles: `title`/`header`/`prompt` (bold cyan), `label`/`dim` (dim), `success` (green), `warning` (yellow), `danger` (bold red), `review` (magenta, for a PR/review handoff), and `strong`/`code` for Markdown. Values that carry their own meaning are colored by that meaning rather than by a fixed role:

| Value | Green | Yellow | Red |
|---|---|---|---|
| Git status | `clean` | `dirty`, `ahead`, `behind`, `no checkout` | `conflict`, `error` |
| Task state | `hot`, `done` | `warm`, `cold`, `parked` | — |
| Fleet host | `ok` | `stale`, `no-dev` | `unreachable`, `timeout`, `incompatible` |
| Skill update | `current` | `update` | `missing`, `failed` |
| Artifact intent | `finalized` | `armed`, `finalizing` | `failed` |

`dev journal` and `dev summary` emit Markdown, so their headings and fenced code blocks are styled the same way `dev help <topic>` styles a quick-reference page. In command help, only the names you can type — command names and flag specs — are colored; descriptions stay plain, and the column alignment cobra computes is unaffected because a terminal gives an escape sequence no width.

Control it with the global `--color <auto|always|never>` flag (default `auto`). `auto` disables color when output is not attached to a terminal, when `NO_COLOR` is set to any non-empty value, or when `TERM=dumb`. The setting reaches the interactive dashboard as well, so `dev --color never` renders it without color. `--json` output is never colored regardless of mode. There is no config-file field for color — `--color` and the environment are the only controls, so piping `dev` never requires `--color never` to stay clean.

## Shell integration

```bash
eval "$(dev shell-init zsh)"
dev shell-init fish | source
```

The trusted `shell-init` output defines a wrapper because a child process cannot change its parent's working directory. For navigation commands, that wrapper reads a NUL-terminated path from a private child-only file descriptor and calls `builtin cd`; it does not evaluate ordinary `dev` command output as shell code.

## Complete generated command reference

The following content is included from `internal/skill/dev-cli/references/commands.md`, beginning after its generated-file preamble:

--8<-- "internal/skill/dev-cli/references/commands.md:7"

## Keeping it current

```bash
go run ./cmd/dev skill sync --check
```

If command help changes, regenerate through `dev skill sync`; do not hand-edit the generated block.

## Sources

- [`internal/cli/root.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/root.go)
- [`internal/config/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/config/config.go)
- [`internal/scaffold/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/scaffold/types.go)
- [`internal/projectconfig/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/projectconfig/types.go)
- [`internal/skill/dev-cli/references/commands.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/commands.md)
- [`internal/cli/color.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/color.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
