# Repo quick notes + TUI notes workflow

## Context

The user wants quick thoughts attached to a repository and triggerable directly
from the TASKS/REPOS TUI. Notes must live in dev's sidecar state rather than
creating another dot-folder in every repository. The in-progress
`try-tui-metadata-reclamation` / `dev-try-wobbly-lovelace` work is introducing
`internal/catalog` with stable asset IDs, path/symlink/common-dir matching and
cross-host locations. Notes should wait for that work to land and use its stable
repository ID rather than invent a competing identity.

Chosen interaction:

- multiple append-only notes, not one overwritable scratchpad;
- `n` adds a quick one-line note to the selected local repo;
- `N` opens a notes overlay for browse/search/edit/delete;
- Markdown files are durable truth; SQLite FTS is a disposable index;
- `td` and `beads` stay optional future adapters, not core dependencies.

## Dependency / sequencing

1. Wait until the catalog agent's changes are committed and available on the
   current branch.
2. Verify the final `catalog.Registry` API and storage location before writing
   notes code. Reuse `EnsureRepository`, `Match`, stable `Entry.ID`, and
   host-local location matching.
3. Do not modify or stage the other agent's currently untracked
   `internal/catalog/`, `internal/pathx/`, or App/config integration while it is
   active.

## Storage design

### Durable source of truth

```
$XDG_DATA_HOME/dev/notes/
└── <catalog-repository-id>/
    ├── <uuid>.md
    └── <uuid>.md
```

One file per thought minimizes cross-machine merge conflicts and keeps every
note readable/exportable without dev. Each file uses YAML/TOML-style frontmatter
plus Markdown body:

```markdown
---
schema_version: 1
id: <uuid>
repository_id: <catalog-id>
created: 2026-08-27T10:30:00Z
updated: 2026-08-27T10:30:00Z
tags: [idea, follow-up]
---

Try replacing the polling loop with an event subscription.
```

The filename ID and frontmatter ID must agree. Writes are temp-file + atomic
rename. Editing preserves ID/created/repository ID. Deleting requires explicit
confirmation (`--yes` escape hatch).

### Search index (disposable)

```
$XDG_CACHE_HOME/dev/notes.db
```

Use the existing pure-Go `modernc.org/sqlite` dependency with FTS5. Index note
body, tags, repo display name and catalog ID. The Markdown files remain
authoritative; if the database is absent/stale/corrupt, rebuild it.

- `dev note reindex` performs an explicit rebuild.
- add/edit/delete update the index incrementally.
- search detects a missing index and builds it.
- `dev cache list` reports `notes`; `dev cache clear notes` deletes only the
  FTS database and never the Markdown source.
- `stats.db` remains activity-only. Do not add note tables to it.

## CLI surface

Add `internal/note/` for the file store and FTS index, then wire:

```bash
dev note add [repo] "thought" [--tag TAG...]
dev note add [repo] --editor
dev note list [repo] [--tag TAG] [--json]
dev note show <note-id>
dev note search <query> [--repo REPO] [--json]
dev note edit <note-id> [--editor CMD]
dev note delete <note-id> [--yes]
dev note path [repo]
dev note reindex
```

Repo inference follows existing command conventions: explicit repo argument,
otherwise task/repository containing cwd. Resolve/ensure repository identity
through the catalog Registry using observed host, navigation path, real path,
Git common dir and normalised remote identity.

`add` requires text or `--editor`; it must not silently create an empty note.
Structured output goes to stdout; diagnostics to stderr.

## TUI interaction

Extend the existing context-sensitive key model in `internal/tui/model.go` and
`internal/tui/view.go`:

### Quick add (`n`)

- TASKS / REPOS: `n` opens a one-line prompt for the selected repo.
- REMOTE: only enabled when a local clone exists.
- Enter atomically creates one note and refreshes note counts/latest preview.
- Esc cancels without writing.

### Notes overlay (`N`)

Default scope is the selected repository:

```text
NOTES · dev-cli                                     4 notes

  WHEN   TAGS              THOUGHT
▸ 2m     idea,git          distinguish staged vs unstaged counts
  3d     follow-up         test glab member endpoint on newer CLI

j/k move · / search · Enter expand · a add · e edit · d delete · Esc back
```

- `/` uses the FTS index and searches body/tags/repo.
- Enter toggles expanded Markdown body.
- `a` enters the same add prompt as `n`.
- `e` suspends Bubble Tea via `tea.ExecProcess`, using the existing editor
  resolution (`--editor`/VISUAL/EDITOR/nvim/vim/vi), then atomically validates
  and reindexes the edited note.
- `d` enters a visible confirmation mode; no one-key deletion.
- The global `e = edit config` remains unchanged outside the notes overlay.

Add a configurable `notes` REPOS column (count), but do not enable it by
default because the default table is already width-constrained. The selected
repo/task detail pane shows note count and latest-note preview.

## Catalog integration

Create a small adapter from live `repo.Repo` / task checkout facts to
`catalog.Observation`; reuse it from CLI and TUI. The note store keys only by
catalog ID, never by mutable filesystem path. A rename, symlink index, physical
move or second host therefore keeps the same notes.

The existing `catalog.Entry.Note` field is not used as the multi-note store.
Leave it compatible for the catalog agent; optionally plan a later migration of
non-empty legacy `Entry.Note` into one append-only note after both features are
stable.

## External task-tool boundary

Keep the TODO entries for `td` and `beads`. A future adapter may detect their
repo-local dot-folders and delegate through their CLIs. It must not mirror those
tasks into dev notes or create their folders automatically. Notes are informal
sidecar thoughts; td/beads are structured repo-local task systems.

## Critical files

- New: `internal/note/{note,store,index}.go` and tests.
- Catalog integration: the final committed `internal/catalog` APIs and
  `internal/cli/app.go` registry wiring.
- CLI: new `internal/cli/note.go`, `internal/cli/root.go`, `internal/cli/cache.go`.
- TUI: `internal/tui/{model,rows,view}.go`, `internal/cli/tui.go` and tests.
- Config/path helpers: `internal/config/config.go` only if note paths/options
  need an explicit surface.
- Docs/skill: README, storage/TUI help topics, `internal/skill/dev-cli`,
  generated command reference, TODO promotion.

## Verification

### Unit

- note frontmatter round-trip and schema/ID validation;
- atomic create/edit and safe delete;
- one-note-per-file concurrency and deterministic ordering;
- catalog ID survives repo rename/symlink/host location change;
- FTS body/tag/repo search, missing-index rebuild and incremental update;
- clearing `notes.db` leaves Markdown files untouched;
- malformed note is diagnosed without hiding valid notes.

### TUI

- `n` adds to the selected TASK/REPO and is disabled for uncloned REMOTE;
- `N` overlay navigation, live search, expand, editor callback and confirmed
  delete;
- note overlay respects terminal height/top-bar invariants;
- returning from editor refreshes note data/index;
- optional notes column and detail preview.

### CLI / end-to-end

In a sandboxed HOME/XDG state:

```bash
dev bootstrap <repo-root>
dev note add demo "try event subscription" --tag idea
dev note list demo --json
dev note search "event subscription"
dev cache clear notes
dev note search "event subscription"   # rebuilds FTS; still finds it
dev note edit <id> --editor <fixture-editor>
dev note delete <id> --yes
```

Then run `gofmt`, `go vet`, `go test -race ./...`, `scripts/e2e.sh`, skill
command-reference sync/check, and skill-author lint. Commit only this feature;
exclude SpecStory artifacts and unrelated concurrent catalog work already
committed by its owning agent.
