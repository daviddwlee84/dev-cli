# Agent configuration interoperability

Plan selected MCP, skill, or instruction transfers while keeping native agent
files, upstream skill locks, credential references, and private recovery separate.

```bash
dev skill transfer plan example --from-agent universal --to-agent claude-code --mode mirror
dev mcp transfer plan --server grafana --from-agent claude-code --to-agent codex --mode copy
dev instructions transfer plan --from AGENTS.md --to CLAUDE.md --mode mirror --style import
dev skill transfer apply --plan <id>
```

Each family has plan/apply/status/refresh/undo. Cross-repo skills use explicit
`skill transfer prepare`, then an install plan with `--prepared <id>`; the pinned
provider runs only in private staging. A supported lock-only source can also be
restored, retaining its matching native lock. Copy stays independent, mirror
shares a source, and move verifies destination content before source retirement.

MCP supports the common subset of five native formats. Unknown policies, OAuth,
helpers, SSE and uneditable layouts are rejected. Stanza refresh/undo preserve
unrelated later settings. Existing equivalent stanzas require `--adopt` before
foreign ownership becomes a mirror. Source/target trust remains client-owned.

Use env reference names, never credential values. Cross-scope use needs explicit
`--bind SERVER_ENV=PROCESS_ENV`. `--bridge` or `--secret-env-file` is an optional
host-local stdio launcher; the JSON env file is relative to the destination scope,
must be private, and is not copied. Only declared server variables are resolved
at launch. HTTP variables must already be in the destination client's environment.

`mcp transfer check <id>` explicitly initializes a server and calls no application
tools. It does not prove authentication or native-client loading. Ordinary
inventory/plan/apply never start a server or contact its endpoint.

`transfer export <id>` prints an optional copy/mirror TOML recipe, commonly saved
as `.agents/interop.toml`. `transfer recipe <file> --entry <name>` plans one entry.
Recipes omit machine paths, credentials, receipts and local launcher bindings.

Private recovery lives under `paths.state_dir/agent-interop`, outside Git. Keep it
while bindings/undo depend on it; it is not a cache or sync directory. Stale plans
make no new changes. Unconfirmed crash effects require inspection rather than
automatic deletion. Native Windows transfers are unavailable until protected
ACL/recovery behavior is verified; inventory and existing provider commands remain.
