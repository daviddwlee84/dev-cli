# Repository bootstrap

`dev repo new`, `repo clone`, and `repo setup` share one scaffold pipeline. A
plain name creates a new repository. A clear Git URL, local Git path, or
`owner/name` passed to either `repo new` or its `create` alias routes to clone
automatically, preserving source history and its remote. Template and
upstream-creation flags conflict with that route; use explicit `repo clone`
whenever callers should state acquisition unambiguously.

```bash
dev repo new                              # interactive
dev repo create api                       # minimal, script-friendly
dev repo new api --template owner/starter --check-in=stage
dev repo clone owner/api                  # optional setup after clone
dev repo setup . --preset agent-ready --check-in=stage
```

The interactive default is `agent-ready`; explicit `repo new NAME` remains
`minimal`. Its canonical `AGENTS.md` starter explicitly labels bootstrap status
incomplete, supplies safe repository-wide and handoff rules, and leaves unknown
purpose, commands, architecture, and invariants as TODOs rather than inventing
project facts. Use `--dry-run` before a custom preset and `--json` for
automation. Dry-run does not mutate the target repository. JSON never prompts,
changes directory, or activates a runtime; automation supplies the required
name/ref and an explicit setup preset.

`repo setup` repeat-safely merges native initializers and preset files. Custom
hooks and skill setup remain responsible for their own idempotency.

The no-argument create/setup wizards keep the common path short. After preset
selection they ask `Customize preset ... options?` once. Answering no accepts
the resolved preset and skips its detailed template, typed-input,
README/gitignore/license, Claude plans, AGENTS, skill and catalog questions.
The gate defaults to yes when the command line already selected a customization
or a required typed input has no default. This changes prompt volume, not preset
resolution: supplied flags still participate in the final plan.

On a real terminal, every shared wizard text field uses an inline editor.
Cursor keys, Home/End, Backspace and Delete edit the current text rather than
being decoded as literal `^[[C`/`^[[D` bytes. Esc and Ctrl-C cancel. Bracketed
defaults remain hints accepted by Enter rather than text silently inserted into
the field. Non-TTY and piped callers retain deterministic line-based input.

## Repository selection

Bare `dev repo clone` uses the existing private forge cache as a candidate list.
It passes the selected provider's exact clone URL into the normal acquisition
flow, supports stale/incomplete cache with a warning, and never refreshes the
network merely because the picker opened. Manual URL, path, and `owner/name`
entry remains available; a missing or source-mismatched cache goes directly to
that prompt.

Outside a checkout, bare `dev start` uses fast live repository discovery for
its picker and performs a full resolve after selection. Inside a repository it
keeps the immediate current-repository default without scanning all roots. `[picker].command` is one direct argv vector,
defaulting to `fzf`; no shell evaluates it. A missing executable falls back to
the built-in Bubble Tea selector, while `command = []` forces that backend.
Compatible external selectors read lines on stdin and return one unchanged line
on stdout. See the repository `contrib/` directory for Television and fzf
composition over `dev repo remote --cached --json`.

## Template snapshots

Only `repo new` accepts a starter tree:

```bash
dev repo new api --template ./starters/service
dev repo new api --template owner/starter --template-ref v2 \
  --template-subdir services/go
```

`--template` accepts a local directory, Git URL, or forge shorthand.
`--template-ref` selects an exact branch, tag, or commit;
`--template-subdir` promotes one relative directory to the new repository root.
A preset may provide `template`, `template_ref`, and `template_subdir`, and an
explicit `--template=none` disables that preset source.

This is intentionally not clone semantics. dev takes a content snapshot and
then initializes a fresh repository, so source commits, remotes, branches and
all `.git` metadata stay behind. Source selection is:

- A local Git worktree without `--template-ref` uses its current tracked files
  plus untracked files selected by Git's standard excludes. Ignored files are
  omitted, so build output and locally ignored secrets are not accidentally
  copied.
- A non-Git local directory uses its complete current regular-file tree.
- A local Git source with a ref, or a remote Git source, uses the tree at the
  resolved commit.

Use `repo clone` when source history and its remote should survive. Credential
userinfo embedded in an HTTP(S) or similar URL is available to Git for the
fetch but is redacted anywhere dev renders, returns, or wraps that source.

Preparation finishes before destination mutation. The snapshot owns stable
copies of file bytes, modes and empty directories; a local source changing
during the read fails closed. Parent traversal, absolute subdirectories,
symlinks, special files and reserved `.git` paths are rejected. Snapshot and
application traverse through held root, directory, and file handles rather
than re-resolving trusted string prefixes, so a directory-to-symlink or rename
race cannot redirect access outside the selected trees.

Application requires a newly initialized repository containing only Git
metadata, uses exclusive file creation, and confines every target below its
held destination root. Template files are materialized before scaffold files,
so preset file generation skips collisions instead of overwriting the selected
starter; native merge initializers may still merge their specific safe
settings.

`--dry-run` never creates the target, but preparing a remote template may still
fetch it into an isolated temporary checkout so the plan can report the exact
resolved commit and content counts. Human plans preview a bounded list of
quoted relative file paths plus a remaining count. A local snapshot with no
resolved commit is explicitly labelled as live current files and should be
reviewed before check-in or publication.

## Check-in disposition

Repository acquisition, generated content, and Git check-in are separate
decisions:

```bash
dev repo new api --check-in=commit
dev repo new api --check-in=stage --message "chore: initial commit"
dev repo setup . --preset agent-ready --check-in=none
```

`--check-in` accepts:

- `auto`: for a new repository, use preset `initial_check_in`; legacy
  `initial_commit=true|false` maps to `commit|none`. Clone/setup preserve
  existing history and otherwise resolve `auto` to `none`.
- `commit`: run `git add -A`, create the commit, and then run `after_commit`
  skill/hook phases.
- `stage`: run `git add -A` after files, selected skills, built-in setup and
  `before_commit` hooks, but leave the commit for the user. `after_commit` does
  not run.
- `none`: leave generated changes unstaged.

`repo setup --commit` remains a compatibility alias for
`--check-in=commit`; combining it with `stage` or `none` is an error. The
`--message` value applies to both committed and staged-for-review outcomes.
Project config can seed an existing-repository wizard with
`[repo.setup] check_in = "stage"`; it cannot set both `check_in` and the legacy
`commit` boolean.

Stage mode seeds `<worktree-git-dir>/LAZYGIT_PENDING_COMMIT` with the suggested
message. Lazygit's lowercase `c` reads this private per-worktree draft and
clears it after a successful commit. It is deliberately best effort:

- Git's `.git/COMMIT_EDITMSG` and `commit.template` are different mechanisms;
  lazygit's `c` does not use them.
- Lazygit's uppercase `C` editor path and other Git clients are not promised to
  read the draft. From lowercase `c`, switching to the editor preserves the
  already loaded text.
- A pre-existing different draft is user state, so dev leaves it untouched and
  warns instead of replacing it.
- Failure to create or sync this optional draft is also only a warning. The
  successfully staged index remains the result; enter the displayed message
  manually rather than rerunning setup.

Creating an upstream from `repo setup` requires `commit`. A newly created
upstream can only be pushed after `commit`, while `stage` cannot be combined
with new-upstream creation at all. An existing clone remote does not prevent
local stage-for-review. `start` also requires a clean committed setup; use
`cd` or `open` while reviewing staged changes.

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

For SpecStory, the common managed `.gitignore` used by `agent-ready` adds only
the exact `.specstory/statistics.json` rule. It deliberately leaves
`.specstory/history/*.md`, `.specstory/.project.json`, and `.specstory/cli/`
visible to Git.

The built-in `agent-history-hygiene` setup creates these files when absent:

```text
.pre-commit-config.yaml
.gitleaks.toml
.specstory/.gitignore
```

The nested SpecStory ignore is intentionally narrow: it ignores only
`.specstory/.project.json` (machine-local project identity) and
`.specstory/statistics.json` (generated session statistics). It does not ignore
`.specstory/history/*.md`; transcripts remain durable review artifacts that
belong beside the changes they produced. When `.specstory/.gitignore` already
exists, dev preserves its custom lines and permission mode and idempotently
appends only missing required rules. Symlink or special-file replacements are
rejected. A top-level rule that ignores all of `.specstory/` still wins, so
remove or correct that broader rule when transcript history is intended to be
committed.

The wizard only offers GitHub/GitLab publication when the matching CLI is
installed and authenticated. Publication uses the local repository name and
configured description, follows required local commit steps, and optionally
pushes the initial/current branch. A create, remote-add, or push failure leaves
local work intact.

Handoff is explicit: `stay`, `cd`, `open`, or `start`. `start` enters the normal
task wizard. None of these choices launches a coding agent.

REPOS `n` uses this same wizard in a suspended terminal with `--handoff stay`,
then refreshes local dashboard inventory. It is available even when no
repository row exists; the TUI does not maintain a reduced second creator.
