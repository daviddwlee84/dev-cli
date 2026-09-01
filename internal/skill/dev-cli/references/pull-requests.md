# Pull requests

Read before using `dev pr`, or before advising on which pull requests can be
merged, reviewed, or whose worktree can be retired.

## What `dev pr` is

A read-only inbox over the forge CLIs. It lists requests the user opened and
requests awaiting their review, and joins them to local checkouts where it can.

`dev pr` **mutates nothing**. It does not approve, merge, comment, close, or
remove a worktree. It renders the commands for those and leaves them to the
user. Do not run an action string on the user's behalf unless they asked for
that specific action.

## The two surfaces, and why a field can be missing

The providers expose two listings with different costs and different fields.
This distinction is the most common source of wrong conclusions.

| | `--scope account` | `--scope local` |
|---|---|---|
| cost | 2 calls total | 1 call per repository |
| covers | every repo the account can see | repos with a `dev` task (`--all-repos` widens) |
| `head_branch` | **absent** | present |
| `review_decision` | **absent** | present |
| `checks` | **absent** | present (GitHub only) |
| states | open only | open, merged, closed, all |

`--scope all` is the default and runs both, with per-repository rows upgrading
account rows for the same request.

Every row carries `detail`: `"summary"` from the account search, `"full"` from
the per-repository listing.

**An absent field on a `summary` row means the surface could not report it, not
that the value is empty.** Never tell the user "no reviews requested" or
"checks are not configured" from a `summary` row. Say the data needs
`dev pr list --scope local --repo <owner/name>` and offer to run it.

GitLab rows are always `full` but never carry `checks`: pipeline status is only
on GitLab's single-MR endpoint, and `dev` does not fan out per request.

## Retirement: report, never infer

`dev pr` can say a request merged. That is **not** sufficient grounds to remove
a worktree, and `dev sweep` deliberately does not consult it.

A squash merge produces a commit that is not an ancestor of the local branch,
so "the forge says merged" cannot prove the work is recoverable from the
remote. `dev sweep --merged-worktrees` proves containment locally with
`git merge-base --is-ancestor`, and `dev done --merged` requires an explicit
`--confirm-squash <commit>` attestation from the operator.

So the correct sequence is:

```bash
dev pr list --scope local --state merged   # find candidates
dev sweep --merged-worktrees               # prove and report, then --apply
```

Never recommend removing a checkout because a pull request is merged. Never
recommend removing one whose `local.dirty` is true.

## Structured output

`dev pr list --json` emits an object, not an array, because a consumer must be
able to tell "no requests" from "GitLab was signed out":

```json
{
  "generated_at": "...", "scope": "all", "state": "open",
  "providers":     [{"forge": "gitlab", "status": "unauthenticated", "action": "run `glab auth login ...`"}],
  "pull_requests": [{"forge": "github", "repo": "o/n", "number": 12, "detail": "full",
                     "head_branch": "feat/x", "review_decision": "approved", "checks": "passing",
                     "local": {"task_id": "...", "checkout": "...", "dirty": false},
                     "actions": {"merge": "gh pr merge 12 --repo o/n --squash"}}]
}
```

Always read `providers` before reporting the queue is empty. A signed-out
provider contributes nothing and says so there.

Fields are added over time and never renamed or removed; treat this as a
compatibility contract like `dev ls --json`.

## Prompts and agents

```bash
dev pr prompt [triage|review|retire]   # render to stdout
dev pr prompt retire --agent claude    # hand to a configured command
```

`dev` renders a prompt containing the live queue and starts a command from the
`[[agent]]` section. It does not read the reply and does not iterate — there is
no agentic loop inside `dev`.

There is **no built-in agent**. With none configured, `--agent` fails with a
paste-ready example. Do not add an `[[agent]]` entry to the user's config
unless they asked; suggest it and show the block.

`[[agent]]` is host configuration. It is denied in a repository's
`.dev-cli/config.toml`, because it names a command `dev` executes.

Prefer `command = [...]` (an argv, no shell) over `run = "..."`. A prompt is
never interpolated into a shell string — these prompts embed shell commands by
design — which is why `input = "argv"` requires `command`.

## Scheduling

There is no daemon or scheduler in `dev`, deliberately. `dev pr list` is a
plain query; recurrence belongs to cron, launchd, or the agent harness.

## When a provider is signed out

`gh` and `glab` are optional and independent. A signed-out one is reported as a
provider status with the exact login command; the other still lists. If none is
authenticated, `dev pr` fails with that remediation rather than a provider
error. `dev doctor` reports the same state.
