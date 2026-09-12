# SSH machine management implementation

Approved scope: optional Tailscale discovery, explicitly bounded LAN SSH discovery
with disposable cache, durable controller-local canonical machine registry,
multi-host/multi-alias setup, and a separate SSH dashboard view. Fleet retains its
repository/task role.

## Decisions

- Use native OpenSSH aliases for ordinary sshd and existing keyless Tailscale SSH.
  Never enable a remote SSH service, change tailnet policy, or replace transport
  with an automatically generated ProxyCommand.
- Registry UUIDs group source-owned connection profiles. They are not remote
  `fleet.machine_id` pins. Adoption/link/unlink/merge is explicit and plan-first;
  listing does not create identities. Source drift remains stale/unresolved.
- Default SSH list stays static. `--tailscale` explicitly reads local status;
  `--lan` only includes cached LAN results. Names are suggestions, never merge
  proof; preserve separate aliases, users, ports, keys and Herdr sessions.
- LAN requires selected on-link IPv4 scope: at most 256 addresses, 16 ports,
  4096 endpoints, 32 workers, 1-second connect/banner deadlines and 30-second
  total deadline. Cache TTL is five minutes, never authentication authority.
- New setup defaults to config only. Keys are explicit; `--auth existing` skips
  key installation. Herdr/fleet registration is opt-in; native approvals stay
  native. Tailscale SSH's `none` authentication cannot verify a selected key.
- Wizard defaults to an editable machine-name alias and one confirmed remote
  user, supports multiple independently configured aliases, and prefers the
  Tailscale IPv4 address (IPv6 fallback, explicit MagicDNS override).
- Append SSH as dashboard view eight without changing existing 1–7 bindings.
  Preserve custom tools using 8. View entry/refresh is local/cache-only; network
  discovery and connection workflows are explicit foreground actions.

## Validation

Use fake runners/dialers/resolvers and isolated HOME/XDG fixtures. Verify
identity transactions and stale plans, key proof classification, bounded
discovery/cancellation/cache privacy, batch partial ledgers, JSON compatibility,
passive TUI behavior, and exact-profile revalidation. Run Go formatting, vet,
race tests, E2E, Windows compile gates, skill sync/check and strict paired docs.
Keep the matching session transcript with the implementation; preserve unrelated
machine-local SpecStory statistics.

When isolating host Git configuration for the full race suite, supply synthetic
Git author and committer names/emails as well: existing repository-creation
fixtures rely on a configured identity. The original isolated run completed
without a race or timeout; its 16 identity-only failures passed with that identity
provided. No lifecycle fixture or host Git configuration was changed.
