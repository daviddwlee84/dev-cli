# MCP declarations

Inspect static MCP server declarations across repositories and supported agent
configuration formats without starting servers or contacting endpoints.

```bash
dev mcp list
dev mcp list --repo api
dev mcp list --all
dev mcp list --agent claude-code,codex --scope project --json
```

The initial adapters cover Claude Code, Codex, Cursor, Gemini CLI, and OpenCode.
Project, local, user, custom, system, and managed declarations remain separate
rows. Claude user configuration honors an absolute `CLAUDE_CONFIG_DIR`; local
rows retain their exact project key. `configured`, `enabled`, and `disabled`
describe declarations plus Claude's documented user/project/local/managed project
approval settings; they are not connectivity or authentication health, and no
other agent's layers are merged into a guessed effective configuration.

The scanner never executes agent MCP list/debug commands, configured commands,
header helpers, servers, or URLs. It does not expand environment placeholders,
read OAuth/token stores, or dereference OpenCode `{file:…}` values. Normalized
output retains only safe facts such as the command basename, provider-specific
environment reference names, argument count, OAuth presence/count facts, and a
URL scheme/host with userinfo, path, query, and fragment redacted. Explicit
Gemini SSE/streamable HTTP and Claude HTTP/streamable HTTP declarations stay
distinct; null server members are diagnostics rather than phantom rows.

Static inventory cannot reproduce every runtime layer. Plugin caches, hosted
connectors, remote organization configuration, inline
`OPENCODE_CONFIG_CONTENT`, and command-line-only inputs are omitted and reported
as coverage limits. Duplicate names in different files/scopes are not merged
into a guessed effective configuration.

The MCP TUI view defaults to the startup context: inside Git it scans only the
exact startup checkout; outside Git it reuses every accepted REPOS target plus
the ordinary startup directory. Uppercase `A` switches both MCP and SKILLS to
all accepted repositories for the current TUI run. It never starts, connects,
adds, removes, or authenticates a server. Filters include `repo:`, `agent:`,
`scope:`, `transport:`, `managed:`, and `state:`.

Press `e` to open the selected declaration's local `ConfigPath`. The `y` copy
menu offers that path (`p`), a normalized sanitized summary (`s`), or the entire
raw local config file (`f`). Raw copy accepts only a regular file up to 1 MiB and
performs no network access, but it can place credentials and other declarations
from the same file into the system clipboard. It does not alter the sanitized
inventory or JSON contract.
