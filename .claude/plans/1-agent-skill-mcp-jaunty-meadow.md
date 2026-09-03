# Context

`dev` currently has a useful SKILLS view, but it only inventories the current checkout plus global scope by delegating reads to the external `skills` CLI. That creates three connected problems: repositories outside the current checkout are invisible, lock-recorded skills cannot be shown when the provider is absent, and the `npx --no-install skills` fallback still performs npm registry resolution before npm cancels on the missing `skills@1.5.23` package. MCP configuration is not modeled at all, even though agents store declarations in different files and scopes. Separately, the TUI Tasks table omits repository identity even though `Task.Repo`, filtering, details, and `dev ls --json` already carry it.

The intended result is a provider-independent, no-network skill inventory across repositories with explicit upstream freshness checks; a separate secret-safe MCP declaration inventory; and an unambiguous responsive Tasks table. Read paths must never execute `npx`, start MCP servers, resolve credentials, or claim live MCP health.

## Agreed product behavior

- Whole-machine TUI scans use each canonical repository checkout once **plus the exact startup checkout when it is a distinct linked worktree**. CLI default behavior remains the exact current checkout; `--all` scans canonical repositories, and `--repo` can name an explicit checkout path.
- Native skill discovery covers the complete path registry represented by upstream `skills` v1.5.23 (77 agent IDs), with a versioned, table-driven snapshot. Shared paths such as `.agents/skills` are reported as registry-compatible rather than pretending `dev` detected every installed agent executable.
- Skill reads are fully native. Remote source checks remain opt-in through `--check` or TUI `c`; no persistent freshness cache is added. **Implementation security note:** the approved plan initially retained `npx` for explicit mutations, but independent review proved that running it in a selected repository could substitute a repository-local `skills` bin. Final add/install/update operations therefore require a directly installed `skills` executable.
- MCP is an independent domain and user surface: `dev mcp list` plus a lazy MCP TUI view. Initial adapters cover Claude Code, Codex, Cursor, Gemini CLI, and OpenCode.
- MCP rows represent static declarations, not effective merged runtime configuration or connectivity. Duplicate declarations remain scope-qualified rows; unsupported/runtime-only sources are diagnosed as partial coverage.

# Implementation plan

## 1. Resolve reusable repository scan targets

- Add a small neutral target type under `internal/agenttarget/` containing repository name/display, canonical path/common-dir identity, and the exact checkout root being scanned.
- Build targets by reusing `config.Config.DiscoveryRoots`, `repo.Discover`, `repo.Resolve`, and `gitx.Discover` rather than adding another filesystem walk.
- Encode selection policy in this package, not Cobra handlers:
  - current mode resolves the exact current checkout, including a linked worktree;
  - `--all` returns non-bare canonical discovered checkouts once;
  - `--repo` accepts existing repository references and explicit linked-worktree paths;
  - TUI aggregation appends the startup checkout when it is not the accepted canonical REPOS row;
  - targets are deduplicated by common-dir plus checkout root and sorted deterministically.
- Reuse accepted `tui.RepoRow` snapshots for lazy TUI loads so SKILLS/MCP do not rediscover repositories or delay initial startup.

## 2. Replace provider-based skill reads with native inventory

Refactor `internal/agentskill/agentskill.go` into focused types/registry/scanner/lock/freshness/provider files while preserving the existing mutation command API.

### Registry and filesystem scan

- Check in a provenance/version-tagged Go registry corresponding to upstream `skills@1.5.23` agent paths, including project/global paths and supported environment overrides (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, XDG config roots, and the other registry-defined homes).
- Collapse identical logical directories before reading them; scan only immediate child directories with a valid, size-bounded `SKILL.md` rather than recursively walking repositories or home directories.
- Parse the skill frontmatter name with a real YAML parser, merge duplicate logical installations by `(scope, target checkout, normalized skill name)`, and retain all installation paths/agent IDs. Resolve physical symlinks for deduplication without losing the logical path that explains agent attribution.
- Treat shared paths as compatible with the registry entries that consume that path; expose an attribution/registry-version field so this is not confused with executable detection.
- Make exact lock names win. Only use normalized-name matching when unique; emit a deterministic diagnostic for collisions instead of selecting a random Go map entry.

### Locks and local status

- Split project and global lock decoding and normalize them internally:
  - project `<checkout>/skills-lock.json` (current v1 fields such as source, ref, skill path, and `computedHash`);
  - global v3 metadata and compatible older fields from `$XDG_STATE_HOME/skills/.skill-lock.json` when `XDG_STATE_HOME` is absolute, otherwise `~/.agents/.skill-lock.json`.
- Bound lock reads and distinguish missing, malformed, unreadable, unsupported-version, and structurally invalid files. Missing is normal; the other cases become safe per-source diagnostics while other repositories continue.
- Include lock-only entries whose installation is absent, and unlocked installed directories as externally managed rows.
- Add repository/checkout identity, logical installations, local presence, attribution, and normalized lock metadata to `agentskill.Skill`/result types without removing existing fields.
- Keep upstream freshness (`unchecked/current/update_available/upstream_missing/unverifiable/check_failed`) separate from local presence/integrity. Only the embedded `dev-cli` skill receives verified/drifted byte-integrity status; do not claim generic lock hashes verify installed bytes.
- Convert `Managed` and `FindProject` to the native lock/scanner primitives so repository scaffolding and selected-skill updates no longer require provider JSON.

### Explicit freshness and mutation boundary

- Preserve and isolate the existing `safeSkillFolder`, folder hashing, Git-source derivation, and grouped `(source URL, ref)` comparisons.
- Aggregate all project/global rows first, then run one bounded, cancellation-aware freshness pass so a shared source/ref is cloned once across all repositories. Preserve deterministic row order, clean every temporary clone, and mark only affected rows failed.
- Keep local/node_modules/well-known sources unverifiable unless their lock metadata provides a supported comparison; do not silently reinterpret them as Git.
- Delete the read-time `npx --no-install skills` path. Read/list/provider-version checks must not invoke npm, a registry, or project Node code.
- Retain `interactiveProvider` for explicit add/install/update operations, but require a directly installed `skills` executable after the security review found repository-local `npx` provider substitution. Update `dev doctor` to describe native inventory as always available and report the optional mutation provider separately.

## 3. Aggregate skills across repositories and expose them in CLI

- Add an inventory collector (representatively `internal/inventory/agent_skills.go`) that scans project targets with the existing context-aware `inventory.Limiter`, scans global paths exactly once, merges diagnostics, and optionally performs one grouped freshness check.
- Extend `dev skill list` in `internal/cli/skill.go` with mutually exclusive `--all` and `--repo <ref>` target selection while preserving `--project`, `--global`, `--check`, and the current-checkout default.
- Keep the existing JSON array and every existing key/type; add repository/checkout, installations, presence/integrity, attribution/registry-version, and normalized lock fields. Write partial diagnostics to stderr so JSON stdout stays valid.
- Add `--repo` to project `dev skill update`; reject repo targeting for global updates. In the TUI, update a selected project skill from that row’s checkout root, while the interactive add wizard continues to use the startup checkout.
- Render a repository-aware human table and keep global rows repository-less rather than duplicating them per target.

## 4. Add a normalized, secret-safe MCP domain

Create `internal/agentmcp/` with normalized types, a scanner, sanitization helpers, and one adapter per supported agent.

### Static sources covered

- **Claude Code:** project `.mcp.json`; user and per-project local declarations in `~/.claude.json`.
- **Codex:** `$CODEX_HOME/config.toml` or `~/.codex/config.toml`; trusted-project `.codex/config.toml`; direct and plugin override declarations.
- **Cursor:** project `.cursor/mcp.json`; user `~/.cursor/mcp.json`.
- **Gemini CLI:** project `.gemini/settings.json`; user `~/.gemini/settings.json`; readable documented system default/override files, with source scope retained.
- **OpenCode:** project `opencode.json`/`opencode.jsonc`; XDG user config; an explicit local `OPENCODE_CONFIG` file and readable managed files. Do not fetch remote `.well-known` config or parse inline `OPENCODE_CONFIG_CONTENT` as durable inventory.
- Use the existing BurntSushi TOML dependency for Codex, `encoding/json` for strict JSON, and a maintained JSONC normalizer for OpenCode rather than an ad-hoc comment stripper.

### Normalization and safety

- Normalize only non-secret facts: server name, agent, declaration scope, repository, config path, managed/direct source, declared enabled/required/trust policy, transport, safe endpoint host or command basename, argument count, and names/kinds of credential references.
- Keep each declaration scope-qualified; do not emulate undocumented merge/precedence rules or label a row “effective.” When transport cannot be proven (for example a Cursor URL declaration), report `remote/unknown` rather than guessing.
- Sanitize during adapter conversion so raw values never enter normalized rows, diagnostics, traces, CLI JSON, or TUI state:
  - never expand environment variables or `{env:…}`/`${…}` placeholders;
  - never read OpenCode `{file:…}` targets or OAuth/token stores;
  - never retain env/header/OAuth values or raw command arguments;
  - reduce commands to a safe basename and URLs to a credential-free representation with userinfo/query/fragment removed or redacted;
  - use fixed diagnostic codes/messages that cannot echo decoder snippets containing secrets;
  - reject control characters and bound config size;
  - confine project config symlinks to the selected checkout, while allowing bounded user-config symlinks for dotfile managers.
- Never invoke agent MCP list/debug commands, helper commands, servers, URLs, or health checks. Document omitted plugin caches, hosted connectors, runtime-only config, remote organization config, and unsupported agent formats as coverage limitations.

## 5. Add MCP CLI and TUI surfaces

- Register a new `dev mcp list [--repo <ref> | --all] [--agent ...] [--scope ...] [--json]` command in `internal/cli/root.go`/a new `internal/cli/mcp.go`.
- Human output shows repo, scope, agent, server, transport, declared state, source, sanitized target, and config path.
- Start the new JSON contract as an envelope containing `servers`, `diagnostics`, and coverage metadata; serialize normalized fields only. Missing files are a successful empty source; malformed individual files preserve partial results; target-discovery/cancellation failures remain non-zero.
- Add `ViewMCP` next to SKILLS and parallel model/action/message/filter/detail state in `internal/tui/`. It is lazy and depends on the accepted REPOS generation just like the broadened SKILLS view.
- Generalize the existing Fleet REPOS-dependency pattern so waiting, invalidation, superseding generations, and cancellation work for SKILLS and MCP. Preserve existing rows during refresh/check (stale-while-revalidate) instead of replacing them with a progress-only screen.
- Add repository-aware structured filters:
  - SKILLS: `repo:`, `scope:`, `agent:`, `update:`, `presence:`, `integrity:`;
  - MCP: `repo:`, `agent:`, `scope:`, `transport:`, `managed:`, `state:`.
- Give MCP only navigation/filter/reload actions; it has no start/connect/add/remove operation. Update the tab strip to remain within narrow terminals after adding the seventh view.

## 6. Show repository in the Tasks table

- Change only the TASKS renderer in `internal/tui/view.go`; use the already persisted `Task.Repo` rather than joining asynchronous repository rows.
- At widths that can retain the existing flexible minimum (target boundary: 97 cells), insert a 16-cell `REPO` column after `STATE` and redistribute TASK/BRANCH/NEXT widths.
- At narrower widths preserve the current `TASK STATE BRANCH GIT AGE NEXT` layout exactly; selected detail still shows the full repo/path.
- Build headers and rows with the existing `fitCell`/`lipgloss.Width` pattern so CJK/wide names do not shift columns. Do not change task identity, sort, filter, detail, or `dev ls --json`—those already contain repository data.

## 7. Tests and compatibility guards

- Add table-driven `agentskill` tests for all registry entries/path overrides, unique-directory deduplication, frontmatter names, shared-path attribution, symlinks, project/global lock versions and XDG selection, malformed locks, collisions, missing installations, and bundled-skill integrity.
- Replace the current npx fallback test with regressions where `PATH` contains marker `npx`/`skills` executables: native listing must invoke neither, and explicit mutations must reject npx-only environments so repository-local bins cannot substitute for the provider. Keep separate direct-provider argv tests.
- Test multi-repo aggregation with duplicate skill names, canonical plus current linked-worktree targets, one global scan, one source/ref fetch, distinct refs, deterministic ordering, cancellation, cleanup, and partial diagnostics.
- Add fixture-driven tests for all five MCP adapters and every documented transport/state variant. Seed sentinel secrets in env, headers, OAuth, URL query/userinfo, args, JSONC substitutions, and malformed input; assert they are absent from domain rows, errors, CLI table/JSON, TUI detail, and traces. Verify no command/helper/network process executes.
- Extend CLI tests for default/`--repo`/`--all`, conflicting flags, additive skill JSON, the MCP envelope/filters, stderr diagnostics, and updating a non-current repository from its own root.
- Extend TUI tests for REPOS-dependent lazy loads, canonical-plus-current target reuse, global deduplication, stale-row retention, generation cancellation, selected-root updates, MCP filters/detail, seven-view navigation, and width-bounded skill/MCP/tab layouts.
- Add TASKS cases at the exact wide/compact boundary, the existing 79-cell floor, CJK names, and two same-named tasks filtered/selected by repository.

## 8. Product documentation and generated surfaces

- Add `[Unreleased]` entries in `CHANGELOG.md` for native cross-repo skill freshness, declaration-only MCP inventory, the npx fix, and the Tasks repo column.
- Update `README.md`, embedded help (`internal/help/topics/skills.md`, `tui.md`, and a new MCP topic), and the bundled `internal/skill/dev-cli/` semantics/reference pages.
- Update paired English/zh-TW TUI, commands/config, compatibility, freshness, and agent-capability documentation. Explicitly document canonical-plus-current semantics, the registry snapshot/version, freshness versus local integrity, opt-in network checks, five supported MCP adapters, declaration-only state, and redaction/coverage limits.
- Run `make skill-sync`, inspect the generated command block, and run `make skill-check`; rebuild after embedded skill edits. Regenerate `docs/llms.txt`/`docs/llms-full.txt` through the documented docs checker.

# Verification

1. Focused packages and integration paths:
   - `go test ./internal/agenttarget ./internal/agentskill ./internal/agentmcp ./internal/inventory ./internal/cli ./internal/tui`
   - targeted no-provider/no-npx, multi-repo grouping, MCP secret-redaction, and TUI width tests.
2. Repository gates:
   - `files="$(gofmt -l .)" && test -z "$files"`
   - `go vet ./...`
   - `go test -race ./...`
   - `make build && make e2e`
3. Generated skill/docs gates:
   - `make skill-sync && make skill-check`
   - `uv sync --frozen --extra docs`
   - `uv run python scripts/check-docs.py --source --generate-llms`
   - `uv run python scripts/check-docs.py --source`
   - `uv run mkdocs build --strict`
   - `uv run python scripts/check-docs.py --site site`
4. Real behavior smoke checks:
   - run `dev skill list --all --check` with no `skills` package installed and prove listing never creates an npm request/log/cache entry;
   - compare native rows against an explicitly installed `skills@1.5.23` in a disposable fixture, allowing documented attribution differences for shared paths;
   - run `dev mcp list --all --json` against secret-bearing fixtures and search all output for sentinels;
   - launch the real TUI at wide and narrow sizes to confirm the TASKS repo column, all-repo SKILLS, and the lazy MCP view.
5. Run an independent high-effort code review/security review after implementation, especially over filesystem confinement, JSON/TOML/JSONC parsing, redaction, cancellation, and structured-output compatibility; address findings before handoff.

# Reference sources

- [skills v1.5.23 agent registry](https://github.com/vercel-labs/skills/blob/v1.5.23/src/agents.ts)
- [skills v1.5.23 installed-skill discovery](https://github.com/vercel-labs/skills/blob/v1.5.23/src/installer.ts)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
- [Cursor MCP configuration](https://cursor.com/docs/mcp)
- [Gemini CLI configuration](https://google-gemini.github.io/gemini-cli/docs/get-started/configuration.html)
- [Gemini CLI MCP servers](https://google-gemini.github.io/gemini-cli/docs/tools/mcp-server.html)
- [OpenCode configuration](https://opencode.ai/docs/config/)
- [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/)
