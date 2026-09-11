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

FLEET displays local and configured host headers independently of repo scans.
Local starts expanded; remote hosts start collapsed. Enter toggles a host or
opens a repository, Space/Ctrl+O/right-click opens actions, and r refreshes the
selected host. Search includes collapsed cached repos and reports coverage;
typing a query never schedules new connections.

Background warming starts about five seconds after startup or on entering FLEET,
once per eligible host, without password prompts/retries. Disable it with
`[tui.fleet] background_refresh = false`. Old snapshots remain explicitly cached;
only a fresh successful empty observation means zero repositories.

Host actions can SSH, observe dotfiles, or delegate Herdr integration without a
repo snapshot. Outside Herdr, native attachment names the remote session
explicitly. Inside Herdr, add/enable uses guarded native machine operations;
registration may install/start a remote server and preserves native approvals.
Herdr 0.9.0 add does not switch the selected machine. Never edit its private
catalog or claim registration updated a different machine's desktop client.

Keep the author's fleet chezmoi, dotcfg and appsrc independent. Dotfiles can
optionally install dev; their bootstrap must remain usable without it.
