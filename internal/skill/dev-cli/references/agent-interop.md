# Agent configuration transfers

Keep native agent configuration and upstream skill provenance separate from dev's
private operation receipts. Do not infer effective authorization or connection
health from inventory or a successful config write.

## Workflow

- `dev mcp transfer plan --server NAME --from-agent claude-code --to-agent codex --mode copy`
  converts one selected native declaration.
- `dev skill transfer plan NAME --from-agent universal --to-agent claude-code --mode mirror`
  creates one relative per-skill link.
- Cross-project skills prefer `transfer prepare NAME --from-repo A --to-repo B`,
  then `transfer plan NAME --mode install --prepared ID` with the same selectors.
  Only prepare runs the trusted `skills@1.5.23` provider or fetches.
- `dev instructions transfer plan --from AGENTS.md --to CLAUDE.md --mode mirror`
  shares instructions; `--style import` preserves Claude-specific text.
- Apply the exact displayed ID with `transfer apply --plan ID`. Refresh and undo
  create new plans, never immediate changes.

Project/user scopes and checkout selectors are independent for source and
destination. Prefer upstream installs, then independent copies, before explicit
cross-project mirrors. Copy does not propagate edits. Move verifies destination
content before retiring source data and native lock membership. A live
cross-project consumer blocks canonical retirement.

## MCP and credentials

Adapters cover Claude Code, Codex, Cursor, Gemini CLI and classic OpenCode.
Only the common stdio/Streamable HTTP fields are converted. Ambiguous remote
transport needs `--transport streamable-http`. Unsupported policy, helpers,
OAuth, SSE, malformed/unknown schemas and uneditable TOML layouts fail closed.

Preserve unrelated settings/comments. A mirror owns its stanza, not a whole
config file. `--adopt` is required for equivalent foreign ownership or changing
a managed source. Do not turn client approvals into portable authorization.

Use environment references. Cross-scope credential use requires explicit
`--bind SERVER_ENV=PROCESS_ENV` mappings. Never place token values in flags,
generated config, recipes, diagnostics or source files. The optional stdio
`--bridge` / `--secret-env-file` launcher resolves only declared server variables
at launch: process env, selected owner-private JSON env object, then fallback.
Empty is set. Unrelated provider/model credentials are not forwarded. Policy
files can be inspected at plan/apply time without resolving credential values.
Rotation takes effect on the next launch; execution/reference changes need refresh.
A bridge is host-local and cannot move away from its retained source declaration.

HTTP env references must be present in the destination client before startup.
A stdio launcher cannot inject into that HTTP client. Keep OAuth token stores,
private-key files, hooks, plugins, model and sandbox settings outside migration.

`dev mcp transfer check ID` is the explicit server initialization operation.
It negotiates MCP 2025-06-18 and calls no application tools. `initialized` does not
mean authenticated queries succeeded or a native agent loaded the config.

## Skills and recipes

The provider profile, native lock schema and content hash are separate checks.
Do not substitute npx/latest. Stage and verify first; fail on drift or unsupported
metadata. A lock-only checkout can be restored with matching source/destination
selectors. Preserve a matching existing lock. The native add/update wizard remains
an expert provider surface, not a frozen restore.

Use per-skill links, preserve foreign content, and do not rewrite provider hashes
during local repair. Source trees must contain regular files/directories; known
credential files, real private-key material, escapes/cycles and case collisions
are rejected. Symlink failure never becomes copy or a junction automatically.
The bundled dev-cli skill remains owned by dev's own installer.

`transfer export ID` prints an optional v1 TOML copy/mirror recipe; the operator
chooses where to save it, usually `.agents/interop.toml`. `transfer recipe FILE
--entry NAME` plans one entry in the selected checkout. Root paths, credentials,
receipts and local launcher bindings are never exported. Native locks remain the
reproduction contract for upstream installs.

## Recovery and platform limits

Private state is `paths.state_dir/agent-interop`, outside Git, with 0700 directories
and 0600 files. It may contain recovery copies of existing mixed configuration;
never sync, commit, print or treat it as disposable cache. No automatic expiry
is performed while an operation or launcher can depend on a record.

Apply guards exact file/checkout/Git identity under the existing cooperative
provider lock. Retire only reviewed entries. Confirmed partial effects are
recorded separately from unconfirmed in-flight operations. Undo revalidates current
ownership and preserves intervening edits. Never guess ownership after a crash.

The initial mutation backend is POSIX. Native Windows transfer writes remain
unsupported pending protected-DACL/recovery validation; existing inventories and
native provider commands still work. Raw Git, editors and external provider calls
remain outside dev's cooperative guarantees.
