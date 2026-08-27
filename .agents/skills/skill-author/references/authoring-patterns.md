# Authoring patterns

Condensed from <https://agentskills.io/skill-creation/best-practices>. Read this
before writing or editing a SKILL.md body.

## Table of contents

1. [Spending context wisely](#spending-context-wisely)
2. [Cross-agent frontmatter compatibility](#cross-agent-frontmatter-compatibility)
3. [Calibrating control](#calibrating-control)
4. [Gotchas sections](#gotchas-sections)
5. [Output templates](#output-templates)
6. [Checklists for multi-step workflows](#checklists-for-multi-step-workflows)
7. [Validation loops](#validation-loops)
8. [Plan-validate-execute](#plan-validate-execute)
9. [Bundling reusable scripts](#bundling-reusable-scripts)
10. [Quick checklist before commit](#quick-checklist-before-commit)

---

## Spending context wisely

Every token in `SKILL.md` competes with the conversation history, the system
prompt, and every other active skill. Spend tokens on what the agent **wouldn't
know without you**:

- Project-specific conventions (table names, repo layout, internal jargon)
- Domain-specific procedures with non-obvious steps
- Edge cases the agent will misjudge by default
- Specific tools/APIs to prefer (with the *why*, briefly)

Don't spend tokens on:

- Explaining what well-known things are (PDF, HTTP, REST, JSON)
- Generic "best practices" the agent already applies (handle errors, write tests)
- Comprehensive option enumerations — give a default and one escape hatch

**Test:** for each paragraph, ask "would the agent get this wrong without it?"
If no, cut it.

### Progressive disclosure

Three loading levels:

| Level | Loaded | Budget |
|---|---|---|
| Frontmatter (`name`, `description`) | always | 120-500 chars preferred, 1024 max |
| `SKILL.md` body | when skill triggers | <500 lines, <5000 tokens |
| `references/*.md`, `assets/*`, `scripts/*` | on demand | unlimited |

If `SKILL.md` is creeping past 500 lines, move sections to `references/<topic>.md`
and reference them with **load conditions** ("Read X if Y happens"), not just
"see X for details". Conditions let the agent load on demand; bare pointers tend
to either get ignored or get pre-loaded out of caution.

---

## Cross-agent frontmatter compatibility

Use this as the portable baseline for coding agents that consume Agent Skills:

- `name`: required, lowercase letters/digits/hyphens only, no leading/trailing
  hyphen, no `--`, <=64 characters.
- `description`: required, non-empty string, <=1024 characters. Include both
  what the skill does and concrete trigger contexts. Keep the first 60
  characters meaningful because `npx skills` truncates picker hints there.
- **Quoting**: a YAML plain (unquoted) scalar may not contain `": "`, and ` #`
  starts a comment. Long English descriptions hit both constantly
  (`"…debug a FastAPI app: choosing def vs async def"`). Wrap such values in
  single quotes — the whole skill is otherwise skipped at install time with
  "Nested mappings are not allowed in compact mappings", or installs with a
  silently truncated description. `scripts/lint-frontmatter.sh` gates this.
- Keep `name` and `description` as the only required frontmatter fields for
  portability. Agent-specific fields are allowed when needed, but isolate them
  intentionally: Cursor uses `disable-model-invocation`, Codex can read
  `agents/openai.yaml`, and some vendored skills carry upstream-only metadata.

Description budget tiers:

| Tier | Length | Meaning |
|---|---:|---|
| Green | 120-500 chars | Preferred for local skills: enough trigger surface without context bloat |
| Yellow | 501-900 chars | Valid, but context-heavy; move details to `SKILL.md` body |
| Orange | 901-1024 chars | Valid, but close to hard loader limits |
| Red | >1024 chars | Invalid for Codex/Cursor/spec-aligned validators |

Agent notes:

- **Codex** skips invalid `SKILL.md` files; OpenAI's validator enforces the
  64-char name and 1024-char description limits.
- **Cursor** managed `create-skill` guidance matches the same 64/1024 limits
  and supports `disable-model-invocation` for explicit-only skills.
- **Claude Code** uses descriptions for discovery and tolerates additional
  frontmatter in practice, but long descriptions still consume always-on
  context and are poor picker hints.
- **OpenCode** follows the Agent Skills convention and uses descriptions for
  invocation; stay within the portable baseline unless a target agent's docs
  say otherwise.
- **Claude.ai upload** may impose stricter UI limits. Treat that as a separate
  packaging target, not the default coding-agent baseline for this repo.

---

## Calibrating control

Match instruction specificity to **task fragility**, not to "we should be thorough".

### Be prescriptive when:

- The operation is destructive or has side effects
- A specific sequence of steps must hold (migration, release, deploy)
- An external system requires exact format (API contract, parser input)

```markdown
## Database migration

Run exactly this sequence:

    python scripts/migrate.py --verify --backup

Do not modify the command or add flags. The `--verify` step seeds an
integrity check that later phases depend on.
```

The trailing sentence is the trick — even when prescriptive, **explain why**.
Agents that understand the reason follow it more reliably than agents that just
see "DO NOT" in caps.

### Give the agent freedom when:

- Multiple valid approaches exist
- The output is judgment-driven (code review, refactor proposal)
- You'd be hand-coding policy if you wrote it all out

```markdown
## Code review process

1. Check database queries for SQL injection (use parameterized queries)
2. Verify auth checks on every endpoint
3. Look for race conditions in concurrent code paths
4. Confirm error messages don't leak internals
```

Notice: *what to look for*, not *how to look for it*. The agent picks the
approach.

### Defaults, not menus

When several tools could work, pick one default and mention alternatives as
escape hatches.

```markdown
Use pdfplumber for text extraction.

For scanned PDFs requiring OCR, fall back to pdf2image with pytesseract.
```

…not:

```markdown
You can use pypdf, pdfplumber, PyMuPDF, or pdf2image depending on...
```

### Procedures over declarations

Teach the agent *how* to approach the class of problem, not *what* to produce
for one instance.

```markdown
Bad — only useful for this exact query:
  Join `orders` to `customers` on `customer_id`, filter region='EMEA',
  sum `amount`.

Good — works for any query:
  1. Read schema from `references/schema.yaml` to find relevant tables.
  2. Join via the `_id` foreign key convention.
  3. Apply user-requested filters as WHERE clauses.
  4. Aggregate numeric columns and format as a markdown table.
```

Specifics still belong in the skill (output format templates, "never output
PII", tool-specific quirks) — but the *approach* should generalize.

---

## Gotchas sections

Often the single highest-value section in a skill. **Concrete corrections to
mistakes the agent will make without being told.** Not generic advice.

```markdown
## Gotchas

- The `users` table uses soft deletes. Queries must include
  `WHERE deleted_at IS NULL` or results will include deactivated accounts.
- The user ID is `user_id` in the database, `uid` in the auth service, and
  `accountId` in the billing API. All three refer to the same value.
- The `/health` endpoint returns 200 if the web server is up, even if the
  database is down. Use `/ready` to check full service health.
```

Keep gotchas in `SKILL.md`, not in a separate reference. The whole point is
that the agent reads them **before** hitting the situation — by the time it
realizes it should load `references/gotchas.md`, the bug has happened.

**Iteration tip:** every time you correct the agent during a real run, add the
correction here. Most skills' gotchas sections grow over weeks, not get written
all at once.

---

## Output templates

Agents pattern-match against concrete structures more reliably than they
follow prose descriptions. When the output has a fixed shape, paste the shape:

````markdown
## Report structure

Use this template, adapting sections as needed:

```markdown
# [Analysis Title]

## Executive summary
[One paragraph]

## Key findings
- Finding 1 with supporting data
- Finding 2 with supporting data

## Recommendations
1. Specific actionable recommendation
2. Specific actionable recommendation
```
````

Long templates or templates that only apply in some cases should live in
`assets/<name>.template` and be loaded on demand from `SKILL.md`.

---

## Checklists for multi-step workflows

Make dependencies and validation gates visible:

```markdown
## Form processing workflow

Progress:
- [ ] 1. Analyze the form  → `scripts/analyze_form.py`
- [ ] 2. Create field mapping  → edit `fields.json`
- [ ] 3. Validate mapping  → `scripts/validate_fields.py`
- [ ] 4. Fill the form  → `scripts/fill_form.py`
- [ ] 5. Verify output  → `scripts/verify_output.py`
```

The agent treats the checkboxes as state and won't skip to step 4 without
ticking 1-3. Dependency arrows make the wiring explicit.

---

## Validation loops

For tasks where output quality is testable, instruct the agent to validate
and re-try, not "do your best and stop":

```markdown
## Editing workflow

1. Make your edits.
2. Run validation: `python scripts/validate.py output/`
3. If validation fails:
   - Read the error message.
   - Fix the issues.
   - Run validation again.
4. Only proceed when validation passes.
```

The validator can be a script, a reference checklist, or a self-check prompt.
The pattern is identical: **do → validate → fix → re-validate → proceed**.

---

## Plan-validate-execute

For batch or destructive operations, force the agent through an intermediate
**plan artifact** that gets validated before any side effects happen:

```markdown
## PDF form filling

1. Extract form fields:
   `python scripts/analyze_form.py input.pdf`  → `form_fields.json`
2. Create `field_values.json` mapping each field name to its intended value.
3. Validate:
   `python scripts/validate_fields.py form_fields.json field_values.json`
   (checks every field exists, types match, required fields present)
4. If validation fails, revise `field_values.json` and re-validate.
5. Fill the form:
   `python scripts/fill_form.py input.pdf field_values.json output.pdf`
```

The magic is step 3. A validator that catches "field 'signature_date' not
found — available: customer_name, order_total, signature_date_signed" lets the
agent self-correct **before** the destructive step 5.

This pattern eliminates an entire class of "oops, ran the wrong thing on prod"
failures. Use it any time the actual operation is hard to undo.

---

## Bundling reusable scripts

When you iterate on a skill (with `skill-creator`), watch the agent transcripts
across test runs. If the agent independently reinvents the same logic each run
— building the same chart, parsing the same format, validating the same way —
that's a signal to write the script once and bundle it in `scripts/`.

Heuristic: if you see the same ~20-line helper appearing in 2+ runs, bundle it.

See `references/script-design.md` for **how** to design bundled scripts so an
agent can call them reliably.

---

## Quick checklist before commit

Before considering a SKILL.md "done":

- [ ] Description includes specific trigger contexts ("use when X, Y, Z"),
      not just a one-line summary
- [ ] Description is "pushy" but within budget: 120-500 chars preferred,
      <=1024 chars required
- [ ] First 60 description chars are useful in picker UIs
- [ ] Name is hyphen-case and <=64 chars
- [ ] SKILL.md is under 500 lines (move overflow into `references/`)
- [ ] Every `references/*.md` is mentioned with a load condition, not just
      listed at the bottom
- [ ] Has a `## Gotchas` section (or an honest reason it's empty)
- [ ] Output format, if structured, is shown as a literal template
- [ ] Multi-step procedures use checklists with dependencies marked
- [ ] Destructive operations go through plan-validate-execute or have
      `--dry-run` support
- [ ] Bundled scripts are listed under `## Available scripts` with their flags
- [ ] No "MUST" / "ALWAYS" / "NEVER" in caps without an accompanying *why*
