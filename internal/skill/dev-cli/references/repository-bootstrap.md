# Repository bootstrap

`dev repo new`, `repo clone`, and `repo setup` share one scaffold pipeline but
keep acquisition explicit: a name creates a new repository, while a URL/path or
`owner/name` belongs to clone.

```bash
dev repo new                              # interactive
dev repo create api                       # minimal, script-friendly
dev repo clone owner/api                  # optional setup after clone
dev repo setup . --preset agent-ready     # existing clean checkout
```

The interactive default is `agent-ready`; explicit `repo new NAME` remains
`minimal`. Use `--dry-run` before a custom preset and `--json` for automation.
Dry-run does not mutate the target repository. JSON never prompts, changes
directory, or activates a runtime; automation supplies the required name/ref
and an explicit setup preset.

`repo setup` repeat-safely merges native initializers and preset files. Custom
hooks and skill setup remain responsible for their own idempotency.

## Configuration and trust

Global presets live in `$XDG_CONFIG_HOME/dev/scaffolds.toml`. A target repo may
override portable setup behavior with `.dev-cli/config.toml` and
`.dev-cli/scaffolds.toml`. It cannot change host paths, runtime, state, forge
inventory, stats, update or TUI policy.

Project-owned hooks and skill setup entrypoints from `.dev-cli/scaffolds.toml`,
plus worktree post-create commands from `.dev-cli/config.toml`, are keyed by
canonical Git identity plus a SHA-256 of executable configuration. Legacy
`.dev.toml` retains its compatibility behavior. Review the plan and approve the
exact hash:

```bash
dev config show --project
dev config trust . --yes
```

A changed hash must be reviewed again. Pre-commit and gitleaks protect committed
bytes; they do not authorize command execution.

## Skills and publication

Selecting a setup-capable skill installs it in project scope. Custom global or
local presets can run a declared script below the installed skill directory in
the declared phase; project-authored executable setup is restricted to local,
content-hashed sources. For `agent-history-hygiene` and
`project-knowledge-harness`, dev uses reviewed built-in initializers for the
pre-commit/gitleaks and TODO/backlog/pitfalls surfaces instead of executing newly
downloaded skill code.

The wizard only offers GitHub/GitLab publication when the matching CLI is
installed and authenticated. Publication uses the local repository name and
configured description, follows required local commit steps, and optionally
pushes the initial/current branch. A create, remote-add, or push failure leaves
local work intact.

Handoff is explicit: `stay`, `cd`, `open`, or `start`. `start` enters the normal
task wizard. None of these choices launches a coding agent.
