---
description: 以持久 Git history、小型 intent registry、可丟棄 worktree 與可替換 runtime 理解 dev-cli。
authority: project
status: stable
verified_on: 2026-08-28
lang: zh-TW
---

# 心智模型與生命週期

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev` 把一個工作單位視為變更流 (change stream)，而它的持久身分是 Git branch。Checkout 與執行環境 (runtime) 都只是可替換的投影。

## 四種責任

```text
Git remote + branch  持久 history 與跨機器 handoff
        ↓
worktree             本機 files、index 與 checked-out HEAD
        ↓
runtime              Herdr workspace、tmux session 或目前 shell
        ↓
dev registry         人類意圖與重建 metadata
```

Registry 保存未來要能採取行動的資訊：repository 與 branch identity、base、checkout path、lifecycle state、owner host、next action、notes/tags、runtime handle、可選 agent session 與 timestamps。即時 Git status、ahead/behind 與 runtime availability 會重新推導，不會被當成權威 cache。

## 四個 lifecycle state

| State | Git 與 checkout | Runtime | 意義 |
|---|---|---|---|
| 🔥 `hot` | branch 加 checkout | 開啟 | 正在處理 |
| 🌤 `warm` | 保留 branch 與 checkout | 關閉 | 幾天內會回來 |
| ❄️ `cold` | branch 已 commit、push；worktree 不存在 | 無 | 已暫停，可在別處重建 |
| ✅ `done` | 已整合 | 無 | 等待 cleanup 回收 entry |

```text
              dev start
                  │
                  ▼
               HOT ───── dev done --ff ─────► DONE
                │ ▲
       dev park │ │ dev resume
                ▼ │
               WARM
                │ ▲
 --cold --push  │ │ dev resume --fetch
                ▼ │
               COLD
```

`dev done --pr` 刻意不是 HOT → DONE transition。它只會發布 branch 供 review，並在尚未確認整合前保留原 state 與 cleanup 狀態。

## Checkout mode

Task 記錄的是意圖，不一定要有 linked worktree。

| Mode | 適用情境 | Lifecycle 限制 |
|---|---|---|
| direct | canonical checkout 中的一個小變更 | HOT ↔ WARM；canonical checkout 不能移除 |
| branch-only | 只需要 history isolation，不需要第二個 directory | push 且 canonical checkout 切離後可進 COLD |
| worktree | 獨立寫入、平行 stream、長期 feature | 完整 lifecycle |

選擇 mode 的依據是碰撞與復原需求，不是儀式感。

## Runtime 刻意是可丟失的

`runtime.Runtime` 提供 `Open`、`Close`、`List` 與 `Annotate`。`auto` 會依序選 Herdr、tmux、Zellij，最後使用永遠可用的 `none` backend。關閉任何 backend 都不能移除 checkout、branch 或 task entry。

因此 reboot、關閉 multiplexer 或更換 runtime backend 都不代表放棄工作。`dev sweep` 能比較 registry 與即時 Git/runtime facts 並回報 drift。

## 跨機器 handoff

Branch 是傳輸邊界：

```bash
# machine A
dev park --cold --push

# machine B
dev resume <task> --fetch
```

每個 branch 同時只有一位 writer。若兩台機器或兩個 agent 必須同時修改同一 feature，應拆成不同 change stream，再於之後整合。

## 來源

- [`internal/task/task.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/task/task.go)
- [`internal/runtime/runtime.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/runtime.go)
- [`internal/help/topics/parking.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/parking.md)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
