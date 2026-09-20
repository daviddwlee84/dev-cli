# TUI navigation

Bare `dev` opens the dashboard when stdin/stdout are terminals; `dev tui`
opens it explicitly. When piped, bare `dev` prints `dev ls` instead, so shell
composition stays predictable.

## Eight views

Switch with `tab`, `l`/`h`, right/left, or by left-clicking a visible tab:

| View | Answers | Data source |
|---|---|---|
| TASKS | What am I working on? | task registry + live Git/runtime state |
| REPOS | What durable repositories exist here? | configured scan roots + asset catalog |
| FLEET | What exists and is active on my other machines? | remote `dev` snapshots over SSH |
| TRY | Which experiments can I resume, archive or graduate? | experiment catalog + live Git/runtime state |
| REMOTE | What can I open or clone? | authenticated forge CLI inventories |
| SKILLS | Which skills would an agent launched from this context read? | context-first targets plus an `A` all-repositories toggle |
| MCP | Which MCP declarations would that agent context expose? | sanitized static agent configuration with the same shared scope |
| SSH | How do I connect to a machine? | local aliases, canonical registry, cached discovery and provider membership |

The initial TASKS frame is built before runtime auto-detection, project-root
lookup, cache decoding, shell tool probes or the optional release refresh can
finish. TASKS, REPOS and TRY then publish independently from one shared local
load cycle; REMOTE, SKILLS, MCP and SSH remain lazy; FLEET warms hosts after a delay. Each view
has its own generation: `r` supersedes the old read, late results are ignored,
failed refreshes keep usable rows, and a successful empty result clears old
rows. Warning-only SKILLS/MCP diagnostics remain fresh partial snapshots instead
of causing revisit loops; a visible dependent view automatically resumes after
REPOS recovers. Cached rows and current live results are separate readiness
stages; there is no all-tabs-ready state for views that may never be opened.

Mouse tracking works alongside the keyboard model. A left-button press selects a
visible row or tab; clicking the selected row opens its action menu, including
keyboard/startup selections and without double-click timing. The wheel moves three
rows, and right-click selects a row then opens its menu. Releases, motion and
modified row clicks do not activate rows. Opening a menu executes no option; Enter/`o` opens
the row. Depending on the terminal, hold Shift/Option for native text selection.

TASKS remains the initial view. The first REPOS visit selects the startup repo,
keeping the configured order and scrolling it into view. Subdirectories, aliases
and linked worktrees identify the main repository through Git's common directory.
Manual selection, filtering and sorting take precedence over delayed results.

An outside startup Git repo gets clickable Add this repo / Scan parent directory
entries in REPOS and Ctrl+O. Preview the actual config file (including --config),
field, path and scope, then click confirm or cancel. Exact repo_paths suit isolated
repos; scan_roots uses the main repo's parent and includes sibling projects under
normal depth-3 rules. Preserve comments, defaults and existing order; duplicate
coverage is rejected and old entries are not consolidated automatically. Saving
refreshes local inventory and selects the added repo; reload failures are reported
separately. Incomplete scans never prove a repo is outside discovery.

Automatic edits use the guarded macOS/Linux file backend with private recovery
under $XDG_DATA_HOME/dev/config-recovery. Changed source bytes or repository
identity reject the preview. Symlink configs, unsupported TOML layouts and other
platforms offer a manual change and the configuration editor.

## Contextual Help

Press `?` or click Help in the dashboard footer. Help starts on the current
view's Keys, with its TL;DR and grouped shortcuts, navigation and configured
tools. Guide explains that view's workflow, columns, colors and symbols. Its
selected-row section is captured when Help opens, retaining source and unknown,
loading or cached state; it is not a new observation or permission to act.

Manual contains all embedded dev help topics, related topics first, and the
shared workflow TL;DR. Search covers names, titles, headings and body text; open
a result at its matching passage. Paragraphs wrap, while code, ASCII diagrams
and tables preserve structure and support horizontal scrolling.

```text
1 / 2 / 3     Keys / Guide / Manual
v             choose help view or All views; dashboard stays unchanged
/             search Keys + Guide, Manual, or the current article
j / k         choose an entry, or scroll its details/article
enter         read the selected entry
n / N         next / previous article match
left / right  pan wide code or diagrams; clickable pan controls also work
f             expand / restore Help
esc           stop editing, clear query, return, then close
q             close Help outside text input
```

Keys/Guide search is limited to the chosen help view unless All views is
selected; shortcuts appear before guide matches. Back preserves the previous
query and reading position. While editing a Help index/results query, Up/Down
selects a visible entry without leaving input; Enter keeps the query and selection
without opening it. Press Enter again outside input to read the selected entry.
Cursor-only text edits retain selection; only changed text resets the results.
Letters, including `j/k`, and number keys stay text while editing. Article find
still submits with Enter and uses `n`/`N` for matches after leaving input.
Tabs, scope, search, Back, Expand, Close and pan controls are clickable. Use the
wheel, PgUp/PgDn or the clickable/draggable scrollbar for longer content.

Help and Ctrl+O share a floating popup, normally at most 104 columns by 32 rows
with a two-cell margin. Below 80 columns or 22 rows it fills the screen. Clicking
outside closes without activating the dashboard. The help reader uses embedded
documents, previously known tool availability and existing dashboard data; it
starts no Git, forge, runtime, skill-provider or MCP probe. Flow and triage keep
their independent help interfaces.

REPOS Guide explains cyan/blue selection overriding normal color, orange dirty
paths and gray quiet/external rows; gray alone cannot prove clean Git. It covers
Git divergence, conflicts/staged/unstaged/untracked counts, clean/local, unknown
?, cached ~, loading/truncated ... and each column's dash. LIVE, WT, task counts,
logical owned SIZE and LATEST have separate explanations. Other views have
their own legends: green MCP means enabled configuration, not server health.

## Repository lifecycle preview

`dev flow [repo]` is a separate preview-labelled, full-screen TUI; it does not
share the dashboard model above and requires terminal input/output. In any
canonical or linked checkout, bare `dev flow` opens the canonical repository and
focuses that exact surface. Outside Git it opens a filterable repository picker;
an explicit repo overrides cwd.

Its rows are every Git-registered worktree plus task-only records, including
COLD/DONE tasks with no checkout: `canonical`, `managed`, `unmanaged`, `harness`,
`task-only`, and `conflict`. The center keeps persisted HOT/WARM/COLD/DONE intent
separate from observed Git/runtime/artifact evidence. The right side offers exact
managed lifecycle plans, or metadata-only Adopt and clean branch-preserving
Remove for eligible unmanaged linked checkouts. Canonical checkout removal,
harness cleanup, and destructive conflict resolution fail closed.

```text
j/k or arrows row    h/l or arrows action    Tab focus    Enter plan
r local facts only   R fetch/query/both       ? help       Esc back
```

Enter never applies. A READY plan needs `y`, or its displayed exact typed token
plus Enter. Apply revalidates the task revision and repository/checkout/ref/
runtime/artifact identities, then retains an ordered result ledger even after a
partial failure. The preview has no dirty commit/discard, WIP, takeover, shared-
writer, or unknown-runtime override; blocked plans show remediation and a CLI
fallback. Startup and `r` never use the network. Confirmed `R` evidence is
run-local and limited to refs plus review existence/state/draft/URL/provider/time,
not review decisions or checks.

For one run, set `DEV_TUI_TRACE` to an absolute file that does not already exist:

```bash
DEV_TUI_TRACE=/tmp/dev-tui-trace.json dev
```

The bounded versioned JSON is written with private permissions after the
alternate screen is restored. It contains only relative timings, aggregate row
counts and categorical view/generation/outcome fields—never project/host/tool
names, paths, commands,
key values, URLs, runtime handles or raw errors—and never enters `stats.db` or
the network. `tui.initial_view_returned` means the view string was built, not
that a terminal rasterized it.

REPOS shows branch, dirty state, owned logical size, linked-worktree count and
HOT/WARM/COLD task tallies. Press `space` to expand a repo into its linked
worktrees; each child has its own Git, runtime and task state, and enter opens
that checkout. Git-backed Tries are shown in TRY rather than appearing twice;
REMOTE still labels their local checkout as `try`. Repos with active work sort
first. Press `n` in REPOS, or choose new repository in Ctrl+O, to suspend into the
existing clone-aware `dev repo new --handoff stay` wizard. This is independent
of the selected row: empty/filtered-empty lists, pending observations or a row
vanishing while the menu is open do not block launch. An active clone still
blocks creation. The wizard retains confirmation and fresh destination,
nested-repository and exclusive-create checks before mutation; other row actions
and REMOTE clone freshness checks remain guarded. Successful completion refreshes
local TASKS/REPOS/TRY state. On a first run with no tasks, press `n` to create a
repository, `s` to start work in an existing one, or use TRY `n` for a low-cost
experiment.

REMOTE loads lazily, so dashboard startup never waits on the network. Its
private XDG cache is decoded after the first view and holds the complete
paginated inventory. Fresh rows avoid a network query; stale rows remain
searchable while refresh runs in the background. Oversized/malformed cache
payloads and caches fingerprinted for another configured GH/GL host or Azure
target are ignored. GitLab uses explicit `GITLAB_HOST`/`GLAB_HOST` (default
`gitlab.com`) rather than inferring a host from cwd; a successful empty refresh
clears obsolete rows. `r` refreshes explicitly. It marks remotes already cloned
under `scan_roots`. Enter opens an existing local clone. For an absent repo, `c`
opens a confirmation: `enter` clones and stays in the dashboard, while `o`
clones and opens afterward. A `project_root` outside configured REPOS discovery
roots/depth is rejected before mutation. An animated row/status marker remains
visible until the local-only REPOS refresh accepts the clone; `q`/Ctrl-C
requests cancellation without abandoning the in-flight result. Once accepted,
REMOTE shows `repo` and REPOS can search it. A failed Git clone that leaves a
destination is labelled `inspect` with its exact path; dev does not delete it
automatically. Filters include visibility, for example
`vis:private`.

SKILLS and MCP also load lazily after the accepted REPOS snapshot. Inside Git,
both scan only the exact startup checkout and read user/global sources once, so
unrelated projects do not add noise. Outside Git, context mode retains the
dashboard's cross-repository inventory and scans every accepted REPOS target plus
the ordinary startup directory. Uppercase `A` switches both views between this
startup context and all accepted repositories for the current TUI run; old rows
are cleared before the new generation loads. Skill listing is native and never
executes Node or a provider; press `c` for the explicit read-only upstream source
check. Refresh/check keeps installed rows visible. MCP lists static, sanitized
declarations only and does not start or health-check servers. `r` reloads local
declarations. On either view, `e` opens the selected local file and `y` offers
path, sanitized-summary, and explicit raw-file copy actions.

A wide TASKS table includes `REPO`; compact layouts keep repository/path in the
selected detail pane. SKILLS filters include `repo:`, `scope:`, `agent:`,
`update:`, `presence:`, and `integrity:`. MCP filters include `repo:`, `agent:`,
`scope:`, `transport:`, `managed:`, and `state:`.

GitHub and GitLab are discovered from authenticated `gh` and `glab` CLIs.
Azure DevOps Services inventory is opt-in because each query needs an explicit
organization and team project:

```toml
[[forge.azure_devops]]
organization = "https://dev.azure.com/acme"
project = "Platform"
```

Repeat the table for additional projects. Azure CLI and its `azure-devops`
extension must already be installed and authenticated; dev does not install the
extension, change Azure defaults, or store credentials.

FLEET is an expandable remote host tree. Local is hidden by default; a or the
menu reveals it collapsed at the end, reusing REPOS. Hidden local is excluded
from search and coverage. Configured hosts and actions do not wait for repo
scans. HERDR shows independently observed saved-profile state, not connection
health. The menu can enable/disable/remove an exact profile, inside or outside
Herdr, while preserving remote sessions. Multiple profiles use a picker.
Catalog actions refresh local metadata without refreshing remote repos.
Cached data remains searchable with age and coverage.
Background refresh warms missing/expired snapshots once, starting after five
seconds or an earlier visit; it never prompts for a password. Set
[tui.fleet] background_refresh = false to disable it. Search itself initiates
no extra connections. See dev help fleet and dev help dotfile for host actions.
Space expands/collapses the host tree; on a child it selects the collapsed
parent. Enter/o navigates; local host Enter switches to REPOS. Inside Herdr it
offers Add/Enable if needed, prepares remote workspaces without focus and reports
the native sidebar target. Outside it attaches to an explicit session. Multiple
profiles require a choice; remote repos require the new helper and exact identity
checks. It never launches a nested Herdr client or falls back to SSH after a
Herdr error. --no-runtime uses SSH directly.
CLI dev fleet list continues to include local and remote repositories.

## Vim-style movement

```
j / k                next / previous
ctrl+d / ctrl+u      half page down / up
g / G                top / bottom
h / l, shift-tab/tab previous / next view
/                    filter as you type
?                    contextual Keys / Guide / Manual help
esc                  close prompt/filter/overlay; when clear, quit
q                    quit (or close help/action menu)
left click           select a visible row or switch a visible tab
click selected row   open its action menu (no double-click timing)
wheel                 move three rows up/down
right click          select a row and open its available actions
```

In all eight dashboard views, Up/Down selects visible results while `/` input
stays focused, retaining the query and text cursor without running an action or
initiating network work. `j/k` and other letters remain text. Left/Right/Home/End
move the text cursor; cursor-only edits and no-op deletions keep the selected
result. Only an actual query change selects the first result. Enter retains the
query and selection and exits input without opening the row; a later list Enter
uses the normal open action. Esc while filtering clears the query. Pending repo
details do not hide the input or its key hints. Notes search still waits for
Enter before querying. Forms, SSH dialogs and Ctrl+O action search keep their
existing behavior.

The `/` query applies to the current view and matches whitespace-separated
terms independently. Structured local filters include `tag:important`,
`remote:none`, `size:>1GiB`, `phase:deprecated` and `where:archived` where
applicable. `gitlab auth` still finds a GitLab repo whose name/description also
contains auth, regardless of word order.

## Actions

TASKS:

```
enter / o  open in the selected runtime
n          add a quick repository note
N          browse/search/edit/delete repository notes
p          park warm, prompting for the next action
c          edit the next action
ctrl+o     choose task-state filters and other actions
a          include DONE
```

Generic open never repairs lifecycle drift. A normal COLD worktree task points
to `dev resume`; a missing or unregistered checkout points to `dev sweep`, which
reports any artifact salvage requirement before offering resume/reap cleanup.

REPOS:

```
n          new repository through the clone-aware CLI wizard
a / N      quick-add / browse repository notes
enter / o  ad-hoc open: no task, branch or worktree
space      expand/collapse linked worktrees
m          edit repository tags and note
d          track direct work on the current branch (usually main)
s          isolated task: branch + worktree + provisioning + runtime + entry
O / R      cycle / reverse activity/latest/name/git/size/tasks sort
y          copy menu; follow with y/p/b/s/w/u
```

FLEET:

```
enter / o  navigate to host or repository; local host switches to REPOS
space      expand/collapse a host; on a repo, collapse and select its host
Ctrl+O     host/repository action menu (also right-click)
a          show/hide local at the end (this session only)
r          refresh the selected host and Herdr metadata
/          filter known hosts and cached/loaded repositories
```

In TRY, `n` remains “new Try”; quick notes intentionally do not attach to Try
assets. Right-clicking a REPOS row exposes the same new-repository action.

The LIVE column makes runtime state explicit (`herdr:working`, `herdr:idle`). A
collapsed repo with several sessions shows `herdr:N live`; expanded children
show their individual status or `closed`. Ephemeral agent-harness worktrees and
other untracked worktrees stay visible as `(ephemeral)` / `(external)`, so the
children always explain the WT count. LATEST is the newest dirty-file mtime,
commit time, or task update. SIZE is `checkout + private Git` logical bytes;
shared Git objects are shown in detail and never charged to every worktree. The
detail pane also calls out `no remote`, local-only branches and multiple
branch-upstream remotes.

`y` opens a second-key copy menu:

```
yy  agent-ready Markdown context (whole repo on parent; one checkout on child)
yp  selected checkout's absolute path
yb  selected branch
ys  runtime handles and agent session IDs (parent or child scope)
yw  every linked-worktree path in the repo, one per line
yu  clone URL; prefer origin, ask among multiple remaining fetch URLs
```

The same full Markdown is available without a clipboard or TUI through
`dev repo context [repo]`. With no argument it resolves the current repository;
inside a linked worktree it still reports the whole repo.

Clone URL copy is local-only, including on REMOTE (`CloneURL`, then `SSHURL`).
It never substitutes a browser URL or copies an empty/unsafe source. Selection
changes and clipboard failures are reported; no clone or submodule is created.

TRY:

```
enter / o  open a present Try
n          create/clone a Try (name, optional clone ref, git yes/no)
Ctrl+O     metadata/lifecycle actions, Trash or permanent disposal
a          include deprecated, archived, evicted and graduated history
O / R      cycle / reverse activity/name/phase/size sort
```

Archive is a reversible same-filesystem move under `tries_root/.dev`; it does
not reclaim disk space. Explicit delete prefers system Trash and retains history.
The catalog keeps a stable ID, per-host location, tags/note, last-opened time and graduation
history; Git and size facts remain live/derived.

REMOTE:

```
yu         copy clone URL without fetching (also in the row action menu)
n / N      notes, only when a local clone exists
enter / o  open an existing local clone
c          confirm an absent repo; then enter stays or o opens after clone
q / ctrl-c request cancellation while clone/refresh/open is pending
r          refresh configured forge CLIs, replacing the cache
```

SKILLS:

```
a          open the interactive installer (default personal skill catalog)
c          check lock-managed Git sources without installing updates
u          confirm and update only the selected lock-managed skill
A          toggle shared context/all repository scope for this TUI session
e          open the primary SKILL.md, or the lock file for a missing skill
y          p path · s safe summary · u source URL · f whole raw file
r          reload local project/global state without network access
```

The detail pane separates repository/checkout, local presence/integrity, agent
compatibility, source, manager, and upstream freshness. Filters include
`repo:api`, `scope:global`, `agent:Codex`, `presence:missing`, and
`update:update_available`.

MCP:

```
A          toggle the same context/all repository scope
e          open the selected declaration's ConfigPath
y          p path · s safe summary · f whole raw config file
r          reload static declarations; no server is started or probed
```

The MCP detail pane contains sanitized config/source/transport/policy facts,
exact Claude local project identity, bounded credential-reference summaries, and
redaction markers—never raw args, environment/header/OAuth values, indirect file
content, or connection health. That normalization still governs rows, safe
summaries, and JSON. Explicit `yf` instead reads the entire local source file (up
to 1 MiB) into the system clipboard; it performs no network access but may copy
credentials and other declarations from the same file. Capability editing uses a
private working copy, revalidates the observed identity immediately before
atomic replacement, and preserves the working copy when a conflict is detected.

## Heatmap and editors

```
H   open the selected repository's calendar-year activity heatmaps
    automatic local-history backfill; b forces it; r refreshes; H / esc returns

e   edit effective dev config on ordinary views; edit the selected capability file on SKILLS/MCP
r   reparse config and reload local/remote data and tool bindings
```

Opening the heatmap reads saved stats, then automatically backfills all local Git history.
Unchanged refs reuse a checkpoint. Years run oldest first; arrows/wheel, PgUp/PgDn
and Home/End scroll. Git activity is a 20-minute estimate per non-merge commit.

Returning from an ordinary-view `e` live-reloads dev config; returning from a
SKILLS/MCP editor reloads only that capability inventory. Changes to scan roots, worktree
policy, forge cache settings and `[[tui.tools]]` take effect immediately. A
runtime backend change needs a restart because existing callbacks and sessions
belong to the backend the TUI opened with; the status line says so rather than
pretending to switch underneath a live workspace.

## External tools are explicit configuration

```bash
dev tui tools
```

shows every binding, the exact command, its source, and whether it can run on
this machine. `dev config init` writes the defaults out in full:

```toml
[[tui.tools]]
key  = "L"
name = "lazygit"
run  = "lazygit"

[[tui.tools]]
key  = "Y"
name = "yazi"
run  = "yazi"

[[tui.tools]]
key  = "V"
name = "nvim"
run  = "nvim ."

[[tui.tools]]
key  = "B"
name = "vibe"
run  = "vibe"
interactive = true

[[tui.tools]]
key  = "P"
name = "plans here"
run  = "claude-plans-here"
interactive = true
```

A configured list replaces the defaults entirely. Commands run through
`$SHELL` in the selected row's checkout, so arguments, environment variables
and executable scripts work. `interactive = true` runs through `$SHELL -lic`
and deliberately evaluates the command *after* rc loading, so this machine's
`vibe` alias and `claude-plans-here` function work. Prefer a real script on PATH
when the binding should be portable to machines with different shell configs.
The dashboard resolves availability in a bounded background load after the first
view; rendering itself never launches the shell. Unknown or missing bindings are
hidden and fail closed until the current config generation has been checked.

Keys are case-sensitive. A tool cannot take a globally owned dashboard key;
config loading reports the collision instead of silently shadowing movement or
quit. `A` remains configurable, but the SKILLS/MCP scope toggle takes precedence
on those two views.

## Lifecycle action menus

`Ctrl+O`, right-click and clicking the selected row open row actions. Space
expands/collapses REPOS worktrees and FLEET hosts; it is unused in flat lists. TASKS offers finish, resume, retire and
selected-task recovery via the existing CLI workflows, with fresh task revision
checks. `a` means show completed tasks. Missing checkouts require recovery.

REPOS `s`/`d` use the complete start wizard with worktree/direct preselected.
Choose open (default) or stay before the final creation confirmation. Terminal
handoffs run only after the dashboard exits; cancellation and ordinary completion
refresh the dashboard. Browser actions open the selected repository homepage.

TRY actions include move to Trash, separately confirmed permanent deletion, and
reassociation after restoring the original folder through the OS. Trash does not
release disk space until emptied. See `dev help tries` for guards and recovery.

## Dashboard navigation and organizer entry

Enter opens a repository/task row or starts FLEET host navigation. Space expands or collapses REPOS/FLEET trees; flat lists leave Space unused. REPOS/TRY `Ctrl+O` offers organization of the
current item, filtered results, or all local work; multi-selection belongs to the
independent triage screen. `1–7` selects TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS,
and MCP respectively. TASKS state filters live in its action menu (`a` still
shows done tasks). Click a data column for ascending → descending → default
ordering; FLEET HOST groups machines. Sorting is local to each view/session and
uses the current snapshot, with unknown values last. The footer keeps two lines
of primary actions and navigation; tools, state filters and sorting are in
`Ctrl+O`, and `?` opens contextual Keys/Guide/Manual help. Existing custom tool bindings for `4–7`
need reassignment; `x`/Ctrl+A are no longer reserved dashboard selection keys.

Triage uses grouped repo/Try checkboxes, mouse selection, and Ctrl+A all/none
within the filtered scope. Results preserve failures when returning to the
dashboard. Git synchronization diagnostics retain a category, exit code, bounded
redacted output and a next step; old receipts cannot recover discarded reasons.
Retries require a new preview. Authentication, fetch and rebase are never
silently performed as error recovery. Use `dev triage` to open the organizer.


Ctrl+O menus: `/` filters the current menu; arrows move and Enter continues.
Escape clears the query before closing. REPOS shows a dated cache first, then
incremental local observations. Pending rows are display-only for row-dependent
operations, which wait for fresh observations. REPOS `n` and the new-repository
menu action are independent entry points; the wizard's final mutation checks
still apply. Clear the presentation cache with `dev cache clear repos`.

REPOS/SKILLS Ctrl+O opens `dev skill manage`: project/global selection, source
checks, reviewed multi-repo updates, and single-repo lock restore/dependency sync.
Wizard multi-selection supports Space and Ctrl+A. Global `skills` is an optional
external dependency, never automatically replaced with npx. Use `dev help skills`
for scope, verification and native-operation details.

## SSH connection view

The eighth tab keeps one machine row, with Space expanding its SSH profiles.
Configured connections come first, ordered by the most recent local `dev ssh
connect` or dashboard connection attempt; unrecorded profiles follow by alias.
CONNECTION, ENDPOINT, SOURCES, CHECK and USED share aligned display widths.
Narrow terminals hide supplementary columns; details retain full source names,
identity state, timestamps and per-profile results. Column sorting and filtering
preserve selection, including when a discovery update regroups a profile.

`c` stays inside the dashboard: choose Tailscale, or a LAN interface, explicit
IPv4 range and ports. LAN defaults to port 22 and retains the 256-address,
16-port and 30-second limits. Progress includes completed endpoints and found
candidates. Cancel keeps collected observations. The list then shows **this
discovery**, so an old filter cannot hide the results; return to all connections
without losing the previous filter. Cache write failures retain the observations
for this session and offer recovery guidance. A missing SSH config, registry,
fzf or optional provider does not prevent LAN discovery.

Enter on an unconfigured candidate opens a native setup form with its selected
endpoint. Review alias, remote user, port and authentication. Configuration-only
is the default; Fleet and Herdr are independent opt-in destinations. Preview
includes first-time SSH initialization and explicit machine bindings. Native
SSH/key and Herdr interactions receive the terminal only after review. Returning
to the dashboard shows each completed, failed or unknown stage and selects the
new profile. Completed configuration survives a later authentication/provider
failure. A changed LAN scope requires renewed discovery/review.

Choice fields show every option with the current one bracketed, such as
`config · existing · [key]`; ←/→ or Space cycle them. Enter or Space on **Key**
opens a picker of file-backed local keys, **+ Generate a new key** and a manual
path; Esc returns to the form, and picking a key selects key authentication.
**+ Generate a new key** opens a short form for the new key path and an optional
comment before returning to the form. Keys from present Bitwarden, 1Password or
Secretive agents and a validated `SSH_AUTH_SOCK` are listed too. Agent-only choices
retain the exact fingerprint and socket; unavailable providers show guidance.
Security-key generation also exposes the type, provider, resident handle,
verify-required and application options. Review shows the native touch/PIN flow
and possible retained hardware effects; automatic Secure Enclave creation remains unavailable.
Vault choices use a separate creation review before returning to SSH setup. The
result retains item receipts if the later SSH form is canceled; Bitwarden desktop
handoff shows newly visible agent keys rather than claiming vault-item creation.

`Ctrl+O` on a row with LAN or Tailscale candidates offers **set up this discovered
target…**, a form prefilled from that observation, including a matching profile's
user. On a configured profile it offers **set up / install an SSH key…**: choose the
exact profile when several exist, pick a key, set Remote OS and optional
Fleet/Herdr, then review. Connection settings are not editable. A foreign alias
receives only the public key; a managed alias also records the key as its
IdentityFile, shown in the review.

`p` offers quick network tests or full SSH verification for a profile, machine,
or filtered profile set. Review the exact targets before starting. DNS/route,
Ping, TCP, SSH banner, handshake, host-key, authentication and session observations
remain separate; Ping failure does not gate SSH. Proxy paths are not bypassed by
direct probes. Up to four profiles run together, each bounded to 30 seconds;
Ping has two seconds and an SSH attempt fifteen. Cancellation retains finished
results. A network-only success is not successful authentication.

Usage and tests are separate private durable records under
`paths.state_dir/ssh/activity/`. Testing never updates USED. Usage records actual
process starts, including failed attempts, and covers only local dev-mediated
connections; no shell history is imported. Test results retain profile revision,
observation time and available route context. Old results are historical evidence,
not live connectivity or deletion authority; clearing discovery cache preserves
activity records.

The first frame reads local data and cache. While this view is active,
`[tui.ssh].background_refresh = true` refreshes missing or expired Tailscale
observations, at most once per five minutes with a five-second query limit.
Failures keep dated observations. Disable it for cache-only viewing. `r` reloads
local state; LAN scanning and SSH tests always require an explicit action.
`Ctrl+O` also opens problems and suggested actions, even on an empty list.

## Repository protection and skill removal

REPOS → Ctrl+O → hygiene offers status, the latest stored scan report, explicit
worktree/staged/local-history scanning, and reviewed setup for the selected
checkout or filtered repository pool. A worktree row targets that exact checkout;
reports never fall back to a sibling checkout or trigger an automatic rescan.
Scanning uses the current policy and a 20-minute limit, without fetching refs or
capturing raw values. Operations suspend into the shared CLI workflow; press
Enter to return. Setup previews changes and asks for confirmation before apply.

SKILLS offers selected removal with explicit agent scopes, ownership and
dependency checks. Use `dev hygiene manage` or `dev skill manage` outside the TUI.

## Contextual action availability

The eight dashboard views share built-in action registrations for menus,
action keys and Help. Actions unrelated to an item are hidden; applicable actions
that lack an integration or await observations stay visible as unavailable.
Select an unavailable action to read its reason without executing it. Opening or
searching a menu does not scan a repository or contact a remote. Fleet retains
its asynchronous local catalog adapter and exact host/profile identities.
