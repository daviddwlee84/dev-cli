---
description: Inventory repositories over SSH, pin remote machine identity, safely fast-forward branches, and explicitly transfer bounded ignored files with dev fleet.
authority: project
status: stable
verified_on: 2026-08-31
---

# Remote repository fleet

`dev fleet` fans out over SSH to other machines running their own `dev`, so you can see and open their repositories and safely propagate a branch, without merging their independent configuration into this machine's view.

## What a fleet is

A remote fleet is a list of other hosts, each running its own `dev` binary against its own `$XDG_CONFIG_HOME`, its own scan roots, its own task registry, and its own runtime. Inventory stays decentralized: each host's `dev` produces its own read-only snapshot over SSH, and one unreachable host never blocks the remaining rows. Mutation is confined to separately named commands such as `fleet sync` and `fleet files --apply`; neither makes this machine authoritative for the target's paths, task registry, or runtime.

This is a different concept from the REMOTE TUI view and publishing/PR flows,
which use `gh` or `glab` for GitHub/GitLab repository publication and PRs, plus
the Azure CLI for Azure DevOps inventory and PRs. Fleet talks to *your own
machines* about *your own local checkouts* on those machines.

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
# Add only after `dev fleet machine-id lab` and independent verification.
machine_id = "00000000-0000-4000-8000-000000000000"

[[hosts]]
name = "vps"
hostname = "203.0.113.10"
user = "dev"
port = 22
identity_file = "~/.ssh/id_ed25519"
ssh_login_password_source = { type = "bitwarden", item = "ssh-vps-login" }
```

`schema_version = 1` is required. `[defaults]` supplies `connect_timeout`, `command_timeout`, `cache_ttl`, `max_parallel` (fan-out concurrency), and `dev_path` (`"auto"` searches `PATH`); each `[[hosts]]` entry inherits any of these it does not set explicitly. A host needs `name` plus either `ssh_alias` (preferred — it reuses `~/.ssh/config`'s `ProxyJump`, `IdentityAgent`, and host-key policy exactly like a plain `ssh` invocation) or `hostname` (optionally with `user`, `port`, `identity_file`). The optional `machine_id` is a durable UUID pin: read-only inventory can warn or proceed without it, but mutating portable-file apply requires an exact match. `ssh_login_password_source.type` is one of `none` (default), `prompt`, `plain` (inline `value`), or `bitwarden` (`item` looked up with the `bw` CLI). A config file with a `plain` password must be mode `0600`, or `dev fleet` refuses to load it.

Discover a target UUID with `dev fleet machine-id <host>`, verify it through an independent channel on that machine, then copy it into `remotes.toml`. The command is read-only and reports `unpinned`, `match`, or `mismatch`; it never edits configuration on your behalf.

## `dev fleet` commands

| Command | Flags | Purpose |
|---|---|---|
| `dev fleet list` | `--host <name>` (repeatable), `--repo <query>`, `--json`, `--cached`, `--strict` | List repositories and activity across this machine and configured hosts |
| `dev fleet status` | `--json`, `--strict` | Probe configured hosts and report snapshot health |
| `dev fleet machine-id <host>` | `--json` | Show the observed durable UUID and compare the configured pin |
| `dev fleet sync <repo>` | `--push`, `--remote <name>`, `--host <name>` (repeatable), `--json` | Push optionally, then safely fast-forward clean matching checkouts |
| `dev fleet files [repo-or-path]` | `--to <host>`, `--file <pattern>` (repeatable), `--apply`, `--replace`, `--yes`, `--json` | Plan or apply one-way transfer of explicit ignored files |
| `dev fleet open <host> <repo>` | — | Open a remote repository through Herdr or an SSH login shell |
| `dev fleet config init` | `-f`/`--force`, `--stdout` | Write a starter `remotes.toml` |
| `dev fleet config edit` | `--editor <cmd>` | Open `remotes.toml` in `$VISUAL`/`$EDITOR` |
| `dev fleet config show` | — | Print the effective configuration (passwords redacted) |
| `dev fleet config path` | — | Print the resolved `remotes.toml` path |

`--repo` on `list` filters by name, remote identity, branch, or path; `--cached` answers entirely from the last saved snapshot with no network activity; `--strict` turns an unreachable/timeout/incompatible/invalid host into a non-zero exit for both `list` and `status`. `sync <repo>` resolves `<repo>` locally like any other repository reference; without `--push` its `HEAD` must already equal the fetched upstream, and `--remote` picks which Git remote identifies the repository across hosts (default: the branch's upstream remote, then `origin`).

## Explicit portable local files

A repository may propose export candidates separately from worktree provisioning:

```toml
# .dev-cli/config.toml
version = 1

[local_files]
include = [".env", ".mcp/**"]
```

`[worktree].include` is local provisioning policy and is never inherited here.
`[local_files].include` also grants no standing permission: an explicit invocation
must select exactly one target, and repeatable `--file` adds ad-hoc patterns for
that invocation only. Patterns expand on the source into sorted exact paths;
no glob crosses the wire.

```bash
dev fleet files api --to lab                    # report only
dev fleet files api --to lab --apply --yes      # create absent target files
dev fleet files api --to lab --replace --apply  # authorize differing bytes separately
```

The source and target must already resolve to one clone with the same normalized
**fetch** identity, attached branch, and exact commit. A push-only URL match is
insufficient. Both hosts independently prove every exact path is untracked and
ignored under their own Git configuration. Only regular files pass: no
symlinks/reparse points, directories, sockets, devices, FIFOs, `.git`, nested
repositories, or submodule boundaries. Compiled ceilings are 128 files,
8 MiB per file, 32 MiB total, with bounded path length/depth; host policy may
lower but never raise them. The source branch, HEAD, and fetch identity are
revalidated both before and after payload reads; target apply shares the
canonical Git-common-directory lease used by `fleet sync`.

Plan is the default and sends no file body. Apply additionally requires the
configured `machine_id` to match the content-free capability probe. Missing files
are atomically created owner-only; identical files are no-ops; differing bytes
block unless `--replace` was present in the displayed plan. Replacement binds the
observed target digest/mode, retains a private rollback copy, revalidates both
roots, and rolls back file changes on failure. `--yes` only answers confirmation
and never implies replacement. Public human/JSON output contains path, size,
mode, and state—never content or hashes.

The transaction journal is durable before manifest/payload staging, so an
interrupted request ID can resume or reconcile. Rollback intentionally leaves an
empty parent directory when its post-crash identity cannot be proven; deleting
another process's replacement would be worse. Native Windows payload transfer is
capability-blocked before content is sent.

This command is not repository/task ownership transfer, clone acquisition,
provisioning, backup, restore, or eviction. It never switches a branch, copies
task/catalog/note state, watches for changes, propagates deletions, or removes the
source.

`dev fleet` also registers bounded hidden capability/file commands in addition to
`_snapshot`, `_sync`, `_open-herdr`, and `_shell`. They exist only for the two
`dev` processes to invoke over SSH; they are wire protocols, not user-facing
surfaces.

## Transport and authentication

Every fleet command reaches a host by shelling out to the system `ssh` binary with `ConnectTimeout`, `ServerAliveInterval=15`, and `ServerAliveCountMax=2`, and first attempts `BatchMode=yes` — key or agent authentication only, exactly like a script-friendly `ssh` call. Fixed protocols use non-PTY `-T` and disable agent, X11, local-command, and all port forwarding while retaining the user's normal host-key policy. `dev fleet open` separately allocates a PTY (`-t`) for its interactive login shell.

If that attempt is rejected with "permission denied" and the host defines a `ssh_login_password_source`, `dev` retries once with password authentication. The password itself never touches argv or the environment: `dev` re-executes itself as a one-shot `SSH_ASKPASS` helper, and the secret is handed to that helper over an inherited file descriptor. `prompt` reads a hidden password from `/dev/tty` at run time; `plain` and `bitwarden` resolve it from the config file or from `bw get password <item>` respectively.

On the remote side, `dev_path = "auto"` (the default) searches common install locations (`~/.local/bin`, `~/go/bin`, mise shims, Homebrew/Linuxbrew, `/usr/local/bin`, `/snap/bin`) and then `PATH`, exiting `127` if no `dev` is found; an explicit absolute `dev_path` skips that search and execs it directly.

## What's cached vs. durable

`$XDG_CONFIG_HOME/dev/remotes.toml` is durable, user-authored configuration, on the same footing as `config.toml` — `dev fleet config init`/`edit` manage it, and nothing regenerates it automatically.

Each successful probe writes a per-host JSON snapshot under `$XDG_CACHE_HOME/dev/fleet/v1/<host-name-slug>.json`. That cache is a disposable accelerator, not durable data: it is fingerprinted with an "endpoint ID" derived from the host's connection fields and timeouts, so editing a host's `machine_id`, `ssh_alias`, `hostname`, `user`, `port`, `identity_file`, `dev_path`, or timeouts automatically invalidates its old cache the next time `dev` reads it. Oversized snapshots, future timestamps, invalid counts, and oversized/NUL-containing identity or path fields are ignored rather than displayed. `dev cache list` shows its path, size, and age; `dev cache clear fleet` (or `dev cache clear all`) removes it outright. There is nothing to "rebuild" explicitly — the next `dev fleet list`, `dev fleet status`, or TUI FLEET reload regenerates it from a fresh probe.

The cache exists so an unreachable, timed-out, incompatible, or invalid-response host can still report its last known state (marked `stale`, with `FromCache` set) instead of vanishing from the listing; `--cached` uses only this cache and never touches the network. Each remote host's own `config.toml`, task registry, and repository paths remain authoritative on that host — the cache only ever holds a read-only snapshot of them.

## Fleet in the TUI

FLEET is one of the TUI's six views (`TASKS`, `REPOS`, `FLEET`, `TRY`, `REMOTE`, `SKILLS`, switched with `tab`/`h`/`l`). Like REMOTE, it loads lazily — no live probe starts until the view is first opened — but its cache is decoded in the background after the initial TASKS view so a valid snapshot still within `defaults.cache_ttl` can seed the table. The TUI hides this machine by default because REPOS already provides its richer local inventory; `a` includes/hides local rows. The local-host snapshot reuses the current accepted REPOS generation instead of running repository/task/runtime discovery again; if that generation is still loading, FLEET keeps cached rows visible and says it is waiting. `r` supersedes an older request and forces a live reload of every configured host. The non-interactive `dev fleet list` output remains local plus remote.

Its table shows `HOST`, `STATE`, `REPO`, `BRANCH`, `GIT`, `LIVE`, `TASKS`, and `PATH`. `enter` opens the selected repository: an explicitly revealed local host row uses an ordinary local open; for a remote row `dev` prefers native Herdr remoting when that host's snapshot reports the `herdr` runtime and the host connects through `ssh_alias` with no password step, and otherwise falls back to an interactive SSH login shell in the repository's directory. Git changes are read-only in this view — FLEET is for inspecting and opening work, not editing it in place.

`e` opens `remotes.toml` in `$VISUAL`/`$EDITOR` — the file this view is about, rather than dev's own `config.toml`, which `e` opens everywhere else. On leaving the editor `dev` reparses the file before using it. A parse failure, an unknown field, or a permissions problem on a file holding a plaintext password is reported and the previous rows are kept, so a typo cannot silently drop a host from the fleet. A file that parses triggers an immediate live reload of every configured host.

## Degrading gracefully

Every host is probed independently, so one bad host never fails the fleet as a whole. Per-host states are `ok`, `stale` (cache reused, optionally with an error explaining why), `no-dev` (the remote has no `dev` on its `PATH` — reported, not an error), `unreachable` (SSH itself failed), `timeout`, `incompatible` (an old or unrecognized remote `dev`), and `invalid-response` (malformed snapshot JSON). `ok` and a clean `no-dev` never fail `--strict`; every other state does, including a `stale` result that carries an error.

Separately, forge integration is optional: `gh` and `glab` enable GitHub/GitLab
inventory, publication, and pull/merge requests, while `az` enables Azure
DevOps inventory and pull requests. `dev doctor` reports missing CLIs as
warnings. Local Git workflows remain available, but an explicit non-interactive
publication request fails with login/install guidance instead of silently
changing its meaning. Azure DevOps inventory is additionally opt-in and stays
disabled until `forge.azure_devops` targets are configured.

## Sources

- [`internal/cli/fleet.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet.go)
- [`internal/fleet/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/config.go)
- [`internal/fleet/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/types.go)
- [`internal/fleet/transport.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/transport.go)
- [`internal/fleet/protocol.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/protocol.go)
- [`internal/fleet/sync.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/sync.go)
- [`internal/localfiles`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/localfiles)
- [`internal/machineid`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/machineid)
- [`internal/cli/fleet_files.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet_files.go)
- [`internal/fleet/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/cache.go)
- [`internal/help/topics/fleet.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/fleet.md)
- [`internal/cli/tui.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/tui.go)
- [`internal/tui/model.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/model.go)
- [`internal/tui/view.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/view.go)
- [`internal/tui/rows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/rows.go)
- [`internal/cli/doctor.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/doctor.go)
- [`internal/cli/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/cache.go)
- [`internal/forge/forge.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/forge.go)
