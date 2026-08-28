# Development journal

Generate a daily, weekly, or long-range record from Git and current development
context, suitable for direct reading or piping to another agent.

```bash
dev journal
dev journal --since 7d --metrics
dev journal --since 3mo --granularity branch
dev journal --author teammate@example.com --since 30d --json
```

The default `auto` view expands commit details and caps them at the newest 100
while retaining complete repository and branch totals. Use
`--max-commits 0` for everything.

For the current Git identity, existing session/WakaTime observations, task
intent, and dirty linked worktrees may supplement the Git history. They remain
separate and labelled; `dev journal` does not add overlapping time sources or
pretend a current dirty snapshot is exact historical evidence.

The command only generates content. It neither invokes an AI agent nor stores
the rendered report, so ordinary shell composition remains explicit:

```bash
dev journal --since 7d | opencode run "summarize this as a weekly report"
```
