---
description: 從 Dashboard 完成與恢復 task、透過系統垃圾桶處理 Try，並開啟 repository 首頁。
authority: project
status: evolving
verified_on: 2026-09-24
lang: zh-TW
---

# Dashboard 生命週期操作

## 選擇操作

按 `Ctrl+O`、右鍵或點目前反白列開啟 row actions。Space 只展開／收合 REPOS
worktrees 與 FLEET 主機；平面列表不使用 Space。TASKS 的 `a` 是顯示已完成 tasks，並不會
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
顏色、符號與擷取時的所選列，**Manual** 則包含全部內嵌 `dev help`
主題。`1–3`、`v`、`/`、`j/k`、`f` 分別操作頁籤、範圍、搜尋、內容與放大。
搜尋、範圍、Back 與文章橫向捲動也可點擊。Help 只讀既有觀察與內嵌文章，
不執行操作或探測工具。搜尋範圍、文章導覽與分層 Esc 規則詳見
[目前分頁的 Help](tui-repos-bootstrap.zh-TW.md)。

Dashboard 的 `/` 在八個 views 即時篩選。輸入時用上下鍵選取可見結果，保留
輸入焦點、查詢與文字游標，不執行操作或觸發網路請求。`j/k` 仍是文字；左右鍵、
Home/End 編輯查詢游標，只有文字改變才重新選取第一筆。Enter 保留查詢與選取並
離開輸入，不會開啟該列；回到清單後再按 Enter 才執行原本的開啟操作。篩選時
Esc 清除查詢，pending repository 詳情不會遮住輸入框。Help 索引／結果搜尋同樣
支援輸入中的上下鍵與保留選取；文章內查找／捲動維持獨立。Notes 搜尋仍只在
Enter 時送出；Ctrl+O 的 Enter 仍執行所選 action，forms／SSH dialogs 保留既有
輸入行為。

TASKS 的完成、恢復、退休與 recovery 沿用既有 CLI workflows。Dashboard 在
提示期間暫停，完成或失敗後重新載入。Task ID／revision 檢查會拒絕過期選取。
`dev work sweep --task <id>` 只回報指定 task；加上 `--apply` 後逐項確認可執行的
建議。`--task` 不能與掃描整批 worktrees 的 sweep modes 合用。

透過 PR 完成 handoff 時保留 HOT／WARM；integration 才寫入 DONE。Retirement
另行移除合格的 execution state，預設保留 branch，並沿用 taskflow 安全規則。

## 建立與開啟工作

REPOS 的 `n` 或 Ctrl+O → new repository 會開啟既有的
`dev repo new --handoff stay` wizard，即使清單空白、篩選後無結果，或 repository
observations 尚在載入也能進入；選單開啟期間原選取列消失，不會阻擋這個入口。
進行中的 clone 仍會阻擋建立。原生 wizard 保留確認，並在 mutation 前重新檢查
目的地、nested-repository 限制與 exclusive-create 條件。其他依賴所選列的操作
及 REMOTE clone 的 freshness checks 維持不變；pending rows 不是 mutation 授權。

離開篩選輸入後，REPOS Enter 只開啟所選 repository，不建立 task。`s`／`d` 開啟完整 start
wizard，分別預選 worktree／direct。Wizard 詢問 task 與必要的 branch、base、
next action，最終建立確認前可選 open（預設）或 stay。Reviewed plan 的 base
必須明確，不能由任意 checked-out branch 推定。

Attach、shell-directory handoff 或外部 retirement coordinator 都在 Dashboard
釋放 terminal 後才執行。一般完成或取消則返回 Dashboard，不會自動啟動 agent。

## 將已畢業的 repository 退回 Try

在 REPOS → Ctrl+O → demote，可把曾 graduate 的 Try 退回實驗區。
此 action 先顯示與 `dev tries demote` 相同的受保護搬移計畫，確認來源與目的地
後才套用。沒有 graduation history 的一般 repository 不適用。

```bash
dev tries demote <repo-or-path-or-catalog-id> --dry-run
dev tries demote <repo-or-path-or-catalog-id>
dev tries demote <catalog-id> --to ~/src/tries/2026-09-24-parser
```

預設回到記錄中的原 Try 路徑。若目的地被占用或已不在目前 `tries_root` 內，
必須用 `--to` 明確指定安全位置。Demote 保留目前所有 bytes，包括 dirty／
untracked／ignored 檔案、Git history／remotes、catalog ID、tags、notes 與
畢業紀錄；Try 變為 active、present。不撤銷 commit 或 publication，也不建立
symlink。Task、runtime／agent、artifact claims，帶有 linked worktrees 的
canonical repository，不完整觀察或過期計畫，都會阻擋搬移。原有同檔案系統、
identity、rollback 與 reconciliation 保護仍適用。見
[命令遷移與 Try 轉換](../reference/cli-v0.3.zh-TW.md)。

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
dev repo browse
dev repo browse api --remote origin
dev repo browse --print
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

Dashboard Enter 開啟 repository／task 列或進入 FLEET 主機導覽；Space 展開／收合 REPOS／FLEET 樹，平面列表不使用 Space。REPOS／TRY 的 `Ctrl+O` 提供整理目前項目、篩選結果或全部本地工作，多選集中在獨立 triage。`1–7` 依序切換 TASKS、REPOS、FLEET、TRY、REMOTE、SKILLS、MCP。TASKS 狀態篩選移到 action menu，`a` 仍顯示 done。點資料欄標題循環升序 → 降序 → 預設，例如 FLEET 的 HOST 可按主機聚集。排序只操作當前快照、每頁獨立保留於 session，未知值置底。Footer 保留兩行主要操作與導覽，另有版本／更新資訊列，工具／狀態篩選／排序放進 `Ctrl+O`，`?` 開啟目前分頁的 Keys／Guide／Manual。既有 `4–7` 自訂工具需改綁；`x`／Ctrl+A 不再是 dashboard 選取保留鍵。

Triage 用 repo／Try 群組 checkbox，支援滑鼠與 Ctrl+A 全選／取消篩選結果。返回 dashboard 時保留失敗摘要。Git 同步診斷包含類別、exit code、截長且遮罩的輸出與下一步；舊 receipt 無法還原已丟棄的原因。重試必須重新 preview，不會暗中登入、fetch 或 rebase。詳見[本地整理](local-triage.md)。


Ctrl+O 的 `/` 只篩選目前選單，方向鍵移動，Enter 執行；Esc 先清除搜尋。
REPOS 可管理目前或篩選範圍的 skills，SKILLS 可選單一 skill、project 或 global。
這些入口使用 [dev agent skill manage](skills-management.zh-TW.md) wizard；更新先檢查
並預覽，experimental restore／sync 僅限單 project。


## 問題與建議動作

八個頁面的 `Ctrl+O` 選單都有 **issues / suggested actions**，空清單也能開啟。
預設查看目前頁面，也可切到全部頁面；內容包含已知 inventory／列內問題與近期
操作失敗，不會為此造訪或探測尚未開啟的頁面。選取問題可看完整診斷、來源、
處理建議並複製文字。問題清單支援分頁，完整訊息另有可捲動的詳情視窗。

各來源的問題處理對照如下：

| 來源 | 保留的問題 | 建議動作與完成判定 |
| --- | --- | --- |
| TASKS | Inventory／Git 失敗、checkout 遺失與生命週期阻擋 | 重讀本機 task 觀測；透過既有 sweep 流程檢查及恢復確切 task。Task revision 改變時必須重新審閱。 |
| REPOS | Repository 身分、worktree、runtime、task、Git topology 與磁碟用量失敗 | 重讀 repository inventory；來源仍存在時開啟確切項目的動作。整理流程保留既有預覽。 |
| FLEET | Host 觀測、連線、信任／版本與 Herdr 失敗 | 查看該 host 的原生動作；本機重查讀取 cache。驗證與即時刷新仍是明確的 host 操作。 |
| TRY | Runtime／Git／磁碟失敗、資料夾遺失與未完成移動 | 重讀本機 Try 狀態；在既有整理／恢復流程審閱確切 Try。不自動重跑移動或刪除。 |
| REMOTE | Inventory／provider 與 clone／open 失敗 | 讀取 cache、查看保留的 clone 結果，使用 provider 登入或明確刷新。沒有 forge CLI 時可審閱安裝選項。 |
| SKILLS | 原生 lock／file 診斷碼與路徑、完整性與更新失敗 | 查看／複製完整診斷、開啟確切來源檔或進入該 skill 的管理動作。成功重讀原生來源後移除已解決診斷。 |
| MCP | 原生來源／設定診斷與 declaration coverage 代碼 | 開啟診斷指向的確切來源或 declaration 動作，再重讀靜態宣告。檢查問題不會啟動 server。 |
| SSH | Registry／SSH 權限、缺少工具、來源失敗／部分完成與 discovery／測試／setup 失敗 | 審阅確切權限修復、來源詳情或安裝選項；重查仍保留可獨立使用的 profiles。Discovery 不代表驗證成功。 |
| 共用操作 | Notes、stats、剪貼簿、editor／config 儲存與部分完成 receipts | 保留完整錯誤／receipts 並提供複製指引；重讀觀測不重跑操作。Notes Markdown 與 stats 資料庫仍是持久資料。 |

問題檢查只讀本機依賴與 metadata，不把未知錯誤文字轉成 shell 命令。修復先產生
具體預覽，使用者選擇 **run this reviewed action in the foreground** 後才執行。
關閉預覽不執行；部分成功保留已完成步驟，不自動重試。

Registry 權限問題顯示確切路徑、owner、目前 mode 與預期條件；尚無 registry
是正常狀態。支援的 Unix 平台只對列出的目前使用者擁有路徑收緊權限，套用前
重新驗證完整檢查範圍的 metadata。Symlink、hardlink、其他 owner 或審閱後變更
會阻擋套用；不遞迴處理、不改 ownership。Windows registry ACL 修復在有經驗證
的後端前提供人工指引；SSH 權限使用既有的 guarded service。

依賴安裝支援 macOS Homebrew、Debian／Ubuntu apt，以及 Windows 的精確 WinGet
package ID。預覽列出套件、已解析的命令與官方指引。未支援的工具／平台或缺少
package manager 時只提供指引。前景安裝保留原生提示，不新增套件來源、不安裝
manager、不預先接受條款。完成後檢查執行檔，`ssh` 另檢查 OpenSSH capability；
驗證與服務啟動分開處理。取消或失敗不自動重試。

## REMOTE snippets

REMOTE 預設顯示 repositories。**Ctrl+O → Show snippets** 載入 GitHub Gists
與 GitLab snippets，**Show repositories** 切回。兩種模式各自保留搜尋與選取
狀態。Snippet actions 提供 provider／project 範圍、重新整理、明確全文搜尋、
開啟／複製 URL，以及 CLI editor 建立 wizard。普通篩選只讀 metadata；
repository clone 與生命週期操作不適用於 snippet rows。詳見 [Snippets](snippets.md)。

## Dashboard 版本與更新提示

Dashboard footer 固定顯示目前執行版本；已知有較新的穩定版本時，另顯示版本號與
`dev self upgrade`，更新仍由使用者明確執行。先讀取現有 24 小時 release 快取，首個畫面
完成後才背景檢查。`[update] check = false` 或 `DEV_NO_UPDATE_CHECK=1` 會關閉
檢查與新版提示，但保留目前版本。舊觀測標示 cached，檢查失敗不打斷其他操作。
開發版／dirty 版本字串會保留；無法比較的版本不會被宣稱為最新版。適用於裸 `dev`
與 `dev tui`。

## REMOTE 統計與排序

Repository 列顯示 GitHub／GitLab stars、forks、open issues 與 open PR／MR。
Issues 不包含 pull requests；`PRS` 欄包含開啟中的 draft PR 與 GitLab MR。
先顯示 inventory，再以每批最多 25 筆的背景 GraphQL 請求補上統計。進入 REMOTE
時，fresh inventory 可直接沿用，缺少或過期的統計另外補查；`r` 同時更新兩者。
遭限流時停止該 provider 的統計請求，待之後刷新再試。排序與 `/` 篩選只使用
已載入資料，不聯絡 provider。

點欄位標題循環升序、降序與預設排序；**Ctrl+O → sort columns** 也能選擇窄螢幕
隱藏的欄位。數值按數字排序，未知值在兩個方向都置底，保留既有預設排序與目前
選取的資源。詳細資訊顯示統計與觀測時間：`0` 是實際量到零，`?` 是未知／失敗，
`—` 是未提供，`~` 是保留的過期數值。統計失敗不會使成功刷新的 inventory 失效。
本版未提供 Azure repository 統計。

GitHub Gist 增加 stars、forks、comments；GitLab snippet 保留既有 metadata，
本版不查詢其 comment connections。Snippet 也可按檔案數排序；`+` 表示檔案清單
不完整，不會當成精確總數排序。Repository 統計使用既有私有快取與
`forge.cache_ttl`；snippet 統計只保留在 dashboard session。兩種模式各自保留排序。
