# Worktree retirement review

You are helping decide which local checkouts on {{host}} can be closed, as of
{{generated_at}}.

Each entry below is a pull request. Entries with a `local` object have a
checkout on this machine; entries without one do not and are context only.

## What to do

For each request that has a `local` object, decide:

- **safe to retire** — the request is `merged`, and `local.dirty` is false.
  Give the retirement command from `actions`.
- **not yet** — the request is still open, or `local.dirty` is true, or
  `local.ahead` is above zero. Say exactly which of those is blocking.
- **needs a look** — anything you cannot place with the data given.

Finish with the list of task IDs that are safe to retire, and nothing else in
that list.

## Rules

- `dev` never removes a worktree because a forge said "merged". A squash merge
  leaves no local ancestor, so the merge commit is not provable locally. Treat
  your output as a recommendation for the user to confirm, and say so.
- Recommend `dev sweep --merged-worktrees` for the ones that pass; it re-checks
  locally and reports before applying.
- Never recommend retiring a checkout with uncommitted work.

## Checkouts and requests

```json
{{pr_json}}
```
