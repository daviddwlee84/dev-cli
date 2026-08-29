---
description: Inventory repositories, tasks, and live runtime activity across SSH-reachable machines with dev fleet, and safely fast-forward a branch between them.
authority: project
status: stable
verified_on: 2026-08-28
---

# Remote repository fleet

`dev fleet` fans out over SSH to other machines running their own `dev`, so you can see and open their repositories and safely propagate a branch, without merging their independent configuration into this machine's view.

## What a fleet is

A remote fleet is a list of other hosts, each running its own `dev` binary against its own `$XDG_CONFIG_HOME`, its own scan roots, its own task registry, and its own runtime. `dev fleet` never centralizes or takes ownership of that state — it only asks each host's `dev` for a read-only snapshot over SSH and reports what comes back. A host with no `dev` installed, or an unreachable host, degrades that one row; it never blocks the rest of the fleet.

This is a different concept from the REMOTE TUI view and `dev repo new`/PR flows, which talk to a forge CLI (`gh`, `glab`, or the Azure CLI) about repositories hosted on GitHub, GitLab, or Azure DevOps Services. Fleet talks to *your own machines* about *your own local checkouts* on those machines.

## Configure hosts

```bash
dev fleet config init
dev fleet config edit
dev fleet config show
dev fleet config path
```

Hosts live in `$XDG_CONFIG_HOME/dev/remotes.toml` (override the path with the global `--remotes <path>` flag). `config init` writes a starter file (`--force` to overwrite, `--stdout` to print without writing); `config edit` opens it in `$VISUAL`/`$EDITOR` (`--editor` overrides); `config show` prints the effective configuration with any plaintext password redacted; `config path` prints the resolved path.

```toml
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

[[hosts]]
name = "vps"
hostname = "203.0.113.10"
user = "dev"
port = 22
identity_file = "~/.ssh/id_ed25519"
ssh_login_password_source = { type = "bitwarden", item = "ssh-vps-login" }
```

`schema_version = 1` is required. `[defaults]` supplies `connect_timeout`, `command_timeout`, `cache_ttl`, `max_parallel` (fan-out concurrency), and `dev_path` (`"auto"` searches `PATH`); each `[[hosts]]` entry inherits any of these it does not set explicitly. A host needs `name` plus either `ssh_alias` (preferred — it reuses `~/.ssh/config`'s `ProxyJump`, `IdentityAgent`, and host-key policy exactly like a plain `ssh` invocation) or `hostname` (optionally with `user`, `port`, `identity_file`). `ssh_login_password_source.type` is one of `none` (default), `prompt`, `plain` (inline `value`), or `bitwarden` (`item` looked up with the `bw` CLI). A config file with a `plain` password must be mode `0600`, or `dev fleet` refuses to load it.

## `dev fleet` commands

| Command | Flags | Purpose |
|---|---|---|
| `dev fleet list` | `--host <name>` (repeatable), `--repo <query>`, `--json`, `--cached`, `--strict` | List repositories and activity across this machine and configured hosts |
| `dev fleet status` | `--json`, `--strict` | Probe configured hosts and report snapshot health |
| `dev fleet sync <repo>` | `--push`, `--remote <name>`, `--host <name>` (repeatable), `--json` | Push optionally, then safely fast-forward clean matching checkouts |
| `dev fleet open <host> <repo>` | — | Open a remote repository through Herdr or an SSH login shell |
| `dev fleet config init` | `-f`/`--force`, `--stdout` | Write a starter `remotes.toml` |
| `dev fleet config edit` | `--editor <cmd>` | Open `remotes.toml` in `$VISUAL`/`$EDITOR` |
| `dev fleet config show` | — | Print the effective configuration (passwords redacted) |
| `dev fleet config path` | — | Print the resolved `remotes.toml` path |

`--repo` on `list` filters by name, remote identity, branch, or path; `--cached` answers entirely from the last saved snapshot with no network activity; `--strict` turns an unreachable/timeout/incompatible/invalid host into a non-zero exit for both `list` and `status`. `sync <repo>` resolves `<repo>` locally like any other repository reference; without `--push` its `HEAD` must already equal the fetched upstream, and `--remote` picks which Git remote identifies the repository across hosts (default: the branch's upstream remote, then `origin`).

`dev fleet` also registers four hidden commands (`_snapshot`, `_sync`, `_open-herdr`, `_shell`) that exist only to be invoked by `dev fleet` itself on the far end of an SSH connection — they are the wire protocol, not a user-facing surface.

## Transport and authentication

Every fleet command reaches a host by shelling out to the system `ssh` binary with `ConnectTimeout`, `ServerAliveInterval=15`, and `ServerAliveCountMax=2`, and first attempts `BatchMode=yes` — key or agent authentication only, exactly like a script-friendly `ssh` call. `dev fleet open` additionally allocates a PTY (`-t`) for its interactive login shell.

If that attempt is rejected with "permission denied" and the host defines a `ssh_login_password_source`, `dev` retries once with password authentication. The password itself never touches argv or the environment: `dev` re-executes itself as a one-shot `SSH_ASKPASS` helper, and the secret is handed to that helper over an inherited file descriptor. `prompt` reads a hidden password from `/dev/tty` at run time; `plain` and `bitwarden` resolve it from the config file or from `bw get password <item>` respectively.

On the remote side, `dev_path = "auto"` (the default) searches common install locations (`~/.local/bin`, `~/go/bin`, mise shims, Homebrew/Linuxbrew, `/usr/local/bin`, `/snap/bin`) and then `PATH`, exiting `127` if no `dev` is found; an explicit absolute `dev_path` skips that search and execs it directly.

## What's cached vs. durable

`$XDG_CONFIG_HOME/dev/remotes.toml` is durable, user-authored configuration, on the same footing as `config.toml` — `dev fleet config init`/`edit` manage it, and nothing regenerates it automatically.

Each successful probe writes a per-host JSON snapshot under `$XDG_CACHE_HOME/dev/fleet/v1/<host-name-slug>.json`. That cache is a disposable accelerator, not durable data: it is fingerprinted with an "endpoint ID" derived from the host's connection fields and timeouts, so editing a host's `ssh_alias`, `hostname`, `user`, `identity_file`, `dev_path`, or timeouts automatically invalidates its old cache the next time `dev` reads it. `dev cache list` shows its path, size, and age; `dev cache clear fleet` (or `dev cache clear all`) removes it outright. There is nothing to "rebuild" explicitly — the next `dev fleet list`, `dev fleet status`, or TUI FLEET reload regenerates it from a fresh probe.

The cache exists so an unreachable, timed-out, incompatible, or invalid-response host can still report its last known state (marked `stale`, with `FromCache` set) instead of vanishing from the listing; `--cached` uses only this cache and never touches the network. Each remote host's own `config.toml`, task registry, and repository paths remain authoritative on that host — the cache only ever holds a read-only snapshot of them.

## Fleet in the TUI

FLEET is one of the TUI's six views (`TASKS`, `REPOS`, `FLEET`, `TRY`, `REMOTE`, `SKILLS`, switched with `tab`/`h`/`l`). Like REMOTE, it loads lazily — nothing is fetched until the view is first opened — but it seeds itself from any cached snapshots that are still within `defaults.cache_ttl` so the first render is not empty. `r` forces a live reload of every configured host.

Its table shows `HOST`, `STATE`, `REPO`, `BRANCH`, `GIT`, `LIVE`, `TASKS`, and `PATH`. `enter` opens the selected repository: for a local host row this is an ordinary local open; for a remote row `dev` prefers native Herdr remoting when that host's snapshot reports the `herdr` runtime and the host connects through `ssh_alias` with no password step, and otherwise falls back to an interactive SSH login shell in the repository's directory. Git changes are read-only in this view — FLEET is for inspecting and opening work, not editing it in place.

`e` opens `remotes.toml` in `$VISUAL`/`$EDITOR` — the file this view is about, rather than dev's own `config.toml`, which `e` opens everywhere else. On leaving the editor `dev` reparses the file before using it. A parse failure, an unknown field, or a permissions problem on a file holding a plaintext password is reported and the previous rows are kept, so a typo cannot silently drop a host from the fleet. A file that parses triggers an immediate live reload of every configured host.

## Degrading gracefully

Every host is probed independently, so one bad host never fails the fleet as a whole. Per-host states are `ok`, `stale` (cache reused, optionally with an error explaining why), `no-dev` (the remote has no `dev` on its `PATH` — reported, not an error), `unreachable` (SSH itself failed), `timeout`, `incompatible` (an old or unrecognized remote `dev`), and `invalid-response` (malformed snapshot JSON). `ok` and a clean `no-dev` never fail `--strict`; every other state does, including a `stale` result that carries an error.

Separately, the forge integration behind the REMOTE view and `dev repo new`/PR flows (`gh` for GitHub, `glab` for GitLab, and `az` for Azure DevOps Services) is independently optional. `dev doctor` reports each as a warning, not a failure, when it is missing from `PATH`, and every entry point that would use one degrades to plain Git behavior instead — no forge-backed repository listing, and no CLI-assisted pull/merge request creation — so no forge CLI being installed is ever required for `dev` to work. Azure DevOps inventory is additionally opt-in: it stays disabled until `forge.azure_devops` targets are configured.

## Sources

- [`internal/cli/fleet.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet.go)
- [`internal/fleet/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/config.go)
- [`internal/fleet/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/types.go)
- [`internal/fleet/transport.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/transport.go)
- [`internal/fleet/sync.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/sync.go)
- [`internal/fleet/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/cache.go)
- [`internal/help/topics/fleet.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/fleet.md)
- [`internal/cli/tui.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/tui.go)
- [`internal/tui/model.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/model.go)
- [`internal/tui/view.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/view.go)
- [`internal/tui/rows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/rows.go)
- [`internal/cli/doctor.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/doctor.go)
- [`internal/cli/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/cache.go)
- [`internal/forge/forge.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/forge.go)
