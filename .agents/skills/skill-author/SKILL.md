---
name: skill-author
description: Author a new agent skill or refactor an existing one to follow agentskills.io best practices — gotchas sections, output templates, validation loops, calibrated specificity (fragility-based), and agentic script design (--help, --dry-run, structured stdout, stderr diagnostics, PEP 723 inline deps, pinned uvx/npx versions). Use whenever the user wants to create a new skill from scratch, scaffold a SKILL.md, write a reference file, design a script meant to be invoked by an agent, lint a draft skill for quality, or convert an ad-hoc workflow into a reusable skill. For evaluating skill output quality with test cases, benchmarking, or optimizing the description trigger rate, defer to the `skill-creator` skill instead — this skill focuses on authoring, not evaluation.
---

# Skill Author

Helps you write **new agent skills** that follow the best-practices documented at
https://agentskills.io/skill-creation/ — without you having to re-read those pages
every time. Pairs with `skill-creator` (which handles eval/iterate/optimize loops
once a draft exists).

## When to use this vs `skill-creator`

| Task | Use |
|---|---|
| "I want to make a new skill for X" | **skill-author** (scaffold + author) |
| "Scaffold the SKILL.md / a reference / a script" | **skill-author** |
| "Lint this skill draft" | **skill-author** |
| "My skill isn't triggering when I want it to" | `skill-creator` (description optimization) |
| "Run test cases against my skill, benchmark it" | `skill-creator` (eval loop) |
| "Compare two versions of my skill" | `skill-creator` (blind comparison) |

If both apply, start here, then hand off to `skill-creator` once the draft is solid.

## Workflow

### 1. Capture intent (1 minute)

Before scaffolding, get four answers — usually just by asking the user:

1. **What does it do?** (one sentence)
2. **When should it trigger?** (specific user phrases / contexts)
3. **What does it output?** (file, command result, plan, prose)
4. **Is the output objectively verifiable?** (decides whether `skill-creator` evals are worth the effort later)

Write these four down before touching files. They become the seeds for the
description, the workflow body, and the future eval set.

### 2. Scaffold

Use `scripts/new-skill.sh` to produce the directory layout. The script
auto-detects **scope** (where to put the skill) and fans out discovery
symlinks for non-universal agents so the skill is loadable immediately.

```bash
bash skills/local/skill-author/scripts/new-skill.sh <skill-name>
```

Three scopes (auto-detected; override with `--local` / `--project` / `--global`):

| Scope | Canonical dir | Symlinks created |
|---|---|---|
| **LOCAL** (this publishing repo: vendor.yaml / skills/local/ found walking up) | `<repo>/skills/local/<name>/` (or `skills/vendor/<name>/` with `--vendor`) | `.agents/skills/<name>` + `.claude/skills/<name>` -> `../../skills/{local,vendor}/<name>` |
| **PROJECT** (inside another git repo) | `<repo>/.agents/skills/<name>/` (universal agents — Cursor / Codex / OpenCode / Warp — pick this up directly) | `.claude/skills/<name>` -> `../../.agents/skills/<name>` (+ any already-present non-universal agent dir at the repo root) |
| **GLOBAL** (no git repo found) | `~/.agents/skills/<name>/` | `~/.claude/skills/<name>` -> `../../.agents/skills/<name>` (+ any detected non-universal agent dir under `$HOME`) |

The template's frontmatter has placeholders that lint will reject — this is on
purpose so the agent can't forget to fill them in.

> **Important**: When the user's intent is ambiguous (they're inside a git repo
> but haven't said whether the skill should live with the project or be
> available everywhere), **ask** "system-wide (`--global`) or just this
> project (`--project`)?" before running the script. The script is
> non-interactive — once you've picked a scope, files land and symlinks fan
> out without further prompting.

After running, the script emits a single JSON object on stdout with
`{skill, mode, canonical, symlinks[], next_steps[]}` so you can chain
follow-up actions deterministically.

### 3. Author the SKILL.md

Read `references/authoring-patterns.md` before writing the body. The patterns
that matter most:

- **Description must be "pushy"** and include concrete trigger contexts. Naive
  descriptions cause undertriggering — the #1 failure mode of new skills. Keep
  this within the cross-agent budget: 120-500 chars is the preferred range,
  501-900 is valid but context-heavy, 901-1024 is valid but close to hard
  loader limits, and >1024 is invalid for Codex/Cursor/spec-aligned skills.
- **Gotchas section** — environment-specific facts that defy reasonable
  assumptions. The single highest-value section in most skills.
- **Output templates** — agents pattern-match against concrete structures more
  reliably than they follow prose descriptions of formats.
- **Calibrated specificity** — be prescriptive only where the task is fragile;
  give the agent freedom (and explain *why*) where multiple approaches are valid.
- **Defaults, not menus** — pick one tool/approach and mention alternatives as
  escape hatches.

Keep `SKILL.md` under 500 lines. If you blow past that, move detail into
`references/<topic>.md` and tell the agent **when** to load it ("Read X if Y
happens"), not just that it exists.

### 4. Design any scripts

Before writing a script, read `references/script-design.md`. Skills script
design is meaningfully different from "regular" CLI script design because the
caller is an LLM reading your stderr to decide what to do next. The non-obvious
requirements:

- No interactive prompts, ever (agents run in non-interactive shells)
- `--help` is the primary interface documentation — keep it concise but complete
- Errors must say *what was expected* and *what to try*, not just *what failed*
- Structured stdout (JSON/CSV/TSV), prose diagnostics to stderr — separate them
- `--dry-run` for any destructive operation
- Pin versions for `uvx` / `npx` / `pipx` invocations

For Python helpers, prefer **PEP 723 inline dependencies** + `uv run` over
`requirements.txt` — the script becomes self-contained and the agent doesn't
have to set up an environment first.

### 5. Lint

Run the linter before committing or handing off to `skill-creator`:

```bash
bash skills/local/skill-author/scripts/lint-skill.sh skills/local/<skill-name>
```

It checks three things:

1. **Frontmatter & length** — `name` and `description` exist; name is
   hyphen-case and <=64 chars; description has a "use when" trigger phrase,
   respects the 1024-char hard limit, and SKILL.md is under 500 lines.
2. **Script hygiene** — every `scripts/*.sh` has a shebang, is executable, and
   responds to `--help` (presence of the flag handler, not necessarily a working
   script).
3. **Reference reachability** — every file in `references/` is mentioned at
   least once from `SKILL.md` (no dead reference docs).

Fix every error. Warnings are advisory but usually worth addressing.

### 6. (Optional) Hand off to skill-creator

Once lint passes and the skill *looks* right, the next questions are
quantitative: does it trigger reliably? does it produce good output? That's
`skill-creator` territory. Tell the user: "skill-author got the structure
right. If you want to validate it works on real prompts, the `skill-creator`
skill runs test cases and benchmarks."

## Available scripts

- **`scripts/new-skill.sh <name>`** — Scaffold the canonical skill dir
  (SKILL.md, references/, scripts/, assets/) and add discovery symlinks for
  non-universal agents. Auto-detects scope (LOCAL / PROJECT / GLOBAL); see
  table in step 2.
  - Scope flags (mutually exclusive): `--local` (force this repo's
    `skills/local/`), `--project` (force `<repo>/.agents/skills/`),
    `--global` (force `~/.agents/skills/`).
  - Other flags: `--vendor` (LOCAL only — `skills/vendor/<name>/` instead of
    `skills/local/`; rare, vendored skills normally come via `vendor.yaml`);
    `--root DIR` (override walk-up); `--no-symlinks` (skip the fan-out);
    `--dry-run`; `--force` (overwrite existing canonical dir + replace
    symlinks).
- **`scripts/lint-skill.sh <skill-dir>`** — Run frontmatter, script hygiene,
  and reference reachability checks. Exit non-zero on any error.
  - Flags: `--strict` (treat warnings as errors); `--quiet` (only print failures).
- **`scripts/lint-frontmatter.sh [PATH...]`** — YAML-parse the frontmatter of
  every `SKILL.md` under PATH (default `.`) with a real parser, and check that
  the root is a mapping with string `name` + `description`. Catches the
  breakage class `lint-skill.sh`'s awk extractor cannot see — an unquoted
  `description:` containing `": "` makes `npx skills` (and Claude Code /
  Cursor) **silently skip the skill**. `lint-skill.sh` calls this for the
  single-skill case; run it directly to sweep a whole collection.
  - Flags: `--parser auto|yq|python3|node` (`node` = the js `yaml` package,
    exactly what `npx skills` uses); `--quiet`.

## Bundled assets

Templates that `new-skill.sh` copies and that you can reference manually:

- `assets/SKILL.md.template` — Frontmatter + workflow skeleton with placeholders.
- `assets/reference.md.template` — Reference doc skeleton (TOC + sections).
- `assets/script-bash.template` — Bash 3.2 compatible script with `set -euo pipefail`,
  `--help` / `--dry-run` flag handler, structured-output friendly logging
  (data → stdout, prose → stderr).
- `assets/script-python.template` — Python script with PEP 723 inline-deps
  block, argparse with help/examples, dry-run support.

## Reference files

- `references/authoring-patterns.md` — The agentskills.io "best practices"
  page condensed: gotchas, templates, validation loops, calibrated specificity,
  defaults-not-menus, procedures-over-declarations, plan-validate-execute.
- `references/script-design.md` — The agentskills.io "using scripts" page
  condensed: agentic CLI design, structured output, error message style,
  PEP 723, uvx/npx/pipx version pinning, dry-run patterns.
- `references/this-repo-conventions.md` — Conventions specific to *this*
  repository: where to put local vs vendor skills, how the 5-level skill
  search works, AGENTS.md ↔ CLAUDE.md sync, where mirrored scripts live.

## Gotchas

- **AGENTS.md is a symlink to CLAUDE.md** — edit either one; never replace the
  symlink with a real file.
- **Skill discovery is one level deep first, then recursive up to 5 levels.**
  `skills/local/<name>/SKILL.md` works because of the recursive fallback. Don't
  nest skills more than 5 deep or they become invisible.
- **`bash 3.2` compatibility** is required for any script that might run on
  stock macOS. No `mapfile`, no `${var,,}` lowercasing, no `[[ -v var ]]`.
  See `references/script-design.md` for the safe subset.
- **`description` is always-on context and has agent-specific hard limits.**
  Put concrete trigger phrases in frontmatter, but keep local skills in the
  120-500 char preferred range when possible. Treat >1024 chars as invalid:
  Codex and Cursor/spec-aligned validators can skip the skill entirely.
- **Quote any `description` containing `:`, `#`, or leading punctuation.** An
  unquoted YAML plain scalar cannot contain `": "` — `npx skills` fails with
  "Nested mappings are not allowed in compact mappings" and drops the skill
  from the picker without a non-zero exit. ` #` is worse: it parses, but YAML
  treats the rest as a comment and silently truncates your trigger string.
  Single quotes need no escaping except `'` itself. Run
  `scripts/lint-frontmatter.sh` to catch both; see
  `pitfalls/skill-description-colon-breaks-yaml-frontmatter.md` in this repo.
- **Don't write a skill that wraps something the agent already does well.**
  If a stock Claude session handles the task in one turn without help, a skill
  adds context-window cost for no gain. Test the no-skill baseline before
  investing in authoring.
- **Scope auto-detect can surprise you.** `new-skill.sh` walks up looking for
  a publishing-repo anchor (`vendor.yaml` / `skills/local/` /
  `skills/.claude-plugin/`) *first*, then a `.git`. If you ran it from inside
  a clone of this repo while meaning to add a skill to your *other* project,
  it'll silently land in `skills/local/`. Pass `--project` or `--global`
  explicitly when intent isn't obvious, and prefer asking the user
  system-wide-vs-project-wide before running.
- **Discovery symlinks must be `../../`-relative**, not repo-root-relative —
  POSIX resolves symlink targets relative to the link's own directory. The
  script enforces this and verifies each link with `test -e <link>/SKILL.md`;
  if you ever hand-create one, see
  [`pitfalls/symlink-target-relative-to-symlink-not-cwd.md`](../../../pitfalls/symlink-target-relative-to-symlink-not-cwd.md).

## When the user is updating an existing skill (not creating new)

Same workflow, but skip step 2 (scaffold). Read the existing SKILL.md first,
identify which patterns from `references/authoring-patterns.md` are missing or
weakly applied, and propose edits one section at a time so the user can sanity
check. After edits, run lint, then offer the `skill-creator` handoff for
quantitative validation.
