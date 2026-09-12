# SSH key provider helpers

Status: deferred; current SSH route, registration and password-save work does not authorize vault migration or hardware provisioning.

Research updated: 2026-09-12.

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
