---
description: Organize future dev-cli, Git, harness, experiment, incident, and source-review notes without destabilizing the main guides.
authority: project-policy
status: maintained
verified_on: 2026-08-28
---

# Notes index

Notes are evidence-bearing working knowledge that may later graduate into a guide or reference page. They are not a dumping ground for unverified instructions.

## Taxonomy

| Note type | Captures | Graduation target |
|---|---|---|
| `harness` | behavior/version research for Claude Code or another coding agent | provider guide or compatibility matrix |
| `experiment` | hypothesis, setup, observations, result, cleanup | best practice or discarded finding |
| `decision` | context, options, chosen rule, consequences | concepts/project policy |
| `incident` | symptom, evidence, recovery, prevention | troubleshooting/compatibility |
| `source-review` | what an external page actually supports and its date/status | sources matrix |
| `recipe` | a repeatable sequence with safety and verification | user guide or skill |

Create future notes under `docs/notes/` with descriptive kebab-case names. Introduce a subdirectory only after several notes share a stable category; avoid deep taxonomies based on one item.

## Lifecycle

```text
draft → verified → maintained
   └──────► superseded / historical
```

- **draft:** incomplete investigation; do not place operational commands on the happy path.
- **verified:** evidence and reproduction/authority are present.
- **maintained:** an active reference with an owner/freshness expectation.
- **superseded:** retained only when the replacement link or historical reasoning matters.
- **historical:** intentionally describes an old release or practice.

## Index entries

When a note is added, list it here with type, status, verification date, and one-sentence value. Once it graduates into a guide, replace the entry with a link to the canonical page rather than keeping two sources of truth.

| Note | Type | Status | Verified | Purpose |
|---|---|---|---|---|
| [Authoring notes](authoring.md) | recipe | maintained | 2026-08-28 | page template, bilingual/source rules, and validation loop |

## Rules

- Separate observation from recommendation.
- Record exact product/version/date for fast-moving harness behavior.
- Link primary sources and code/tests, not only summaries.
- Include reproduction and verification commands where possible.
- State external side effects and cleanup.
- Never publish secrets, private absolute paths, credentials, or copied private logs.
- Use [Sources and freshness](../reference/sources-freshness.md) when a note promotes a claim into the main site.

## Add a page

Use the project helper after adding the `Notes` nav section:

```bash
bash .claude/skills/mkdocs-site-bootstrap/scripts/add-docs-page.sh \
  --section Notes \
  --title "Topic title" \
  --slug topic-title
```

The configured languages create an English page and zh-TW stub. Complete both before merging.

## Related pages

- [Authoring notes](authoring.md)
- [Sources and freshness](../reference/sources-freshness.md)
- [Compatibility and known limitations](../reference/compatibility.md)
