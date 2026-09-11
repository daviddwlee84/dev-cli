# SSH hosts

Discover exact OpenSSH aliases, configure dev-owned fragments, install public
keys without copying private material, and optionally register a freshly verified
alias in dev fleet. OpenSSH remains connection authority.

## Establish the managed Include

```bash
dev ssh init                 # report exact create/update/noop/blocked plan
dev ssh init --json          # one versioned plan object
dev ssh init --apply         # apply after interactive confirmation
dev ssh init --apply --yes   # confirm only the local plan non-interactively
```

The only root directive `ssh init` installs is `Include ~/.ssh/dev.d/*.conf`, before the
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
read-only to setup, so every connection-field flag is rejected for them.

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

## Machine management and configuration organization

`dev ssh` opens a menu on a terminal; pipes retain the command help. The default
configuration-cleanup choice is formatting. Group restructuring is a separate,
explicit operation.

```bash
dev ssh manage                          # joint inventory and multi-select wizard
dev ssh manage --json                   # local inventory; no SSH authentication
dev ssh manage --action register --alias lab --alias build --to both --target-os posix
dev ssh manage --action register --alias lab --to herdr --herdr-session agents --apply --yes
dev ssh manage --action rename --fleet-host lab --name workstation --apply
dev ssh manage --action disable --herdr-profile <profile-id> --apply
dev ssh manage --action remove --fleet-host lab --fleet-host build --apply

dev ssh format                          # four-space indentation preview
dev ssh format --indent 2 --file ~/.ssh/config.d/work/lab.conf
dev ssh format --apply                  # confirm local changes and retain recovery
dev ssh organize                        # select complete Host blocks and assign groups
dev ssh organize --group lab=work --group nas=personal --numbered --json
dev ssh restore <receipt>               # preview undo; --apply confirms it
```

Explicit management actions and file transformations are plan-only until
`--apply`; noninteractive apply requires `--yes`. The wizard previews all selected
operations before confirmation. `manage --json` reports a versioned joint
inventory, or a plan/result when `--action` is present. Registration plans expose
`fleet_registration` with the desired name, SSH alias and target OS. Existing `ssh list`
JSON/TSV remains unchanged and invokes no subprocess; `manage` additionally calls
local `herdr machine list --json`, but listing/planning never runs SSH or Match
exec. Failed or unsupported provider reads stay unavailable, rather than becoming
an empty, authoritative inventory.

SSH aliases, fleet profile names and Herdr labels are distinct. New names default
to the selected alias; existing custom names remain intact. One-alias registration
accepts `--fleet-name` and `--herdr-label`. `Host a b` remains one configuration
block with two selectable aliases; choosing one does not remove the other. Do not
merge aliases by IP: aliases may intentionally select different SSH options.
Herdr membership is matched by exact SSH target and explicit session; changes
use the native profile ID. Existing disabled profiles stay disabled during
registration. Use an explicit enable action to reconnect them.

Herdr 0.9.0 or a compatible machine CLI is optional. Its native `machine add`
prepares a remote installation and starts the selected server before saving;
open clients then connect automatically. The plan lists these effects. Native
installation/server-replacement confirmations remain Herdr's, even with dev
`--yes`. Missing approvals in a noninteractive run fail without inventing a saved
profile. Remove/disable only affects registration/client connections; remote
sessions keep running. Remote servers must meet Herdr's Linux/macOS requirements.
Fleet remains the repo/task inventory authority, and its existing remote-open
behavior is unchanged.

Already-working aliases can join fleet after a fresh ordinary login without
reinstalling keys. Each requested provider action gets a result, and failures
retain completed actions. Retry compares current membership before adding.
Fleet rename/remove supports canonical generated fragments and normal primary
`[[hosts]]` tables, preserving comments, child tables and unrelated bytes. Bulk
removals in one source are coalesced. Unsupported inline host layouts require a
native edit; removal never deletes SSH definitions or remote repositories.

Formatting changes leading indentation only (four spaces by default; `2` or
`tab` are supported), preserving option values, quotes, order and line endings.
Select the root config or explicit user files under `config.d`; dev/provider-owned
fragments remain with their owner. Previews redact command-like/sensitive lines.

Organization moves complete Host blocks and adjacent leading comments into
`~/.ssh/config.d/<group>/<host>.conf`. Multi-alias blocks remain together. Root
Includes are explicit and retain original evaluation order, including interleaved
groups. Filenames are unnumbered by default; `--numbered` adds original ordinals
for readability. `--group alias=group` or `--group @block-id=group` assigns blocks;
unassigned blocks retain their group or use `ungrouped`. Re-running the wizard
can move existing grouped fragments without opening several editors. Single-block
fragments retain their existing basename during an unnumbered regroup.

The organizer handles inline root Host blocks and directly included grouped
fragments. Foreign Includes retain their position. Match, dynamic/incomplete
Includes and repeated selected references require manual organization; formatting
remains available. Existing dormant files are not activated by a new broad glob.
New files or directories covered by an existing Include glob are blocked because
they would be read before the root switch. Existing destination files are not
silently overwritten.

File plans are bound to original bytes/identities and cooperative owner locks.
Apply creates new fragments before switching the root, then removes retired
sources. Interruptions leave a private recovery receipt under
`$XDG_DATA_HOME/dev/ssh-recovery/` (or the XDG default), outside Git and outside
cache. `restore` reverses verified completed steps and rejects subsequent user edits or
file replacement. A hard crash before an after-state is recorded requires manual
reconciliation using the retained private originals.
Representable metadata is retained; security attributes, unsupported inode flags,
unsafe links or writable-by-others ancestors require native handling. New local
file writes require the macOS/Linux backend. Herdr's external CLI and raw editors
remain outside dev's cooperative locks.

These explicit format/organize/management operations are the only exceptions to
setup/remove's narrow ownership rules. Normal setup never rewrites foreign SSH
connection definitions. Key vault import and hardware-key provisioning are
separate future work; private key bytes are not part of machine registration.

## Layered connection diagnosis

```bash
dev ssh diagnose lab
dev ssh diagnose 192.0.2.30 --json
dev ssh diagnose lab --compare-qos --timeout 60s
```

Use explicit diagnosis when TCP connectivity and SSH behavior disagree, or when
an ordinary probe cannot explain a failure. Unlike `probe`, this accepts an alias,
hostname or IPv4/IPv6 literal without requiring a uniquely discovered Host block.
The menu also offers diagnosis. Existing `probe` behavior and JSON are unchanged.

The report separates effective config, DNS, route/interface, TCP, SSH banner,
handshake, host identity, authentication and remote exit. Native route collectors
use macOS `route -n get`, Linux `ip -j route get`, and Windows `Find-NetRoute` through
a fixed noninteractive PowerShell program. No elevation, tool installation,
network/configuration repair, known_hosts update or host-key import is performed.
`ssh -G` and the configured connection can still execute user-authored Match exec,
proxy and key helpers. A route observation is not proof of a working VPN.

The total deadline defaults to 60 seconds. Configuration and DNS have 5-second
caps, route observations share 10 seconds, TCP shares 5 seconds, banner reading
has 3 seconds, and each SSH attempt has 15 seconds. Up to four resolved addresses
are observed. The total deadline always wins; cancellation preserves completed
stages. Missing tools, unavailable scope/binding support, and unrecognized or
truncated evidence stay unknown/unsupported. Proxied targets skip direct local
DNS/route/TCP/banner tests rather than bypassing the configured proxy.

Diagnostic SSH uses a fresh connection, BatchMode, strict host-key checking,
no host-key updates, no forwarding or local command, and a fixed remote `exit 0`.
A host-key rejection is distinct from authentication denial; successful
TCP/banner checks alone do not prove login. An authenticated connection with a
failed remote exit is a session failure. The diagnosis may therefore stop at a
stricter host-key boundary than an ordinary login configured to accept new keys.

`--compare-qos` allows one fresh `IPQoS=none` comparison after a transport timeout.
It requires positive client marking evidence, unchanged effective configuration
and observed route, and matching endpoints. Stock Windows OpenSSH may accept the
option while providing no marking capability; the report marks that comparison
unavailable. A changed result is correlation, not attribution to a VPN vendor or
proof of zero DSCP on the wire. No global QoS change or raw DSCP socket probe occurs.

JSON uses `schema_version: 1`, `kind: ssh_diagnosis`, and `privacy: local`.
It contains the selected endpoint/user/identity paths, ordered stage codes,
bounded route observations, attempts and next-action codes, but never raw debug
logs, server banners, config comments or proxy command strings. Do not publish
this local JSON directly. Feedback uses a separate allowlisted public projection.
Operational failure still emits one JSON document and a nonzero exit status.

Failed proxy connections keep final-target stages unknown: a helper may print
its own successful jump-host authentication, which is not final-target proof.
