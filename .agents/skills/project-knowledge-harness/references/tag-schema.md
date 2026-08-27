# Tag schema (priority × effort)

Two orthogonal axes prevent the "important but unimplementable" trap:

## Priority

- `P1` — likely next batch (you'd reach for this if you sat down today)
- `P2` — worth doing, no rush
- `P3` — someday / nice-to-have
- `P?` — needs evaluation first; spike before committing to a priority

## Effort

- `S` — under an hour
- `M` — half day
- `L` — multi-day
- `XL` — architectural; design doc required before code

## Useful combinations

- `P?` + `[?/L]` — explicit "unknown of size L"; the most honest tag
- `P3` + `[S]` — "small enough to slip into any free moment"
- `P1` + `[XL]` — warns "you said this is urgent but it's actually huge — re-scope"

## Heading and item syntax (validator-checked)

The bundled `scripts/todo-kanban.sh` enforces this exact form. If you change
the syntax, update the script in the same commit.

- Section headings, in order: `## P1`, `## P2`, `## P3`, `## P?`, `## Done`
- Active items in `P1` / `P2` / `P3`:
  `- [ ] **[Effort] Title** — description`
- Active items in `P?`:
  `- [ ] **[?/Effort] Title** — description`
- Shipped items in `Done`:
  `- ✅ [YYYY-MM-DD] [P#/Effort] Title — one-line shipped summary`
- Optional trailing on active items:
  `→ [research](backlog/<slug>.md)`

Anything that is NOT a top-level `- [ ]` / `- ✅` item — prose paragraphs,
blockquotes, HTML comments, `---` rules, indented sub-bullets — is ignored
by the validator. Use this room for explanatory text without breaking the
machine-readability of the lanes.
