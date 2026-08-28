# Repository quick notes

Read this when capturing, searching, editing or deleting informal repository
thoughts, or when deciding whether a thought belongs in dev, td, or beads.

## Storage boundary

Durable source:

```text
<paths.state_dir>/notes/<catalog-repository-id>/<note-id>.md
```

`paths.state_dir` defaults to `$XDG_DATA_HOME/dev` but is configurable.

Search index:

```text
$XDG_CACHE_HOME/dev/notes.db
```

Markdown is authoritative. SQLite FTS is disposable and rebuilt automatically
when absent or stale. `dev cache clear notes` must never remove Markdown.

Each note has a stable UUID, created/updated timestamps, normalized tags and a
Markdown body. Repo rename, symlink index or worktree path does not change its
attachment because the repository directory key is the catalog asset ID. `dev`
does not synchronize sidecar files: cross-host attachment assumes both notes
and catalog state have been synchronized.

## CLI workflow

```bash
dev note add "try event subscription" --repo api --tag idea
dev note list api
dev note search "event subscription" --repo api
dev note show <id-or-unique-prefix>
dev note edit <id-or-prefix>
dev note delete <id-or-prefix>       # confirms
dev note reindex
dev note path api
```

A note ID prefix must be unique and at least eight characters.

`add` requires text or `--editor`; it never creates an empty note. `edit` opens
a temporary body, then validates and atomically replaces the durable file only
when the editor succeeds with non-empty content. Tags survive unless `--tag`
is explicitly supplied. Edit uses an optimistic content revision under the
mutation lock; if another process won first, the stale edit is rejected and its
temporary body path is reported for manual recovery.

Search is term-wise prefix FTS for Latin text. Non-ASCII input uses literal
term-wise matching because SQLite `unicode61` does not segment CJK substrings;
`改善` must still find `記得改善 worktree`.

## TUI workflow

On TASKS or REPOS (including a child worktree):

```text
n   quick one-line note, Enter saves, Esc cancels
N   notes overlay for the selected repository
```

On a REMOTE row, notes are enabled only when a local clone exists. In TRY,
lowercase `n` remains “new Try”; repo quick notes do not attach to Try assets.

Inside NOTES:

```text
j/k       move
/         search body, tags and repo
Enter     expand/collapse body
a or n    add
e         edit body in VISUAL/EDITOR
d         visible delete confirmation; y confirms
N/Esc/q   return
```

The configurable REPOS column `notes` shows a count; it is not enabled by
default because the table is width-constrained. Repo detail shows count +
latest preview when notes exist; task detail does so when the task resolves to a
loaded repository row.

## What is not a quick note

- task `--next` is the next executable action;
- `dev park --note` stores context for one task;
- `catalog.Entry.Note`, exposed by `dev repo mark --note`, is one metadata
  summary for a catalog asset;
- `dev note` stores multiple durable repository observations and does not
  overwrite either task context or catalog metadata.

`td` / `beads` are structured repo-local task systems and may create dot-folders
inside a checkout. They remain optional future adapters. Do not mirror their
tasks into quick notes or initialize either tool automatically.

## Gotchas

- A note opened from a linked worktree attaches to the canonical clone by Git
  common-directory identity, not the disposable worktree path.
- Delayed TUI load results carry target ID + request generation and are ignored
  after switching repositories; never remove that guard from `noteListMsg`.
- Deleting `notes.db` deletes no thoughts; the next search rebuilds it.
- Deleting a Markdown file is durable deletion and requires confirmation.
- The FTS database contains full note bodies. Note files and the index use mode
  0600 on Unix; Windows privacy follows the containing directory's ACL.
