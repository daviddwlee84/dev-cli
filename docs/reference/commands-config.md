---
description: Find the dev-cli command groups, generated exact flags, configuration layers, and stable automation surfaces.
authority: project
status: generated-plus-authored
verified_on: 2026-09-03
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
| terminal UI | `tui`, `tui tools`, independent preview `flow [repo]` |
| configuration/shell | `config init/show/path/edit/trust`, `config scaffolds init/show/path/edit`, `shell-init`, completion |
| SSH hosts | `ssh init`, `ssh list`, `ssh show`, `ssh setup`, `ssh probe`, `ssh remove` |
| remote fleet | `fleet list`, `fleet status`, `fleet machine-id`, `fleet sync`, `fleet files`, `fleet open`, `fleet config …` |
| agent skills | `skill list`, `skill add`, `skill update`, `skill install`, `skill sync`, `skill print` |
| static MCP declarations | `mcp list` |
| generated policy/assets | `gitignore`, `skill install/sync` |
| activity/data | `summary`, `journal`, `stats …`, `cache …` |
| help | `help [topic]` |

Run `dev <command> --help` for the installed binary; this site describes the repository version identified in its freshness metadata.

## High-value structured interfaces

```bash
dev ls --json
dev repo list --json
dev repo context [repo] --json
dev repo remote --json
dev fleet machine-id <host> --json
dev fleet files [repo-or-path] --to <host> --json
dev repo new NAME --json
dev repo clone <ref> --json
dev repo setup [repo-or-path] --preset PRESET --json
dev note list [repo] --json
dev note search <query> --json
dev note show <note-id> --json
dev ssh init --json
dev ssh list --json
dev ssh list --format tsv
dev ssh show <alias> --json
dev ssh setup <alias> --dry-run --json
dev ssh probe <alias> --json
dev ssh remove <alias> --dry-run --json
dev skill list --all --json
dev mcp list --all --json
dev bootstrap --json
```

Prefer JSON or the agent-ready Markdown context over parsing human tables. Tables are optimized for terminals and may change columns/width without changing the structured contract. `repo context --json` is an additive schema-v1 report: unavailable facts remain null/error entries with explicit provenance instead of becoming zero values. `fleet files --json` is content-free and never includes file hashes or bodies.

`dev skill list --json` keeps its existing array and keys while adding repository, checkout, installation, presence/integrity, registry, and lock metadata. `dev mcp list --json` begins with a `servers`/`diagnostics`/`coverage` envelope; exact Claude local rows add `local_project_path`. Every server field is already sanitized. Declaration state may include Claude's documented project approvals, but must not be interpreted as health or a generally effective merged configuration.

Every `dev repo list --json` row includes `notes.count`. When a latest note exists, the same object adds `notes.latest_id`, `notes.latest_preview`, and `notes.latest_updated`; these optional fields are omitted when the count is zero. `dev note list --json` and `dev note search --json` return arrays of complete note records, while `dev note show --json` returns one complete record.

## SSH host and fleet contracts

`dev ssh` keeps OpenSSH authoritative while owning only the exact root
`Include ~/.ssh/dev.d/*.conf`, canonical `~/.ssh/dev.d/<alias>.conf` files, and
explicit generated fleet registrations. `ssh init` is report-only unless
`--apply` is present. `ssh list` and completion are static and never run `ssh`,
`Match exec`, a resolver, an agent, or the network; `ssh show` deliberately uses
plain `ssh -G`, and `ssh probe` performs one fresh BatchMode login with connection
sharing disabled.

`ssh setup` handles new/managed/foreign aliases in one command. Connection flags
(`--hostname`, `--user`, `--port`, `--proxy-jump`, `--identity-file`,
`--identities-only`) apply only to new or managed aliases. `--config-only` stops
after local verification. Full setup requires an explicit `--key` or
`--generate-key`; noninteractive full setup also requires `--target-os`, and
noninteractive generation requires `--no-passphrase`. Route/platform controls
are `--hop-os`, `--install-on-working-jump`, and
`--windows-admin-authorized-keys`. `--dry-run` does no OpenSSH evaluation,
network access, generation, or writes. `--fleet` is an explicit final step after
a fresh ordinary alias login; `--fleet-name` never implies it.

Every SSH JSON form emits one object with `schema_version`, `kind`, and stable
status/action/error codes; operational failures still emit one safe document and
child diagnostics stay on stderr. Kinds are `ssh_init_plan|ssh_init_result`,
`ssh_list`, `ssh_show`, `ssh_setup_plan|ssh_setup_result`, `ssh_probe`, and
`ssh_remove_plan|ssh_remove_result`. `ssh list --format tsv` emits one definition
per row with six fields: alias, status, ownership, source, line, and
comma-separated fleet names. See [SSH host onboarding](../guides/ssh-hosts.md)
for schemas, partial/unknown outcomes, and the no-private-key/no-revocation
boundary.

Fleet's durable input is a merge, not one file: user-authored primary
`remotes.toml` first, then strict dev-owned sibling
`remotes.d/ssh-<alias>.toml` fragments in lexical order, then defaults.
`remote_os = "posix"|"windows"` selects the target launcher/path semantics and
participates in cache identity. `dev fleet config show` prints the effective
merge with secrets redacted and generated origins identified; `config edit` and
the TUI edit action continue to open only the primary file. Generated fragments
are owned by `dev ssh setup/remove --fleet` and cannot contain `machine_id`.

`dev fleet machine-id <host>` performs the content-free `_capability` exchange,
reports `unpinned`, `match`, or `mismatch`, and never writes the optional UUID
pin. `dev fleet files` remains report-only until explicit `--apply`; it uses the
separate `[local_files].include`/`--file` allowlist, requires a matching pin for
apply, and never infers `--replace` from `--yes`. Windows transport allowlists the
capability helper for identity diagnostics but blocks native file payload helpers
before content is sent.

## Repository bootstrap

Repository setup remains separate from acquisition. For acquisition,
`new`/`create` distinguishes a plain repository name from a clear clone
reference:

| Command | Behavior |
|---|---|
| `dev repo new` | interactive wizard; its first field accepts either a new name or a clone reference |
| `dev repo new NAME` / `dev repo create NAME` | create a new local repository; a clear Git URL, local Git path, or owner/name instead routes to clone and preserves history/remote |
| `dev repo clone [owner/name\|url]` | clone into the configured destination, then optionally apply a preset; setup defaults off |
| `dev repo setup [repo-or-path]` | repeat-safely merge native initializers and preset files into an existing clean checkout; defaults to the current repository and does not commit unless requested |

A clone-routed `new` retains the source `origin`; it therefore rejects
new-upstream creation flags. It also rejects `--template*`, whose explicit
meaning is “copy content into a fresh history.”

Across these commands, controls include `--preset`, `--path`, typed input values through
`--set`, item selection through `--enable`/`--disable`, `--check-in
<auto|commit|stage|none>`, `--dry-run`, `--yes`, `--json`, and `--handoff
<stay|cd|open|start>`. `repo new` additionally accepts `--template`,
`--template-ref`, and `--template-subdir`. JSON mode is non-interactive and
never changes directory or opens a runtime. Dry-run performs no target
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
  downloaded skill scripts are not executed for these built-ins. Matching
  skills with the same source and identical agent targets share one installer
  invocation, while their setup phases still run per skill. The history
  initializer creates `.pre-commit-config.yaml` and `.gitleaks.toml`, then
  ensures `.specstory/.gitignore` contains rules for SpecStory's `.project.json`
  and `statistics.json`, never the trackable `.specstory/history/` directory.
  Existing custom ignore content and its mode are preserved; only missing
  managed rules are appended.

After preset selection, the new-repository wizard asks “Customize preset and
template options?” with a default of no. The ordinary `agent-ready` flow uses
its reviewed template, file, input, and skill defaults without presenting each
question. Answer yes—or provide customization flags—to expand those controls.

Presets can add typed `string`, `bool`, and `choice` inputs, text templates,
hooks, and project skills. Hooks run in fixed `before_commit`, `after_commit`,
and `after_remote` phases. The safe hook form is an argv `command`; a shell
`run` is explicit, and only `interactive = true` loads an interactive shell.
Required failures stop later commit/remote steps; optional failures are
reported as warnings. Produced local files are retained for recovery. Native
initializers and preset files are repeat-safe; custom hooks and skill setup are
responsible for their own idempotency.

### Snapshot templates

`dev repo new NAME --template SOURCE` seeds a fresh repository from a content
snapshot. `SOURCE` may be a local directory/repository, Git URL, or owner/name.
For a Git source, `--template-ref` selects an arbitrary branch, tag, or commit;
`--template-subdir` selects a clean relative directory as the new repository
root. Without a ref, a local Git working tree includes existing tracked files
plus untracked files that are not ignored by Git; ignored build/cache content
is omitted. A non-Git directory snapshots its complete current tree.

The new repository does not inherit source history or remotes. Dev excludes
every source `.git` entry, rejects traversal, symlinks and special files,
preserves regular-file modes, and validates the complete snapshot before
creating the destination. Snapshot files take precedence when the selected
scaffold would write the same path; the scaffold still fills missing files and
runs the selected initializers. Source and destination traversal is performed
relative to held `os.Root` handles, and file bytes come from the already-open
validated source handle rather than a second mutable pathname lookup.

Confirmation and human dry-run output show a bounded preview of selected paths
and warn when a local source is a live working-tree/directory snapshot rather
than a commit. Credential-bearing URL userinfo is removed from summaries,
structured output, and clone/template errors.

Presets expose the same operation through scalar `template`, `template_ref`,
and `template_subdir` fields. They inherit normally and explicit CLI flags win,
which supports one starter catalog repository plus a child preset for each
subfolder.

### Check-in policy

The interactive wizard offers `commit`, `stage`, and `none`; scripts may also
pass `auto`. The behavior is:

| Value | Behavior |
|---|---|
| `commit` | `git add -A`, commit with `--message`/the preset message, then run `after_commit` setup |
| `stage` | run `before_commit` setup, then `git add -A`; leave the checkout staged and do not run `after_commit` |
| `none` | leave generated changes unstaged and uncommitted |
| `auto` | for `repo new`, use `initial_check_in` and compatible `initial_commit`; clone/setup otherwise perform no automatic check-in |

In `stage` mode, dev best-effort writes `LAZYGIT_PENDING_COMMIT` in the exact
worktree Git directory. [Lazygit v0.59.0 reads this
file](https://github.com/jesseduffield/lazygit/blob/v0.59.0/pkg/gui/controllers/helpers/working_tree_helper.go#L191-L216)
as the initial message for lowercase `c`; uppercase `C` and Git itself do not use this integration. An
existing different draft is preserved and reported rather than overwritten.
This is an implementation-detail adapter, not `commit.template`.

Staging is the durable outcome: if the optional lazygit draft cannot be
written, dev emits a warning and retains the staged index instead of rolling it
back.

Staged setup cannot create an upstream, and `handoff=start` requires committed
setup because a new worktree would omit the staged files. `stay`, `cd`, and
`open` remain available for review. `repo setup --commit` is retained as a
compatibility alias for `--check-in=commit`; `--message` applies to both commit
and stage. Structured results retain `committed` and add `staged`,
`staged_paths`, `commit_message`, `commit_draft_provider`, and a content-free
`template` summary where applicable.

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

Publishing from `repo setup` requires `--check-in=commit` (or the compatible
`--commit` flag), so the newly created upstream cannot omit the generated setup
changes. `--check-in=stage` is incompatible with any new upstream; for a new
repository, `none` may create an intentionally empty upstream only with
`--push=false`.

### Handoff

`stay` prints the result only. `cd` uses the trusted `shell-init` wrapper to
change the parent shell. `open` opens the configured Herdr/tmux/Zellij runtime
and falls back to `cd` when runtime is `none`. `start` continues into the
existing task wizard with the repository fixed, and is unavailable when setup
leaves uncommitted files that a new worktree would omit. Neither repository
bootstrap nor a default `dev start` launches a coding agent. Explicit
worktree-mode `dev start --run '<shell command>'` dispatches one command only
when a newly created first-class Herdr worktree returns its exact root pane;
`--focus` independently controls navigation afterward.

All shared TTY text fields in the repository, task-start, and finish wizards
use an inline editor: Left/Right, Home/End, Delete/Backspace, insertion at the
cursor, and Esc/Ctrl-C cancellation work as terminal actions rather than being
inserted as raw escape bytes. Buffered and piped non-TTY input retains its
line-oriented behavior.

## `dev flow [repo]` preview

`dev flow [repo]` is an independent full-screen command for interactive TTYs;
it has no JSON or non-interactive contract. With no `repo`, a canonical or
linked checkout opens the repository identified by the same Git common
directory and focuses the exact current surface. Outside Git it opens an
asynchronous repository picker. An explicit `repo` overrides cwd.

Startup and `r` load only local topology and evidence. `R` offers Fetch refs,
Refresh PR/MR, or Both; every choice first creates an exact guarded plan and
then requires approval. A provider review observation retains only portable,
run-local existence, `open`/`draft`/`merged`/`closed` state, URL, provider, and
observation time; it does not represent CI checks or approvals. `runtime=none`
remains unobserved and does not expose expert overrides such as
`--assume-no-runtime`. See [Repository lifecycle flow](../guides/repository-flow.md)
for keys, row kinds, partial ledgers, and the raw-tool escape boundary.

## `dev done` finish flags

`dev done` for branch/worktree tasks integrates through exactly one of `--ff`
(rebase onto the base, then fast-forward it), `--pr` (push and open a pull/merge
request), or `--merged` (verify external integration against `--base-ref`).
Omitting an integration choice opens the interactive finish wizard on a TTY —
see [Change-stream workflow](../guides/change-stream-workflow.md) for the
prompts.

A dirty checkout is handled by `--dirty <auto|fail|commit|discard>` (default `auto`):

| Value | Behavior |
|---|---|
| `auto` | interactive: prompts to commit or discard; non-interactive: fails, same as `fail` |
| `fail` | refuses to finish with a dirty checkout |
| `commit` | commits everything with `--message`/`-m` (prompted interactively if omitted) |
| `discard` | resets tracked changes and removes untracked files; destructive, requires `--yes` outside a TTY |

`--yes`/`-y` confirms the selected finish plan; it is mandatory for a
non-interactive `--dirty discard` and otherwise skips the interactive
confirmation step. `--push` pushes the branch or base selected by the
integration mode. Successful local or externally verified integration records
DONE/MERGED while always retaining the runtime, worktree, and branch for a
separate `dev retire`; `--keep-worktree` remains only as a no-op compatibility
warning, while `--delete-branch` fails with guidance to use
`dev retire --delete-branch`. `--merged` can use
`--confirm-squash <merge-commit>` as explicit operator attestation for a squash
result; provider status never implies that attestation.

## Configuration

```bash
dev config init
dev config show
dev config path
dev config scaffolds init
dev config scaffolds show
dev config scaffolds path
dev config scaffolds edit

dev fleet config init      # user-authored primary remotes.toml
dev fleet config show      # effective primary + generated remotes.d merge
dev fleet config edit      # primary only
dev ssh init               # report dedicated OpenSSH Include plan
dev ssh init --apply       # install it after confirmation
```

`config init` detects local roots and writes explicit defaults. A missing config file is allowed; the built-in defaults keep core Git behavior usable, but generated config is recommended because it makes machine policy reviewable.

Key sections:

| Section | Controls |
|---|---|
| `[paths]` | scan roots, project/tries/worktree roots, worktree template, state path |
| `[runtime]` | `auto`, Herdr, tmux, Zellij, or none plus metadata settings |
| `[worktree]` | local ignored-file provisioning, linked dirs, setup commands, strategies, timeout |
| `[local_files]` | host ceilings for portable files; project overlays provide the separate off-machine candidate allowlist |
| `[forge]` / `[[forge.azure_devops]]` | complete remote inventory cache TTL and opt-in Azure organization/project targets |
| `[picker]` | optional external selector argv; empty selects the built-in picker |
| `[bootstrap]` | recursion, symlink handling, index/layout policy |
| `[tui]` / `[[tui.tools]]` | columns, sorting, and external-tool bindings |
| `[stats]` | sampler and optional WakaTime import |
| `[update]` | `check` (default `true`) — allow the once-a-day "newer release available" hint and its background cache refresh; `DEV_NO_UPDATE_CHECK` overrides it |

Interactive repository selection uses one direct argv vector, never shell source:

```toml
[picker]
command = ["fzf", "--height=60%", "--layout=reverse", "--border", "--prompt", "{prompt}> "]
```

The executable must implement a line-selector contract: candidates arrive on
stdin and the selected original line is returned on stdout. The default is
`fzf`; compatible selectors can replace the entire array. A missing executable
falls back to dev's Bubble Tea picker, and `command = []` forces that fallback.
This is global host policy and cannot be overridden by a repository. Non-TTY
callers retain deterministic line prompts.

`DEV_TUI_TRACE=/absolute/new-file.json` enables a one-run TUI startup/readiness
trace. The target must be absolute and must not exist; dev never overwrites it.
The private bounded document is written after TUI teardown and contains relative
categorical timings plus aggregate row counts rather than names, paths, or raw
payloads. It is not configuration,
cache, durable stats, stdout, or network telemetry. For TUI invocations, the
optional update-cache network refresh is also deferred until the initial view
has returned.

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
initial_check_in = "stage"
template = "acme/starter-catalog"
template_ref = "v2"
template_subdir = "services/go"

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
`enabled = false`. A `[[presets.*.files]]` rendering source must remain in the
`templates/` tree next to its config source; that file-level mechanism is
separate from the repository snapshot `template` scalar above. Destinations
must remain inside the repository, and a skill setup script must remain inside
the installed skill directory. `initial_check_in` accepts `commit`, `stage`, or
`none`; the legacy `initial_commit` boolean remains readable, but a preset must
not set both.

### Safe project overlays

A repository may commit these fixed files:

- `.dev-cli/config.toml`: allowlisted worktree provisioning, separately
  proposed portable local-file patterns, and repository setup wizard defaults.
- `.dev-cli/scaffolds.toml`: project presets, templates, hooks, and skill
  setup using the same versioned schema.

```toml
# .dev-cli/config.toml
version = 1

[worktree]
include = [".env.example"]
strategy = "reinstall"

# Proposed candidates only; export still requires explicit fleet files --to.
[local_files]
include = [".env", ".mcp/**"]

[repo.setup]
preset = "team"
handoff = "cd"
check_in = "stage"
```

Effective precedence, lowest to highest, is built-ins, global config/scaffolds,
legacy `.dev.toml`, the target repository's `.dev-cli/*`, then explicit CLI or
wizard choices. `.dev.toml` remains readable for compatibility; new project
configuration should use `.dev-cli/config.toml`. Global `default_preset` and the
project `[repo.setup]` preset, handoff, and check-in fields seed interactive
wizard choices; they do not change scripted defaults, which are controlled by
the corresponding flags. The legacy `commit` boolean remains readable, but may
not be combined with `check_in` in the same layer.

`[local_files].include` never inherits `[worktree].include`: provisioning a local checkout is not authorization to export a secret. Project overlays may propose only the portable include list; host-owned count/size/path ceilings come from global config and cannot be raised by a repository. The command remains report-only until an explicit `--apply`, target pin, and confirmation.

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
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/config/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/config/config.go)
- [`internal/scaffold/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/scaffold/types.go)
- [`internal/projectconfig/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/projectconfig/types.go)
- [`internal/skill/dev-cli/references/commands.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/commands.md)
- [`internal/cli/color.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/color.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/sshhost`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/sshhost)
- [`internal/fleet/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/config.go)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/cli/fleet_files.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet_files.go)
- [`internal/localfiles`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/localfiles)
- [`internal/machineid`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/machineid)
