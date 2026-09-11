---
description: 從 Dashboard 完成與恢復 task、透過系統垃圾桶處理 Try，並開啟 repository 首頁。
authority: project
status: evolving
verified_on: 2026-09-10
lang: zh-TW
---

# Dashboard 生命週期操作

## 選擇操作

按 `Ctrl+O`、右鍵或點目前反白列開啟 row actions。TASKS 與 TRY 也可按 Space；REPOS 的
Space 仍用來展開 linked worktrees。TASKS 的 `a` 是顯示已完成 tasks，並不會
把 task 標成 done。Checkout 遺失時提供 recovery，不能直接當成完成。

點其他列先選取，再點反白列開選單，不限間隔，也不會直接執行選單項目。
鍵盤或啟動時預選的列也適用。首次切到 REPOS 會預選啟動目錄所屬 repo，
保持原排序。未被掃描涵蓋的啟動 repo 提供可點擊的預覽入口，確認加入
`repo_paths` 或把父目錄加入 `scan_roots` 後，重新載入本機清單並定位。
詳見 [TUI 啟動 repo 與掃描設定](tui-repos-bootstrap.zh-TW.md)。

Ctrl+O 與 Help 共用浮窗，提供可點擊的 **Expand**、**Close** 與可拖曳捲軸。
通常最大 104 欄 × 32 列；少於 80 欄或 22 列時使用全畫面。`/` 篩選目前
action menu，方向鍵選取、Enter 繼續。Esc 先停止輸入或清除查詢，再關閉；
非輸入狀態的 `q` 直接關閉。點外部只關閉，不把點擊傳給 dashboard。
既有操作條件與確認規則仍適用。

按 `?` 或點 footer **Help**，預設看目前分頁的 **Keys**；**Guide** 解釋
顏色、符號與擷取時的所選列，**Manual** 則包含全部 23 個內嵌 `dev help`
主題。`1–3`、`v`、`/`、`j/k`、`f` 分別操作頁籤、範圍、搜尋、內容與放大。
搜尋、範圍、Back 與文章橫向捲動也可點擊。Help 只讀既有觀察與內嵌文章，
不執行操作或探測工具。搜尋範圍、文章導覽與分層 Esc 規則詳見
[目前分頁的 Help](tui-repos-bootstrap.zh-TW.md)。

TASKS 的完成、恢復、退休與 recovery 沿用既有 CLI workflows。Dashboard 在
提示期間暫停，完成或失敗後重新載入。Task ID／revision 檢查會拒絕過期選取。
`dev sweep --task <id>` 只回報指定 task；加上 `--apply` 後逐項確認可執行的
建議。`--task` 不能與掃描整批 worktrees 的 sweep modes 合用。

透過 PR 完成 handoff 時保留 HOT／WARM；integration 才寫入 DONE。Retirement
另行移除合格的 execution state，預設保留 branch，並沿用 taskflow 安全規則。

## 建立與開啟工作

REPOS Enter 只開啟所選 repository，不建立 task。`s`／`d` 開啟完整 start
wizard，分別預選 worktree／direct。Wizard 詢問 task 與必要的 branch、base、
next action，最終建立確認前可選 open（預設）或 stay。Reviewed plan 的 base
必須明確，不能由任意 checked-out branch 推定。

Attach、shell-directory handoff 或外部 retirement coordinator 都在 Dashboard
釋放 terminal 後才執行。一般完成或取消則返回 Dashboard，不會自動啟動 agent。

## 處理不再需要的 Try

```bash
dev tries delete <ref> --dry-run
dev tries delete <ref>
dev tries delete <id> --permanent --confirm-delete <id>
```

`rm` 是 `delete` 的 alias。預設移入 macOS Trash、Linux GIO Trash 或 Windows
Recycle Bin。Helper 缺失、volume 不支援或執行失敗，都不會自動轉為永久刪除。
Linux removal 另需 kernel／filesystem 提供 statx mount identity，避免將 bind
mount 誤認為 Try 擁有的子目錄。
垃圾桶與 archive 一樣，在 bytes 真正被刪除前仍占用空間；helper 不會自動安裝。
macOS 15+ 優先使用系統 `trash --stopOnError`，舊版則透過不使用 out-pointer
bridge 的 JXA 呼叫 Foundation。

永久刪除是獨立選項，互動時需輸入 `DELETE <完整 ID>`，scripts 則需明確指定
`--permanent --confirm-delete <id>`。單獨 `--yes` 只確認 Trash。`--json` 不會
提示輸入；`--dry-run` 不會刪除。預覽涵蓋 ignored／untracked files 與所有 local
Git storage，包括 branches、tags、stash；不表示 remote backup 已驗證。

只有本機獨立且已 cataloged 的 active／deprecated Tries 可以處理，也包含
archived folders。Task claims、live runtime、cwd、nested repositories、
linked／shared Git、mount boundaries 與不安全路徑都會阻擋刪除。Runtime coverage
未知時，需要另外明示 `--assume-no-runtime`；不能覆蓋已觀察到的 live session。

Catalog ID、phase、metadata 與其他主機位置保留。本機 location 成為 `evicted`，
並增加 `removal_id`／`removal_method`。Durable operation JSON 存於 assets 旁的
`try-removals`，不是 cache，也不是 verified backup receipt。中斷的操作保持
indeterminate，不會自動重試刪除；若已記錄成功移除，reconciliation 可完成尚未
寫入的 catalog update。

## 從系統垃圾桶還原

先透過 OS Trash UI 還原原資料夾，再於修改內容前重新關聯：

```bash
dev tries restore <ref> --from <restored-path>
```

TRY history 中的操作也提供相同 path prompt。Dev 驗證原 filesystem identity 與
content inventory，恢復相同 catalog ID；不搜尋垃圾桶，也不能救回永久丟棄的
bytes。原本的 archive restore 仍使用 `dev tries restore <ref> [--to <path>]`。

## 開啟 repository 首頁

```bash
dev browse
dev repo browse api --remote origin
dev browse --print
dev repo context
```

不帶參數時，browse 解析目前 repository，也支援 linked worktree。Remote 依序
選目前 branch upstream、origin、唯一 remote；仍有歧義時互動選擇，scripts 必須
指定 `--remote`。`--print` 只印出 HTTPS URL，不提示輸入或開啟瀏覽器。解析只讀
local Git，不 fetch 或查詢 forge，也不猜測未知 host／SSH alias。

TASKS、REPOS、Git-backed TRY 與 REMOTE 都有 browser actions，開啟後留在
Dashboard。詳細資訊仍使用 `repo context`；browse 開 repository 首頁，不定位
特定 branch 或 file。

Trash 操作中斷後，`restore --from` 可明確重新關聯未變更的原資料夾或已還原
資料夾，不會重試刪除。若系統將資料夾放回原 archive path，location 仍為
archived；之後再用一般 `tries restore` 回到可見位置。

## Dashboard 導覽與整理入口

Dashboard Enter 永遠開啟目前列，REPOS／TRY 的 `Ctrl+O` 提供整理目前項目、篩選結果或全部本地工作，多選集中在獨立 triage。`1–7` 依序切換 TASKS、REPOS、FLEET、TRY、REMOTE、SKILLS、MCP。TASKS 狀態篩選移到 action menu，`a` 仍顯示 done。點資料欄標題循環升序 → 降序 → 預設，例如 FLEET 的 HOST 可按主機聚集。排序只操作當前快照、每頁獨立保留於 session，未知值置底。Footer 只保留兩行主要操作與導覽，工具／狀態篩選／排序放進 `Ctrl+O`，`?` 開啟目前分頁的 Keys／Guide／Manual。既有 `4–7` 自訂工具需改綁；`x`／Ctrl+A 不再是 dashboard 選取保留鍵。

Triage 用 repo／Try 群組 checkbox，支援滑鼠與 Ctrl+A 全選／取消篩選結果。返回 dashboard 時保留失敗摘要。Git 同步診斷包含類別、exit code、截長且遮罩的輸出與下一步；舊 receipt 無法還原已丟棄的原因。重試必須重新 preview，不會暗中登入、fetch 或 rebase。詳見[本地整理](local-triage.md)。


Ctrl+O 的 `/` 只篩選目前選單，方向鍵移動，Enter 執行；Esc 先清除搜尋。
REPOS 可管理目前或篩選範圍的 skills，SKILLS 可選單一 skill、project 或 global。
這些入口使用 [dev skill manage](skills-management.zh-TW.md) wizard；更新先檢查
並預覽，experimental restore／sync 僅限單 project。
