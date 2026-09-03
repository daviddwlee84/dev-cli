# Remote fleet

See repository, task, and runtime state from several machines without sharing
their filesystem paths or copying their dev configuration.

## Configure hosts and ownership layers

```bash
dev fleet config init
dev fleet config edit          # primary remotes.toml only
dev fleet config show          # redacted effective merge + generated origins
dev fleet config path          # primary path
dev fleet status
dev fleet machine-id lab
```

Fleet merges the user-authored primary `$XDG_CONFIG_HOME/dev/remotes.toml` with
strict dev-owned sibling `remotes.d/ssh-<alias>.toml` fragments, then applies
defaults. A custom `--remotes /srv/dev/lab.toml` uses `/srv/dev/lab.d`; a path
without `.toml` also gains `.d`. The primary may be absent, but is never rewritten
by SSH registration.

Prefer `ssh_alias`; explicit `hostname`/`user`/`port`/`identity_file` fields are
available in primary profiles. `remote_os = "posix"|"windows"` selects target
launcher/path semantics; omission remains POSIX. Password fallback in primary
profiles supports `prompt`, `plain`, and `bitwarden`, but key/agent authentication
is attempted first. Generated fragments can contain only name, ssh_alias, and
remote_os—never passwords, machine pins, or alternate connection policy.

Host names are globally unique. Existing primary profiles may share an SSH alias,
but any collision involving a generated fragment fails. Do not edit generated
fragments: reconcile them with explicit `dev ssh setup <alias> … --fleet` and
remove them with `dev ssh remove <alias> --fleet`.

Read-only commands tolerate an unpinned host. Before a mutating file transfer,
run `dev fleet machine-id <host>`, verify the UUID independently, and add it as
that primary host's `machine_id`; the command reports but never writes the pin.

## Register a freshly verified alias

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup winlab --key ~/.ssh/id_winlab --target-os windows \
  --fleet --fleet-name windows-builder
```

`--fleet` is never implied by SSH discovery/bootstrap. Registration is written
last, only after exact-key proof and a separate fresh ordinary alias login. A
missing remote dev is allowed and later appears as `no-dev`. Partial or unknown
bootstrap skips registration while retaining valid local SSH config/generated
keys.

## Inspect and open

```bash
dev fleet list
dev fleet list --host lab --repo api
dev fleet list --cached
dev fleet list --json
dev fleet open lab api
```

Every host runs its own `dev`, so its `config.toml`, scan roots, exact repo paths,
task registry, and runtime remain authoritative. An unreachable host can reuse
the last successful private XDG snapshot. Cache identity includes the full SSH
endpoint, port, timeouts, dev path, remote OS, and optional machine-ID pin.

The dashboard FLEET view hides this machine by default because REPOS already
shows richer local state; `a` includes local rows. `dev fleet list` always keeps
local plus configured remote hosts. FLEET `e` edits only primary `remotes.toml`
and reparses the complete primary-plus-generated merge on return.

Opening a remote repo prefers native Herdr remoting for an eligible host. It
otherwise opens through SSH, validates the repository path through remote dev,
changes directory, and starts a login shell.

## POSIX and Windows remote transport

POSIX hosts use an injection-safe shell launcher. Windows hosts use an encoded
PowerShell wrapper that accepts only `_snapshot`, `_sync`, content-free
`_capability`, `_open-herdr`, and `_shell` helper shapes, locates `dev.exe` when
`dev_path = "auto"`, preserves protocol stdin, returns 127 when dev is absent,
and propagates its status. Native Windows `_files-plan`/`_files-apply` payloads
remain denied before content is sent. Explicit paths
are validated with target-OS semantics rather than controller filepath rules.
Generated Windows hosts require automatic dev lookup.

The wrapper is not a general remote-command interface and does not weaken
OpenSSH host-key or known-hosts policy.

## Safely propagate a branch

```bash
dev fleet sync api --push
dev fleet sync api                 # HEAD must already equal fetched upstream
dev fleet sync api --host lab
```

The source must be clean and attached. Targets are matched by normalized Git
remote identity, not directory name. Each target fetches first; only a clean
checkout of the same strictly-behind branch advances with `merge --ff-only`.
Different branches are not switched. Dirty, ahead, divergent, ambiguous, and
unreachable targets remain untouched and make sync non-zero. Hosts without dev
or without that repository are ignored and reported explicitly.

There is no automatic rebase, force push, background hook, or all-repository
pull. Resolve divergent work interactively on the machine that owns it.

## Transfer explicit ignored files

```bash
dev fleet files api --to lab
dev fleet files api --to lab --apply --yes
dev fleet files api --to lab --replace --apply
```

The report-only default expands `[local_files].include` plus repeatable `--file`
patterns into exact source paths. Source and target must already match by fetch
identity, attached branch and commit. Both sides must prove each path untracked
and ignored; only bounded regular files pass. `--replace` separately authorizes
a target whose bytes differ, and `--yes` never implies replacement.

Apply requires a matching `machine_id` pin, uses a bounded non-PTY protocol,
keeps file content and hashes out of public output/cache, writes owner-only
files through held roots, and journals rollback before publication. It does not
clone, switch branches, transfer tasks/catalog/notes, provision dependencies,
delete source files, or evict a repository. Native Windows payloads are blocked.
