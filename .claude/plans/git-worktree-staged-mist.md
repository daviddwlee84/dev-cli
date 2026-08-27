# Context

`dev-cli` currently has no MkDocs configuration or published GitHub Pages site, while its usage model spans several layers that are easy to conflate: Git branches/worktrees, the `dev` task registry, Herdr/tmux runtimes, and coding-agent orchestration. The requested site should explain both the tool and the surrounding Git/Claude Code knowledge, make safe parallel work easy to follow, and remain structured enough to absorb many future notes.

The user confirmed:

- MkDocs Material should be added to this repository.
- The intended target is `daviddwlee84/dev-cli`, with automatic deployment from `main`.
- English is the default language and every first-release page also has a complete `zh-TW` sibling.
- The first release is Claude-first while defining a reusable shape for other coding-agent harnesses later.

The repository currently has a heavily modified working tree, an empty `docs/`, no `mkdocs.yml`, and no Git remote. `gh` cannot currently find `daviddwlee84/dev-cli`. The documentation implementation therefore stops after local/CI-ready verification; publishing the repository and enabling Pages remain explicit outward-facing consent gates.

## Recommended implementation

### 1. Re-check the workspace and persist the approved preferences

- Immediately before writing, re-run `git status --short`, verify that `docs/` is still empty and that `mkdocs.yml`, `pyproject.toml`, and `.github/workflows/docs.yml` still do not exist. If another session created any target, stop rather than overwrite it.
- Do not stash, reset, clean, rebase, or use any `--force` scaffold option; do not alter the user's existing dirty files.
- Verify required local tools (`uv` and mikefarah `yq` v4) before starting.
- Use `.claude/skills/mkdocs-site-bootstrap/scripts/check-preferences.sh` to create `.skills/preferences.yaml` with the approved decisions: `enabled=true`, `decided_at=2026-08-27`, `stack=mkdocs-material`, `auto_deploy=true`, `pages_deployed=false`, `existing_docs_decision=none`, `repo_slug=daviddwlee84/dev-cli`, and `site_url=https://daviddwlee84.github.io/dev-cli/`.

### 2. Scaffold the site safely, then add i18n

- Run `init-docs-site.sh` first with `--dry-run`, then with the same real arguments only after checking the preview:
  - `--site-name "dev-cli"`
  - a description covering dev-cli, Git workflows, coding-agent harnesses, and safe parallel work
  - `--repo-slug daviddwlee84/dev-cli`
  - `--repo-name dev-cli`
  - `--site-url https://daviddwlee84.github.io/dev-cli/`
  - `--target-dir /Users/david/Documents/Program/dev-cli`
  - `--existing skip`
- Do not pass `--social`; the first release should avoid Cairo/Pango and social-card cache dependencies.
- Preserve `.github/workflows/ci.yml`; the scaffold only adds `.github/workflows/docs.yml`. Let it append `/site/` to `.gitignore` idempotently.
- Run `uv sync --extra docs` and an initial `uv run mkdocs build --strict` while the scaffold is still English-only; retain the generated `uv.lock`.
- Add Traditional Chinese with `add-language.sh --lang zh-TW --default-lang en --target-dir ... --drop-strict`, again dry-running first and never using `--force`.
  - Keep `mkdocs-llmstxt` so `/llms.txt` and `/llms-full.txt` remain available.
  - Accept non-strict builds after i18n because `mkdocs-llmstxt` emits known false-positive source-URI warnings with `mkdocs-static-i18n`.
  - Populate `nav_translations` manually for navigation chrome; do not use bilingual labels in the nav.

### 3. Establish a durable information architecture

Use `add-docs-page.sh` for every new page so file creation and nav insertion remain consistent. Dry-run the first representative addition, never pass `--force`, then curate the final nav order in `mkdocs.yml`.

Every canonical page below gets a complete sibling such as `page.zh-TW.md`; no `Translation pending` stubs remain:

```text
Home
├── index.md
├── getting-started.md
└── best-practices.md

Concepts
├── concepts/mental-model.md
└── concepts/isolation-glossary.md

Using dev-cli
├── guides/change-stream-workflow.md
├── guides/worktrees-provisioning.md
├── guides/parallel-agents-runtimes.md
└── guides/tui-repos-bootstrap.md

Git and GitHub
├── git/github-flow.md
├── git/branches-commits-prs.md
└── git/worktree-semantics-recovery.md

Claude Code
├── claude/agentic-loop-tools.md
├── claude/parallel-work-chooser.md
├── claude/subagents-agent-view.md
├── claude/teams-dynamic-workflows.md
├── claude/worktree-isolation.md
└── claude/extensions-agent-sdk.md

Reference
├── reference/commands-config.md
├── reference/compatibility.md
└── reference/sources-freshness.md

Notes
├── notes/index.md
└── notes/authoring.md
```

The two Notes pages define the long-term taxonomy and a copyable note template so future harnesses, experiments, troubleshooting records, and source reviews can be added without changing the top-level architecture.

### 4. Author the English and zh-TW content from explicit sources of truth

- Lead the landing page and `best-practices.md` with a concise, executable path. The central rules are:
  - one branch per independent change stream;
  - choose worktrees by mutation boundary, not agent count;
  - worktree per change stream and pane per cooperating agent;
  - assign a single integration owner and explicit file/dependency ownership;
  - provision fresh worktrees from an allowlist and never assume ignored secrets/dependencies exist;
  - checkpoint/push before handoff, preserve the next action, and keep one writer per branch;
  - test the integrated result, then clean up only when work is recoverable.
- Explain the `dev` model from code/tests: durable Git history plus a small intent/reconstruction registry, versus lossy per-host runtime state; HOT/WARM/COLD/DONE; Herdr → tmux → none fallback; worktree provisioning and conservative cleanup.
- Reuse the generated command source rather than duplicating it: `reference/commands-config.md` should include `internal/skill/dev-cli/references/commands.md` through `pymdownx.snippets`, with a translated introduction/usage guide in its zh-TW sibling.
- Treat current GitHub Docs as the normative GitHub Flow source: branch → commits/push → PR → review → merge → delete. Put `githubflow.github.io` and the 2019 Wayback material in an explicitly historical subsection; do not prescribe either historical deploy/merge ordering.
- Document Claude Code's public behavior and extension contracts, not proprietary implementation details. Keep these primitives distinct:
  - foreground/background subagents;
  - Agent view background sessions (research preview);
  - agent teams (experimental and not automatically worktree-isolated);
  - Dynamic Workflows (repeatable JavaScript orchestration);
  - worktrees as a file-state isolation layer, not a coordination model;
  - `Agent` tool, task-list tools, `/tasks`/`TaskStop`, and the project-specific `dev task` domain.
- State the real isolation boundary: linked worktrees separate working files/index/HEAD but still share repository data and may collide through refs, hooks, ports, databases, caches, generated files, and deployment targets.
- Adapt ideas from `/Users/david/Documents/Program/agent-skills/skills/local/git-workflow/` only after independent rewriting and attribution. Do not copy its stale Claude worktree path, “zero collisions” claim, per-agent-worktree rule, or helper-script promises.
- Every page receives YAML metadata for `description`, `authority`, `status`, `verified_on`, and optional `minimum_version`/`tested_with`. Volatile pages also show a visible freshness admonition and end with Sources and Related pages.
- `reference/sources-freshness.md` holds the claim/source matrix for version-sensitive statements and explains the authority order: repository code/tests → current Git/GitHub/Anthropic docs → dated historical material → explicitly labeled project policy.
- Complete zh-TW bodies in 4–6 page batches, building between batches. Follow the terminology rule: use `中文 (English original)` on first mention, keep non-canonical terms in English, and never translate code, API/tool names, CLI flags, package names, or paths.

### 5. Publish code-verified limitations without modifying product behavior

Use `reference/compatibility.md` to distinguish implemented behavior, project policy, preview/experimental upstream behavior, and known gaps. Include the currently verified caveats where relevant, such as Claude-owned cleanup being version-dependent, `dev done --pr` not marking a task DONE, `sweep` not detecting merged PRs, `--focus` not reaching the runtime, cold tasks requiring `dev resume`, and AgentSession persistence lacking a production assignment path.

Do **not** edit `README.md`, `internal/help/topics/**`, `internal/skill/dev-cli/**`, TUI text, or Go product code in this task. Those files are active dirty work owned by another change stream. The site should avoid repeating stale statements; reconciling existing product prose or fixing behavior is a separate follow-up after the current WIP lands.

### 6. Restore deterministic quality gates despite the non-strict i18n build

Add a small docs-only checker at `scripts/check-docs.py`, run inside the `uv` docs environment, that:

- parses `mkdocs.yml` and page front matter;
- ensures every English page has one zh-TW sibling and no orphan translation;
- rejects `Translation pending`, missing required metadata, missing/duplicate nav entries, local absolute paths, and the known stale worktree wording/path;
- verifies snippet targets exist;
- after the MkDocs build, checks local rendered links/fragments and confirms non-empty `site/llms.txt` and `site/llms-full.txt`.

Keep external URL availability out of the deterministic CI gate; sources are dated and reviewed during authoring so transient external failures do not block deployment.

Adjust the generated `.github/workflows/docs.yml` to:

- build/check on pull requests that touch docs/config/checker inputs;
- build/check/deploy on pushes to `main` and on `workflow_dispatch`;
- run `uv sync --extra docs`, the source checker, `uv run mkdocs build`, and the rendered-site checker;
- upload/deploy the Pages artifact only outside pull-request events;
- include `docs/**`, `mkdocs.yml`, `pyproject.toml`, `uv.lock`, `scripts/check-docs.py`, the generated command reference, and the docs workflow itself in path filters;
- preserve the required `contents: read`, `pages: write`, and `id-token: write` permissions.

### 7. Keep publication behind two explicit gates

After local implementation and verification, stop and present the diff plus the unresolved remote state.

1. **Repository publication gate:** because `daviddwlee84/dev-cli` is not currently visible to `gh`, obtain explicit permission before creating a public repository, adding `origin`, committing, or pushing. Review the whole dirty project/history first; never publish a docs-only commit that describes uncommitted product behavior.
2. **Pages control-plane gate:** only after the workflow exists on GitHub, show the exact dry-run and ask again before running:
   - `gh api -X POST repos/daviddwlee84/dev-cli/pages -f build_type=workflow`
   - `gh workflow run docs.yml --repo daviddwlee84/dev-cli`

Only after a successful first deployment should preferences be updated to `pages_deployed=true` and `pages_enabled_at=<date>`, followed by live checks of the English home page, a zh-TW page, `/llms.txt`, and `/llms-full.txt`.

## Critical files

Files to create or modify:

- `.skills/preferences.yaml`
- `.gitignore` (idempotent `/site/` entry only)
- `mkdocs.yml`
- `pyproject.toml`
- `uv.lock`
- `.github/workflows/docs.yml`
- `scripts/check-docs.py`
- `docs/index.md`, `docs/index.zh-TW.md`, and the paired page families listed above
- scaffolded `docs/assets/copy-to-llm/**` and `docs/_snippets/README.md`

Existing read-only sources to reuse/verify:

- `README.md`
- `internal/help/topics/{agents,worktrees,branching,commits,parking,tui,bootstrap}.md`
- `internal/skill/dev-cli/references/{commands,task-lifecycle,worktree-ownership}.md`
- `internal/task/{task,store}.go`
- `internal/wt/{manager,plan,ecosystem,provision}.go`
- `internal/runtime/{runtime,herdr,tmux,none}.go`
- `internal/cli/{start,park,resume,done,sweep,list,worktree,tui,adopt,bootstrap,repo}.go`
- corresponding Go tests, `.github/workflows/ci.yml`, and `scripts/e2e.sh`

## Verification

Run after each translation batch and once more at the end:

```bash
uv sync --extra docs
uv run python scripts/check-docs.py --source
uv run mkdocs build
uv run python scripts/check-docs.py --site site
```

Then verify the documentation work did not break or drift from the product:

```bash
go test ./...
go vet ./...
go run ./cmd/dev skill sync --check
./scripts/e2e.sh
git diff --check
```

Finally inspect the path-scoped diff, confirm the pre-existing dirty files are untouched, and manually preview the English/zh-TW home, best-practices, parallel-work chooser, GitHub Flow, compatibility, language switcher, copy-to-LLM control, and both LLM text outputs. No commit, push, repository creation, Pages API call, or workflow dispatch occurs during this implementation phase.
