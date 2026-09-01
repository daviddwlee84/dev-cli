# `dev pr` — a stateless PR inbox, agent handoff, and forge-auth UX

## Context

Opening a PR ends a worktree's useful life, but nothing tells you that. Open PRs
accumulate: some are yours and waiting on review, some are waiting on *your*
review, and the worktrees behind the merged ones sit around because `dev` has no
way to know they merged. `dev sweep --merged-worktrees` only tests local ancestry
(`git merge-base --is-ancestor`), and `dev done --pr` prints the PR URL and throws
it away. This is the already-recorded gap in `TODO.md` ("P3 · M — Query forge
merge status and squash identity") and in
`docs/reference/compatibility.md` ("Pull-request completion is not tracked
automatically"). This branch is that item.

Three outcomes:

1. **See the queue.** One command lists PRs you authored and PRs awaiting your
   review, account-wide, joined against local worktrees and tasks so "this
   checkout is done, retire it" becomes visible.
2. **Hand it to an agent without hardcoding one.** `dev` renders a prompt from
   built-in templates with the live scan embedded, and optionally pipes it to a
   *user-configured* agent command. `dev` does not parse the reply, does not
   loop, and does not know what Claude or Codex are.
3. **Stop the signed-out-CLI noise.** A first run with an unauthenticated `gh`
   currently shows
   `✗ glab api projects --method GET -f membership=true …: glab: HTTP 401: exit status 1`
   in the TUI footer. It should read: `glab is signed out — run` `glab auth login`.

Explicitly **not** built: any scheduler or daemon. `dev pr ls` is a fast,
repeatable query; cron/launchd or Claude Code's `/loop` handles recurrence.

## Design

### 1. Forge PR layer (`internal/forge`)

Add an **optional capability interface**, following the `RepoPublisher`
precedent (`forge.go:67`) rather than widening `Forge`. Callers type-assert and
degrade when the assertion fails.

```go
type PRLister interface {
	// ListPRs returns pull/merge requests matching q. Like ListRepos it may
	// return partial results alongside an error.
	ListPRs(ctx context.Context, q PRQuery) ([]PullRequest, error)
}

type PRRole string // "author" | "reviewer"
type PRState string // "open" | "merged" | "closed"

type PRQuery struct {
	Roles []PRRole
	// Repos restricts to specific owner/name identities. Empty means the
	// account-wide search. A non-empty Repos selects the richer per-repo
	// surface, which is the only one carrying HeadBranch and ReviewDecision.
	Repos []string
	State PRState
	Limit int
}

type PullRequest struct {
	Forge      Kind      `json:"forge"`
	Repo       string    `json:"repo"`     // owner/name, matches IdentityFromURL
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	State      PRState   `json:"state"`
	Draft      bool      `json:"draft"`
	Author     string    `json:"author"`
	Roles      []PRRole  `json:"roles"`

	// Present only from the repo-scoped surface; omitted from account search.
	HeadBranch     string `json:"head_branch,omitempty"`
	BaseBranch     string `json:"base_branch,omitempty"`
	ReviewDecision string `json:"review_decision,omitempty"`
	Checks         string `json:"checks,omitempty"`
	Mergeable      string `json:"mergeable,omitempty"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}
```

**Two surfaces, not GraphQL.** Verified against the installed CLIs
(gh 2.98.0, glab 1.92.1):

- `gh search prs --json` exposes only assignees, author, authorAssociation,
  body, closedAt, commentsCount, createdAt, id, isDraft, isLocked,
  isPullRequest, labels, number, repository, state, title, updatedAt, url —
  **no `headRefName`, no `reviewDecision`, no `statusCheckRollup`.**
- `gh pr list --json` does expose `headRefName`, `baseRefName`,
  `reviewDecision`, `statusCheckRollup`, `mergeable`, `mergedAt`.

So the account-wide inbox and the branch↔worktree join genuinely need different
calls. Use `gh search prs --author=@me` / `--review-requested=@me` for the
account sweep (two calls total, regardless of repo count) and
`gh pr list --repo <r> --json …` per known repo for the join. Prefer this over
one `gh api graphql` call: both are documented, versioned CLI surfaces that
match the existing `gh api` idiom in `github.go`, and they are far easier to
fake in tests than a GraphQL response.

GitLab mirrors the split: account-wide via
`glab api merge_requests -f scope=created_by_me -f state=opened` (and
`scope=assigned_to_me`, plus a reviewer query), reusing the pagination loop in
`gitlab.go:145-170`; repo-scoped via `glab mr list -F json`. Azure DevOps
returns `&ErrUnsupported{Kind: AzureDevOps, Operation: "list pull requests"}` —
it simply does not implement `PRLister` in this change.

Reuse the existing `ListRepos` shape throughout: 60 s context timeout, `per_page=100`
pagination, and **return partial results with the error** rather than discarding.

**Files:** `internal/forge/pr.go` (types + shared helpers), and `ListPRs` methods
appended to `github.go` and `gitlab.go`.

### 2. Auth error classification (`internal/forge`)

The noise comes from `run()` (`forge.go:429-446`) embedding the full argv in
every error. Do **not** change `run` — it is generic and has no `Kind`. Instead
add a thin per-adapter wrapper:

```go
// internal/forge/autherr.go
type ErrAuth struct {
	Kind   Kind
	Bin    string
	Action string // reused verbatim from authProbe(kind)
	cause  error  // the full argv + stderr, still reachable via errors.Unwrap
}

func (e *ErrAuth) Error() string { return e.Bin + " is signed out — " + e.Action }
func (e *ErrAuth) Unwrap() error { return e.cause }

// classifyAuth returns an *ErrAuth when err looks like an authentication
// failure, and err unchanged otherwise.
func classifyAuth(kind Kind, bin string, err error) error
```

Detection is a substring match over the wrapped message for `HTTP 401`,
`HTTP 403`, `Bad credentials`, `401 Unauthorized`, `not logged in`,
`authentication required`, and `auth login`. Each adapter routes its API calls
through `g.run(ctx, dir, args...)` = `classifyAuth(kind, bin, run(...))`.

The **`Action` string is not new text** — reuse the one `authProbe(kind)` already
produces (`forge.go:302-318`), so the remediation stays in one place:
`` run `gh auth login --hostname github.com` ``.

Nothing downstream needs to change to benefit: `ErrAuth.Error()` is short, so the
TUI footer (`internal/tui/view.go:1095-1103`), the `errors.Join` aggregate in
`collectRemotesWithOptions` (`internal/cli/tui.go:1275-1286`), the
`dev: partial remote results: …` warnings (`internal/cli/repo.go:626,695`), and
the `status.Error` text persisted into `remotes.json` all improve automatically.
Diagnostic detail is preserved in the `Unwrap` chain rather than in the headline.

**`dev doctor` starts probing auth.** Replace the `exec.LookPath` loop at
`doctor.go:118-128` with `forge.ProbeAll(ctx)` — which already exists and is
currently used only by the repo-create wizard:

| Readiness | doctor row |
|---|---|
| `ready` | `✓ gh   /usr/local/bin/gh — GitHub: clone, create, PRs` |
| `missing-cli` | `! gh   not found — install `gh`, then run `gh auth login`` |
| `unauthenticated` | `! gh   signed out — run `gh auth login --hostname github.com`` |
| `probe-failed` | `! gh   probe failed — <detail>` |

Still `checkWarn`, never `checkFail`: doctor's stated policy is that everything
but git is optional. This is the direct answer to "a first-time user has not run
`gh auth login` and should not see something bizarre".

Cost caveat: `gh auth status` validates the token against the API, so this makes
`dev doctor` do network work it previously avoided. `ProbeAll` runs the providers
concurrently under the existing 10 s per-probe timeout (`forge.go:268`), so the
worst case is bounded at roughly 10 s rather than multiplying. If that proves
annoying, gate the probe behind a `--probe` flag and keep bare `dev doctor` on
`LookPath` — decide after feeling the real latency, not up front.

### 3. Command surface (`internal/cli/pr.go`)

Standard group pattern: parent with no `RunE`, leaves as `newPrXCmd(app)`.
Register with one line in `root.go:145-179`.

```
dev pr ls [query]
  --scope account|local|all   default all
  --mine / --review           role filters; default both
  --repo <owner/name>         repeatable; implies repo-scoped surface
  --all-repos                 widen local scope to every discovered repo
  --state open|merged|closed|all   default open
  --limit N
  --json
  --refresh / --cached
  --commands                  add a suggested gh/glab command per row

dev pr prompt [triage|review|retire]
  --agent <name>              hand the prompt to a configured agent
  --print                     force stdout even when an agent is default
  (same filter flags as `pr ls`)
```

- `--scope account` — the two `gh search prs` calls
  (`--author=@me` / `--review-requested=@me`, both `--state open`). Cheap,
  whole-account, no `head_branch`/`review_decision`. Note that
  `gh search prs --state` accepts only `open|closed` (verified via `--help`), so
  **`--state merged` implies the repo-scoped surface** — account scope cannot
  answer it. `dev pr ls --state merged` should therefore select `--scope local`
  automatically and say so rather than silently returning nothing.
- `--scope local` — one repo-scoped call per **engaged** repo, meaning a repo
  with at least one `dev` task or linked worktree. This matters: `repo.Discover`
  returns every repository under the scan roots (the TUI note in `TODO.md` cites
  56 on this machine), and one `gh pr list` per discovered repo would make the
  command unusably slow. Engaged repos are typically a handful, and they are
  exactly the ones that can answer "can I close this worktree". Derive the set
  from `inventory.Collect` rows plus `gitx.Worktrees()`. Bound the calls with a
  small dedicated semaphore (~4) rather than reusing `inventory.NewLimiter`:
  that limiter exists to bound *local* enrichment across collectors
  (`internal/inventory/limiter.go:5-7`), and forge APIs rate-limit on a different
  budget than disk. `--all-repos` widens to the
  full `repo.Discover` set for anyone who wants it, and prints how many repos it
  is about to query.

  Key on `string(kind) + "/" + strings.ToLower(name)` from
  `forge.IdentityFromURL(gitx.RemoteFromConfig(r.CommonDir, "origin"))` —
  exactly what `matchRemoteLocals` does (`internal/cli/tui.go:1339-1386`).
  **Reuse its ambiguity handling**: when two local clones map to the same remote
  identity it drops the entry rather than guessing, and it skips bare repos.
  Factor that loop out of `matchRemoteLocals` instead of copying it.
- `--scope all` (default) — both, deduped by `(forge, repo, number)`, with local
  rows upgrading account rows on overlap.

**Local join.** Done inside `pr.go`, *not* by adding a field to
`inventory.Row`. `inventory.Collect` runs on every `dev ls` and TUI refresh; a
network probe there would make the hot path depend on a forge round-trip.
`dev pr ls` calls `inventory.Collect(ctx, tasks, rt, inventory.Options{SkipRuntime: true})`
itself and matches `Row.Status.Branch` against `PullRequest.HeadBranch`.

Table: `PR  REPO  TITLE  ROLE  STATE  CHECKS  REVIEW  WORKTREE  AGE`.

JSON row (`prJSONRow` + `makePRJSONRow`, declared in `pr.go` per house style,
`omitempty` on every field the account surface cannot fill):

```json
{
  "forge": "github", "repo": "owner/name", "number": 12,
  "title": "…", "url": "…", "state": "open", "draft": false,
  "author": "me", "roles": ["author"],
  "head_branch": "feat/x", "base_branch": "main",
  "review_decision": "REVIEW_REQUIRED", "checks": "passing",
  "updated_at": "…",
  "local": { "repo_path": "…", "worktree_path": "…", "task_id": "…",
             "task_state": "hot", "branch_synced": true },
  "commands": { "view": "gh pr view …", "approve": "…", "merge": "…" }
}
```

Treat this as an automation contract from day one, like `dev ls --json`
(AGENTS.md): add fields later, never rename or remove.

**`--commands`** renders per-row `gh`/`glab` invocations (`view`, `approve`,
`merge`, and a `comment` that posts a review-trigger phrase). They are *printed*,
never executed — the user copies them or an agent reads them from `--json`.

**Retirement stays in `dev sweep`.** `dev pr ls --scope local` reports a merged
PR whose worktree still exists; it does not act. Wiring PR state into sweep's
`--apply` path would mean inferring integration from a forge answer, which
AGENTS.md and the existing `--confirm-squash` attestation deliberately refuse.
The guide shows the composition (`dev pr ls --scope local --json` →
`dev sweep --merged-worktrees`) instead.

### 4. Prompt templates and the `[[agent]]` config

**Templates** live in `internal/cli/assets/prompts/{triage,review,retire}.md`,
embedded with `//go:embed` — the precedent is
`internal/cli/repo_builtin_skill_setup.go:20`. No new package; `internal/prompt`
would collide conceptually with the interactive prompter in
`internal/cli/prompt.go`. Rendering reuses `scaffold.RenderTemplate`
(`internal/scaffold/template.go:97`, `{{a.b.c}}` dotted lookup, unknown variable
is an error) with variables `{{pr_json}}`, `{{pr_table}}`, `{{worktree_json}}`,
`{{host}}`, `{{generated_at}}`.

**Config** — new top-level repeated table, modeled on `[[tui.tools]]`
(`config.go:196-243`):

```toml
[[agent]]
name = "claude"
run = "claude -p"
default = true

[[agent]]
name = "codex"
run = "codex exec --file {{prompt_file}}"
timeout = "10m"
```

```go
type Agent struct {
	Name        string   `toml:"name"`
	Run         string   `toml:"run"`
	Interactive bool     `toml:"interactive"`
	Default     bool     `toml:"default"`
	Timeout     Duration `toml:"timeout"`
}
```

Deliberately **no `DefaultAgents()`**. An empty list means no agent is
configured, and `--agent` then fails with a pointer to `dev edit`. Shipping a
default `claude` entry would be exactly the hardcoding the user rejected.

Validation in `Validate()`: unique non-empty names, non-empty `run`, at most one
`default`, dotted-key error messages (`agent[%d].name must not be empty`).
A commented block goes into `const starterConfig`
(`internal/cli/config.go:143-345`) so `dev config init` documents it.

**Delivery.** The prompt goes to the agent's **stdin** by default; if `run`
contains `{{prompt_file}}`, `dev` writes a 0600 temp file, substitutes the path,
and removes it afterwards. Execution reuses the `[[tui.tools]]` mechanics
(`tui.go:996-1022`): `$SHELL -c run`, or `$SHELL -lic 'eval "$1"' dev-agent run`
when `interactive`, with stdio wired to `app.In/Out/Err` and a default 10 min
timeout matching `runRepoHook`.

**Security.** `[[agent]]` is host config only — a repository must never define a
command `dev` will execute. This is already structurally true: `projectFile`
(`internal/projectconfig/load.go:21-25`) decodes only `worktree` and `repo`, so a
repo-supplied `[[agent]]` is inert. Add `"agent": true` to
`deniedTopLevelSections` (`load.go:27-30`) anyway, so it is reported as **denied**
rather than as an unknown key — the diagnostic is the point, not a hole being
closed. The `projectconfig` trust store is not involved.

### 5. Cache

New `$XDG_CACHE_HOME/dev/prs.json`, reusing the hardened primitives in
`internal/forge/cache.go` (atomic temp + 0600 + rename, per-path write locks,
size/clock-skew validation). Extract those helpers and add a sibling
`PRCache{Version, SourceID, FetchedAt, Complete, Providers, PRs []PullRequest}`
rather than genericizing `Cache`. `SourceID` fingerprints the forge hosts *and*
the query (scope, roles, state, repo set) so a narrowed `--repo` run cannot serve
a later full scan. Register `{"pr", …/prs.json"}` in `cacheItems()`
(`internal/cli/cache.go:70`) and extend the `Use`/`ValidArgs` strings on
`dev cache clear`.

This is the most separable slice — if it slips, `dev pr ls` still works, just
without `--cached`.

## Out of scope (record in `TODO.md`)

- A TUI PR view. `dev pr ls` first; the TUI can consume the same collector later.
- Persisting PR identity on the task TOML. That needs the versioned task-schema
  work already listed as "P2 · L".
- CI/check-run watching beyond the `statusCheckRollup` summary `gh pr list`
  already returns.
- Azure DevOps PR listing.
- Any automatic retirement driven by forge state (see §3).

Update the existing `TODO.md` "P3 · M — Query forge merge status and squash
identity" entry to record what this change delivers (read-only PR state) and what
it still defers (persisted identity, squash proof).

## File-by-file work

**New**
- `internal/forge/pr.go` — `PRLister`, `PullRequest`, `PRQuery`, `PRRole`, `PRState`, shared parsing.
- `internal/forge/autherr.go` — `ErrAuth`, `classifyAuth`.
- `internal/cli/pr.go` — `newPrCmd`, `newPrListCmd`, `newPrPromptCmd`, `prJSONRow`, `makePRJSONRow`, local join, `--commands` rendering.
- `internal/cli/assets/prompts/{triage,review,retire}.md`.
- `internal/help/topics/pull-requests.md`.
- `internal/skill/dev-cli/references/pull-requests.md`.
- `docs/notes/ai-pr-review-options.md` + `.zh-TW.md`.
- `docs/guides/pull-request-inbox.md` + `.zh-TW.md`.

**Modified**
- `internal/forge/github.go`, `gitlab.go` — `ListPRs`, plus route API calls through the auth-classifying wrapper.
- `internal/forge/cache.go` — extract atomic-write/validate helpers, add `PRCache`.
- `internal/config/config.go` — `Agent` struct, `Config.Agents []Agent`, `Default()`, `Validate()`, `EffectiveAgents()`.
- `internal/projectconfig/load.go` — deny `agent`.
- `internal/cli/root.go` — register `newPrCmd(app)`.
- `internal/cli/doctor.go` — `forge.ProbeAll` rows.
- `internal/cli/cache.go` — register the `pr` cache.
- `internal/cli/config.go` — `starterConfig` block.
- `internal/cli/tldr.go` — `familyTLDR["dev pr"]` (ASCII only) and `helpTopics["dev pr"]`.
- `internal/skill/dev-cli/SKILL.md` — link the new reference inline and in the index at :506-523.
- `internal/skill/skill_test.go` — add the reference to both hardcoded slices at :33-70.
- `mkdocs.yml` — two nav entries plus their `nav_translations` labels.
- `docs/notes/index.md` (+ zh-TW) — index-table row for the new note.
- `docs/reference/sources-freshness.md` (+ zh-TW) — claim-matrix rows.
- `docs/reference/compatibility.md` (+ zh-TW) — revise "Pull-request completion is not tracked automatically", and add a new "Forge CLI authentication" subsection covering signed-out behavior and remediation.
- `TODO.md`, `CHANGELOG.md` `[Unreleased]`.

## Documentation specifics

The four deliverables, all bilingual and subject to `scripts/check-docs.py`:

1. **`docs/notes/ai-pr-review-options.md`** — a `source-review` note comparing
   Claude managed Code Review, Claude Code GitHub Action, Codex Code Review, and
   CodeRabbit: trigger phrase, whether a GitHub Action is required, plan tier and
   prerequisites, cost, and when to pick which.
   `.specstory/references/chatgpt-觸發-GitHub-Agent-Review-20260901-2038.md` is
   background only — **every claim must be re-verified against the primary docs
   at write time** and cited, because the note carries a `verified_on` date.
2. **`docs/guides/pull-request-inbox.md`** — the queue, the agent handoff, and
   composing with `dev sweep`.
3. **`internal/skill/dev-cli/references/pull-requests.md`** — agent-facing;
   English, no frontmatter, not part of the MkDocs corpus.
4. **Forge-auth troubleshooting** into `docs/reference/compatibility.md`.

Constraints the checker enforces, worth stating because they are easy to trip:
`status` must come from `evolving, experimental-and-versioned,
generated-plus-authored, maintained, official, research-preview-partial, stable`
— `draft`/`verified`/`historical` appear in `docs/notes/authoring.md` but are
**rejected**. `verified_on` cannot be in the future. `authority: anthropic-docs`
(or a preview/experimental status) requires `minimum_version` or `tested_with`.
`authority`, `status`, `verified_on`, `minimum_version`, `tested_with` must be
**identical** between the English and zh-TW siblings. zh-TW pages need
`lang: zh-TW` and the standard `!!! note "術語規則"` admonition as the first body
block. No `/Users/…` paths in any body. Every new page must appear in
`mkdocs.yml` nav and its label in `nav_translations`.

## Verification

```bash
make fmt && make vet
go test -race ./...
go test ./internal/forge -run '^TestListPRs'
go test ./internal/cli -run '^TestPr'
go test ./internal/config -run '^TestAgent'

make skill-sync && make skill-check      # regenerates references/commands.md
make build

uv sync --frozen --extra docs
uv run python scripts/check-docs.py --source --generate-llms
uv run python scripts/check-docs.py --source
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

Test plan:

- **`internal/forge`** — `ListPRs` tests against recorded `gh search prs` /
  `gh pr list` / `glab api` JSON. The list path is **not** faked through the
  `probeRunner` seam: `pagination_test.go:48` installs a real stub script on
  `PATH` (`installPagedCLI`, which logs `"$*"` and dispatches on `page=N`).
  Generalize that helper — it currently hardcodes two pages and a `page=` switch
  — rather than introducing a new runner variable. Cover pagination, the
  partial-results-plus-error contract, and the account-vs-repo surface split.
  `classifyAuth` unit tests over real gh/glab 401 stderr strings, asserting
  `errors.As` finds `*ErrAuth`, that `Error()` contains no argv, and that
  `Unwrap()` still does. Azure asserts `ErrUnsupported`.
- **`internal/cli`** — `pr_test.go` in package `cli_test` driving
  `cli.NewRootCommandWithIO(&out, &errOut)` with `--json`, asserting the schema
  and that account-scope rows omit `head_branch`. A doctor test asserting an
  unauthenticated probe renders a warn row with the login action and does not
  fail the command. A `pr prompt` test asserting the rendered template reaches
  stdout and that `--agent unknown` errors with the config pointer.
- **`internal/config`** — `[[agent]]` round-trip and validation (duplicate name,
  two defaults, empty `run`). A `projectconfig` test asserting a repo-supplied
  `[[agent]]` is reported as `denied`.

End-to-end, by hand: `dev doctor` with `gh` signed out shows the warn row and no
argv; `dev pr ls` in a repo with an open PR shows the row; `dev pr ls --json |
jq '.[0].local'` shows the worktree join; `dev pr prompt triage` prints a
complete prompt; configuring `[[agent]]` and running
`dev pr prompt triage --agent claude` hands it over.

## Sequencing

Each step builds and tests on its own:

1. `ErrAuth` + `classifyAuth` + `dev doctor` probing. Standalone UX fix; ship-able alone.
2. `PRLister` + gh/glab `ListPRs`.
3. `dev pr ls` (account scope, table + `--json`).
4. Local join, `--scope local|all`, `--commands`.
5. Prompt templates + `[[agent]]` config + `dev pr prompt`.
6. `prs.json` cache (droppable).
7. Docs, skill reference, `make skill-sync`, CHANGELOG, TODO.
