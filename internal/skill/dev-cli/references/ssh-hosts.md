# SSH hosts

Read this before using `dev ssh`, touching a dev-owned SSH fragment, installing a
public key, or registering/removing a fleet host.

## Non-negotiable boundaries

- OpenSSH is the connection authority. `dev ssh list` is a static
  provenance/candidate scan; plain `ssh -G` owns effective values.
- Foreign Include/Host/Match content stays user-owned. Setup/remove cannot rewrite
  it; explicit format/organize can transform selected user files with recovery.
- Dev owns only the exact root `Include ~/.ssh/dev.d/*.conf`, canonical
  `~/.ssh/dev.d/<alias>.conf` files, and strict generated fleet fragments.
- Only a bounded normalized **public** key record may cross the connection.
  Never copy/read private key bytes for transfer, put passphrases/passwords in
  argv/environment, or bypass the SSH agent protocol.
- Preserve host-key and known-hosts policy. `--yes` approves local plans only; it
  never accepts a host key or supplies credentials.
- Fleet registration (`setup --fleet` or `manage --to fleet|both`) is explicit
  and occurs only after a fresh ordinary alias login. Discovery and successful bootstrap do not imply registration.
- Remote installation cannot be rolled back safely. Unknown means the public key
  may already be present. Do not delete from `authorized_keys` on the user's
  behalf.

## Decide what operation is needed

| Goal | Command | Effect boundary |
|---|---|---|
| report/install the managed Include | `dev ssh init [--apply]` | report by default; explicit local root write only |
| inventory aliases and provenance | `dev ssh list` | static files only |
| evaluate an alias | `dev ssh show <alias>` | runs plain `ssh -G`; Match exec/resolver may run |
| configure/bootstrap/register | `dev ssh setup <alias>` | owned local config, then public-key remote work, then optional fleet |
| prove ordinary login | `dev ssh probe <alias>` | one fresh BatchMode network login |
| remove dev-owned intent | `dev ssh remove <alias>` | owned SSH and explicit generated fleet fragments only |

Use `dev doctor` for read-only local capability/config/permission checks. It does
not contact hosts or repair SSH state.

## One-time initialization

Always show the report before applying:

```bash
dev ssh init
dev ssh init --json
dev ssh init --apply             # interactive confirmation
dev ssh init --apply --yes       # noninteractive local confirmation
```

Do not use `--yes` without `--apply`. If the plan is blocked by unsafe metadata,
links/reparse points, hardlinks, source races, or foreign/drifted `dev.d` content,
follow the manual exact-Include guidance rather than weakening checks. Dev
preserves supported root metadata and never removes a successful Include.

## Discovery is static; show/probe are effectful

```bash
dev ssh list
dev ssh list --format tsv
dev ssh list --json
dev ssh show lab
dev ssh probe lab
```

`list` and completion do not execute `ssh`, resolver, `Match exec`, agent, or
network. Preserve `complete: false`, `unknown`, `inactive`, and conflict outcomes;
do not turn them into success. TSV is one row per definition with six columns:
alias, status, ownership, source, line, comma-separated fleet names.

`show` deliberately runs the system's plain `ssh -G <alias>` and includes system
configuration. It can run configured resolver/Match exec behavior. `probe` uses
one fresh ordinary `BatchMode=yes` login with `-S none`; user
KnownHostsCommand/UpdateHostKeys behavior can still run.

## Alias ownership

Setup classes an alias as:

- **new:** a portable lowercase exact alias; create one canonical fragment;
- **managed:** exactly one structurally valid expected dev-owned fragment;
- **foreign:** any existing non-owned definition; connection policy remains
  read-only, but explicit key bootstrap and `--fleet` may use the alias.

Only new/managed aliases accept these connection fields:

```text
--hostname --user --port --proxy-jump --identity-file --identities-only
```

Foreign aliases must not receive them. A new noninteractive alias needs
`--hostname`; an interactive run may prompt for HostName.

Use local-only modes when remote access is not requested:

```bash
dev ssh setup lab --hostname 192.0.2.20 --user dev --config-only
dev ssh setup lab --hostname 198.51.100.20 --dry-run
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --dry-run --json
```

`--config-only` cannot combine with key/route/bootstrap/fleet flags. `--dry-run`
does not generate, write, run `ssh -G`, touch known_hosts, or use the network; it
reports route/remote actions as unknown. It may perform bounded local validation
of named files. A dry-run fleet plan still requires `--target-os`.

## Explicit key selection or generation

Public-key bootstrap requires exactly one of:

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix
dev ssh setup lab --generate-key --target-os posix
dev ssh setup winlab --hostname 203.0.113.30 --generate-key \
  --key-path ~/.ssh/id_winlab --no-passphrase --target-os windows --yes
```

`--key` accepts a validated `.pub`, identity with a companion `.pub`, or security
key stub with a companion. A missing `.pub` can be derived only after confirmation
with native `ssh-keygen -y`; noninteractive encrypted derivation returns
interaction-required.

Generation is Ed25519 and defaults to `~/.ssh/id_ed25519_dev`; `--key-path` and
`--comment` customize it. Interactive generation leaves the hidden passphrase
prompt to ssh-keygen. Noninteractive generation requires `--no-passphrase`.
Destinations are no-replace; do not delete a colliding file to make the command
pass. Generated pairs remain after later partial failures and after `ssh remove`.

JSON mode is batch-only even on a terminal. Noninteractive public-key bootstrap requires
`--target-os`; local mutation requires `--yes`. Native OpenSSH may prompt only in
interactive human mode.

## ProxyJump and remote OS

Routes are resolved outermost-first from plain `ssh -G`. Supported ProxyJump
forms include alias, comma/nested routes, `user@alias`, `alias:port`, and
bracketed IPv6. ProxyCommand/URI routes, repeated hops, cycles, and ambiguous
syntax fail closed.

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix \
  --hop-os bastion=posix --hop-os winjump=windows
```

`--target-os` names the final target. Repeat `--hop-os alias=posix|windows` for
unknown jumps; a noninteractive run must specify every unknown hop. An already
working jump is skipped unless the user explicitly requests
`--install-on-working-jump`.

Each proof disables connection sharing. The remote installer receives one public
record on stdin:

- POSIX: fixed `sh`, `~/.ssh` mode `0700`, `authorized_keys` mode `0600`, exact
  idempotent append.
- Windows standard account: `%USERPROFILE%\.ssh\authorized_keys`, protected
  current-user + SYSTEM ACL.
- Windows administrator-group account: only after explicit
  `--windows-admin-authorized-keys`, use
  `%ProgramData%\ssh\administrators_authorized_keys` with SYSTEM +
  BUILTIN\Administrators ACL. Dev does not automate UAC; manual elevation may be
  necessary.

Never replace `--target-os windows` with POSIX guessing or bypass reparse/ACL
failures.

## Fresh proof sequence and partial state

For each hop, setup attempts:

1. ordinary fresh BatchMode probe;
2. exact selected-key proof;
3. public-key install if needed/allowed;
4. exact selected-key verification;
5. separate fresh ordinary alias gate.

Fleet requires the final ordinary gate and known target OS. If any remote step
fails after local publication, preserve local managed config and generated keys.
If installer start is uncertain, status is `unknown`; later hops/fleet are
skipped. Report completed per-hop states and suggest rerunning after remediation.
Never claim the remote was unchanged and never implement ad hoc revocation.

## Explicit dev-fleet registration

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup winlab --key ~/.ssh/id_winlab --target-os windows \
  --fleet --fleet-name windows-builder
```

The primary `remotes.toml` is user-authored and byte-preserved. Registration
creates strict `remotes.d/ssh-<alias>.toml` containing only name, ssh_alias, and
remote_os after the fresh gate. Custom path derivation:

- `/srv/dev/lab.toml` -> `/srv/dev/lab.d`
- `/srv/dev/lab` -> `/srv/dev/lab.d`

Fleet loads primary first, generated fragments lexically, then defaults. Names
are globally unique. Primary profiles may share an alias, but any collision
involving a generated fragment fails. `remote_os` defaults to POSIX for legacy
primary entries, participates in cache identity, and chooses POSIX shell versus
encoded allowlisted PowerShell remote transport. A managed Windows entry uses
automatic `dev.exe` lookup.

Use `dev fleet config show` to inspect the redacted effective merge; it marks
generated origins. `dev fleet config edit` and FLEET `e` edit only the primary.
Use `dev ssh setup/remove --fleet` for generated files. A missing remote dev is a
valid registration and later appears as `no-dev`.

### Explicit fleet file export

`dev fleet files [repo-or-path] --to <host>` previews a one-shot export of
explicitly included ignored files. `[worktree].include` is local provisioning,
not export permission: use `[local_files].include` or explicit `--file` paths.
Apply requires an independently verified `machine_id` pin from
`dev fleet machine-id <host>`. `--yes` does not imply `--replace`.

Both existing clones must match by fetch identity, attached branch and exact
commit. Each path must remain untracked and ignored on both sides, and only
bounded regular files qualify. The operation does not clone, switch branches,
provision, transfer task/catalog/note state, delete source files, or evict a
repository. Native Windows payload transfer is disabled. Read `dev fleet files
--help` and `dev help fleet` for the current flags and full transaction behavior.

## Removal

```bash
dev ssh remove lab --dry-run
dev ssh remove lab --yes
dev ssh remove lab --fleet --yes
```

Only canonical secure dev-owned host fragments are removable. A generated fleet
fragment blocks unless `--fleet` is explicit, then fleet removal happens first.
A primary user-authored `remotes.toml` reference always blocks and must be edited
through `dev fleet config edit`.

Removal never touches:

- root managed Include;
- local private/public keys;
- known_hosts;
- remote authorized_keys;
- foreign SSH configuration.

Rotation/revocation/expiry, key deletion, alias rename/adoption, arbitrary SSH
directives, managed wildcards/Match, ProxyCommand automation, custom
AuthorizedKeysFile, password/vault storage, bulk inventory import, and an SSH TUI
are deferred.

## Machine output

Existing alias commands emit one JSON object with `schema_version`, `kind`,
and stable status/action/error codes. Operational failures still emit one safe
object; syntax errors do not emit partial JSON. Diagnostics/progress remain on
stderr. Plans/results may expose paths, modes, digests, fingerprints, and
per-hop/fleet booleans, but never private material, passwords/passphrases,
complete public-key lines, agent payloads, or raw command-like SSH options.

Kinds:

```text
ssh_init_plan | ssh_init_result
ssh_list
ssh_show
ssh_setup_plan | ssh_setup_result
ssh_probe
ssh_remove_plan | ssh_remove_result
```

Treat `partial`, `unknown`, `blocked`, `interaction_required`, and source/security
errors as real outcomes, not warnings to override.

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
