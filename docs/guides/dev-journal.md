---
description: Generate daily, weekly, or long-range development journals for people, scripts, and AI agents.
authority: project
status: stable
verified_on: 2026-08-28
---

# Development journal

`dev journal` derives a report from repositories already visible under the
configured scan roots. It writes Markdown by default, makes no AI request, and
does not persist the generated report.

```bash
dev journal                         # today, current Git identity
dev journal --since 7d --metrics
dev journal --since 3mo --granularity branch
dev journal --author teammate@example.com --since 30d
dev journal --since 7d --json
```

The default `auto` granularity shows commit details when the report is small.
After 100 commits it retains complete repository and branch totals, shows the
newest 100 details, and reports how many were omitted. Use
`--max-commits 0` or explicit `--granularity commit` for an unlimited report.

## Evidence and attribution

Git author timestamps and emails are the historical authority. Current-user
reports may supplement them with source-separated session/WakaTime totals,
task title/state/next action, and dirty linked worktrees whose latest existing
changed-file mtime falls inside the selected calendar-day range. These current
snapshots are labelled as heuristics and are not added to committed metrics.

Branch names are reconstructed from current refs, preferring an existing dev
task's explicit base/branch relationship. Git cannot recover the original name
of every deleted branch without a separate event log, so surviving-ref and
unattributed results are marked as best effort.

`--metrics` reports file touches, additions, deletions, and churn from commit
numstats. Binary changes count as files but do not invent line counts.

## Agent and script composition

Markdown is suitable as direct context:

```bash
dev journal --since 7d | opencode run "summarize this as a weekly report"
```

`--json` emits schema version 1 with the window, authors, full aggregate totals,
shown/omitted commit counts, repository and branch records, optional metrics,
current context, activity evidence, and collection warnings.
