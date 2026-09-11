# Dotfiles and host actions

Use `dev dotfile status --json` for passive chezmoi discovery and `dev dotfile
setup` for repository selection. The optional `--preset david` recommends the
author's standalone platform repository; `--repo URL` accepts the user's own.
Existing sources are retained. Missing chezmoi or specialized platform bootstrap
needs produce native installation guidance, not a downloaded installer.

Setup outside a terminal reports by default. `--yes` executes initialization;
`--apply` additionally requests deployment. These flags do not answer native
repository prompts. `--chezmoi-config PATH` is a local native-config override,
distinct from dev's own `--config`.

`dev dotfile diff [targets...]`, `apply [targets...]` and `update` delegate native
operations. Apply does not implicitly update. Update can invoke custom update
commands and apply scripts. Do not describe native diff/dry-run as sandboxed:
chezmoi hooks and template functions can run. Do not use rendered output as a
secret-free cache or promise rollback of arbitrary installers.

Passive status never runs chezmoi, hooks, template evaluation or Git fetch.
Preserve sourceDir, `.chezmoiroot` source-state directory and workingTree as
separate facts; a Git revision does not prove deployment. Missing/invalid or
ambiguous observations stay unknown, not clean. No chezmoi setup does not imply
that other dotfile managers are absent.

`dev fleet dotfile status --host NAME [--host OTHER] --json` uses each host's own
passive dev helper. Do not send controller config paths, source contents,
credentials or rendered diffs. Missing/old remote dev needs capability guidance,
not a fallback to executing chezmoi remotely. There is no remote apply in v1.

FLEET displays configured remote hosts independently of repo scans. Local is
hidden by default; a or the menu reveals it collapsed at the end for this
session, reusing REPOS. Hidden local data is excluded from search and coverage.
Space expands/collapses hosts; Enter/`o` navigates to a host or repository.
Ctrl+O/right-click opens actions.
Space on a child collapses and selects its host; search-only expansion changes
are discarded when the query clears. Flat lists leave Space unused.
The HERDR column observes the local saved catalog separately from SSH snapshots:
not added, enabled, disabled, mixed counts or unknown. Enabled is intent, not
proof of a live connection. r updates the selected host and Herdr metadata.
Search includes known Herdr state/profile labels/sessions and collapsed cached
repos; typing a query never schedules new connections.

Background warming starts about five seconds after startup or on entering FLEET,
once per eligible host, without password prompts/retries. Disable it with
`[tui.fleet] background_refresh = false`. Old snapshots remain explicitly cached;
only a fresh successful empty observation means zero repositories.

Host actions can SSH, observe dotfiles, or delegate Herdr integration without a
repo snapshot. Outside Herdr, native attachment names the remote session
explicitly. Add/enable/disable/remove use guarded exact-profile operations
inside or outside Herdr; multiple matching profiles use a picker rather than
implicit bulk actions. Disable preserves registration, Remove deletes it, and
both retain remote sessions. Catalog actions refresh only shared local metadata.
Connection capability restrictions must not hide disable/remove of an existing
exact saved profile. Registration may install/start a remote server and preserves
native approvals. Herdr 0.9.0 add does not switch the selected machine. Never edit its private
catalog or claim registration updated a different machine's desktop client.

Inside Herdr (`HERDR_ENV=1`), Enter offers Add/Enable as needed and reports the
native sidebar target. Outside it attaches to the chosen explicit session
without changing catalog enabled intent. Multiple profiles require a choice;
unknown observations are errors. Repo navigation verifies a compatible remote
helper and exact identity before profile changes, then prepares a workspace
without focus. It never starts a nested Herdr client or invokes session-wide
focus. A Herdr failure returns its partial result without falling back to SSH;
`--no-runtime` uses SSH directly. See [session behavior](runtime-herdr.md).

Keep the author's fleet chezmoi, dotcfg and appsrc independent. Dotfiles can
optionally install dev; their bootstrap must remain usable without it.
