# dev

A thin glue layer over git, worktrees, forges and agent runtimes.

Submodule superprojects can use a complete worktree with independently
initialized child repositories. Clone/worktree creation defaults to recursive
initialization at committed gitlinks; `start --submodule PATH` selects which
children get task branches. Explicit `--recursive` cleanup proves child recovery
before removing the parent. See [Submodule workspaces](docs/guides/submodule-workspaces.md)
for configuration, inside-out integration and recovery limits.

Use `dev submodule add` (or `dev repo add-as-submodule`) to select a known
repository and add it to this checkout. Choose pinned/default-branch checkout;
only the new gitlink and `.gitmodules` are staged, with no commit or push.
In REPOS/REMOTE, `y u` copies a clone URL without fetching.

It exists to stop four things collapsing into one:

```
git remote      durable code state, the source of truth
git worktree    a disposable local checkout
herdr / tmux / zellij
                a per-host live runtime
dev             human intent: what am I working on, and what is next
```

Everything derivable from git or the runtime is derived live. `dev` persists
human intent Git cannot answer—task **state/owner/next action/context**, stable
asset identity, catalog metadata, repository quick notes, and experiment
lifecycle—and a few explicit machine policies such as generated SSH/fleet
fragments. OpenSSH, Git, and each remote machine remain authoritative for the
connection, code, and host-local state they own. Live Git status stays live;
logical size measurements are explicitly disposable cache.

## The problem

When a terminal multiplexer's sidebar is the only record of what you are
working on, nothing can ever be closed: closing a workspace loses the thread.
So the sidebar grows to thirty-odd entries and stops being scannable, which
defeats the point of having one.

The fix is not discipline. It is giving "what am I working on" a home outside
the runtime, so closing a session is just closing a session.

Bare `dev` opens an interactive dashboard when it has a terminal, and prints
the plain listing when piped — so `dev | grep` and `dev > file` behave as
expected.

```
$ dev ls
   TASK                   STATE  REPO       BRANCH                    GIT   AGE  SESSION       NEXT
🔥 token refresh          HOT    atp-sipui  fix/gx-security-recovery  ↑2 ●  2h   herdr:working add the regression test
🌤 orderbook experiment   WARM   trading    exp/orderbook-v2          clean 6d   —             compare against the baseline
❄️ settings redesign      COLD   website    feat/settings             clean 3w   —             pick up after the API lands
```

## Repository hygiene

Use `dev hygiene manage --all` to preview and deploy commit checks across selected
repositories, independently of agent skills. Use `dev hygiene status` to inspect effective hooks and `dev hygiene setup` to
preview configuration. `scan --scope staged|worktree|history` checks secret/privacy
policy; reviewed `rules` and `redact` plans keep private values and recovery outside
Git. Hooks block rather than auto-stage. Final transcript cleanup requires the
writer to stop first. See the [hygiene guide](docs/guides/hygiene.md).

## Install

### Homebrew (macOS)

```bash
brew install daviddwlee84/tap/dev-cli
dev config init  # detects this machine's repo roots and writes a config
```

The formula installs the `dev` binary plus bash, zsh and fish completions. It
does not write into your home directory or install the bundled agent skill.
Each stable release updates the tap automatically. A Homebrew-owned `dev`
never self-replaces: `dev upgrade` delegates to the matching `brew upgrade`
command, which preserves Homebrew's install records, linking, rollback, and cleanup.
Maintainers can retry or backfill formula publication for an existing release
with `gh workflow run publish-homebrew.yml -f version=vMAJOR.MINOR.PATCH`.

### Scoop (Windows)

```powershell
scoop bucket add dev-cli https://github.com/daviddwlee84/scoop-bucket
scoop install dev-cli
```

The manifest for each release is also attached to the GitHub release as
`dev-cli.json` if you prefer `scoop install <url>`.

### Go or source

```bash
go install github.com/daviddwlee84/dev-cli/cmd/dev@latest
# Pin @v0.2.31 instead when you need a reproducible install.
# Or from a checkout: make install  # also installs the bundled agent skill
```

Source checkouts include `mise.toml`, pinning the repository toolchain to Go
1.26.4. With mise installed, `mise install` selects the same compiler used by
the module and CI instead of relying on a possibly mismatched global GOROOT.

Every release also publishes `darwin/arm64`, `darwin/amd64`, `linux/amd64`,
`linux/arm64` (`.tar.gz`) and `windows/amd64`, `windows/arm64` (`.zip`) archives
with a `SHA256SUMS` file, so a binary can be verified without a Go toolchain.

To find out whether the binary you have is current, and to update it:

```bash
dev version           # what this build is, and whether it is a published release
dev version --check    # also ask GitHub for the newest release (cached for a day)
dev doctor            # reports the running version, install owner/path, and PATH collisions
dev upgrade --check    # report whether a newer release exists
dev upgrade            # delegate to its owner, or verify and replace a standalone binary
```

`dev upgrade` replaces the binary in place only for a standalone install. If
Homebrew, Scoop or `go install` owns the file, it runs that tool's upgrade
command instead; release automation advances the Homebrew tap so that command
can see every stable tag. Once a day an interactive `dev` command prints a one-line hint
when a newer release is cached; set `[update] check = false` in `config.toml`
(or export `DEV_NO_UPDATE_CHECK=1`) to silence it. Every network call here is
either explicit or a best-effort background refresh — `dev --version` and
`dev doctor` stay local and work offline.

### Windows

`dev` builds and runs on Windows, and core commands work. Native Windows support
also covers SSH host discovery/managed fragments, OpenSSH key generation and
bootstrap ACL checks, and fleet transport to POSIX or Windows OpenSSH servers.
Windows fleet targets launch only dev's allowlisted hidden helpers through an
encoded PowerShell command. There is no tmux, Zellij or Herdr on the controller,
so `dev` uses the no-multiplexer backend (it prints a `cd` directive the shell
wrapper consumes). Use the PowerShell wrapper:

```powershell
Invoke-Expression (& dev shell-init powershell | Out-String)
```

For a non-Homebrew install, generate completion files wherever your shell loads
them:

```bash
mkdir -p ~/.zfunc ~/.local/share/bash-completion/completions ~/.config/fish/completions
dev completion zsh  > ~/.zfunc/_dev
dev completion bash > ~/.local/share/bash-completion/completions/dev
dev completion fish > ~/.config/fish/completions/dev.fish
```

Tab completion includes commands and flags plus local tasks, repositories,
worktrees, statically discovered SSH aliases, help topics, and bundled gitignore
templates. Completion never queries a forge, runs `ssh`/`Match exec`, or touches
the network.

Add the directory-changing wrapper to your shell rc file:

```bash
eval "$(dev shell-init zsh)"      # zsh
eval "$(dev shell-init bash)"     # bash
dev shell-init fish | source      # fish
```

```powershell
Invoke-Expression (& dev shell-init powershell | Out-String)   # PowerShell
```

A child process cannot change its parent's working directory, so the wrapper
passes directory changes back through a private, child-only file descriptor
while leaving normal stdout/stderr connected to the terminal. That preserves
the interactive TUI and normal pipe behavior.

The agent skill is an explicit, optional post-install step:

```bash
dev skill install
dev skill list --all    # native project/global inventory across repositories
dev mcp list --all     # static, sanitized MCP declarations for five agents
dev doctor             # what works on this machine, and what degrades
```

Only **git** is required at runtime. The system `ssh` client enables SSH host
inspection, probing, bootstrap, and fleet transport; `ssh-keygen` enables public
companion derivation and key generation; PowerShell is needed on a Windows
OpenSSH target. `herdr`, `tmux`, `zellij`, `gh`, `glab`, and Azure CLI each enable
other capabilities and degrade cleanly when absent. `dev doctor` checks these
capabilities locally without evaluating an alias or contacting a host.

## Onboard OpenSSH hosts

OpenSSH remains the source of truth. Dev can read exact aliases from the active
user Include closure, but it mutates only canonical files in its dedicated
namespace and never rewrites a foreign `Host` block.

Run the one-time initialization as a report, then explicitly apply it:

```bash
dev ssh init                         # exact plan; no write
dev ssh init --apply                 # confirm interactively
dev ssh init --apply --yes           # confirm the local plan non-interactively
```

The only root directive dev installs is `Include ~/.ssh/dev.d/*.conf`, placed
before the first `Host` or `Match`. If root metadata or the managed namespace
cannot be preserved safely, init makes no change and gives manual placement
guidance.

Discover aliases statically, then cross into OpenSSH evaluation only when needed:

```bash
dev ssh list                         # no ssh, resolver, Match exec, agent, or network
dev ssh list --format tsv            # alias/status/owner/source/line/fleet rows
dev ssh list --json                  # one schema-versioned object
dev ssh show lab                     # definitions plus plain `ssh -G lab`
dev ssh probe lab                    # one fresh BatchMode login, sharing disabled
```

`show` includes system configuration because it runs plain `ssh -G`; configured
`Match exec` and resolver behavior may run. `probe` preserves the user's host-key
and known-hosts policy. Static discovery reports incomplete/dynamic closure and
conflicts instead of calling uncertain aliases usable.

Setup is one idempotent command for new managed aliases, existing managed
aliases, and read-only foreign aliases:

```bash
dev ssh setup lab --hostname 192.0.2.20 --config-only
dev ssh setup lab --hostname 192.0.2.20 --key ~/.ssh/id_ed25519 \
  --target-os posix
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup winlab --hostname 198.51.100.30 --generate-key \
  --key-path ~/.ssh/id_winlab --target-os windows --fleet
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --dry-run
```

Public-key bootstrap requires exactly one explicit `--key` or `--generate-key`. Generated
keys are Ed25519; noninteractive generation additionally requires
`--no-passphrase`, while an interactive `ssh-keygen` owns hidden passphrase
prompts. A private/security-key identity passed to `--key` must have a validated
`.pub` companion; dev may derive a missing public companion with confirmed
`ssh-keygen -y`, but never reads or transfers private bytes.

Inspect available identities before choosing one:

```bash
dev ssh key list                    # local public-key metadata plus the current agent
dev ssh key list --no-agent --json  # file metadata only; no OpenSSH evaluation
dev ssh key list --alias lab        # explicitly evaluate lab's identity/agent settings
dev ssh key doctor                 # metadata-only permission report; no agents or ssh
dev ssh key doctor --fix            # preview exact repairs, then confirm
dev ssh key doctor --key ~/.ssh/custom-key --fix --yes --json
```

The setup wizard offers a key picker with paths, comments, fingerprints and signer
availability, plus manual path entry. It checks basic SSH directory/configuration
permissions early and offers separately confirmed, narrowly scoped tightening.
Completed permission repairs remain if setup is canceled later; aliases, registry
bindings, key generation and remote actions still wait for the main preview.

`key doctor` scans public-key companions and exact standard private-key names
under `~/.ssh`, without reading private contents. Repeat `--key` to restrict it to
selected paths plus canonical SSH setup paths, including a custom private key
without a `.pub`. A normal report never writes; `--fix` applies one reviewed plan
and verifies the result. Blocked or incomplete scans authorize no repairs. Setup
continues to check only its baseline paths and chosen key, not every unselected key.

Use selected fleet machines as SSH profile sources, or run their native SSH:

```bash
dev ssh discover --source fleet --host gateway --refresh
dev ssh list --fleet                         # cached profiles; no remote refresh
dev ssh setup internal-api --from fleet:gateway/api --config-only
dev ssh key list --on fleet:gateway --alias api
dev ssh connect api --on fleet:gateway       # keys stay on gateway
dev ssh key derive ~/.ssh/custom-key --apply
```

Fleet import previews a complete local ProxyJump route while retaining distinct
hop users, ports and keys. Remote profile identities and fingerprints are checked
before changes; remote identity/agent paths are never copied locally. The first
explicit capability may initialize the source user's dev UUID, but never sets a
fleet pin or merges machines. Source dry runs consume cached resolution only.

A successful controller-driven password login can offer Yes / No / Never (default
No). `--password-store system|bitwarden` on setup/connect selects the save provider.
Passwords stay in the OS credential store or Bitwarden; local TOML stores only
context, policy and references. Remote source-to-target passwords remain outside
this save flow. Private-key vault imports and hardware provisioning are deferred.

Optional registration uses two initially unchecked boxes: Fleet and Herdr. Select
either or both; Enter with neither selected skips registration, while Esc cancels.
Single-choice pickers use configured `fzf` when available; multi-select uses the
built-in picker (Space toggles, Ctrl+A selects/clears, Enter confirms).

ProxyJump routes are resolved outermost-first. Use repeatable
`--hop-os alias=posix|windows` when a hop's OS cannot be determined; already
working jumps are not modified unless `--install-on-working-jump` is explicit.
Windows standard accounts use their profile `authorized_keys`; administrator
group accounts require `--windows-admin-authorized-keys` before dev can target
the shared administrators file, and elevation can still require manual action.

`--fleet` is a separate, explicit durable decision. Key bootstrap writes a generated
`remotes.d/ssh-<alias>.toml` only after exact-key proof and a second fresh
ordinary alias login succeed. Existing authentication can register after a fresh
ordinary login without installing a key. A remote without `dev` still onboards successfully
and later appears as fleet `no-dev`. Failures after local setup retain valid
config/generated keys and report partial or unknown remote state so rerunning can
converge.

Removal is deliberately narrower than setup:

```bash
dev ssh remove lab --dry-run
dev ssh remove lab --yes
dev ssh remove lab --fleet --yes       # remove generated fleet fragment first
```

It removes only a structurally valid dev-owned host fragment and, with explicit
`--fleet`, its generated fleet fragment. It never removes the shared Include,
local keys, `known_hosts` entries, or remote `authorized_keys`; key rotation and
revocation remain manual. A primary user-authored `remotes.toml` reference blocks
removal until it is changed with `dev fleet config edit`.

| Data | Authority / owner | Durability |
|---|---|---|
| root SSH config and foreign Includes/`Host` blocks | user + OpenSSH | durable; setup is read-only; explicit format/organize can edit selected user files |
| `~/.ssh/dev.d/<alias>.conf` | `dev ssh setup/remove` | durable managed connection fragment |
| private/public key pair | user + native `ssh-keygen` | durable; generated assets are retained, never removed by `ssh remove` |
| primary `$XDG_CONFIG_HOME/dev/remotes.toml` | user via `dev fleet config init/edit` | durable; explicit manage rename/remove preserves unrelated bytes |
| sibling `remotes.d/ssh-<alias>.toml` | `dev ssh setup/remove --fleet` | durable generated fleet registration |
| remote paths/tasks/runtime | that remote machine's `dev` | host-local authority, never copied into controller config |
| `$XDG_CACHE_HOME/dev/fleet/` | controller cache | disposable snapshot |
| `paths.state_dir/machines/registry.db` | canonical machine registry | durable UUIDs and explicit provider bindings; separate from remote `machine_id` pins |
| `$XDG_CACHE_HOME/dev/ssh-discovery/` | Tailscale/LAN discovery | disposable observations with timestamps |

Use the SSH dashboard tab or the setup wizard to work with Tailscale peers,
existing aliases and LAN candidates:

```bash
dev ssh setup                        # select hosts, then configure each connection
dev ssh list --tailscale --lan        # live Tailscale status plus cached LAN observations
dev ssh discover --source tailscale
dev ssh discover --source lan --interface en0 --cidr 192.168.1.0/24
dev ssh setup lab --from tailscale:lab --user dev --config-only
dev ssh setup lab --from tailscale:lab --user dev --auth existing --to both
dev ssh machine show --json
```

Tailscale is optional. Dev calls `tailscale status --json` only for an explicit
Tailscale request and continues to use system `ssh` for connections. Regular
OpenSSH over the tailnet can use public keys; Tailscale SSH authenticates through
tailnet policy, so choose `--auth existing` for that server. An advertised
Tailscale SSH endpoint on port 22 refuses public-key installation. A separately
configured ordinary sshd port can use the explicit key bootstrap flow.

LAN discovery inspects only selected on-link IPv4 ranges and ports (22 by default),
with a 256-address/16-port bound and a 30-second deadline. Reverse-DNS names are
editable suggestions. `ssh list --lan` reads its five-minute cache and never scans.
Machine grouping preserves distinct SSH users, ports and keys. Canonical UUIDs
are created through setup or explicit `ssh machine adopt`; reviewed link/unlink/merge
changes only local associations. See [SSH onboarding](docs/guides/ssh-hosts.md#discovery-and-canonical-machines).

`dev ssh manage` joins SSH aliases, fleet names and Herdr 0.9.0 saved machines
in an explicit multi-select wizard. Already-working aliases can join fleet
without reinstalling keys. Profiles keep their own display names; Herdr actions
use native IDs and preserve remote sessions on remove/disable. Native add may
prepare/start a remote server and retains its own installation approvals.

`dev ssh format` previews four-space indentation; `dev ssh organize` optionally
moves complete Host blocks and their comments into group directories, preserving
explicit Include order. Both require `--apply`, retain private recovery receipts,
and support `dev ssh restore <receipt>`. New local edits use the macOS/Linux
backend. Existing foreign Includes and dormant files are not automatically
activated or migrated. `ssh list` remains static and its JSON/TSV stays compatible.

See [SSH host onboarding](docs/guides/ssh-hosts.md) and
[Remote repository fleet](docs/guides/remote-fleet.md) for full contracts.

## Create, clone, or set up a repository

Repository bootstrap works from any directory. Bare `dev repo new` opens an
interactive flow that chooses the destination under the configured
`project_root`, previews the setup, and confirms before writing anything:

```bash
dev repo new                                  # interactive new-repository flow
dev repo create api --preset agent-ready      # `create` is an alias for `new`
dev repo new owner/api                        # clear clone reference: preserve history/origin
dev repo new api --template owner/starters --template-subdir go/service
dev repo new api --check-in stage -m "chore: initialize api"
dev repo clone owner/api                      # owner/name or a Git URL
dev repo setup . --preset agent-ready         # add the same setup to an existing repo
```

`repo new NAME` keeps the small scripted default: `main`, README, and an
initial commit. When the argument is clearly a Git URL, local Git path, or
owner/name, however, `new`/`create` routes to the clone flow and preserves the
source history and configured remote; `repo clone` remains the explicit spelling.
Bare `dev repo clone` selects from the existing private forge cache without a
network refresh, while still offering manual URL/path/`owner/name` entry. When run outside a checkout, bare
`dev start` similarly selects a local repository with fast live discovery; inside
a repository it keeps the immediate current-repository default.
The external selector defaults to `fzf`; if it is missing (or `[picker] command = []`),
dev uses its built-in Bubble Tea picker. Multi-selection always uses the built-in
picker. Pipes remain line-oriented.
Clone can optionally apply a preset after the checkout exists, while `repo setup` repeat-safely merges native
initializers and preset files into a repository you already have. Custom hooks
and skill setup remain responsible for their own idempotency. Use `--dry-run`
to inspect the available plan without mutating the target repository.

`repo new` can also start from a content snapshot rather than an empty tree.
`--template` accepts a local directory/repository, Git URL, or owner/name;
`--template-ref` selects a branch, tag, or commit, and `--template-subdir`
selects a confined folder within the source. The snapshot becomes a fresh Git
history: source `.git` metadata is never copied, template files win scaffold
conflicts, regular-file modes are preserved, and traversal, symlinks, and
special files fail before the destination is created. A local Git working tree
without `--template-ref` includes existing tracked files plus untracked,
non-ignored files; Git-ignored files are omitted. A non-Git directory snapshots
its complete current tree. The same `template`, `template_ref`, and
`template_subdir` keys can live in a preset, which makes one starter catalog
repository with a preset per subfolder practical.

Human confirmation and dry-run plans preview the selected file paths (with a
bounded list) and warn when the source is a live local snapshot rather than a
commit. URL userinfo is removed from rendered summaries and errors. Filesystem
walking and writes are confined relative to held `os.Root` handles, and source
file content is read from the handle that was validated instead of reopening a
mutable path.

Bootstrap's check-in is explicit:

| `--check-in` | Result |
|---|---|
| `commit` | stage and commit generated files, then permit commit-dependent publishing/hooks |
| `stage` | `git add -A` for human review, but do not commit or run `after_commit` hooks |
| `none` | leave generated files unstaged and uncommitted |
| `auto` | for `repo new`, use the preset's compatible default; clone/setup otherwise perform no automatic check-in |

`--message` supplies the commit or staged draft message. In `stage` mode dev
best-effort writes the worktree-local `LAZYGIT_PENDING_COMMIT`; current lazygit
uses it to prefill lowercase `c`. An existing different draft is preserved with
a warning. This is a lazygit integration detail, not Git `commit.template`.
Staged setup cannot create an upstream or use the worktree-creating `start`
handoff; review and commit it first. The existing `repo setup --commit` remains
a compatibility spelling for `--check-in=commit`. If the lazygit draft cannot
be written, dev reports a warning but keeps the successfully staged index for
manual review and commit.

The built-in `agent-ready` preset adds an explicitly incomplete starter
`AGENTS.md`, a common `.gitignore` section, and project-scoped Claude plans. The
starter establishes safe repository-wide and handoff rules, but labels unknown
purpose, commands, architecture, and invariants as TODOs instead of inventing
project facts. Its common ignore block excludes only SpecStory's derived
`.specstory/statistics.json`; histories, project identity, and config remain
visible to Git.

If selected, the optional `agent-history-hygiene` and
`project-knowledge-harness` skills are installed and dev's reviewed built-in
initializers create the pre-commit/gitleaks and TODO/backlog/pitfalls surfaces
during bootstrap; setup does not wait for a later agent to happen to trigger the
skill. The history initializer also ensures `.specstory/.gitignore` contains
rules for SpecStory's machine-local `.project.json` and generated
`statistics.json`—not `.specstory/history/`, which remains part of the review
trail. Existing custom ignore content and file mode are preserved; only missing
managed rules are appended. Skills with the same source and identical agent
targets are sent to the installer together, while each skill's declared setup
still runs in its own phase. Presets may also declare typed inputs,
repository-contained file templates, and ordered
`before_commit`/`after_commit`/`after_remote` hooks.

When `gh` or `glab` is installed and authenticated, the wizard can create a
GitHub or GitLab upstream using the local repository name and description,
choose its namespace and visibility, add `origin`, and optionally push the
initial/current branch. Local-only is the default and published repos
default private. A forge or push failure leaves the local repository and any
already-created upstream intact, with recovery information instead of deleting
work.

TTY text fields in the repository, task-start, and finish wizards support
inline insertion plus Left/Right, Home/End, Delete/Backspace, and Esc/Ctrl-C
cancellation. Arrow keys are interpreted by the editor rather than appearing
as literal `^[[C`/`^[[D`; piped and buffered non-TTY input keeps its existing
line-oriented behavior.

The no-argument new-repository wizard places detailed file, template, input,
and skill questions behind a default-no “Customize preset and template
options?” gate. The normal `agent-ready` path therefore uses its reviewed
defaults; answer yes when you want to override individual choices.

The final handoff is explicit:

| Handoff | Result |
|---|---|
| `stay` | print the result and leave the shell where it is |
| `cd` | enter the repository through the trusted `shell-init` wrapper |
| `open` | open the configured Herdr/tmux/Zellij runtime; fall back to `cd` when runtime is `none` |
| `start` | continue into the existing `dev start` task wizard with this repository fixed |

Neither bootstrap nor a default `dev start` launches a coding agent. They
prepare the repository, checkout, and optional runtime surface. An explicit
worktree-mode `dev start --run '<shell command>'` can dispatch one command to a
new first-class Herdr root pane; it never chooses an agent profile or permission
mode on the user's behalf.

Global custom presets live in `$XDG_CONFIG_HOME/dev/scaffolds.toml`. A project
may commit `.dev-cli/config.toml` for allowlisted worktree/setup wizard defaults
and `.dev-cli/scaffolds.toml` for project presets, templates, hooks, and skill
setup. Project files cannot override host paths, runtime selection, forge
inventory, credentials, state, stats, update policy, or TUI policy. Legacy
`.dev.toml` worktree settings remain readable; the `.dev-cli` files win when
both are present. See [Commands and configuration](docs/reference/commands-config.md#repository-bootstrap)
for the schema, precedence, and executable-config trust boundary.

## The lifecycle

| State | Git | Runtime | Meaning |
|---|---|---|---|
| 🔥 `hot` | worktree + branch | session open | working on it now |
| 🌤 `warm` | worktree + branch kept | session normally closed | back within days |
| ❄️ `cold` | committed and pushed; worktree removed | nothing | paused, reconstructible anywhere |
| ✅ `done` | merged | may still be open | **MERGED**, waiting for external retirement |

READY/MERGED/RETIRED are derived milestones, not new persisted task states.
An agent prepares and exits; an external coordinator integrates and retires it.

```bash
dev start                                            # interactive managed-task wizard
dev start api --task "token refresh" --base main     # non-interactive fast path → hot
dev start api --task "token refresh" --base main \
  --run 'specstory run codex -c "codex"' --focus      # dispatch, then switch to the exact new pane
dev park --next "add the regression test" --wip      # → warm; self-runtime stays alive until exit
dev park --cold --push                               # → cold, only from outside the target runtime
dev resume "token refresh"                           # → hot, rebuilt if needed
dev flow api                                          # preview the repository lifecycle, plan first

dev prepare --session claude:<uuid> --plan .claude/plans/task.md
dev artifact finalize --intent <id> --writer-stopped # manual post-wrapper proof
dev done                                             # TTY finish wizard
dev done --ff                                        # → done/MERGED; runtime + worktree kept
dev retire "token refresh" --delete-branch           # external close/wait/remove → RETIRED

dev done --pr                                        # open review; keep task/worktree
dev done --merged --base-ref origin/main             # verify commit-preserving merge
dev sweep                                            # report drift and cleanup-pending work
dev sweep --merged-worktrees                         # from main: audit contained linked worktrees
dev sweep --merged-worktrees --apply                 # confirm each safe retirement
```

On a TTY, bare `dev done` reports branch ahead/behind and classifies every
staged, unstaged and untracked path against the base before offering
commit-all, discard-all or cancel. Unique discard requires typing `DROP`;
scripts use `--dirty=commit --message ...` or `--dirty=discard --yes`. After a
managed worktree reaches MERGED, the bare wizard also previews covering
runtime panes and agents, then offers keep, retire while keeping the branch, or
retire and delete the freshly contained branch. A caller-owned Herdr workspace
is closed only by a newly created external coordinator after a second fresh
retirement audit; active agents and mixed workspaces remain blockers.

Parent/canonical agent sessions and other tasks' tabs are preserved. A parent
agent blocks FF; the wizard offers recheck, PR, or cancel. It can select exact
idle/done task-worktree panes for closure under the final guarded plan, with no
closure before final approval. General foreground programs need an independent
confirmation that FF changes checkout files while they keep running. Interactive
`dev done` cleanup and `dev retire` list workspace/tab/pane IDs, program names,
PIDs and directories, and require `CLOSE <workspace-id>` before terminating
known non-agent programs. Background jobs are not inspected; unknown process
observations are not presented as idle shells. Parent and mixed workspaces stay
open. See [retirement scope](docs/guides/agent-safe-retirement.md#task-worktree-scope).
If local
fast-forward is blocked only by dirty canonical-checkout bytes, dev lists the
paths (marking agent artifacts) and offers PR handoff, exact stash+restore,
typed `DROP` discard, or cancel. Stash+restore preserves staged state and
untracked files, drops only its exact stash OID after a successful restore, and
retains that stash with a recovery command if restore conflicts. Dirty
submodules and nested repositories remain blockers. It never silently commits
unrelated canonical bytes, and active or unknown agents are not closeable from
this flow.

### A task does not have to mean a worktree

Choose the lightest mode that preserves the boundary you need:

```bash
# No task, no branch, no worktree: just open the canonical repo for ad-hoc work.
dev repo open api

# Track a quick change directly on the branch already checked out (usually main).
dev start api --task "fix typo" --direct

# Use a short-lived branch in the canonical checkout, but no linked worktree.
dev start api --task "small feature" --branch-only --base main

# Default: independent branch + worktree, provisioned and runtime-opened.
dev start api --task "token refresh" --base main
```

Direct work can be parked WARM and resumed, but cannot go COLD because the
canonical checkout cannot be removed. `dev done` on a clean direct task needs
no `--ff` or `--pr`: the work is already on its destination branch.

Start direct for one change stream, then create a normal worktree task later
when real parallelism appears. A new worktree starts from committed HEAD; dirty
main changes are deliberately not smuggled into it, so checkpoint first when
the parallel task depends on them.

### Guarded Git transactions

`dev git` wraps only operations that need durable receipts or failure recovery;
it does not replace ordinary Git aliases:

```bash
dev git uncommit              # soft reset; save old OID/message receipt
dev git recommit              # commit -C the receipt, then clear it
dev git pull-rebase           # exact stash OID + --index restore; never stash@{0}
dev git amend-all             # add -A + amend --no-edit, with normal hooks
dev git amend-all --exclude-agent-artifacts
dev git setup --print         # print optional aliases; never edit config
```

Published commit rewrites require `--rewrite-published`. `amend-all` includes
agent artifacts by default only when a project scanner is present (or the user
explicitly accepts `--allow-unscanned-artifacts`).

For automation, `dev start … --json` emits one pure creation object with absolute
paths and transient runtime facts. Only a newly created first-class Herdr
worktree with a non-empty exact `root_pane_id` is a launch target; reuse,
fallback, Tmux, none, or missing pane data fails closed. Worktree starts use the
same `repo/branch` label as `dev wt create` and pins the Git-derived parent
checkout with Herdr `--cwd`, preserving native nested repository/worktree
grouping without separate provenance metadata.

For a human one-liner, `--run '<shell command>'` uses that same exact-pane proof
and sends the command to the new worktree's interactive shell. It is incompatible
with `--json`, `--direct`, `--branch-only`, and non-Herdr runtimes. It only
confirms dispatch, never waits for command completion or reports its exit code;
add `--focus` when the caller should switch or attach after dispatch.

Herdr-aware writer claims—`start --direct`, `start --branch-only`, and
`resume`—reject another recognized agent in the same canonical Git worktree.
Every state, including `idle`, `done`, and `unknown`, is occupied. Pure
`repo open`, `wt open`, and TUI Enter reuse/focus the live owner without
claiming another writer. `--allow-shared-checkout` is only for explicitly
coordinated disjoint ownership; normal new-worktree creation remains allowed.

Task runtime handles carry their backend name and are validated against live
checkout coverage before reuse or close. Destructive cleanup additionally
resolves every covering pane. A caller inside the target, a mixed workspace, or
a working/blocked/waiting agent always stops retirement; unknown status needs an
external `--close-unknown`. `dev done` never closes or removes anything.

### Find forgotten local work

REPOS/TRY `Ctrl+O` opens triage for the current item, filtered results, or all
local work. Enter remains ordinary navigation. Multi-selection lives in triage:
grouped repo/Try checkboxes, mouse support, Ctrl+A all/none, and explicit
Select → Action → Preview → Results steps. Scoped inspection reuses metadata
and refreshes affected rows/SIZE only; no new persistent inventory cache is added.
Dashboard `1–7` switches existing views; `8` opens SSH unless a custom tool owns that key. Click column headers for ascending/descending/default
sorting; tools and state filters are in Ctrl+O, keeping the footer short.

`dev triage` finds uncommitted work, unpushed commits, missing upstreams, diverged
branches, ignored data and runtime/task drift across ordinary repos and Tries.
It includes branches that are not checked out and every registered worktree.

```bash
dev triage                         # grouped work; choose actions explicitly
dev triage --report                 # read-only text, also the non-TTY default
dev triage --json                   # schema-v1 report with completeness
dev triage --kind try --stale-days 30
```

Select an action and rows, press Enter to preview, then approve that exact batch.
Fetch is a separate round; push and fast-forward never auto-commit or rebase.
Checkout removal preserves branches and requires a typed `CLEAN N` confirmation.
Ignored files remain blockers unless you explicitly declare their exact directory
replaceable with `R`. Whole-repository eviction still requires future verified
backup/restore work. `L` marks intentional local work and `s` snoozes seven days;
new work brings either back. Local refresh does not contact remotes or reconcile
catalog records. See [local triage](docs/guides/local-triage.md) for keys, guards,
Try handoffs and partial-result receipts.

### Repository flow preview

`dev flow [repo]` is a preview-labelled, full-screen, TTY-only state-machine UI.
It is independent of the six-view `dev tui` dashboard. From a canonical or linked
checkout, `dev flow` resolves the canonical repository and focuses that exact
surface; outside Git it opens a filterable repository picker. An explicit
`dev flow api` overrides cwd.

The left panel is the union of Git's registered worktrees and task records that
have no checkout, including normal COLD and DONE tasks. Rows are labelled
`canonical`, `managed`, `unmanaged`, `harness`, `task-only`, or `conflict`.
Canonical worktrees are never removable; harness-owned and ambiguous/conflicting
rows have no destructive path. An exact unmanaged linked checkout can be
**Adopted** by creating task metadata without changing Git bytes, or **Removed**
only when clean and safe; removal is non-force and always preserves the branch.

The center distinguishes persisted task intent (`HOT`, `WARM`, `COLD`, `DONE`)
from observed Git/worktree/runtime/artifact facts. Unknown, failed, loading, or
stale evidence never becomes a false clean/closed fact. `runtime=none` leaves
session occupancy unobserved, but the local Git/task snapshot can still be fresh;
metadata-only adoption then remains WARM.

The right panel offers concrete mode/state actions: warm/cold park, resume,
direct or fast-forward completion, review handoff, verified-merge completion,
and DONE retirement where legal. The preview deliberately excludes dirty
commit/discard, WIP checkpoint, shared-writer, ownership-takeover, and unknown-
runtime overrides. A blocked plan shows its exact evidence, remediation, and CLI
fallback; existing command flags remain available outside the preview.

```text
j/k or ↑/↓       choose a surface      h/l or ←/→     choose an action
Tab/Shift-Tab    move panel focus       Enter          build a plan, never apply
r                reload local facts    R              Fetch refs / Query review / Both
Esc              back out              ?              evidence and key help
```

A READY plan shows ordered conditions/effects, retained resources, network and
destructive markers, and its exact PlanID. Press `y` only for a non-typed plan;
typed branch deletion requires the displayed token and `Enter`. Apply locks and
reloads the task revision plus repository, checkout, refs, runtime, artifact, and
remote identities. Any change rejects the stale plan before a new effect. Once an
effect starts, quit/refresh waits for the retained step ledger; partial success
means completed effects remain completed and recovery is explicit.

Startup and `r` never fetch or query a forge. `R` runs only the confirmed choice
and keeps its evidence in this TUI run: named ref OIDs plus portable review
existence, open/draft/merged/closed state, URL, provider, and observation time.
It does not query review decisions or checks. Completion records DONE while
keeping branch, checkout, and runtime resources; Retire cleans them from outside
the target. Raw Git and configured external tools remain outside these
`dev`-mediated locks and safety checks.

### The dashboard

Bare `dev` (or `dev tui`) opens eight lists, switched with `tab`:

- **TASKS** — the change streams dev is tracking. What am I working on.
- **REPOS** — durable repositories under the scan roots, with branch, dirty
  state, owned size, runtime, worktrees and task tally. What do I have here.
- **FLEET** — an expandable remote host tree with cached repositories and a
  separate HERDR registration/enabled column. Local is hidden by default; `a`
  reveals it at the end. Host actions provide SSH, dotfile status, and exact
  Herdr profile enable/disable/remove without waiting for repository loading.
- **TRY** — dated scratch experiments, including non-Git folders, with durable
  tags/notes and explicit active/deprecated/archived/graduated state.
- **REMOTE** — repositories visible through authenticated forge CLIs, including
  configured Azure DevOps projects, marked when a local repo or Try exists.
  What can I open or clone.
- **SKILLS** — native project/global agent-skill inventory for the exact startup
  checkout plus global sources, with local presence/integrity and an explicitly
  checked upstream freshness state. Outside Git it starts as a cross-repository
  inventory over accepted REPOS targets; `A` toggles either launch context to all
  accepted repositories for this TUI session.
- **MCP** — sanitized static MCP declarations for Claude Code, Codex, Cursor,
  Gemini CLI, and OpenCode, scoped to the same startup context by default and
  sharing the `A` context/all toggle. It resolves only Claude's documented
  project-approval settings; it does not claim connection health or a generally
  effective merged runtime configuration.
- **SSH** — canonical machines, exact SSH profiles, Tailscale/LAN observations and
  fleet/Herdr membership. `r` reads local/cached state; `c` starts explicit discovery,
  `n` opens setup, and `p` probes a selected alias.

The first view is constructed before runtime auto-detection, project-root lookup,
cache decoding, shell-based tool checks, or the optional release refresh can
finish. TASKS, REPOS, and TRY then publish independently from one shared local
load cycle; REMOTE, SKILLS, MCP, and SSH remain lazy. FLEET warms missing/expired
host snapshots after five seconds or an earlier visit, without blocking startup
or prompting for authentication. A cached REMOTE/FLEET snapshot
is immediately usable but is tracked separately from a current live result.
Every requested view has its own generation, so `r` cancels the old read, a late
result cannot replace a newer one, a failed refresh keeps usable rows visible,
and a successful empty result clears obsolete rows. Warning-only SKILLS/MCP
source diagnostics keep a fresh partial snapshot, and a visible dependent view
resumes automatically after a failed REPOS generation is repaired and reloaded.

TASKS, REPOS and TRY use the same services as their non-interactive commands.
Git-backed Tries appear in TRY rather than being duplicated in REPOS; REMOTE
still knows that local checkout exists. The repo list matters on day one: with
forty repositories and no tasks recorded yet, a task-only dashboard would just
be empty.

Navigation is vim-style, arrows alongside:

```
j k        move                 ctrl+d ctrl+u   half a page
g G        top / bottom         h l / tab       previous / next view
/          filter as you type   esc             clear, then quit
```

Press `?` or click **Help** in the footer to open the current view's **Keys**.
**Guide** explains that view's workflow, columns, colors and Git marks, including
a read-only snapshot of the selected row. **Manual** searches and reads all
embedded `dev help` topics and the shared workflow TL;DR without leaving the TUI.
Keys/Guide search stays within the selected help view unless you choose **All
views**; changing help scope leaves the dashboard selection and sorting alone.

Help and Ctrl+O share a floating popup, up to 104 columns × 32 rows; terminals
below 80 columns or 22 rows use the full screen. In Help, `1–3` selects the tab,
`v` selects its view, `/` searches, `j/k` chooses or scrolls, and `f` expands.
Click tabs, search, scope, Back, Expand/Close, article pan controls or the scrollbar;
the scrollbar also supports dragging. Esc stops editing, clears a query, returns,
then closes; `q` closes when not typing. Clicks outside close without activating
the underlying row. Help uses embedded text and existing observations; opening
it never starts another Git, forge, runtime, skill-provider or MCP probe.

Mouse cell tracking is enabled alongside the keyboard model. Left-click selects a
visible row or tab. Clicking the selected row opens its actions, including a row
selected by the keyboard or at startup; no double-click timing is required. The
wheel moves three rows, and right-click selects a row then opens the same actions.
Row clicks act on press; release/motion and modified row clicks are ignored. Opening a
menu does not execute its options. Terminals that reserve tracked mouse input may
require Shift/Option while selecting text.

The dashboard still starts on TASKS. On the first REPOS visit it selects the repo
containing the startup directory, keeping the configured ordering and scrolling
the row into view. Subdirectories and linked worktrees identify the same main
repository. Manual selection, filtering or sorting takes precedence over late loads.

When the startup Git repository is outside discovery, REPOS shows clickable
**Add this repo…** and **Scan parent directory…** entries, also available in
Ctrl+O. Review and confirm the actual config file, path and scope before saving.
Prefer `paths.repo_paths` for isolated repos and `paths.scan_roots` for a parent
that holds related projects. Additions preserve comments and existing entry order,
respect `--config`, avoid duplicates and retain defaults for omitted settings.
They reload local inventory and select the added repo. Automatic edits use the
macOS/Linux guarded file backend and retain private recovery under
`$XDG_DATA_HOME/dev/config-recovery`; symlink configs and unsupported TOML layouts
offer a manual edit. Scan failures remain unknown and do not trigger enrollment.

`enter` opens a selected local row only when its checkout is currently valid. Inside Herdr/tmux/Zellij it
asks the runtime to focus the target; outside it exits the dashboard and attaches to
the target session. A COLD worktree task requires `dev resume`; a missing or
unregistered worktree requires `dev sweep` first so artifacts can be salvaged
before the task is resumed or reaped. A wide TASKS table includes `REPO`; its
compact layout keeps repository/path in the selected detail pane. In TASKS, `p`
parks and prompts for the next action and `c` edits it. In REPOS, `enter` is pure ad-hoc open,
`space` expands linked worktrees inline, `m` edits repository tags/summary,
`s` starts an isolated worktree task, and `d` starts a tracked direct task.
In REPOS, `n` suspends into the same clone-aware `dev repo new` wizard used by
the CLI and returns with refreshed local inventory; it also works when the list
is empty. `a` quick-adds a repository thought and `N` opens its notes overlay.
TASKS retains `n` for quick notes. Expanded children carry their own
Git/session/task state and can be opened directly. In TRY, `n` creates or clones an experiment;
`Ctrl+O` opens mark/deprecate/archive/restore/graduate/Trash/delete actions; `a` includes
retained history. Inside Git, SKILLS and MCP use only the exact startup worktree
plus global/user sources; outside Git they reuse every accepted REPOS target and
the ordinary startup directory. Uppercase `A` switches both views between that
startup context and all accepted repositories for this TUI session, clearing old
scope rows before a generation-guarded reload. In SKILLS, `a` opens the upstream
interactive installer, `c` performs the opt-in read-only network check after the
local snapshot has loaded, and `u` confirms before updating only the selected
lock-managed skill in that row's checkout. Source checks hash Git object bytes
without running checkout filters; non-ASCII provider folder hashes that cannot be
reproduced portably remain unverifiable. Mutations skip repository-local PATH
shims and are serialized across `dev` processes. `r` reloads local state without
checking the network. MCP only filters/reloads static declarations; it never
starts a server or helper. On either capability view, `e` edits a private working copy, revalidates the
observed target immediately before atomic replacement, and preserves the working
copy when it detects a conflict. `y` offers file copy choices. `?` opens the complete
context-sensitive key map. That makes
the branch/worktree and lifecycle costs explicit rather than silently applying
them to every directory.


### Dashboard lifecycle actions

Press `Ctrl+O`, right-click a row, or click the selected row for actions.
Space expands/collapses REPOS worktrees and FLEET hosts; it is unused in flat lists. TASKS offers the existing finish,
resume, retirement and selected-task recovery workflows. `a` shows completed
tasks; it does not mark a task done. Missing checkout rows go through recovery.
The dashboard suspends for the shared CLI wizard and refreshes on return.

REPOS `s`/`d` open the full start wizard with worktree/direct preselected. The
last steps let you open the task (default) or stay in the dashboard. Browser
homepage actions leave the dashboard open. See the
[dashboard action guide](docs/guides/dashboard-actions.md) for Trash and recovery.

For a one-run startup/readiness trace, name an absolute file that does not exist:

```bash
DEV_TUI_TRACE=/tmp/dev-tui-trace.json dev
```

The file is created with private permissions after the alternate screen is
restored and before a selected runtime is activated. It contains only a bounded,
versioned list of relative microsecond timings, aggregate row counts, and
categorical view/generation/outcome fields; it never includes project names,
paths, commands, key values,
URLs, runtime handles, or raw errors, and it is not written to `stats.db` or sent
anywhere. `tui.initial_view_returned` means the Bubble Tea view string was built,
not that a terminal rasterized it. Cache acceptance, live snapshot acceptance,
and load completion are separate events; there is no artificial “all tabs ready”
event because REMOTE, FLEET, SKILLS, MCP, and SSH may never be visited.

REPOS also has an agent-handoff copy menu. Press `y`, then `y` for contextual
Markdown, `p` for the checkout path, `b` for the branch, `s` for runtime/agent
sessions, or `w` for every linked-worktree path. Parent `yy` includes the whole
repo; child `yy` includes only that worktree. SKILLS uses `yp` for the row's
primary `SKILL.md` (or lock-file) path, `ys` for a sanitized summary, `yu` for its
sanitized source URL, and `yf` for raw file contents. MCP uses `yp` for
`ConfigPath`, `ys` for the sanitized declaration, and `yf` for the whole raw
config file. Raw reads accept only a local regular file up to 1 MiB and perform no
network access, but the system clipboard may then contain credentials and other
servers stored in that file. The same full repository context is pipe-friendly
outside the TUI:

```bash
dev repo context api
dev repo context          # current repo, even from inside a linked worktree
dev browse --print        # current repository homepage, no browser launch
dev repo browse api       # open a selected repository homepage
dev repo context --json   # additive schema-v1 automation contract
dev repo context --refresh  # live forge + configured fleet probes
```

Local checkout, Git, task, worktree, and runtime facts are always collected live
without network access. External forge/fleet facts come from private caches by
default and retain their source, age, freshness, completeness, and errors; only
`--refresh` performs network probes. Readiness stays split by checkout, task, and
worktree scope, and unknown evidence is never rendered as clean.

**External tools are configured, not fixed.** They run through your shell in
the selected row's checkout; the dashboard suspends and redraws when they exit:

```toml
[[tui.tools]]
key  = "L"
name = "lazygit"
run  = "lazygit"

[[tui.tools]]
key  = "V"
name = "nvim"
run  = "nvim ."

[[tui.tools]]
key  = "B"
name = "vibe"
run  = "vibe"
interactive = true     # load + evaluate aliases/functions after shell rc

[[tui.tools]]
key  = "P"
name = "plans here"
run  = "claude-plans-here"
interactive = true
```

`dev config init` writes the defaults out in full rather than leaving them
implicit, and `dev tui tools` shows what is bound here and whether each one is
actually installed. The dashboard checks bindings in a bounded background load
after its first view; unresolved or missing programs are not offered, and
rendering never launches a login shell. A tool cannot take a globally owned key;
dev reports the clash on load. `A` remains configurable for compatibility but the
SKILLS/MCP scope toggle takes precedence on those two views.

REMOTE loads lazily, so dashboard startup never waits on the network. Its
private XDG cache is decoded after the first view and contains the complete
paginated inventory. Fresh rows are reused without network access; stale rows
remain searchable while a background refresh runs. Cache payload size and
identity fields are validated before display, and a source fingerprint binds
the cache to configured GH/GL hosts and Azure targets so another endpoint is
never seeded automatically. GitLab inventory passes `GITLAB_HOST` (or
`GLAB_HOST`, default `gitlab.com`) explicitly, so the current repository cannot
silently select another authenticated host. A successful empty provider inventory replaces old
rows instead of resurrecting them later. `r` forces a
refresh of all configured forge providers.
`/` searches provider, owner/name, visibility and description; `vis:private`
is an exact visibility filter. Enter opens an existing local clone. For an
absent repo, `c` opens a confirmation where `enter` clones and stays in the
dashboard while `o` clones and opens it. REMOTE rejects a `project_root` outside
the configured REPOS discovery depth/roots before mutation. The row and status
spinner remain pending through Git clone and the generation-guarded local REPOS
refresh; `q`/Ctrl-C requests cancellation without abandoning the in-flight
result, and the refresh does not contact the forge. Once accepted, REMOTE marks
the checkout as `repo` and REPOS can search it immediately. If Git fails after creating the
destination, REMOTE labels the exact path `inspect` and requires inspection or
a move before retrying; it never deletes that directory automatically. The same
inventory is available without the full-screen UI via
`dev repo remote [query]`; `--cached` is its instant/offline form. That JSON
contract also powers the optional Television channel and fzf shell helper under
[`contrib/`](contrib/README.md): run `dev repo remote --refresh` once, then
compose a selected exact clone URL with `dev repo clone` without teaching
another command about forge freshness.

SKILLS also loads lazily. Inside Git it describes the exact startup checkout;
outside Git it describes the accepted repository inventory. `A` can inspect all
accepted repositories without changing the next TUI run's default. Native reads
use the versioned `skills@1.5.23` 77-agent path registry and lock files; they never
start Node, `skills`, or `npx`. Use `dev skill list --all` for canonical
repositories, `--repo` for one checkout, and `--check` only when a remote freshness
check is wanted; checks hash Git object bytes without checkout filters. MCP is
separately available through `dev mcp list`; it reads five agents' static config
formats, resolves only Claude's documented project approvals, and redacts
secret-bearing values before producing rows or JSON. TUI `e` and raw `yf` operate
on the local source file after inventory; they do not weaken the list/JSON
redaction contract.

Azure DevOps Services inventory is opt-in because `az repos list` requires an
organization and team project. Repeat the target for every project wanted:

```toml
[[forge.azure_devops]]
organization = "https://dev.azure.com/acme"
project = "Platform"
```

Install Azure CLI's `azure-devops` extension and authenticate with Azure CLI.
`dev` does not install extensions, change Azure defaults or store credentials.
Azure DevOps Server/on-premises is not supported by the official extension.
REPOS has an explicit LIVE column (`herdr:working`, `herdr:idle`, …). Multiple
repo sessions collapse to `herdr:N live`; expanding the repo reveals each
checkout and whether its session is live or closed. External and turn-scoped
agent worktrees remain visible with `(external)` / `(ephemeral)` labels, so the
tree always reconciles with WT. `H` opens the selected repo's
calendar-year activity heatmaps and `e` edits config. Returning from the editor, or
pressing `r`, reparses config and reloads data/tool bindings without restarting
the TUI; a runtime-backend change is reported as requiring restart.

REPOS includes `LATEST`, defined as the newest dirty-file mtime, commit time, or
task update, plus asynchronous `SIZE`. Size is portable logical bytes:

- `checkout_bytes` excludes only the checkout root's `.git` entry.
- `private_git_bytes` belongs only to this checkout and is included in
  `owned_bytes`, the table value.
- `shared_git_bytes` is shown separately and never charged to every linked
  worktree. `+S` means shared Git storage exists but is not reclaimable with
  that row.

Measurements stream in after the first frame, use a 10-minute private XDG cache,
and can be forced with `r`; unreadable subtrees display a lower bound (`≥`).
Columns and default ordering are config:

```toml
[tui.repos]
columns = ["repo", "branch", "git", "size", "live", "latest", "worktrees", "tasks"]
sort = "activity"       # activity | latest | name | git | size | tasks
reverse = false
```

In REPOS and TRY, `O` cycles sort and `R` reverses it. Structured filters include
`tag:important`, `remote:none`, `size:>1GiB`, `phase:deprecated` and
`where:archived` where applicable. Local repo probes run with bounded
parallelism, and the alternate screen appears before they finish.

Use `?` for contextual Keys/Guide/Manual, or `dev help tui` outside the dashboard.

### Experiments and local-data risk

`dev try <name>` keeps its low-friction positional grammar. Lifecycle management
uses the separate plural group, so `dev try archive` still means "open/create a
Try named archive":

```bash
dev tries list --json                 # active, present Tries
dev tries list --all --sizes          # include deprecated/archived history
dev tries mark redis --add important --note "compare streams"
dev tries deprecate redis             # intent only; files do not move
dev tries archive redis               # reversible move under tries_root/.dev
dev tries restore redis
dev tries graduate redis -c Infra     # same service as dev graduate
```

Archive is organization, **not disk reclamation**: it moves the directory to a
hidden location on the same filesystem and preserves its stable catalog ID.
Triage Try multi-selection supports reviewed whole-directory Trash, including ignored
and untracked files; an uncataloged Try is registered only during approved Apply.
Confirmed missing accidental Tries offer `forget-try`, also available as
`dev tries forget <ref> --dry-run` and exact-ID `--confirm-forget <id>` approval.
Other host locations, tasks, runtime/agent claims, notes/tags and recovery
references block forgetting. It deletes catalog metadata only.

`dev tries delete <ref>` moves a cataloged Try to system Trash after a preview.
Trash is also retained storage until emptied. Permanent disposal requires
`--permanent` and typing `DELETE <catalog-id>` (or `--confirm-delete <id>` for
scripts); `--yes` alone never authorizes it. Task/runtime claims, linked/shared
Git, nested repositories, cwd and unsafe paths block removal. Unobserved runtime
coverage needs a separate `--assume-no-runtime` acknowledgement.

The catalog ID and host-local removal history survive. After restoring the
original folder in the system Trash UI, run
`dev tries restore <ref> --from <restored-path>` before editing it. Interrupted
operations retain their record and never automatically retry. Verified remote
backup and safe automatic reclamation remain future work.

The catalog also exposes personal repository tags/notes (`dev repo mark`). To
find local Git state at risk before any future cleanup:

```bash
dev repo list --no-remote              # no configured remotes at all
dev repo list --local-only             # at least one branch lacks remote upstream
dev repo list --multiple-remotes
dev repo list --multiple-upstreams     # branches track more than one remote
dev repo list --sizes --json           # full remotes/branches/size contract
```

`no remote`, `local-only branch`, and `multiple upstreams` are distinct facts.
They do not claim whether commit objects are present remotely; a future reclaim
preflight must compare every local head/tag/note/stash against actual remote
refs immediately before removal.

Going cold is safe because **the branch is the identity and the directory is a
cache**. `dev park --cold` refuses unless the branch is pushed, and `dev resume`
rebuilds the checkout from `origin/<branch>`. Once that holds, the local
filesystem stops being a graveyard of half-finished worktrees.

## Rich Git state

Every inventory surface uses the same compact, starship-like status:

```text
⇕⇡3⇣2 =1 +4 !2 ?3
```

- `⇡` / `⇣` / `⇕` — ahead, behind, diverged
- `=` — conflicted paths
- `+` — staged paths
- `!` — unstaged paths
- `?` — untracked paths

`dev status` and the TUI detail pane also show the unique changed-path total
and type breakdown (added / modified / deleted / renamed). A path staged and
then modified again is one changed path, while correctly appearing in both the
staged and unstaged categories. JSON output exposes all counts separately.
`dev status` also renders independent local `checkout`, `task`, and `worktree`
readiness outcomes. It never contacts a forge/fleet host, and unavailable task,
runtime, or worktree evidence remains indeterminate instead of looking clean.

## Worktree ownership

Three mechanisms create git worktrees. `dev` takes one position so nobody has
to improvise:

| Kind | Owner | Where | Lifetime |
|---|---|---|---|
| Feature, fix, experiment, cross-machine handoff | **`dev`** | `~/Worktrees/<repo>/<slug>` | until external `dev retire` |
| Harness-owned turn-scoped subagent isolation | **Claude Code** | `.claude/worktrees/` (gitignored) | owned by that harness; no history-relocation guarantee |
| `herdr worktree create` | **not used** — `dev` runs `git worktree add`, then `herdr worktree open --path …` | — | — |

**If code, history, or plans must remain reviewable—or you may return tomorrow—
use `dev`.**

Claude Workflow cleanup is an explicit, stricter exception to the normal
harness-owned lifetime. From the canonical non-bare checkout, first review:

```bash
dev sweep --ephemeral-worktrees --stale-days 14
dev sweep --ephemeral-worktrees --json        # schema version 1; report only
```

The V1 adapter reads only bounded fixed-depth structure under
`~/.claude/projects`; it reports normalized IDs/states/times and never emits
prompts, scripts, logs, result bodies, or transcript content. A candidate needs
one exact provider mapping, a `completed` or `killed` workflow, matching `done`
agent plus journal `started` and `result`, no same-ID resumed transcript, and
provider inactivity older than the threshold. Killed/no-result, progress, or
resumed evidence remains `unknown` with no attestation bypass.

There is one additional non-replay requirement: provider metadata must bind the
run to the live branch, HEAD, common-dir, and an opaque registration generation
that cannot be reused with the path. Claude Code 2.1.259 records none of that Git
identity. Its current claims therefore show `provider-git-identity: unknown` and
remain report-only; even `--apply` will not prompt for or remove them. This is
intentional—stale terminal metadata must not authorize deleting a later checkout
that reused the same path. Do not infer identity from the path, branch naming, or
GitDir pathname.

Eligibility also requires a present registered unlocked non-prunable named
linked worktree with matching common-dir/branch/HEAD, no staged, unstaged,
conflicted, untracked, ignored, or recursive-submodule content, no Git operation,
no task or unsafe artifact intent, a caller outside the target, and known empty
runtime coverage. Missing, prunable, unregistered, and orphan paths are report
only. Apply is interactive and confirms each item, then reacquires every proof
under a common-dir cleanup lock and compares a stable fingerprint before plain
non-force removal:

```bash
dev sweep --ephemeral-worktrees --apply
# Optional only when separately approved and proved safe:
dev sweep --ephemeral-worktrees --apply --delete-branches --base main
```

A clean worktree may have commits unique to its branch because the branch is
retained by default. Branch deletion separately requires unchanged branch/base
tips, containment, zero unique commits, and `git branch -d`. This flow never
prunes registrations, closes runtimes, deletes Claude metadata, or rescues,
stashes, commits, or force-removes dirty work.

`dev` does not delegate placement to herdr because the path policy has to hold
on machines without herdr. It creates the checkout with plain git and asks
herdr only to *open* it — which still surfaces it in the sidebar grouped under
its parent repo with its own branch and ahead/behind row.

`dev` refuses to create a worktree inside a repository: a checkout nested in
another checkout makes every indexer, file watcher and `rg` run see a second
copy of the tree.

### Provisioning

A worktree is a clean checkout — no `node_modules`, no `.venv`, no `.env`.
Without provisioning, every new one starts broken. `dev` copies the gitignored
files you list, optionally symlinks heavy directories, and runs a setup
command detected from the lockfiles. Included files must remain the same
regular file through open; source swaps and symlinked destination parents are
refused, existing destinations are reported as skipped, and contents are never
logged:

```toml
[worktree]
include     = [".env", ".env.local"]   # only files that are ALSO gitignored
link        = []                       # opt-in; sharing node_modules is risky
post_create = "auto"                   # uv.lock → uv sync, package-lock.json → npm ci, …

# Separate opt-in export policy for dev fleet files; worktree.include is never
# inherited for off-machine transfer.
[local_files]
include = [".env", ".mcp/**"]
```

`[local_files]` may be committed in `.dev-cli/config.toml`, but it only proposes
portable patterns. Nothing leaves the host until an explicit `dev fleet files`
invocation selects one target; every pattern expands locally to sorted exact
paths before the protocol begins.

`.claude/settings.local.json` is not a universal include. Add that exact path
only for an explicitly selected sticky/plain-Claude launcher and verify it
arrives. `claude-copilot-once` preserves an existing Copilot pin and creates/
removes only one it added when absent; its proxy must already run.
`codex-copilot-once` injects backend via CLI and may auto-start its proxy path,
so neither wrapper needs the copied file.

**Copy or reinstall?** Reinstalling is always correct but can take minutes;
copying is fast but only sound for dependency trees carrying no absolute paths.
That is a per-ecosystem fact, not a preference, so dev knows it:

```toml
[worktree]
strategy = "reinstall"      # reinstall | copy | link | skip

[worktree.strategies]
node = "copy"               # node_modules copies soundly
```

Asking to copy a virtualenv is refused with the reason — it bakes its own
absolute path into `pyvenv.cfg` and `bin/activate` — and narrowed back to
reinstalling rather than silently producing a broken checkout.

`dev wt plan` shows exactly what a new worktree of a repository would get:
which project types were detected, which tools are missing, and every file and
command involved. `dev wt plan --write` seeds
`<repo>/.dev-cli/config.toml` from it, so
a project can commit its own setup and every machine provisions the same way.

```
$ dev wt plan
PROJECT  MANAGER  FROM               DEPENDENCIES  TOOL
node     npm      package-lock.json  node_modules  installed
python   uv       uv.lock            .venv         installed

   ACTION    WHAT          WHY
✓  copy      .env          gitignored, so the checkout would not have it
✓  copy-dir  node_modules  npm dependencies, copied instead of reinstalling
✓  run       uv sync       uv.lock detected
```

## Other things it does

```bash
dev repo list --sizes          # repos, remote topology and owned logical size
dev repo list --no-remote      # find local Git with no configured backup remote
dev repo context api           # agent-ready paths, Git, WT, runtime and tasks
dev flow api                   # TTY-only guarded repository lifecycle preview
dev repo new                   # interactive local/published repository bootstrap
dev repo setup . --preset agent-ready   # safely initialize an existing repo
dev repo clone owner/name -c Web   # expand forge shorthand, then clone with Git
dev repo clone https://dev.azure.com/acme/Platform/_git/api -c Work
dev repo sync --all            # fetch + prune, and report what moved
dev ssh list                   # static OpenSSH aliases and source ownership
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh probe lab              # fresh ordinary BatchMode login
dev fleet list                 # Git/task/runtime state from every configured machine
dev fleet machine-id lab       # inspect/compare the target's stable machine pin
dev fleet sync api --push      # push, then safely fast-forward clean remote checkouts
dev fleet files api --to lab   # report-only plan for explicit ignored local files

dev try redis-streams          # dated scratch directory for an experiment
dev tries archive redis-streams    # reversible local archive; does not delete
dev graduate redis-streams -c Infra --remote   # promote it into a real project

dev pr list                    # requests you opened or were asked to review
dev pr list --scope local      # with head branches, joined to your worktrees
dev pr list --actions          # the gh/glab commands; dev prints, never runs
dev prompt list                # built-in read-only context recipes
dev prompt agents --json       # sorted profile capabilities, no private argv/shell
dev prompt render pr-triage    # inspect/copy the exact prompt
dev prompt run session-close --agent my-agent   # bounded one-shot, no user stdin
dev prompt open workspace-closeout . --agent my-agent  # foreground current TTY

dev gitignore                  # .gitignore from GitHub's templates + the rest
dev adopt                      # import existing worktrees/sessions as tasks

dev stats --heatmap            # where the time actually went
dev summary                    # current machine-wide project snapshot
dev journal                    # today's commits plus current task/WIP context
dev help worktrees             # quick-reference pages for the workflow
dev help wt                    # same page, reached by command name
```

### Prompt handoffs

Escalate only as far as the situation needs: use `dev status`, `dev sweep`, or a
lifecycle command when deterministic facts already answer the question; use
`dev prompt render <recipe>` to inspect or copy context; use `run` for a bounded
batch answer; and use `open` only when a foreground conversation can resolve a
semantic question.

The built-in recipes are `pr-triage`, `session-close`, and
`workspace-closeout`. They collect read-only deterministic context and instruct
the receiver not to mutate it. `dev` never parses an agent reply, starts a loop,
or treats the reply as permission for `done`, `park`, `sweep`, or `retire`.
There is no built-in vendor or launcher: host config defines optionally
described profiles with independent `[agent.run]` and `[agent.open]` commands.
`dev prompt agents [--json]` lists their sorted capabilities while hiding argv,
shell source, executable directories, prompt text, environment, and config path.
Global explicit/default/sole selection happens before recipe collection; the
selected profile must support the requested mode and never falls back to another
profile. `--agent` completion is mode-specific. `run` receives finite prompt
input and defaults to a 10-minute timeout; `open` checks for a TTY before
collection, reserves stdin for the conversation, has no default timeout, and
stays in the current terminal/pane. It does not create, focus, or reuse a
Herdr/tmux/Zellij surface. See [Prompt handoffs](docs/guides/prompt-handoffs.md)
for configuration, transport, permission, closeout-audit, and rebase-conflict
boundaries.

### Repository quick notes

Quick thoughts live beside repos in dev state, not inside each checkout:

```bash
dev note add "try event subscription" --repo api --tag idea
dev note list api
dev note search "event subscription"
dev note edit <id-or-prefix>
dev note delete <id-or-prefix>
```

An ID prefix must be unique and at least eight characters.

In TASKS, `n` quick-adds a note. In REPOS, `a` quick-adds while `n` opens the
new-repository wizard. `N` opens a browse/search/edit/delete overlay in both.
A REMOTE row supports notes only after it has a local clone; TRY keeps `n` for
creating experiments. Add `notes` to `[tui.repos].columns` for the count;
repository detail shows count/latest preview, and task detail does so when the
task resolves to a loaded repository row.

Durable Markdown lives under configured
`paths.state_dir/notes/<catalog-id>/`, defaulting to
`$XDG_DATA_HOME/dev/notes/<catalog-id>/`. The catalog ID survives path moves,
symlink indexes and linked worktrees. `dev` does not synchronize notes or
catalog state; synchronize both when attachments must travel between hosts.
The disposable `$XDG_CACHE_HOME/dev/notes.db` is only an FTS index:

```bash
dev cache clear notes                    # Markdown remains
dev note search "event"                 # index rebuilds automatically
dev note reindex                         # explicit rebuild
```

`repo mark --note` remains a single catalog summary and is not overwritten.
`dev repo list --json` always includes `notes.count` and adds
`latest_id`/`latest_preview`/`latest_updated` when a latest note exists;
`dev note list/search/show --json` expose complete note records. Structured task
systems such as td/beads remain optional future adapters rather than creating
dot-folders automatically.

### Machine-wide summary

`dev summary` is the quick context dump for the whole machine. It combines
repositories, present Tries, worktrees, tasks, runtime sessions, recovery risk,
latest activity and one recent commit into Markdown suitable for a person or an
agent. Active work is expanded; quiet projects remain in a compact index:

```bash
dev summary
dev summary --attention
dev summary --detail compact --no-runtime
dev summary --recent-commits 3 --sizes
dev summary --json | jq '.projects[] | select(.active)'
dev summary | opencode run "give me a quick view of this machine"
```

Use `dev journal` when the question has a date range, and `dev repo context`
when one repository needs every checkout/task/session detail.

### Development journal

`dev journal` emits Markdown designed for a daily/weekly report or direct input
to another tool. It does not invoke an AI agent or persist the generated prose:

```bash
dev journal
dev journal --since 7d --metrics
dev journal --since 3mo --granularity branch
dev journal --author teammate@example.com --since 30d --json
dev journal --since 7d | opencode run "summarize this development journal"
```

The default `auto` view expands commits, keeping complete repo/branch totals
while limiting details to the newest 100 commits. Pass `--max-commits 0` for an
unabridged report. The current user's report can also include source-separated
session/WakaTime evidence, task intent and dirty linked worktrees whose latest
file mtime falls inside the requested calendar-day range.

`dev stats` draws a contribution-style heatmap from two sources: a sampler
watching live agent sessions (the only way to count time spent reading and
debugging, including sessions in external linked worktrees), and git history (which backfills the past and survives losing the
database). WakaTime can be imported alongside for editor time.

```bash
dev stats backfill                          # seed all repos from git history
dev stats backfill --repo api               # seed only one repo
dev stats sample --interval 5m              # from cron, every five minutes
dev stats import-wakatime                   # optional
dev stats path                              # durable SQLite location
dev stats clear --repo api                  # guarded selective deletion
```

In the TUI, `H` displays stored activity immediately and automatically backfills
all available local Git history for that repository. Unchanged refs reuse a
checkpoint. Calendar years run oldest first; use the wheel, arrows, PgUp/PgDn or
Home/End to scroll. `r` refreshes and `b` forces backfill. Git activity remains an
estimate of 20 minutes per non-merge commit; no remote fetch runs.

Stats are **data**, not cache: session samples and WakaTime imports may not be
reconstructible. They live at `$XDG_DATA_HOME/dev/stats.db` and clearing them
requires a scope plus confirmation (`--repo`, `--source`, or `--all`).
Regenerable data lives separately:

```bash
dev cache list
dev cache path
dev cache clear remote
dev cache clear notes          # FTS only; Markdown remains
dev cache clear fleet
dev cache clear ssh-discovery        # preserves canonical machines and manual bindings
dev cache clear repos
dev cache clear skills
dev cache clear size
dev cache clear gitignore
dev cache clear licenses
dev cache clear all
```

Those remove only regenerable files under `$XDG_CACHE_HOME/dev/` (remote
inventory, note FTS, size measurements and gitignore templates) and never touch
`stats.db`, durable note Markdown or project data.

## Bootstrapping an existing machine

There is nothing to migrate for ordinary use: `dev` discovers repositories
below `scan_roots` and at exact `repo_paths`, and never requires a particular physical layout.
When you want a recursive audit or a curated navigation layer, bootstrap is the
explicit path:

```bash
dev bootstrap ~/code /mnt/work                    # recursive report, no changes
dev bootstrap ~/code --json                       # machine-readable inventory
dev bootstrap ~/code --index ~/Projects           # plan a flat symlink catalog
dev bootstrap ~/code --index ~/Projects --apply   # create only the ready links
dev bootstrap ~/old --move ~/Projects             # plan physical moves
```

The scanner classifies canonical checkouts, linked worktrees, bare repositories
and symlink aliases, deduplicating by Git common-directory identity rather than
path.

**A symlink index is the default recommendation.** It gives a flat,
metadata-aware navigation root while every physical repository stays exactly
where it was. Put the index first in `scan_roots` and that alias becomes the UI;
normal discovery follows direct repo symlinks and deduplicates index + physical
paths.

Physical move exists, but refuses dirty repos, linked worktrees anywhere in the
clone, live sessions, a current shell inside, aliases that would break,
occupied destinations and cross-filesystem renames. A plan with any blocked row
moves nothing. `--config-out` writes a fresh config for the resulting root
without silently rewriting the user's current one. See `dev help bootstrap`.

## Adopting work in flight

There is nothing to migrate. `dev` discovers repositories wherever your scan
roots point and **never moves, renames or deletes anything** you already have.
`dev config init` probes the conventional locations — `~/Documents/Program`,
`~/src`, `~/code`, a `GHQ_ROOT`, and so on — counts the repositories in each,
and writes only the ones that exist:

```
$ dev config init
wrote ~/.config/dev/config.toml

Detected:
ROOT                 REPOS  ROLE
~/Documents/Program  40     scan root, new projects land here
~/src                16     scan root
```

Repositories are discovered; *tasks* are not. `dev adopt` finds the work
already in flight — linked worktrees from any tool, live runtime sessions, and
local branches ahead of the default branch — and offers to record it. It skips
branches already merged, and the turn-scoped worktrees an agent harness cleans
up itself:

```bash
dev adopt            # report only
dev adopt --apply    # record as tasks; nothing on disk changes
```

A worktree you already have somewhere else keeps working exactly as it did —
`dev` records the path git reports rather than relocating it. Full detail in
`dev help adopting`.

## .gitignore

```bash
dev gitignore                 # detect the languages from the repo's files
dev gitignore python node     # or say so explicitly
dev gitignore --offline       # cached and bundled templates only
```

Language sections come from [GitHub's templates](https://github.com/github/gitignore),
fetched once and cached. On top of those it adds what no language template
covers: the host platform's junk files, editor state, local env files, and
ephemeral coding-agent state such as linked worktrees and local settings.
Review artifacts such as SpecStory histories and agent plans remain trackable
unless you add your own ignore rule for them.

Everything it writes goes inside a delimited block, so re-running updates that
block and leaves rules you added by hand alone.

## Configuration

`$XDG_CONFIG_HOME/dev/config.toml`. Write a commented starter with
`dev config init`; see the effective settings with `dev config show`; open the
actual file in `$VISUAL` / `$EDITOR` with either form:

```bash
dev edit
dev config edit
DEV_EDITOR=unused dev edit --editor "code --wait"   # explicit override
```

If the file does not exist, `edit` generates the machine-detected starter first
rather than opening an empty file. Resolution is `--editor` → `$VISUAL` →
`$EDITOR` → `nvim` → `vim` → `vi`.

Every path is configurable, because the right answer depends on the machine —
a faster volume, a different naming convention:

```toml
[paths]
scan_roots    = ["~/Documents/Program", "~/src/tries"]
repo_paths    = ["~/.local/share/chezmoi"] # exact repos outside useful roots
worktree_root = "/mnt/fast/worktrees"
worktree_path = "{{worktree_root}}/{{repo|lower}}/{{branch|slug}}"
state_dir     = "~/.local/share/dev"        # point at a git repo to sync it
```

`state_dir/tasks/*.toml` stores task intent; `state_dir/assets/*.toml` stores
stable repository/Try identity, tags, one metadata summary, experiment
lifecycle and per-host locations; `state_dir/notes/<catalog-id>/*.md` stores
multiple repository quick notes; `state_dir/artifact-intents/v1/*.json` stores
versioned post-writer handoffs and commit receipts. Git status, logical byte
counts and `.specstory/statistics.json` are derived rather than durable review
history. Size cache lives in `$XDG_CACHE_HOME/dev/sizes-v1.json` and is safe to
delete.

Template variables: `worktree_root`, `repo`, `repo_path`, `branch`, `category`,
`host`, `date`. Filters: `|slug` (`feat/auth/x` → `feat-auth-x`), `|lower`,
`|base`. A typo in a variable name fails at load, not as a directory literally
named `{{rep}}`.

A flat project layout is already first-class — it needs no migration and no
special mode:

```toml
[paths]
scan_roots   = ["~/code"]
project_root = "~/code"       # repo new/clone/graduate land at ~/code/<repo>
worktree_root = "/mnt/fast/wt"
worktree_path = "{{worktree_root}}/{{repo|lower}}--{{branch|slug}}"
```

Omit `--category` and canonical repos stay flat. Categories are metadata the
user may choose, never a directory structure dev imposes.

## Dotfiles

`dev dotfile` locates chezmoi configuration and guides setup without requiring a
particular dotfiles repository. Existing sources are retained. If you have no
repository in mind, the optional author-maintained `david` preset recommends
the standalone Unix, Windows, Termux, iSH or OpenWrt repository for this machine.

```bash
dev dotfile status --json           # passive paths/revision; no hooks or fetch
dev dotfile setup                   # keep existing source or review a new one
dev dotfile setup --preset david    # optional platform-specific recommendation
dev dotfile setup --repo https://github.com/you/dotfiles.git
dev dotfile diff
dev dotfile apply                   # current source; may execute repo scripts
dev dotfile update                  # native update and deployment
dev fleet dotfile status --host lab
```

Without chezmoi, setup gives native installation/bootstrap guidance. Outside a
terminal, `--yes` executes initialization and `--apply` separately requests
deployment; native prompts remain native. Static status does not prove that
the source revision has been applied. Diff/apply/update delegate to chezmoi and
can execute its hooks. Dev does not synchronize HOME, migrate the author's
legacy fleet inventory, or bundle appsrc/dotcfg. See [Dotfiles](docs/guides/dotfiles.md).

## Multiple machines

Do not sync worktrees or runtime state. Sync branches, and let the remote be
the handoff boundary:

```bash
dev park --cold --push      # on the machine holding the work
dev resume <task> --fetch   # on the machine picking it up
```

`dev` records an owner host per task and refuses to resume someone else's
without `--force`. Two machines committing to one branch is the reliable way
to produce a conflict here; the ownership check prevents it.

Configure fleet profiles separately from each machine's host-local paths. The
primary file remains user-authored:

```toml
# $XDG_CONFIG_HOME/dev/remotes.toml
schema_version = 1

[defaults]
connect_timeout = "15s"
command_timeout = "5m"
cache_ttl = "15m"
max_parallel = 4
dev_path = "auto"

[[hosts]]
name = "lab"
ssh_alias = "lab"
remote_os = "posix"       # omitted still means posix
# Add only after `dev fleet machine-id lab` and independent verification:
machine_id = "00000000-0000-4000-8000-000000000000"
```

An explicit `dev ssh setup winlab --key ~/.ssh/id_ed25519 --target-os windows --fleet`
instead generates the strict sibling
`$XDG_CONFIG_HOME/dev/remotes.d/ssh-winlab.toml`; it never rewrites the primary
file. A custom `--remotes /srv/dev/lab.toml` uses `/srv/dev/lab.d`, while a path
without `.toml` appends `.d`. Fleet loads the primary first, then validated
managed fragments in lexical order, and applies defaults after the merge.
Duplicate names always fail. Existing primary profiles may share an SSH alias,
but any alias collision involving a generated fragment fails closed.

`dev fleet config show` prints the effective merge, redacts plaintext passwords,
and identifies generated hosts with instructions to use `dev ssh setup/remove`.
`dev fleet config edit` and the FLEET TUI `e` key continue to open only primary
`remotes.toml`; generated registrations have a different owner.

The UUID pin is optional for read-only inventory and diagnostics, but mandatory
for `fleet files --apply`. `dev fleet machine-id <host>` is itself read-only: it
shows the observed UUID, the configured value, and `unpinned`, `match`, or
`mismatch`; it never edits `remotes.toml`.

`dev fleet list` runs each machine's own `dev`, so its XDG config and paths stay
host-local. `remote_os = "windows"` selects the encoded, argument-allowlisted
PowerShell launcher and Windows target-path semantics; POSIX is the compatible
default and uses the existing shell launcher. Missing `dev` installations are
reported as `no-dev`; unreachable hosts can fall back to the last private XDG
snapshot. Cache identity includes the complete SSH endpoint, including port and
remote OS. FLEET hides local by default; `a` or the menu reveals it collapsed at
the end, reusing REPOS. Search and coverage exclude hidden local data and include
known Herdr profile state, labels and sessions. A separate local catalog read
supplies the HERDR column: not added, enabled, disabled, mixed counts or unknown.
Space expands/collapses the host tree; on a child it collapses and selects its
host. Enter/`o` navigates. Inside Herdr, it offers Add or Enable when required;
outside, it attaches to the selected explicit session. Several profiles require
a choice. Repository navigation checks a compatible remote dev and the exact repo
before profile changes, then prepares its workspace without changing focus.
Select the reported machine/workspace in the native Herdr sidebar. Navigation
never starts a nested Herdr client or silently falls back to SSH after an error;
`--no-runtime` uses SSH directly. Enter on the local host switches to REPOS.
Ctrl+O offers SSH, dotfile status and per-profile Herdr control inside or
outside Herdr. Disable keeps the profile; Remove deletes its registration;
remote sessions continue running. Herdr actions refresh catalog metadata only.
Each remote repository snapshot refreshes independently. Set
`[tui.fleet] background_refresh = false` to disable delayed automatic refresh.
See [FLEET host actions](docs/guides/remote-fleet.md#dashboard-host-tree).
The CLI output remains the full local-plus-remote inventory.

`dev fleet sync <repo> --push` publishes the clean source branch, then fetches
matching clones by normalized Git remote identity. Only a clean checkout of the
same branch that is strictly behind is fast-forwarded. Dirty, ahead, divergent,
ambiguous and unreachable targets remain untouched and make the command fail;
hosts without `dev` or without that repository are explicitly ignored.

`dev fleet files [repo-or-path] --to <host>` is a separate, one-shot channel for
explicit ignored local files:

```bash
dev fleet files api --to jingle                    # plan only
dev fleet files api --to jingle --apply --yes      # create absent files
dev fleet files api --to jingle --replace --apply  # separately authorize conflicts
```

The source and target must already be the same fetch identity, attached branch,
and exact commit. Both Git configurations must classify every selected path as
untracked and ignored. Only bounded regular files are accepted (128 files,
8 MiB each, 32 MiB total at most); directories, links, special files, nested
repositories, `.git`, and submodule boundaries fail closed. The target reopens
held roots, journals before staging payloads, publishes owner-only files
atomically, and rolls back changed files on failure. It deliberately leaves
unprovably-owned empty parent directories rather than risk deleting another
process's replacement. Native Windows payload apply remains disabled.

This is **not** repository/task ownership transfer, backup, restore, or clone
eviction. It does not clone a missing target, switch branches, run provisioning,
synchronize catalog/notes, delete source files, watch for changes, or merge in
both directions.

## The agent skill

For configuration sharing, see [Agent configuration interoperability](docs/guides/agent-interop.md).
`dev mcp transfer`, `dev skill transfer`, and `dev instructions transfer` use
reviewed plans, exact source/target revalidation, and guarded undo. Skills can
share one tree through relative links or use verified upstream preparation in
another repository. MCP writers support five native formats and retain unrelated
settings; optional launchers resolve selected credentials only at runtime.
Copy/mirror recipes are optional, and ordinary native copies have no dev runtime
dependency. Private recovery stays outside Git; native Windows transfer writes
are currently disabled. Static inventories remain local and do not probe tools.

`dev` embeds its own skill so the installed guidance matches the binary. Its
entry is deliberately small: 323 words / 2,732 bytes, including a 32-word
discovery description. It introduces the core purpose, essential ownership
boundaries and where to read next. Detailed references remain installed but are
read only for the relevant advanced operation.

Agents use `dev <command> --help` for current syntax, `dev help <topic>` for
workflows, and the skill's conditional reference links for agent coordination,
retirement, SSH or transfer details. They need not preload the command reference,
repeat `dev --skill`, or run diagnostics for every task. `dev --skill` and
`dev skill print` print the same installable `SKILL.md` that `skill install`
writes; existing dotfiles installers retain that contract. These print routes
and `dev help` bypass unrelated config loading, release checks and stale Windows
binary cleanup, while preserving argument and color validation.

```bash
dev skill list                 # current checkout + global native inventory
dev skill list --all --check --json
dev skill list --repo api --project
dev skill transfer plan example --from-agent universal --to-agent claude-code --mode mirror
dev skill transfer apply --plan <id> # apply the exact reviewed file/link changes
dev skill add                  # interactive wizard for daviddwlee84/agent-skills/skills
dev skill update project-knowledge-harness --global --yes
dev skill install              # → ~/.agents/skills/dev-cli, symlinked into ~/.claude/skills
dev skill install --check      # compare the installed bundle with this binary
dev skill uninstall --dry-run  # preview removal of dev-owned files and matching links
dev skill uninstall            # confirm and remove the bundled skill
dev mcp list --all --json      # sanitized static declarations; never health probes
dev --skill                    # print it, for a dotfiles installer to sync
dev skill sync                 # regenerate the command reference from the command tree
dev skill sync --check         # fail if it has drifted — wire into CI
```

After `dev upgrade`, the new executable refreshes the default bundled skill only
if it is already installed. `dev doctor` reports content drift. Recorded local
edits block automatic refresh and uninstall; unrelated skills, files and foreign
links remain. Custom `--dir` installs need an explicit refresh with that directory.
When upgrading directly through a package manager or from a binary older than
v0.2.23, run `dev skill install` once after upgrading. Legacy installs need that
first refresh to record ownership before uninstall. See the
[skills lifecycle guide](docs/guides/skills-management.md#bundled-dev-cli-skill-lifecycle).

`dev skill add [package]` is only a shortcut into the upstream interactive
wizard. It never selects all skills or agents. Listing is native and never runs
`skills`, npm, `npx`, or project code. Add/update are explicit actions, require
a directly installed `skills` executable, and may access the network; `dev` skips
repository-local `node_modules` candidates, rejects source-less update locks, and
serializes provider processes. `--all` scans each configured canonical checkout
once. The TUI instead defaults to its startup context—only the exact checkout
inside Git, or accepted REPOS plus the ordinary directory outside Git—and `A`
toggles all accepted repositories for that run; global paths are read once.
Freshness hashes Git object bytes without checkout
filters or autocrlf transforms; locale-dependent non-ASCII folder hashes stay
unverifiable. Lock hashes describe upstream freshness, not installed-file integrity;
only the embedded `dev-cli` skill can verify that every bundled file matches
(additional user files are ignored).

`dev mcp list` inventories declarations from Claude Code, Codex, Cursor, Gemini
CLI, and OpenCode files. It keeps scopes separate, honors absolute
`CLAUDE_CONFIG_DIR`, retains exact Claude local project keys, and resolves only
Claude's documented user/project/local/managed project approvals. It deliberately
omits runtime health, a generally effective merged configuration, plugin caches,
hosted connectors, remote organization config, and command-line-only config.
Environment/header/OAuth values, raw arguments, URL credentials, and indirect file
contents never enter normalized output; only safe reference names and finite
policy/count facts remain.

The skill defers to the companion `git-workflow` skill for commit conventions,
SemVer and branch naming rather than restating them. It owns what is new: the
worktree ownership rule, the task lifecycle, and how `dev` and herdr divide
responsibility.

## Development

```bash
make          # fmt, vet, test, build
make test
make e2e      # drives the real binary through a full lifecycle in a sandbox
make skill-check
```

Tests build throwaway repositories under `t.TempDir()` via
`internal/gitx/gittest`, and the runtime adapters share one contract suite that
skips backends not installed on the machine — so the suite is meaningful in CI
(where only the null backend exists) and locally (where herdr and tmux do).

The opt-in macOS desktop smoke test moves and restores only unique temporary
folders: `DEV_TEST_NATIVE_TRASH=1 go test ./internal/desktop -run TestNativeTrashRoundTrip`.


`Ctrl+O` action menus support `/` filtering, arrow navigation and Enter. Escape
clears search before closing the menu. REPOS first shows dated cached rows,
then streams local discovery and Git enrichment; pending rows cannot authorize
actions. `dev cache clear repos` removes this disposable presentation cache.

Selected skill removal is available through SKILLS / `dev skill manage`, with
agent scopes, ownership/dependency checks and private results. Existing artifact
finalizers remain separate; see [skills management](internal/skill/dev-cli/references/skills-management.md).

For skills maintenance, use `dev skill manage [--repo api | --all]` or the
REPOS/SKILLS action menus. The wizard checks sources, selects project/global
skills, previews updates and retains individual results. It requires a global
`skills` executable only for mutations (`npm install -g skills`), with no npx
fallback. Single-project advanced actions restore from `skills-lock.json` or sync
from installed `node_modules`. Lock-only/gitignored skill trees remain visible.
See [Skills management](docs/guides/skills-management.md) for exact scope and
native restoration semantics. `dev skill install`/`sync` still manage the bundled
skill. Check evidence is dated and invalidated by lock changes.

### SSH connection diagnosis

Use `dev ssh diagnose <alias-or-host>` for bounded configuration, DNS, native
route, TCP, banner, host-key and authentication stages. `--compare-qos` opts into
a comparable fresh QoS test; `--json` is a local report containing endpoint data.
See [SSH diagnosis](docs/guides/ssh-hosts.md#ssh-diagnosis).

### Feedback and isolated repair

`dev feedback` collects a local report; agents can use `feedback draft`,
`feedback issue` and plan-first `feedback repair --base <ref>`. Reviewed GitHub
publication is optional. Repair prepares a retained isolated worktree/task and
can render `dev prompt render feedback-fix <id>` for the current agent; another
agent requires an explicit profile and user consent. See the
[feedback guide](docs/guides/feedback.md).
