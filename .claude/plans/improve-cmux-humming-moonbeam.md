# SSH key providers: custom paths, named agents, hardware keys, vault-created keys → v0.2.38

## Context

The user asked whether a generated SSH key can have a custom name/location, and whether keys can live in
Bitwarden or on a YubiKey so no plaintext private key stays on the machine (plus other recommended tools).

Today (verified in code):
- CLI generation accepts `--key-path`/`--comment` and the terminal picker asks a path, but only inside
  `~/.ssh`, parent must pre-exist at 0700, always Ed25519 (`internal/sshhost/keys.go:1094-1160, 1446-1473`).
  The dashboard uses a fixed `~/.ssh/id_ed25519_dev_<alias>` with no comment.
- Agent-held keys can be installed from the terminal picker, but the managed fragment never records
  `IdentityAgent`, so later logins/proofs only work if the alias already points at that agent
  (`bootstrap.go:510-514` `ErrAgentPolicyMismatch`). The dashboard hides agent keys.
- Existing security-key stubs install fine; generating `-sk` keys, Secure Enclave keys, or vault keys is absent.
- Managed fragments (`# dev-cli managed SSH host v1`) allowlist only HostName/User/Port/ProxyJump/
  IdentityFile/IdentitiesOnly (`managed.go:206-268`).

Research (vendor docs, local checks on this Mac): Bitwarden desktop agent sockets per OS; desktop app can
generate Ed25519 in the vault but `bw` cannot generate (item type 5 needs caller-supplied key; no
`item.sshKey` template); 1Password `op item create --category ssh` generates in vault; Apple Passwords has
no SSH keys; macOS `/usr/lib/ssh-keychain.dylib` + `sc_auth` gives Secure Enclave `ecdsa-sk` (present here);
Apple OpenSSH lacks USB FIDO (YubiKey needs Homebrew openssh/libfido2); Secretive/KeePassXC/ssh-tpm-agent/
gpg-agent expose agent sockets. Local: bw 2026.3.0 (vault locked), Bitwarden agent socket absent (agent
disabled), no op/ykman, `golang.org/x/crypto v0.57.0` already a dependency (`ssh.MarshalPrivateKey`).

User decisions: new **v2 managed header**; auto-create only a **direct child of `~/.ssh`**; Bitwarden
**desktop handoff and experimental in-memory `bw` generation**; work **directly on main**, one commit per
phase, **single release v0.2.38** at the end.

## P1 — Custom key name, location, comment

- `internal/sshhost/keys.go` `planGeneratedKey`: return blocked-plan `Diagnostic`s with `Path`
  (`key_path_outside_ssh`, `key_path_unsupported_expansion`, `key_name_pub_suffix`, `key_parent_missing`,
  `key_parent_unsafe`) instead of bare errors. Add `KeyPlan.CreateParent` (JSON `create_parent`) when the
  missing parent is a direct child of `~/.ssh`; apply creates it with existing `ensurePrivateChild(SSHDir,
  name, false)`; deeper missing dirs stay blocked with a `mkdir -m 700` hint. `RevalidateKeySelection`
  accepts a still-missing CreateParent and rejects one that appeared unsafe.
- `internal/cli/ssh_keys.go` `chooseSSHKeyInteractive`: after `New key path`, ask `Key comment` → `options.comment`;
  bare names in prompts normalize to `~/.ssh/<name>` (flag semantics unchanged); show CreateParent in plan text.
- Dashboard: `tui.SSHKeyChoice` + `sshflow.OnboardRequest.KeyComment`; `chooseSSHKey` on a Generate choice
  opens a `keygen` dialog (path prefilled with default, comment); `finishSSHKeyGenerate` →
  `applyKeyChoice(path, true)`; `prepareSSHTUIOnboarding` passes `KeyRequest.Comment`.
- Tests: `internal/sshhost/keys_test.go` (subdir ok, CreateParent, nested blocked, changed parent rejected,
  `-C` argv), `internal/cli/ssh_setup_key_picker_test.go` (comment + bare name), `internal/tui/ssh_key_setup_test.go`.
- Commit `feat(ssh): choose generated key path and comment`.

## P2 — Named agent providers (Bitwarden, 1Password, Secretive, custom)

- New `internal/sshhost/agent_providers.go`: `AgentProviderID` (`bitwarden`, `1password`, `secretive`,
  `custom`), `AgentProviderObservation{Provider, Label, Socket, SocketPresent, AppDetected}`,
  `(*Service).ObserveAgentProviders(goos)` — `Lstat` only (socket mode, current-user owner, no symlinked
  parents), never executes. Socket table from research (Bitwarden App Store/.dmg/Linux/Snap/Flatpak;
  1Password macOS Group Containers + `~/.1password/agent.sock`; Secretive container socket). Windows shared
  pipe `\\.\pipe\openssh-ssh-agent` reported as `windows-openssh-pipe`, never attributed to a vendor.
- `key_types.go`: `KeyCatalogRequest.Agents []AgentSocketRef`, `KeyCandidate.Agent *AgentSocketRef`,
  `KeyPublishAgent` operation, `KeyRequest.PublicDestination`. `keys.go` `Catalog` reads each socket with the
  existing `keyAgentContext`/`readAgentKeys`; dedupe by fingerprint (alias IdentityAgent > request order >
  SSH_AUTH_SOCK, `agent_key_duplicate` diagnostic). `planAgentPublication`/`applyAgentPublication` write a
  public-only `~/.ssh/dev_agent_<provider>_<alias>.pub` (noop if identical, collision otherwise, revalidate
  agent key first, no-replace) so `IdentitiesOnly yes` can select the agent key.
- Managed v2: `ManagedDefinition` gains `IdentityAgent`, `SecurityKeyProvider`; `ManagedHeaderV2`.
  `RenderManaged` emits byte-identical v1 when both are empty, v2 otherwise; `ParseManaged` accepts v1 (new
  directives rejected) and v2; validation (IdentityAgent absolute/clean/no `% $ ~`; SecurityKeyProvider
  `internal` or absolute); `VerifyManagedEffective` compares both via `resolveIdentityAgent`.
- CLI: agent-key plans set `definition.IdentityAgent`, `IdentityFile` = published `.pub`, `IdentitiesOnly yes`
  in `ssh.go` (842-861) and `ssh_onboard.go` (404, 934). Setup order (ApplyKey → ApplyManaged → ResolveRoute →
  Bootstrap, `ssh.go:969-1024`) makes the proof see the new IdentityAgent. Foreign aliases block with
  `identity_agent_manual` printing the exact lines to add. Dry-run annotates "pending managed IdentityAgent".
  Flags `dev ssh setup --identity-agent bitwarden|1password|secretive|<abs path>` and
  `dev ssh key list --agent …` (repeatable). Picker labels `Bitwarden agent: <comment>`; installed-but-disabled
  providers shown as disabled items with enable guidance.
- Dashboard: `SSHKeyChoice` gains `Fingerprint`, `AgentProvider`, `AgentSocket`; `OnboardRequest` gains
  `KeyFingerprint`, `KeyAgentSocket`; `listSSHTUIKeys` keeps agent keys; `prepareSSHTUIOnboarding` re-catalogs and
  requires matching fingerprint+socket (fingerprint may replace KeyPath).
- Doctor/issues: `internal/cli/doctor.go` stat-only `sshAgentProviderChecks` (warn: app installed, agent
  socket absent); `internal/tuiissue/install.go` tools `bw`, `ykman` (+ `op` guidance); issue `agent_provider_disabled`.
- Tests: provider table per GOOS; v1 byte-identity, v2 round-trip, v1-with-IdentityAgent rejected; socket path
  with spaces; multi-socket precedence; publication noop/collision; managed-v2 proof passes
  (extend `keys_catalog_test.go:458`); foreign `identity_agent_manual`; TUI fingerprint selection.
- Commit `feat(ssh): select keys from named SSH agents`.

## P3 — Hardware-backed generation (YubiKey FIDO2, macOS Secure Enclave)

- Types: `KeyType` (`ed25519` default, `ed25519-sk`, `ecdsa-sk`), `SecurityKeyOptions{Provider ("internal" |
  "apple-secure-enclave" | abs path), Resident, VerifyRequired, Application}`; `KeyRequest.Type/SecurityKey`;
  `KeyPlan.SecurityKeyProvider/Keygen`.
- New `internal/sshhost/keygen_capability.go`: probe candidates (PATH, `/opt/homebrew/bin`, `/usr/local/bin`,
  `/usr/bin`) with `ssh-keygen -K` in a private staging dir, noninteractive env, empty stdin, short timeout;
  `internal security key support not enabled` ⇒ incapable. Also require the PATH `ssh` to be FIDO-capable,
  else `ssh_client_lacks_fido` (fleet/Herdr use PATH ssh).
- `planGeneratedKey`/`applyGeneratedKey`: `-sk` requires Interactive (touch/PIN); `Application` matches
  `^ssh:[A-Za-z0-9._-]{1,64}$` (default `ssh:dev-<alias>`); argv `-t … -O application=… [-O resident]
  [-O verify-required] [-w provider]`; exact algorithm check (`sk-ssh-ed25519@openssh.com` /
  `sk-ecdsa-sha2-nistp256@openssh.com`); keep `ssh-keygen -y`; `Provenance{SecurityKeyStub: true}`;
  "Touch your security key" notices.
- Secure Enclave (experimental, only if `/usr/lib/ssh-keychain.dylib` exists): check label with
  `sc_auth list-ctk-identities`; `sc_auth create-ctk-identity -l dev-<alias> -k p-256-ne -t bio` (retained side
  effect, reported, never rolled back); `ssh-keygen -w /usr/lib/ssh-keychain.dylib -K -N ""` in staging;
  choose the new stub by snapshot diff/fingerprint; no-replace publish; fragment v2
  `SecurityKeyProvider /usr/lib/ssh-keychain.dylib` + `IdentityFile`.
- Bootstrap already replays `SecurityKeyProvider` after ApplyManaged; `VerifyRequired` uses the interactive
  proof config (`renderSelectedConnectionConfig(..., interactive=true)`), terminal only.
- Flags `--key-type`, `--sk-provider`, `--sk-resident`, `--sk-verify-required`, `--sk-application`; pickers add
  "+ Generate on a security key (YubiKey)" / "+ Generate in Secure Enclave (Touch ID)" only when capable,
  otherwise disabled with reason + Homebrew openssh recipe; TUI keygen form gains type/resident/verify fields.
- Tests: fake-runner argv/blocking; probe stderr classification table; stub provenance + algorithm mismatch;
  Secure Enclave multi-stub; v2 SecurityKeyProvider; interactive proof for VerifyRequired.
- Commit `feat(ssh): generate security-key backed SSH keys`.

## P4 — Keys created in a vault

- New `internal/sshvault` package (AGENTS.md architecture line), reusing `sshcredential.CommandRunner`/
  `NativeRunner` (timeouts, stdin bytes, capped stdout, wipe). Types `VaultKeyRequest{Provider, Vault, Title}`,
  `VaultKeyRef{Provider, ItemID, Fingerprint}`, errors `ErrLocked`, `ErrUnknown`, `ErrUnsupportedVersion`.
- **1Password**: `op --version` ≥ 2.20, `op whoami` (fail ⇒ locked), `op vault list --format json`;
  `op item create --category "SSH Key" --title T --vault V --ssh-generate-key ed25519 --format json` — parse only
  the ID (runner wipes buffer), read `public key` field, fingerprint via existing `parsePublicKeyRecord`;
  ambiguous ⇒ `ErrUnknown` with title, no retry. Verify fingerprint on the 1Password socket (P2); missing ⇒
  `vault_key_not_in_agent` (agent.toml vault list, never edited by dev); continue with P2 publication + v2 fragment.
- **Bitwarden desktop handoff**: picker "+ Create in Bitwarden desktop…" — require agent socket (else P2
  disabled guidance), snapshot agent fingerprints, show steps (New → SSH key; Settings → Enable SSH agent),
  wait for Enter, offer only new fingerprints, continue with P2.
- **Bitwarden in-memory `bw` generation (experimental)**: `bw status` must be `unlocked`; `BW_SESSION` only from
  the environment (never argv/prompt). Generate with `crypto/ed25519` + `ssh.MarshalPrivateKey` (PEM in memory),
  public line via `ssh.MarshalAuthorizedKey`, fingerprint `ssh.FingerprintSHA256`; build item JSON from
  `bw get template item` (type 5, `login` null, `sshKey{privateKey, publicKey, keyFingerprint}`, dev owner
  field), base64 over stdin to `bw create item`; keep the exact returned ID; best-effort zero byte slices; never
  write key bytes to disk, logs, errors or JSON output. Outcome: created + visible in Bitwarden agent ⇒
  continue with P2; created but not yet in agent (sync) ⇒ report item ID and "run bw sync / unlock desktop, then
  select from agent"; ambiguous ⇒ `ErrUnknown`, no retry. Gate behind explicit selection
  ("+ Generate into Bitwarden vault (experimental)") and a confirmation that the key briefly exists in dev's memory.
  **Verification spike first**: with the user's vault unlocked, create a throwaway item, confirm the JSON shape
  and that the desktop agent serves it, then delete it (user-approved); if the shape fails, ship handoff only.
- Tests: fake `op`/`bw` runners (version gate, locked, create JSON, private field never in logs/errors,
  unknown outcome, created-not-in-agent); handoff fingerprint diff; in-memory key parses and matches fingerprint.
- Provider recommendations table in `docs/guides/ssh-hosts.md` (+zh-TW): 1Password, Bitwarden, Secretive, macOS
  Secure Enclave, YubiKey FIDO2 (libfido2 OpenSSH), YubiKey PIV (not supported), KeePassXC, ssh-tpm-agent,
  gpg-agent, Windows Hello (guidance) — where the key lives and dev support. Update
  `backlog/ssh-key-provider-helpers.md` status.
- Commit `feat(ssh): create SSH keys in 1Password or Bitwarden`.

## Docs/skill per phase

`docs/guides/ssh-hosts.md` + `.zh-TW.md` (authority table/allowlist now v1/v2, key sections, provider table),
`docs/reference/commands-config.md` pair (new flags), `internal/skill/dev-cli/references/ssh-hosts.md`,
`internal/help/topics/ssh.md`/`tui.md`, AGENTS.md (`sshhost` named agents + v2 directives, `sshvault`),
CHANGELOG `[Unreleased]`; `make skill-sync && make skill-check`; regenerate `docs/llms-full.txt`.

## Verification

```bash
export PATH=/Users/zhouhanru/.local/share/mise/installs/go/1.26.4/bin:$PATH   # homebrew go mismatches GOROOT
gofmt -l internal cmd; make vet
go test ./internal/sshhost ./internal/sshvault ./internal/cli ./internal/tui   # per phase
go test -race -timeout 20m ./...                                              # before release
make skill-sync && make skill-check && make build && make e2e
uv run python scripts/check-docs.py --source --generate-llms && uv run python scripts/check-docs.py --source \
  && uv run mkdocs build --strict && uv run python scripts/check-docs.py --site site
```
Manual on this Mac: P1 `./dev ssh setup <alias>` generate into `~/.ssh/work/…` with comment (cancel at review);
P2 enable Bitwarden desktop SSH agent → `./dev ssh key list --agent bitwarden`, setup writes v2 fragment with
IdentityAgent; `./dev doctor` warns while agent disabled; P3 capability probe reports Apple ssh-keygen as
FIDO-incapable and Secure Enclave available (hardware generation itself needs user touch — user-run check);
P4 Bitwarden spike with an unlocked vault; `op` paths tested with fakes only (op not installed).

## Release

After P4 on main: `chore(release): prepare v0.2.38` (CHANGELOG section + compare links, README pins,
AGENTS.md baseline), `git push origin main`, `git tag -a v0.2.38` on that commit, push tag, watch `release.yml`
(poll `gh run view`, low memory) and confirm assets + Homebrew formula. Commits stage specific files only
(never the user's `.claude/plans` / `.specstory` changes).

## Risks

YubiKey/Touch ID/op cannot be exercised here (fakes + user-run checks); agent approval prompts may time out
`ssh-add -L`/proofs (treated as unavailable/unproven); `-K` probe depends on stderr wording; PATH `ssh` may lack
FIDO; Windows pipe is shared (documented, not auto-selected); `bw` sshKey JSON shape unverified (spike gates it);
v2 fragments make ≤ v0.2.37 dev refuse setup/remove on those aliases (documented).
