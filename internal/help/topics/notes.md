# Repository quick notes

Capture informal thoughts without adding another dot-folder to the repository.

## Quick use

```bash
dev note add "try event subscription" --repo api --tag idea
dev note list api
dev note search "event subscription"
dev note show <id-or-prefix>
dev note edit <id-or-prefix>
dev note delete <id-or-prefix>
```

Inside a repo, `--repo` is optional.

## TUI

```text
TASKS / REPOS:  n quick add · N browse
REMOTE:         n/N only when a local clone exists
TRY:            n remains new Try
```

Notes overlay:

```text
j/k move · / search · Enter expand · a/n add · e edit · d delete · Esc back
```

Delete opens a visible confirmation and requires `y`. Editing suspends the TUI,
opens only the Markdown body in VISUAL/EDITOR, validates non-empty content, then
atomically updates the source and FTS index. If another process changed the note
while the editor was open, dev rejects the stale update and reports where the
edited temporary body was preserved.

## Where notes live

```text
$XDG_DATA_HOME/dev/notes/<catalog-repo-id>/<note-id>.md  durable, mode 0600
$XDG_CACHE_HOME/dev/notes.db                            disposable FTS, mode 0600
```

Repository catalog IDs survive path moves, symlink indexes and linked worktree
paths. A note created while standing in a worktree therefore appears on the
canonical repo.

```bash
dev note path api
dev note path --all
dev note reindex
dev cache clear notes       # source Markdown remains
dev note search "query"     # rebuilds missing/stale index
```

Search is term-wise prefix FTS for Latin text, with literal term-wise fallback
for CJK substrings.

`repo mark --note` is a separate single-line catalog summary. Structured task
systems such as td/beads are separate optional future adapters; dev does not
initialize their repo dot-folders.
