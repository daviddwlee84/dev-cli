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
dev tries try scratch-parser        # create it, or open it if it already exists
dev tries list                # what is active
dev tries mark scratch-parser --add spike --note "compare two tokenizers"
dev tries archive scratch-parser   # out of sight, ID and metadata kept
dev tries restore scratch-parser   # back into a visible path
dev tries graduate scratch-parser        # promote it into a real project
dev tries demote scratch-parser --dry-run # preview returning a graduated Try
```

`dev tries deprecate` marks an experiment finished without moving anything, for
the case where the directory is still worth reading but should stop appearing
as active work. `dev tries reactivate` undoes it.

## What is not a Try

A Try is catalog identity, not a task mode or task lifecycle state. It may
contain Git history and branches, but cannot itself be parked or resumed. If
work needs tracked task intent and an integration path, start a task:

```bash
dev work start parser-rewrite --repo myproject --base main
```

Graduation is the bridge: it turns an experiment that earned a future into a
real repository, after which ordinary task flow applies.

## Return a graduated Try

```bash
dev tries demote <repo-or-path-or-catalog-id> --dry-run
dev tries demote <repo-or-path-or-catalog-id>
dev tries demote <catalog-id> --to ~/src/tries/2026-09-24-parser
```

Only a repository previously graduated from a Try can be demoted. The CLI shows
source and destination and applies the move; `--dry-run` only previews. REPOS's
action menu previews the same plan and asks for confirmation.

Run from outside the checkout. A destination must be an immediate visible child
of the current Try root, and runtime coverage must be observable; `none` does
not prove that the checkout is unused.

The recorded original Try path is the default. If it is occupied or outside the
current `tries_root`, choose a safe destination explicitly with `--to`. Demote
keeps catalog ID, tags, notes, graduation history, Git history/remotes and every
current file, including dirty, untracked and ignored data. The Try is active and
present again. It never reverses commits/publication or leaves a symlink.

Task/runtime/agent/artifact claims are not retargeted. Canonical repositories
with linked worktrees, unsafe paths, incomplete observations and stale plans
block the move. Revalidation and interrupted-move recovery still apply.

| Dimension | Transitions |
|---|---|
| Identity | Try → graduate → Repo → demote → Try |
| Intent | active → deprecate → deprecated → reactivate → active |
| Storage | Try → archive → Archive → restore → Try |
| Disposal | Try → delete → Trash; restore there, then tries restore --from |

Deprecation does not move files. Permanent deletion cannot be restored. Remote
existence or a synced branch does not prove that all local data is recoverable.

## Moves are guarded

Archive, restore, graduate and demote all move directories. Each one revalidates the
source, refuses to cross a filesystem boundary silently, rejects a path that
escapes its root through a symlink or `..`, and records an intent so an
interrupted move can be rolled back or reconciled rather than left half-done.

## Explicit disposal and system Trash

```bash
dev tries delete scratch-parser --dry-run
dev tries delete scratch-parser
dev tries delete <id> --permanent --confirm-delete <id>
dev tries restore <id> --from <restored-path>
```

Delete (`rm` is an alias) previews the exact catalog ID, path, entry count,
logical size and method. The default uses macOS Trash, Linux GIO Trash, or the
Windows Recycle Bin. Unavailable Trash never falls back to permanent deletion.
`--yes` approves Trash for scripts; permanent disposal needs its own exact ID
confirmation. `--json` never prompts, and `--dry-run` never removes anything.

Deletion includes ignored/untracked files, local branches/tags and stash. No
backup has been verified. Task claims, live runtime, cwd, nested repositories,
linked/shared Git, mount boundaries and unsafe paths block removal. Unknown
runtime coverage needs an explicit `--assume-no-runtime` acknowledgement; it
cannot override observed occupation.

Catalog identity, metadata and other-host locations remain. Local state becomes
`evicted`; `removal_id` and `removal_method` describe the operation, and durable
JSON records live in `try-removals` beside the assets directory. Interrupted
operations remain indeterminate and are never retried automatically.

For Trash recovery, first restore the original directory using the system UI,
then use `restore --from` before modifying it. dev verifies the retained filesystem
identity and contents. Archive restoration continues to use ordinary `restore`
(or `--to`). Permanent deletion has no restoration path. Neither archive nor
Trash releases space until the bytes are actually deleted.

After an interrupted Trash operation, `restore --from` can explicitly reassociate
an unchanged original/restored folder; it never retries deletion. A folder put
back into its original archive path remains archived and can then use ordinary
`tries restore` to become visible again.

## Dashboard navigation and organizer entry

Enter opens a repository/task row or starts FLEET host navigation. Space expands or collapses REPOS/FLEET trees; flat lists leave Space unused. REPOS/TRY `Ctrl+O` offers organization of the
current item, filtered results, or all local work; multi-selection belongs to the
independent triage screen. `1–7` selects TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS,
and MCP respectively. TASKS state filters live in its action menu (`a` still
shows done tasks). Click a data column for ascending → descending → default
ordering; FLEET HOST groups machines. Sorting is local to each view/session and
uses the current snapshot, with unknown values last. The footer keeps two lines
of primary actions and navigation plus a version/update row; tools, state filters and sorting are in
`Ctrl+O`, and `?` lists the full key map. Existing custom tool bindings for `4–7`
need reassignment; `x`/Ctrl+A are no longer reserved dashboard selection keys.

Triage uses grouped repo/Try checkboxes, mouse selection, and Ctrl+A all/none
within the filtered scope. Results preserve failures when returning to the
dashboard. Git synchronization diagnostics retain a category, exit code, bounded
redacted output and a next step; old receipts cannot recover discarded reasons.
Retries require a new preview. Authentication, fetch and rebase are never
silently performed as error recovery. Use `dev triage` to open the organizer.
