# Repository bootstrap

Create, clone, or initialize a repository from any directory.

```bash
dev repo new                         # interactive wizard
dev repo create api                  # minimal scripted creation
dev repo new api --template owner/starter --check-in=stage
dev repo clone owner/api             # clone, optionally set up
dev repo setup . --preset agent-ready --check-in=stage
```

`repo new NAME` preserves the minimal contract: create the configured
`project_root[/category]/NAME`, initialize `main`, write README, and make an
initial commit. A bare `repo new` opens the richer wizard. `create` is an alias.
When the argument is clearly a Git URL, local Git path, or `owner/name`, both
`new` and `create` route to clone automatically and preserve source history and
its remote. Template and upstream-creation flags are rejected on this route;
use explicit `repo clone` when the operation should be unmistakable.
`repo setup` repeat-safely merges native initializers and preset files; custom
hooks and skill setup must provide their own idempotency. `--dry-run` does not
mutate the target repository.

After choosing a preset, the wizard asks once whether to customize its template
and detailed options. Declining accepts the preset defaults and skips the
README/gitignore/license/Claude/AGENTS/skill questionnaire. The gate defaults
to yes when customization flags were supplied or a preset has a required input
without a default. Existing-repository setup uses the same condensed gate.

When both stdin and stdout are terminals, wizard text fields use an inline
editor: left/right, Home/End, Backspace and Delete edit the current value
instead of inserting terminal escape bytes such as `^[[C`. Esc or Ctrl-C
cancels the prompt. Piped input retains the line-oriented interface used by
scripts and tests; a bracketed default remains a hint accepted with Enter.

## Templates and check-in

`repo new` can seed the otherwise-empty repository from `--template`, using a
local directory, Git URL, or forge shorthand. `--template-ref` selects a
branch, tag, or commit and `--template-subdir` selects the directory that
becomes the new repository root. Presets expose the same settings as
`template`, `template_ref`, and `template_subdir`; `--template=none` disables a
preset template.

A template is a filesystem snapshot, not a clone: source history, remotes and
Git metadata are not copied. A local Git worktree without a ref uses its current
tracked files plus untracked files that are not ignored; ignored build outputs
and secrets stay out. A non-Git local directory snapshots its complete current
tree. A Git source with a ref, or a remote Git source, snapshots the resolved
commit. The confirmation/dry-run plan previews relative paths and warns when a
local source is live rather than pinned to a commit.

dev validates and reads the complete snapshot before changing the destination,
rejects traversal, symlinks and special files, excludes `.git`, and preserves
regular-file modes and empty directories. Held root, directory, and file
handles confine reads and writes if a path is swapped while the operation is in
progress. URL userinfo such as embedded credentials is redacted from plans,
results, and errors. Templates apply only to a new repository; use `repo clone`
when history should be retained.

`--check-in=auto|commit|stage|none` controls the generated changes:

- `auto` follows a new repository's preset (`initial_check_in`, or legacy
  `initial_commit`); clone/setup otherwise preserve history and default to no
  automatic check-in.
- `commit` stages and commits with the preset or `--message` text.
- `stage` runs `git add -A` and leaves the index ready for human review.
- `none` leaves generated changes unstaged.

`repo setup --commit` remains a compatibility alias for
`--check-in=commit`. Creating an upstream from `repo setup`, pushing a newly
created upstream, and handing changed setup directly to `start` require
`commit`. `stage` cannot create a new upstream; review and commit locally
first.

Stage mode also writes the suggested message to the worktree Git directory as
`LAZYGIT_PENDING_COMMIT`, so lazygit's lowercase `c` dialog opens with that
message. This is a best-effort lazygit implementation detail, not a Git commit
template: `.git/COMMIT_EDITMSG`, `commit.template`, uppercase `C`, and other Git
clients are not promised to consume it. An existing different lazygit draft is
preserved and reported rather than overwritten. Staging is the primary
operation: if the optional draft cannot be written, setup still succeeds with
the index staged and emits a warning telling the user to enter the message
manually.

## Presets

The built-in `minimal` preset matches scripted creation. `agent-ready` adds a
managed `.gitignore`, `AGENTS.md`, and repository-local Claude plans. Optional
skills may declare a setup entrypoint. For `agent-history-hygiene` and
`project-knowledge-harness`, dev installs the skill but uses reviewed built-in
initializers before the first commit instead of executing newly downloaded code.

The built-in `agent-history-hygiene` initializer seeds
`.pre-commit-config.yaml`, `.gitleaks.toml`, and, when absent,
`.specstory/.gitignore`. That nested ignore contains only `/.project.json` and
`/statistics.json`; `.specstory/history/*.md` remains part of the review trail
and must not be ignored. If the nested file already exists, dev preserves its
custom rules and mode and appends only whichever required entries are missing.
If a parent `.gitignore` already ignores the entire `.specstory/` directory,
review and remove that broader rule yourself.

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
