---
name: dev-cli
description: 'Manage repositories, SSH hosts, remote fleets, and work-in-progress with the dev CLI: create/clone/setup agent-ready repos, bootstrap and organise them safely, discover/configure OpenSSH aliases without copying private keys, own worktree/task lifecycle, safely prepare/finalize/retire agent sessions, render or hand off deterministic prompt recipes, run guarded Git transactions, capture sidecar repo notes, track HOT/WARM/COLD tasks, navigate via TUI, and bridge gh/glab/Azure DevOps/herdr/tmux/zellij. Use when onboarding or probing an SSH host, configuring fleet machines, creating/starting/parking/resuming/retiring work, preserving agent transcripts, triaging pull requests or closeout context, scanning skills/MCP or organising repos, capturing/searching repo thoughts, choosing worktree isolation, or cleaning stale branches/checkouts/sessions.'
---

# dev-cli

The `dev` command is a thin glue layer over git, worktrees, forges and agent
runtimes. It exists to stop four different things collapsing into one:

```
git remote      durable code state, the source of truth
git worktree    a disposable local checkout
herdr / tmux / zellij
                a per-host live runtime
dev             human intent: what am I working on, and what is next
```

Everything derivable from git or the runtime is derived live. `dev` persists
human/task facts such as identity, checkout mode, **state**, **owner** and **next
action** in one TOML file per task under configured `paths.state_dir/tasks`
(default `$XDG_DATA_HOME/dev/tasks`); transient pane data is not persisted.
Machine connection intent has separate owners: OpenSSH remains authoritative,
while dev owns only canonical `~/.ssh/dev.d` fragments and explicit generated
fleet registrations beside user-authored `remotes.toml`.

**The problem it solves:** when a terminal multiplexer's sidebar is the only
record of what you are working on, nothing can ever be closed. `dev` gives that
record a home outside the runtime, so closing a session stops meaning
abandoning a task.

## Check it is there first

`dev` is not installed everywhere. Before using it:

```bash
command -v dev || echo "not installed"
dev doctor          # version/install owner/path, PATH collisions, and capabilities
```

Only **git** is required. System `ssh` enables SSH evaluation/probing/bootstrap
and fleet transport; `ssh-keygen` enables key derivation/generation; Windows
OpenSSH targets need PowerShell. `herdr`, `tmux`, `zellij`, `gh`, `glab` and
Azure CLI enable other capabilities and degrade cleanly when absent — never make
a step hard-depend on one unless the requested operation crosses that boundary.

## Worktree ownership — read this before creating any worktree

There are three mechanisms that create git worktrees, and picking the wrong one
is the most common source of confusion. The rule:

| Kind of worktree | Owner | Where | Lifetime |
|---|---|---|---|
| Feature, fix, experiment, cross-machine handoff — anything you might return to | **`dev`** | `paths.worktree_path`, default `~/Worktrees/<repo>/<branch-slug>` | until external `dev retire` |
| Harness-owned turn-scoped isolation (`isolation: worktree`, `/batch`, `EnterWorktree`) | **Claude Code** | `.claude/worktrees/` (keep gitignored) | owned by that harness; not a transcript-relocation guarantee |
| `herdr worktree create` | **not used** — `dev` runs `git worktree add` itself, then `herdr worktree open --path …` | — | — |

**Rule of thumb: if code/history/plan must remain reviewable or a human may
return tomorrow, use `dev`. Use native isolation only when the harness owns the
entire turn-scoped lifecycle.**

Why `dev` does not delegate to `herdr worktree create`: the path policy has to
hold on machines where herdr is not installed. `dev` creates the checkout with
plain git and asks herdr only to *open* it — which still makes it appear in the
sidebar grouped under its parent repo with its own branch and ahead/behind row.

Never nest a `dev` worktree inside a repository. A checkout inside another
checkout makes every indexer, file watcher and `rg` run see a second copy of
the tree. `dev` refuses to do it.

Full detail: `references/worktree-ownership.md`.

## The task lifecycle

| State | Git | Runtime | Meaning |
|---|---|---|---|
| 🔥 `hot` | worktree + branch | session open | working on it now |
| 🌤 `warm` | worktree + branch kept | normally closed | back within days |
| ❄️ `cold` | committed and pushed; worktree removed | nothing | paused, reconstructible anywhere |
| ✅ `done` | merged | may remain open | MERGED; external retirement pending |

```bash
dev repo open api                                  # ad hoc; no task/worktree
dev start api --task "typo" --direct                # track current/main directly
dev start api --task "small" --branch-only --base main
dev start api --task "token refresh" --base main   # isolated worktree → hot
dev park --next "add the regression test" --wip    # → warm, session closed
dev park --cold --push                             # → cold, worktree removed
dev resume "token refresh"                         # → hot, rebuilt if needed
dev prepare --session claude:<uuid>                # arm final transcript handoff
dev done                                           # TTY wizard: inspect dirty state, then FF/PR/merged
dev done --ff                                      # → done/MERGED; resources kept
dev retire "token refresh"                         # external-only cleanup → RETIRED
dev sweep --ephemeral-worktrees --json              # strict V1 report only
```

For stale Claude Workflow isolation, run `dev sweep --ephemeral-worktrees` only
from the canonical non-bare checkout. The bounded metadata adapter never emits
prompt/script/log/result/transcript content. A candidate must have one exact
mapping, terminal workflow, done agent, journal start/result, no same-ID resume,
sufficient provider inactivity, clean Git including no ignored or recursive
submodule content, no task/unsafe artifact/runtime/caller claim, and unchanged
registration/branch/HEAD/common-dir facts. It additionally requires a provider-
observed opaque registration generation matching the live registry. Claude Code
2.1.259 does not expose that non-replayable Git identity, so current Claude claims
report unknown and remain report-only; path/name/GitDir reuse is not proof. Apply
is TTY-only, rejects bypass
flags, confirms each item, and revalidates the fingerprint under a common-dir
lock before non-force removal. It never prunes, closes runtimes, changes Claude
metadata, or repairs dirty work. Branches are retained unless separately safe
`--delete-branches --base <ref>` was requested.

**Parking is the move that matters.** `dev park --next "…"` is what makes it
safe to close a session, and the `--next` text is what makes resuming cheap.
Always suggest a `--next` when parking.

Full detail: `references/task-lifecycle.md`. Before integrating or deleting an
agent-owned checkout, read `references/agent-retirement.md`.

### Repository flow preview

`dev flow [repo]` is a preview-labelled, full-screen, TTY-only repository
state-machine UI independent of the six-view dashboard. From any canonical or
linked checkout it opens the canonical repository and focuses that exact
surface; outside Git it opens a picker. It lists every registered worktree plus
task-only records, including COLD/DONE tasks without a checkout.

Rows are `canonical`, `managed`, `unmanaged`, `harness`, `task-only`, or
`conflict`. Managed bindings receive legal mode/state lifecycle actions.
Eligible unmanaged linked rows receive metadata-only Adopt and clean, non-force,
branch-preserving Remove Checkout. Canonical checkout removal, harness cleanup,
and destructive conflict resolution fail closed.

Enter always builds a plan first. The plan separates persisted HOT/WARM/COLD/DONE
intent from live facts, shows conditions/remediation/effects/retained resources/
fallback, and applies only after `y` or its displayed typed token. Apply locks and
revalidates the task revision plus exact repository/worktree/ref/runtime/artifact
identity; partial effects remain in a result ledger with recovery.

`r` reloads local facts without network access. `R` offers explicit fetch,
review query, or both; review evidence is run-local and only portable existence,
state, draft, URL, provider, and observation time—not decisions or checks.
Runtime `none` leaves occupancy unobserved without invalidating otherwise fresh
local Git/task facts. The preview omits dirty commit/discard, WIP, shared-writer,
takeover, and unknown-runtime overrides; use the displayed CLI fallback when an
expert compatibility flag is actually required.

## Pick the task's checkout mode

A task does not imply a worktree. Use the lightest explicit boundary:

- `dev repo open <repo>` / REPOS Enter — ad-hoc open, no task at all.
- `dev start … --direct` / REPOS `d` — track current branch (usually main),
  create no branch/worktree. HOT ↔ WARM only; `dev done` needs no merge mode.
- `dev start … --branch-only` — create and switch a branch in the canonical
  checkout. No concurrent branch there.
- `dev start …` / REPOS `s` — default isolated branch + worktree. Use once work
  may be interrupted, experimental, or parallel.

Do not create isolation before it has a job, but do not carry dirty main changes
into a parallel task implicitly either: a worktree starts from committed HEAD.
Checkpoint first when the new task depends on that work.

## Parallel background work

Read `references/parallel-agents.md` before spawning an independent agent while
another remains live. The fail-closed workflow uses `dev start --json`, requires
a newly created first-class Herdr worktree and its exact returned root pane,
then launches an explicit SpecStory/profile command there. Reused, fallback,
non-Herdr, missing or unverified panes are never launch targets.

For a human one-liner, `dev start --run '<shell command>'` uses the same exact
new-worktree/root-pane proof and `--focus` independently switches afterward.
It does not choose a launcher profile, permission mode, agent name, or wait
policy; use the full JSON workflow when those require separate validation.

`dev` guards writer-claiming direct/branch starts and resume when another
recognized Herdr agent occupies the same canonical Git worktree. Every state,
including `idle`, `done`, and `unknown`, is occupied; Herdr resolves the current
pane before excluding it. Pure repo/worktree/TUI open is navigation to the live
owner, not writer authorization. Never add `--allow-shared-checkout` unless
agents already have coordinated disjoint file ownership.

A new external agent does not inherit the parent permission mode, and a resumed
Claude fork does not restore bypass. Inspect the effective SpecStory/provider
command: local wrappers may already be elevated, so never claim they are safe or
append a conflicting mode. Herdr `done` is turn-settled—not cleanup done.

## Everyday commands

```bash
dev                        # interactive dashboard (plain listing when piped)
dev note add "thought"     # append to repo containing cwd
dev note search "query"    # rebuilds FTS index if needed
dev edit                   # open the effective config; generate it first if absent
dev ls                     # what am I working on, everywhere
dev ls --json              # stable machine-readable form (also over ssh)
dev status                 # local repo/branch/task/session + scoped readiness
dev flow [repo]            # TTY-only guarded repository lifecycle preview
dev sweep                  # what has gone stale or drifted, and what to do
dev sweep --apply          # act on it, confirming each change
dev sweep --merged-worktrees  # from main, audit contained tracked/untracked worktrees
dev pr list                # requests you opened or were asked to review
dev pr list --scope local --state merged  # forge evidence; never retirement authority
dev prompt agents [--json]                # sorted, redacted host profile inventory
dev prompt render pr-triage               # inspect/copy deterministic context
dev prompt run session-close --agent <name>  # bounded batch; no user stdin
dev prompt open workspace-closeout . --agent <name>  # current foreground TTY
dev prepare --session claude:<uuid>  # arm exact post-writer artifact commit
dev artifact list          # finalization handoffs and receipts
dev done --ff              # integrate only; runtime/worktree stay alive
dev retire <task>          # external-only close/wait/remove/reap

dev git uncommit           # soft reset with a per-worktree message receipt
dev git recommit           # reuse that exact message with normal hooks
dev git pull-rebase        # exact stash OID + index-preserving restore
dev git amend-all          # add -A + no-edit amend; hooks remain enabled

dev start                  # context-aware wizard; confirms before creating
dev start -t "token refresh" --base main  # fast managed worktree task
dev start -t "token refresh" --base main --run 'codex' --focus

dev wt create feat/auth --base main    # worktree at the configured path
dev wt list                            # every worktree of this repo
dev wt plan                            # what a new worktree would be set up with
dev wt plan --write                    # seed committed .dev-cli/config.toml
dev wt rm feat/auth                    # remove the checkout; the branch stays

dev repo list --sizes      # repos, recovery topology, owned logical bytes
dev repo list --no-remote  # local Git with no configured remote
dev repo list --local-only # branches lacking a remote-backed upstream
dev repo context [repo]    # agent-ready Markdown; --json is schema-v1
# --refresh alone performs live forge/configured-fleet probes
dev repo clone owner/name  # expand forge shorthand, then clone with Git
dev repo clone https://dev.azure.com/acme/Platform/_git/api
dev repo new               # confirmed repository/scaffold/upstream wizard
dev repo create api        # minimal scripted creation (alias of repo new)
dev repo new api --template owner/starter --check-in=stage
dev repo setup . --preset agent-ready  # initialize an existing clean repo
dev repo sync --all        # fetch + prune, and report what moved
dev ssh init                # report the one-time managed Include plan
dev ssh list                # static aliases/provenance; no ssh or network
dev ssh show lab            # definitions plus plain ssh -G effective values
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh probe lab           # fresh ordinary BatchMode authentication
dev fleet list              # aggregate repo/task/runtime state over SSH
dev fleet machine-id lab    # verify the optional target machine pin
dev fleet sync api --push   # publish, then fast-forward safe matching checkouts
dev fleet files api --to lab  # report-only explicit ignored-file plan
dev repo remote [query]     # search configured forge inventories
dev bootstrap ~/code       # recursively inventory an existing machine
dev bootstrap ~/code --index ~/Projects   # plan a non-destructive symlink catalog
dev gitignore              # .gitignore from GitHub templates + the common bits
dev adopt                  # import existing worktrees/sessions/branches as tasks

dev try redis-streams      # dated scratch directory for an experiment
dev tries mark redis-streams --add important  # durable personal metadata
dev tries archive redis-streams               # reversible; does not delete
dev graduate redis-streams --category Infra   # promote it into a real project

dev stats --heatmap        # where the time actually went
dev stats backfill --repo api  # seed one repo; TUI H then b does this in place
dev stats path             # durable XDG data, not cache
dev summary                # current machine-wide agent context
dev journal                # today's agent-ready development journal
dev journal --since 7d --metrics | opencode run "summarize this"
dev cache list             # regenerable forge/fleet/size/gitignore/license/note-index caches
dev help worktrees         # quick-reference pages; dev help wt also works
```

Complete generated reference: `references/commands.md`.

## SSH host onboarding and fleet registration

Read `references/ssh-hosts.md` before changing SSH configuration, installing a
key, or registering/removing a fleet host. The safe sequence is explicit:

```bash
dev ssh init                     # report only
dev ssh init --apply             # confirm the one-time Include
dev ssh list                     # static; no ssh/Match exec/network
dev ssh show lab                 # deliberately runs plain ssh -G
dev ssh setup lab --hostname 192.0.2.20 --config-only
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh probe lab
dev ssh remove lab --fleet       # narrow owned-fragment removal
```

OpenSSH is authority. Dev reads foreign definitions but never edits them; only
canonical portable aliases under `~/.ssh/dev.d` are mutable. Full setup needs an
explicit `--key` or `--generate-key`. It sends only one public record to a fixed
remote installer, preserves host-key policy, disables connection sharing for
proofs, and never copies/reads private key bytes for transfer. A working
ProxyJump is skipped unless `--install-on-working-jump` is explicit. Windows
administrator authorized_keys needs the separate
`--windows-admin-authorized-keys` consent and may still need manual elevation.

`--fleet` is never inferred. It writes a strict generated `remotes.d` fragment
only after a second fresh ordinary alias login succeeds. The primary
`remotes.toml` remains user-authored and is edited through
`dev fleet config edit`; `fleet config show` displays the merged config and identifies generated
origins. `remote_os` selects POSIX shell or encoded Windows PowerShell transport.

Treat partial and unknown results literally. If a remote installer started, the
key may have arrived; do not "clean up" `authorized_keys`. Local config and
generated keys are retained so a rerun can converge. `ssh remove` never revokes a
remote key, deletes local keys/known_hosts, or removes the shared Include.

Read `references/prompt-handoffs.md` before using `dev prompt`, configuring a
launcher, or interpreting session/workspace closeout advice. Discover profiles
with `dev prompt agents`; its output is intentionally redacted. Profile selection
is global before recipe collection, requested modes never fall back across
profiles, and prompt recipes remain read-only context handoffs rather than
mutation or permission authority.

## Bootstrapping and adopting an existing machine

Read `references/bootstrap.md` before recursively scanning paths, creating a
symlink index, moving physical repositories, or generating layout config. Move
is destructive and must follow its report → review → apply procedure.

There is nothing to migrate for ordinary use. `dev` discovers repositories through
`paths.scan_roots` plus exact `paths.repo_paths` and never moves, renames or deletes anything.

```bash
dev config init     # detects this machine's roots; every value stays editable
dev adopt           # report existing worktrees, sessions and unmerged branches
dev adopt --apply   # record them as tasks (nothing on disk changes)
```

Do not assume the user's layout is `~/Documents/Program`. Run `dev config show`
or `dev repo list` to see what this machine is actually configured for.

## Creating and setting up repositories

Read `references/repository-bootstrap.md` before creating a published repo,
running project-owned hooks, or installing setup-capable skills. A bare
`dev repo new` is interactive; `dev repo new NAME` deliberately retains the
minimal README + initial-commit contract. A clear Git URL, local Git path, or
`owner/name` argument to `new` or `create` automatically clones and preserves
its remote; template and upstream-creation flags cannot accompany that route.
Explicit `repo clone` remains available when scripts should state acquisition
directly. On a TTY, bare `repo clone` selects an exact clone URL from the
existing private forge cache and retains a manual URL/path/owner-name option;
it never refreshes providers implicitly. Outside a checkout, bare `start`
selects a local repository with fast live discovery; inside one it keeps the
immediate current-repository default. The configured line selector defaults to `fzf` and
falls back to dev's built-in Bubble Tea picker when missing; non-TTY input stays
line-oriented.
`repo new --template` snapshots a local directory or Git tree into a fresh
history; it is not a clone. `--template-ref` and `--template-subdir` select the
exact source tree.

Use `--check-in=commit|stage|none` to choose whether generated changes are
committed, staged for human review, or left unstaged (`auto` follows a new
repository's preset and otherwise defaults to none). `repo setup --commit`
remains the compatibility spelling for commit.
Stage mode cannot create an upstream and cannot hand dirty setup directly to
`start`.

On a TTY, wizard fields are real inline editors: arrows, Home/End,
Backspace and Delete edit typed text rather than inserting escape sequences.
Esc or Ctrl-C cancels; piped input remains line-oriented.

The create/setup wizard asks one customization-gate question after preset
selection. Declining uses resolved defaults and skips detailed template,
README/gitignore/license/agent/skill questions; explicit customization flags or
a required input without a default make yes the suggested answer.

Global recipes live in `scaffolds.toml`. Repositories may commit the allowlisted
`.dev-cli/config.toml` and `.dev-cli/scaffolds.toml`, but executable project
configuration in those files must be trusted by its exact content hash before
it runs. Legacy `.dev.toml` retains its compatibility behavior.

## Dashboard and forge inventory

Bare `dev` / `dev tui` is the seven-view dashboard described here. It is separate
from the plan-first `dev flow [repo]` repository lifecycle preview above.

The dashboard has TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS and MCP views, switched
with tab, vim-style h/l, or a left click on a visible tab. Left-click selects a
row without opening it, the wheel moves three rows, and right-click selects a row
and opens the same legal actions used by keyboard shortcuts. Repeated clicks do
not imply open. Wide TASKS tables show `REPO`; compact layouts keep it in detail.
TRY `n` creates an experiment; `space` opens metadata/lifecycle actions; `a`
includes retained history. Archive is a reversible same-filesystem move, not
deletion or disk reclamation. `?` opens the full key map.

The initial view never waits for runtime/project-root resolution, cache decoding,
or shell tool probes. TASKS/REPOS/TRY publish independently from one shared local
cycle; optional tabs remain lazy. Refreshes are generation-scoped: late results
are ignored, failed refreshes keep usable rows, warning-only capability results
stay fresh, visible tabs resume after REPOS recovery, and valid empty results
clear obsolete rows. For a one-run diagnostic, set `DEV_TUI_TRACE` to an absolute new
file. The private bounded trace is written after TUI teardown with relative
startup/view timings and aggregate row counts only; it excludes names, paths,
commands, key values, URLs, handles and raw errors, and never enters `stats.db`
or a network sink. SKILLS and MCP are startup-context-first: inside Git they
scan only the exact current checkout plus global/user sources; outside Git they
reuse every accepted REPOS target and the ordinary startup directory. Uppercase
`A` toggles both views to all accepted repositories for the current TUI run.
Skill listing uses the native versioned 77-agent path registry and never runs
Node/`npx`; `c` hashes remote Git objects without checkout filters, while
mutations require a trusted direct provider and are serialized. MCP inventories
only sanitized static declarations for Claude Code, Codex, Cursor, Gemini CLI and
OpenCode; only Claude's documented project approvals are resolved. It never
starts or health-checks a server. In either capability view, `e` opens the selected
local source and `y` copies its path, safe summary, or explicit raw file; raw reads
are regular-file-only, capped at 1 MiB, and may put credentials into the system
clipboard without changing sanitized inventory output. Full detail:
`references/agent-capabilities.md`.

REMOTE queries authenticated `gh` and `glab` plus configured Azure DevOps
organization/project targets lazily and fully paginates every repository the
account can access. A private cache is decoded after the first view; stale rows
stay searchable while refresh runs. Malformed/oversized payloads and caches
fingerprinted for another configured GH/GL host or Azure target are ignored.
GitLab uses explicit `GITLAB_HOST`/`GLAB_HOST` (default `gitlab.com`) instead of
cwd inference; a successful empty refresh clears obsolete rows. `/` filters
provider, owner/name, visibility and description, with `vis:private` for an exact
visibility match. Enter opens an existing local clone. For an absent remote, `c`
confirms; `enter` clones and stays in the dashboard, while `o` clones and opens
after a spinner-backed, local-only REPOS refresh. A `project_root` outside the
configured REPOS discovery roots/depth is rejected before mutation. `q`/Ctrl-C
requests cancellation without abandoning the in-flight result. Once accepted,
REMOTE shows `repo` and REPOS can search the clone immediately. If Git leaves a
destination after failure, REMOTE marks the exact path `inspect` and does not
delete it automatically. Use `dev repo remote [query] --json` for the non-interactive form; `--cached` avoids
a network query and `--refresh` forces a complete synchronization.

`dev journal` derives calendar-day reports from Git rather than storing a
second history database. The default current-user report adds existing
session/WakaTime observations, matching task intent and recent dirty-worktree
snapshots, clearly separated from committed history. `auto` expands at most 100
commit details while retaining complete aggregates; `--max-commits 0` is
unlimited. Use `--author <email>` or `--all-authors` for commit-only team views,
and `--json` for a stable structured report. dev generates the context only; it
does not launch the receiving agent.

`dev summary` is the complementary current-state view across this machine. It
combines durable repositories and present active/deprecated Tries with live Git,
worktree, task, runtime, recovery and catalog facts. The default `auto` Markdown
expands active work and leaves quiet projects in a compact index; `--attention`
selects both active work and recovery hazards, while `--json` always emits the
complete selected snapshot. Use journal for time ranges and `repo context` for
one repository.

Azure DevOps inventory is opt-in and cloud-only:

```toml
[[forge.azure_devops]]
organization = "https://dev.azure.com/acme"
project = "Platform"
```

Install Azure CLI's `azure-devops` extension separately. `dev` never installs
the extension, logs in, changes Azure defaults, or stores a PAT.

FLEET merges user-authored primary `remotes.toml` with strict dev-owned sibling
`remotes.d/ssh-<alias>.toml` fragments, then reuses the accepted REPOS snapshot
for the local host and calls each configured host's own `dev` over SSH. Generated
entries are owned by explicit `dev ssh setup/remove --fleet`; FLEET `e` and
`fleet config edit` open only the primary file, while `fleet config show` marks
generated origins. `remote_os` selects POSIX shell or allowlisted encoded
PowerShell transport and participates in endpoint cache identity with the SSH
port and optional `machine_id` pin. If REPOS is still loading, FLEET waits while keeping cached rows instead
of rescanning. The TUI hides this machine by default because REPOS is richer;
`a` includes local rows. `dev fleet list` remains local plus remote. Enter
prefers Herdr remote navigation and falls back to an SSH login shell.
`dev fleet sync` is explicit and strict: it only fast-forwards a clean checkout
of the same behind-only branch; dirty/ahead/diverged clones are never rewritten.

`dev fleet files [repo-or-path] --to <host>` is a separate one-shot channel for
explicitly exported ignored files. It is report-only unless `--apply` is passed;
`--yes` never implies `--replace`. Both clones must already match by fetch
identity, attached branch and exact commit, and each exact path must be untracked
and ignored on both sides. Only bounded regular files are accepted. Apply also
requires a `machine_id` pin obtained with the read-only `dev fleet machine-id
<host>` command and verified independently. The operation never clones, switches
branches, runs provisioning, transfers task/catalog/note state, deletes source
files, or evicts a repository; native Windows payloads are disabled.

REPOS has LIVE, LATEST and asynchronous SIZE. SIZE is logical
checkout+private-Git bytes; shared Git is separate and marked `+S`. Detail also
shows no-remote/local-only/multi-upstream recovery topology. Press space to
expand linked worktrees; children
show their own Git/session/task state and mark harness-owned or untracked
checkouts `(ephemeral)` / `(external)`. Press `y` followed by `y/p/b/s/w` to
copy contextual Markdown, path, branch, runtime/agent sessions, or every
worktree path. `dev repo context [repo]` prints the full Markdown without a TUI.
`[tui.repos]` chooses columns and default sort; `O` cycles sort and `R` reverses
it.

`H` opens the selected repo's heatmap. On an empty panel, `b` backfills only
that repo and redraws; `r` rereads existing stats. Stats live in
`$XDG_DATA_HOME/dev/stats.db` and are durable observations, not cache — use
`dev stats clear` with an explicit scope. `dev cache clear` only removes
regenerable forge/size/gitignore/note-FTS data under `$XDG_CACHE_HOME/dev`.

On ordinary views, `e` edits dev config and returning live-reloads data/tool
bindings; on SKILLS/MCP it opens the selected capability file and reloads only
that inventory. `r` reloads explicitly. Runtime backend changes require restarting
the TUI.

External tools are explicit `[[tui.tools]]` config. Run `dev tui tools` before
recommending a binding; it shows the exact command and whether it is available.
Commands run through `$SHELL` in the selected checkout. For an alias/function
that exists only in shell rc, set `interactive = true`; dev uses `$SHELL -lic`
and evaluates the command after rc loading. Prefer an executable on PATH when
the binding should be portable across machines.

## Repository quick notes

Read `references/notes.md` before adding/searching/editing/deleting notes or
when deciding whether content belongs in dev vs td/beads.

Quick notes are multiple timestamped Markdown files under configured
`paths.state_dir/notes/<catalog-id>/` (default `$XDG_DATA_HOME/dev/notes`); the
catalog ID attaches them to the canonical clone across worktrees, symlinks and
moves. `dev` does not synchronize notes or catalog state, so cross-host
attachment requires syncing both. SQLite FTS at `$XDG_CACHE_HOME/dev/notes.db`
is disposable: clearing it removes no thoughts and the next search rebuilds it.

TUI context keys: `n` quick-adds on TASKS/REPOS; `N` browses with `/` search,
Enter expand, `a` add, `e` editor, and confirmed `d` delete. In TRY, `n` remains
“new Try.” REMOTE needs a local clone.

## Editing configuration

Use `dev edit` (or `dev config edit`) rather than guessing the XDG path. It
opens the file selected by `--config`, generating the machine-detected starter
when absent, and resolves `--editor` → `$VISUAL` → `$EDITOR` → nvim/vim/vi.

## Rules for agents

1. **Do not equate task with worktree.** Ad-hoc opening needs no task; `--direct`
   tracks current/main; `--branch-only` is lightweight isolation; default
   worktree is for interruption/experimentation/parallelism.

2. **Always pass `--base` for branch/worktree tasks.** Without it a new branch starts from the current
   HEAD, so starting a task while standing on `feature/A` silently builds on
   `feature/A`. This is the single most common way to produce a confusing
   history unattended.

3. **Never `--force` a worktree removal on your own.** `dev wt rm` refuses a
   checkout with uncommitted changes; that refusal is the feature. Ask the
   user before overriding it.

4. **Report drift, do not fix it silently.** `dev sweep` without `--apply`
   changes nothing. Show the user its output rather than running `--apply --yes`
   for them. For merged worktree cleanup, run `dev sweep --merged-worktrees`
   from the canonical main checkout, present every candidate and blocker as a
   QA question, and use `--apply --yes` only after the user explicitly approves
   that exact set. Branch deletion remains a separate `--delete-branches` opt-in.
   `dev pr list --scope local --state merged` finds candidates, but a merged
   pull request is never on its own grounds to remove a checkout: a squash
   merge leaves no local ancestor, so containment still has to be proven
   locally. See `references/pull-requests.md`. If an explanation is useful, use
   the generic `workspace-closeout` recipe only after reading
   `references/prompt-handoffs.md`; its audit is advisory and never an approval.

5. **Prefer a checkpoint commit over `git stash`.** A stash is invisible in the
   log, easy to forget, and cannot be pushed — so it can never reach another
   machine. `dev park --wip` makes a `wip:` commit instead.

6. **One writer per branch at a time.** `dev resume` refuses a task owned by
   another host without `--force`. Before overriding, confirm that machine has
   pushed.

7. **Check `dev wt plan` before blaming a worktree.** A worktree that comes up
   broken is nearly always a provisioning gap, not a git problem. The plan
   shows the detected project types, which tools are missing, and exactly what
   would be copied or run. Dependencies arrive by a per-ecosystem strategy —
   `reinstall` (default), `copy`, `link` or `skip` — and dev refuses an unsound
   one rather than producing a broken checkout: a virtualenv cannot be copied,
   because it bakes its own absolute path into `pyvenv.cfg`.

8. **Do not create a worktree per agent.** Worktrees isolate *change streams*;
   panes isolate *agents*. A writer claim in one checkout needs explicit
   disjoint ownership; pure open/focus may navigate to the existing owner.

9. **Parallel launch is exact-pane and fail-closed.** Accept only a new
   first-class Herdr worktree/root pane from `dev start --json` or the same
   internal proof used by explicit `dev start --run`. Never infer from
   focus/sidebar order, relocate SpecStory with mid-session `EnterWorktree`, or
   treat any recognized agent state as a free pane. Never add
   `--allow-shared-checkout` without coordinated ownership.

10. **Launcher profile and effective permissions are explicit.** Standard
    Claude/Codex put the complete native command inside SpecStory `-c`; verified
    `*-copilot-once` wrappers already wrap SpecStory but inherit effective
    provider permissions, which may be dangerous. Never append a conflicting
    safer mode to an elevated command. Read `parallel-agents.md`.

11. **Cleanup is external, never self-triggered by agent state.** Herdr `done`
    means a turn settled. Arm and finalize exact artifacts first; `dev done`
    integrates only, and `dev retire` must run outside the target workspace.
    Never run raw `git worktree remove --force` against the caller's checkout.

12. **Prefer `dev bootstrap --index` over `--move`.** If the problem is
   navigation, a symlink catalog solves it without changing the authoritative
   paths. For a physical move, never add `--apply --yes` on the user's behalf;
   blocked rows are preconditions to resolve, not checks to bypass.

13. **`dev adopt` without `--apply` changes nothing.** Show the user its report
   rather than applying it for them — which branches count as work in flight is
   their judgement, not yours.

14. **Treat Markdown notes as durable and notes.db as cache.** Never delete
   configured `paths.state_dir/notes` to fix search; use `dev cache clear notes`
   and reindex. Note deletion itself requires explicit confirmation, and `dev`
   does not synchronize note/catalog state between hosts.

15. **Do not call stats.db a cache.** Session/WakaTime observations may not be
   reconstructible. Use `dev stats clear --repo/--source/--all`; use
   `dev cache clear` only for regenerable remote/fleet/size/gitignore/license/note-FTS caches.

16. **Archive is not eviction.** `dev tries archive` is a reversible hidden move
   on the same filesystem; it does not free space. Phase 1 has no project-data
   delete command. Never substitute `rm -rf` merely because a remote exists —
   no-remote, local-only refs, ignored files and stash are independent risks.

17. **Read Git state as counts, not a dirty boolean.** `⇡`/`⇣` are upstream
   divergence; `=` conflicts, `+` staged, `!` unstaged, `?` untracked. Use
   `dev status` or JSON for the unique-path and type breakdown before cleanup.

18. **OpenSSH is authority; foreign SSH config is read-only.** Run `ssh init`
   without `--apply` first, never hand-edit a dev-owned fragment, and never turn
   alias discovery into implicit `--fleet`. Full setup must name/generate a key,
   but only its public record may reach the remote. Do not copy private keys,
   weaken host-key policy, delete remote authorized_keys after an unknown
   result, or promise that `ssh remove` performs revocation.

19. **Portable export is separately authorized.** Never treat `[worktree].include`
   as permission to send a file off-machine. Use only `[local_files].include` or
   explicit `--file`, show the report first, independently verify and pin the
   target UUID before apply, and never infer `--replace` from `--yes`.

20. **Commit messages stay English** and follow Conventional Commits, even when
   the conversation is in another language — see the companion `git-workflow`
   skill, which owns commit conventions, SemVer and branch naming. This skill
   does not duplicate them.

21. **Never bypass `.dev-cli` project-config trust.** Inspect the rendered hooks
    and skill entrypoints, then use `dev config trust <repo> --yes`. A changed
    executable hash is a new decision. Pre-commit/gitleaks inspect generated
    content; they do not make an untrusted command safe to execute. Legacy
    `.dev.toml` remains a compatibility surface rather than acquiring this trust
    contract retroactively.

## When to use this skill

- "What am I working on?" / "我在做什麼" / too many terminal workspaces open.
- Starting, parking or resuming a piece of work.
- Spawning independent background agents without disturbing a live checkout.
- "Where should this worktree go?" / "should I use `claude --worktree` or herdr?"
- A new worktree is missing `node_modules`, `.venv` or `.env`.
- Cleaning up stale branches, worktrees and sessions.
- Rendering or handing off pull-request/session/workspace operational context.
- Cloning, creating or auditing repositories across a machine.
- Discovering, inspecting, configuring, probing, or removing an OpenSSH alias.
- Installing a public key through ProxyJump or registering a verified dev fleet host.
- Promoting an experiment into a real project.
- "Which repo do I spend my time in?"
- Setting up a `.gitignore`, or a harness worktree showing as untracked.
- Adopting a machine that already has repos, worktrees and sessions.
- Recursively scanning, indexing, or physically reorganising existing repos.
- Configuring a flat repo/worktree layout or generating config for one.

## When NOT to use it

- **Commit messages, SemVer, branch naming, PR-vs-main tiering** — that is the
  `git-workflow` skill. This one links to it rather than restating it.
- **Standalone Herdr pane/agent control** — that is the `herdr` skill. The
  `dev start --json` and explicit `--run` cross-tool launch paths remain here.
- A single `git add` + `git commit` needs no tooling at all.

## Reference files

- `references/ssh-hosts.md` — read before SSH discovery, managed-fragment
  changes, public-key bootstrap, probing, or generated fleet registration/removal.
- `references/notes.md` — read before repository quick-note operations or
  choosing the boundary with td/beads.
- `references/bootstrap.md` — read before scanning or organising an existing
  machine; includes the non-destructive index default and move safety gates.
- `references/worktree-ownership.md` — who owns which worktree, and how a
  worktree gets a working environment. Read before creating one.
- `references/task-lifecycle.md` — HOT/WARM/COLD/DONE, when to park, and how
  work moves between machines.
- `references/agent-retirement.md` — prepare/finalize/integrate/retire, caller
  safety, exact transcript handling and orphaned-runtime recovery.
- `references/runtime-herdr.md` — how `dev` and herdr divide responsibility,
  including native worktree grouping, collisions and cleanup semantics.
- `references/parallel-agents.md` — read before launching parallel background
  work; exact JSON/pane validation, SpecStory profiles and permissions.
- `references/commands.md` — the full command reference, generated from the
  binary by `dev skill sync` so it cannot drift.
- `references/repository-bootstrap.md` — new/clone/setup presets, project
  overrides, skill initialization, upstream publishing, trust and handoff.
- `references/pull-requests.md` — read before using `dev pr` or advising on a
  pull request; covers effective scope, local checkout health, why a field can
  be missing, and why a merged request cannot authorize retirement.
- `references/prompt-handoffs.md` — read before rendering/running/opening a
  generic recipe or configuring a launcher; covers transports, current-terminal
  behavior, permissions, closeout audits, and rebase-conflict safety.

## Gotchas

- **`dev` may be a shell wrapper around the binary.** The trusted `shell-init`
  output defines a wrapper that gives navigation commands a private side channel
  (a child-only file descriptor for POSIX shells, a `DEV_SHELL_CD_FILE` temp
  file for `shell-init powershell`), reads one NUL-terminated path and calls
  `builtin cd` / `Set-Location` without evaluating command output. Without the
  wrapper, the binary's printable legacy directive cannot change the parent
  shell — that is not a bug.
- **Windows runs SSH natively but without a multiplexer.** SSH discovery,
  protected managed fragments, ssh-keygen, public-key bootstrap, and Windows
  fleet transport are supported. There is no tmux, Zellij or Herdr on Windows,
  so the runtime backend is always `none`; `dev fleet open` starts a child shell
  instead of replacing the process. Windows remote helpers use only the encoded
  allowlisted PowerShell launcher; `remote_os` must match the target. `dev upgrade` self-replaces only
  a standalone install and otherwise delegates to the detected Homebrew/Scoop/
  `go install` command. Stable releases advance the Homebrew tap; never overwrite its Cellar
  binary manually.
- **A worktree is a clean checkout.** It has no `node_modules`, no `.venv` and
  none of the gitignored env files. `dev` provisions it; `--no-provision`
  skips that and leaves you to it.
- **Only explicitly included, gitignored stable regular files are copied.**
  Source swaps and symlinked destination parents are refused; existing targets
  are reported skipped, not copied. Do not universally include
  `.claude/settings.local.json`: only sticky/plain-Claude profiles opt in.
- **Worktree provisioning and fleet export use different allowlists.**
  `[worktree].include` is local-only. `fleet files` reads `[local_files].include`
  plus explicit `--file`, expands patterns before transport, and defaults to a
  content-free plan. It is not task transfer, backup, restore, or eviction.
- **A repository template is a snapshot, not a clone.** Source Git metadata,
  remotes and history are excluded. A local Git worktree without a ref includes
  tracked plus nonignored untracked files; a non-Git directory includes its
  complete current tree. Human plans preview paths and warn about live unpinned
  sources. URL userinfo is redacted, while held root/directory/file handles
  confine traversal. Symlinks, special files, traversal and source changes fail
  closed, and the snapshot applies only to an otherwise-empty new repo.
- **Lazygit commit drafts are a best-effort adapter.** `--check-in=stage` writes
  `LAZYGIT_PENDING_COMMIT` in that worktree's Git directory so lowercase `c`
  can preload the proposed message. This is not `commit.template` or
  `COMMIT_EDITMSG`, uppercase `C` and other clients need not read it, and an
  existing different draft is preserved. Draft write/sync failure is a warning,
  not rollback: the successfully staged index remains ready for manual commit.
- **Agent-ready guidance is honest about unknowns.** Its canonical `AGENTS.md`
  starter labels bootstrap status incomplete, supplies safe working/handoff
  rules, and leaves project purpose, commands, architecture, and invariants as
  TODOs rather than fabricating facts.
- **Agent history remains reviewable.** The common managed ignore excludes only
  `.specstory/statistics.json`; histories, project identity, and config remain
  visible. Built-in `agent-history-hygiene` additionally seeds
  `.specstory/.gitignore` only for machine-local `.project.json` and generated
  `statistics.json`; it does not ignore `.specstory/history/*.md`. An existing
  nested ignore keeps its custom content and mode while missing required rules
  are appended. Correct any broader parent `.specstory/` rule explicitly.
- **Removing a worktree never deletes the branch.** Those are separate
  decisions; conflating them is how work gets lost.
- **Bare `dev done` is interactive only on a TTY.** It reports branch relation
  and dirty-content equivalence, then offers commit/discard and FF/PR/merged
  handling. It records DONE without closing/removing resources; Retire is
  separate. Non-interactive use remains report-only without `--ff`/`--pr`. Unique discard
  requires `DROP` or explicit `--dirty=discard --yes`.
- **The stats sampler must be scheduled.** `dev stats` is empty until
  `dev stats backfill` runs once and `dev stats sample` runs periodically.
