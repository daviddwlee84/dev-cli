# SSH hosts

Discover exact OpenSSH aliases, manage only dev-owned fragments, install public
keys without copying private material, and optionally register a freshly verified
alias in dev fleet. OpenSSH remains connection authority.

## Establish the managed Include

```bash
dev ssh init                 # report exact create/update/noop/blocked plan
dev ssh init --json          # one versioned plan object
dev ssh init --apply         # apply after interactive confirmation
dev ssh init --apply --yes   # confirm only the local plan non-interactively
```

The only root directive dev installs is `Include ~/.ssh/dev.d/*.conf`, before the
first Host, Match, or earlier Include. It preserves supported root metadata and
bytes outside the insertion. Unsafe metadata, links/reparse points, concurrent
changes, or foreign/drifted content in `dev.d` block without writing and include
manual placement guidance. Dev never removes the shared Include automatically.

## Discover, inspect, and probe

```bash
dev ssh list
dev ssh list --format tsv
dev ssh list --json
dev ssh show lab
dev ssh show lab --json
dev ssh probe lab
dev ssh probe lab --json
```

`list` and alias completion are static: they walk the bounded user Include
closure but never invoke ssh, a resolver, Match exec, an agent, or the network.
Exact aliases retain source/line, definition ownership, reachability/conflict,
and dev-fleet membership. Wildcard-only or unprovable declarations are
non-selectable diagnostics; incomplete closure stays explicit.

TSV emits one definition per row with six fields and no header: alias, status,
ownership, source, line, comma-separated fleet names. JSON is one versioned
object with definitions, provenance, completion state, and diagnostics.

`show` deliberately runs plain `ssh -G <alias>` so user and system configuration
participate. Configured resolver and Match exec behavior may run. `probe` performs
one fresh `BatchMode=yes` ordinary login with `-S none`; it preserves host-key,
known-hosts, KnownHostsCommand, and UpdateHostKeys policy.

## Configure only

```bash
dev ssh setup lab --hostname 192.0.2.20 --user dev --config-only
dev ssh setup lab --hostname 198.51.100.20 --config-only --yes
dev ssh setup lab --hostname 203.0.113.20 --dry-run
```

Unknown portable lowercase aliases may become one canonical
`~/.ssh/dev.d/<alias>.conf`. Allowed fields are `--hostname`, `--user`, `--port`,
`--proxy-jump`, `--identity-file`, and `--identities-only`. Existing managed
aliases reconcile only their canonical fragment. Foreign aliases remain
read-only, so every connection-field flag is rejected for them.

`--config-only` cannot be combined with key, route, bootstrap, or fleet flags.
`--dry-run` never writes, generates, runs `ssh -G`, touches known_hosts, or
contacts a remote; route/install state remains unknown.

## Bootstrap an explicit public key

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix
dev ssh setup lab --generate-key --target-os posix
dev ssh setup winlab --hostname 198.51.100.30 --generate-key \
  --key-path ~/.ssh/id_winlab --target-os windows --fleet
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --dry-run --json
```

Full non-dry-run setup requires exactly one explicit `--key` or `--generate-key`.
`--key` accepts a validated `.pub`, identity with a companion `.pub`, or security
key stub with a companion. A missing public companion is derived with confirmed
`ssh-keygen -y`; encrypted noninteractive derivation fails instead of accepting a
passphrase in argv/environment.

Generation is Ed25519 through native ssh-keygen. `--key-path` and `--comment` are
optional; default identity is `~/.ssh/id_ed25519_dev`. Interactive generation
uses ssh-keygen's hidden passphrase prompts. Noninteractive generation requires
`--no-passphrase`. Both halves are validated and installed no-replace; generated
keys survive later failures and are never removed by `ssh remove`.

Route/platform flags are:

```text
--target-os posix|windows
--hop-os alias=posix|windows       repeatable
--install-on-working-jump
--windows-admin-authorized-keys
```

Nested/comma ProxyJump routes are flattened outermost-first. Cycles,
ProxyCommand/URI forms, and ambiguous hops are rejected. Working jump hosts are
not changed unless `--install-on-working-jump` is explicit. Every proof disables
connection sharing.

POSIX uses a fixed `sh` installer for `~/.ssh/authorized_keys`. Windows standard
accounts use `%USERPROFILE%\.ssh\authorized_keys`; administrator-group accounts
require `--windows-admin-authorized-keys` before targeting the shared
`%ProgramData%\ssh\administrators_authorized_keys`. Protected ACLs and reparse
checks are enforced; elevation may still require manual remediation.

Dev sends exactly one bounded public record on stdin. It never copies private
keys, supplies a password backend, accepts a host key through `--yes`, or weakens
known-hosts policy. Interactive mode leaves native credential/host-key prompts to
OpenSSH; JSON and non-TTY modes are batch-only. Noninteractive full setup also
requires `--target-os`, and local writes require `--yes`.

## Explicit fleet registration and partial outcomes

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix \
  --fleet --fleet-name build-lab
```

`--fleet-name` requires `--fleet`. Registration is written last, only after exact
selected-key proof and a separate fresh ordinary alias login succeed. The default
fragment is `$XDG_CONFIG_HOME/dev/remotes.d/ssh-<alias>.toml`; a custom
`--remotes /srv/dev/lab.toml` uses `/srv/dev/lab.d`. It contains only name,
ssh_alias, and verified remote_os. A missing remote dev is allowed and later
appears as fleet `no-dev`.

If a remote installer has started, timeout/cancellation/failure is `unknown`
because the key may already be present. Local managed config and generated keys
stay in place, completed hops are reported, later hops/fleet registration are
skipped, and rerunning converges. Dev does not attempt remote revocation rollback.

## Remove owned intent

```bash
dev ssh remove lab --dry-run
dev ssh remove lab --yes
dev ssh remove lab --fleet --yes
dev ssh remove lab --fleet --dry-run --json
```

Removal requires a structurally valid expected dev-owned host fragment. If its
generated fleet fragment exists, omission of `--fleet` blocks; the flag removes
that fragment first. A reference in primary user-authored `remotes.toml` always
blocks and points to `dev fleet config edit`.

Removal never deletes keys, known_hosts entries, the shared Include, remote
authorized_keys, or foreign config. Rotation/revocation, alias rename/adoption,
arbitrary directives, ProxyCommand automation, custom AuthorizedKeysFile,
password storage, bulk onboarding, and an SSH TUI are outside this release.

## Structured output

Every SSH `--json` form writes exactly one safe schema-versioned object on stdout;
operational failures still return one result with stable kind/status/action/code
fields. Plans/results may include paths, modes, digests, fingerprints, and
per-hop/fleet state, but never private bytes, passwords/passphrases, complete
public-key lines, agent payloads, or raw command-like SSH options. Diagnostics and
child progress stay on stderr.
