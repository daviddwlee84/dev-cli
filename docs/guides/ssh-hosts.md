---
description: Discover OpenSSH aliases, manage only dev-owned host fragments, bootstrap public-key access, and explicitly register verified hosts in dev fleet.
authority: project
status: stable
verified_on: 2026-09-01
---

# SSH host onboarding

`dev ssh` adds a conservative lifecycle around the OpenSSH configuration you already use. OpenSSH remains connection authority; dev provides static provenance, a small owned fragment namespace, public-key bootstrap, fresh authentication proofs, and an explicit bridge into [dev fleet](remote-fleet.md).

## Authority and ownership

| Surface | Authority / owner | What dev may do |
|---|---|---|
| `~/.ssh/config`, its foreign Includes, and foreign `Host`/`Match` blocks | user + OpenSSH | read statically; evaluate an alias only through plain `ssh -G`; never rewrite |
| `Include ~/.ssh/dev.d/*.conf` in the root config | dev, after explicit `ssh init --apply` | install once before the first `Host`, `Match`, or earlier Include; never remove automatically |
| `~/.ssh/dev.d/<alias>.conf` | `dev ssh setup/remove` | create, reconcile, or remove only canonical v1 files with an allowlisted single `Host` block |
| local key files | user + native `ssh-keygen` | validate an explicit key, derive a confirmed missing `.pub`, or generate a no-replace Ed25519 pair; never copy private bytes |
| remote `authorized_keys` | remote OpenSSH account | append one bounded normalized public record idempotently; never remove or revoke it |
| primary `remotes.toml` | user via `dev fleet config` | read and merge; never rewrite during SSH setup |
| sibling `remotes.d/ssh-<alias>.toml` | `dev ssh setup/remove --fleet` | create only after a fresh ordinary login; remove only when explicitly requested |

A foreign alias remains usable for `list`, `show`, `probe`, key bootstrap, and fleet registration, but connection flags are rejected during `setup`. Dev will not compete with its existing definition. New dev-managed aliases use a portable lowercase exact-name grammar and one deterministic file containing `HostName`, optional `User`, `Port`, `ProxyJump`, `IdentityFile`, and `IdentitiesOnly`. Arbitrary directives and wildcard/`Match` blocks are not managed.

## Command map

| Command | Exact local flags | Boundary |
|---|---|---|
| `dev ssh init` | `--apply`, `--yes`, `--json` | plan by default; only `--apply` may install the dedicated Include |
| `dev ssh list` | `--json` or `--format tsv` | bounded static user-config scan; no subprocess or network |
| `dev ssh show <alias>` | `--json` | static definitions plus effective values from plain `ssh -G <alias>` |
| `dev ssh setup <alias>` | connection, key, route, fleet, plan, confirmation, and JSON flags listed below | owned local config, public-key bootstrap, optional fleet registration |
| `dev ssh probe <alias>` | `--json` | one fresh ordinary BatchMode login with sharing disabled |
| `dev ssh remove <alias>` | `--fleet`, `--dry-run`, `--yes`, `--json` | remove only canonical dev-owned SSH/fleet fragments |

`dev doctor` also reports the local `ssh`/`ssh-keygen` capabilities, static Include reachability, managed namespace permissions/ACLs, and generated fleet-fragment health. It does not run `ssh -G`, contact a host, or repair anything.

## One-time initialization is report-before-apply

```bash
dev ssh init
dev ssh init --json
dev ssh init --apply
dev ssh init --apply --yes
```

Without `--apply`, init reports the root path, managed directory, exact Include, and `create`/`update`/`noop`/`blocked` action without writing. `--yes` is valid only with `--apply`; it confirms the local plan but supplies no credential and accepts no host key.

The only inserted directive is:

```sshconfig
Include ~/.ssh/dev.d/*.conf
```

Dev preserves root bytes outside the insertion, BOM/newline style, and supported Unix metadata or Windows owner/DACL. It rejects unsafe path components, links/reparse points, special files, hardlinks, concurrent source changes, unrepresentable metadata, and foreign or drifted content in `dev.d`. On a blocked plan it makes no change and reports where to place the exact Include manually.

## Static list versus OpenSSH or network evaluation

```bash
dev ssh list
dev ssh list --format tsv
dev ssh list --json
dev ssh show lab
dev ssh show lab --json
dev ssh probe lab
```

`list` and SSH alias completion walk the active user Include closure rooted at `~/.ssh/config`. They do **not** run `ssh`, a resolver, `Match exec`, an SSH agent, or the network. The scanner follows bounded lexical Include expansion, records source line and Include provenance, and classifies each exact positive alias as active, inactive, unknown, or conflicting. Wildcard-only declarations are collision diagnostics rather than selectable aliases. Dynamic/unsupported guards, cycles, limits, and unprovable Include behavior set `complete: false`; uncertain declarations are never promoted to usable hosts.

`--format tsv` emits one row per discovered definition, with no header and these six tab-separated fields:

```text
alias  status  ownership  source  line  comma-separated-fleet-names
```

Fields are sanitized to one physical line. The selector contract is intentionally small; use JSON for full definition/provenance/diagnostic data.

`show` first retains the static definitions and then deliberately runs plain `ssh -G <alias>`—without replacing the user's config with `-F` and without parsing unstable `ssh -vv` prose. This includes system configuration and OpenSSH's scalar/additive semantics, but configured resolver behavior and user-authored `Match exec` may run.

`probe` crosses the network. It runs one ordinary alias login equivalent to a fresh `ssh -S none -o BatchMode=yes …`, so an existing ControlMaster cannot produce a false success. It does not override `StrictHostKeyChecking`, `UserKnownHostsFile`, or related policy. User-configured `KnownHostsCommand`, `UpdateHostKeys`, resolver, and `Match exec` behavior may still run.

## Setup modes and flags

Setup handles three alias classes:

1. **new:** requires `--hostname` outside the interactive HostName prompt and creates one dev-owned fragment;
2. **managed:** reconciles only the existing canonical fragment;
3. **foreign:** preserves all connection policy; any connection-field flag blocks the operation, but explicit key bootstrap and `--fleet` may proceed.

Connection fields for a managed alias are:

```text
--hostname --user --port --proxy-jump --identity-file --identities-only
```

Operational flags are:

```text
--config-only
--key <public-or-identity-path>
--generate-key [--key-path <identity>] [--comment <text>] [--no-passphrase]
--target-os <posix|windows>
--hop-os <alias=posix|windows>       # repeatable
--install-on-working-jump
--windows-admin-authorized-keys
--fleet [--fleet-name <name>]
--dry-run --yes --json
```

`--config-only` cannot be combined with key, route, bootstrap, or fleet flags. It stops after local managed-config publication/verification; for a foreign alias it performs plain `ssh -G` verification only.

`--dry-run` is side-effect-free: it does not generate keys, write files, run `ssh -G`, touch `known_hosts`, probe the network, or start a remote installer. Remote and route actions remain honestly `unknown`. It may perform bounded local reads needed to validate an explicitly named key or existing config. `--fleet` in a dry run still requires `--target-os` so the proposed fragment is determinate.

Full non-dry-run setup requires exactly one explicit `--key` or `--generate-key`. JSON mode is noninteractive even on a terminal; any noninteractive full setup also requires `--target-os`, and local mutation requires `--yes`. `--yes` only approves the local plan. Native OpenSSH still owns password/passphrase and host-key interaction, and batch mode returns `interaction_required` rather than inventing a credential path.

## Existing keys and generation

```bash
# Create only a local managed alias.
dev ssh setup lab --hostname 192.0.2.20 --user dev --config-only

# Bootstrap with an existing identity or public file.
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix

# Generate an Ed25519 pair at the default ~/.ssh/id_ed25519_dev path.
dev ssh setup lab --generate-key --target-os posix

# A fully specified noninteractive generation.
dev ssh setup winlab --hostname 198.51.100.30 \
  --generate-key --key-path ~/.ssh/id_winlab --no-passphrase \
  --target-os windows --yes
```

`--key` accepts a validated `.pub` record, a private identity with a companion `.pub`, or a security-key stub with a companion `.pub`. If an identity lacks its public companion, dev asks before running `ssh-keygen -y`; `--yes` supplies that local confirmation for a script. Encrypted noninteractive derivation fails with `interaction_required` rather than placing a passphrase in argv or the environment.

`--generate-key` invokes native `ssh-keygen` for Ed25519. Interactive mode delegates hidden passphrase prompts to it. Noninteractive generation requires explicit `--no-passphrase`. Both halves are generated under a private staging basename, matched by fingerprint, hardened, and published with no-replace semantics; any destination collision blocks rather than overwrites. A successfully generated pair is retained after later route/bootstrap/fleet failure and is never removed by `dev ssh remove`.

All output is content-safe: fingerprints, algorithms, paths, digests, and booleans may appear; private bytes, passphrases, passwords, complete public-key lines, agent payloads, and unredacted command-like SSH options do not.

## ProxyJump and remote operating systems

Dev evaluates plain `ssh -G` for the target and each discovered jump, flattens nested/comma-separated `ProxyJump` routes outermost-first, and supports alias, `user@alias`, `alias:port`, and bracketed IPv6 forms. It rejects cycles, repeated hops, unsupported URI/`ProxyCommand` routes, and ambiguous overrides instead of guessing.

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix \
  --hop-os bastion=posix --hop-os winjump=windows
```

`--target-os` applies to the final target. Use repeatable `--hop-os` for ambiguous jumps; interactive mode can ask, while noninteractive mode requires every unknown hop to be specified. Each hop is probed with ordinary fresh BatchMode authentication first. A jump that already works is not modified unless `--install-on-working-jump` is explicit.

For a POSIX hop, a constant `sh` installer validates/creates `~/.ssh/authorized_keys`, applies `0700`/`0600`, and idempotently appends exactly one public record received on stdin. For Windows OpenSSH:

- a standard account uses `%USERPROFILE%\.ssh\authorized_keys` with protected current-user + SYSTEM ACLs;
- an administrator-group account requires explicit `--windows-admin-authorized-keys` before dev targets `%ProgramData%\ssh\administrators_authorized_keys`, with SYSTEM + BUILTIN\Administrators ACLs;
- dev detects group membership, rejects reparse targets, and never automates a UAC/elevation grant. Elevation or a non-default server policy can require manual remediation.

The system `ssh` binary is used on macOS, Linux, and Windows controllers. PowerShell is a target capability for Windows installers, not a controller-side host database or password backend.

## Fresh proofs, partial outcomes, and fleet registration

For every route hop, setup performs an ordinary fresh probe, an exact selected-key proof, any required public-key installation, another exact-key proof, and a separate ordinary alias gate. Every proof uses `-S none`; exact proofs add `IdentitiesOnly=yes` and the selected identity.

Remote installation cannot be made transactional. Once an installer starts, timeout/cancellation or a non-zero result is reported as `unknown` because the remote may already have appended the public key. Dev never attempts unsafe compensating deletion. Local managed config and a generated key pair remain in place, completed hop facts are returned, later hops and fleet registration are skipped, and rerunning setup converges.

Fleet registration is always opt-in:

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup winlab --key ~/.ssh/id_winlab --target-os windows \
  --fleet --fleet-name windows-builder
```

Only after the final fresh ordinary alias login succeeds does dev write the generated sibling fragment. The default primary path uses `$XDG_CONFIG_HOME/dev/remotes.d/ssh-<alias>.toml`; `--remotes /srv/dev/lab.toml` uses `/srv/dev/lab.d`, and a non-`.toml` path gains `.d`. The fragment contains only `name`, `ssh_alias`, and verified `remote_os`; it carries no password or alternate connection policy. A missing remote `dev` does not invalidate SSH onboarding and later appears as fleet `no-dev`.

## Structured output

All public SSH JSON is exactly one schema-versioned object on stdout. Operational failures still emit that one safe result object; CLI syntax/usage errors do not emit a partial JSON document. Diagnostics and child progress go to stderr.

| Command | `kind` values | Notable fields |
|---|---|---|
| `ssh init --json` | `ssh_init_plan`, `ssh_init_result` | `status`, source-bound `plan`, optional `result`, `error_code` |
| `ssh list --json` | `ssh_list` | `complete`, root/include state, aliases, definitions, fleet membership, diagnostics |
| `ssh show --json` | `ssh_show` | alias status, definitions, safe effective subset, fleet membership |
| `ssh setup --json` | `ssh_setup_plan`, `ssh_setup_result` | alias class, local/key/bootstrap plans/results, per-hop state, fleet action, partial/error code |
| `ssh probe --json` | `ssh_probe` | safe `ready`/`not_ready` status, code, exit code |
| `ssh remove --json` | `ssh_remove_plan`, `ssh_remove_result` | owned plan/result, explicit fleet action, status/error code |

Consumers should branch on `schema_version`, `kind`, machine-readable `status`/`action`/`code`, and honest `partial`/`unknown` state rather than parse human tables or stderr.

## Removal limits

```bash
dev ssh remove lab --dry-run
dev ssh remove lab --yes
dev ssh remove lab --fleet --yes
```

Removal accepts only a portable managed alias whose expected file is still canonical, secure, and structurally dev-owned. If its generated fleet fragment exists, omission of `--fleet` blocks instead of silently deleting a second durable intent; with the flag, fleet removal happens first. A reference in primary user-authored `remotes.toml` always blocks removal and points to `dev fleet config edit`. Manual drift, links/reparse points, changed sources, unsafe metadata, and ambiguous ownership also fail closed.

Removal never deletes the shared Include, local private/public key files, `known_hosts`, remote `authorized_keys`, or foreign configuration.

## Security boundary and deferred scope

Implemented safety properties include source-bound plans, no-follow/reparse checks, private Unix modes or protected Windows DACLs, atomic/no-replace publication, concurrent-source revalidation, post-write plain-`ssh -G` verification for managed config, rollback only while the just-written local identity is still provably owned, process-group/Job-Object cancellation, and material-safe structured output.

Deliberately deferred:

- key rotation, expiry, revocation, or deletion from remote `authorized_keys`;
- deletion of local keys or `known_hosts` repair/removal;
- alias rename/adoption, managed wildcards/`Match`, arbitrary SSH directives, or an SSH config editor;
- automated `ProxyCommand`, certificates/CAs, forwarding, custom `AuthorizedKeysFile`, or forced-shell policy;
- password/vault storage, automatic password fallback, private-key copying, direct Bitwarden integration, or weakened host-key checks;
- bulk/cloud/Tailscale/chezmoi fleet import, a dedicated SSH TUI, or background probing.

When a server policy falls outside the verified POSIX/Windows installer contract, dev reports manual remediation rather than silently weakening it.

## Sources

- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/sshhost`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/sshhost)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/help/topics/ssh.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/ssh.md)
