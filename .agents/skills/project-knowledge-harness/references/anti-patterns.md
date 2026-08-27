# Anti-patterns to avoid

When applying or maintaining this harness, avoid the following:

- **Spawning new files** like `IDEAS.md`, `ROADMAP.md`, `WISHLIST.md`,
  `FUTURE.md`, `BACKLOG.md`, `LESSONS.md`, `TROUBLESHOOTING.md`,
  `GOTCHAS.md` alongside `TODO.md` / `backlog/` / `pitfalls/`. Three
  surfaces, always.

- **Backlog or pitfall docs in `docs/`**. `docs/` is user-facing reference;
  these folders are maintainer-facing memory. Mixing them confuses readers.

- **Pitfall docs titled by root cause** (e.g.
  `tmux-update-environment.md`) instead of symptom (e.g.
  `tmux-pane-loses-ssh-connection-var.md`). You'll search by what you're
  seeing, not by what you eventually learned.

- **Paraphrasing error messages** in pitfall docs. Copy-paste the full
  error including stack/codes — paraphrasing throws away the searchable
  bits.

- **Backlog or pitfall docs without dates**. A 6-month-old "we decided X"
  without a date loses meaning — re-validate or treat as stale.

- **Bulk-migrating scattered historical pitfalls** into `pitfalls/` on day
  one. High risk of broken cross-links + lost context. Index them in the
  README's cross-reference table; physically migrate only when natural.

- **Auto-redacting `backlog/` or `pitfalls/`** the way agent scratchpads
  are redacted. These are first-class docs you write deliberately; treat
  them like any other doc for secret review.

- **TODO heading drift**. Keep the exact section order (`P1`, `P2`, `P3`,
  `P?`, `Done`) and item forms that `scripts/todo-kanban.sh` validates.
  If you need to evolve the format, update the validator and the
  documented format in the same change.

- **Letting `## Done` grow unbounded**. Prune into a `CHANGELOG.md` (or
  similar) once prior-year items appear or the section grows past ~20
  entries, keeping recent entries for context.

- **Overwriting an existing `TODO.md` with the template**. The bundled
  `scripts/init.sh` is conservative on purpose — pass `--force` only after
  preserving any real entries you want to keep.
