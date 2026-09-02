---
description: Inventory repositories, tasks, and runtime activity across POSIX or Windows SSH hosts using merged user-authored and dev-managed fleet configuration.
authority: project
status: stable
verified_on: 2026-09-01
---

# Remote repository fleet

`dev fleet` fans out over SSH to other machines running their own `dev`, so you can inspect/open their repositories and safely propagate a branch without centralizing their paths, task registries, or runtime state.

[SSH host onboarding](ssh-hosts.md) is the optional entry point for discovering aliases, bootstrapping a public key, and explicitly generating a verified fleet registration. Fleet itself continues to accept user-authored profiles.

## What a fleet is

A fleet is a merged controller-side list of other hosts. Each remote runs its own `dev` against its own XDG config, scan roots, task registry, and runtime. Fleet asks for read-only snapshots and sends narrowly allowlisted sync/open helpers; it does not copy the remote configuration into the controller. A missing remote `dev` or unreachable host degrades one row rather than blocking other hosts.

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

`schema_version = 1` is required when the primary exists. `[defaults]` supplies timeouts, cache TTL, fan-out concurrency, and `dev_path`; each primary host inherits values it does not set. A host needs `name` plus either `ssh_alias` (preferred because ordinary OpenSSH configuration retains `ProxyJump`, `IdentityAgent`, and host-key policy) or `hostname` with optional `user`, `port`, and `identity_file`.

`remote_os` accepts `posix` or `windows`; omission remains POSIX for backward compatibility. It selects the remote command launcher and target path semantics, and participates in endpoint cache identity.

`ssh_login_password_source.type` is `none` (default), `prompt`, `plain`, or `bitwarden`. Fleet always tries key/agent BatchMode first, then retries a permission-denied host only when a password source is configured. A primary containing a plaintext password must be mode `0600` or loading fails. Generated fragments cannot carry any password source.

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
| `dev fleet sync <repo>` | `--push`, `--remote <name>`, `--host <name>` (repeatable), `--json` | optionally publish, then safely fast-forward clean matching checkouts |
| `dev fleet open <host> <repo>` | — | open a remote repository through Herdr or an SSH login shell |
| `dev fleet config init` | `-f`/`--force`, `--stdout` | write/print starter primary `remotes.toml` |
| `dev fleet config edit` | `--editor <cmd>` | open only primary `remotes.toml` |
| `dev fleet config show` | — | print effective merged config with passwords redacted and generated ownership comments |
| `dev fleet config path` | — | print the primary config path |

`list --repo` filters name, remote identity, branch, or path. `--cached` avoids network activity. `--strict` turns unreachable/timeout/incompatible/invalid/stale-error hosts into a non-zero exit; a clean `no-dev` is informational. `sync` resolves the repository locally; without `--push`, source `HEAD` must already equal fetched upstream. `--remote` selects the cross-host Git identity (branch upstream remote, then `origin`, by default).

Four hidden helpers—`_snapshot`, `_sync`, `_open-herdr`, and `_shell`—form the remote wire surface. Users should not invoke them directly.

## POSIX and Windows transport

Every fleet operation shells out to the controller's system `ssh` with connection/server-alive bounds and starts with `BatchMode=yes`. `fleet open` allocates a PTY for its interactive login shell. No fleet launcher weakens the user's host-key or known-hosts policy.

A POSIX target receives the existing injection-safe shell launcher. With `dev_path = "auto"`, it checks common local user/package-manager locations and `PATH`, returning exit `127` when `dev` is absent. An explicit path is quoted and interpreted with POSIX target semantics, never expanded using the controller environment.

A Windows target receives `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand …`. The wrapper:

- decodes helper arguments as data and permits only the four hidden fleet command shapes;
- locates `dev.exe` in common user install/shim locations or through `Get-Command` when `dev_path = "auto"`;
- returns `127` when no executable exists and propagates the remote dev exit code;
- does not consume stdin, so `_sync` receives its JSON request unchanged;
- validates explicit Windows drive/UNC paths using target-OS semantics rather than the controller's `filepath` rules.

Generated Windows registrations require `dev_path = "auto"`. User-authored Windows profiles may use an absolute Windows target path. The encoded wrapper is a transport boundary, not permission to run arbitrary PowerShell supplied by a caller.

If BatchMode is denied and a primary profile has a password source, fleet retries once. The password is not placed in SSH argv or the environment: a one-shot self-executed `SSH_ASKPASS` helper receives it over an inherited descriptor. `prompt` reads hidden terminal input; `plain` comes from the protected primary config; `bitwarden` invokes `bw get password <item>`. SSH-host bootstrap itself has no password backend—interactive setup leaves native prompts to OpenSSH, while noninteractive setup stays batch-only.

## Cache versus durable state

| Data | Role |
|---|---|
| primary `remotes.toml` | durable user-authored fleet intent |
| sibling `remotes.d/ssh-<alias>.toml` | durable dev-owned fleet intent created/removed only by explicit SSH commands |
| each remote's config/tasks/repositories/runtime | host-local authority; never centralized |
| `$XDG_CACHE_HOME/dev/fleet/v1/*.json` | disposable controller snapshots |

A successful probe writes a private per-host JSON snapshot. Its endpoint ID includes connection fields, SSH port, timeouts, `dev_path`, and `remote_os`; changing the target invalidates stale cache identity. Oversized/malformed snapshots, future timestamps, invalid counts, and unsafe fields are ignored. `dev cache clear fleet` or `dev cache clear all` removes this cache; the next fleet request rebuilds it.

The cache lets an unavailable host retain last-known state as `stale`; `--cached` reads only it. It never becomes authoritative for remote paths or tasks.

## Fleet in the TUI

FLEET is one of six views (`TASKS`, `REPOS`, `FLEET`, `TRY`, `REMOTE`, `SKILLS`). It loads live remotes lazily while decoding valid cache after the initial view. This machine is hidden by default because REPOS has richer local data; `a` toggles it. The local row reuses the accepted REPOS generation rather than rescanning. `r` supersedes prior work and refreshes all merged configured hosts. Noninteractive `dev fleet list` remains local plus remote.

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
- [`internal/fleet/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/cache.go)
- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/help/topics/fleet.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/fleet.md)
