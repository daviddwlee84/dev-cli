# Context

The repository-guidance work was committed separately as `bfefe7a` (`docs: add repository guidance and align references`). The remaining request is a new user-facing help/documentation feature: make the normal dev-cli lifecycle understandable at a glance in the terminal and as a visual closed loop in the bilingual MkDocs site.

Today, `dev --help` explains the architecture but not the operating loop, while bare `dev help` is a topic index. The two CLI surfaces should share one ASCII TL;DR source. The existing MkDocs change-stream guide already owns the full start/park/resume/integrate/review/cleanup procedure, and Mental Model already owns the narrower HOT/WARM/COLD/DONE state graph, so those pages should be enhanced rather than adding another workflow page.

This is a post-`v0.1.0` user-facing feature. Record it under `CHANGELOG.md` `[Unreleased]` for the next feature release (normally `v0.2.0` under the current `0.x` policy); do not create or move a release tag as part of this implementation.

## Implementation

### 1. Add one shared terminal TL;DR

- In `internal/cli/root.go`, define one 7-bit-ASCII workflow constant and append it to `rootLong` so it appears above Cobra's generated command list in `dev --help`.
- Correct the existing root prose from “three things” to “four things.”
- In `internal/cli/help.go`, print the same constant before the topic table when `dev help` has no topic. Topic-specific `dev help <topic>` output remains unchanged.
- Keep the diagram compact and command-oriented:
  - `dev start` enters active work.
  - `dev park --next` / `dev resume` forms the interruption loop.
  - Direct tasks finish with `dev done`.
  - Branch/worktree tasks finish locally with `dev done --ff`.
  - `dev done --pr` enters review/CI while the task remains active; requested changes return to work and remote merge detection/cleanup is explicitly not automatic.
  - DONE entries are reported by `dev sweep` and reaped only by `dev sweep --apply`.
- Do not create another embedded help topic or duplicate the diagram into README/SKILL.

Add focused external-package tests in a new `internal/cli/root_help_test.go`:

- Execute `--help` with a nonexistent config path and prove root help is configuration-independent.
- Execute `help` and prove the same TL;DR appears before the topic index.
- Assert stable command/state anchors, the corrected “four things” wording, and absence of the old phrase; avoid a brittle full-output snapshot.
- Assert the diagram contains only ASCII bytes and does not present `dev done --pr` as a transition to DONE.

### 2. Enable native Material Mermaid rendering

Update the existing `pymdownx.superfences` entry in `mkdocs.yml`:

```yaml
- pymdownx.superfences:
    custom_fences:
      - name: mermaid
        class: mermaid
```

Use Material's existing Mermaid integration. Add no plugin, Python dependency, vendored JavaScript, CSS, or custom asset. Omit the documented `!!python/name:...fence_code_format` tag because `scripts/check-docs.py` deliberately uses `yaml.safe_load`; the pinned PyMdown version already defaults this custom fence to the correct formatter.

### 3. Add the bilingual closed-loop diagrams

- Add the full usage flow immediately after the introduction in:
  - `docs/guides/change-stream-workflow.md`
  - `docs/guides/change-stream-workflow.zh-TW.md`
- Replace the existing ASCII lifecycle graph with a narrower Mermaid state graph in:
  - `docs/concepts/mental-model.md`
  - `docs/concepts/mental-model.zh-TW.md`
- Preserve equivalent graph structure in both locales. Translate explanatory node/edge labels plus `accTitle`/`accDescr`, but keep command names, flags, modes, and HOT/WARM/COLD/DONE tokens unchanged.
- Use labels/shapes as well as visual styling; color must not be the only meaning carrier.

The full guide diagram must show:

1. `dev start` selecting direct, branch-only, or worktree mode, then active HOT work.
2. HOT ↔ WARM through park/resume.
3. COLD only for eligible branch/worktree tasks through `dev park --cold --push`, returning through `dev resume --fetch`.
4. Direct `dev done` and branch/worktree `dev done --ff` reaching DONE.
5. `dev done --pr` entering an active review loop; requested changes return to work. An external merge leads to an explicit “verify integration and finish the local lifecycle” step, not directly to DONE or sweep, because remote merge completion is not currently detected.
6. DONE → report-only `dev sweep` → explicit `dev sweep --apply` reap → next change stream.

The Mental Model diagram remains state-focused and does not repeat checkout-mode decision detail.

### 4. Make rendered diagrams testable

Extend the existing `LinkParser` in `scripts/check-docs.py` to count `<pre>` elements whose class list includes `mermaid`. In rendered-site validation, require at least one Mermaid block in both locale variants of the canonical workflow and mental-model pages.

This focused assertion proves that the safe-load-compatible MkDocs configuration actually turns fences into Mermaid containers without introducing a general Mermaid parser. `mkdocs build --strict` plus the browser-side Material integration remains responsible for rendering.

### 5. Changelog, generated docs, and commit boundary

- Add one `[Unreleased] / Added` entry in `CHANGELOG.md` covering shared CLI workflow orientation and bilingual Mermaid diagrams.
- Regenerate `docs/llms-full.txt` with the repository generator; include `docs/llms.txt` only if the generator changes it.
- Review existing README, embedded parking help, skill lifecycle reference, Best Practices, and GitHub Flow mapping for semantic contradictions, but do not duplicate the new diagrams when their current concise text remains accurate.
- Keep all active `.specstory/**` and `.claude/plans/**` paths untouched. After validation, stage/scan/commit only exact feature paths as a separate commit (for example, `feat: add workflow diagrams to help and docs`), using the same temporary-index redacted scan / `git commit --only` approach that preserved the existing SpecStory index for `bfefe7a`.

## Critical files

- CLI: `internal/cli/root.go`, `internal/cli/help.go`, new `internal/cli/root_help_test.go`
- MkDocs plumbing: `mkdocs.yml`, `scripts/check-docs.py`
- Canonical bilingual content: `docs/guides/change-stream-workflow*.md`, `docs/concepts/mental-model*.md`
- Release/generated output: `CHANGELOG.md`, `docs/llms-full.txt`

## Verification

1. Run the focused root-help tests, then manually inspect both `./dev --help` and `./dev help` to ensure the shared ASCII flow is readable and precedes normal content.
2. Run `gofmt` on the new Go test and `go test -race ./...`.
3. Run `make skill-check`; no generated command-reference change is expected because only root long/help body text changes.
4. Regenerate and validate documentation:
   - `uv run python scripts/check-docs.py --source --generate-llms`
   - `uv run python scripts/check-docs.py --source`
   - `uv run mkdocs build --strict`
   - `uv run python scripts/check-docs.py --site site`
5. Confirm both English and zh-TW workflow/mental-model HTML pages contain the tested Mermaid container and all generated per-route Markdown/LLM output is current.
6. Run an independent read-only QA pass against lifecycle implementation (`start`, `park`, `resume`, `done`, `sweep`) and the two diagrams; close all reviewer runtimes afterward.
7. Inspect path-only Git status/diff, run the redacted scan against a temporary index containing only this feature's files, and commit only those paths without changing the unrelated SpecStory index.
