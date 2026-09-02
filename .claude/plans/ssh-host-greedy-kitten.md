# Context

`dev` already has a mature SSH-backed `internal/fleet` (`remotes.toml`, transport, cache, CLI, and FLEET TUI), while this machine's chezmoi tooling separately demonstrates SSH alias discovery, safe config fragments, public-key bootstrap, ProxyJump handling, and Windows `authorized_keys` behavior. The missing product layer is first-class SSH-host lifecycle: discovering the user's real OpenSSH aliases, safely owning selected aliases, provisioning public keys, verifying ordinary SSH behavior, and optionally registering a verified alias in **dev's** fleet.

The feature should be a portable `dev` capability, not a wrapper around chezmoi, Television, Tailscale, Bitwarden, or `~/.config/fleet/machines.toml`. OpenSSH remains the durable source of truth. `dev` must not create a third host database, rewrite foreign `Host` blocks, copy private keys, weaken host-key policy, or silently turn every SSH alias into a fleet member.

The intended outcome is:

- one-time, explicit installation of a dedicated OpenSSH Include;
- read-only discovery of active foreign and managed aliases;
- idempotent setup/update of only `dev`-owned per-host fragments;
- guided key selection or generation and public-key-only installation across ProxyJump routes;
- full macOS/Linux/Windows controller support and POSIX/Windows OpenSSH remote support;
- explicit `--fleet` registration in the existing dev fleet after a fresh ordinary login succeeds.

## Product and command contract

Add a top-level `dev ssh` family backed by a new `internal/sshhost` domain package. Keep Cobra responsible only for flags, prompts, rendering, and injected I/O.

- `dev ssh init [--apply] [--yes] [--json]`
  - Report the exact one-time change by default.
  - With explicit `--apply`, install one unconditional top-level `Include ~/.ssh/dev.d/*.conf` before the first `Host`/`Match` directive.
  - Never install the broader `config.d/*` Include or activate unrelated chezmoi fragments.
  - If the existing root file's identity or metadata cannot be preserved safely, make no change and print precise manual placement guidance.
- `dev ssh list [--json | --format tsv]`
  - Statically walk the active user-config Include closure without making a network connection or executing `Match exec`.
  - Emit exact aliases, declaration source/line, per-definition managed ownership, conflicts, and matching dev-fleet membership. Wildcard-only and definitely unreachable declarations are diagnostics rather than selectable hosts.
  - Mark the result incomplete, with reasons, when dynamic `Match`/Include behavior cannot be proven. TSV is a small documented selector contract suitable for Television; JSON is a versioned object rather than a bare array.
- `dev ssh show <alias> [--json]`
  - Show all discovered definitions plus effective values from **plain** `ssh -G <alias>` so system SSH config is included just as it is for fleet.
  - Warn that user-authored `Match exec` and resolver behavior may run. Do not parse unstable `ssh -vv` prose as field provenance.
- `dev ssh setup <alias> [connection/key flags] [--config-only] [--fleet] [--fleet-name <name>] [--dry-run] [--yes] [--json]`
  - This is the idempotent create/update/bootstrap command; do not add separate `add` and `update` families.
  - For an unknown alias, collect `HostName` and optional `User`, `Port`, `ProxyJump`, `IdentityFile`, and `IdentitiesOnly` values and create a managed fragment.
  - For a valid managed alias, reconcile only that fragment. For a foreign alias, never alter connection policy; key setup and fleet registration may proceed against the existing alias.
  - `--config-only` stops after local config verification. `--fleet` is always explicit and occurs only after fresh ordinary authentication succeeds.
  - Human mode may prompt for local confirmation, passphrase, password, and host-key decisions through native OpenSSH tools. JSON/non-TTY mode is batch-only; `--yes` confirms only the local plan and never supplies credentials or accepts a host key.
- `dev ssh probe <alias> [--json]`
  - Perform one fresh BatchMode login with connection sharing disabled, preserving the user's host-key and known-hosts policy. Classify failures conservatively and document that user-configured `KnownHostsCommand`, `UpdateHostKeys`, or `Match exec` behavior may still run.
- `dev ssh remove <alias> [--fleet] [--dry-run] [--yes] [--json]`
  - Delete only a structurally valid, expected dev-owned host fragment.
  - Never remove the shared Include, key files, known_hosts entries, or remote authorized keys.
  - If a generated fleet fragment exists, require `--fleet` and remove it first; without the flag, block instead of silently mutating a second durable intent. A reference in primary user-authored `remotes.toml` also blocks removal and points the user to `dev fleet config edit`.

Extend the existing `dev doctor` with read-only SSH capability/config/permission checks; do not create a repair-capable `dev ssh doctor` or a new SSH TUI in this release.

All public JSON uses one versioned object with a `kind`, stable machine-readable status/action codes, and exactly one document on stdout. Plans/results include paths, modes, digests, fingerprints, per-hop state, fleet state, and honest `partial`/`unknown` outcomes, but never private bytes, passphrases, passwords, complete public-key lines, agent payloads, or unredacted command-like SSH options. Diagnostics and child progress remain on stderr.

## 1. Build the SSH host domain and discovery model

Create `internal/sshhost` with an injected service/runner, immutable plans/results, and platform backends. Representative files are `service.go`, `types.go`, `discover.go`, `managed.go`, `keys.go`, `bootstrap.go`, `runner.go`, `securefs_unix.go`, and `securefs_windows.go`.

- Reuse `internal/pathx` component/confinement validation, held-`os.Root`/`os.SameFile` patterns from `internal/repotemplate/snapshot.go`, `lockx` serialization, synced private writes from `internal/note/store.go`, no-replace platform patterns from `internal/experiment`, and real-vs-redacted argv handling from `internal/runtime/herdr.go`.
- Implement a bounded OpenSSH user-config scanner that handles case-insensitive directives, quoting/comments, multiple Include arguments, tilde/tokens/environment expansion supported by OpenSSH, inline lexical glob expansion, relative paths, cycles/repeated edges, and `Host`/`Match` guard context. It is a provenance/candidate scanner, not a replacement SSH evaluator.
- Treat dynamic or unsupported constructs conservatively: report `closure_incomplete`; block a mutation when an exact-alias collision or managed path reachability cannot be ruled out.
- Use plain `ssh -G <alias>` as semantic authority for effective values. Model scalar first-value-wins options separately from additive ordered options such as `IdentityFile`.
- Restrict managed aliases to a portable lowercase exact-name grammar (no wildcard, negation, whitespace/control, drive prefix, slash/backslash, or case-fold collision). Foreign exact aliases outside that grammar remain listable/showable but cannot be adopted or mutated in v1.

## 2. Own a dedicated fragment namespace and explicit root initialization

Use `$HOME/.ssh/dev.d/<alias>.conf`; the root Include matches only `*.conf`. `dev` owns the directory and files it creates, not the surrounding `.ssh` tree.

- Render a timestamp-free v1 header plus exactly one `Host <alias>` block with a strict directive allowlist and deterministic order: `HostName`, optional `User`, `Port`, `ProxyJump`, `IdentityFile`, and `IdentitiesOnly`. Never emit `StrictHostKeyChecking` or arbitrary passthrough directives.
- Consider a file managed only when canonical path, non-symlink/non-reparse regular-file type, filename, version/header, alias, single-block shape, and directive allowlist all agree. Unknown content or manual drift is a conflict, not permission to overwrite/delete.
- Preserve byte-identical files and mtimes. Re-read identity/content immediately before mutation and verify desired effective fields with plain `ssh -G` after writing. If local verification fails, restore only the just-replaced managed file while the lock and identity checks still hold.
- `ssh init --apply` must preserve the root file's bytes outside the inserted preamble, BOM/newline style, mode/owner, extended attributes/flags on Unix, and owner/DACL on Windows. Use same-directory staging, file sync, platform atomic replacement, and directory sync where supported; reject symlinks, reparse points, special files, hardlinks, concurrent changes, or metadata that cannot round-trip. A missing root may be created securely. Successful Include installation is monotonic and is never removed automatically.
- Ensure staging/backup names cannot match `*.conf`. Validate that `dev.d` contains only expected owned files before enabling it.

The Unix security backend enforces no-follow traversal, owner-controlled non-writable parents, `0700` private directories, `0600` config/private files, and no-replace key installation. The Windows backend uses `golang.org/x/sys/windows` (already a direct dependency) to reject every mutable reparse component, retain/revalidate handle identity, and set/verify protected DACLs using the current-user and SYSTEM SIDs. Existing user-owned `.ssh` ACLs are validated, not silently rewritten; root edits clone their verified metadata.

## 3. Implement guided key and remote bootstrap without private-key transfer

- Build a key catalog from effective `IdentityFile` values, validated local `.pub` files, and standard `ssh-add -L`/IdentityAgent behavior. De-duplicate by SHA256 fingerprint. Agent-only/Bitwarden-backed keys are used only through the SSH agent protocol; `dev` never invokes `bw` for this feature.
- `--key` accepts a validated public key or a private/security-key stub with a companion `.pub`. Derive a missing public half with `ssh-keygen -y` only after confirmation; encrypted noninteractive derivation fails rather than accepting a passphrase in argv/environment.
- `--generate-key` creates Ed25519 by default through native `ssh-keygen`. Interactive mode delegates hidden passphrase prompts to it; noninteractive generation requires explicit `--no-passphrase`. Generate into a private staging basename, validate the pair/fingerprint, then install both final paths with platform no-replace semantics so a concurrent file is never overwritten. Generated keys are retained after later failures and are never removed by `ssh remove`.
- Resolve ProxyJump routes outermost-first, including nested/comma routes and explicit user/port/IPv6 forms. Reject cycles and unsupported ProxyCommand/URI forms rather than guessing. Track OS/admin state per hop; accept repeatable per-alias OS overrides for ambiguous/noninteractive routes.
- Probe each hop with ordinary fresh BatchMode authentication first. Do not install the selected target key on a jump that already works unless the user explicitly opts in. Disable existing multiplexed sockets for every authentication proof (for example `-S none`), because a user-configured ControlMaster can otherwise create false positives.
- Replace controller-side `ssh-copy-id` with one portable installer runner. Invoke the system `ssh` on every controller and send exactly one bounded, normalized **public** key record on stdin to a fixed remote program:
  - POSIX: a constant `sh` installer creates/validates `~/.ssh`, safely and idempotently appends to `authorized_keys`, and sets `0700`/`0600` without interpolating key data into shell text.
  - Windows: a compact UTF-16LE/Base64 PowerShell installer reads the key from stdin, rejects reparse targets, appends idempotently, and applies verified ACLs. Standard users use `%USERPROFILE%\.ssh\authorized_keys`. Administrator-group users require a separate explicit confirmation/noninteractive flag before using shared `%ProgramData%\ssh\administrators_authorized_keys`, whose DACL is SYSTEM + BUILTIN\Administrators.
- Preserve normal OpenSSH host-key/password prompts in TTY mode; never force `accept-new`, disable known_hosts, or add a password backend. Non-TTY mode uses BatchMode and fails with `interaction-required` when native interaction would be needed.
- Launch subprocesses in a killable Unix process group or Windows Job Object. Once a remote installer starts, timeout/cancellation is reported as `unknown`; never infer that the server did not append the key.
- After each install, verify the exact selected identity with connection sharing disabled, then gate fleet registration on a second fresh **ordinary alias** login. A remote failure leaves valid local config/generated keys in place, never revokes a possibly installed key, skips fleet registration, and returns a resumable partial result.

## 4. Add safe native dev-fleet registration and Windows fleet transport

Extend `internal/fleet` rather than creating a second transport or editing the personal chezmoi fleet.

- Derive a managed fragment directory from the selected primary remotes path: default `$XDG_CONFIG_HOME/dev/remotes.d`; `--remotes /x/lab.toml` uses `/x/lab.d` (non-`.toml` paths append `.d`).
- Keep primary `remotes.toml` byte-for-byte user-authored. Load strict owned `ssh-<alias>.toml` fragments after it in lexical order. Each fragment contains only a v1 header, one `[host]` with `name`, `ssh_alias`, and `remote_os`, and secure mode/DACL; defaults and password/connection duplicates are forbidden.
- Apply defaults only after merging. Preserve every previously accepted primary-file rule, including multiple primary host profiles sharing an SSH alias. Duplicate names still fail globally; an alias collision becomes an error only when a managed fragment participates. Missing primary config remains valid.
- Add optional `remote_os = "posix"|"windows"` to `fleet.Host`; empty remains POSIX for backward compatibility. Include it in endpoint cache identity. Managed registration records the platform verified by bootstrap.
- Update `fleet.Transport`'s remote command launcher by remote OS:
  - retain the current POSIX launcher;
  - for Windows, use an injection-safe encoded PowerShell wrapper that locates `dev.exe`, returns 127 when absent, passes only allowlisted hidden fleet helper arguments, preserves stdin for `_sync`, and propagates exit status. Managed Windows entries use `dev_path = auto`; fix explicit remote-path validation so it uses target-OS semantics rather than controller `filepath.IsAbs`/local expansion.
- Keep existing `fleet list/status/sync/open`, cache, and FLEET TUI behavior. They consume the merged loader automatically. `fleet config edit` continues to open only primary `remotes.toml`; `config show` redacts secrets and identifies generated entries so users know to use `dev ssh setup/remove` rather than edit them.
- Write a fleet fragment only after the fresh ordinary login gate. A missing remote `dev` is still a valid SSH onboarding result and will continue to appear as fleet `no-dev`.

Local changes form an ordered, idempotent saga rather than pretending the remote operation is atomic: validate everything; create/verify key and managed SSH fragment; install/verify remote public keys; then write fleet registration last. Each file uses atomic no-follow replacement and fresh source checks. Partial results state exactly what completed; rerunning converges. Do not add a durable host database or a repair journal in v1.

## 5. Wire CLI, diagnostics, completion, and existing UI seams

- Add thin command assembly/rendering in `internal/cli/ssh.go` (and a small wizard helper if necessary), register it in `internal/cli/root.go`, and use `App.In/Out/Err`, `newPrompter`, and explicit TTY checks.
- Keep dry-run side-effect-free: no key generation, file writes, known_hosts effects, `ssh -G`, network calls, or remote probes. Render unknown remote actions honestly.
- Add host-name completion from the static scanner without invoking network or `Match exec`.
- Extend `internal/cli/doctor.go` with optional OpenSSH binaries, Include reachability, managed ownership, Unix mode/Windows DACL, and fleet-fragment checks; Git remains the only universally required dependency.
- Do not add a Bubble Tea SSH view. Add only regression coverage proving generated fleet fragments appear in the existing FLEET view and that `e` still edits primary `remotes.toml`.

## Critical files

- New domain and tests: `internal/sshhost/*.go`, including platform security/process/replace files and focused scanner, ownership, key, bootstrap, and fault-injection tests.
- Existing fleet integration: `internal/fleet/config.go`, `transport.go`, `cache.go`, and their tests.
- CLI wiring/contracts: `internal/cli/ssh.go`, `root.go`, `doctor.go`, `fleet.go`, command/usage/JSON tests, and the existing FLEET TUI integration tests.
- Architecture and shipped help: `CLAUDE.md`/`AGENTS.md`, `internal/help/topics/ssh.md`, `internal/skill/dev-cli/SKILL.md`, and a new authored `internal/skill/dev-cli/references/ssh-hosts.md`.
- User docs: `README.md`, `CHANGELOG.md`, new paired `docs/guides/ssh-hosts.md` / `.zh-TW.md`, paired remote-fleet/commands-config/compatibility/sources-freshness updates, and `mkdocs.yml`.
- Windows acceptance gate: `.github/workflows/ci.yml` plus release cross-build coverage.

## Verification

1. **Domain and parser tests**
   - Quoting/comments/case-insensitive directives, multiple and nested Includes, lexical globs, relative/tilde/token expansion, Host/Match guards, cycles, bounds, incomplete closures, active versus unreachable aliases, exact/wildcard collisions, and additive `IdentityFile` behavior.
   - Managed alias grammar, deterministic rendering, header/shape ownership, manual drift, symlink/reparse/special/hardlink rejection, idempotent mtime preservation, root preamble/newline/BOM preservation, metadata round-trip, concurrent source change, and atomic rollback.
2. **Key/bootstrap tests with injected runners**
   - File/agent catalogs, fingerprints without material leaks, `.pub` derivation, native-passphrase and explicit no-passphrase generation, no-replace races, key retention, no private-key/scp path, ProxyJump flattening/cycles/mixed OS, existing-hop skip, `-S none` fresh-auth behavior, TTY versus batch interaction, host-key-policy preservation, process-tree cancellation, and unknown-after-send outcomes.
   - Fixed POSIX and PowerShell installers: stdin-only key payload, idempotency, permissions/DACLs, standard/admin Windows paths and consent, reparse rejection, and final exact-key plus ordinary-alias verification.
3. **Fleet tests**
   - Primary absent/present, custom `--remotes` derivation, strict fragment schema/ownership/permissions, stable merge/default order, legacy primary duplicate aliases, managed collision errors, primary bytes/comments unchanged, remove blocking, redaction, and existing TUI loader/error-retention behavior.
   - Windows `remote_os`, endpoint invalidation, PowerShell argv/encoding/injection resistance, hidden-helper allowlist, stdin preservation for sync, no-dev exit 127, shell/open behavior, and target-OS `dev_path` validation.
4. **CLI/structured-output tests**
   - Isolated HOME/XDG trees and PATH fakes for every command, init report-before-apply, setup foreign/managed/new paths, config-only/full/fleet flow, explicit fleet removal, non-TTY `--yes`, prompt cancellation, dry-run zero side effects, stdout/stderr separation, secret/material redaction, and exactly one JSON value followed by EOF.
   - TSV selector format and completion remain static/read-only.
5. **Native Windows CI**
   - Add a required `windows-latest` focused test step for `internal/sshhost`, affected `internal/fleet`, and CLI tests (DACL, junction/reparse, atomic replacement, Job Object cancellation, `ssh-keygen.exe`, PowerShell encoding/installers). Keep the broad existing Windows suite separately if still advisory, and compile the new packages for windows/arm64.
6. **Repository gates**
   - `make fmt`, `make vet`, `go test ./...`, `go test -race ./...`, `make build`, and `make e2e`.
   - Run focused real-OpenSSH config tests only against temporary HOME/config fixtures; never mutate the developer's real `~/.ssh` or contact a real remote in automated tests.
   - `make skill-sync`, inspect generated `internal/skill/dev-cli/references/commands.md`, then `make skill-check`.
   - `uv sync --frozen --extra docs`; source check (regenerate `docs/llms*.txt` if requested), strict MkDocs build, and site check per repository guidance.

## Explicitly deferred

- Key rotation/revocation/expiry, deletion from remote `authorized_keys`, private-key deletion, known_hosts repair/removal, alias rename/adoption, and automatic credential rollback.
- Arbitrary SSH directives, managed wildcard/Match blocks, ProxyCommand topology automation, certificates/CAs, forwards, and SSH config formatting/editor functionality.
- Vault/passphrase/password storage, direct Bitwarden CLI integration, private-key copying, and automatic password fallback.
- Chezmoi fleet, Tailscale/cloud inventory sync, bulk host onboarding, a dedicated SSH TUI, and background probing. Television can consume the generic TSV/JSON list in a separate dotfiles change without becoming a dev-cli dependency.
- Non-default/custom remote `AuthorizedKeysFile`, forced-shell policies, or ACL-incapable filesystems that cannot satisfy the verified POSIX/Windows installer contract; these fail with manual remediation rather than weaker behavior.
