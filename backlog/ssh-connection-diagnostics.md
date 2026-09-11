# Layered SSH connection diagnostics

**Status**: shipped in v0.2.26.
**Effort**: L
**Related**: [TODO](../TODO.md), [SSH key providers](ssh-key-provider-helpers.md),
[TCP succeeds while SSH times out](../pitfalls/ssh-times-out-but-tcp-connects.md).

## Context

2026-09: diagnosing a macOS client using an SSLVPN alongside a proxy TUN and
Tailscale exposed a gap between "route exists", "TCP connects" and "SSH works".
A target's SSH banner was reachable, while ordinary OpenSSH timed out before
authentication. A controlled IPQoS comparison isolated a DSCP-dependent failure.
The subsequent host-key and user-key checks were separate steps, not evidence
that the earlier timeout was a credential problem.

All incident identifiers in these notes are synthetic. Documentation-reserved
addresses, generic aliases, account names and key paths replace the originals;
no real host fingerprints, config comments or raw transcripts are included.

## Existing surfaces and investigation

- `dev ssh show <alias>` exposes static declarations and effective `ssh -G`
  values. `dev ssh probe <alias>` performs a fresh noninteractive ordinary login.
  Reuse [the CLI adapters](../internal/cli/ssh.go) and
  [sshhost probing](../internal/sshhost/probe.go), rather than implementing a
  competing SSH config parser or changing probe's existing contract silently.
- OpenSSH remains authority for Includes, Match conditions and first-obtained
  options. Effective evaluation can execute configured Match exec behavior;
  it belongs in explicit diagnosis, not passive inventory.
- A destination-specific route reached the SSLVPN interface. Plain TCP 22
  repeatedly connected and returned `SSH-2.0-OpenSSH_for_Windows_9.5`.
- Default SSH timed out twice; its effective IPQoS was `ef cs0`. TCP probes
  with TOS `0xb8` (EF DSCP) timed out twice, while `0x0` connected twice.
  SSH with `IPQoS=none` reached host-key verification and subsequently logged in
  with the configured public key after matching an existing trusted identity.
- These observations identify a DSCP-sensitive path, not the specific component
  responsible. Client, tunnel implementation, gateway and intermediate devices
  remain possible locations. Do not generalize this case to every VPN failure.

## Proposed capability

Use `dev ssh diagnose <target>` as the working command name, accepting an alias
or explicit host. Provide human output and a structured JSON result, both with
clear stage outcomes, evidence, uncertainty and a suggested next action.
Keep policy in the SSH domain layer and reuse runners, redaction, cancellation
and effective-config facilities from `sshhost`.

Diagnosis proceeds through effective destination/user/port/identity selection,
DNS when relevant, route/interface observations, TCP connect, banner, SSH
handshake, host-key verification and noninteractive user authentication.
ProxyJump/ProxyCommand and interface scoping must be reported: a direct TCP
comparison can exercise a different path from configured SSH. Unsupported OS
route collectors or opaque proxy paths report unknown rather than success.

Use bounded per-stage and total deadlines; distinguish connection refusal,
connect timeout, banner timeout, host-key rejection, authentication denial and
successful login. Missing credentials, a locked agent and a missing/changed
host key must not collapse into "VPN broken". Authentication must use a fresh
connection, not a pre-existing ControlMaster session.

The QoS comparison is an explicit optional active probe. When plain TCP and
SSH disagree, compare ordinary SSH with a one-invocation `IPQoS=none` override;
where supported, an additional DSCP-controlled TCP test can strengthen the
evidence. Never change global QoS or automatically edit SSH/network settings.
ICMP is optional supporting evidence; no Echo Reply is not a VPN-down verdict.

Suggested remedies must preserve host-key checking. An alternate HostKeyAlias
is only appropriate after verifying that the presented host key matches the
already trusted identity of the intended same host. Unknown or different keys
require a trustworthy identity check, not `StrictHostKeyChecking=no`.

## Options and implementation boundary

Start with the existing Go SSH services plus small OS-specific route collectors.
The separate Python multi-VPN diagnostic experiment is a source of lessons, not
a new runtime dependency or a reason to duplicate its entire policy model.
Keep diagnosis independent from SSH setup, key installation and fleet registration.
Expose it to the bundled skill as a troubleshooting workflow; do not execute
network probes whenever inventory opens or an unrelated command fails.

Detailed public JSON schema and cross-platform collector coverage must be designed
when this item is scheduled. The first implementation should cover the current
macOS reproducer while retaining explicit unsupported-stage outcomes elsewhere.

## Acceptance scenarios

- Alias and literal IP select different users/identities; explain the difference.
- Correct route but refused/blackholed port; working TCP but stalled banner.
- TCP succeeds, default SSH times out, QoS override succeeds; report correlation
  without attributing the fault to a particular VPN vendor or gateway.
- Host key unknown, changed, or independently matched to a trusted alternate
  identity; preserve strict checking in each case.
- Correct host key but rejected user key, unavailable agent, or password-only
  server; no interactive credential prompt in a noninteractive probe.
- ProxyJump/ProxyCommand, scoped routes, tunnel renumbering, incomplete capture,
  timeout and cancellation; uncertain evidence stays unknown.
- Hermetic fixtures for stage classification and report redaction; live probes
  opt in. Never place private keys, usernames, real endpoints or raw SSH logs in
  test fixtures or reports intended for publication.

## References

- [OpenSSH IPQoS and host-key settings](https://man.openbsd.org/OpenBSD-7.7/ssh_config.5)
- [Mihomo TUN behavior](https://wiki.metacubex.one/config/inbound/tun/)
- [Tailscale DNS](https://tailscale.com/docs/reference/dns-in-tailscale)
