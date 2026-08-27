# Worktree ownership

Read this before creating a worktree, or when a new checkout is missing its
dependencies or `.env`.

## The confusion this resolves

Three mechanisms create git worktrees, and they all look interchangeable:

- `claude --worktree <name>` / `isolation: worktree` / `/batch` — Claude Code's
  own, under `.claude/worktrees/`.
- `herdr worktree create --branch … --base …` — creates a checkout under
  `~/.herdr/worktrees/` and opens it as a herdr workspace.
- `git worktree add` by hand, or a tool like Worktrunk.

They are all standard git worktrees underneath, so nothing *breaks* if you mix
them. What breaks is the mental model: two lifecycle owners for one checkout,
and no single answer to "where do worktrees live on this machine?"

## The rule

| Kind of worktree | Owner | Where | Lifetime |
|---|---|---|---|
| Feature, fix, experiment, cross-machine handoff | **`dev`** | `paths.worktree_path` | until `dev done` / `dev sweep` |
| Turn-scoped subagent isolation | **Claude Code** | `.claude/worktrees/` | dies with the turn |
| Everything else | **`dev`** | as above | as above |

**Might a human come back to it tomorrow → `dev`. Does it die with this agent
turn → Claude Code native.**

Concretely:

- Two agents implementing *different approaches* to the same problem, so the
  best can be picked → Claude Code worktrees. They are disposable.
- Two agents working on *disjoint files of one feature* → no worktree at all.
  One checkout, one branch, separate panes. Worktrees isolate change streams;
  panes isolate agents.
- A feature you will still be working on next week → `dev start`.
- Picking up on a different machine → `dev park --cold --push` here,
  `dev resume` there. The branch is the identity; the directory is a cache.

## Why `dev` does not call `herdr worktree create`

`dev` runs `git worktree add` itself, then calls
`herdr worktree open --path <path> --label <task>`.

The path policy has to hold on machines without herdr — a laptop, a CI box, a
server you ssh into. Delegating placement to herdr would mean worktrees land
somewhere different depending on what happens to be installed. Creating the
checkout with git and asking herdr only to *open* it keeps one policy and still
gets the full sidebar treatment: the workspace is grouped under its parent repo
with its own branch and ahead/behind row.

## Never nest a worktree inside a repository

`.claude/worktrees/` is acceptable for turn-scoped work because Claude Code
cleans it up and the directory is gitignored. For anything longer-lived, a
checkout inside another checkout means every file watcher, language server,
indexer, backup tool and `rg` run in the outer repo sees a second copy of the
whole tree.

`dev` refuses to create one, and will tell you to point
`paths.worktree_path` somewhere outside the repo.

If you do use `.claude/worktrees/`, keep `.claude/worktrees/` in `.gitignore`
so the contents never show as untracked files in the main checkout.

## Where dev puts them

Default:

```
~/Worktrees/<repo>/<branch-slug>
```

Fully configurable, because the right answer depends on the machine — a
different volume, a different naming convention:

```toml
[paths]
worktree_root = "/mnt/fast/worktrees"
worktree_path = "{{worktree_root}}/{{repo|lower}}/{{branch|slug}}"
```

Variables: `worktree_root`, `repo`, `repo_path`, `branch`, `category`, `host`,
`date`. Filters: `|slug` (filesystem-safe: `feat/auth/x` → `feat-auth-x`),
`|lower`, `|base`.

The reason for a separate root rather than siblings of the repo: with one or
two worktrees, `repo/` and `repo.feat-auth/` side by side is perfectly clear.
With ten, the projects directory stops answering "what projects do I have?" —
the filesystem hierarchy starts doing git's job badly.

## Provisioning: making a worktree actually usable

A worktree is a **clean checkout**. It has no `node_modules`, no `.venv`, and
none of the gitignored files the project needs to run. Without provisioning,
every new worktree starts broken.

`dev wt create` (and `dev start`) does three things, configured under
`[worktree]` globally or in a repo's own `.dev.toml`:

```toml
[worktree]
# Gitignored files to carry over.
include = [".env", ".env.local"]

# Heavy directories to symlink instead of copy. Opt-in and empty by default —
# sharing node_modules across checkouts breaks native builds often enough that
# it must never happen by accident.
link = []

# "auto" detects from lockfiles, or give an explicit list.
post_create = "auto"
```

**Only files that are both listed *and* gitignored are copied.** A tracked file
is already in the new checkout on the correct branch; copying the source
branch's version over it would be wrong. The classic mistake is listing
`.vscode/settings.json` — it is committed, so it is already there.

`post_create = "auto"` detects one command per ecosystem:

| Marker | Command |
|---|---|
| `uv.lock` | `uv sync` |
| `poetry.lock` | `poetry install` |
| `pnpm-lock.yaml` | `pnpm install --frozen-lockfile` |
| `package-lock.json` | `npm ci` |
| `yarn.lock` | `yarn install --immutable` |
| `go.mod` | `go mod download` |
| `Cargo.toml` | `cargo fetch` |
| `Gemfile.lock` | `bundle install` |

A detected command whose tool is not installed is skipped, not failed. A failed
command is reported but leaves the checkout in place — it is usable, and
throwing it away would cost the branch for nothing.

Per-repo override, committed next to the code so it reaches every machine:

```toml
# <repo>/.dev.toml
[worktree]
include     = [".env", "config/local.json"]
post_create = ["make bootstrap"]
```

Re-run provisioning on an existing checkout with `dev wt provision`.

## Cleanup

```bash
dev wt rm feat/auth      # removes the checkout; the branch survives
```

Removing a worktree and abandoning a change stream are different decisions.
`dev` never deletes a branch as a side effect, and refuses to remove a checkout
with uncommitted changes without an explicit `--force`.

If a directory was deleted behind git's back, `dev wt rm` and `dev sweep` prune
the stale administrative entry instead of failing.
