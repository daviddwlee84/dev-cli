# Pull requests

How to see what is waiting on you, and hand it to an agent you chose.

## Why this exists

Opening a pull request is usually the end of a worktree's useful life, but
nothing local says so. The branch is pushed, the request is open, and the
checkout sits there. Meanwhile requests accumulate in two directions at once:
ones you opened and are waiting on, and ones waiting on your review.

`dev pr` puts both in one list and, where `dev` already has a checkout for the
branch, says which one. It changes nothing.

## Two surfaces

The providers expose two different listings, and the difference matters:

```bash
dev pr list --scope account   # gh search prs: 2 calls for your whole account
dev pr list --scope local     # gh pr list: 1 call per engaged repository
dev pr list                   # both (default); local rows upgrade account rows
```

The account-wide search is cheap regardless of how many repositories you have,
but it **cannot report a head branch, a review decision, or check status**. So
its rows can never be matched to a worktree, and a `—` in those columns means
"this surface does not know", not "there is nothing".

The per-repository listing reports all of it, at one call per repository. To
keep that affordable it asks only about repositories `dev` is engaged with —
ones carrying a task. `--all-repos` widens it to everything under
`paths.scan_roots`, which on a populated machine is a lot of calls.

`--state merged` and `--state closed` only exist on the per-repository surface,
so asking for them selects it automatically.

## Reading the table

```text
PR                      TITLE          ROLE    STATE   CHECKS  REVIEW    LOCAL   UPDATED
github:owner/repo#12    Add retry      mine    open    pass    approved  ~/wt/…  2026-09-01
```

- `ROLE` — `mine` if you opened it, `review` if it asked for your review, both if both.
- `CHECKS` — `—` means the surface could not report it.
- `LOCAL` — the checkout `dev` has for that head branch.

## Acting on one

`dev` prints commands and never runs them:

```bash
dev pr list --actions          # the gh/glab commands for each request
dev pr list --json | jq '.pull_requests[].actions'
```

Approving and merging are decisions, not conveniences, so they stay yours.

## Retiring the worktree behind a merged request

`dev pr` reports that a request merged. It does not retire anything, and
`dev sweep` does not consult it:

```bash
dev pr list --scope local --state merged   # which checkouts are finished
dev sweep --merged-worktrees               # re-checks locally, reports first
```

The reason is squash merges. When a request is squashed the merged commit is
not an ancestor of anything local, so "the forge says merged" is not proof the
work is recoverable. `dev sweep` proves containment with `git merge-base
--is-ancestor` instead, and `dev done --merged` requires an explicit
`--confirm-squash` attestation. Treat the PR list as the prompt to look, not as
permission to delete.

## Handing the queue to an agent

```bash
dev pr prompt                  # render the triage prompt to stdout
dev pr prompt retire           # which checkouts can be closed
dev pr prompt review           # work through your review queue
dev pr prompt --agent claude   # hand it to a configured command
```

There is no built-in agent. `dev` renders a prompt and starts a command you
define; it does not read the reply and does not iterate:

```toml
[[agent]]
name = "claude"
command = ["claude", "-p"]
default = true
```

The prompt arrives on the agent's stdin by default. Set `input = "file"` and
put `{{prompt_file}}` in the command for a tool that wants a path. `run = "…"`
takes a shell line instead of an argv, but a prompt is never spliced into it —
these prompts contain shell commands, and interpolating one would be a command
injection. That is why `input = "argv"` requires the `command` form.

`[[agent]]` is host configuration only. A repository cannot define one.

## Scheduling

There is no daemon and no built-in scheduler. `dev pr list` is a plain query,
so recurrence belongs to whatever you already use:

```bash
*/30 * * * * dev pr list --json > ~/.cache/pr-inbox.json
```

## Signed out

`gh` and `glab` are optional and independent. A signed-out provider is reported
as a note under the table with the exact command to fix it, and the other
provider still lists. `dev doctor` reports the same thing before you start.
