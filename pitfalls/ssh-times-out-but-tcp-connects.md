# SSH times out but TCP port 22 connects over a VPN

**Symptoms**: `Operation timed out`, `Connection established.`,
`Host key verification failed.`, `ipqos ef cs0`, TCP banner succeeds but SSH fails.
**First seen**: 2026-09
**Affects**: observed on macOS over an SSLVPN with concurrent proxy/Tailscale
interfaces; target reported `OpenSSH_for_Windows_9.5`. No general affected-version
range or vendor-specific defect has been established.
**Status**: per-host workaround verified; exact failing network component unknown.

## Symptom and evidence

Identifiers below are documentation examples. Error wording is retained, but
addresses and other identifying fields have been replaced.

```text
debug1: Connecting to 192.0.2.30 [192.0.2.30] port 22.
debug1: connect to address 192.0.2.30 port 22: Operation timed out
ssh: connect to host 192.0.2.30 port 22: Operation timed out
```

The active host route selected the SSLVPN interface, and ordinary TCP socket
probes repeatedly received `SSH-2.0-OpenSSH_for_Windows_9.5`.

| Controlled test | Observation |
|---|---|
| Ordinary SSH, effective `IPQoS ef cs0` | Two connect timeouts |
| TCP socket with IPv4 TOS `0xb8` | Two timeouts |
| Same destination/port, TOS `0x0` | Two successful connects and banners |
| SSH with `IPQoS=none` | Handshake reached host-key verification |
| Same override plus verified host identity and configured user key | Public-key login and remote exit succeeded |

The network layer was not the only configuration detail. A literal-IP invocation
used the local username and default identities, whereas the configured alias
selected the intended remote user and dedicated identity. The internal IP also
had no known_hosts entry, although the same machine's external entry was trusted.

## What this establishes

The path was sensitive to DSCP marking. This explains the repeated difference
between a plain TCP probe and default OpenSSH without blaming user authentication.
It does not identify whether the VPN client, gateway, firewall or another device
discarded the marked packets. TUN route selection alone is not packet evidence,
and the point-to-point interface address need not equal a server-side assigned
VPN address or the source seen by the remote host after translation.

Ping failure is independent: ICMP may be filtered even when an application port
works. Test the actual service and distinguish TCP connect from SSH authentication.

## Workaround and diagnostic sequence

Check the alias's effective settings and use a temporary QoS override first:

```sh
ssh -G corp-windows-internal
ssh -o IPQoS=none -o BatchMode=yes -o ConnectTimeout=8 \
    -o ConnectionAttempts=1 -o ControlPath=none \
    -o StrictHostKeyChecking=yes -o UpdateHostKeys=no \
    corp-windows-internal exit
```

If a new host identity stops the probe, that is a separate stage. In this case
the presented ED25519 fingerprint exactly matched the existing external entry.
Only after confirming the endpoint is the same intended machine, reuse that
trusted lookup identity with `HostKeyAlias`. Never bypass verification or copy a
new unverified scan into known_hosts as a way to make the test pass.

The resulting **example** per-host configuration is:

```sshconfig
Host corp-windows-internal 192.0.2.30
  HostName 192.0.2.30
  User remote-user
  Port 22
  IdentityFile ~/.ssh/example_ed25519
  IdentitiesOnly yes
  IPQoS none
  HostKeyAlias [198.51.100.10]:13022
```

The HostKeyAlias assumes that external identity was already verified; it is not
a generic setting to paste into another environment. Apply the IPQoS workaround
only to affected hosts. On the observed client, `ssh -G` normalizes `IPQoS none`
to `ipqos none none`; validators should account for both traffic classes.

After editing, check effective settings for the alias, literal IP and unrelated
hosts. Preserve first-obtained option precedence, Include order, file permissions
and a private backup. Prove login through a fresh connection with strict host-key
checking; do not mistake reuse of an existing master connection for a new success.

## Prevention and privacy

Record stage-specific outcomes, compare one network variable at a time, and
separate observed behavior from inferred causes. Raw config comments and debug
logs can contain sensitive material; extract allowlisted fields rather than
copying complete files into a backlog or issue. Redact real public/private IPs,
domains, account names, machine-specific paths, key fingerprints and credentials.

Related: [SSH diagnosis backlog](../backlog/ssh-connection-diagnostics.md),
[SSH Include precedence pitfall](ssh-aliases-missing-after-second-include.md),
[OpenSSH reference](https://man.openbsd.org/OpenBSD-7.7/ssh_config.5).
