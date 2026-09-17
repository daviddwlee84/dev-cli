---
description: 初始化完整的 submodule 工作區、選擇任務分支，並由內往外回收子 repository。
authority: project
status: evolving
verified_on: 2026-09-17
tested_with: Git 2.55.0
lang: zh-TW
---

# Submodule 工作區

Superproject 以 gitlink 記錄各 submodule 的 commit ID。完整工作區包含外層
worktree 與各自獨立的子 repository checkout，不會帶入原 checkout 的未提交內容。

## 把已知 repository 加為 submodule

```bash
dev submodule add                           # 選來源、路徑、checkout 模式
dev submodule add owner/library libs/library --dry-run --json
dev submodule add owner/library libs/library --checkout=pinned --ref=v1.2.0 --yes
dev repo add-as-submodule owner/library libs/library --checkout=default-branch --yes
```

來源可以是精確的已知名稱、`owner/name` 或網路 Git URL。Picker 合併本機已知 repo
的 remote 與既有 forge 快取，不隱含更新遠端清單；同名歧義須選擇或提供精確 URL。
選本機 repo 是使用其 remote URL，不搬移工作目錄、不重用其 objects。第一版不支援
local-only 來源、含憑證的 URL 或收編既有目錄；清單過時時明確執行
`dev repo remote --refresh`。

Parent 是 cwd 最近一層 checkout，包含 linked worktree 或 submodule；可用
`--parent PATH` 指定另一個 checkout。目標路徑相對於該 checkout 根目錄，預設為
來源名稱。跨越既有子 repo 的路徑會被阻擋，應明確以該子 repo 為 parent。Parent
須已有 commit 並位於 branch；既有目標／Git store、symlink、conflict、進行中的
Git operation 或修改過的 `.gitmodules` 都會阻擋。其他 staged／dirty 工作保留；
不支援 `.gitmodules` 的 filter／working-tree encoding。

Wizard 提供 `pinned`／`default-branch`，非互動預設 pinned。`--ref` 僅在 pinned
模式選擇已取得的 commit／tag／branch；省略時使用新增當次 default branch 的
commit。Default-branch 模式建立 tracking branch，不假設名稱為 `main`。兩種模式
都 stage 固定 gitlink，不設定 `update --remote` 政策、不改變日後 clone／worktree
的初始化方式，也不自動建立 task-member intent。需要任務分支時另用
`dev submodule develop`。

`--submodules=recursive|none` 覆蓋 parent 的有效初始化設定，只處理新子 repo 的
後代。只 stage `.gitmodules` 與新 gitlink，不 commit／push／建立 runtime，也不
修改其他 checkout 共用的 submodule 設定。非互動變更須有來源與 `--yes`；`--json`
不開提示。`--dry-run` 不連線、不寫入，尚未解析的 ref 明確顯示為待解析。

JSON 包含 `operation`、`parent`、`source`、相對 `path`、`checkout`、選用 `ref`、
`submodules`、`phase`、`git_dir`、選用 `head`／`branch`、`staged` 與選用 `warnings`。
Phase 區分 `planned`、`not-added`、`clone-incomplete`、`cloned`、`added-unstaged`、
`added`、`initialization-incomplete`、`complete`；失敗回傳非零並保留部分結果。
Clone／ref／staging 失敗可能留下檔案或 metadata：先檢查回報的精確路徑，必要時
手動完成 metadata／staging，再於新子 repo 內用 `dev submodule init` 重試缺少的
後代。不覆蓋重跑 add、不強制刪除殘留 clone；retirement 專用的 `recover` 不是
新增流程的復原指令。

REPOS／REMOTE 的 `y u` 可複製網路 clone URL：REPOS 使用選中 checkout 的 origin，
或要求選擇其餘 remote；REMOTE 使用 CloneURL，其次 SSHURL。不拿瀏覽器網頁 URL
代替，也不為了複製而 fetch。

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
即使全部 gitlink 都是空的，移除 linked worktree 仍需此旗標。外層分支預設保留；
canonical 與共享 repository 不會被連帶刪除。`--force` 不略過子 repo 檢查。
`dev flow` 有明確的 managed／unmanaged checkout 遞迴動作，done 清理流程也會
詢問是否包含子 repo。

僅在移除 linked worktree 時，從未初始化或完全空的 gitlink 可由本地證明為空：
子路徑不存在或真正為空，且沒有保留的子 Git store。這種子項目不必為了移除外層
checkout 而初始化、下載、取得遠端恢復證明或 push；clone／worktree 初始化預設
與子 repo 發布要求不變。Deinitialized 但仍有資料、orphan Git store、本地或
ignored 檔案、非預期 `.git`、symlink／reparse point、外部所有權或不完整觀測，
都會阻擋這條空子項目路徑。

實體上為空不等於移除許可。手動刪除 tracked gitlink 目錄會使 parent 變 dirty，
在管理目錄修剪前就阻擋一般不帶 force 的移除。清理不會默默建立或恢復空目錄來
繞過檢查。從未初始化、由 Git 建立的空目錄可通過這些檢查；子路徑不存在本身
不能證明 parent 可安全移除。

空子項目仍受所有權檢查保護。Task／artifact claims 依子路徑比對，不讓 Git
向上探索而誤用 parent repository。任何符合路徑且非 discarded 的 artifact
intent（包含已 finalized）都會保守阻擋空子項目移除；discarded 紀錄仍納入
plan authority。布局改變、子項目初始化或新增 claims 都會使已審閱 plan 失效。

刪除已初始化的子 clone 前，仍以當次遠端 refs 與隔離 fetch 證明可由指定 origin
重建，不能只看 origin 存在或 ahead=0。未發布 refs、reflog／unreachable 的
本地獨有物件、stash、dirty／untracked／ignored 內容、未完成 artifacts、其他
task、runtime 活動與額外子 worktree 都會阻擋。Filtered／LFS 資料、sparse／
partial 布局，或無完整保存證明的私有 Git 設定／檔案也會阻擋。
`objects/info/alternates` 仍是刻意保留的阻擋條件；空子項目例外不放寬真實
Git store 的恢復要求。

子 repo origin 必須符合其宣告來源；遠端失敗或 refs 在證明期間變動就停止。
Raw Git／外部 writer，以及之後發生的遠端歷史刪除，仍在 dev 合作式鎖與當次
證明的保證範圍之外。

## 中斷恢復

已初始化的子 checkout 由深至淺移到同檔案系統的私有暫存區，空與已初始化子項目
混合時也維持此流程。只可在既有鎖內修剪精確審閱布局中的空 modules 管理目錄；
每次 native directory-only removal 前，立即重新驗證目錄身分與空狀態。空的
checkout 目錄留給一般不帶 force 的 `git worktree remove` 處理。這不是強制移除
或遞迴刪檔的替代手段；一般 worktree 安全檢查維持不變，也不使用
`submodule deinit` 改寫共享設定。

管理目錄修剪若部分失敗會如實回報，不算 task retirement 成功。真實子 Git store
仍保有 journal 與 rollback 行為。每次移動都有同步保存的 journal。外層移除失敗時，只在原位置未被占用的情況下
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
