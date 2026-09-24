---
description: List pull requests you opened and requests awaiting your review, understand provider cost and missing fields, and inspect local checkout health.
authority: project
status: stable
verified_on: 2026-09-24
---

# Pull request inbox

`dev pr list` shows the pull and merge requests waiting on you and, when the
provider reports a head branch, the matching local task/checkout. It changes
nothing.

!!! info "Freshness"
    **Authority:** `internal/forge`, `internal/prflow`, `internal/cli/pr*.go`, and their tests ·
    **Status:** stable · **Verified:** 2026-09-24.

## The problem

Opening a pull request usually ends a worktree's active writing phase, but
nothing local says so. The branch is pushed, review owns the outcome, and the
checkout stays on disk. Meanwhile requests accumulate in two directions: ones
you opened and ones awaiting your review.

```bash
dev pr list
```

```text
PR                      TITLE          ROLE    STATE   CHECKS  REVIEW    LOCAL       UPDATED
github:owner/api#12     Add retry      mine    open    pass    approved  ~/Worktr…   2026-09-01
github:owner/web#31     Fix parse      review  open    fail    —         —           2026-08-30
```

## Account and local surfaces

The providers expose account-wide and per-repository listings with different
fields and costs.

| | `--scope account` | `--scope local` |
|---|---|---|
| coverage | requests across the authenticated account | selected local repositories |
| query cost | author and reviewer are separate role queries | up to one paginated query per repository **per requested role** |
| default roles | author + reviewer | author + reviewer (therefore up to two queries per repository) |
| repository set | all account results, then filtered by `--repo` | repositories carrying a `dev` task; `--all-repos` widens |
| states | open | open, merged, closed, all |

`--scope all` is the default and unions both surfaces. A richer row upgrades a
summary row for the same provider/repository/number.

Provider fields are not symmetrical:

- GitHub's account search produces `detail: "summary"` and cannot report
  `head_branch`, `review_decision`, or `checks`.
- GitHub's per-repository list produces `detail: "full"` with those fields.
- GitLab's account and repository lists produce full branch/merge detail, but
  neither carries `checks`; pipeline status exists only on its single-request
  endpoint. `review_decision` is also not reported by these list surfaces.

An absent field means the surface did not report it, not that the underlying
value is empty. Read `detail` and provider capabilities before drawing a
conclusion.

```bash
dev pr list --scope account
dev pr list --scope local
dev pr list --repo owner/api
dev pr list --repo github:owner/api
dev pr list --linked
```

`--repo` filters both account results and local query targets. It accepts
`owner/name`, `provider:owner/name`, or a forge URL; a provider-qualified value
pins the provider. `--linked` means the request's expected branch is actually
checked out and its status was read—it does not merely mean a task mentions the
branch.

Account search cannot distinguish merged from closed. If `--state merged`,
`closed`, or `all` is requested with account/all scope, collection narrows to the
local surface. Structured output reports this **effective** scope (`"local"`),
not the broader value originally requested.

## Repository pages and the REMOTE tree

Expand a GitHub/GitLab repository in REMOTE with **Space** to load its PR/MR
children. The repository inventory includes personal projects and accessible
organization repositories. This page can show requests from other authors too;
being the author or a requested reviewer is displayed separately.

The default page contains up to 50 requests. If more than 50 open requests are
known, the automatic view switches to your authored requests and requested
reviews. The current scope, loaded count, overall count or lower bound, stale
state, and **Load more** availability stay visible. Use **Ctrl+O** to select all
open requests, related requests, refresh, or another page. Related queries are
filtered by the provider, so an old request is not lost behind unrelated pages.
Filtering `/` searches loaded rows only. Row badges and summaries use the
accepted observations; missing checks remain unknown.

The equivalent CLI repository page is explicit:

```bash
dev pr list --scope repo --repo github:owner/api
dev pr list --scope repo --repo gitlab:group/api --role author,reviewer
dev pr list --scope repo --repo github:owner/api --page-size 50 --json
dev pr list --scope repo --repo github:owner/api --cursor '<next_cursor>' --json
```

This scope requires one provider-qualified repository or repository URL. It
supports `--role all|author|reviewer|author,reviewer` and all documented states.
Continue with the returned opaque cursor and unchanged account, repository,
state, role and page size. JSON adds `pagination` (`next_cursor`, `complete`,
`total`, `total_lower_bound`, `relationship`) to the existing schema-v1 object.
A loaded page is not the whole repository when another cursor remains.

## Inspect and try a request

```bash
dev pr view https://github.com/owner/api/pull/12
dev pr view https://gitlab.com/group/api/-/merge_requests/12 --json
dev pr diff https://github.com/owner/api/pull/12
dev pr diff https://github.com/owner/api/pull/12 --no-pager > pr.diff
dev pr checkout https://github.com/owner/api/pull/12 --dry-run
```

`view` reads fresh checks, review/merge readiness, change size where available,
and local checkout candidates. `diff` uses optional `diffnav` in a terminal and
otherwise writes the provider diff. Terminal output neutralizes control
sequences; piped output retains patch bytes. This is a live remote preview,
not immutable approval evidence. Provider limits, binary omissions and changed
file counts are reported. An incomplete preview can be inspected interactively;
noninteractive output refuses to produce a misleading complete patch. `--web`
opens the request or diff in the browser.

`checkout` reuses an exact matching checkout without resetting it. Dirty files
or a local tip different from the current PR head are reported and retained.
Otherwise it fetches the selected request and creates a task-free worktree from
the verified head. Multiple local matches require selecting `--repo`; an
existing matching SSH remote keeps its native transport, and `--source-remote`
can select among ambiguous base remotes. No personal fork is created.

```bash
dev pr checkout https://github.com/owner/api/pull/12 --repo ~/src/api
dev pr checkout https://github.com/owner/api/pull/12 --try
dev pr checkout https://github.com/owner/api/pull/12 --clone --path ~/src/api
dev pr checkout https://github.com/owner/api/pull/12 --try --provision
```

Without a local repository, interactive checkout offers an independent dated
Try clone or a project clone plus worktree; Try is the suggested choice. Scripts
must choose `--try` or `--clone`. Provisioning, ignored-file copying, dependency
installation, project hooks and submodule initialization are off for PR
acquisition until explicit `--provision`; established project trust still
applies. A new Try keeps catalog identity and its own clone. `--no-open` prepares
only the checkout; `--json` reports paths and retained effects without opening a
runtime. Task adoption and eventual cleanup remain separate actions.

## Merge and update the local base

```bash
dev pr merge https://github.com/owner/api/pull/12 --squash --dry-run
dev pr merge https://github.com/owner/api/pull/12 --squash
dev pr merge https://github.com/owner/api/pull/12 --squash --sync-base ff-only --repo ~/src/api
dev pr sync-base https://github.com/owner/api/pull/12 --repo ~/src/api --strategy rebase
```

Merge reviews one exact head and rechecks the account, repository, refs,
permissions and current provider readiness before one immediate squash request.
Use `--yes` for a reviewed noninteractive operation. Passing checks alone do not
mean ready: draft, review requirements, unknown policy, queue/auto-merge and
GitLab merge trains can block this action. Those deferred workflows remain in
the provider UI; dev offers no admin override. Provider branch-deletion policy
is shown before merge, although dev does not request branch deletion.

Private attempt/result receipts live under `<state_dir>/pr-merges/`. An
interrupted write may have completed remotely: dev preserves **unknown**,
reconciles only by reads, and does not send the merge again. A cached ready badge
or a previously viewed diff never authorizes a new write.

Base synchronization starts only after confirmed merge and uses the actual PR
base branch, which need not be `main`. It fetches first, then constructs a fresh
local synchronization plan. Fast-forward is the default; rebase requires an
explicit strategy. Dirty, occupied, ambiguous or changed checkouts block
mutation. No branch switch, stash, force reset or automatic cleanup is implied.
Merge and local synchronization have separate results: a later sync failure
retains the completed remote merge and any recovery information.

`dev pr list --actions` still prints operator commands without executing them.
Approve and comment remain provider actions; comment suggestions contain only a
generic `'...'` placeholder, never an AI-review trigger phrase.

## Native gh-dash

The dashboard action opens an explicitly installed `dlvhdr/gh-dash` through
`gh dash`, retaining its configuration and keybindings. It passes the selected
GitHub host/repository and a verified local checkout when available; otherwise
it uses a neutral temporary directory, not another project's local config.
It neither installs the extension nor rewrites `.gh-dash.yml`, and does not
promise to select a particular PR inside the native dashboard. GitLab requests
continue through dev's MR actions or the browser.

## Retiring the worktree behind a merged request

A forge reporting a merged request is evidence, not retirement authorization:

```bash
dev pr list --scope local --state merged   # candidates
dev work sweep --merged-worktrees               # proves containment, reports first
```

A squash merge does not make the local feature branch an ancestor of the base,
so the forge answer alone cannot prove recovery. `dev work sweep
--merged-worktrees` proves containment locally, while `dev work done --merged`
requires explicit squash attestation where applicable. Treat the inbox as a
reason to inspect, never as permission to delete.

For deterministic agent-readable triage, use the generic prompt surface rather
than a PR subcommand:

```bash
dev agent prompt render pr-triage
dev agent prompt run pr-triage --agent my-agent
dev agent prompt open pr-triage --agent my-agent
```

See [Prompt handoffs](prompt-handoffs.md) for recipe, configuration, transport,
TTY, permission, and runtime boundaries.

## Provider availability

`gh` and `glab` are optional and independent. A signed-out provider is reported
under the table with the exact login command, while another ready provider still
contributes rows. `dev self doctor` reports the same state. In JSON, inspect
`providers` before concluding that an empty `pull_requests` array means an empty
inbox.

Azure DevOps pull requests are not currently listed. A configured Azure target is
reported as unsupported rather than failing a successful GitHub/GitLab result.

## Structured output

`dev pr list --json` emits a schema-versioned object, not a bare array:

```json
{
  "schema_version": 1,
  "generated_at": "2026-09-02T12:00:00Z",
  "scope": "local",
  "state": "open",
  "roles": ["author", "reviewer"],
  "repositories": ["github:owner/api"],
  "providers": [{"forge": "github", "status": "ready"}],
  "pull_requests": [{
    "forge": "github",
    "repo": "owner/api",
    "number": 12,
    "detail": "full",
    "head_branch": "feat/retry",
    "local": {
      "task_id": "retry",
      "task_state": "hot",
      "repo_path": "<repo-path>",
      "checkout": "<checkout-path>",
      "expected_branch": "feat/retry",
      "live_branch": "feat/retry",
      "branch_checked_out": true,
      "checkout_exists": true,
      "worktree_registered": true,
      "status_available": true,
      "git": {"dirty": false, "ahead": 0, "behind": 0, "upstream": "origin/feat/retry"}
    },
    "actions": {"comment": "gh pr comment 12 --repo owner/api --body '...'"}
  }]
}
```

Top-level `scope`, `state`, `roles`, and `repositories` describe the effective
collection. `providers` distinguishes an empty inbox from unavailable sources.

The optional `local` object separates durable task intent from live checkout
facts:

- `expected_branch` is the branch recorded by the task; `live_branch` is what
  status actually observed.
- `checkout_exists`, `worktree_registered`, and `status_available` identify
  independent health gates.
- `branch_checked_out` is true only when the checkout exists, remains registered,
  status was available, and live branch equals expected branch.
- `status_error` explains unavailable/missing/unregistered state when present.
- `git` is optional and appears only after the expected branch was proven live;
  it contains `dirty`, `ahead`, `behind`, and optional `upstream`.

Schema version 1 is add-only: fields may be added, while existing field names and
meanings are preserved.

## Scheduling

There is no daemon or built-in scheduler. `dev pr list` is a plain read-only
query, so recurrence belongs to cron, launchd, or another scheduler:

```bash
*/30 * * * * dev pr list --json > ~/.cache/pr-inbox.json
```

## Related pages

- [Prompt handoffs](prompt-handoffs.md)
- [Agent-safe retirement](agent-safe-retirement.md)
- [Change-stream workflow](change-stream-workflow.md)
- [Compatibility and known limitations](../reference/compatibility.md)
