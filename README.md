# dev

A thin glue layer over git, worktrees, forges and agent runtimes.

It exists to stop four things collapsing into one:

```
git remote      durable code state, the source of truth
git worktree    a disposable local checkout
herdr / tmux / zellij
                a per-host live runtime
dev             human intent: what am I working on, and what is next
```

Everything derivable from git or the runtime is derived live. `dev` persists
only human intent Git cannot answer: task **state/owner/next action/context**,
stable asset identity, a catalog metadata summary, multiple repository quick
notes, and experiment lifecycle. Live Git status stays live; logical size
measurements are explicitly disposable cache.

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
# Pin @v0.2.6 instead when you need a reproducible install.
# Or from a checkout: make install  # also installs the bundled agent skill
```

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

`dev` builds and runs on Windows, and core commands work. There is no tmux,
Zellij or Herdr there, so `dev` uses the no-multiplexer backend (it prints a
`cd` directive the shell wrapper consumes). Use the PowerShell wrapper:

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
worktrees, help topics and bundled gitignore templates. Completion never queries
a forge or the network.

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

Only **git** is required at runtime. `herdr`, `tmux`, `zellij`, `gh`, `glab` and
Azure CLI each enable more and degrade cleanly when absent.

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

The built-in `agent-ready` preset adds a starter `AGENTS.md`, a common
`.gitignore` section, and project-scoped Claude plans. If selected, the optional
`agent-history-hygiene` and `project-knowledge-harness` skills are installed and
dev's reviewed built-in initializers create the pre-commit/gitleaks and
TODO/backlog/pitfalls surfaces during bootstrap; setup does not wait for a later
agent to happen to trigger the skill. The history initializer also ensures
`.specstory/.gitignore` contains rules for SpecStory's machine-local
`.project.json` and generated `statistics.json`—not `.specstory/history/`, which
remains part of the review trail. Existing custom ignore content and file mode
are preserved; only missing managed rules are appended. Skills with the same
source and identical agent targets are sent to the installer together, while
each skill's declared setup still runs in its own phase. Presets may also declare
typed inputs, repository-contained file templates, and ordered
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
scripts use `--dirty=commit --message ...` or `--dirty=discard --yes`.

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

Bare `dev` (or `dev tui`) opens seven lists, switched with `tab`:

- **TASKS** — the change streams dev is tracking. What am I working on.
- **REPOS** — durable repositories under the scan roots, with branch, dirty
  state, owned size, runtime, worktrees and task tally. What do I have here.
- **FLEET** — repository and activity facts from configured remote machines.
  The TUI hides this machine by default because REPOS already shows it; `a`
  includes local rows. Enter opens the remote checkout through Herdr when
  possible, then SSH. `dev fleet list` continues to include this machine.
- **TRY** — dated scratch experiments, including non-Git folders, with durable
  tags/notes and explicit active/deprecated/archived/graduated state.
- **REMOTE** — repositories visible through authenticated forge CLIs, including
  configured Azure DevOps projects, marked when a local repo or Try exists.
  What can I open or clone.
- **SKILLS** — native project/global agent-skill inventory across canonical
  repositories plus the startup checkout, with local presence/integrity and an
  explicitly checked upstream freshness state.
- **MCP** — sanitized static MCP declarations for Claude Code, Codex, Cursor,
  Gemini CLI, and OpenCode. It reports configured scopes and resolves only
  Claude's documented project-approval settings; it does not claim connection
  health or a generally effective merged runtime configuration.

The first view is constructed before runtime auto-detection, project-root lookup,
cache decoding, shell-based tool checks, or the optional release refresh can
finish. TASKS, REPOS, and TRY then publish independently from one shared local
load cycle; REMOTE, FLEET, SKILLS, and MCP remain lazy. A cached REMOTE/FLEET snapshot
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

`enter` opens a selected row only when its checkout is currently valid. Inside Herdr/tmux/Zellij it
switches the current client; outside it exits the dashboard and attaches to
the target session. A COLD worktree task requires `dev resume`; a missing or
unregistered worktree requires `dev sweep` first so artifacts can be salvaged
before the task is resumed or reaped. A wide TASKS table includes `REPO`; its
compact layout keeps repository/path in the selected detail pane. In TASKS, `p`
parks and prompts for the next action and `c` edits it. In REPOS, `enter` is pure ad-hoc open,
`space` expands linked worktrees inline, `m` edits repository tags/summary,
`s` starts an isolated worktree task, and `d` starts a tracked direct task.
On TASKS and REPOS, `n` quick-adds a repository thought and `N` opens its notes
overlay. Expanded children carry their own Git/session/task state and can be
opened directly. In TRY, `n` creates or clones an experiment;
`space` opens mark/deprecate/archive/restore/graduate actions; `a` includes
retained history. SKILLS reuses the accepted REPOS snapshot, adds the exact
startup worktree when distinct, and scans global paths once. `a` opens the
upstream interactive installer, `c` performs the opt-in read-only network check
after the local snapshot has loaded, and `u` confirms before updating only the
selected lock-managed skill in that row's checkout. Source checks hash Git object
bytes without running checkout filters; non-ASCII provider folder hashes that
cannot be reproduced portably remain unverifiable. Mutations skip repository-local
PATH shims and are serialized across `dev` processes. `r` reloads local state
without checking the network. MCP only filters/reloads static declarations; it
never starts a server or helper. `?` opens the complete context-sensitive key map. That makes
the branch/worktree and lifecycle costs explicit rather than silently applying
them to every directory.

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
event because REMOTE, FLEET, SKILLS, and MCP may never be visited.

REPOS also has an agent-handoff copy menu. Press `y`, then `y` for contextual
Markdown, `p` for the checkout path, `b` for the branch, `s` for runtime/agent
sessions, or `w` for every linked-worktree path. Parent `yy` includes the whole
repo; child `yy` includes only that worktree. The same full context is
pipe-friendly outside the TUI:

```bash
dev repo context api
dev repo context          # current repo, even from inside a linked worktree
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
rendering never launches a login shell. A tool cannot take a key the dashboard
already uses; dev reports the clash on load.

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
is an exact visibility filter. Enter opens a local clone,
and `c` confirms before cloning an absent repo into `project_root`. The same
inventory is available without the full-screen UI via `dev repo remote [query]`;
`--cached` is its instant/offline form.

SKILLS also loads lazily. Native reads use the versioned `skills@1.5.23`
77-agent path registry and lock files; they never start Node, `skills`, or `npx`.
Use `dev skill list --all` for canonical repositories, `--repo` for one checkout,
and `--check` only when a remote freshness check is wanted; checks hash Git object
bytes without checkout filters. MCP is separately available through `dev mcp
list`; it reads five agents' static config formats, resolves only Claude's documented
project approvals, and redacts secret-bearing values before producing rows or JSON.

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
one-year activity heatmap and `e` edits config. Returning from the editor, or
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

See `dev help tui` for the full key map.

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
Phase 1 deliberately has no `evict`, recursive delete, or automatic remote
backup. Safe local removal needs repo-wide ref verification and remains a
follow-up rather than treating "has a remote" as proof.

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
dev fleet list                 # Git/task/runtime state from every configured machine
dev fleet machine-id lab       # inspect/compare the target's stable machine pin
dev fleet sync api --push      # push, then safely fast-forward clean remote checkouts
dev fleet files api --to lab   # report-only plan for explicit ignored local files

dev try redis-streams          # dated scratch directory for an experiment
dev tries archive redis-streams    # reversible local archive; does not delete
dev graduate redis-streams -c Infra --remote   # promote it into a real project

dev gitignore                  # .gitignore from GitHub's templates + the rest
dev adopt                      # import existing worktrees/sessions as tasks

dev stats --heatmap            # where the time actually went
dev summary                    # current machine-wide project snapshot
dev journal                    # today's commits plus current task/WIP context
dev help worktrees             # quick-reference pages for the workflow
dev help wt                    # same page, reached by command name
```

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

In TASKS/REPOS, `n` quick-adds and `N` opens a browse/search/edit/delete overlay.
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

In the TUI, select a repo and press `H`. If it has no data, `b` backfills only
that repo and refreshes the panel; `r` merely rereads existing stats data.

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

Configure SSH machines separately from host-local paths:

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
name = "jingle"
ssh_alias = "jingle-235"
# After `dev fleet machine-id jingle` and independent verification:
machine_id = "00000000-0000-4000-8000-000000000000"
```

The UUID pin is optional for read-only inventory and diagnostics, but mandatory
for `fleet files --apply`. `dev fleet machine-id <host>` is itself read-only: it
shows the observed UUID, the configured value, and `unpinned`, `match`, or
`mismatch`; it never edits `remotes.toml`.

`dev fleet list` runs each machine's own `dev`, so its XDG config and paths stay
host-local. Missing `dev` installations are reported as `no-dev`; unreachable
hosts can fall back to the last private XDG snapshot. Cache identity includes the
complete SSH endpoint, including port. The FLEET TUI view reuses the accepted
REPOS snapshot for its local host instead of scanning twice, but exposes the
configured remote-host inventory by default; press `a` to include this machine.
Enter opens a selected path through remote Herdr when its server is active,
otherwise through `ssh -t` and a login shell. The CLI output remains the full
local-plus-remote inventory.

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

`dev` ships the agent skill that documents it, embedded in the binary — the
same pattern `herdr --skill` uses. A skill vendored separately drifts from the
tool it describes, and an agent reading a stale command list is worse than one
reading none.

```bash
dev skill list                 # current checkout + global native inventory
dev skill list --all --check --json
dev skill list --repo api --project
dev skill add                  # interactive wizard for daviddwlee84/agent-skills/skills
dev skill update project-knowledge-harness --global --yes
dev skill install              # → ~/.agents/skills/dev-cli, symlinked into ~/.claude/skills
dev mcp list --all --json      # sanitized static declarations; never health probes
dev --skill                    # print it, for a dotfiles installer to sync
dev skill sync                 # regenerate the command reference from the command tree
dev skill sync --check         # fail if it has drifted — wire into CI
```

`dev skill add [package]` is only a shortcut into the upstream interactive
wizard. It never selects all skills or agents. Listing is native and never runs
`skills`, npm, `npx`, or project code. Add/update are explicit actions, require
a directly installed `skills` executable, and may access the network; `dev` skips
repository-local `node_modules` candidates, rejects source-less update locks, and
serializes provider processes. `--all` scans each configured canonical checkout
once, while the TUI also includes its exact startup linked worktree when distinct
and reads global paths once. Freshness hashes Git object bytes without checkout
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
