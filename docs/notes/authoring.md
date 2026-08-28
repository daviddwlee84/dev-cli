---
description: Use a repeatable metadata, source, bilingual-writing, and verification template for every new documentation note.
authority: project-policy
status: maintained
verified_on: 2026-08-28
---

# Authoring notes

A useful note answers what is true, why the evidence supports it, when it was checked, how to act safely, and how to verify or clean up.

## Page template

```markdown
---
description: One sentence that stands alone in search and llms.txt.
authority: project | git-scm | github-docs | anthropic-docs | project-policy
status: draft | verified | maintained | evolving | historical
verified_on: YYYY-MM-DD
minimum_version: optional
tested_with: optional
---

# Title

One-sentence answer.

!!! info "Freshness"
    Authority, status, verification date, and version boundary.

## Best-practice rule
## When to use it
## Decision table or mental model
## Minimal workflow
## Safety boundaries and known limitations
## Verification and cleanup
## Sources
## Related pages
```

Do not create empty sections merely to satisfy the template. Omit a section when it adds no decision value.

## Source discipline

- Product behavior: link code and tests; run the behavior when practical.
- Git: cite the current installed-version manual or `git-scm.com`.
- GitHub collaboration: cite current GitHub Docs.
- Claude Code: cite current Anthropic docs and record preview/experimental/version state.
- Standards: name the exact specification version.
- Historical pages: include the snapshot/date and a non-normative warning.
- Project recommendation: label it `project-policy`; never imply upstream guarantees it.

Avoid copying external prose or the local `git-workflow` skill. Independently explain verified claims and link the primary source.

## Bilingual workflow

1. Finish a focused English page with sources and metadata.
2. Translate its full meaning into the `*.zh-TW.md` sibling.
3. When using an established Chinese translation, introduce it as `中文 (English original)`; keep product names and Git/CLI/agent domain terms in English when translation would reduce precision.
4. Never invent a translation or translate code, tool/API names, CLI flags, package names, or paths.
5. Use canonical `.md` links in both languages; the i18n plugin resolves language context.
6. Update both pages and `verified_on` in the same change.
7. Never merge a `Translation pending` placeholder.

## Add and validate a page

```bash
bash .claude/skills/mkdocs-site-bootstrap/scripts/add-docs-page.sh \
  --section Notes \
  --title "Topic title" \
  --slug topic-title

uv run python scripts/check-docs.py --source --generate-llms
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

The source check enforces metadata, nav membership, bilingual parity, snippet targets, and generated LLM indexes. The rendered check verifies local links/anchors, language targets, and non-empty LLM outputs.

## Writing guidance

- Lead with the decision or rule, not chronology.
- Use tables for choices; use numbered steps for operations.
- State exactly what a tool isolates and what it shares.
- Put destructive commands after inspection/dry-run/recovery steps.
- Distinguish “not observed,” “unsupported,” and “failed.”
- Keep version numbers near the claim they constrain.
- Prefer a small reproducible example over many speculative edge cases.
- Link to one canonical explanation instead of duplicating it across pages.

## Review checklist

- [ ] Description stands alone.
- [ ] Authority/status/date/version are accurate.
- [ ] Current behavior was checked against code/test or primary docs.
- [ ] English and zh-TW pages convey the same rules and caveats.
- [ ] No private path, secret, or unlicensed copied prose appears.
- [ ] Commands state prerequisites, side effects, and cleanup.
- [ ] Internal links and fragments resolve after build.
- [ ] `llms.txt` describes the page.
- [ ] A superseded page points to its replacement.

## Related pages

- [Notes index](index.md)
- [Sources and freshness](../reference/sources-freshness.md)
- [Best practices](../best-practices.md)
