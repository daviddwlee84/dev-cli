# Bootstrapping an existing machine

How to discover and optionally organise repositories without breaking their
current layout.

## Start with a report

```bash
dev bootstrap ~/code /mnt/work
dev bootstrap ~/code --max-depth 0     # unlimited depth
dev bootstrap ~/code --json            # machine-readable inventory
```

The scanner recursively identifies:

- canonical checkouts;
- linked worktrees (their own checkout, shared Git common directory);
- bare repositories / worktree hubs;
- symlink aliases to the same physical checkout.

It deduplicates by Git identity rather than by path, so a physical checkout,
an index symlink to it, and its linked worktrees are represented honestly
instead of looking like unrelated duplicate repositories.

A plain scan changes nothing.

## Option A — make a symlink index (recommended)

```bash
dev bootstrap ~/code /mnt/work --index ~/Projects --layout flat
dev bootstrap ~/code /mnt/work --index ~/Projects --layout flat --apply
```

This creates only symlinks:

```
~/Projects/api       → /mnt/work/company/backend/api
~/Projects/dotfiles  → ~/src/config/dotfiles
```

The physical repositories remain exactly where they were. This is the safe
answer when the existing layout works but navigation does not.

Layouts:

- `flat` — `<index>/<repo>`; duplicate names are reported as blocked rather
  than silently renamed.
- `preserve` — mirror each repo's path relative to its scan root.

`--relative-links` makes the catalog portable when the catalog and repos move
together. Absolute links are less surprising across mount points and are the
default.

To use the index as dev's navigation UI, put it first in `scan_roots`; direct
symlinks to repositories are first-class discovery entries, and scanning the
index plus the physical root still shows one repo:

```toml
[paths]
scan_roots = ["~/Projects", "/mnt/work/company", "~/src"]
```

Or have bootstrap write a new config:

```bash
dev bootstrap /mnt/work --index ~/Projects --apply \
  --config-out ~/.config/dev/indexed.toml
```

## Option B — move the physical repositories

Use this only when you actually want a new disk layout, not merely easier
navigation.

```bash
dev bootstrap ~/old --move ~/Projects --layout preserve       # plan
dev bootstrap ~/old --move ~/Projects --layout preserve --apply
```

Move is deliberately stricter than indexing. It refuses a repository when:

- it has uncommitted changes;
- it has any linked worktree, even outside the scanned paths;
- a live runtime session or the current shell is inside it;
- a symlink alias would become broken;
- the destination exists;
- source and target are on different filesystems (an atomic rename is
  impossible there).

If one row is blocked, `--apply` moves none. Resolve every row and plan again.
That makes the report the exact artifact being approved rather than a vague
preview of a partial operation.

When a move succeeds, task `repo_path` fields are updated. `scan_roots` is not
silently rewritten; use `--config-out` or edit it, because config comment and
root order are user-owned policy.

## Flat layouts are already first-class

No category is required anywhere:

```toml
[paths]
scan_roots   = ["~/code"]
project_root = "~/code"

# New repos land at ~/code/<repo> when --category is omitted.
```

Worktrees have their own fully configurable template:

```toml
[paths]
worktree_root = "/mnt/fast/wt"
worktree_path = "{{worktree_root}}/{{repo|lower}}--{{branch|slug}}"
```

That produces a completely flat worktree pool. `flat` vs categories is a
configuration choice, not a migration requirement.

## Existing work in flight is a separate step

Bootstrap answers **what repositories exist and where**. `dev adopt` answers
**which branches/worktrees/sessions are active work**:

```bash
dev bootstrap ~/code       # inventory the machine
dev adopt                  # report work in flight
dev adopt --apply          # record selected candidates as tasks
```
