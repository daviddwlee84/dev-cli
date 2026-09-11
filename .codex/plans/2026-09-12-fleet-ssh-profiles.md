# Fleet SSH profile import and credentials

Approved implementation: selected fleet-source inventory, local native
ProxyJump import, explicit remote-local SSH connection, per-hop key bootstrap,
standalone public companion derivation, and controller-local reusable SSH
password saving (Yes/No/Never; system credential store default, Bitwarden optional).

## Invariants and interfaces

- `discover --source fleet --host NAME` selects sources only; no remote fleet
  recursion. Static inventory never evaluates SSH config or queries an agent.
  Selected resolve/key operations are explicit. `list --fleet` consumes cache.
- `setup LOCAL --from fleet:HOST/ALIAS` previews native local ProxyJump; segments
  are encoded once, and opaque profile IDs support automation. Dry-run uses cache
  only. Remote identity/agent/known_hosts paths never become local paths.
- `connect ALIAS --on fleet:HOST --key-id FINGERPRINT` is explicit remote-native
  interactive execution, with freshly re-resolved source/key handles. No remote
  config edits, private-key transfer, agent forwarding or replay of execution.
- `key derive PRIVATE_KEY --apply` reuses native guarded missing-companion
  generation; default is plan only and existing .pub is not overwritten.
- Source identity includes remote dev UUID, account and config scope. First
  explicit capability may initialize its own remote UUID. Existing fleet pins
  are enforced; observations never automatically merge machines or write pins.
- Fix native comma-list ProxyJump semantics before imports. Dependency cycles
  differ from finite repeated invocations; shared prefixes are reusable only for
  identical source/route/user/port/credential/trust. Network scope distinguishes
  equal private addresses. Configuration writes revalidate reviewed graph first.
- Keep local host-key policy. Block ambiguous cross-network endpoints and require
  an explicitly configured local profile rather than inventing trust namespaces.
- Default one selected local key, with per-hop overrides; working jumps retain
  existing auth unless explicitly changed. Selected-key proof remains fresh.
- Discovery is BatchMode except an explicit configured password source. Explicit
  bootstrap/connect may interact. Only dev-collected reusable password input with
  successful password-only proof can be offered for saving, never OTP/native
  terminal/passphrase captures. Saved policies/references live in private config;
  secret material stays in the selected native vault.
- Replace inherited-FD askpass with protected Unix-socket/Windows-pipe IPC. Each
  hop has independent broker context and frozen route. Internal streaming
  connectors are temporary only; no ControlMaster or command retries, and
  cancellation cleans the whole process tree.

## Delivery and verification

Implement route/derive, protocol/cache, import/provenance, remote connection,
then credential broker/providers. Validate stale sources, scoped identity,
repeated imports, native route parity, per-hop credentials, binary tunnels,
native test sshd, exit/cancellation behavior, strict protocol/old-schema
compatibility, secret-safe outputs, optional-provider failures, cross-platform
builds, race/vet, skill synchronization and paired documentation checks.

Provider migration is future work: SSH key import to Bitwarden, distinct YubiKey
FIDO2/PIV/OpenPGP workflows, and native macOS Passwords export/import. Record them
in the existing provider backlog, with fingerprint verification and separate
source-key cleanup; do not execute migrations in this implementation.
