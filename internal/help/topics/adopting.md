# Adopting an existing setup

What to do on a machine that already has repositories, worktrees and sessions.

## There is nothing to migrate

`dev` does not own a directory layout. It discovers repositories wherever
`paths.scan_roots` points, and **never moves, renames or deletes anything** you
already have. Whatever your projects tree looks like — `~/Documents/Program`,
`~/src`, `~/code`, a ghq root, several of them — it keeps working as is.

```bash
dev config init     # detects this machine's roots and writes a config
dev repo list       # confirm it found what you expected
```

`config init` probes the conventional locations, counts the repositories in
each, and writes only the ones that exist. If it guessed wrong, the generated
file is ordinary TOML — edit `scan_roots`.

## Importing work already in flight

Repositories are discovered; *tasks* are not. `dev adopt` finds the work that
is already happening and offers to record it:

```bash
dev adopt                   # report only
dev adopt --apply           # record, confirming each one
```

It looks for three things:

- **linked worktrees** git already knows about, from any tool;
- **live runtime sessions** sitting in a repository;
- **local branches ahead of the default branch** — unfinished work that is
  otherwise invisible.

It skips branches already contained in the base (that work has landed) and the
turn-scoped worktrees an agent harness creates and cleans up itself.

Nothing on disk changes either way. Adopting only writes task entries.

Afterwards, give the ones you care about a next action — that is what makes
parking them cheap later:

```bash
dev park <task> --next "…"
```

## Worktrees you already have elsewhere

A worktree created by hand, or by another tool, in a location that is not
`paths.worktree_path` keeps working exactly as it did. `dev` records the path
git reports; it does not relocate it.

New worktrees go to the configured template. If you want the old ones there
too, move them the way git expects:

```bash
git worktree move <old-path> ~/Worktrees/<repo>/<branch-slug>
```

There is no need to. Consistency is nice; disruption is not.

## A different projects root

```toml
[paths]
scan_roots    = ["/mnt/work/repos", "~/code"]
project_root  = "/mnt/work/repos"
worktree_root = "/mnt/work/worktrees"
worktree_path = "{{worktree_root}}/{{repo|lower}}/{{branch|slug}}"
```

`project_root` is only used by `dev repo new|clone` and `dev graduate` to
decide where a *new* project goes. Existing ones are found through
`scan_roots` regardless.

## Trying it without committing to anything

Every path is overridable per invocation, so you can point a throwaway config
at a sandbox and see what happens:

```bash
dev --config /tmp/try-dev.toml config init
dev --config /tmp/try-dev.toml ls
```

And `dev --no-runtime` keeps it from touching your multiplexer at all.
