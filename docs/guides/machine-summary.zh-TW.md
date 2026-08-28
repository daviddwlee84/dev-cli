---
description: 產生目前機器上 repositories、Tries、tasks、worktrees、runtimes 與 recovery risks 的完整 snapshot。
authority: project
status: stable
verified_on: 2026-08-28
lang: zh-TW
---

# Machine summary

!!! note "術語規則"
    CLI flag、Git、JSON、runtime、snapshot 與 AI agent 保留英文，避免自行創造不穩定譯名。

`dev summary` 回答「這台機器目前有什麼？」預設輸出 Markdown，不會呼叫 AI
agent，也不會持久保存另一份 Git 或 runtime state。

```bash
dev summary
dev summary --attention
dev summary --detail compact --no-runtime
dev summary --recent-commits 3 --sizes
dev summary --json
```

預設 `auto` detail 會展開含 dirty/conflicted checkout、live session 或 HOT/WARM
task 的 projects。其他 repositories 與 Tries 保留在 compact index，顯示 branch、
Git status、recent activity、recovery risk 與 latest commit subject。

`--attention` 選出 active work，以及 topology error、missing/prunable checkout、
沒有 remote、含 local-only branch 的 repositories。Auto detail 會展開所有匹配
項目；`--detail full` 展開全部，明確指定
`--detail compact` 時則全部維持單行。

預設同時包含 durable repositories 與本機 present 的 active/deprecated Tries，
non-Git experiment 只呈現其可支援的較小 facts 集合。`--include-history` 會加入
archived、evicted 與 graduated records。尚未 clone 的 remote-only repositories
不屬於這個 local machine snapshot。

## Structured output 與 performance

`--json` 不受 Markdown detail 影響，固定輸出完整 selected snapshot。Schema
version 1 包含 collection capabilities、aggregate counts、project identity、current
Git/recovery facts、checkouts、tasks、sessions、recent commits、optional size、
attention reasons 與 warnings。

Runtime sessions 只查詢一次，再提供 repository 與 Try collection 共用。
`--no-runtime` 會跳過查詢並將 `runtime_collected` 標為 false，避免把未查詢誤認為
沒有 session。Size scanning 必須明確使用 `--sizes`，並沿用既有 disk-usage cache。

依問題選擇三種 context surface：

```text
dev summary       目前 machine-wide snapshot
dev journal       calendar-day range 內的 activity
dev repo context  單一 repository 的完整 context
```
