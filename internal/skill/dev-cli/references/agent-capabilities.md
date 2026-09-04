# Agent skill and MCP inventory

Use these inventories to answer two different questions:

- `dev skill list`: which reusable instruction packages are present, and whether
  an explicit upstream source check reports newer lock-recorded content.
- `dev mcp list`: which external MCP capabilities are declared in supported
  static agent configuration files.

Do not treat an MCP declaration as a skill, a live connection, or permission to
invoke tools.

## Skill inventory

```bash
dev skill list
dev skill list --repo <repo-or-checkout> --project
dev skill list --all
dev skill list --all --check --json
```

Reads use a versioned snapshot of all 77 agent paths from `skills@1.5.23` plus
project/global locks. They never execute `skills`, Node, npm, `npx`, agent
detectors, or project code. `--all` scans canonical repositories. The TUI is
context-first: inside Git it scans only the exact startup checkout plus global
paths; outside Git it scans all accepted REPOS targets plus the ordinary startup
directory. Uppercase `A` switches both SKILLS and MCP to all accepted repositories
for the current TUI run. In SKILLS, `e` opens the row's primary installed `SKILL.md` (or a missing
row's lock file), while `y` copies path (`p`), safe summary (`s`), sanitized source
URL (`u`), or whole raw file (`f`). Editors use a private working copy, revalidate
the observed source immediately before atomic replacement, and retain the working
copy when a conflict is detected.

Keep status dimensions separate:

- presence: local directory present or lock-only/missing;
- integrity: only embedded `dev-cli` files can be verified against the binary; additional user files are ignored;
- update: lock-recorded upstream content is unchecked/current/changed/missing or
  unverifiable.

`--check` is the explicit network action. It groups equal Git source/ref pairs
and hashes Git object bytes without populating a checkout, so filters/autocrlf do
not run or alter results. Non-ASCII provider folder hashes remain unverifiable
when locale ordering cannot be reproduced. `skill add` and `skill update` are
explicit, serialized mutations and require a directly installed `skills`
executable; `dev` skips repository-local npm bins and rejects source-less locks.

## MCP inventory

```bash
dev mcp list
dev mcp list --repo <repo-or-checkout>
dev mcp list --all --json
dev mcp list --agent claude-code,codex --scope project
```

Adapters cover Claude Code, Codex, Cursor, Gemini CLI, and OpenCode. Rows remain
scope-qualified declarations; local Claude rows retain the exact project key and
an absolute `CLAUDE_CONFIG_DIR` relocates user sources. `dev` resolves only
Claude's documented user/project/local/managed project approvals; it does not
guess a generally effective configuration. `configured`, `enabled`, and
`disabled` are declaration/approval facts, not health.

The TUI applies the same context-first target policy and shared `A` toggle as
SKILLS. Press `e` to open the selected local `ConfigPath`; `y` copies the path
(`p`), normalized summary (`s`), or whole raw config file (`f`).

The scanner never runs agent MCP commands, configured commands/helpers, servers,
or URLs. It never expands environment variables, reads OAuth/token stores, or
loads OpenCode `{file:…}` content. Normalized output retains only safe command
basenames, endpoint scheme/host, argument counts, credential reference names,
policy counts, and redaction markers. Explicit raw copy occurs after inventory,
reads only a regular file up to 1 MiB, and performs no network access; it can put
credentials and other declarations from that source file into the system
clipboard without changing normalized rows or JSON.

Omitted sources include plugin caches, hosted connectors, remote organization
configuration, inline `OPENCODE_CONFIG_CONTENT`, and command-line-only inputs.
Treat the coverage metadata as part of the result.
