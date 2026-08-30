# Repository bootstrap

Create, clone, or initialize a repository from any directory.

```bash
dev repo new                         # interactive wizard
dev repo create api                  # minimal scripted creation
dev repo clone owner/api             # clone, optionally set up
dev repo setup . --preset agent-ready
```

`repo new NAME` preserves the minimal contract: create the configured
`project_root[/category]/NAME`, initialize `main`, write README, and make an
initial commit. A bare `repo new` opens the richer wizard. `create` is an alias;
Git URLs belong to `repo clone` so a typo cannot silently change operation.
`repo setup` repeat-safely merges native initializers and preset files; custom
hooks and skill setup must provide their own idempotency. `--dry-run` does not
mutate the target repository.

## Presets

The built-in `minimal` preset matches scripted creation. `agent-ready` adds a
managed `.gitignore`, `AGENTS.md`, and repository-local Claude plans. Optional
skills may declare a setup entrypoint. For `agent-history-hygiene` and
`project-knowledge-harness`, dev installs the skill but uses reviewed built-in
initializers before the first commit instead of executing newly downloaded code.

Global presets live in `$XDG_CONFIG_HOME/dev/scaffolds.toml` and are managed
with `dev config scaffolds init|show|path|edit`. A repository may commit
`.dev-cli/config.toml` and `.dev-cli/scaffolds.toml`; only portable setup and
worktree policy is allowed there. Paths, state, runtime and credentials remain
host-owned global configuration.

Executable hooks and skill setup from `.dev-cli/scaffolds.toml`, plus
`post_create` from `.dev-cli/config.toml`, are trusted by repository identity
and content hash. Legacy `.dev.toml` retains its compatibility behavior:

```bash
dev config show --project
dev config trust . --yes
dev config trust . --revoke
```

Changing executable configuration invalidates the prior approval. Trust data
is private host state and is never committed.

## Publishing and handoff

The wizard offers GitHub or GitLab only when its CLI is installed and
authenticated. Local-only and private visibility are the defaults. Publication
uses the local repository name and configured description, follows required
local commit steps, and optionally pushes the initial/current branch. A remote
or push failure never deletes the checkout.

Final handoffs are explicit:

- `stay` prints the result.
- `cd` enters it through `dev shell-init`.
- `open` opens the configured runtime, falling back to `cd` with no runtime.
- `start` continues into the normal tracked-task wizard.

Neither repository bootstrap nor `dev start` launches a coding agent.
