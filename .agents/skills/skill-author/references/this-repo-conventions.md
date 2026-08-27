# This-repo conventions

Conventions specific to the `daviddwlee84/agent-skills` repository. Skills
authored elsewhere may follow different rules; this file is the source of
truth for **this** repo.

## Layout

```
skills/
├── local/             # skills authored in this repo (committed in full)
│   └── <skill-name>/
│       ├── SKILL.md
│       ├── references/
│       ├── scripts/
│       └── assets/
└── vendor/            # skills cherry-picked from upstream repos
    └── <skill-name>/
        └── ...        # synced via scripts/sync-vendor.sh
```

- **New skills authored here go in `skills/local/`.** Always.
- **Don't manually edit anything in `skills/vendor/`** — it gets overwritten by
  the next `make sync`. Upstream changes go through `vendor.yaml` + `make sync`.
- **`.agents/skills/` and `.claude/skills/` at the repo root are active
  discovery-symlink farms**, not legacy. Each entry under them is a relative
  symlink (`../../skills/local/<name>` or `../../skills/vendor/<name>`) so
  that Cursor / Codex / Warp / OpenCode (`.agents/skills/`) and Claude Code
  (`.claude/skills/`) can dogfood the skills authored in this repo. The
  `new-skill.sh` scaffolder creates these symlinks automatically in **LOCAL
  mode**; never add new *canonical* skill content directly under them here.

## `new-skill.sh` placement scopes

`scripts/new-skill.sh` supports three placement scopes that mirror the
behavior of `npx skills add` (`vercel-labs/skills`). Auto-detection
precedence (when no flag given):

1. **LOCAL** — publishing-repo anchor (`vendor.yaml`, `skills/local/`, or
   `skills/.claude-plugin/marketplace.json`) found walking up. This is the
   default *inside this very repo*. Canonical content lives in
   `<repo>/skills/local/<name>/` (or `skills/vendor/<name>/` with
   `--vendor`); the script adds the `.agents/skills/` + `.claude/skills/`
   discovery symlinks for you.
2. **PROJECT** — a git root found walking up. Canonical content goes to
   `<repo>/.agents/skills/<name>/` (universal agents like Cursor / Codex /
   OpenCode / Warp / Gemini-CLI / Copilot read this directly); a relative
   symlink `<repo>/.claude/skills/<name> -> ../../.agents/skills/<name>` is
   added for Claude Code, plus links for any other non-universal agent dir
   (e.g. `.windsurf/`, `.continue/`) that already exists at the repo root.
3. **GLOBAL** — neither anchor found, or `--global` forced. Same shape as
   PROJECT but rooted at `$HOME`: canonical at `~/.agents/skills/<name>/`,
   plus `~/.claude/skills/<name>` and any other already-present
   `~/.<agent>/skills/` link.

The fan-out rule is "claude-code always, others only if their config root
already exists at the base dir" — this mirrors `npx skills add`'s
"don't create `.windsurf/` unless the project already uses Windsurf"
behavior. The script never creates a new top-level agent home dir.

Symlinks are always *relative* with a fixed `../../` prefix because both
the link's parent (`.../skills/`) and the canonical dir
(`.../.agents/skills/` or `.../skills/local/`) are exactly two levels under
the base dir. See [`pitfalls/symlink-target-relative-to-symlink-not-cwd.md`](../../../../pitfalls/symlink-target-relative-to-symlink-not-cwd.md)
for the historical bug this convention prevents.

## Skill discovery

The `skills` CLI looks for `SKILL.md` files like this:

1. **First pass:** one level deep — `skills/<name>/SKILL.md`.
2. **Fallback:** recursive up to **5 levels** if first pass found nothing
   useful.

That's why `skills/local/<name>/SKILL.md` and `skills/vendor/<name>/SKILL.md`
both work — they're 2 levels deep, well within the recursive fallback. **Don't
nest a skill more than 5 levels deep** or it becomes invisible.

## Scripts naming and mirroring

Some scripts are bundled into a skill **and** mirrored to `scripts/` at repo
root. The pattern:

| Canonical location | Mirror |
|---|---|
| `skills/local/project-knowledge-harness/scripts/add-todo.sh` | `scripts/add-todo.sh` |
| `skills/local/project-knowledge-harness/scripts/sweep-inbox.sh` | `scripts/sweep-inbox.sh` |
| `skills/local/project-knowledge-harness/scripts/promote-todo.sh` | `scripts/promote-todo.sh` |
| `skills/local/project-knowledge-harness/scripts/todo-kanban.sh` | `scripts/todo-kanban.sh` |

Reason: the canonical copy travels with the skill (downstream consumers get it
via `init.sh`). The root-level mirror is for *this* repo's own use so `make`
targets and CI don't have to know the skill's path.

**Mirrors must stay byte-identical.** If you edit one, run the equivalent
`cp` to update the other and verify with `diff`. There is currently no
automated check for this — be careful.

## Documentation

- `README.md` — short repo entrypoint with install snippet.
- `docs/` — built into a MkDocs Material site at
  `https://daviddwlee84.github.io/agent-skills/`.
- `docs/skills/<skill-name>.md` — user-facing docs for each local skill. Link
  back to the canonical SKILL.md on GitHub via absolute URL (see linking rules
  in `docs/reference/docs-stack-recipe.md`).

When you add a new local skill, also add:

1. A row to `docs/skills/index.md`.
2. A `docs/skills/<skill-name>.md` page (can be short; cross-link to SKILL.md).
3. A nav entry in `mkdocs.yml`.
4. A row to the README's "What's in here" table.

## Project memory

This repo uses the `project-knowledge-harness` skill on itself. That means:

- `TODO.md` is structured (P1/P2/P3/P?/Done sections, validated by
  `scripts/todo-kanban.sh --validate-only`).
- `backlog/inbox.md` is the loose-capture inbox.
- `backlog/<slug>.md` holds research notes for non-trivial items.
- `pitfalls/<slug>.md` holds debugging traps with symptom-first titles.

When authoring a skill, if you discover a non-obvious trap, write it to
`pitfalls/`. Don't bury it in commit messages or chat history.

## AGENTS.md ↔ CLAUDE.md

`AGENTS.md` is a **symlink** to `CLAUDE.md`. Edit either one. Don't replace
the symlink with a real file or they'll drift.

## Bash compatibility

All bundled scripts must be **bash 3.2 compatible** (stock macOS). See
`script-design.md` § "Bash 3.2 compatibility" for the safe subset and
escape hatches.

## When this repo's conventions conflict with agentskills.io

The agentskills.io best practices are upstream / generic; this file is local /
specific. When they conflict, this file wins **for skills authored in this
repo**. Notable specifics:

- We default to bundling helper scripts in `scripts/` rather than relying on
  ad-hoc `uvx` invocations, because we want the helpers to compose with the
  repo's `Makefile` and CI.
- We use Bash for orchestration scripts (TODO management, vendor sync) so they
  run without a Python environment. PEP 723 Python is fine for analytical or
  parsing-heavy helpers.
- Skill descriptions in this repo lean **even pushier** than the agentskills.io
  guidance because we're optimizing for the OpenCode/Claude Code default
  triggering behavior, which under-triggers more aggressively than Claude.ai.
