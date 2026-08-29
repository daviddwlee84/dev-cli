# Land the finished dev-cli worktrees, backfill their docs, then harden `dev` itself

## Context

Five dev-cli linked worktrees have accumulated finished-but-unlanded work. `main` has moved
8 commits ahead of `origin/main`, so **nothing can ff-merge as-is** — every branch needs a
rebase first. Meanwhile three of the branches shipped zero `docs/` and zero `CHANGELOG.md`
changes, which violates the repo's own rule in `AGENTS.md` ("Every user-visible feature must
be added to `[Unreleased]`"; "A code change is incomplete when the bundled skill or public
documentation still describes old behavior").

The intended outcome: every finished branch is integrated into `main` with complete bilingual
docs and a changelog entry, its worktree/runtime/branch is retired safely, and the gaps that
let this happen are closed in the tool itself.

### Inventory (verified 2026-08-28)

| Branch | Worktree | Ahead/behind `main` | Dirty | Conflict hunks vs `main` | docs/ | CHANGELOG |
|---|---|---|---|---|---|---|
| `feat/attach-session` | `~/Worktrees/dev-cli/feat-attach-session` | 2 / 7 | clean | 1 | ✗ | ✗ |
| `feat/dev-journal` | `~/Worktrees/dev-cli/feat-dev-journal` | 1 / 5 | specstory only | 8 | ✅ 11 files | ✅ |
| `feat/remote-fleet` | `~/.herdr/worktrees/dev-cli/feat-remote-fleet` | 1 / 10 | clean | 16 | ✗ | ✗ |
| `feat/agent-skill-manager` | `~/Worktrees/dev-cli/feat-agent-skill-manager` | **0** / 7 | **1033 lines uncommitted + untracked `internal/agentskill/`** | — | ✅ (uncommitted) | ✗ |
| `feat/agent-safe-retirement` | `~/Worktrees/dev-cli/feat-completion-and-brew` | 3 / 13 | clean | 4 | ✗ | ✗ |

`feat/agent-skill-manager` is not "an agent still running" — its herdr status is `idle` and its
branch tip `a1fd1d0` is already contained in `main`. A complete feature (`internal/agentskill/`
591+212 lines, `dev skill list/add/update`, a TUI SKILLS panel, and its own EN+zh-TW docs) was
left uncommitted when the session ended. Step 1 commits it.

Already-merged branches with no worktree (`feat/azure-devops-remote-repo`, `feat/completion-and-brew`,
`feat/qa-cold`, `feat/repo-quick-notes`, `feat/try-tui-lifecycle`) just need branch cleanup.

### Two artifact-only orphan directories

`~/Worktrees/dev-cli/feat-azure-devops-remote-repo` and `~/Worktrees/dev-cli/feat-copy-metadata-and-nested-wt`
exist on disk, are **not** registered git worktrees, and contain only `.specstory/` + `.DS_Store`.
This is exactly the orphan case documented in
`internal/skill/dev-cli/references/agent-retirement.md` ("SpecStory then recreated the path with
only `.specstory/` content"). Their transcripts appear to already exist in `main`'s
`.specstory/history/` — verify byte-identity before removing.

### The tooling already exists on a branch

`feat/agent-safe-retirement` already implements the "merged → safe to retire" capability the user
asked for. Do **not** rewrite it:

- `dev sweep --merged-worktrees --base <ref> [--delete-branches]` — `internal/cli/sweep.go:252`
  (`suggestMergedWorktrees`). Git-first enumeration of `gitx.Worktrees(...)`, `merge-base
  --is-ancestor <branch> <base>` containment test, synthesises a Done task for *unmanaged*
  worktrees, then runs a blocker gauntlet (locked, prunable, dirty, `gitx.InProgress`,
  unfinalized artifacts, `retirement.Inspect`) before offering an actionable suggestion.
- `dev retire [target] [--delete-branch --close-unknown --assume-no-runtime --timeout]` —
  `internal/cli/retire.go:18`, policy in `internal/retire/safety.go` + `service.go`.
- `dev done --merged --base-ref <ref> [--confirm-squash <commit>]`; `done` no longer removes
  worktrees — cleanup moved to `retire`.

By contrast, `main` today has **no** merged-branch detector: the only ancestry checks are the
reactive gate `deleteMergedBranch` (`internal/cli/done.go:212`) and the inverse filter
`unmergedBranches` (`internal/cli/adopt.go:244`). `dev sweep`'s `Done` case
(`internal/cli/sweep.go:174`) says `"merged"` but only reads persisted task state.

---

## Approach

Serial, one branch at a time: **rebase → backfill docs → run every gate → `merge --ff-only` → retire.**
`main` stays green and fully documented after every single merge.

### Order and why

1. `feat/attach-session` — smallest conflict surface (1 hunk) today; lands semantic colors and
   the interactive `done` flow that everything else then inherits.
2. `feat/dev-journal` — docs already complete; only needs rebase + `llms*.txt` regeneration.
3. `feat/remote-fleet` — largest conflict surface (16 hunks), mostly additive TUI panels.
4. `feat/agent-skill-manager` — commit first, then land; largest TUI diff (`model.go +247`).
5. `feat/agent-safe-retirement` — **last on purpose.** It and `attach-session` are the two branches
   that rewrite `internal/cli/done.go`; landing retirement last makes its semantics
   (`done` = MERGED, `retire` = cleanup) the authoritative final state rather than something a
   later rebase has to re-derive. Its binary is then what we use to close everything.

Branches 2–4 all extend `internal/tui/{model,view,rows}.go` with additive panels — expect
mechanical, not semantic, conflicts there. `internal/skill/dev-cli/references/commands.md` is
**generated**: never hand-resolve it, take either side then run `make skill-sync`.

---

## Step 0 — Safety net (do first, no exceptions)

```bash
cd /Users/david/Documents/Program/dev-cli
d=$(date +%Y%m%d)
for b in feat/attach-session feat/dev-journal feat/remote-fleet \
         feat/agent-skill-manager feat/agent-safe-retirement; do
  git tag "backup/${b#feat/}-$d" "$b"
done
git tag "backup/main-pre-integration-$d" main
```

Commit `feat/agent-skill-manager`'s working tree **before** tagging it (Step 1), so the tag
captures real work. Confirm with the user before any `git push`.

---

## Step 1 — Rescue `feat/agent-skill-manager`

In `~/Worktrees/dev-cli/feat-agent-skill-manager`:

1. `gofmt -l .`, `go vet ./...`, `go test ./...`.
2. If green: `git add internal/agentskill/ internal/cli/ internal/tui/ internal/help/ internal/skill/ README.md docs/` and commit as `feat: add agent skill manager`.
   Handle `.specstory/` per the `agent-history-hygiene` skill; `.specstory/statistics.json` is derived — the repo already treats it as noise.
3. If red: fix before committing; do not commit failing work.

---

## Step 2 — Per-branch landing loop

Run this identically for each branch, in the order above. `BR` = branch, `WT` = its worktree.

**2a. Rebase**

```bash
git -C "$WT" rebase main            # resolve conflicts in the worktree, never in main
git -C "$WT" status                 # must be clean when done
```

Conflict guidance:
- `internal/cli/root.go` — command registration; keep **both** sides' `AddCommand` calls.
- `internal/skill/dev-cli/references/commands.md` — generated, take either side, regenerate.
- `internal/skill/dev-cli/SKILL.md` — merge by hand: "Everyday commands" block, numbered
  "Rules for agents" (renumber), and the "Reference files" bullet list.
- `internal/tui/{model,view,rows}.go` — additive panels; keep both.
- `docs/llms.txt`, `docs/llms-full.txt` — generated, resolve trivially and regenerate in 2b.
- `internal/cli/done.go` (branches 1 and 5 only) — semantic; on branch 5 the retirement
  semantics win.

**2b. Backfill docs and changelog** (skip for `feat/dev-journal`, which is already complete —
just regenerate `llms*.txt`). Follow the exact contract enforced by `scripts/check-docs.py`:

- EN page + `.zh-TW.md` sibling, identical heading structure; zh-TW adds `lang: zh-TW` and the
  mandatory `!!! note "術語規則"` admonition right after the H1.
- Frontmatter `description`, `authority`, `status`, `verified_on` — `authority` ∈ the 8-value set,
  `status` ∈ `{stable, maintained, evolving, official, research-preview-partial,
  experimental-and-versioned, generated-plus-authored}`, `verified_on` not in the future.
  `authority`/`status`/`verified_on` must be **byte-identical** across the pair.
  **Trap:** the template in `docs/notes/authoring.md` lists `draft`/`verified`/`historical`,
  which the checker rejects. Do not copy it verbatim.
- Add to `nav:` in `mkdocs.yml` **and** to `plugins.i18n.languages[zh-TW].nav_translations`.
- Add a row to the Claim/source matrix in `docs/reference/sources-freshness.md` **and** its zh-TW sibling.
- No private absolute paths (`/Users/<x>` fails the checker).
- `CHANGELOG.md` `[Unreleased] → ### Added` entry.
- Regenerate: `uv run python scripts/check-docs.py --source --generate-llms` and commit
  `docs/llms.txt` + `docs/llms-full.txt`.

Docs work per branch:

| Branch | Docs deliverable |
|---|---|
| `attach-session` | No new page. Extend `docs/guides/change-stream-workflow.md` (+zh-TW) with the interactive `dev done` finalization flow; add semantic-color config + new `done` flags to `docs/reference/commands-config.md` (+zh-TW) and `docs/reference/compatibility.md` (+zh-TW). |
| `remote-fleet` | **New** `docs/guides/remote-fleet.md` (+zh-TW): the `dev fleet` command family, `[fleet]` config, transport/cache model, degradation when a forge CLI is absent. Plus commands-config, compatibility, sources-freshness rows, nav + nav_translations. |
| `agent-skill-manager` | Verify the already-written `docs/guides/tui-repos-bootstrap.md` (+zh-TW) edits cover `dev skill list/add/update` and the SKILLS panel; add commands-config rows + CHANGELOG. |
| `agent-safe-retirement` | **New** `docs/guides/agent-safe-retirement.md` (+zh-TW): READY/MERGED/RETIRED milestones, `dev prepare` / `dev artifact` / `dev done --merged` / `dev retire` / `dev sweep --merged-worktrees`, the seven refusal conditions, and the explicit non-guarantee that raw `git worktree remove --force` bypasses dev. Source material already exists in `internal/skill/dev-cli/references/agent-retirement.md` and `internal/help/topics/retirement.md`. Plus nav, nav_translations, sources-freshness rows, commands-config, compatibility. |

**Subagent usage (sonnet):** launch three sonnet subagents *now, in parallel* to draft page
bodies (EN + zh-TW) for `attach-session`, `remote-fleet`, and `agent-safe-retirement` into the
scratchpad, reading each feature's code from its worktree. Drafting is parallel; **applying is
serial**, inside each branch's turn — all three would otherwise collide on `mkdocs.yml`,
`sources-freshness.md`, `CHANGELOG.md`, and `llms*.txt`.

**2c. Gates — all must pass before merging**

```bash
files="$(gofmt -l .)" && test -z "$files"
go vet ./...
go test -race ./...
make skill-sync && make skill-check          # regenerates commands.md; commit if it changes
uv sync --frozen --extra docs
uv run python scripts/check-docs.py --source
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

Run `make e2e` once at the end of the whole sequence rather than per branch (it is slow and
builds an isolated `HOME`).

**2d. Fast-forward into main**

```bash
git -C /Users/david/Documents/Program/dev-cli switch main
git merge --ff-only "$BR"          # must succeed; if it does not, the rebase was incomplete
```

Keeps `main` linear — it currently has zero merge commits.

**2e. Retire the worktree** — see Step 3 for branches 1–4; branch 5 retires itself last.

---

## Step 3 — Retire with the tool we just built

After branch 5 lands, `make install` (or `make build`) so the `dev` on `PATH` — currently a stale
`v0.1.0` predating quick notes — actually has `retire` and `sweep --merged-worktrees`.

```bash
cd /Users/david/Documents/Program/dev-cli   # must run from the canonical checkout
dev sweep --merged-worktrees                # REPORT ONLY — review candidates and blockers
```

Present the exact candidate/blocker list to the user, get approval, then:

```bash
dev sweep --merged-worktrees --apply        # confirms each item individually
dev sweep --merged-worktrees --apply --delete-branches   # only with separate explicit approval
```

Per `references/agent-retirement.md`, containment alone is never permission, and branches remain
by default. For branches 1–4 landed before the tool exists, either wait and retire them all in
this one pass (preferred), or use today's `dev done <task> --ff` + `dev wt rm <branch>`.

Then clean up the residue:

- Verify the two orphan directories' transcripts are byte-identical to `main`'s
  `.specstory/history/` copies (`cmp`), then remove the empty shells.
- Delete the already-merged branchless branches: `feat/azure-devops-remote-repo`,
  `feat/completion-and-brew`, `feat/qa-cold`, `feat/repo-quick-notes`, `feat/try-tui-lifecycle`
  (`git branch -d`, which refuses unmerged work).
- Clear stale task records (`dev sweep --apply` already offers this for
  `azure-devops-remote-repo` and `exact-agent-history-selectors`), and close the stale herdr
  workspace still pointing at the removed `feat-azure-devops-remote-repo` path.
- Ask before `git push origin main`.

---

## Step 4 — Harden the harness (new branch `feat/integration-gates`)

Three of the four asks; the fourth (`sweep` merged-branch detection) arrives with branch 5 and
needs no new code.

**4a. Generic change gate (`internal/config` + new `internal/gate`)**

Repo-configured, tool-agnostic — no knowledge of mkdocs, Go, or Cobra:

```toml
[[gate]]
name = "changelog"
when_changed  = ["internal/**", "cmd/**"]
require_changed = ["CHANGELOG.md"]
severity = "warn"        # warn | error; default warn
```

`dev` compares the branch-vs-base diff against the rules and reports. Surface it in `dev doctor`
and in the `land` preflight (4b). This project then declares its own CHANGELOG rule in config;
the dev-cli-specific coupling (Cobra tree → `commands.md` → mkdocs nav → `llms.txt` → zh-TW
parity) stays where it already works, in `make skill-check` and `scripts/check-docs.py`.

**4b. `dev land [task]` (`internal/cli/land.go`)**

Fixes the exact manual sequence this plan just executed, report-before-apply:
resolve canonical repo + explicit base → rebase onto base → evaluate 4a gates → `merge --ff-only`
→ hand off to `retire`. Reuse `fastForward` (`internal/cli/done.go:191`), `retire.Service`, and
`gitx.InProgress` (`internal/gitx/transactions.go:35`). Never infer the base from whatever branch
happens to be checked out. Report-before-apply, per the `AGENTS.md` reconciliation contract.

**4c. Artifact-only orphan detection**

Teach `retire`/`wt rm` to recognise a path that exists, is unregistered by git, and contains only
artifact directories — the incident `agent-retirement.md` already describes. Offer removal only
after proving the transcripts are reachable in the base branch; treat it as an orphan needing
salvage, never as RETIRED.

**4d. Synchronization required by `AGENTS.md`** for 4a–4c: `make skill-sync` + `make skill-check`;
`SKILL.md` (Everyday commands, Rules for agents, Reference files); a new
`internal/help/topics/` entry for `land`; `docs/reference/commands-config.md` +zh-TW;
`docs/reference/compatibility.md` +zh-TW; `docs/reference/sources-freshness.md` +zh-TW;
`CHANGELOG.md`.

---

## Step 5 — Version bump

`AGENTS.md`: "Do not publish or describe a post-`v0.1.0` feature build as `v0.1.0`." This lands
five features plus new commands, so the next release is `v0.2.0`. Move `[Unreleased]` entries
into `## [0.2.0] - <date>`, update comparison links, and tag only when the user asks.

---

## Verification

Per branch, before its `merge --ff-only`: `gofmt -l` empty, `go vet`, `go test -race ./...`,
`make skill-check`, and the full four-command docs chain (`check-docs.py --source`,
`mkdocs build --strict`, `check-docs.py --site site`).

End to end, after everything:

```bash
make all && make e2e
git log --oneline --graph -25 main        # still linear, no merge commits
git branch -vv                            # only main + anything deliberately kept
git worktree list                         # only the canonical checkout + active worktrees
ls ~/Worktrees/dev-cli/                   # orphan shells gone
dev ls && dev sweep                       # no stale task records
dev sweep --merged-worktrees              # reports nothing left to retire
dev land --help && dev retire --help && dev doctor
```

Spot-check the built site: the two new guide pages render in both locales, appear in the zh-TW
nav with translated labels, and `docs/llms.txt` lists them under the right section.

## Risks

- **Rebase 5 branches with 30+ conflict hunks total.** Backup tags in Step 0 make every step
  reversible; `git rebase --abort` plus the tag is always a clean exit.
- **Conflict counts grow as branches land** — the 1/8/16/4 figures are against today's `main`.
  Recompute with `git merge-tree` before each rebase.
- **`go test -race ./...` + the docs chain × 5** is slow. Batch `make e2e` to the end.
- **Semantic colors land first**, so `journal`/`fleet`/`retire` output will not be colorized.
  Cosmetic; optional follow-up, not part of this plan.
- **Do not touch** any worktree the user later marks active. Right now none are: all herdr
  dev-cli agent statuses are `idle`/`done`.
