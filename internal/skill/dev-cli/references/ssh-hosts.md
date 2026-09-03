# SSH hosts

Read this before using `dev ssh`, touching a dev-owned SSH fragment, installing a
public key, or registering/removing a fleet host.

## Non-negotiable boundaries

- OpenSSH is the connection authority. `dev ssh list` is a static
  provenance/candidate scan; plain `ssh -G` owns effective values.
- Foreign Include/Host/Match content is read-only. Never use raw file edits to
  make `dev` "adopt" it.
- Dev owns only the exact root `Include ~/.ssh/dev.d/*.conf`, canonical
  `~/.ssh/dev.d/<alias>.conf` files, and strict generated fleet fragments.
- Only a bounded normalized **public** key record may cross the connection.
  Never copy/read private key bytes for transfer, put passphrases/passwords in
  argv/environment, or bypass the SSH agent protocol.
- Preserve host-key and known-hosts policy. `--yes` approves local plans only; it
  never accepts a host key or supplies credentials.
- `--fleet` is always explicit and occurs only after a fresh ordinary alias
  login. Discovery and successful bootstrap do not imply registration.
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

Full non-dry-run setup requires exactly one of:

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

JSON mode is batch-only even on a terminal. Noninteractive full setup requires
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

Every SSH JSON command emits exactly one object with `schema_version`, `kind`,
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
