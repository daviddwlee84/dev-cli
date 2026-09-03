---
description: Inventory repositories over POSIX or Windows SSH hosts using merged fleet configuration, pin remote identity, safely fast-forward branches, and explicitly transfer bounded ignored files.
authority: project
status: stable
verified_on: 2026-09-01
---

# Remote repository fleet

`dev fleet` fans out over SSH to other machines running their own `dev`, so you can inspect/open their repositories and safely propagate a branch without centralizing their paths, task registries, or runtime state.

[SSH host onboarding](ssh-hosts.md) is the optional entry point for discovering aliases, bootstrapping a public key, and explicitly generating a verified fleet registration. Fleet itself continues to accept user-authored profiles.

## What a fleet is

A fleet is a merged controller-side list of other hosts, each running its own
`dev` binary against its own `$XDG_CONFIG_HOME`, scan roots, task registry, and
runtime. Inventory stays decentralized: each host produces its own read-only
snapshot over SSH, and a missing remote `dev` or unreachable host degrades one
row rather than blocking the rest. Mutation is confined to narrowly allowlisted,
separately named commands such as `fleet sync` and `fleet files --apply`; neither
copies remote configuration back to the controller or makes it authoritative for
target paths, tasks, or runtime.

This differs from the REMOTE TUI view and repository publishing/PR flows. Those use authenticated `gh`/`glab`, plus Azure CLI for configured Azure DevOps inventory/PRs. Fleet talks to machines over ordinary OpenSSH about local checkouts on those machines.

## Two configuration ownership layers

```bash
dev fleet config init
dev fleet config edit
dev fleet config show
dev fleet config path
```

Fleet loads two durable layers:

1. **Primary user-authored config:** `$XDG_CONFIG_HOME/dev/remotes.toml`, or the root `--remotes <path>` override. `dev fleet config init` writes a starter (`--force` overwrites, `--stdout` prints); `config edit` opens only this file; `config path` prints only this path.
2. **Generated dev-owned fragments:** the sibling `remotes.d/ssh-<alias>.toml` files created by explicit `dev ssh setup <alias> … --fleet` after a fresh ordinary alias login succeeds. `dev ssh remove <alias> --fleet` is their removal owner.

Directory derivation is deterministic:

| Primary path | Generated directory |
|---|---|
| default `$XDG_CONFIG_HOME/dev/remotes.toml` | `$XDG_CONFIG_HOME/dev/remotes.d` |
| `--remotes /srv/dev/lab.toml` | `/srv/dev/lab.d` |
| `--remotes /srv/dev/lab` | `/srv/dev/lab.d` |

The primary file may be absent; valid generated fragments still load. The primary is decoded first, managed fragments load after it in lexical filename order, and defaults apply only after the merge. Loading never rewrites primary bytes or comments.

Every generated file has a fixed v1 header, `schema_version = 1`, and exactly one `[host]` containing only `name`, `ssh_alias`, and `remote_os`. Defaults, passwords, explicit hostname/user/port/identity, and arbitrary fields are forbidden. The directory/file must satisfy private Unix mode or protected Windows DACL and no-link/reparse ownership checks; drift is a conflict, not permission to overwrite.

`dev fleet config show` prints the effective merged config, redacts plaintext password values, and precedes generated entries with source/ownership comments directing edits to `dev ssh setup/remove`. `config edit` and the FLEET TUI `e` key still open only primary `remotes.toml`; they warn when generated entries are present.

## Primary host profiles

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
remote_os = "posix"
# Add only after `dev fleet machine-id lab` and independent verification.
machine_id = "00000000-0000-4000-8000-000000000000"

[[hosts]]
name = "winlab"
ssh_alias = "winlab"
remote_os = "windows"

[[hosts]]
name = "vps"
hostname = "203.0.113.10"
user = "dev"
port = 22
identity_file = "~/.ssh/id_ed25519"
remote_os = "posix"
ssh_login_password_source = { type = "bitwarden", item = "ssh-vps-login" }
```

`schema_version = 1` is required when the primary exists. `[defaults]` supplies
`connect_timeout`, `command_timeout`, `cache_ttl`, `max_parallel`, and `dev_path`;
each primary host inherits values it does not set. A host needs `name` plus either
`ssh_alias` (preferred because ordinary OpenSSH configuration retains `ProxyJump`,
`IdentityAgent`, and host-key policy) or `hostname` with optional `user`, `port`,
and `identity_file`.

`remote_os` accepts `posix` or `windows`; omission remains POSIX for backward
compatibility. It selects remote launcher and target path semantics. Optional
`machine_id` is a durable UUID pin: read-only inventory may proceed unpinned, but
portable-file apply requires an exact match. Run `dev fleet machine-id <host>`,
verify the UUID through an independent channel, then copy it into the primary
`remotes.toml`; the command reports `unpinned`, `match`, or `mismatch` and never
writes configuration.

`ssh_login_password_source.type` is `none` (default), `prompt`, `plain`, or
`bitwarden`. Fleet always tries key/agent BatchMode first, then retries a
permission-denied host only when a password source is configured. A primary
containing a plaintext password must be mode `0600` or loading fails. Generated
fragments cannot carry a password source or machine pin.

## Merge and collision rules

- Host names are globally unique across primary and generated layers.
- Existing primary profiles may continue to share one SSH alias; this preserves previously accepted configurations.
- An SSH alias collision becomes an error when any generated fragment participates. A generated registration cannot silently compete with a primary profile or another managed entry.
- Alias comparisons involving generated entries are case-insensitive.
- A malformed, insecure, manually edited, or noncanonical generated fragment blocks the merged load.
- Defaults are applied after all hosts are merged; generated fragments therefore inherit controller defaults without duplicating them.

Use `dev fleet config edit` to resolve a primary collision. Use `dev ssh setup <alias> … --fleet` to reconcile a valid generated fragment; do not hand-edit it.

## Explicit registration through `dev ssh`

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup winlab --key ~/.ssh/id_winlab --target-os windows \
  --fleet --fleet-name windows-builder
```

`--fleet` is never implied by alias discovery or successful SSH bootstrap. Setup writes the fleet fragment last, only after exact-key verification and a second fresh ordinary alias login succeed. The registered platform is the verified `--target-os`; managed Windows entries use `dev_path = "auto"`. A remote without `dev` is still a valid SSH onboarding result and appears as `no-dev` until installed.

A partial/unknown bootstrap, failed ordinary gate, or fleet-fragment collision leaves valid local SSH config/generated keys in place but skips registration. Rerun setup after remediation.

## `dev fleet` commands

| Command | Flags | Purpose |
|---|---|---|
| `dev fleet list` | `--host <name>` (repeatable), `--repo <query>`, `--json`, `--cached`, `--strict` | list repositories/activity across this machine and merged configured hosts |
| `dev fleet status` | `--json`, `--strict` | probe configured hosts and report snapshot health |
| `dev fleet machine-id <host>` | `--json` | show the observed durable UUID and compare the configured primary pin |
| `dev fleet sync <repo>` | `--push`, `--remote <name>`, `--host <name>` (repeatable), `--json` | optionally publish, then safely fast-forward clean matching checkouts |
| `dev fleet files [repo-or-path]` | `--to <host>`, `--file <pattern>` (repeatable), `--apply`, `--replace`, `--yes`, `--json` | plan or apply one-way transfer of explicit ignored files |
| `dev fleet open <host> <repo>` | — | open a remote repository through Herdr or an SSH login shell |
| `dev fleet config init` | `-f`/`--force`, `--stdout` | write/print starter primary `remotes.toml` |
| `dev fleet config edit` | `--editor <cmd>` | open only primary `remotes.toml` |
| `dev fleet config show` | — | print effective merged config with passwords redacted and generated ownership comments |
| `dev fleet config path` | — | print the primary config path |

`list --repo` filters name, remote identity, branch, or path. `--cached` avoids network activity. `--strict` turns unreachable/timeout/incompatible/invalid/stale-error hosts into a non-zero exit; a clean `no-dev` is informational. `sync` resolves the repository locally; without `--push`, source `HEAD` must already equal fetched upstream. `--remote` selects the cross-host Git identity (branch upstream remote, then `origin`, by default).

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

## POSIX and Windows transport

Every fleet command reaches a host through the controller's system `ssh` binary
with `ConnectTimeout`, `ServerAliveInterval=15`, and `ServerAliveCountMax=2`, and
first attempts `BatchMode=yes`—key or agent authentication only. Fixed protocols
use non-PTY `-T` and disable agent, X11, local-command, and all port forwarding
while retaining the user's normal host-key and known-hosts policy. `dev fleet
open` separately allocates a PTY (`-t`) for its interactive login shell.

A POSIX target receives the existing injection-safe shell launcher. With `dev_path = "auto"`, it checks common local user/package-manager locations and `PATH`, returning exit `127` when `dev` is absent. An explicit path is quoted and interpreted with POSIX target semantics, never expanded using the controller environment.

A Windows target receives `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand …`. The wrapper:

- decodes helper arguments as data and permits only `_snapshot`, `_sync`, content-free `_capability`, `_open-herdr`, and `_shell` shapes;
- rejects native `_files-plan` and `_files-apply` payload helpers before content is sent;
- locates `dev.exe` in common user install/shim locations or through `Get-Command` when `dev_path = "auto"`;
- returns `127` when no executable exists and propagates the remote dev exit code;
- does not consume stdin, so `_sync` and `_capability` receive their JSON requests unchanged;
- validates explicit Windows drive/UNC paths using target-OS semantics rather than the controller's `filepath` rules.

Generated Windows registrations require `dev_path = "auto"`. User-authored Windows profiles may use an absolute Windows target path. The encoded wrapper is a transport boundary, not permission to run arbitrary PowerShell supplied by a caller.

If BatchMode is denied and a primary profile has a password source, fleet retries
once. The password is not placed in SSH argv or the environment: a one-shot
self-executed `SSH_ASKPASS` helper receives it over a platform-specific inherited
descriptor or handle. `prompt` reads hidden terminal input; `plain` comes from the
protected primary config; `bitwarden` invokes `bw get password <item>`. SSH-host
bootstrap itself has no password backend—interactive setup leaves native prompts
to OpenSSH, while noninteractive setup stays batch-only.

## Cache versus durable state

| Data | Role |
|---|---|
| primary `remotes.toml` | durable user-authored fleet intent |
| sibling `remotes.d/ssh-<alias>.toml` | durable dev-owned fleet intent created/removed only by explicit SSH commands |
| each remote's config/tasks/repositories/runtime | host-local authority; never centralized |
| `$XDG_CACHE_HOME/dev/fleet/v1/*.json` | disposable controller snapshots |

A successful probe writes a private per-host JSON snapshot. Its endpoint ID includes `machine_id`, connection fields, SSH port, timeouts,
`dev_path`, and `remote_os`; changing the target invalidates stale cache identity. Oversized/malformed snapshots, future timestamps, invalid counts, and unsafe fields are ignored. `dev cache clear fleet` or `dev cache clear all` removes this cache; the next fleet request rebuilds it.

The cache lets an unavailable host retain last-known state as `stale`; `--cached` reads only it. It never becomes authoritative for remote paths or tasks.

## Fleet in the TUI

FLEET is one of the TUI's seven views (`TASKS`, `REPOS`, `FLEET`, `TRY`,
`REMOTE`, `SKILLS`, `MCP`, switched with `tab`/`h`/`l`). Like REMOTE, it loads
lazily—no live probe starts until the view is first opened—but valid cache is
decoded after the initial TASKS view. The TUI hides this machine by default
because REPOS provides richer local inventory; `a` toggles local rows. The local
snapshot reuses the accepted REPOS generation instead of rescanning, and cached
rows remain visible while that generation loads. `r` supersedes older work and
reloads every host from the merged primary-plus-generated configuration. The
non-interactive `dev fleet list` output remains local plus remote.

The table shows host/state/repository/branch/Git/runtime/task/path facts. Enter uses native Herdr remoting when an eligible POSIX-style profile reports Herdr and no password step is needed; otherwise it opens through SSH and a remote login shell. On a Windows controller, the local fallback starts a child `%COMSPEC%` shell because Windows has no `exec(2)`.

The `e` key opens only primary `remotes.toml`. After return, dev reparses the full primary-plus-generated merge. An invalid primary or generated fragment reports the error and preserves previous usable rows; a valid merge triggers a live reload.

## Safe branch propagation and degradation

```bash
dev fleet sync api --push
dev fleet sync api                 # HEAD must already equal fetched upstream
dev fleet sync api --host lab
```

The source must be clean and attached. Targets match normalized Git remote identity, not directory name. Each target fetches first; only a clean checkout on the same strictly-behind branch advances by fast-forward. Different branches are not switched. Dirty, ahead, divergent, ambiguous, or unreachable targets stay untouched and make sync non-zero. Hosts without `dev` or that repository are explicitly reported and ignored.

Per-host states are `ok`, `stale`, `no-dev`, `unreachable`, `timeout`, `incompatible`, and `invalid-response`. No automatic rebase, force push, background hook, or all-repository pull exists.

## Sources

- [`internal/cli/fleet.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet.go)
- [`internal/fleet/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/config.go)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/fleet/transport.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/transport.go)
- [`internal/fleet/protocol.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/protocol.go)
- [`internal/fleet/sync.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/sync.go)
- [`internal/localfiles`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/localfiles)
- [`internal/machineid`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/machineid)
- [`internal/cli/fleet_files.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet_files.go)
- [`internal/fleet/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/cache.go)
- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/help/topics/fleet.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/fleet.md)
