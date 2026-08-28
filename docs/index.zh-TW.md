---
description: 使用 dev-cli 保存 Git 變更流，讓 worktree、runtime 與 coding agent 都能安全重建。
authority: project
status: stable
verified_on: 2026-08-28
lang: zh-TW
---

# dev-cli

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev` 是連接 Git、worktree、forge、terminal runtime 與 coding agent 的輕量協調層。它把工作的持久身分留在 Git，另記錄足以讓你今天關閉 terminal、明天接續同一條變更流 (change stream) 的意圖。

!!! tip "先記住一條規則"
    **每條獨立變更流一個 branch；每個變動邊界 (mutation boundary) 一個 worktree；每個協作 agent 一個 pane。**

## 它解決什麼問題

四個層次很容易被誤當成同一件事；`dev` 明確分開責任：

| 層次 | 負責內容 | 能否安全重建？ |
|---|---|---|
| Git remote 與 branch | 持久 code history 與 handoff | 不行——這是 source of truth |
| Git worktree | branch 的一份 checkout | commits 可復原後可以 |
| Herdr、tmux、Zellij 或 shell | 每台 host 的即時執行環境 (runtime) | 可以 |
| `dev` task registry | state、owner、next action 與重建 metadata | 可由 Git 加上人類意圖重建 |

因此，關閉 runtime 只會關掉 session，不會結束工作。進入 COLD 會移除 checkout，不會刪除 branch。只有完成整合後，task 才是 DONE。

## 選擇閱讀路徑

- **第一次使用：**從[快速開始](getting-started.md)安裝並跑完一個 task。
- **日常工作：**遵循[變更流工作流程](guides/change-stream-workflow.md)。
- **同時使用多個 agent：**先看[平行工作決策指南](claude/parallel-work-chooser.md)。
- **新 worktree 缺少 `.env` 或 dependencies：**看[Worktree 與環境佈建](guides/worktrees-provisioning.md)。
- **只需要短版規則：**使用[最佳實務 checklist](best-practices.md)。
- **需要精確 flags：**開啟[命令與設定](reference/commands-config.md)。

## 網站結構

- **核心概念**定義 lifecycle、ownership boundary 與共同詞彙。
- **使用 dev-cli**提供 task、worktree、runtime 與 TUI 的 recipes。
- **Git 與 GitHub**分開目前的 GitHub Flow、Git 語義和歷史版本。
- **Claude Code**說明公開的 agentic harness、parallel primitives 與限制。
- **參考資料**保存命令、相容性、來源與 freshness。
- **筆記**提供未來研究可持續擴充的固定位置與模板。

## 適合 LLM 的輸出

每頁都有 copy-to-LLM 操作。建置也會發布：

- [`llms.txt`](https://daviddwlee84.github.io/dev-cli/llms.txt)：附描述的雙語頁面索引。
- [`llms-full.txt`](https://daviddwlee84.github.io/dev-cli/llms-full.txt)：完整英文與繁中內容。

兩者都由同一份 nav 與 page metadata 產生，並由 CI 檢查。

## 來源政策

專案行為要回到 code 與 tests 驗證；Git 與 GitHub 行為以目前官方文件為準；Claude Code 行為以目前 Anthropic 文件為準，experimental 或 research preview 必須明示；歷史資料永遠不能冒充現行 workflow。

完整規則與查核日期請看[來源與時效](reference/sources-freshness.md)。
