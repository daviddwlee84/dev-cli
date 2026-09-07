---
description: Copy, move, mirror, and reconstruct selected agent configuration while keeping credentials and ownership separate.
authority: project-and-upstream
status: evolving
verified_on: 2026-09-07
tested_with: skills 1.5.23 compatibility fixtures and synthetic MCP 2025-06-18 servers
---

# Agent configuration interoperability

Use native agent files as runtime configuration. A transfer plan names the exact
source, destination, scope and changes; `apply` checks those observations again.
Ordinary copies and relative skill links do not require dev at agent startup.

## Choose the relationship

| Need | Operation |
|---|---|
| One skill shared by agents in a checkout | Per-skill `mirror` from `.agents/skills` |
| Reuse a skill in another repository | Verified upstream `prepare`, then `install` |
| Independent local content | `copy`; future source edits do not propagate |
| Relocate selected content | `move`; publish and verify before retiring the source |
| One MCP definition with generated client projections | `mirror`, followed by reviewed `refresh` |
| Shared instructions | `CLAUDE.md -> AGENTS.md`, or a Claude `@AGENTS.md` import |

Keep each repository independently reproducible. Commit its native configuration,
relative links, and project `skills-lock.json`; commit installed skill contents
too when offline reproduction is required. Cross-project mirrors are explicit
dependencies on another local checkout, not portable package installations.
Putting a skill in `~/.agents/skills` enables user-wide discovery in Codex; a
private skills Git repository is a better source for selectively installed
skills. [Codex skills](https://developers.openai.com/codex/skills/)

## Skills

```bash
dev skill transfer plan example --from-agent universal --to-agent claude-code --mode mirror
dev skill transfer apply --plan <id>

dev skill transfer prepare example --from-repo api --to-repo web
dev skill transfer plan example --from-repo api --to-repo web --mode install --prepared <id>
dev skill transfer apply --plan <id>
```

Only `prepare` invokes the directly installed `skills@1.5.23` provider and may
fetch. It uses a private staging directory and checks the provider version,
source identity, native lock, and installed/staged content. Plan/apply do not run
an installer. Missing installed content can be reconstructed from a supported
lock: select the same checkout for source and destination. An existing matching
lock is retained byte for byte.

Provider version, lock schema and skill content are separate compatibility
checks. Project v1 uses a folder content hash; global v3 uses a Git tree hash.
Unsupported versions/fields, non-ASCII hash ordering, changed upstream content,
and raw commit refs that the provider cannot clone are reported explicitly.
There is no fallback to latest, npx, or an independent copy.
The legacy `skill add` wizard and `skill update` remain explicit native provider
operations; they are not frozen restores.
[Project lock](https://github.com/vercel-labs/skills/blob/v1.5.23/src/local-lock.ts),
[global lock](https://github.com/vercel-labs/skills/blob/v1.5.23/src/skill-lock.ts),
[upstream install](https://github.com/vercel-labs/skills/blob/v1.5.23/src/install.ts)

Moves verify lock-owned contents, preserve native destination membership, then
retire matching source membership and exact native symlinks. Independent agent
copies remain untouched. Active cross-project mirrors block canonical retirement
until those consumers are migrated. Local copies do not invent upstream hashes.

## MCP: five explicit adapters

```bash
dev mcp transfer plan --server grafana --from-agent claude-code --to-agent codex --mode copy
dev mcp transfer apply --plan <id>

dev mcp transfer plan --server grafana --from-agent claude-code --to-agent codex \
  --mode mirror --secret-env-file .claude/settings.local.json
dev mcp transfer apply --plan <id>
dev mcp transfer refresh <relation-id>
```

The five adapters are Claude Code, Codex, Cursor, Gemini CLI and OpenCode.
Project and user/global are explicit scopes; `--from-repo` and `--to-repo`
select exact checkouts. Native user-home overrides remain supported. Claude's
project-local entries inside `~/.claude.json`, plugins, system policy and managed
config are inventory surfaces, not transfer scopes.

Supported conversion is deliberately the common subset: stdio command/arguments,
compatible environment references, and explicit Streamable HTTP URLs/header
references. Ambiguous remote declarations require `--transport streamable-http`.
Unknown fields, disabled source declarations, applicable source tool restrictions,
SSE, OAuth configuration/token stores and dynamic helpers are not silently dropped.
OpenCode's classic `mcp.<name>` schema is supported; v2 `mcp.servers` requires a
separate profile.

Writers patch the selected JSON/JSONC member or TOML table ranges. Unrelated
settings and comments survive. A selected inline/dotted TOML definition or an
inline `mcp_servers` container requires a native edit. An equivalent existing
stanza is a no-op without ownership; use `--adopt --mode mirror` to adopt it.
Changing a managed source also requires explicit adoption.

MCP mirror updates are reviewed projections, not background synchronization.
Ownership is per stanza: editing an unrelated model setting or adding a second
server does not invalidate the first relation. Undo restores only selected
stanzas and preserves later unrelated edits; empty native container files may
remain.

### Credentials and the optional launcher

Keep secret values outside version-controlled content. Configuration carries
environment names, not copied tokens. Cross-scope credential use requires
explicit `--bind SERVER_ENV=PROCESS_ENV` mappings. The preview lists references
without their values.

Native mappings are preferred. Codex forwards same-name stdio variables through
`env_vars` and uses its native HTTP reference fields. When a stdio mapping needs
a local JSON `env` source or variable renaming, choose `--bridge` or
`--secret-env-file`. The latter is relative to the destination scope and must be
an owner-private regular file. This is compatibility for an existing local file;
dev does not create secret files or copy them to another project.

The launcher resolves selected references at launch: process environment first,
then the selected local `env` object, then an explicit fallback. An empty value
is set, not missing. Rotation takes effect at the next launch. Other model/provider
variables are not passed to the server. Source policy files may be parsed during
planning, but credential resolution and injection happen only during launch.

The launcher is host-local, binds an exact checkout and executable, and requires
the referenced dev installation. Moving/cloning needs a new binding. Source
execution/reference changes require refresh; ordinary secret rotation does not.
A launcher cannot inject credentials into an already-running HTTP client, and a
launcher-backed declaration cannot be moved away from its source.

Environment variables are not isolation from other processes owned by the same
user. An existing secret manager can inject them before client startup, for
example with [1Password runtime injection](https://developer.1password.com/docs/cli/secrets-environment-variables/).
Native Keychain/Vault/YubiKey integrations are not implemented. OAuth login
stores and private-key files are never migration payloads.

### Optional connection evidence

```bash
dev mcp transfer check <applied-id> --json
```

This explicitly starts a stdio server or contacts its HTTP endpoint, negotiates
MCP 2025-06-18 and sends the initialized notification. It does not call application
tools. The result distinguishes `configured` from `initialized`; authentication
and native-client loading stay `not-checked`. Ordinary list/plan/apply do not
probe servers, install tools or contact endpoints.
[MCP lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle)

## Instructions and optional recipes

```bash
dev instructions transfer plan --from AGENTS.md --to CLAUDE.md --mode mirror
dev instructions transfer plan --from AGENTS.md --to CLAUDE.md --mode mirror --style import
dev instructions transfer apply --plan <id>

dev skill transfer export <applied-id> > .agents/interop.toml
dev skill transfer recipe .agents/interop.toml --entry skill-example
dev skill transfer apply --plan <id>
```

Import prepends the shared source while retaining Claude-specific bytes and order.
A symlink can replace an identical regular instruction file only with `--adopt`;
different content is a conflict. Moves do not generalize project assumptions and
refuse to leave another native instruction link dangling.
[Claude instructions](https://code.claude.com/docs/en/memory)

Recipes are optional v1 TOML with named `transfers` entries. Select one entry per
fresh plan. They record copy/mirror intent and native source references, never
absolute checkout paths, credentials, receipts or host-local launcher bindings.
User entries resolve the current agent's native home. Unknown fields or profiles
fail closed. Cross-project absolute dependencies, moves and prepared payloads
remain explicit operations; upstream installs already have native locks.

## Recovery and platform boundaries

`transfer status` reports recorded operations; `transfer undo <id>` creates a
reverse plan. Neither a receipt nor inventory's additive `interop` metadata is
evidence of current client activation or health. Apply locks and revalidates exact
file/checkout/Git identities. A stale plan has no new effect; later failures
report confirmed steps and an unconfirmed in-flight step separately.

Recovery objects live under `paths.state_dir/agent-interop`, with 0700 directories
and 0600 files. This is durable private state, not a cache or sync directory.
Git-backed state locations are refused because recovery of an existing mixed
configuration may contain unrelated credentials. There is no automatic expiry:
retain records while a launcher, refresh or undo depends on them. Do not commit
or synchronize this directory. An unconfirmed crash effect requires inspection;
dev does not guess ownership and delete it.

The initial mutation backend is POSIX. Native Windows transfers fail before agent
writes pending a verified protected-DACL/recovery adapter; inventory and existing
native provider commands remain available. Symlink failure never silently becomes
a copy, junction or elevation request. External editors, raw Git and native
provider commands remain outside dev's cooperative transaction guarantees.
