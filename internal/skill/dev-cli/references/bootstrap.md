# Bootstrapping an existing machine

Read this when a user asks to initialise dev on an existing machine, recursively
find repositories, create a flat project catalog, reorganise physical repos, or
migrate from ghq / Worktrunk / a hand-built directory layout.

## Default procedure

Use this order; do not skip the report before an operation:

- [ ] 1. Inspect the effective config: `dev config show`.
- [ ] 2. Scan only: `dev bootstrap <roots...>`.
- [ ] 3. Review canonical checkouts, worktrees, bare repos, aliases and warnings.
- [ ] 4. Prefer a symlink index when the problem is navigation, not storage.
- [ ] 5. Plan the operation without `--apply` and show the user every blocked row.
- [ ] 6. Apply only after the user explicitly chose index vs move and the target.
- [ ] 7. Verify with `dev repo list --long` and `dev doctor`.
- [ ] 8. Run `dev adopt` separately to find work in flight; bootstrap itself does
      not invent task intent from filesystem layout.

## Report only

```bash
dev bootstrap ~/code /mnt/work
dev bootstrap ~/code --max-depth 0
dev bootstrap ~/code --json
```

A scan changes nothing. It recursively identifies canonical checkouts, linked
worktrees, bare repositories and symlink aliases, deduplicating by Git common
directory rather than path.

## Prefer a symlink index

When existing repositories are in usable places but hard to navigate, make a
catalog instead of moving them:

```bash
dev bootstrap ~/code /mnt/work --index ~/Projects --layout flat
dev bootstrap ~/code /mnt/work --index ~/Projects --layout flat --apply
```

Only symlinks are created. Physical repositories remain authoritative.

- `flat`: `<index>/<repo>`. Duplicate names are blocked; do not invent suffixes
  without asking the user.
- `preserve`: mirror each path relative to its scan root.
- `--relative-links`: portable when index and repos move together; absolute is
  the default and is less surprising across volumes.

To make the index the UI, put it first in `paths.scan_roots`. Normal repo
discovery follows direct repo symlinks and deduplicates index + physical paths,
so the same clone appears once and the first root's path wins.

## Physical move — fragile, explicit only

```bash
dev bootstrap ~/old --move ~/Projects --layout preserve    # plan only
dev bootstrap ~/old --move ~/Projects --layout preserve --apply
```

Do not add `--apply --yes` on the user's behalf. Move is blocked when a repo is
dirty, has linked worktrees, has a live session or the current shell inside,
has symlink aliases, targets an occupied path, or crosses filesystems. If any
row is blocked, no move runs. Resolve the state and plan again.

A successful move updates task `repo_path` values, but does not rewrite
`scan_roots` silently — root ordering and config comments are user-owned policy.
Use `--config-out <path>` to generate a new config when wanted.

## Flat layouts need no migration

```toml
[paths]
scan_roots   = ["~/code"]
project_root = "~/code"
worktree_root = "/mnt/fast/wt"
worktree_path = "{{worktree_root}}/{{repo|lower}}--{{branch|slug}}"
```

Omit `--category` and new repos land at `<project_root>/<repo>`. The worktree
path template can likewise be fully flat. Do not move a user's repos merely to
match the examples in dev's default config.

## Output shape

The human report has one row per checkout:

```text
KIND       REPO  BRANCH   GIT    ALIASES  PATH
canonical  api   main     clean  1        /mnt/work/api
worktree   api   feat/x   ●      —        /mnt/wt/api/feat-x
bare       hub   —        —      —        /srv/git/hub.git
```

Organisation plans add `ready`, `blocked`, or `current` to every row. Treat a
blocked reason as a required precondition, not as something to override.

## Gotchas

- A direct symlink to a repo and its physical checkout are one clone. Root
  ordering decides which path the UI retains; put the curated index first.
- Linked worktrees may live outside every scanned root. Move safety asks Git for
  its complete worktree list, so they still block a physical move.
- An atomic rename cannot cross filesystems. Bootstrap refuses rather than
  falling back to recursive copy, because a partially copied `.git` directory
  is not a migration.
- Existing worktrees are recorded at their current paths by `dev adopt`; they do
  not have to be relocated to `paths.worktree_path`.
