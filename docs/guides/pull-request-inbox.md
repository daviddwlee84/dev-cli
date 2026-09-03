---
description: List pull requests you opened and requests awaiting your review, understand provider cost and missing fields, and inspect local checkout health.
authority: project
status: stable
verified_on: 2026-09-02
---

# Pull request inbox

`dev pr list` shows the pull and merge requests waiting on you and, when the
provider reports a head branch, the matching local task/checkout. It changes
nothing.

!!! info "Freshness"
    **Authority:** `internal/forge`, `internal/cli/pr*.go`, and their tests ·
    **Status:** stable · **Verified:** 2026-09-02.

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

## Acting on a request

`dev` prints commands and never runs them:

```bash
dev pr list --actions
dev pr list --json | jq -r '.pull_requests[].actions.merge'
```

Approving, merging, commenting, resuming, and retiring remain operator actions.
The current comment action contains only a generic body placeholder (`'...'`);
`dev` does not synthesize a vendor review-trigger phrase.

## Retiring the worktree behind a merged request

A forge reporting a merged request is evidence, not retirement authorization:

```bash
dev pr list --scope local --state merged   # candidates
dev sweep --merged-worktrees               # proves containment, reports first
```

A squash merge does not make the local feature branch an ancestor of the base,
so the forge answer alone cannot prove recovery. `dev sweep
--merged-worktrees` proves containment locally, while `dev done --merged`
requires explicit squash attestation where applicable. Treat the inbox as a
reason to inspect, never as permission to delete.

For deterministic agent-readable triage, use the generic prompt surface rather
than a PR subcommand:

```bash
dev prompt render pr-triage
dev prompt run pr-triage --agent my-agent
dev prompt open pr-triage --agent my-agent
```

See [Prompt handoffs](prompt-handoffs.md) for recipe, configuration, transport,
TTY, permission, and runtime boundaries.

## Provider availability

`gh` and `glab` are optional and independent. A signed-out provider is reported
under the table with the exact login command, while another ready provider still
contributes rows. `dev doctor` reports the same state. In JSON, inspect
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
