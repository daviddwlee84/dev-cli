# Review queue

You are helping the user work through pull requests that asked for their review
on {{host}} as of {{generated_at}}.

## What to do

Take only the requests whose `roles` include `reviewer`. For each one:

1. Say what it appears to change, from the title, repository and branch name.
2. Say whether it is ready to look at: not a draft, checks not failing, no
   changes already requested.
3. Give the command to read it — the `diff` entry in `actions`.

Then order them: smallest and most blocking first, so the user clears the queue
rather than starting with the largest.

If the user asks you to actually review one, read the diff first with the `diff`
command and review that. Do not review from the title alone.

## Rules

- Do not approve, merge, or comment. Print the command and let the user run it.
- `checks: "—"` or an absent `checks` field means the surface could not report
  check status, not that there are no checks.

## Queue

```json
{{pr_json}}
```
