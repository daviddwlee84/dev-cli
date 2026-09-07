# Agent configuration interoperability: Claude MCP → Codex and shared skills

**Status**: P2 — implementation in progress on `feat/agent-config-interop`
**Effort**: L, split into independently useful phases
**Recorded**: 2026-09-07
**Related**: [TODO](../TODO.md), [MCP inventory](../internal/help/topics/mcp.md), [skill inventory](../internal/help/topics/skills.md), [agent capability reference](../internal/skill/dev-cli/references/agent-capabilities.md)

## Accepted implementation amendment (2026-09-07)

The user approved implementing the complete design in ordered commits. This
amendment supersedes the narrower first-release scope and open decisions below.

- MCP adapters: Claude Code, Codex, Cursor, Gemini CLI, and OpenCode; supported
  common stdio/Streamable HTTP fields only. Unknown transport, policy, OAuth,
  helpers, or schema must not silently lose semantics in conversion.
- Exact project/worktree and user/global scopes support independent copies,
  guarded moves, and explicit one-way mirrors. MCP mirrors refresh by reviewed
  projection; skill mirrors use per-skill relative symlinks. No daemon/proxy.
- Across repositories prefer a verified upstream install, then an explicitly
  selected independent copy. No automatic fallback or latest-version upgrade.
- Keep native agent files and upstream locks. Optional `.agents/interop.toml`
  stores reconstruction intent only; ordinary transfers need no dev manifest.
- Share AGENTS.md through a CLAUDE.md symlink or explicit @AGENTS.md import.
  Never merge different instruction text or generalize project rules implicitly.
- Env references and explicitly selected legacy local-env sources are the
  initial secret backends. Cross-scope credential use requires a new binding.
  Keychain, external secret-manager, and hardware-key adapters are follow-ups.
- Keep plans, ownership, and recovery records under Config.StateDir() with
  private permissions. Plan/apply revalidate scope and file identities, expose
  sanitized reports, and keep accurate partial-effect ledgers.
- Initial skills compatibility profile is 1.5.23. Validate provider version,
  project/global lock schema, and content separately. Upstream install is not a
  frozen install: stage and verify before publication. Raw SHA refs are not
  assumed to work with upstream's `git clone --branch`.

Ordered commits: compatibility/plan; guarded filesystem core; local skills;
provider staging and reproducibility; Claude/Codex MCP; remaining MCP adapters;
env launcher and mirror refresh; cross-scope moves/recovery; instructions and
optional recipes; E2E/docs/release readiness. Tests and affected product docs
travel with each exposed feature.

Verified references: [skills installer](https://github.com/vercel-labs/skills/blob/v1.5.23/src/install.ts),
[project lock](https://github.com/vercel-labs/skills/blob/v1.5.23/src/local-lock.ts),
[global lock](https://github.com/vercel-labs/skills/blob/v1.5.23/src/skill-lock.ts),
[clone/ref handling](https://github.com/vercel-labs/skills/blob/v1.5.23/src/git.ts),
[Claude instructions](https://code.claude.com/docs/en/memory),
[Cursor MCP](https://prod.cursor.com/docs/mcp),
[Gemini MCP](https://geminicli.com/docs/tools/mcp-server/),
[OpenCode MCP](https://opencode.ai/docs/mcp-servers/).

### Implementation ledger

- [x] Record approved scope and compatibility baseline.
- [ ] Guarded transfer engine and recovery.
- [ ] Skills copy/mirror and provider staging.
- [ ] Five MCP adapters and env launcher.
- [ ] Cross-scope moves, instructions, optional recipes.
- [ ] Full validation and documentation synchronization.

## Context and intended outcome

While fixing the `trading-vm` → `gs-vm` telemetry label mismatch, a working
Grafana MCP declaration was available in a neighboring Tadronaut-Mono-Repo
checkout, but Codex did not have a corresponding project declaration. The
existing configuration was split between:

- `.mcp.json`: the `grafana` stdio server (`uvx mcp-grafana`) and a Context7
  HTTP server; Grafana environment values were references, not literal secrets.
- `.claude/settings.local.json`: the local Grafana URL/token, mixed with
  unrelated Claude model/provider environment settings.
- Codex's `.codex/config.toml`: absent before this session.

A small project-local Python launcher was added to that checkout. It resolves
only the selected server's environment placeholders, leaves the secret in the
existing local settings file, and launches the original `.mcp.json` declaration.
The Codex configuration also declares Context7 directly. Grafana initialization
returned 72 tools, and an authenticated `query_prometheus` call returned
`up{source="gs-vm",job="integrations/unix"}=1`. Startup was verified from a
nested Git submodule as well as the parent project. A secret scan passed.

The user requested a dev-cli backlog plan to make this repeatable, together with
using `.agents/skills` as the project skill source of truth and projecting it
into `.claude/skills` using symlinks. This document authorizes planning only;
the example commands below are proposed interfaces and do not exist yet.

Desired result: a user can select an existing MCP declaration or local skills,
review the exact configuration/link changes, and apply them without maintaining
duplicate secrets or duplicate skill trees. Static inventory remains local-only.

## Existing implementation and integration points

Inspected in the dev-cli checkout on 2026-09-07:

| Existing surface | What can be reused | Boundary to preserve |
| --- | --- | --- |
| `internal/cli/mcp.go`, `internal/agentmcp/{scanner,claude,codex,model,sanitize}.go` | Scope-qualified discovery, transport/policy diagnostics, safe identifiers and redaction vocabulary | Inventory deliberately drops raw arguments, endpoint paths and credential values. A sanitized `Declaration` cannot reconstruct a runnable server. Do not make `Scan` load secrets or execute commands. |
| `internal/agentskill/{agentskill,registry,scan,provider}.go` | Project/provider path registry, physical vs logical installation identity, lock provenance, mutation serialization | Existing add/update delegates to the installed `skills` provider. Local linking must not masquerade as an upstream update or download packages. |
| `internal/skill/skill.go`, `dev skill install` | Precedent for installing dev's bundled skill and linking agent directories | This owns only the embedded `dev-cli` skill; it is not an ownership authority for arbitrary user/provider skills. |
| `internal/cli/skillsync.go` | Existing generated-reference maintenance | `dev skill sync` already regenerates dev's command reference. Do not reuse that name for cross-agent skill synchronization. |
| `internal/cli/repo_skills.go`, `repo_builtin_skill_setup.go` | Explicit repository setup phases | Integrate only as an opt-in setup action after the standalone behavior is stable. No new action on ordinary repo discovery. |
| `internal/agenttarget`, `gitx`, `pathx`, `lockx` | Exact checkout selection, canonical Git identity, bounded path handling and cooperative locks | A monorepo, nested submodule and linked worktree are different checkout contexts. Never silently borrow secrets from another checkout. |

Suggested domain boundary: a new `internal/agentinterop` package owns bridge/link
plans, source/destination validation, managed ownership and apply results.
`internal/cli` remains an adapter. Reuse compatible transaction utilities, but
do not turn these local file operations into persisted task lifecycle states.

## Scope and non-goals

First release:

1. One-way project MCP bridging from a selected Claude Code `.mcp.json` server
   to a Codex project configuration.
2. Runtime references to an explicitly selected local environment source;
   the Grafana case is the acceptance fixture.
3. Local per-skill links from `.agents/skills/<name>` to
   `.claude/skills/<name>`, preserving other Claude-only entries.
4. Preview, conflict reporting, idempotent apply and ownership-aware recovery.

Not part of the first release:

- Bidirectional configuration synchronization or a new universal MCP manifest.
- A replacement secret manager, copying OAuth token stores, or propagating
  Claude model/API settings into Codex.
- Changing trust, approval, sandbox or tool-execution policies to make a
  converted server work; Claude approval lists are not Codex authorization.
- Automatic server startup, connectivity probes, package installation or
  network access during inventory, plan, or apply.
- Bulk user/system/plugin configuration migration, cross-host secret sync, or
  rewriting chezmoi/provider-owned installations.
- Converting agent instruction files, hooks, plugins, or agent definitions.

## Options considered

| Option | Benefit | Cost / decision |
| --- | --- | --- |
| Copy resolved JSON values into Codex TOML | Small initial implementation | Reject for secret-backed servers: duplicates credentials and becomes stale on rotation. Literal values can also hide in arguments, headers and URLs. |
| Export all Claude local environment variables before starting Codex | Avoids a second token file | Reject: exposes unrelated provider/model settings to the whole Codex process and relies on GUI/shell inheritance. |
| One-way managed bridge with a runtime environment reference | Preserves the original declaration and secret location | Recommended. Adds a small launcher/runtime contract and requires explicit ownership, source scope and path resolution. |
| New agent-neutral manifest for all MCP clients | A possible future source of truth | Defer until there is a second real conversion direction; do not introduce a competing manifest for this first use case. |
| Replace the whole `.claude/skills` directory with a symlink | Very simple for an empty project | Not the default: hides or overwrites provider-specific skills and makes selective membership difficult. |
| Per-skill relative symlinks | Keeps one physical skill tree and preserves unrelated entries | Recommended, with explicit conflict handling and supported-platform checks. |

## Proposed CLI and operation model

Keep the existing `mcp` and `skill` command families. Names/flags below need a
final CLI review before implementation:

```text
dev mcp bridge plan --from claude-code --to codex --scope project \
  --repo . --server grafana --secret-env-file .claude/settings.local.json
dev mcp bridge apply --plan <plan-id>

dev skill link plan --repo . --source .agents/skills --agent claude-code
dev skill link apply --plan <plan-id>
```

- Plan prints a sanitized change list and distinguishes `create`, `noop`,
  `conflict`, `unsupported`, and `needs-local-configuration`. Apply acts only on
  the exact reviewed items. Selecting all servers must be explicit.
- Bind plans to checkout/root identity, exact source declaration, target bytes,
  link identities and managed revision. Lock and re-read before mutation; a
  stale plan reports no new effects. Secret values never enter plans or their
  fingerprints. File/source changes unrelated to secret rotation still need
  appropriate revalidation.
- Store only versioned non-secret ownership metadata: source reference,
  destination stanza/link, operation ID and managed output identity. Receipts
  are not runtime connectivity evidence or a second inventory database.
- Expose useful structured results additively; keep `dev mcp list --json` and
  existing skill inventory semantics compatible.
- Separate configuration validation from an explicitly requested MCP connection
  check. `configured`, `initialized`, and `authenticated query succeeded` must
  remain different results.

## MCP bridge design

### Preserve scope and configuration structure

1. Resolve the selected repository and exact source file/server/scope. Duplicate
   names in different layers remain conflicts, not an implicit precedence rule.
2. Read the selected declaration through a dedicated bounded source reader.
   Keep executable payloads separate from sanitized inventory/report models.
3. Add or update only an owned `[mcp_servers.<name>]` stanza. Preserve unrelated
   model, approval, sandbox and other MCP settings, comments and formatting.
   A whole-file decode/re-encode of arbitrary TOML is not sufficient.
4. An existing same-name server is a no-op only when equivalence is proven.
   Otherwise report the difference and require explicit adoption/replacement;
   never silently seize ownership of a user-authored stanza.
5. Retain Codex's normal trusted-project requirement. A written TOML stanza is
   not proof that the current client loaded it; report the required session
   reload/restart separately.

### Transport and field support matrix

| Source configuration | Initial behavior |
| --- | --- |
| Stdio `command` / `args` / `env` with supported references | Bridge to the selected declaration through dev's launcher; preserve argument boundaries. |
| Plain HTTP endpoint with no dynamic authentication | Map to Codex streamable HTTP only after compatibility is verified. Context7 is a candidate fixture. |
| HTTP bearer/header environment references | Use Codex's documented environment-reference fields when the target client can actually receive those variables. A stdio launcher cannot inject environment into an already-running Codex HTTP client. Otherwise report unsupported/needs-local-configuration; do not embed a resolved token. |
| URL templates, literal credentials, SSE, helpers, OAuth extensions, plugin or managed declarations | Diagnose unsupported fields explicitly in the first slice. Do not drop them, execute helpers during conversion, or pretend all HTTP-like transports are interchangeable. |

### Runtime environment and launch behavior

- Prefer an installed Go `dev` launcher over copying the session's ad hoc
  Python/shell wrapper into every project. Do not introduce a Python dependency
  or install `uvx`/Node merely to create a bridge. Missing executables are a
  clear runtime prerequisite failure.
- The bridge stores references, not resolved credentials. Only on the explicit
  server launch path does it read the chosen local settings `env` object and
  resolve the variable names required by that server.
- Specify and test placeholder semantics, including unset vs empty values,
  `${NAME}` and `${NAME:-default}`. The prototype used process environment,
  then local settings, then fallback; verify the desired Claude compatibility
  against current official documentation instead of treating that prototype
  order as a general provider contract.
- Do not `source` JSON, use `eval`, interpolate secrets into shell commands, or
  copy unrelated `ANTHROPIC_*` / model configuration into Codex settings.
- Validate local secret-file scope, regular-file identity and permissions.
  Reject or explicitly handle symlink escapes and cross-checkout references.
  No broad search through neighboring repositories or global token stores.
  Missing credentials report only missing names and the selected local source.
- Resolved source credentials must not be copied into generated config, logs,
  diagnostics, diffs, receipts, temporary files, process arguments or automated
  clipboard output. Existing destination-file backups have the separate private
  recovery requirements described below. Existing
  raw-config copy remains a separate user action, not part of this flow.
  Credential changes should be picked up on the next server launch.
- Resolve monorepo/submodule/worktree launch roots deterministically. The session
  initially considered `git rev-parse --show-toplevel`, which chooses a nested
  submodule rather than its intended parent bridge. Do not hardcode the author's
  absolute checkout path as the portable solution either.
- Pin the selected server and source scope in the launcher reference. Detect
  changes to executable/argument/policy structure that require a new bridge
  review; ordinary secret rotation must not require regenerating config.
- Preserve stdout for MCP protocol messages, send bounded diagnostics to stderr,
  propagate signals/exit status, and avoid orphaned child processes. Validate
  executable resolution against dev's trusted external-tool conventions.

## Shared skill layout and conflict policy

Canonical project layout:

```text
.agents/skills/example/SKILL.md       # authored/installed source
.claude/skills/example -> ../../.agents/skills/example
```

Codex already discovers `.agents/skills`; no `.codex/skills` projection is needed
for this project-level case. Use registry-derived locations rather than hardcode
an expanding list of clients.

| Destination/source observation | Proposed result |
| --- | --- |
| Source is valid; destination missing | Plan a relative per-skill symlink. |
| Destination already resolves to the same source | No-op; retain provenance. |
| Destination is a real directory with identical bytes | Offer explicit adoption with a preserved backup; do not delete merely because hashes match. |
| Destination differs, is a foreign symlink, or is a special file | Conflict; preserve both sides. |
| Skill exists only under `.claude/skills` | A separately reviewed migration into `.agents/skills`, followed by the link. Never infer which copy wins. |
| Source is missing, cyclic, unreadable or outside the selected scope | Diagnostic; no link mutation. |

Additional requirements:

- Validate names, `SKILL.md` presence, case collisions, dangling/cyclic links,
  resolved parents and path containment. Use `Lstat`-aware checks and revalidate
  under the mutation lock before replacing any directory entry.
- Preserve unselected Claude-only skills and provider/user modifications.
  Unlink only links still matching a dev ownership record; never remove the
  canonical skill contents as an effect of unlink.
- Respect `skills-lock.json` and upstream ownership. Native link repair does not
  rewrite upstream content hashes or invoke `skills add/update`. Continue
  deduplicating inventory by physical identity while reporting logical paths.
- A copied compatibility tree on Windows is not a symlink or a source of truth.
  Report unsupported symlink capability clearly; do not silently choose copies,
  elevation or junctions. Design an explicit fallback only with tested semantics.
- Worktree links should resolve within their selected worktree. Deliberately
  shared external stores require a separate explicit scope/ownership decision.
- Both clients seeing a skill does not prove identical discovery timing or
  precedence. Verify discovery independently from filesystem correctness.

## Phases and acceptance criteria

### Phase 0 — compatibility spike (S)

- Confirm current Claude/Codex MCP schemas, reference expansion and skill
  discovery rules using official docs and the supported installed versions.
- Produce an explicit supported/unsupported conversion matrix and settle CLI
  vocabulary, TOML preservation strategy and root selection.
- Build fixtures from the Grafana/Context7 case using synthetic credentials only.

### Phase 1 — MCP bridge (M)

- Implement one selected project stdio server, deterministic launcher references,
  deferred secret resolution, preview and guarded apply.
- Preserve arbitrary unrelated Codex configuration; repeat apply is a no-op.
- Add compatible direct HTTP mapping only if the Phase 0 evidence supports it.
- Provide configuration status and an explicitly invoked connection check.
- Acceptance: a fake stdio MCP handshake works from a repo root, ordinary
  subdirectory, monorepo submodule and linked worktree; the selected token reaches
  only the fake server environment, never generated artifacts or output.

### Phase 2 — local skill links (M)

- Implement preview/apply for missing and already-correct per-skill links.
- Report real-directory and foreign-link conflicts without mutation; add explicit
  adoption/migration only after its recovery behavior is tested.
- Acceptance: editing the canonical skill is visible through the Claude link;
  both paths resolve to the same content; inventory retains one physical install
  with both logical paths; reruns preserve unrelated skills and provider locks.

### Phase 3 — lifecycle integration (M)

- Add ownership-aware removal/recovery and optional repo-setup integration.
- Only then consider TUI actions, explicit multi-server plans and more providers.
- Document supported OS/client versions and migration/downgrade behavior for
  generated launcher references that depend on a particular dev version.

## Validation and operational recovery

Tests should verify boundaries, not just mirror the generated template:

- Static list/plan/apply must not execute a sentinel server/helper/provider or
  contact a sentinel endpoint; an explicit connection check may do so.
- Snapshot tests assert no synthetic secret appears in TOML, JSON, stdout,
  stderr, errors, temporary files, receipts or rollback metadata. Include
  arguments, headers, URLs and malformed configuration error paths.
- TOML fixtures include comments, quoted/dotted server names, unrelated tables,
  same-name foreign stanzas and unsupported fields. Unrelated bytes survive.
- Test missing/empty environment values, rotation between launches, disabled
  source declarations and policy incompatibilities without broadening access.
- Test parallel applies, changed source/target/link identities, interrupted
  writes and partial filesystem failure. Report partial effects accurately;
  recovery must not overwrite intervening user changes.
- Filesystem fixtures cover relative paths, spaces/non-ASCII names, symlink
  escape/cycle, case differences, foreign directories and unavailable Windows
  symlink privileges.
- Use synthetic local MCP servers for normal tests. A real Grafana check is an
  explicit smoke test, never a CI dependency or an automatic `dev doctor` action.

Backups/rollback need special care: a user's existing Codex file may already
contain unrelated secrets. Do not copy the whole file into a tracked plan or a
world-readable cache. Prefer a guarded reversible patch; if recovery needs a raw
backup, keep it in an explicitly private local location, report its lifecycle,
and never include it in structured output. Atomic per-file replacement plus a
partial-result ledger must not be described as a cross-file atomic transaction.

When implementation ships, follow AGENTS.md: update CHANGELOG, authored help,
README, embedded skill references and paired English/zh-TW docs; run
`make skill-sync`, `make skill-check`, focused domain/CLI tests and the required
documentation checks. This backlog-only change needs no feature changelog or
generated command-reference changes.

## Open decisions and recommended next step

1. Use the existing source `.mcp.json` plus runtime local-env references for the
   first slice. Revisit a neutral manifest only after a second provider direction
   demonstrates that it removes more duplication than it creates.
2. Start with project scope and selected servers/skills. Global/plugin/managed
   scope should not slip into the MVP through an `--all` flag.
3. Decide whether launcher ownership metadata belongs under a portable repo-local
   dev file or XDG state with regeneration. Credentials belong in neither.
4. Verify Claude's symlink discovery and the supported client versions before
   promising both-client activation; its documentation fetch returned HTTP 403
   during this investigation. Codex's symlink support was confirmed in its docs.
5. Establish a minimum dev version for generated launcher references and a
   clear missing/older-binary diagnostic; do not silently fall back to another
   executable from the repository.

Recommended first implementation: Phase 0 followed by the Grafana stdio bridge,
then per-skill links. Keep this as P2/L until scheduled; no dev-cli feature was
implemented in the planning session.

## References and reproduction material

- [Codex MCP configuration](https://developers.openai.com/codex/mcp/): project
  `.codex/config.toml`, stdio and HTTP fields; fetched during the preceding setup.
- [Codex skills](https://developers.openai.com/codex/skills/): `.agents/skills`
  discovery and symlinked skill folders; fetched 2026-09-07.
- [Claude Code MCP](https://code.claude.com/docs/en/mcp) and
  [Claude Code skills](https://code.claude.com/docs/en/skills): verify before
  implementation; fetch was denied (HTTP 403), so this plan does not claim those
  current semantics were independently verified.
- Local prototype in the sibling `Tadronaut-Mono-Repo` checkout:
  `.codex/config.toml`, `scripts/mcp-with-local-env.py`, and its README's
  **Codex MCP** section. Inspect behavior, not secret values; this is a prototype,
  not a runtime dependency of dev-cli.
- The existing `internal/agentmcp` and `internal/agentskill` tests are the
  regression baseline for preserving read-only inventory and provider ownership.
