---
description: 使用 dev-cli 進行安全 Git、worktree 與 coding-agent 協作的簡潔 checklist。
authority: project-policy
status: stable
verified_on: 2026-08-31
lang: zh-TW
---

# 最佳實務

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

把這頁當作短版 operating policy；只有遇到判斷問題時，才繼續開啟細節頁。

## Checklist

1. **從已知 base 開始。** 先 fetch，並明確指出預期的 default-branch commit 或 base branch。
2. **每條獨立變更流 (change stream) 建立一個 branch。** 不相關改動和互斥方案要分開。
3. **依變動邊界 (mutation boundary) 決定 worktree，不要依 agent 數量。** Read-only researcher 與檔案範圍明確分離的 writer 可以共用 checkout；重疊或未知範圍則要分 branch 與 worktree。
4. **指定唯一 integration owner。** 平行工作開始前，先分配 file ownership、dependency order、merge order 與最後測試責任。
5. **只從 allowlist 佈建。** 只複製明確列出且 Git 確認為 ignored 的檔案；除非 ecosystem 證明 copy 安全，否則重新安裝 dependencies。
6. **分開授權 export。** Local worktree include 不代表 off-machine permission。使用獨立 portable-file allowlist、先看 report、透過獨立管道驗證 target UUID，且不能讓 `--yes` 隱含 replacement。
7. **每個 branch 同時只有一位 writer。** 跨機器時先 push branch 並移交 ownership，另一位 writer 才 resume。
8. **Handoff 前建立 checkpoint。** Commit、push 可復原工作並留下具體 `--next`；跨機器工作優先使用 feature branch 上的暫時 `wip:` commit，不要用 stash。
9. **檢查整合後的結果。** 每個 worker 的局部測試不夠；組合所有變更後要跑完整相關 suite。
10. **只有可復原時才清理。** Dirty、untracked、unpushed 或 locked worktree 都不能移除；移除 worktree 不得偷偷刪 branch。
11. **標明 authority。** Git 語義、現行產品行為、experimental harness 行為、project policy 與歷史建議必須分開。

## 選擇最小且安全的 topology

| 情境 | Branch | Worktree | 協調方式 |
|---|---:|---:|---|
| 一個小型可逆變更 | 0 或 1 | 目前 checkout | 一位 writer |
| 同一 feature、檔案互不重疊 | 1 | 1 | pane/subagent 加明確 file owner |
| Research 加 implementation | 1 | 1 | read-only researcher、一位 writer |
| 互斥實作方案 | 每方案一個 | 每方案一個 | 比較後留一個，其餘安全移除 |
| 未知或重疊寫入 | 每位 writer／stream 一個 | 每位 writer／stream 一個 | integration owner 與有順序的 merges |
| 大型可重跑分析 | 依寫入邊界決定 | 必要處隔離 | Dynamic Workflow 加 verification stage |

Worktree 不是 coordination system。它避免 working-directory collision，但不會分配 ownership，也不隔離 port、database、cache、hook、shared ref 或 deployment target。

## dev-cli 預設循環

```bash
dev start api --task "refresh tokens" --base main
dev park --next "add expiry regression test" --wip
dev resume "refresh tokens"
dev done --ff
dev sweep
```

需要 review 或 CI 負責整合時，以 `dev done --pr` 取代 `--ff`。建立 request 不會把 task 標成 DONE。

## GitHub Flow 預設循環

現行 GitHub Flow 是 branch → commits/push → pull request → review → merge → delete branch。Deployment policy 由專案自行定義，不是現代 GitHub Flow 的必要步驟。

請看 [GitHub Flow](git/github-flow.md)與[Branch、commit 與 pull request](git/branches-commits-prs.md)。

## 平行 agent 預設規則

> **每條變更流一個 worktree；每個協作 agent 一個 pane。**

選擇 subagent、Agent view、agent team 或 Dynamic Workflow 前，先看[平行工作決策指南](claude/parallel-work-chooser.md)。

## 來源

- [現行 GitHub Flow](https://docs.github.com/en/get-started/using-github/github-flow)
- [Git worktree 文件](https://git-scm.com/docs/git-worktree)
- [Claude Code：平行執行 agents](https://code.claude.com/docs/en/agents)
- [`internal/help/topics/agents.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/agents.md)
