# Pull requests

How to see what is waiting on you and whether its expected branch is healthy
locally. `dev pr` is inventory only; generic agent handoff lives under
`dev prompt`.

## What `dev pr` is

`dev pr list` is a read-only inbox over available forge CLIs. It lists requests
the user opened and requests awaiting their review, then joins a reported head
branch to local task/checkout evidence where possible.

It does not approve, merge, comment, close, resume, retire, or remove anything.
`--actions` and JSON contain command strings for operator review only. Current
comment actions use a generic `'...'` body; they do not emit vendor trigger
phrases.

## Account and local surfaces

```bash
dev pr list --scope account
dev pr list --scope local
dev pr list                         # both (default)
```

- Personal account inventory asks for author and reviewer separately.
- Personal local inventory may make one paginated query per requested role for
  every selected repository—up to two per repository by default.
- Local scope uses repositories carrying a `dev` task; `--all-repos` widens it.
- `--repo owner/name`, `provider:owner/name`, or a forge URL filters both account
  rows and local query targets.
- `--state merged|closed|all` cannot use account search, so account/all narrows
  to local. JSON reports the effective `"local"` scope.

GitHub account search rows have `detail: "summary"` and cannot report a head
branch, review decision, or checks. GitHub per-repository rows are `full`.
GitLab account/repository lists report full branch/merge detail but not checks or
a normalized review decision. An absent field means the surface did not report
it, not that its real value is empty.

## Reading local health

`--linked` selects only rows whose expected task branch was proven checked out.
In JSON, optional `local` keeps intent and observation separate:

- `expected_branch` is task intent; `live_branch` is observed status;
- `checkout_exists`, `worktree_registered`, and `status_available` are separate;
- `branch_checked_out` requires all three plus matching expected/live branches;
- optional `status_error` explains missing/unavailable evidence;
- optional `git` (`dirty`, `ahead`, `behind`, `upstream`) appears only for a
  proven live expected branch.

A missing, cold, unregistered, or unreadable checkout therefore never looks
clean through zero values.

## Acting on one

```bash
dev pr list --actions
dev pr list --json | jq '.pull_requests[].actions'
```

Approving and merging remain operator decisions. A merged request is evidence,
not permission to retire its worktree:

```bash
dev pr list --scope local --state merged   # candidates only
dev sweep --merged-worktrees               # local proof; report before apply
```

Squash merge breaks ordinary ancestry equivalence, so a forge answer cannot
replace containment, artifact, task, cleanliness, or runtime checks.

## Structured output

`dev pr list --json` emits a schema-versioned object with:

- `schema_version: 1`, `generated_at`, effective `scope`, `state`, `roles`, and
  optional normalized `repositories`;
- `providers`, which must be read before calling an empty inbox complete;
- `pull_requests`, each with provider fields, optional local health, and action
  strings.

Schema version 1 is add-only: existing field names and meanings remain stable.

## Generic prompt handoff

For deterministic triage or closeout context, use the generic recipes:

```bash
dev prompt render pr-triage
dev prompt run pr-triage --agent my-agent
dev prompt open workspace-closeout . --agent my-agent
```

Run `dev help prompts` for render/run/open choice, nested host configuration,
transport, TTY/runtime, permissions, and closeout safety. There is no
`dev pr prompt` command.

## Signed out and unsupported providers

`gh` and `glab` are optional and independent. A signed-out provider is reported
with the exact login command while another ready provider still lists. If none
is authenticated, the command fails with remediation. `dev doctor` reports the
same state. Azure DevOps pull-request inventory is not implemented and is
reported unsupported rather than poisoning other results.

## Scheduling

There is no daemon or scheduler. `dev pr list` is a stateless query; use an
external scheduler when recurrence is wanted.
