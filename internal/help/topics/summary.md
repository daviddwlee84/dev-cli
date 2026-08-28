# Machine summary

Generate one current snapshot of repositories, present Tries, worktrees, tasks,
runtimes and recovery risks for a person or another agent.

```bash
dev summary
dev summary --attention
dev summary --detail compact --no-runtime
dev summary --json
```

The default `auto` Markdown expands dirty/live/HOT/WARM projects and keeps quiet
projects in a compact index. `--attention` also selects missing checkouts,
topology errors, no-remote repositories and local-only branches.

Use `dev journal` for a date range and `dev repo context` for one repository.
The command generates context only; it does not launch an agent or store the
rendered summary.
