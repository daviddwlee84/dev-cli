---
description: 以變更流 ownership、明確 mutation boundary 與可替換 runtime 協調多個 coding agents。
authority: project-policy
status: stable
verified_on: 2026-08-31
lang: zh-TW
---

# 平行 Agent 與 Runtime

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

> **每條變更流 (change stream) 一個 worktree；每個協作 agent 一個 pane。**

不要只因為多了一個 agent 就增加 worktree。只有另一位獨立 writer 可能修改重疊或未知 state 時，才需要新的 mutation boundary。

## 判斷 agents 能否共用

| 平行工作 | 同一 checkout？ | 條件 |
|---|---|---|
| researcher 加 implementer | 可以 | researcher 是 read-only |
| frontend 加 backend | 通常可以 | shared schema/manifest 的 owner 明確 |
| tests 加 implementation | 通常可以 | 一位 integration owner 處理 interface change |
| 兩個已知互斥 directories | 可以 | 沒有共用 codegen/formatter output |
| 都修改同一 manifest/API | 有風險 | serialize 或拆分 streams |
| 互斥實作方案 | 不行 | 每方案獨立 branch/worktree |
| 大型 refactor 或未知範圍 | 不行 | 無法限制 overlap |
| 任一 worker 會 switch/reset/rebase `HEAD` | 不行 | Git state mutation 會影響 checkout |

Worktree 隔離 files/index/HEAD，不隔離 port、database、cache、hook、credential、queue、generated artifact 或 deployment target；這些資源要另外分配。

## 同一 feature 的 topology

```text
一條 change stream / branch / dev worktree
                    │
             一個 runtime workspace
          ┌─────────┼─────────┐
        pane A     pane B     pane C
        agent      agent      tests/review
```

這個 topology 保留單一 integration history，也避免合併 execution artifacts。每位 writer 都要有 path/symbol contract，並指定最後負責 combined test 的人或 agent。

在 `dev` worktree 內，直接於現有 checkout 啟動 agents。不要自動要求 Claude Code 再建立巢狀 worktree；外層已經提供 mutation boundary。

## 互斥或獨立工作的 topology

```bash
dev wt create exp/jwt --base main
dev wt create exp/session --base main
dev wt create exp/oauth --base main
```

每個方案使用自己的 branch 與 worktree。比較結果後刻意整合一個；其餘 worktree 只有在有價值的 commits 或 notes 可復原後才移除。

## Runtime 選擇

`runtime.Select("auto")` 依序選擇：

1. **Herdr：**binary 與 server 都可用時使用。它能把 linked worktree 呈現為 grouped workspace，並回報 agent activity/session ID。
2. **tmux：**已安裝時使用。它建立以 checkout 為 root 的 named session，並在 user option 保存 display metadata。
3. **Zellij：**有相容安裝時使用。它會開啟或 focus 以 checkout 為 root 的 session。
4. **none：**其他情況都可用。核心 Git/task/worktree 行為不受影響，shell integration 負責切換 directory。

Runtime 只開啟與顯示 checkout；branch/worktree lifecycle 由 `dev` 管理，不由 Herdr 或 tmux 管理。關閉 runtime 不會刪除 task 或 checkout。

若要在新建的獨立 worktree 執行一個已 review 的 shell command，Herdr 支援直接
one-liner：

```bash
dev start api --task "token refresh" --base main --run 'codex' --focus
```

`--run` 只接受建立 first-class Herdr worktree 時回傳的 exact root pane。Reuse、
fallback、缺少 pane identity、其他 runtime，以及 direct/branch-only modes 都會
fail closed。Dispatch 失敗時 task 與可用 worktree 仍會保留。`--focus` 只在成功
dispatch 後獨立控制轉跳；dev 不等待 command exit status。

## 協調 contract

啟動 parallel writers 前先記錄：

- shared goal 與 acceptance test；
- 每位 worker 的 files/symbols，以及不可碰的 shared surfaces；
- dependencies 與 blocked work 的開始條件；
- worker 是否能執行改變 `HEAD` 的 Git 操作；
- merge/integration order；
- integration owner 與完整 verification command；
- worktree、runtime、port、container 與 temporary data 的 cleanup owner。

需要獨立 context，但增加 writer 只會增加 merge cost 時，使用 read-only researcher 或 reviewer。

## 安全完成

1. 停止新的 mutation，收集每位 worker 的 summary/tests。
2. 檢查每個 diff 並保留有價值的 commits。
3. 依 dependency order 整合。
4. 由一位 owner 執行一次 formatting/codegen。
5. 在 integrated checkout 跑完整相關 suite。
6. Park 或關閉 runtime sessions。
7. 只移除 clean 且可復原的 worktree；merge status 未確認前保留 branch。

Claude-specific primitive 的選擇請接著看[平行工作決策指南](../claude/parallel-work-chooser.md)。

## 來源

- [`internal/help/topics/agents.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/agents.md)
- [`internal/runtime/runtime.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/runtime.go)
- [`internal/runtime/herdr.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/herdr.go)
- [Claude Code：平行執行 agents](https://code.claude.com/docs/en/agents)
