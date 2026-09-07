---
description: 初始化完整的 submodule 工作區、選擇任務分支，並由內往外回收子 repository。
authority: project
status: evolving
verified_on: 2026-09-06
tested_with: Git 2.55.0
lang: zh-TW
---

# Submodule 工作區

Superproject 以 gitlink 記錄各 submodule 的 commit ID。完整工作區包含外層
worktree 與各自獨立的子 repository checkout，不會帶入原 checkout 的未提交內容。

## 初始化與開發

```bash
dev repo clone owner/dotfiles-all
dev start dotfiles-all --task platform-change --base main \
  --submodule dotfiles --submodule dotfiles-windows
# 在新的 managed worktree 中：
dev submodule status
dev submodule develop another/module --submodule-base another/module=origin/main
```

Clone 與建立 worktree 預設遞迴初始化。子 repo 停在外層 gitlink 指定的 commit，
呈現 detached HEAD，這是可重現的起點。選中的子 repo 才從同一個 commit 建立
與外層同名的 task branch；不覆寫或默默接管既有分支。Direct／branch-only
模式不自動切換子 repo 分支。

```toml
[submodules]
init = "recursive" # 或 "none"
develop = ["dotfiles", "dotfiles-windows"]
```

全域設定與 `.dev-cli/config.toml` 都支援這些欄位。明確旗標／wizard 選擇優先於
repo 設定，再優先於全域設定；clone 取得外層後才讀取其 repo 設定。
`--submodules=none` 略過初始化；`--no-provision` 只略過環境設定，不略過 Git
初始化。工具不會自動把子 repo 推進到最新 main。

`dev submodule init --dry-run` 僅做本地檢查。實際 init 可連線取得缺少的
checkout，但保留既有 HEAD 與 dirty 內容；未初始化卻非空的目錄會被阻擋。
初始化失敗會保留部分成果、回傳失敗，並停止 runtime handoff／agent dispatch。
先重試初始化，再繼續環境設定。

## 由內而外整合

跨平台修改先完成各子 repo 的 commit、整合與 push，再一起 stage 對應 gitlinks
並提交外層。依 `dotfiles-all` 的規則，同時修改兩個平台時，用一個外層 pointer
commit 記錄兩個 gitlinks 與相關跨 repo 文件。

Manager 負責驗證前提，不自動替內層 commit／push／merge。外層 `--push` 不代表
遞迴推送子 repo；gitlink 更新與衝突解決仍是明確操作。

`dev status`、`dev repo context`、`dev ls --json` 與 repository UI evidence
會顯示子 repo 狀態；`dev submodule status --json` 提供完整本地 graph。
未初始化、無法確認或讀取失敗都不等於 clean，這些讀取不會 fetch／查詢遠端。

## 暫停與回收

從目標 checkout 及 runtime 外執行：

```bash
dev park platform-change --cold --push --recursive
dev resume platform-change --fetch
# 外層 task 已整合後：
dev retire platform-change --recursive
dev sweep --merged-worktrees --recursive # 先看報告
```

Cold 要求工作已提交、推送且可重建，但不要求已合併。Retire 另要求選中的子 repo
已整合至紀錄的 base。Workspace 成員意圖以獨立版本化紀錄保存，避免舊 task TOML
寫入者抹掉新欄位；resume 依 gitlink 重建選中的任務分支。

`--recursive` 明確授權刪除工作區專屬的子 clone，包含其私有 refs／objects。
外層分支預設保留；canonical 與共享 repository 不會被連帶刪除。不提供此旗標時，
若仍有子 repo，會阻擋外層移除；`--force` 不略過內層保存證明。`dev flow` 有明確
的 managed／unmanaged checkout 遞迴動作，done 清理流程也會詢問是否包含子 repo。

變更前會以當次遠端 refs 與隔離 fetch 證明各子 repo 可由指定 origin 重建，不能
只看 origin 存在或 ahead=0。未發布 refs、reflog／unreachable 的本地獨有物件、
stash、dirty／untracked／ignored 內容、未完成 artifacts、其他 task、runtime
活動與額外子 worktree 都會阻擋。Filtered／LFS 資料、sparse／partial 布局，或
無完整保存證明的私有 Git 設定／檔案也會阻擋。要求完整遞迴證明前，先初始化缺少
的子 repo。

子 repo origin 必須符合其宣告來源；遠端失敗或 refs 在證明期間變動就停止。
Raw Git／外部 writer，以及之後發生的遠端歷史刪除，仍在 dev 合作式鎖與當次
證明的保證範圍之外。

## 中斷恢復

子 checkout 由深至淺移到同檔案系統的私有暫存區，原處保留空 gitlink 目錄，再以
不帶 force 的 Git 操作移除外層。不使用 `submodule deinit` 改寫共享設定。

每次移動都有同步保存的 journal。外層移除失敗時，只在原位置未被占用的情況下
恢復；程序中斷或路徑被重用則保留暫存資料，並回報精確的 `journal.json`：

```bash
dev submodule recover /exact/.dev-submodule-retirement-ID/journal.json --dry-run
dev submodule recover /exact/.dev-submodule-retirement-ID/journal.json
```

Recover 用於恢復，不沿用舊證明刪除資料。外層 checkout 已移除時，只有保留的分支
仍指向紀錄的 commit 才能重建；被重用或非空的恢復位置會阻擋。恢復後重新執行一般
遞迴清理，取得新的計畫與遠端證明。

升級後重新載入 shell integration，才能使用 done 後的遞迴 handoff；舊 wrapper
會拒絕新動作，不會猜測如何執行。
