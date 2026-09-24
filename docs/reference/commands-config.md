---
description: Find the dev-cli command groups, generated exact flags, configuration layers, and stable automation surfaces.
authority: project
status: generated-plus-authored
verified_on: 2026-09-24
---

# Commands and configuration

`dev git submodule add [source] [path]` and `dev repo add-as-submodule` share
`--parent`, `--checkout=pinned|default-branch`, pinned-only `--ref`,
`--submodules=recursive|none`, `--dry-run`, `--yes` and `--json`. Addition stages
only its metadata/gitlink and emits partial-result phases on failure; see
[Submodule workspaces](../guides/submodule-workspaces.md).

Submodule configuration is shared by clone and worktree acquisition: `[submodules] init = "recursive"` (or `"none"`) and `develop = ["path"]`, with project overrides in `.dev-cli/config.toml`. CLI uses `--submodules`, repeatable `start --submodule` / `--submodule-base`, and explicit cleanup `--recursive`. See [Submodule workspaces](../guides/submodule-workspaces.md).

Use the authored map for intent and the embedded generated reference for exact flags. The generated block comes from the binary's Cobra command tree and is checked by `dev agent skill sync --check`.

## Command map

The root lists 17 primary entrypoints. Existing top-level commands remain
permanent shortcuts with compatible arguments, output and completion. Explore
`dev help --tree`, use `--depth 0` for every level, add a command path to focus,
and add `--aliases` to reveal shortcuts. See the [v0.3 migration guide](cli-v0.3.md).

| Goal | Commands |
|---|---|
| tracked work | `work list/start/park/resume/done/adopt/retire/sweep` |
| repositories/remotes | `repo list/context/new/clone/setup/open/browse/sync/remote/mark`, `repo bootstrap`, `repo note …`, `repo flow [repo]` |
| experiments | `tries try/open/list/graduate/demote`, `tries deprecate/reactivate/archive/restore/delete` |
| Git and checkout support | `git uncommit/recommit/pull-rebase/amend-all/setup`, `git ignore`, `git worktree …`, `git submodule …`, `git hygiene …` |
| coding-agent support | `agent skill …`, `agent mcp …`, `agent instructions …`, `agent prompt …`, `agent artifact …`, `agent artifact prepare` |
| activity | `activity journal`, `activity stats …` |
| dev installation | `self config …`, `self cache …`, `self doctor`, `self version`, `self upgrade`, `self completion`, `self shell-init`, `self feedback …` |
| independent entrypoints | `snippet`, `ssh`, `fleet`, `dotfile`, `pr`, `summary`, `triage`, `status`, `tui`, `help` |


Run `dev <command> --help` for the installed binary; this site describes the repository version identified in its freshness metadata.

## Read help on demand (v0.2.24)

`dev --help` lists commands. Use the relevant leaf command's `--help` for syntax
and flags, and `dev help <topic>` for workflow explanations. Dashboard `?` or the
clickable **Help** footer opens contextual **Keys**, **Guide** and **Manual**;
Manual uses the same embedded topics and workflow TL;DR as CLI help.
See [contextual Help](../guides/tui-repos-bootstrap.md#contextual-help-v0224).

The bundled skill has a small entry covering core purpose, essential boundaries
and conditional links to advanced references. `dev --skill`, `dev agent skill print`
and the `SKILL.md` written by `dev agent skill install` remain byte-identical. Installing
the bundle keeps the detailed references available without requiring an agent to
preload them. Load only the matching reference for advanced coordination,
retirement, provisioning, SSH or transfer work.

`dev --skill` when true, `dev agent skill print` and `dev help` print embedded content
without loading application config, checking releases or deleting stale Windows
upgrade binaries. Invalid arguments, flags and color values still fail normally;
`--skill=false` retains ordinary startup. No CLI flags or JSON fields are changed.

## Graduate interface (v0.3.1)

`dev tries graduate [try]` keeps `--name` and `--category`; it adds `--yes`/`-y`,
`--remote-url`, `--forge auto|github|gitlab|none`, `--namespace` and
`--visibility private|public|internal`. Existing `--remote`, `--private` and
`--push` stay supported. TTY runs use a reviewed wizard unless `--yes` or
`--dry-run` selects the direct path; non-TTY runs are direct. See
[Try graduation](../guides/try-graduation.md) for remote-preservation rules,
mode-specific defaults and partial failures. Try JSON adds the optional
`experiment.graduated_name`; existing schema-1 fields are unchanged.

## High-value structured interfaces

```bash
dev work list --json
dev repo list --json
dev repo context [repo] --json
dev repo remote --json
dev fleet machine-id <host> --json
dev fleet files [repo-or-path] --to <host> --json
dev repo new NAME --json
dev repo clone <ref> --json
dev repo setup [repo-or-path] --preset PRESET --json
dev repo note list [repo] --json
dev repo note search <query> --json
dev repo note show <note-id> --json
dev ssh init --json
dev ssh list --json
dev ssh list --format tsv
dev ssh show <alias> --json
dev ssh setup <alias> --dry-run --json
dev ssh probe <alias> --json
dev ssh remove <alias> --dry-run --json
dev agent skill list --all --json
dev agent mcp list --all --json
dev pr list --json
dev agent prompt list --json
dev agent prompt render <pr-triage|session-close|workspace-closeout>
dev work sweep --ephemeral-worktrees --json
dev git hygiene report --json
dev repo bootstrap --json
```

Prefer JSON or the agent-ready Markdown context over parsing human tables. Tables are optimized for terminals and may change columns/width without changing the structured contract. `repo context --json` is an additive schema-v1 report: unavailable facts remain null/error entries with explicit provenance instead of becoming zero values. `fleet files --json` is content-free and never includes file hashes or bodies.

`dev agent skill list --json` keeps its existing array and keys while adding repository, checkout, installation, presence/integrity, registry, and lock metadata. `dev agent mcp list --json` begins with a `servers`/`diagnostics`/`coverage` envelope; exact Claude local rows add `local_project_path`. Every server field is already sanitized. Declaration state may include Claude's documented project approvals, but must not be interpreted as health or a generally effective merged configuration.

Every `dev repo list --json` row includes `notes.count`. When a latest note exists, the same object adds `notes.latest_id`, `notes.latest_preview`, and `notes.latest_updated`; these optional fields are omitted when the count is zero. `dev repo note list --json` and `dev repo note search --json` return arrays of complete note records, while `dev repo note show --json` returns one complete record.

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
after local verification. Public-key bootstrap uses `--key` or `--generate-key`;
an interactive run without either opens a key picker (local keys, generate, or a
path). Noninteractive key bootstrap requires one of them and `--target-os`, and
noninteractive software-key generation requires `--no-passphrase`. Route/platform controls
are `--hop-os`, `--install-on-working-jump`, and
`--windows-admin-authorized-keys`. `--dry-run` does no OpenSSH evaluation,
network access, generation, or writes. `--fleet` is an explicit final step after
a fresh ordinary alias login; `--fleet-name` never implies it. Explicit agent-key
selection can still query the selected local SSH agent during dry-run.

Local `--generate-key` accepts `--key-type ed25519|ed25519-sk|ecdsa-sk` (default
`ed25519`). FIDO types add `--sk-provider internal|<absolute library>`,
`--sk-resident`, `--sk-verify-required` and `--sk-application ssh:...`; these are
not existing-key/agent/auth options. SK generation requires native interactive
approval and reports unknown tool/device capability rather than promising readiness.
Known incompatible toolchains block; reviewed custom toolchains can attempt the
operation without a passive hardware probe. Managed aliases use v2
`SecurityKeyProvider`. New hardware generation during fleet-source profile imports
and automatic `apple-secure-enclave` creation are unavailable. Local files and
hardware created/unknown effects are reported separately; no automatic enrollment
retry or credential deletion. See [hardware keys](../guides/ssh-hosts.md#fido-security-keys-yubikey-and-compatible-authenticators).

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

### Pull-request schema and effective scope

`dev pr list --json` emits an object with `schema_version: 1`, `generated_at`,
`scope`, `state`, `roles`, optional normalized `repositories`, provider readiness,
`warnings`, and `pull_requests`. Partial role/provider failures appear in
`warnings` even when useful rows are still returned. `scope` is the surface that actually ran: requesting
merged/closed/all from account or all narrows collection to local, and JSON says
`"local"`. `--repo` filters account rows as well as local query targets.
Personal local inventory may make one paginated query per repository for each
requested role (author and reviewer by default). Every row carries its provider
`host`; a local enterprise remote whose host differs from `GH_HOST`/`GITLAB_HOST`
is reported rather than queried against the wrong endpoint. Full GitHub rows also carry
optional `head_repo` and `is_cross_repository`; local evidence joins against the
source repository, and a deleted/unknown source never falls back to an unrelated
target-repository branch with the same name.

A request's optional `local` object distinguishes task intent from live checkout
health. It carries `expected_branch`, observed `live_branch`, and independent
`branch_checked_out`, `checkout_exists`, `worktree_registered`, and
`status_available` booleans, plus optional `status_error`. Optional `git` appears
only when the expected branch was actually proven checked out and status was
available; it contains `dirty`, `ahead`, `behind`, and optional `upstream`.
Missing/cold/unregistered status therefore cannot look clean through zero values.
Schema 1 is add-only.

### Ephemeral-worktree report and apply contract

`dev work sweep --ephemeral-worktrees --json` emits one object with
`schema_version: 1`, generation time, canonical repository/common-dir identity,
provider inactivity threshold, explicit-base/branch-deletion request, sorted
capabilities/diagnostics/candidates, and summary counts. Candidates contain only
validated provider/run/agent IDs, normalized states/times, worktree path/branch/
registry HEAD, live Git facts and counts, task/artifact/caller/runtime facts,
stable checks/classification, a separate branch-deletion audit, planned actions,
and one stable fingerprint. It never includes metadata filenames or prompt,
script, log, result-body, or transcript content. Empty collections are arrays and
schema 1 is add-only.

A candidate also has the `provider-git-identity` check. It is eligible only when
the source recorded branch, HEAD, common-dir, and an opaque non-replayable
registration generation that live collection independently matches. Claude Code
2.1.259 records no such identity, so current Claude Workflow candidates expose
that check as `unknown` and stay report-only. Path/name/GitDir reuse is not
accepted as identity.

The JSON form is report-only; `--json --apply` is rejected. Human apply requires
an interactive terminal and one confirmation per eligible candidate. It rejects
`--yes`, `--close-unknown`, `--assume-no-runtime`, and apply with `--no-runtime`.
`--stale-days` is provider inactivity (default 14, minimum 1), not commit age.
`--ephemeral-worktrees` and `--merged-worktrees` are mutually exclusive.

Branches are retained by default, including branches with commits unique from a
base. `--delete-branches` requires `--apply` plus an explicit `--base`; the
candidate's branch action remains unsafe unless both tips resolve unchanged, the
branch is contained, and it has zero unique commits. Apply recollects all proof
under a common-dir lock and accepts only the same fingerprint. Results are
versioned internally as removed, partial, skipped-changed, or failed; branch
failure after worktree removal is partial and retains the branch.

### Prompt recipes and structured behavior

`dev agent prompt list --json` returns sorted recipe metadata (`name`, `summary`,
`scope`, optional `target_usage`, and `context_version`). The three recipes are
`pr-triage`, `session-close`, and `workspace-closeout`.

`dev agent prompt render <recipe>` prints a Markdown prompt whose JSON envelope has
`schema_version: 1`, recipe/context versions, generation time, host, scope,
optional target, capabilities, warnings, and recipe context. Collection is
read-only and missing evidence stays explicit.

`dev agent prompt agents [--json]` is the sorted, redacted profile inventory. Human
output has `PROFILE DEFAULT RUN OPEN DESCRIPTION`; direct launchers expose only
the executable basename, shell launchers say `shell`, and unavailable modes say
`—`. Every JSON object has `name`, `description`, `default`, and nested
`run`/`open` objects containing `configured`, `kind` (`command|shell|none`), and
`executable`. Neither form emits argv, shell source, executable directories,
environment, prompt text, or config path.

| Command | Process contract |
|---|---|
| `dev agent prompt run <recipe> [--agent NAME] [--dry-run]` | Resolve the global profile and its run launcher before collection; then run one batch process with no user stdin, stdin/file/argv prompt transport, and a 10-minute default timeout. |
| `dev agent prompt open <recipe> [--agent NAME] [--dry-run]` | Resolve the global profile/open launcher and check the non-dry TTY before collection; then run one foreground process with file/argv prompt transport and no default timeout. |

`--agent` wins, otherwise the unique configured default wins, otherwise the sole
agent wins. Multiple agents with no default fail as ambiguous. Selection is not
mode-local: a selected profile missing the requested mode fails without using a
different profile. Diagnostics list sorted mode-capable profiles and point to
`dev agent prompt agents`. Dynamic completion loads parsed `--config`, filters names by
run/open capability, sanitizes descriptions through the shared completion
format, and degrades invalid config to no candidates plus no file completion.
Dry-run shows the resolved mode, cwd, transport, timeout, safe command markers,
and complete prompt, and starts nothing. Real launches in a checkout remain
subject to the writer-occupancy guard after collection supplies the cwd. `dev`
never parses a reply, starts an agent loop, changes permissions, or treats advice
as lifecycle authorization. `open` never creates, focuses, or reuses a
Herdr/tmux/Zellij surface; inside Herdr it remains in the current pane. See
[Prompt handoffs](../guides/prompt-handoffs.md).

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

Across these commands, controls include `--preset`, repeatable `--component`, `--path`, typed input values through
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
- `agent-ready`: extends `minimal` with common ignores, a short deferred
  `AGENTS.md`, and `.claude/settings.json` pointing to local plans. Agents maintain
  project-specific guidance after the user defines the project. No `.gitkeep` or
  empty plans/SpecStory directories are created; writers create their own folders.
  Matching orphan imports create the plans directory only when copying a file. The common ignore block excludes only
  `.specstory/statistics.json`; histories, project identity, and config remain
  visible to Git. Unmodified legacy `agent-history-hygiene` and
  `project-knowledge-harness` suggestions are hidden unless explicitly selected
  or customized. Existing `--enable` IDs and inherited presets remain valid.
  When selected, dev installs them and
  runs reviewed built-in initializers for their project surfaces before the
  initial commit; downloaded skill scripts are not executed for these built-ins.
  Matching skills with the same source and identical agent targets share one
  installer invocation, while their setup phases still run per skill. The
  history initializer creates `.pre-commit-config.yaml` and `.gitleaks.toml`,
  then additionally ensures `.specstory/.gitignore` contains rules for
  SpecStory's `.project.json` and `statistics.json`, never the trackable
  `.specstory/history/` directory. Existing custom ignore content and its mode
  are preserved; only missing managed rules are appended.

After preset selection, the wizard offers language/tool components and their
optional skills, then asks “Customize preset and template options?” with a
default of no. Only selected skills need agent targets; the legacy deployment
input is prompted only when its knowledge skill is selected.

Built-in components are `python`, `go`, `node`, `rust`, `java`, and `ruby`.
Each adds its language gitignore template. Python also offers the optional
`python-project-best-practice` skill from the configured upstream catalog,
without running its project initializer. Installation reuses the trusted direct
`skills` provider and native `skills-lock.json`; no custom skill copier or npx
fallback is introduced.

`--component python --component node` replaces a preset's saved component list;
`--component none` clears it. Components combine ignore templates with stable
case-insensitive deduplication. `--gitignore` remains the final override.
Component skills need no duplicate catalog entry; identical IDs deduplicate,
and conflicting definitions or duplicate native skill names are rejected before
scaffold mutation: different agent targets can share the same skill storage. Components cannot declare setup scripts;
use an explicit preset for executable setup. File `default = false` keeps a
file available for `--enable` while omitting it by default; `enabled = false`
removes it. `--enable claude-plans-directory` explicitly restores `.gitkeep`.

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
bootstrap nor a default `dev work start` launches a coding agent. Explicit
worktree-mode `dev work start --run '<shell command>'` dispatches one command only
when a newly created first-class Herdr worktree returns its exact root pane;
`--focus` independently controls navigation afterward.

All shared TTY text fields in the repository, task-start, and finish wizards
use an inline editor: Left/Right, Home/End, Delete/Backspace, insertion at the
cursor, and Esc/Ctrl-C cancellation work as terminal actions rather than being
inserted as raw escape bytes. Buffered and piped non-TTY input retains its
line-oriented behavior.

## `dev repo flow [repo]` preview

`dev repo flow [repo]` is an independent full-screen command for interactive TTYs;
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

## `dev work done` finish flags

`dev work done` for branch/worktree tasks integrates through exactly one of `--ff`
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
DONE/MERGED while retaining the worktree and branch for a
separate `dev work retire`; `--keep-worktree` remains only as a no-op compatibility
warning, while `--delete-branch` fails with guidance to use
`dev work retire --delete-branch`. `--merged` can use
`--confirm-squash <merge-commit>` as explicit operator attestation for a squash
result; provider status never implies that attestation.

For a managed worktree, bare interactive `dev work done` adds a post-MERGED cleanup
choice: keep, retire while keeping the branch, or retire and delete the
contained branch. It previews covering runtime panes and agent states first.
Caller-owned Herdr workspaces are handed to a fresh external coordinator;
explicit modes and non-interactive invocations continue to stop at DONE.
Parent agents remain open and block FF with recheck/PR/cancel. Only exact
idle/done task-worktree panes may close under final Apply approval. General
foreground programs require separate FF file-change consent; both interactive
cleanup entrances require `CLOSE <workspace-id>` for known program termination.
Background jobs are not inspected. A dirty canonical target still offers PR,
stash+restore, typed `DROP`, or cancel. See [retirement scope](../guides/agent-safe-retirement.md#task-worktree-scope).

## Retirement containment flags

`dev work retire [task-or-worktree] --base <ref>` overrides the containment target:
local branch, then remote-tracking ref (such as `origin/main`), then commit.
Apply re-resolves the same input and rejects a changed kind/ref/OID. Without an
override, a task's recorded fork-point commit is retained; if it cannot prove
integration, retirement stays blocked and requests `--base <branch>`.
`dev work sweep --base` forwards the override to DONE tasks, and `--merged-worktrees`
passes its verified base to managed tasks too. Unrelated sibling removals no
longer invalidate the remainder of an approved sweep batch.

`done --merged --base-ref X` carries X into cleanup hints, the wizard and external
coordinator handoff (`dev work retire --base X <task>`). Optional `--delete-branch`
(`sweep --delete-branches`) still uses `git branch -d`: Git checks upstream/HEAD,
not `--base`, so a non-HEAD base may produce partial completion after removal.

## Configuration

```bash
dev self config init
dev self config show
dev self config path
dev self config scaffolds init
dev self config scaffolds show
dev self config scaffolds path
dev self config scaffolds edit

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
| `[[agent]]`, `[agent.run]`, `[agent.open]` | host-only names, optional descriptions, global default selection, and independent batch/foreground prompt launchers; no built-in entries |

Interactive repository selection uses one direct argv vector, never shell source:

```toml
[picker]
command = ["fzf", "--height=60%", "--layout=reverse", "--border", "--prompt", "{prompt}> "]
```

The executable must implement a line-selector contract: candidates arrive on
stdin and the selected original line is returned on stdout. The default is
`fzf`; compatible selectors can replace the entire array. A missing executable
falls back to dev's Bubble Tea picker, and `command = []` forces that fallback.
Multi-selection always uses the built-in picker, regardless of the external command.
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

### Agent prompt launchers

There are no built-in agent entries. Launchers are host-only policy in the user
config; the `agent` section is denied in repository `.dev-cli/config.toml`.

```toml
[[agent]]
name = "my-agent"
description = "Local review and implementation agent"
default = true

[agent.run]
command = ["my-agent", "--batch"]
input = "stdin"
timeout = "10m"

[agent.open]
command = ["my-agent", "{{prompt_file}}"]
input = "file"
```

`[[agent]]` has required `name`, optional `description` and `default`, and nested
`run`/`open` launchers. Names cannot have surrounding whitespace, compare
case-insensitively, and must be unique; at most one entry may be default. An
entry must configure at least one mode. Selection remains explicit/default/sole
across all profiles, then the requested mode is required on that exact profile;
the other mode remains unavailable rather than inheriting or falling back.

Each launcher sets exactly one of `command` (direct argv) or `shell` (static
shell text), a required `input`, and optional `load_shell_rc` for `shell` only.
Only `run` accepts an optional non-negative `timeout`; `open` rejects one. `load_shell_rc = true` uses `$SHELL -lic`;
otherwise shell launch uses `$SHELL -c`. Shell text cannot contain prompt
placeholders, so prompt data is never interpolated into a command string.

Transport constraints are exact:

- `stdin`: `run` only; no placeholder; the finite prompt replaces child stdin.
- `file`: a `command` needs exactly one whole `{{prompt_file}}` element; a
  `shell` must reference `$DEV_PROMPT_FILE`/`${DEV_PROMPT_FILE}`. The temporary
  directory/file modes are 0700/0600 and cleanup follows process exit.
- `argv`: `command` only, with exactly one whole `{{prompt}}` element; maximum
  rendered prompt size is 100 KiB.

Embedded placeholder forms are rejected. Omitted/zero timeout resolves to 10
minutes for `run`; `open` has no deadline and rejects a timeout. See
[Prompt handoffs](../guides/prompt-handoffs.md) for recipe and runtime safety.

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
components = ["go", "editor"]
handoff = "cd"
initial_check_in = "stage"
template = "acme/starter-catalog"
template_ref = "v2"
template_subdir = "services/go"

[[presets.team.inputs]]
id = "service"
type = "string"
default = "api"

[[presets.team.files]]
id = "service-readme"
source = "service/README.md" # templates/service/README.md beside this file
destination = "docs/service.md"

[[presets.team.hooks]]
id = "verify"
phase = "before_commit"
command = ["make", "test"]
required = true

[components.editor]
description = "Editor support"
gitignore = ["VisualStudioCode"]

[[components.editor.skills]]
id = "team-style"
source = "owner/team-skills"
name = "team-style"
default = false
```

Skill setup normally names a project-local script inside the installed skill.
Legacy explicitly selected setups can use `builtin = "agent-history-hygiene"` or
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

`dev activity journal` and `dev summary` emit Markdown, so their headings and fenced code blocks are styled the same way `dev help <topic>` styles a quick-reference page. In command help, only the names you can type — command names and flag specs — are colored; descriptions stay plain, and the column alignment cobra computes is unaffected because a terminal gives an escape sequence no width.

Control it with the global `--color <auto|always|never>` flag (default `auto`). `auto` disables color when output is not attached to a terminal, when `NO_COLOR` is set to any non-empty value, or when `TERM=dumb`. The setting reaches the interactive dashboard as well, so `dev --color never` renders it without color. `--json` output is never colored regardless of mode. There is no config-file field for color — `--color` and the environment are the only controls, so piping `dev` never requires `--color never` to stay clean.

## Shell integration

```bash
eval "$(dev self shell-init zsh)"
dev self shell-init fish | source
```

The trusted `shell-init` output defines a wrapper because a child process cannot change its parent's working directory. For navigation and post-MERGED retirement, that wrapper reads a NUL-terminated path plus a narrow fixed retire action from private child-only file descriptors and calls `builtin cd` before invoking the exact task ID. It does not evaluate ordinary `dev` output or arbitrary shell code.

## Complete generated command reference

The following content is included from `internal/skill/dev-cli/references/commands.md`, beginning after its generated-file preamble:

--8<-- "internal/skill/dev-cli/references/commands.md:7"

## Keeping it current

```bash
go run ./cmd/dev agent skill sync --check
```

If command help changes, regenerate through `dev agent skill sync`; do not hand-edit the generated block.

## Sources

- [`internal/cli/root.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/root.go)
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/config/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/config/config.go)
- [`internal/cli/prompt_command.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/prompt_command.go)
- [`internal/handoff`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/handoff)
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

## Agent artifact transfers

Use `dev agent skill transfer plan <name> --from-agent universal --to-agent claude-code --mode mirror`
for a reviewed per-skill link. `--mode copy` creates independent content and
`--mode move` publishes the destination before source cleanup. Select exact
checkouts with `--from-repo`/`--to-repo`; apply with `transfer apply --plan <id>`.
`transfer status`, `transfer undo <id>`, and `transfer refresh <id>` expose the
local ledger and create guarded follow-up plans. Private recovery content is
stored under the configured state directory and is omitted from JSON reports.

For upstream reuse across repositories, run `dev agent skill transfer prepare <name>`
with the source/destination selectors, then `transfer plan <name> --mode install
--prepared <id>`. Only prepare may fetch. The initial compatibility profile pins
`skills@1.5.23` and verifies native lock provenance and staged content before any
agent files are published. A version/schema/hash mismatch fails without silently
upgrading or falling back to copy.

MCP and instruction transfers use the same guarded family: `dev agent mcp transfer`
and `dev agent instructions transfer`. MCP offers explicit `check`; all three families
can `export` an optional recipe and plan one `recipe` entry. See the
[complete interoperability workflow](../guides/agent-interop.md) for native
scope mappings, stanza ownership, credential references and platform limits.
Skill JSON rows and MCP JSON envelopes may add `interop` and `interop_coverage`;
`receipts-only` never means the native client loaded or authenticated a tool.

## Dashboard lifecycle additions

New commands: `dev repo browse` / `dev repo browse [repo-or-path] --remote <name> --print`; `dev work sweep --task <id>`; `dev tries delete <ref>` (`rm` alias) with `--dry-run`, `--json`, `--yes`, `--permanent`, `--confirm-delete <id>` and `--assume-no-runtime`; `dev tries restore <ref> --from <restored-path>`. [Behavior and confirmation rules](../guides/dashboard-actions.md).

## Explicit SSH machine management

`dev ssh manage` compares SSH aliases, fleet profiles and optional Herdr 0.9.0 saved
machines. Existing names stay independent; registration, rename/remove and Herdr
enable/disable are selected actions with a preview. `dev ssh format`, `organize`
and `restore` provide optional guarded local edits and private recovery. Existing
`ssh list` JSON/TSV and fleet snapshot contracts remain compatible. See
[SSH host management](../guides/ssh-hosts.md#machine-management-and-configuration-organization).

## Local triage report and intent

`dev triage --json` includes schema version, generation time, source completeness, items and cached-remote evidence. Items retain ordinary repo/Try kind, branch/checkout identity, findings, action candidates, deferred intent and disposable-directory declarations. `--report`, `--kind`, repeated `--root`, `--all` and `--stale-days` control inspection. Intent and batch ledgers live under `<state_dir>/triage/`, separately from task TOML. See [local triage](../guides/local-triage.md).

## Dashboard navigation and organizer entry

Enter opens a repository/task row or starts FLEET host navigation. Space expands or collapses REPOS/FLEET trees; flat lists leave Space unused. REPOS/TRY `Ctrl+O` offers organization of the
current item, filtered results, or all local work; multi-selection belongs to the
independent triage screen. `1–7` selects TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS,
and MCP respectively. TASKS state filters live in its action menu (`a` still
shows done tasks). Click a data column for ascending → descending → default
ordering; FLEET HOST groups machines. Sorting is local to each view/session and
uses the current snapshot, with unknown values last. The footer keeps two lines
of primary actions and navigation plus a version/update row; tools, state filters and sorting are in
`Ctrl+O`, and `?` opens contextual Keys/Guide/Manual help. Existing custom tool bindings for `4–7`
need reassignment; `x`/Ctrl+A are no longer reserved dashboard selection keys.

Triage uses grouped repo/Try checkboxes, mouse selection, and Ctrl+A all/none
within the filtered scope. Results preserve failures when returning to the
dashboard. Git synchronization diagnostics retain a category, exit code, bounded
redacted output and a next step; old receipts cannot recover discarded reasons.
Retries require a new preview. Authentication, fetch and rebase are never
silently performed as error recovery. See [local triage](../guides/local-triage.md).


### Skills maintenance and local presentation caches

`dev agent skill manage [--repo <ref> | --all]` is an interactive wizard, without a
non-interactive apply shortcut. Existing
`skill update <skill> --project|--global --yes` remains compatible.
`skill list --json` adds optional RFC3339 `update_checked_at`.
`dev self cache clear repos` clears repository presentation snapshots;
`dev self cache clear skills` clears dated source comparisons; `all` includes both.
Neither removes `stats.db` or skills run receipts.
See [Skills management](../guides/skills-management.md).

## Dashboard discovery registration (v0.2.24)

The first REPOS visit selects the startup repository without reordering; clicking
the selected row opens its actions. An outside startup Git repo offers clickable
previews to append its main root to `paths.repo_paths` or its parent to
`paths.scan_roots`. The active file, including `--config`, remains the only config
source. Comments, existing order and inherited defaults are preserved. Prefer exact
entries for isolated repos; parent scans also cover sibling projects and require
explicit confirmation. Guarded writes support macOS/Linux with private recovery
under `$XDG_DATA_HOME/dev/config-recovery`; symlink configs, unsupported TOML layouts
and other platforms offer manual edits and the config editor. Failed scans remain
unknown. Successful saves refresh local inventory; reload failures are reported
separately. CLI flags and existing JSON contracts are unchanged.

## Dotfiles and dashboard fleet

`dev dotfile status --json` is a versioned passive configuration/source/revision report.
`dev fleet dotfile status --host NAME` accepts repeatable exact host selectors.
Local `--chezmoi-config PATH` is distinct from dev `--config` and is not sent to
remote hosts. Setup supports `--repo URL` or optional `--preset david`; outside
a terminal it reports unless `--yes`, with separate `--apply`. Diff/apply/update
are explicit native operations. See [Dotfiles](../guides/dotfiles.md).

```toml
[tui.fleet]
background_refresh = true
```

This default warms missing/expired snapshots once after five seconds or an
earlier FLEET visit. It never prompts for credentials. Set false for explicit
updates only. Host rows, cached search, and per-host actions remain available.

## FLEET Herdr catalog state

FLEET hides local by default; a or the menu reveals it collapsed at the end
for this session. Hidden local data is excluded from search and coverage. HERDR
reports a shared local catalog observation (not added/enabled/disabled/mixed or
unknown), separately from repository STATE and runtime LIVE. Per-profile
enable/disable/remove work inside or outside Herdr, retain remote sessions and
refresh only catalog metadata. Connection eligibility and catalog cleanup are
separate. --no-runtime skips Herdr; background_refresh controls only automatic
repository SSH reads. Existing fleet snapshot JSON is unchanged by this UI
metadata. See [host controls](../guides/remote-fleet.md#dashboard-host-tree).

## SSH diagnosis

`dev ssh diagnose <target> [--compare-qos] [--timeout 60s] [--json]` performs
explicit bounded network observations. See [SSH diagnosis](../guides/ssh-hosts.md#ssh-diagnosis)
for stage meanings, platform collectors and local-output privacy.

## Feedback reports and repair sources

`dev self feedback draft` saves a local report; `issue <id>` previews its public body,
`--search` queries related issues and `--publish --yes --revision <revision>`
expresses prior authorization for the exact content/target. `repair <id> --base
<ref>` saves a guarded plan; `--apply --plan <id> --yes` prepares its isolated
checkout/task. Optional `[feedback].source_repo` is an absolute or home-relative
local dev-cli path. See [feedback and repair](../guides/feedback.md).

## Discovery and machine registry interfaces

`ssh list --tailscale` explicitly queries optional Tailscale status; `--lan` reads
cached LAN results. The default alias JSON and six-column TSV are unchanged.
Combined JSON adds `machines`, `sources` and `observed_at`; combined TSV has row ID,
label, state and comma-separated aliases. `ssh discover --source tailscale|lan`
emits a `ssh_discovery` document. LAN accepts `--interface`, repeatable `--cidr`,
`--ports` (default 22) and `--refresh`; it is bounded and on-link only.

`ssh setup [alias]` has an interactive multi-select flow plus `--from tailscale:<peer>|lan:<ip:port>`, `--auth existing`, `--to fleet|herdr|both`,
`--machine`, `--herdr-label` and `--herdr-session`. Source-aware setup defaults to
local configuration/mapping; key installation stays explicit. Its preview/results
use `ssh_onboarding_plan`/`ssh_onboarding_result`. A source dry run may read local
Tailscale status, but writes no config, registry, key or cache and does not log in.

`ssh machine show [machine-id]` reads the durable registry. `adopt`, `link`,
`unlink` and `merge` accept `--machine`, `--source` (repeatable), `--label`,
`--into`, `--apply`, `--yes` and `--json` as applicable; unsupported combinations
are rejected. Actions preview by default. Snapshot/transaction documents contain
`schema_version`, machine revisions and scoped bindings; the registry UUID never
replaces remote fleet's `machine_id` pin. See [SSH onboarding](../guides/ssh-hosts.md#discovery-and-canonical-machines).

## SSH vault-key creation

`ssh key create` takes no positional arguments. Flags are `--provider
1password|bitwarden`, `--title` (default `SSH key`), `--account`, `--vault`,
`--desktop`, `--dry-run`, `--yes` and `--json`, plus Bitwarden's `--experimental`
and `--native-context`. An interactive run may review the current account; actual
RPC creation with `--yes` or outside a terminal requires an exact `--account` ID.
1Password requires an exact vault ID; Bitwarden normalizes the personal vault and
needs BOTH approvals. Native-context delegates profile/configuration authority,
not endpoint attestation; `--yes` cannot substitute for either approval.

`--desktop` requires `--provider bitwarden` and rejects account/vault/experimental/
native-context scopes. It always requires interactive GUI-completion confirmation
and explicit fingerprint selection, even with `--yes`. Actual desktop handoff
cannot use `--json`; intent-only `--dry-run --json` is allowed. The native GUI owns
the title and creation, so dev does not invent an item ID or creation receipt.

Vault-create dry-run validates static intent only, executing no provider/agent and
leaving account/vault/agent observations unknown. Actual creation obtains a fresh
service-bound plan. Native-context metadata contains the reviewed profile/tool,
not session values or hashes. Successful Bitwarden native-context creation keeps
`result.binding_status: unknown` and `result.endpoint_status: unverified`; only
`result.native_context_status` becomes `observed_consistent`. This expected binding
uncertainty is separate from `result.status: created`, not a reason to retry creation.
Public output preserves item IDs/fingerprints and created/unknown/context state,
not private material or full public-key lines. Created items are not rolled back
when agent visibility or later SSH setup fails. No auto-retry, import, native
configuration rewrite, or item deletion is implied. Desktop handoff uses a complete
same-socket public inventory and explicit newly-visible-key selection, not guessed
vault item IDs. JSON uses `schema_version: 1`, `kind: ssh_key_create`. Dry-run is
`status: intent_only` with `preview.intent_only: true`, not apply-ready. Pre-effect
statuses include `blocked`, `confirmation_required`, `canceled` and `not_started`;
RPC outcomes are `created` or `unknown`, and a created receipt may accompany a
nonzero partial/context error. Desktop states are `observed`, `no_new_visible_keys`
or `unavailable`. `result.agent_status` is `not_checked`, `not_selected`,
`unavailable`, `not_visible` or `offered`; offered means listing, not signing proof.
`result.receipt` carries public item/attempt metadata with no `public_line`.
A controller-local `attempt_id` distinguishes separately reviewed creation attempts,
including unknown outcomes without an item ID. It is not a vault item ID or auth
proof. Duplicate delivery of one attempt is reconciled without dropping distinct
unknown attempts; UI receipts survive transient dialogs and immutable SSH reviews.
Receipt `observation` is a controller-local ordering counter, not a provider revision
or timestamp: an older delivery cannot erase newer context uncertainty. Known created,
item-ID and fingerprint facts are retained; conflicting nonempty identities appear in
`conflicting_identities` instead of silently replacing one another. These reconciliation
fields and retained receipts never grant key-selection or apply authority.
There is no serialized-plan apply flag.
See [vault creation](../guides/ssh-hosts.md#create-a-vault-key-then-select-its-agent-identity).

## SSH key catalog and picker behavior

`ssh key list` accepts `--json`, `--no-agent`, `--alias <alias>`, repeatable
`--agent bitwarden|1password|secretive|<absolute socket>` and explicit
`--on fleet:HOST` (not combined with `--agent`). Candidates from a named agent carry
`agent: {provider, socket}`. `ssh setup --identity-agent <agent>` selects `--key` (a
SHA256 fingerprint or `.pub` path) from that agent and writes a v2 managed alias with
`IdentityAgent`; errors use `agent_provider_unavailable`, `agent_key_not_found` or,
when a foreign alias needs a native configuration change, `identity_agent_manual`.
`--identity-agent` cannot be combined with local key generation or configuration-only/
existing-authentication modes. `--no-agent` and `--agent` are mutually exclusive.
The default local catalog skips `ssh -G`; explicit alias mode evaluates configured identities
and agent settings. JSON uses `schema_version: 1`, `kind: ssh_key_list`, candidates,
completeness and diagnostics. Local listing never repairs permissions or logs in; `--on` explicitly contacts
the selected source.

Interactive setup selects an existing key from this catalog or a manual path.
Its narrowly scoped permission preflight is a separate confirmed repair before
later wizard steps; completed tightening remains after cancellation. Fleet/Herdr
registration uses two optional checkboxes: none, either or both map to the existing
registration behavior. Multi-selection uses built-in Bubble Tea; fzf remains the
configured default for single selection. See [SSH key selection](../guides/ssh-hosts.md#key-selection-and-optional-registration).

## SSH key permission reports and fixes

`ssh key doctor` accepts `--fix`, `--yes`, `--json` and repeatable `--key PATH`.
Without `--key`, its bounded metadata scan includes public companions and exact
standard private-only key names. Explicit paths restrict the scan/repair scope,
alongside canonical SSH setup paths. It never reads key contents or runs an agent,
OpenSSH, key generation or remote authentication.

The default is a read-only report; repairable findings exit successfully while
blocked/incomplete scans fail without writes. `--fix` previews exact changes and
confirms; noninteractive/JSON apply requires `--fix --yes`. One guarded permission
plan retains completed changes on partial failure and verifies state afterwards.
Setup continues to use only its baseline/selected-key scope. Specific key-list
safety diagnostics point here; malformed key data still needs manual correction.
JSON kinds are `ssh_key_doctor_plan`/`ssh_key_doctor_result`, with scope, completeness,
key paths, the permission plan and optional result/recheck. See
[SSH key doctor](../guides/ssh-hosts.md#ssh-key-doctor).


## Fleet SSH sources, connection and password contexts

`ssh discover --source fleet --host NAME` accepts repeated source names and
`--refresh`; it does not recursively traverse fleets. JSON kind is
`ssh_fleet_discovery`, with one status/optional inventory per source. `ssh list
--fleet` adds cached `fleet_ssh_sources` to its existing JSON document and performs
no remote refresh. Host/alias selectors use `fleet:HOST/ALIAS` with percent-encoded
segments, or a cached exact `fleet-ssh:` profile ID.
Unreadable cache entries preserve local aliases and valid source rows, with
`complete: false` and a `fleet_ssh_diagnostics` entry rather than an empty success.

`ssh setup LOCAL_ALIAS --from fleet:HOST/ALIAS` imports a reviewed local ProxyJump
route. Configuration-only remains the default; `--auth existing`, local key
selection, `--hop-key local-alias=key-path` and provider registration stay explicit.
A dry run requires cached source/route metadata and does not invoke OpenSSH.
`ssh connect ALIAS [--on fleet:HOST] [--key-id SHA256:...]` opens one interactive
session on the selected executing host. It accepts no remote command arguments.
`ssh key list --on fleet:HOST [--alias ALIAS] [--no-agent] [--json]` returns scoped
remote metadata (`ssh_remote_keys`); remote paths never become local identities.

`ssh key derive PRIVATE_KEY [--apply] [--yes] [--json]` previews or creates only a
missing `.pub` inside `~/.ssh`. JSON kinds are `ssh_key_derive_plan` and
`ssh_key_derive_result`; existing companions are retained, and apply is separate
from permission repair or key installation.

Setup/connect accept `--password-store system|bitwarden` (default system). The
post-authentication save choice defaults to No; Never persists only for that
credential context. `$XDG_CONFIG_HOME/dev/ssh-credentials.toml` stores ask/never
policy, provider references and pending/unknown metadata, not secret values.
Explicit fleet password sources retain priority; discovery does not use the
saved-reference resolver. Remote source-to-target credentials are outside the
controller save flow. See [SSH workflows](../guides/ssh-hosts.md#fleet-source-profiles-and-local-routes).

## Repository hygiene

`dev git hygiene` provides staged/worktree/history scopes, per-repo block/warn/off
policies, private local identity imports and reviewed text replacement/recovery on
macOS, Linux and Windows. CI uses public rules only. See the
[hygiene workflow](../guides/hygiene.md) for schema-v1 coverage and hook contracts.

`dev git hygiene report` reads this checkout's newest stored scan without scanning
again; `--scope staged|worktree|history` selects that scope's latest and
`--report ID` selects an exact report. Latest lookup is checkout-specific and
excludes snapshots. Stored reports are historical; `checkout_current` compares
roots, not source bytes. `--rescan` requests new observations (default worktree);
`--range`, repeatable `--file`, `--timeout` (default 20m) and `--audit` require it.
`--report` and `--rescan` cannot combine.

`--by severity,rule,file,category` selects groups (default `severity,rule,file`);
`--top N` defaults to 10, with `0` showing all. `--disposition block,warn,accepted`,
`--rule`, `--category secret,known,generic` and repeatable `--path <glob>` filter
before aggregation; invalid globs are rejected and reported filters are
policy-masked. `--findings` adds individual findings. Agents should prefer
`dev git hygiene report --json` (`hygiene_summary` schema 1) over parsing scan output;
see the [summary contract](compatibility.md#hygiene-summary-json-contract).

`--values` adds masked distinct values and implies `--rescan` unless `--report`
names a scan captured with values. Raw values and context stay only in a private
0600 `<report-id>.values.review.txt`, not stdout, JSON or scan records.
`dev git hygiene review-path <plan-or-report-id>` prints a plan proposal or captured
values review location without its contents; never paste that file into chat,
Git or CI. Additive `file_id` keeps exact files distinct; legacy masked-path
counts are marked `file_counts_complete: false`. `values_status` reports
complete/truncated/failed capture. Count/byte limits omit oversized values with
`values_truncated`; a failed sidecar saves a partial report with
`values_capture_failed` and no review-path hint. Stored summaries exit 0; an
incomplete rescan prints the summary before exiting non-zero.

`hygiene repair-encoding --file PATH [--invalid replace|remove]` previews
working-file UTF-8 repair; `--apply --plan ID --yes` applies the reviewed plan,
with `--writer-stopped` required for artifacts. Index bytes stay unchanged.
Schema 1 retains `unsupported_text_encoding` and adds optional `encoding`
diagnostics and historical `commit` to coverage gaps; repair plan files also
carry encoding diagnostics. Repair does not claim a successful secret scan.

`hygiene manage [repo...] [--all]` opens batch repository setup; `--json` is
preview-only. `setup --migrate-hooks` previews narrow known-hook/rule migration.
`rules import --from machines` reads private candidates from existing local
Tailscale/LAN/Fleet caches. `skill manage` also removes selected owned skills with
explicit agent scopes; existing bundled `skill uninstall` keeps its meaning.

## Agent history policy

### Native closeout additions (v0.2.40)

`prepare` defaults to `--closeout product-first`: committed products and an empty
index. Optional `--specstory-path PATH` persists an exact provider/UUID export
within its capture scope. Source-commit and archive finalizers recheck selection,
live writer guards and native intent revisions; `--revision HASH` binds an exact
reviewed record.

`prepare --closeout co-commit --session claude:UUID --specstory-path PATH
--message-file PATH` instead requires one `--plan PATH` or explicit `--no-plan`
and a reviewed feature-only index. Optional `--closeout-helper` selects the
installed canonical v2 scripts directory; `--allow-large` acknowledges a new
untracked transcript over 2 MiB. The authentic wrapper must run without
`--allow-commit`. Queueing never launches/closes the agent: retain the real ACK,
report finalization queued and exit without further repository work. `--run-id`
only repairs the binding of an already-queued exact run; no new queue/ACK.

External `artifact finalize --intent ID --allow-commit [--revision HASH]` guards
canonical prepare-only and one commit-capable call; unknown outcomes reconcile
only. `--preview-review --json` is read-only and cannot combine with
`--allow-commit`, `--review-file` or `--rotation-confirmed`. Applying exact private
review requires an absolute `--review-file`; `--rotation-confirmed` means actual
credential rotation, not fixture review. See [AI artifacts](../guides/ai-artifacts.md)
for review rules, helper installation boundaries and platform limits.

`prepare --json` and `artifact finalize --json` use schema-1 kinds
`artifact_preparation` / `artifact_finalization` for product-first. Co-commit uses
schema 1, kind `co_commit_handoff`, retaining partial status, request ID, native
revision/intent, helper observation and, on queue success, canonical v2 `queue_ack`.
Human queue output retains that ACK as one compact JSON line for the recorder.
`artifact list --json` uses schema 1, kind `artifact_handoffs`, with `handoffs`
rows whose persisted Intent fields remain separate from `co_commit_observation`
or `observation_error`. Listing/status/readiness never mutate or reconcile.

### Retention configuration

`dev agent artifact setup` and `dev repo setup --artifacts` share reviewed plans.
`.dev-cli/artifacts.toml` records project ID, track/archive/unmanaged mode,
specstory/files source, capture location policy, literal paths and export rules.
Host-local archive/protection bindings and signed receipts stay under
`paths.state_dir/agent-history/`. They are durable private state, not cache.

Capture/provider identity, source retention, off/check/redact copy protection
and distribution exclusions are independent. See [AI artifacts](../guides/ai-artifacts.md)
for supported limits, exact preview/apply commands and historical migration.

## SSH dashboard settings (v0.2.34)

```toml
[tui.ssh]
background_refresh = true
```

This option controls only automatic Tailscale status in the active SSH view. LAN scans and connection tests remain explicit. `ssh diagnose --ping --network-only ALIAS` observes ICMP/TCP/banner without an SSH authentication attempt; omit `--network-only` for full verification. `--network-only` cannot combine with `--compare-qos`.

## Optional dashboard observations

The dashboard version hint uses the existing update configuration and release
cache; it does not perform upgrades. REMOTE statistics are optional dated
observations. Existing remote JSON/cache v2 and snippet JSON schema 1 gain an
optional `metrics` object (and Gist `node_id` identity); existing fields retain
their meaning. Each count carries `state`, optional `value`, `observed_at`, and
an optional bounded error. A failed refresh may retain the previous value/time
with `state: error`; `attempted_at` then records the later attempt. Missing/null
counts never mean zero. Inventory `complete` remains independent of statistics.
Old caches remain readable. Missing GraphQL permissions or older GitLab schemas
leave counts unavailable while repositories remain usable. See the
[dashboard guide](../guides/dashboard-actions.md#remote-statistics-and-sorting).
