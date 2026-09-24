# Pull requests

How to see what is waiting on you and whether its expected branch is healthy
locally. `dev pr list` remains inventory only; generic agent handoff lives under
`dev agent prompt`.

## What `dev pr list` does

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
dev work sweep --merged-worktrees               # local proof; report before apply
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
dev agent prompt render pr-triage
dev agent prompt run pr-triage --agent my-agent
dev agent prompt open workspace-closeout . --agent my-agent
```

Run `dev help prompts` for render/run/open choice, nested host configuration,
transport, TTY/runtime, permissions, and closeout safety. There is no
`dev pr prompt` command.

## Signed out and unsupported providers

`gh` and `glab` are optional and independent. A signed-out provider is reported
with the exact login command while another ready provider still lists. If none
is authenticated, the command fails with remediation. `dev self doctor` reports the
same state. Azure DevOps pull-request inventory is not implemented and is
reported unsupported rather than poisoning other results.

## Scheduling

There is no daemon or scheduler. `dev pr list` is a stateless query; use an
external scheduler when recurrence is wanted.
