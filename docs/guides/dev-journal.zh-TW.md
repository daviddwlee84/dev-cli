---
description: 產生日、週或長時間範圍的 development journal，供人、script 與 AI agent 使用。
authority: project
status: stable
verified_on: 2026-08-28
lang: zh-TW
---

# Development journal

!!! note "術語規則"
    CLI flag、Git、JSON、AI agent 與產品名稱保留英文，避免自行創造不穩定譯名。

`dev journal` 從 configured scan roots 下已可見的 repositories 推導報告。預設輸出
Markdown，不會呼叫 AI，也不會持久保存生成後的報告。

```bash
dev journal                         # 今天、目前 Git identity
dev journal --since 7d --metrics
dev journal --since 3mo --granularity branch
dev journal --author teammate@example.com --since 30d
dev journal --since 7d --json
```

預設 `auto` granularity 會在報告較小時展開 commit details。超過 100 commits
時，仍以完整資料計算 repository 與 branch totals，只顯示最新 100 筆並標明省略
數量。使用 `--max-commits 0` 或明確的 `--granularity commit` 可輸出全部。

## Evidence 與 attribution

Git author timestamp 與 email 是歷史資料 authority。目前使用者的報告可額外包含
分開呈現的 session/WakaTime totals、task title/state/next action，以及最新 changed
file mtime 落在 calendar-day range 內的 dirty linked worktrees。這些 current
snapshots 會標成 heuristic，不會併入 committed metrics。

Branch name 由 current refs 重建，並優先使用既有 dev task 的 explicit
base/branch 關係。沒有獨立 event log 時，Git 無法復原所有已刪除 branch 的原始
名稱，因此 surviving-ref 與 unattributed 結果會標為 best effort。

`--metrics` 從 commit numstats 報告 file touches、additions、deletions 與 churn。
Binary change 會計入 files，但不會虛構 line count。

## 組合 AI agent 與 scripts

Markdown 可直接作為 context：

```bash
dev journal --since 7d | opencode run "summarize this as a weekly report"
```

`--json` 輸出 schema version 1，包含 window、authors、完整 aggregate totals、
shown/omitted commit counts、repository/branch records、optional metrics、current
context、activity evidence 與 collection warnings。
