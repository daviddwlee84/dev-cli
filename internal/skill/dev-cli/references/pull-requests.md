# Pull requests

Read before using `dev pr`, or before advising on which requests need review or
which local checkout might correspond to one. Prompt handoff is generic and is
documented in `references/prompt-handoffs.md`.

## Read-only inventory

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

## Explicit PR workflow

```bash
dev pr list --scope repo --repo github:owner/api --page-size 50 --json
dev pr list --scope repo --repo gitlab:group/api --role author,reviewer
dev pr view https://github.com/owner/api/pull/12 --json
dev pr diff https://github.com/owner/api/pull/12
dev pr checkout https://github.com/owner/api/pull/12 --try --dry-run
dev pr merge https://github.com/owner/api/pull/12 --squash --dry-run
```

Repository scope defaults to all authors and requires one provider-qualified
repository or repository URL. It adds `pagination` without changing schema-v1
fields. Continue with its opaque `--cursor` and the same account, repository,
state, role and page size. Related author/reviewer queries run on the provider;
missing rows from the first all-request page are not treated as an empty inbox.
The REMOTE tree loads children on Space, defaults to pages of 50, and switches
its automatic scope to related requests above 50 open requests. Scope, loaded
count, overall count/lower bound, freshness and more results remain explicit.
Filtering and loaded summaries do not fetch additional requests.

`view` observes current checks, reviews, size and readiness. `diff` uses optional
diffnav in an interactive terminal; `--no-pager` supports patch pipes and `--web`
opens the provider. The diff is live and independent of merge authority.
Control sequences are neutralized for terminal output. Omitted/oversized/binary
contents remain visible as limitations; noninteractive incomplete patch output
fails rather than appearing complete.

Checkout prefers an exact existing checkout and retains its dirty files or
changed local tip. Otherwise it creates a worktree from the verified request
head, with no task or fork. Use `--repo` for ambiguous local clones and
`--source-remote` for an exact existing base transport. Without a local match,
choose `--try` for an independent catalog-backed clone or `--clone [--path PATH]`
for a project clone plus worktree. Interactive acquisition suggests Try;
scripts must choose. Project provisioning, ignored-file copying, hooks,
dependency installation and submodule initialization require `--provision` and
existing trust checks. `--no-open` and JSON leave runtime navigation to the caller.

`merge <URL> --squash` previews and confirms one immediate merge bound to the
reviewed head, account and repository. Apply reloads provider readiness.
Queue/auto-merge, GitLab trains and unknown policy are not bypassed by dev; open
the provider UI. There is no admin override. Provider branch-deletion policy is
shown before merge. Private attempt/result receipts under `<state_dir>/pr-merges/`
retain uncertain writes; reconciliation uses reads and never repeats the merge.

`--sync-base ff-only|rebase` runs only after confirmed merge. Alternatively use
`dev pr sync-base <URL> --repo <local-repo> --strategy ff-only|rebase`.
Synchronization fetches, then reviews a fresh plan for the actual base checkout;
rebase is explicit, dirty/occupied/changed targets block, and there is no implicit
switch, stash, reset or cleanup. Remote merge and local sync have independent
results. `--yes` approves a reviewed noninteractive action; `--json` alone does not.

Native gh-dash is a dashboard handoff to an already installed `dlvhdr/gh-dash`,
retaining native settings/keybindings and the selected GitHub host/repository
context. It does not install or rewrite configuration, or promise a selected PR.

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
dev work sweep --merged-worktrees               # prove/report; apply only after approval
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
dev agent prompt render pr-triage
dev agent prompt run pr-triage --agent my-agent
dev agent prompt open pr-triage --agent my-agent
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
