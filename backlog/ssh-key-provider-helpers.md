# SSH key provider helpers

Status: deferred; machine registration and configuration organization ship first.

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
