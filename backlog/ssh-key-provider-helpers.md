# SSH key provider helpers

Status: partially implemented in the unreleased v0.2.38 work; existing-key migration, endpoint-attested Bitwarden writes and automatic Secure Enclave provisioning remain separate/unresolved. Do not mark the milestone shipped before its verification and release gates close.

Research updated: 2026-09-14.

## Remaining safe-automation gates

The approved key-provider increment covers custom local destinations, named SSH
agents, explicit FIDO generation and separately approved vault creation. It does
not authorize importing existing private keys, deleting vault items or credentials,
changing provider configuration, or executing live hardware/vault tests implicitly.

- **Secure Enclave identity binding:** native `sc_auth create-ctk-identity` is
  documented, but there is no verified machine-readable creation receipt or stable
  listing contract. `list-ctk-identities -t ssh` changes the hash format; it is not
  an SSH-only filter. Human tables contain blank fields, arbitrary labels and
  localized dates. An original public example confirms one SHA-1 EC-point /
  SHA-256 SK-wire fingerprint mapping, not a general parser guarantee. Establish
  an exact identity reader before creation; unknown/ambiguous input must fail
  before any hardware effect. Never select the last row or rely on label uniqueness.
  `ssh-keygen -K` writes resident handle files and is never a passive capability
  probe. Any future import must filter one verified identity using child-scoped
  `KEYCHAIN_CERTIFICATES`, use private staging, and retain/report uncertain effects.
- **Bitwarden endpoint-attested mode:** pinned CLI 2026.3.0 reports only a base
  `serverUrl`, null for both US/EU cloud accounts. A non-null base may coexist with
  independent API/identity overrides; read-only `config server` does not prove the
  effective endpoints. Do not label the base URL as a verified destination or
  mutate configuration to discover it. An explicitly approved native-context mode
  is a different authority: it delegates endpoint configuration to a reviewed
  native profile and keeps endpoint status unverified. It must separately approve
  transient memory generation and native-context delegation, freeze the actual
  command environment, bind/revalidate user/session/profile/tool context, use
  no-interaction commands, and retain exact item receipts on uncertainty. A
  `BW_SESSION` hash is comparison material, not an account/server identifier.
  Internal native configuration is not made transactional by pre/post checks.
- **Native Windows:** explicit provider pipes, hardware tools and vault profiles
  need verified identity/ACL/reparse-point contracts. Cross-compilation is not a
  substitute for dedicated native gates. Existing native OpenSSH authentication
  remains a separate supported boundary.
- **Fleet-source imports:** controller-selected agents/hardware provider settings
  must become part of immutable shared-hop authority before those generation paths
  can be enabled. Never copy remote provider paths or append new authentication
  intent after an import plan was reviewed. Registration *to* fleet is distinct
  from importing connection profiles *from* fleet.

Sources: [original CTK example](https://gist.github.com/arianvp/5f59f1783e3eaf1a2d4cd8e952bb4acf),
[`sc_auth` manual](https://keith.github.io/xcode-man-pages/sc_auth.8.html),
[`ssh-keychain` manual](https://keith.github.io/xcode-man-pages/ssh-keychain.8.html),
[pinned Bitwarden status](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/apps/cli/src/commands/status.command.ts),
[pinned endpoint resolution](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/libs/common/src/platform/services/default-environment.service.ts).
No live keychain enumeration, hardware enrollment or vault item creation was used
for this research.

## Existing migration research

The existing chezmoi `ssh-to-bitwarden` helper already imports SSH key items and
its shell configuration selects Bitwarden's agent. Reuse a native handoff before
building another vault writer. A future implementation must show key fingerprints
and provider state without logging key bytes, vault unlock data or credentials.
Audit the existing helper's name-based duplicate detection and Secure Note fallback
before treating it as a dev-supported credential transfer protocol.

YubiKey is a separate design: FIDO2-generated hardware credentials, PIV imports
and OpenPGP agent integration have different lifecycle and recovery semantics.
Do not present them as equivalent destinations for uploading an existing key.
Public key installation can reuse sshhost once a provider exposes a supported
public identity and ordinary OpenSSH authentication works.

Suggested first increment: agent/socket diagnostics, public fingerprint listing,
provider-specific prerequisites and an explicit native-tool handoff. Vault import,
hardware provisioning and credential revocation need their own reviewable plans.
No new provider execution belongs in passive SSH or fleet inventory.

Sources: [Bitwarden SSH agent](https://bitwarden.com/help/ssh-agent/),
[Yubico SSH approaches](https://developers.yubico.com/SSH/).

Herdr integration also has an upstream opportunity: metadata-only registration
and structured dry-run/apply outcomes would avoid native preparation during add.
The implemented adapter uses the current native CLI; it does not edit Herdr's
private endpoint catalog or silently answer server-replacement approvals.


## Distinct future workflows

The user requested these as later helpers, not additions to the current SSH
password-save feature. Default to explicit native-tool handoff and metadata
preview. An approved transfer may process private material in memory, but normal
setup/discovery must retain its no-private-key-transfer contract. Keep names,
public fingerprints, destination identity and verification results reviewable;
never log key bytes, passphrases, vault sessions or exported account passwords.

### Existing SSH private key to Bitwarden

Bitwarden accepts OpenSSH and PKCS#8 private-key import; its documented agent
supports Ed25519 and RSA SHA-256/SHA-512, and PuTTY input is unsupported. The SDK
can decrypt supported encrypted input with an explicit passphrase and normalize
it to OpenSSH; vault representation is encrypted. A future helper must detect
format/algorithm support rather than promise arbitrary PEM conversion.

The CLI documents SSH item type 5, but its current template switch has no SSH-key
case. Do not assume `bw get template item.sshkey` exists, or fall back silently to
Secure Note after template failure. Audit the existing chezmoi helper, pin the
supported CLI schema, use stdin rather than secret argv/clipboard where possible,
and retain the exact returned item ID and public fingerprint. Duplicate names
are not identity. Verify provider agent signing before offering separate local
key-file cleanup; import never implies source deletion or remote revocation.

Sources: [Bitwarden About SSH](https://bitwarden.com/help/about-ssh/),
[SDK import](https://sdk-api-docs.bitwarden.com/bitwarden_wasm_internal/ssh/fn.import_ssh_key.html),
[CLI schema](https://bitwarden.com/help/cli/),
[CLI template source](https://raw.githubusercontent.com/bitwarden/clients/main/apps/cli/src/commands/get.command.ts).

### YubiKey: generate versus import

FIDO2 SSH creates a new `ed25519-sk` or `ecdsa-sk` credential and a local handle;
it is not a route for uploading the same ordinary OpenSSH private key. PIV can
import supported asymmetric private keys into selected slots, while OpenPGP uses
its own existing-PGP-key import flow. Supported algorithms depend on firmware
and application. Occupied PIV slots can irrecoverably replace an existing key,
so device/firmware/slot inventory, explicit replacement scope and recovery need
their own reviewed plan. Do not label all three paths “move this SSH key”.

Sources: [Yubico FIDO2 SSH](https://developers.yubico.com/SSH/Securing_SSH_with_FIDO2.html),
[PIV compatibility](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-apps-piv.html),
[PIV import/replacement](https://docs.yubico.com/yesdk/users-manual/application-piv/commands.html),
[OpenPGP import](https://developers.yubico.com/PGP/Importing_keys.html).

### Apple Passwords to Bitwarden

On Mac, use Passwords' native selected/all-password CSV export and Bitwarden's
Safari/macOS importer. CSV is unencrypted and has export exclusions (including
Wi-Fi, some shared-group credentials and Sign in with Apple); it is not an SSH-key
or passkey archive. Bitwarden imports do not deduplicate repeated imports.
A helper should show selected scope, record completion/uncertainty and guide
explicit cleanup of the export artifact, without claiming secure erasure.

Bitwarden separately documents app-to-app Credential Exchange from Apple
Passwords on iOS 26+, including passwords and passkeys. This research did not
establish macOS CXP support: detect native capability at implementation time,
otherwise describe the supported CSV route. Never substitute a Keychain dump or
scripted vault read for the user's native export authorization. Website passkeys,
account passwords and SSH private keys are different data types and migration
contracts.

Sources: [Apple Mac export](https://support.apple.com/guide/passwords/export-passwords-to-a-file-mchl35b12625/mac),
[Bitwarden macOS migration](https://bitwarden.com/help/import-from-safari/),
[Credential Exchange import](https://bitwarden.com/help/import-data/).
