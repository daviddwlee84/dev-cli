# Repository quick notes

Capture informal thoughts without adding another dot-folder to the repository.

## Quick use

```bash
dev repo note add "try event subscription" --repo api --tag idea
dev repo note list api
dev repo note search "event subscription"
dev repo note show <id-or-prefix>
dev repo note edit <id-or-prefix>
dev repo note delete <id-or-prefix>
```

Inside a repo, `--repo` is optional. An ID prefix must be unique and at least
eight characters.

## Choose the right note-like field

- task `--next` is the next executable action;
- `dev work park --note` records context on one task;
- `dev repo mark --note` stores one catalog metadata summary;
- `dev repo note` stores multiple durable repository observations.

Quick notes are not task lifecycle state and do not overwrite the catalog
summary.

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
opens only the Markdown body in VISUAL/EDITOR, validates non-empty content,
atomically replaces the durable Markdown source, then updates the disposable
index. A later search reconciles a stale index. If another process changed the
note while the editor was open, dev rejects the stale update and reports where
the edited temporary body was preserved.

## Where notes live

```text
<paths.state_dir>/notes/<catalog-repo-id>/<note-id>.md  durable source
$XDG_CACHE_HOME/dev/notes.db                            disposable FTS
```

Both files use mode 0600 on Unix. On Windows, privacy follows the containing
directory's ACL rather than Unix permission bits.

`paths.state_dir` defaults to `$XDG_DATA_HOME/dev`. Repository catalog IDs
survive path moves, symlink indexes and linked worktree paths. A note created
while standing in a worktree therefore appears on the canonical repo. `dev`
does not synchronize note or catalog files; sync both parts of sidecar state
when attachments must travel between hosts.

```bash
dev repo note path api
dev repo note path --all
dev repo note reindex
dev self cache clear notes       # source Markdown remains
dev repo note search "query"     # rebuilds missing/stale index
```

Search is term-wise prefix FTS for Latin text, with literal term-wise fallback
for CJK substrings.

Structured task systems such as td/beads are separate optional future adapters;
dev does not initialize their repo dot-folders.
