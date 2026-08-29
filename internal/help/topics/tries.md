# Tries and experiments

A Try is a dated scratch directory with a durable identity. The directory is
disposable and moves; the catalog ID that names it does not.

## Why an ID and not just a path

An experiment that turns out to matter gets archived, restored, renamed, or
promoted into a real project. Each of those changes its path. Anything that
referenced the experiment by path — a note, a tag, an activity record — would
break. `dev` keeps a stable catalog ID and treats the path as current location,
so the history survives every move.

## The lifecycle

```bash
dev try scratch-parser        # create it, or open it if it already exists
dev tries list                # what is active
dev tries mark scratch-parser --add spike --note "compare two tokenizers"
dev tries archive scratch-parser   # out of sight, ID and metadata kept
dev tries restore scratch-parser   # back into a visible path
dev graduate scratch-parser        # promote it into a real project
```

`dev tries deprecate` marks an experiment finished without moving anything, for
the case where the directory is still worth reading but should stop appearing
as active work. `dev tries reactivate` undoes it.

## What is not a Try

A Try is not a task and not a worktree. It has no branch, no base, and no
lifecycle state — nothing to park or resume. If the work needs a branch and an
integration path, start a task instead:

```bash
dev start parser-rewrite --repo myproject --base main
```

Graduation is the bridge: it turns an experiment that earned a future into a
real repository, after which ordinary task flow applies.

## Moves are guarded

Archive, restore, and graduate all move directories. Each one revalidates the
source, refuses to cross a filesystem boundary silently, rejects a path that
escapes its root through a symlink or `..`, and records an intent so an
interrupted move can be rolled back or reconciled rather than left half-done.
