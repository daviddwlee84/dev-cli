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

Public-key bootstrap requires exactly one explicit `--key` or `--generate-key`.
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

On a failed SSH command, successful handshake/host-key/authentication log markers
remain reported hints with unknown state. Only a zero-exit fresh login establishes
completed authentication. Server-supplied banners/debug messages cannot provide
positive client evidence; QoS comparisons use pre-connection marking and actual
connection progress or a verified successful login.

## Discover and organize machine connections

```bash
dev ssh setup                    # multi-select hosts, configure each connection
dev ssh list --tailscale --lan    # explicit Tailscale status + cached LAN
dev ssh discover --source tailscale --json
dev ssh discover --source lan --interface en0 --cidr 192.168.1.0/24
dev ssh setup lab --from tailscale:lab --user dev --config-only
dev ssh setup lab --from tailscale:lab --user dev --auth existing --to both
dev ssh machine show --json
```

Plain list/completion remain static. The discovery flags add a machine view with
SSH aliases, Tailscale, LAN, fleet and Herdr columns. JSON retains the alias fields
and adds machines, sources and observed_at. Each connection keeps its own user,
port, key and route. Names alone do not prove that sources are the same machine.

Tailscale is optional and is queried only when explicitly selected. Dev uses
system ssh, preserves host-key policy and does not enable/login/configure
Tailscale. Use --auth existing for Tailscale SSH or already-working ordinary
SSH; this path performs a fresh login without installing a public key. Choose
--key or --generate-key for ordinary sshd public-key bootstrap. An advertised
Tailscale SSH port-22 endpoint blocks key installation, and a login without the
selected key cannot count as its exact-key proof. The optional tailscale ssh
wrapper's MagicDNS/userspace transport is not written into managed aliases.

--from tailscale:<peer> accepts an exact or unambiguous peer selector;
--from lan:<ip:port> selects a literal endpoint. Available IPv4 is the default
HostName; --hostname can choose an explicit MagicDNS FQDN. New discovery aliases
require --user outside a terminal. Source-aware setup defaults to local config
and machine mapping. --to fleet|herdr|both explicitly registers after fresh SSH
proof; --fleet remains compatible. Herdr approvals stay native.

LAN scans use selected on-link IPv4 ranges, port 22 by default, at most 256
addresses/16 ports/4,096 endpoints/32 workers and 30 seconds. TCP/banner state
and reverse-DNS names are observations and suggestions. --ports selects other
ports; --refresh bypasses a fresh matching cache. The five-minute cache under
$XDG_CACHE_HOME/dev/ssh-discovery is disposable; list --lan and dashboard refresh
never scan. IPv6 range scans and mDNS are not implemented.

The durable private registry is paths.state_dir/machines/registry.db. It owns
controller-local machine UUIDs, not remote fleet machine_id pins. Discovery/list
never create IDs. Setup enrolls connections; --machine <uuid> reuses an existing
machine. Each provider binding retains its namespace and reviewed fingerprint.
Changed/missing sources remain stale/unresolved instead of being silently rebound.

```bash
dev ssh machine adopt --label lab --source <reference-id> --json
dev ssh machine adopt --label lab --source <reference-id> --apply --yes
dev ssh machine link --machine <uuid> --source <reference-id> --apply
dev ssh machine unlink --machine <uuid> --source <reference-id> --apply
dev ssh machine merge --machine <source-uuid> --into <survivor-uuid> --apply
```

Get reference IDs from list --tailscale --lan --json. Registry actions preview
before --apply; noninteractive apply requires --yes. Unlink retains suppression;
merge keeps the survivor and old-ID redirect. These actions do not edit provider
configuration or stop sessions. cache clear ssh-discovery/all cannot delete the
registry. Registry snapshots/transactions carry schema_version; discovery and
source-aware setup use ssh_discovery and ssh_onboarding_plan/result documents.

## Key selection and optional registration

```bash
dev ssh key list
dev ssh key list --json
dev ssh key list --no-agent
dev ssh key list --alias lab --json
```

The default key listing scans bounded public-key files under `~/.ssh` and the
current SSH agent without evaluating an alias. It deduplicates by fingerprint
and reports algorithm, comment, source paths, source provenance and signer-availability
hints. `--no-agent` skips agent enumeration. Only explicit `--alias`
uses plain `ssh -G` and the alias's configured identity/agent settings; configured
Match exec or resolver behavior may run. Listing never reads private-key contents,
derives/generates a key, repairs permissions or authenticates remotely.
`--json` emits one `ssh_key_list` document with candidates, completeness and source
diagnostics; a missing or unusable source does not become an empty success claim.

Choosing an existing key in the setup wizard opens this catalog, with an
**Enter a key path…** fallback. A public file without an available signer remains
visible but cannot silently satisfy bootstrap. Select another identity, load its
signer into the agent or provide the matching private-key path. The selected key
is validated before continuing to later registration prompts; generation and
remote installation retain their own explicit choices and native prompts.

Before the no-argument setup wizard's host picker, dev checks only existing
`~/.ssh`, its root config and `dev.d` permissions. Choosing a key adds its selected
private/public companion and required parents to the check. On macOS/Linux,
straightforward tightening uses 0700 for directories and 0600 for config/private
files; public companions only lose group/world write bits. Repairs never add
permissions or create missing files. ACLs and unsupported security metadata need
manual handling; Windows validates existing ACLs without rewriting them. A repair
preview shows exact paths and mode changes and asks separately before tightening them.
Completed tightening remains if the later wizard is canceled. This is not a
recursive chmod: unrelated Include files, other keys, ownership changes,
links/hardlinks and unsupported metadata require manual remediation. Declining
repair stops before configuration or authentication; normal listing/dry runs do
not perform repairs. The main onboarding preview still precedes aliases,
registry bindings, generated keys and remote changes.

Registration uses independent **Fleet** and **Herdr** checkboxes, both initially
unchecked. Space toggles an item, Ctrl+A selects/clears, Enter accepts and Esc
cancels. No checked item means no registration; one selects that provider and two
select both. The setup wizard continues without provider-specific prompts when
none are selected. Standalone manage/dashboard registration returns a no-op
without authentication or provider changes. CLI `--to fleet|herdr|both` remains
unchanged.

The external picker is configured by `[picker].command` and defaults to fzf for
single selections, including key selection. A missing executable or an empty
command uses the built-in picker. Multi-selection always uses the built-in
Bubble Tea picker, even when fzf is installed. Required host/source selectors
still reject an empty selection; registration alone treats it as an accepted skip.

## SSH key doctor

```bash
dev ssh key doctor
dev ssh key doctor --json
dev ssh key doctor --fix
dev ssh key doctor --key ~/.ssh/custom-key --key ~/.ssh/other-key.pub
dev ssh key doctor --key ~/.ssh/custom-key --fix --yes --json
```

The default report performs a bounded metadata-only scan under `~/.ssh`. It
inspects public-key paths and their existing private companions, plus exact
standard private-key filenames directly under `~/.ssh`, even when no `.pub` exists. It does not guess
that every extensionless file is a private key. Traversal stays in direct,
owned directories. Noncandidate symlinks are skipped without following them;
symlinks that look like recognized key files make the scan incomplete. Canonical
SSH setup paths are independently guarded by the permission plan. Repeat `--key PATH` to restrict
the scope to selected paths/companions and canonical `~/.ssh`, root config and
`dev.d` paths; this also covers a custom private key without a public companion.
Neither mode reads key contents, queries an agent, runs `ssh`/`ssh-keygen`,
resolves an alias or authenticates remotely.

Without `--fix`, the command only reports observations and proposed permission
changes. Repairable findings return success so the report can be used before a
later explicit repair. A blocked or incomplete scan returns an error and cannot
authorize any write, including when `--fix` is present. Missing paths are not
created, and unsupported ownership, links/hardlinks, ACLs or security metadata
remain manual remediation.

`--fix` shows a concrete path/mode preview and asks for confirmation. `--yes` is
valid only with `--fix` and is required for noninteractive or JSON repair. The
selected repairs form one source-bound plan applied under the existing SSH
operation lock, with revalidation before changes and a fresh post-repair check.
On macOS/Linux it can only tighten supported modes; Windows verifies existing
ACLs and does not rewrite them. An interrupted or partly completed repair keeps
completed tightening and reports its outcomes; it never broadens permissions to
simulate rollback. The setup wizard uses the same permission core but still
checks only baseline paths and the chosen key, not all unselected keys.

`ssh key list` diagnostics retain stable codes such as `public_key_unreadable`
and `private_key_permissions`, now accompanied by the specific observed cause
and an applicable next action. Permission/path failures point to `dev ssh key doctor`;
missing or malformed public files get specific path/format guidance. A missing configured `.pub` can be an unused
OpenSSH default, not proof that a private key exists. Doctor inspects path and
permission safety; it does not repair malformed public-key contents or prove
that a signer or remote login works.

JSON uses `schema_version: 1` and `ssh_key_doctor_plan`/`ssh_key_doctor_result`
kinds. It includes `scope` (`discovered` or `selected`), `complete`, `key_paths`
and the exact permission `plan`; applied results retain per-path outcomes and a
`recheck` when completed repairs are verified.


## Fleet source profiles and local routes

```bash
dev ssh discover --source fleet --host gateway --host lab --refresh
dev ssh list --fleet --json
dev ssh setup internal-api --from fleet:gateway/api --config-only
dev ssh setup internal-api --from fleet:gateway/api --auth existing
dev ssh setup internal-api --from fleet:gateway/api --dry-run --json
```

Fleet discovery reads only the explicitly selected sources (at most 16). Without
`--host`, an interactive picker selects them; noninteractive use requires host
names. It never explores a source's own fleet. Metadata uses BatchMode first;
only an already configured fleet password source may authorize a password retry,
and a configured prompt needs an interactive controller. `--refresh` bypasses a
fresh cache. `ssh list --fleet` only displays cached source profiles and does not
contact them. Existing default `ssh list` JSON/TSV remains static.

A compatible remote `dev` exports a bounded static alias inventory without
running remote `ssh -G`, a resolver, or an agent. The first explicit capability
exchange may create that remote user's dev UUID. The observed UUID is reported,
never copied into a fleet `machine_id` pin or used to merge machines automatically.
Missing/older dev, failed authentication, timeouts, changed source identity and
incomplete responses retain distinct states; independently cached metadata may
remain visible as stale.

Each remote profile has a stable ID scoped to its source UUID, login user, SSH
root and alias. Fingerprints describe the observed configuration revision. The
human selector is `fleet:HOST/ALIAS`; names containing delimiters use percent
encoding, or automation can use an exact `fleet-ssh:` ID from discovery. Identical
names on different sources do not identify the same connection.

Setup resolves only the selected remote route through native `ssh -G`, then
previews local managed aliases and the complete ProxyJump route. Configured
Match exec/resolver behavior may run during this explicit resolution. The local
gateway and each hop keep their own user, port and credential context. Compatible
local aliases may be reused; foreign definitions are not rewritten. Unsupported
routing or source-local command policy requires explicit local configuration.
Remote IdentityFile/IdentityAgent paths and trust files are not copied: local SSH
configuration and selected controller keys govern the imported route.

The default remains configuration only. Key installation, per-hop key choices
(`--hop-key local-alias=key-path`) and provider registration are explicit. A
selected target key does not authorize its installation on every working jump.
Before changes, setup rechecks source identity, fingerprints and route facts.
`--dry-run` uses cached remote inventory/resolution and static local facts only;
missing resolution is reported rather than causing an SSH connection or write.

## Choose where SSH runs

```bash
dev ssh connect internal-api
dev ssh key list --on fleet:gateway --alias api --json
dev ssh connect api --on fleet:gateway
dev ssh connect api --on fleet:gateway --key-id SHA256:FINGERPRINT
```

A normal connection runs SSH on the controller. `--on fleet:HOST` starts that
source host's native SSH client using its own alias, agent and key files. Remote
key listings are display metadata; their paths are never interpreted locally.
`--key-id` is reselected and checked in the executing host's catalog, including
its agent policy. Private keys are never transferred. This command opens an
interactive session only, disables agent forwarding and preserves child exit status;
it never retries a started session or accepts extra remote-command arguments.

Ordinary native connections retain the user's remaining SSH behavior. When a
password or exact-key workflow needs a private temporary configuration, supported
settings are preserved and unsupported LocalCommand, port forwarding, SetEnv or
RemoteCommand values using `%` expansion are rejected rather than silently lost.

With no selected key or managed password context, an opaque ProxyCommand alias
can use a guarded native-only connection: its complete user Include closure and
native effective settings are rechecked, without inventing route hops or an
exact-key proof. ProxyJump cycles and unsupported exact-key operations remain
rejected; opaque routes still cannot be imported as local ProxyJump profiles.

## Derive a missing public companion

```bash
dev ssh key derive ~/.ssh/custom-key
dev ssh key derive ~/.ssh/custom-key --apply
dev ssh key derive ~/.ssh/custom-key --apply --yes --json
```

Derive accepts a private identity inside `~/.ssh` and previews its missing `.pub`
without running ssh-keygen or reading private contents. `--apply` confirms and
invokes native `ssh-keygen -y`; noninteractive/JSON apply needs `--yes`. Native
ssh-keygen owns encrypted-key prompts. Existing companions are never overwritten.
The operation revalidates the selected path under the SSH operation lock and
publishes only the public companion. It does not install a key or repair modes;
use `ssh key doctor` first when permissions need attention.

## Remember a successful SSH password

After a controller-driven password login has matching authentication evidence,
dev offers **Yes / No / Never**, with **No** selected by default. Yes saves that
password in the chosen provider. No keeps it only for the current operation.
Never persists suppression for that exact origin/profile/route/host/user/port
context; it is not a global preference. Unknown, MFA, passphrase and host-key
prompts are not treated as a reusable account password.

`--password-store system|bitwarden` on setup/connect chooses the save provider;
system is the default. macOS uses Security framework, Windows uses Credential
Manager, and Linux uses an available Secret Service. Bitwarden requires an
installed, unlocked CLI and uses stdin for create/edit payloads. The feature
never places passwords in argv, environment, ordinary files, logs or JSON.
Unavailable or denied providers do not become a plaintext fallback. Uncertain
writes remain pending/unknown and are not automatically retried.

`$XDG_CONFIG_HOME/dev/ssh-credentials.toml` stores only context, ask/never policy,
provider references and write-state metadata. Edit that policy to re-enable a
Never context. Removing a reference stops dev reuse but does not delete its
vault item or change the remote password; use the provider's native interface
for vault cleanup. Existing explicit fleet password sources retain priority.
Discovery does not opt into saved-reference lookup. Remote source-to-target
passwords in `connect --on` remain outside this controller save workflow.

These features do not import SSH private keys into a vault, provision YubiKeys,
or export Apple Passwords. Those are separate future migration workflows.
