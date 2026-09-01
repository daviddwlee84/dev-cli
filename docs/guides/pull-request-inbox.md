---
description: List the pull requests you opened and the ones awaiting your review, join them to local worktrees, and hand the queue to an agent you configure.
authority: project
status: stable
verified_on: 2026-09-01
---

# Pull request inbox

`dev pr` shows the pull and merge requests waiting on you, and which of your
worktrees they belong to. It changes nothing.

!!! info "Freshness"
    **Authority:** `internal/forge`, `internal/cli/pr*.go` and their tests ·
    **Status:** stable · **Verified:** 2026-09-01.

## The problem

Opening a pull request usually ends a worktree's useful life, but nothing local
says so. The branch is pushed, review owns the outcome, and the checkout stays
on disk. Meanwhile requests accumulate in two directions: ones you opened and
are waiting on, and ones waiting on your review.

```bash
dev pr list
```

```text
PR                      TITLE          ROLE    STATE   CHECKS  REVIEW    LOCAL       UPDATED
github:owner/api#12     Add retry      mine    open    pass    approved  ~/Worktr…   2026-09-01
github:owner/web#31     Fix parse      review  open    fail    —         —           2026-08-30
```

## Two surfaces, and why a column can be blank

The providers expose two different listings, and the difference is not cosmetic.

| | `--scope account` | `--scope local` |
|---|---|---|
| cost | 2 calls, whole account | 1 call per repository |
| covers | every repo the account can see | repos with a `dev` task |
| head branch, review, checks | **not reported** | reported |
| states | open only | open, merged, closed, all |

`gh search prs` cannot return a head branch, a review decision, or check status
at all. So an account-scope row shows `—` in those columns because *the surface
does not know*, not because there is nothing there. Each row records which
produced it in its `detail` field: `summary` or `full`.

`--scope all` is the default and runs both, with per-repository rows upgrading
account rows for the same request.

Per-repository queries are limited to repositories `dev` has a task for.
Querying everything under `paths.scan_roots` would be one subprocess per
repository, which on a populated machine is dozens of calls for rows you did not
ask about. `--all-repos` opts into the wide scan.

```bash
dev pr list --scope account          # cheap, whole account, no branches
dev pr list --scope local            # branches and checks, engaged repos only
dev pr list --repo owner/api         # one repository
dev pr list --linked                 # only requests with a local checkout
```

## Acting on a request

`dev` prints commands and never runs them:

```bash
dev pr list --actions
dev pr list --json | jq -r '.pull_requests[].actions.merge'
```

Approving and merging are decisions, not conveniences. Review-trigger comments
work the same way — see
[AI pull-request review options](../notes/ai-pr-review-options.md) for the
phrases each reviewer expects.

## Retiring the worktree behind a merged request

`dev pr` reports that a request merged. It does not retire anything, and
`dev sweep` does not consult it:

```bash
dev pr list --scope local --state merged   # candidates
dev sweep --merged-worktrees               # proves containment, reports first
```

That separation is deliberate. When a request is squashed, the merged commit is
not an ancestor of the local branch, so "the forge says merged" cannot prove the
work is recoverable from the remote. `dev sweep --merged-worktrees` proves
containment locally with `git merge-base --is-ancestor`, and `dev done --merged`
requires an explicit `--confirm-squash` attestation. Treat the PR list as the
prompt to look, not as permission to delete.

## Handing the queue to an agent

```bash
dev pr prompt                  # triage prompt to stdout
dev pr prompt review           # work through your review queue
dev pr prompt retire           # which checkouts can be closed
```

The prompt embeds the live queue as JSON, so it can be piped anywhere:

```bash
dev pr prompt | pbcopy
dev pr prompt retire > /tmp/queue.md
```

With `--agent` it goes to a command you configure:

```toml
[[agent]]
name = "claude"
command = ["claude", "-p"]
default = true

[[agent]]
name = "codex"
command = ["codex", "exec", "--file", "{{prompt_file}}"]
input = "file"
timeout = "10m"
```

```bash
dev pr prompt retire --agent claude
dev pr prompt retire --agent codex --dry-run   # show the command, run nothing
```

There is **no built-in agent and no default entry**. `dev` renders a prompt and
starts a command you define; it does not read the reply and does not iterate.
Shipping a default would make `dev` depend on one particular tool.

The prompt arrives on the agent's stdin by default. `input = "file"` writes a
private temporary file and substitutes `{{prompt_file}}`. `run = "…"` takes a
shell line instead of an argv, but a prompt is never spliced into one — these
prompts contain shell commands by design, so `input = "argv"` requires the
`command` form.

`[[agent]]` is host configuration. A repository cannot define one: it names a
command `dev` executes, so `.dev-cli/config.toml` rejects the section.

## Running it on a schedule

There is no daemon and no built-in scheduler, deliberately. `dev pr list` is a
plain query, so recurrence belongs to whatever you already run:

```bash
*/30 * * * * dev pr list --json > ~/.cache/pr-inbox.json
```

## When a provider is signed out

`gh` and `glab` are optional and independent. A signed-out one is reported under
the table with the command to fix it, and the other still lists:

```text
  gitlab: signed out; run `glab auth login --hostname gitlab.com`
```

`dev doctor` reports the same state before you start. In `--json`, the
`providers` array carries it — read that before concluding the queue is empty.

## Structured output

`dev pr list --json` emits an object, not an array, so a consumer can tell "no
requests" from "GitLab was signed out":

```json
{
  "generated_at": "2026-09-01T12:00:00Z",
  "scope": "all", "state": "open",
  "providers":     [{"forge": "gitlab", "status": "unauthenticated", "action": "run `glab auth login ...`"}],
  "pull_requests": [{"forge": "github", "repo": "owner/api", "number": 12, "detail": "full",
                     "head_branch": "feat/retry", "review_decision": "approved", "checks": "passing",
                     "local": {"task_id": "retry", "checkout": "…", "dirty": false},
                     "actions": {"merge": "gh pr merge 12 --repo owner/api --squash"}}]
}
```

Fields are added over time and never renamed or removed, the same contract as
`dev ls --json`.

## Known limitations

- GitLab rows never carry `checks`: pipeline status is only on GitLab's
  single-merge-request endpoint, and `dev` does not fan out per request.
- Azure DevOps requests are not listed. Configured targets are reported as
  unsupported rather than failing the command.
- `--state merged` and `--state closed` need the per-repository surface, so
  asking for them selects it automatically.

## Related pages

- [AI pull-request review options](../notes/ai-pr-review-options.md)
- [Agent-safe retirement](agent-safe-retirement.md)
- [Change-stream workflow](change-stream-workflow.md)
- [Compatibility and known limitations](../reference/compatibility.md)
