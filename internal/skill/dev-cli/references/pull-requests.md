# Pull requests

Read before using `dev pr`, or before advising on which requests need review or
which local checkout might correspond to one. Prompt handoff is generic and is
documented in `references/prompt-handoffs.md`.

## Inventory only

`dev pr list` is a read-only inbox over available forge CLIs. It lists requests
the user authored and requests awaiting their review, then joins a reported head
branch to local task/checkout evidence where possible.

It does not approve, merge, comment, close, resume, retire, or remove anything.
Action strings are for operator review. Do not run one unless the user explicitly
requested that exact action. Current comment actions contain only a generic
`'...'` body; never claim that `dev` emits a vendor trigger phrase.

## Account and local surfaces

| | `--scope account` | `--scope local` |
|---|---|---|
| coverage | requests across the authenticated account | selected local repositories |
| role cost | author/reviewer queried separately | up to one paginated query per repository per requested role |
| repository filter | account rows are filtered by `--repo` | `--repo` targets, otherwise repos with a task (`--all-repos` widens) |
| states | open | open, merged, closed, all |

`--scope all` is the default and unions both. Author and reviewer are both used
when `--role` is omitted, so personal local collection may make two queries per
repository.

`--repo` accepts `owner/name`, `provider:owner/name`, or a forge URL and filters
both surfaces. A provider-qualified selector pins that provider.

GitHub account search rows are `detail: "summary"` and cannot report
`head_branch`, `review_decision`, or `checks`; never interpret absence as empty.
GitHub per-repository rows are full. GitLab account and repository list rows
carry branch/merge detail but not checks or normalized review decision.

Account search cannot distinguish merged from closed. A request for
merged/closed/all narrows account/all collection to local; JSON reports the
effective `"local"` scope. Never tell the user the broad account surface ran
when the payload says otherwise.

## Local health is evidence, not one dirty boolean

`--linked` means the expected branch was proven checked out. The optional
schema-v1 `local` object carries:

- task intent: `task_id`, `task_state`, `repo_path`, `checkout`,
  `expected_branch`;
- live evidence: `live_branch`, `checkout_exists`, `worktree_registered`,
  `status_available`, optional `status_error`;
- `branch_checked_out`, true only if the checkout exists, is registered, status
  succeeded, and expected/live branches match;
- optional `git` (`dirty`, `ahead`, `behind`, optional `upstream`) only for that
  proven live branch.

Do not infer clean from an absent `git` object. Do not describe a task-associated
but missing/cold/unregistered checkout as linked.

## Retirement: report, never infer

A merged request is **not** sufficient grounds to remove a worktree:

```bash
dev pr list --scope local --state merged   # find candidates
dev sweep --merged-worktrees               # prove/report; apply only after approval
```

A squash merge can leave no local ancestry proof. Forge state cannot replace
cleanliness, no in-progress operation, known base/containment, done task,
finalized/reachable artifacts, worktree identity, or runtime checks. Never
recommend cleanup from PR status alone.

If reasoning across all workspace evidence is useful, render/open the generic
`workspace-closeout` recipe, but its audit remains advisory. Read
`references/prompt-handoffs.md` and `references/agent-retirement.md` first.

## Structured output

`dev pr list --json` emits an object with:

- `schema_version: 1`, `generated_at`, effective `scope`, `state`, `roles`, and
  optional normalized `repositories`;
- `providers`, including ready/missing/signed-out/unsupported evidence;
- `pull_requests`, with provider detail, optional local health, and actions.

Always read `providers` before reporting an empty queue. Schema version 1 is
add-only: tolerate new fields and preserve existing meanings.

## Generic prompt handoff

There is no `dev pr prompt` command. Use:

```bash
dev prompt render pr-triage
dev prompt run pr-triage --agent my-agent
dev prompt open pr-triage --agent my-agent
```

The recipe accepts the same inbox filters. Read
`references/prompt-handoffs.md` before launching an agent or advising on its
configuration, permissions, TTY/runtime behavior, or closeout authority.

## Scheduling and unavailable providers

There is no daemon or scheduler. Inventory and prompt invocations are stateless;
external recurrence must invoke them afresh.

`gh` and `glab` are optional and independent. A signed-out provider is reported
with remediation while another may still contribute. If none is authenticated,
collection fails. Azure DevOps PR inventory is unsupported and should be
reported as a capability gap, not silently treated as an empty queue.
