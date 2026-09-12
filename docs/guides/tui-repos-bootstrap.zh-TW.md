---
description: 在 dashboard 瀏覽 tasks、repositories、fleet hosts、experiments、remotes、agent skills 與靜態 MCP declarations、記錄 quick notes、inventory/adopt 現有工作，並以獨立 dev flow 檢查 guarded lifecycle。
authority: project
status: evolving
verified_on: 2026-09-11
tested_with: skills 1.5.23; Claude Code 2.1.252; Codex/Cursor/Gemini CLI/OpenCode docs 2026-09-01
lang: zh-TW
---

# TUI、Repository、Quick Notes 與 Bootstrap

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Standard input/output 都是 terminal 時，直接執行 `dev` 會開啟 interactive dashboard；透過 pipe 執行時會輸出 plain task listing，讓 shell composition 保持可預期。

`dev flow [repo]` 是另一個獨立、僅限 TTY 且標示為 preview 的全螢幕介面，不是 dashboard 的 tab 或 view。它聚焦單一 canonical repository 的所有 registered worktrees 與 task-only rows，並把 lifecycle intent、live evidence 與 plan-first actions 並列；完整說明見 [Repository Flow 預覽](repository-flow.zh-TW.md)。

## 八個 view

REPOS／REMOTE 支援 `y u`，row action menu 也有 **copy clone URL**。REPOS 只在
本機讀取選中 checkout 的 fetch URL，優先 origin，多個候選時要求選擇。REMOTE
複製 CloneURL，其次 SSHURL，不拿瀏覽器 URL 代替。缺少／不安全的 URL、stale
selection 與 clipboard 錯誤不會觸發 clone／fetch 或複製空字串。要加入 dependency，
請用 CLI 的 [submodule 新增 wizard](submodule-workspaces.zh-TW.md)；dashboard
本身不新增 submodule。

| View | 回答問題 | 來源 |
|---|---|---|
| TASKS | 我正在處理什麼？ | task registry 加即時 Git/runtime facts |
| REPOS | 本機有哪些持久 repositories？ | 設定的 scan roots 與 local catalog |
| FLEET | 設定的機器上有哪些 repository 與 active work？ | 已接受的 local REPOS snapshot 加上透過 SSH 取得的 remote `dev` snapshots |
| TRY | 哪些 experiments 能 resume、archive 或 graduate？ | experiment catalog 加 live facts |
| REMOTE | 有哪些 repository 能 open 或 clone？ | authenticated `gh`/`glab` inventories 與 cache |
| SKILLS | 從目前 context 啟動 agent 時會讀到哪些 skills？ | context-first targets 加 `A` all-repositories toggle |
| MCP | 該 context 會暴露哪些 MCP declarations？ | 使用相同 shared scope 的 sanitized static config |
| SSH | 如何連到某台機器？ | local aliases、canonical registry、cached discovery 與 provider membership |

初始 TASKS frame 會先建立，不等待 runtime auto-detection、project-root lookup、
cache decode、shell tool probe 或 optional release refresh 完成。TASKS、REPOS 與
TRY 接著由同一個 shared local cycle 獨立發布；REMOTE、SKILLS、MCP 與 SSH 維持
lazy，FLEET 則延後逐台預熱。每個被請求的 view 都有自己的 generation：`r` 會 supersede 舊讀取，晚到
結果會被忽略，refresh 失敗時保留可用 rows，而成功的空結果會移除過時 rows。
Cache acceptance 與目前 live load completion 是不同階段；可能從未開啟的 optional
view 不存在虛構的 all-tabs-ready 狀態。

單次診斷 trace 必須指定一個尚不存在的 absolute path：

```bash
DEV_TUI_TRACE=/tmp/dev-tui-trace.json dev
```

Private、bounded JSON 會在 alternate screen 還原後才寫入。內容只有 relative timing、
aggregate row counts 與 categorical view/generation/outcome fields，不包含 repository/task/host/tool
名稱、paths、commands、key values、URLs、handles 或 raw errors。它不是
`stats.db`，也不會送出本機。`tui.initial_view_returned` 只代表 Bubble Tea view
string 已建立，不代表 terminal 已 rasterize。

用 `tab`、`h`/`l`、左右鍵，或 left-click visible tab 切換。Left-button
press 選取 visible row；點目前反白列就開啟 actions，包含鍵盤或啟動時預選的列，
不限兩次點擊間隔。Wheel 每次移動三列；right-button press 先選取 row，再開啟
該 row 的 actions。Modified row click、motion 與 release 不會啟動列；開啟選單的點擊
只開選單，另行點選項目才執行。Enter/`o` 開啟目標。Mouse tracking
啟用時，部分 terminal 的原生文字選取需要按住 Shift/Option。

## 目前分頁的 Help（v0.2.24）

按 `?` 或點 footer 的 **Help**，預設開啟目前 dashboard 分頁的 **Keys**，
頂部保留該頁的 TL;DR。Help 分成三個頁籤：

| 頁籤 | 內容 |
|---|---|
| Keys | 本頁操作、通用導覽與已設定的工具，包含可用狀態及必要條件 |
| Guide | 使用流程、欄位、顏色與符號；開啟 Help 時擷取的所選列唯讀快照 |
| Manual | 全部內嵌 `dev help` 主題，相關主題優先，另有共用 workflow TL;DR |

Keys 與 Guide 共用搜尋，先列快捷鍵、再列說明。預設只搜尋選定的 Help
分頁；選 **All views** 才搜尋七頁。切換說明範圍不改變實際 dashboard
分頁、選取、filter 或排序。所選列的解讀只出現在開啟 Help 時的那一頁，
保留 unknown、loading 與 cached 狀態；它說明擷取時的畫面，不能當成
執行操作的新證據。

REPOS 的 Guide 解釋：青／藍色選取會覆蓋原列色，橘色表示 dirty paths，
不能只憑灰色判定 Git clean。另包含 `⇡N`／`⇣N`／`⇕`、`=N`、`+N`、`!N`、
`?N`、`clean`／`local`、未知 `?`、舊快照 `~`、載入或截短 `…`，以及各欄
`—` 的不同含義。LIVE、WT、task 數量、logical owned SIZE 與 LATEST
各有說明。其他分頁有自己的圖例，例如 MCP 綠色表示設定啟用，不能推斷
server 正在執行或健康。實際顏色受 terminal palette 與 `--color` 影響。

Manual 搜尋主題名稱、標題、小節與全文，並顯示摘錄；開啟結果會定位到
命中段落。一般段落換行，程式碼、ASCII 圖與表格保留結構並支援橫向捲動。
文章內 `/` 查找、`n`／`N` 切換命中；Back 保留上一層搜尋與閱讀位置。
每篇文章都標示對應的 CLI 指令。

用 `1–3` 或點頁籤切換，`v` 或點 **View** 選範圍，`/` 或點搜尋欄輸入，
`j/k` 選取或捲動，Enter 閱讀。PgUp/PgDn、滾輪與可點擊／拖曳的捲軸
瀏覽長內容；文章的橫向捲動按鈕或左右鍵可查看過寬的程式碼與圖。
Esc 依序停止輸入、清除查詢、返回上一層、關閉 Help。非輸入狀態下 `q`
直接關閉；輸入中的 `q`、`v`、`f` 等字元仍是文字。

Help 與 Ctrl+O 共用有 **Expand**、**Close** 的浮窗，通常最大為
104 欄 × 32 列、四周至少留兩格；少於 80 欄或 22 列時使用全畫面。
Help 可按 `f` 放大。點外部只關閉，不會啟動背景列；浮窗支援拖曳捲軸，
dashboard 列啟動仍忽略 mouse motion。

開啟與搜尋 Help 只讀內嵌文章、既有工具可用狀態及畫面快照，不另行探測
Git、forge、runtime、provider 或 MCP。Flow 與 triage 維持各自的 Help。

## 啟動 repo 與掃描設定（v0.2.24）

啟動時仍顯示 TASKS。首次切到 REPOS 時，預選啟動目錄所屬的 repository，
保留原排序並讓該列出現在可視範圍。子目錄、alias 與 linked worktree 透過
Git common directory 對應；linked worktree 選取 repo 主列。使用者手動選取、
篩選或排序後，晚到的資料不會再把游標拉回啟動 repo。

啟動所在 Git repo 未被掃描設定涵蓋時，REPOS 顯示可點擊的 **Add this repo…**
與 **Scan parent directory…**，Ctrl+O 也提供相同入口。前者把主 repo 根目錄加入
`paths.repo_paths`，後者把該根目錄的父目錄加入 `paths.scan_roots`，依原有最大
深度 3 的規則掃描同層專案，不使用目前子目錄當候選根目錄。零散位置優先用
repo_paths；專門集中存放專案的目錄適合用 scan_roots。

兩種操作都先預覽實際設定檔（尊重 `--config`）、路徑、範圍與欄位變更，並提供
可點擊的確認／取消。較長的預覽可在文字上使用滾輪或 PgUp/PgDn 捲動。
只追加所選欄位，保留註解、項目順序與省略欄位繼承的預設值；避免重複加入，
不自動合併或刪除既有項目。儲存後重新載入本機清單並選取新加入的 repo。
掃描失敗或不完整維持未知，不會直接當成未納管提示加入。

自動寫入使用 macOS/Linux 的 guarded file backend，保留 metadata，並在
`$XDG_DATA_HOME/dev/config-recovery` 保存私有復原紀錄。設定檔被同時編輯或
repo 身分改變時拒絕套用舊預覽。Symlink 設定檔、不支援的 TOML 格式或平台
會提供手動修改內容與設定編輯器入口。若儲存成功但重載失敗，會分別回報。

## 常用 actions

### TASKS

```text
enter/o   開啟選取的 task
p         park warm 並輸入 next action
c         編輯 next action
n/N       quick-add／瀏覽 repository notes
ctrl+o    task state filters
```

COLD worktree task 必須透過 `dev resume` 重建；TUI 不會用 generic open action 靜默重建。若 worktree 已遺失或不再由 Git 註冊，必須先執行 `dev sweep`，讓它在 resume 或 reap 前回報需要 salvage 的 agent artifacts。Enter 不會開啟只剩 artifacts 的 abandoned directory。Terminal 寬度至少 97 cells 時，TASKS table 會顯示 display-width-aware `REPO` column；更窄時保留原 columns，並在 detail 顯示 repo/path。

### REPOS

```text
enter/o   ad-hoc open，不建立 task
n         透過 clone-aware `repo new` wizard 建立 repository
a/N       quick-add／瀏覽 repository notes
space     展開 linked worktrees
m         編輯 repository tags/summary
d         追蹤目前 branch 的 direct work
s         啟動 isolated work：branch + worktree + provisioning + runtime
H         開啟 repository activity heatmap
y         開啟 copy/context actions
```

展開後會顯示每個 linked worktree，包括 harness-owned `(ephemeral)` 與未受管理的 `(external)` checkout。LIVE column 將 runtime activity 與 task state 分開。

REPOS 為空時仍可按 `n`。Dashboard 會 suspend 到
`dev repo new --handoff stay`，保留 config/scaffold overrides，成功後重新載入
local TASKS/REPOS/TRY state；TUI 不會另外維護一套縮減版 repository creator。

`dev repo context [repo]` 會輸出 TUI copy menu 相同的 agent-ready Markdown context，包含 paths、Git/worktree/runtime facts 與 tasks。`--json` 加入 schema-v1 evidence/readiness contract；只有 `--refresh` 會 live-probe optional forge 與 configured fleet sources。

### 獨立 Repository Flow

```text
dev flow [repo]         依 cwd 或明確 repo 開啟 canonical repository
j/k 或 Up/Down          選 surface row
h/l 或 Left/Right       選 action
Enter                   只建立 plan
r                       只更新 local facts
R                       明確選 fetch refs、query review 或 both
```

Flow 的 `CANONICAL`、`MANAGED`、`UNMANAGED`、`HARNESS`、`TASK-ONLY` 與 `CONFLICT` rows 各有不同 action set。只有 READY plan 經 `y` 或 exact typed token 的第二次 confirmation 後才 Apply；沒有 generic force、dirty discard、`--close-unknown`、`--assume-no-runtime` 或 shared-writer override。Runtime `none` 顯示為 unobserved，不會被解讀成已證明沒有 session。Result 會保留 partial step ledger，Apply 後再 fresh local reload。

### TRY 與 REMOTE

TRY 管理低成本 experiment、可逆 archive/restore、mark 與 graduation。Archive 是整理，不是 deletion 或 disk reclamation。

REMOTE 延遲載入，因此 startup 不等待 network。Private XDG cache 會在 first
view 後 decode，並保存完整 paginated inventory；fresh rows 不需要 network，
stale rows 仍可搜尋並在背景 refresh。Oversized／malformed payload，以及
fingerprint 屬於其他 configured GH/GL host 或 Azure target 的 cache 都會被忽略。
GitLab 使用 explicit `GITLAB_HOST`／`GLAB_HOST`（預設 `gitlab.com`），不從 cwd
推測 host；成功但為空的 inventory 會清除舊 rows。Enter 開啟既有 local clone。
對尚未 clone 的 repository，`c` 會確認受限於 `project_root` 的 destination；
`enter` 執行 clone 並留在 dashboard，`o` 則在 clone 後開啟。若 `project_root`
超出 configured REPOS discovery roots/depth，會在 mutation 前拒絕。Git clone 與
local-only、generation-guarded REPOS refresh 期間，row 與 status 會持續顯示 animated
marker；`q`／Ctrl-C 會要求取消，但不會拋棄 in-flight result。Snapshot 接受後，REMOTE
會標成 `repo`，REPOS 也能立即搜尋。若 Git 失敗後留下 destination，REMOTE 會把
exact path 標成 `inspect`，不會自動刪除或提供誤導性的 retry。`r` 才會
強制更新 forge inventories。使用 `/vis:private` 可精確過濾 visibility。只有 REMOTE row
能解析到 local clone 時才能使用 notes。TRY 保留 lowercase `n` 建立新 Try，不會改成
repository note。

### CLI repository pickers

Bare `dev repo clone` 會對既有 private forge cache 開啟 picker。它使用各 provider
的 exact clone URL，絕不 implicit refresh network，並保留 manual URL/path/`owner/name`
選項。Stale 或 incomplete cache 仍可選，但會顯示 warning；cache 必須明確 populate
或 replace：

```bash
dev repo remote --refresh
dev repo clone
```

在 checkout 外，bare `dev start` 使用相同 picker UI 選 fast live local discovery
的結果，之後才 full resolve 選定 repository 並規劃 task。在 repository 內則保留
immediate current-repository default，不掃描所有 configured roots。Default external selector 是 `fzf`；
executable 缺少時 fallback 到 built-in Bubble Tea list，`[picker] command = []` 會
強制使用 built-in。相容 external command 從 stdin 讀 candidates，並須在 stdout
原樣回傳一行。Non-TTY caller 保留 line prompt，絕不收到 picker UI。

Repository 的 `contrib/television/dev-remote-repos.toml` 與
`contrib/fzf/dev-repo-clone.bash` 會組合相同 public source，不建立另一份 inventory：

```bash
dev repo remote --refresh                 # 只在明確要求時 populate／refresh
ref="$(tv dev-remote-repos)" && [ -n "$ref" ] && dev repo clone "$ref"
source contrib/fzf/dev-repo-clone.bash
dev-repo-clone-fzf
```

兩個 external recipe 都需要 `jq`、只讀 `dev repo remote --cached --json`，並將
一個 quoted clone URL 傳給 `dev repo clone`。它們是供 copy 或 symlink 的範例；
dev 不會修改 Television、shell 或 chezmoi config。

### FLEET

FLEET 採遠端主機樹。本機預設隱藏；按 `a` 或使用選單，才在最後以收合狀態
顯示並重用 REPOS。隱藏時搜尋與 coverage 都排除本機。Repos 尚在載入時，
主機列與動作仍可用。Space 展開／收合主機，Enter／`o` 導覽至主機或 repo；
Ctrl+O／右鍵開啟選單。
HERDR 欄由共享的本機 catalog 查詢呈現登錄／啟用狀態，與 SSH snapshot 分開。
Enable／disable／remove 操作精確 profile，遠端 sessions 保留；多筆先用 picker
選取。r 更新選取主機及 catalog metadata。有效快取持續可搜尋並標示時間與範圍。

初始畫面後五秒或提早進入 FLEET 時，開始一次性的缺少／過期快取預熱，不跳出
密碼提示。設定 [tui.fleet] background_refresh = false 可保留純手動更新。
搜尋包含收合主機的已知 repos，暫時展開匹配項，不額外觸發 SSH。詳見
[主機樹與 Herdr 動作](remote-fleet.zh-TW.md#dashboard-host-tree)。

## Repository quick notes

在 TASKS，lowercase `n` 開啟單行 quick-add prompt。REPOS 將 `n` 保留給 new
repository，並以 `a` quick-add note；兩個 view 都用 uppercase `N` 開啟選取
repository 的 notes overlay。Child worktree 會透過 catalog identity 解析到同一個 canonical repository。

```text
j/k       移動
/         搜尋 body、tags 與 repository
Enter     展開或收合 Markdown body
a or n    新增另一則 note
e         用 VISUAL/EDITOR 編輯 body
d         進入確認；y 才刪除
Esc       不改資料並返回
```

可選的 REPOS column `notes` 顯示數量。Table 寬度有限，因此預設不啟用。Notes 存在時，repository detail 顯示數量與最新 preview；task 能解析到已載入 repository row 時，task detail 才會顯示。

不使用 TUI 也能操作同一份 source of truth：

```bash
dev note add "try event subscription" --repo api --tag idea
dev note list api
dev note search "event subscription" --repo api
dev note show <id-or-prefix>
dev note edit <id-or-prefix>
dev note delete <id-or-prefix>       # 會確認
dev note path api
dev note reindex
```

Note ID prefix 必須唯一，且至少八個字元。

Configured `paths.state_dir/notes` 下的 Markdown 是 durable data；`$XDG_CACHE_HOME/dev/notes.db` 只是可重建的 search index。精確 flags 請見[命令與設定 reference](../reference/commands-config.md)中的完整 generated command reference。

SKILLS 與 MCP 都會等目前 REPOS generation 被接受後才延遲載入。在 Git 內，兩者只掃描
exact startup checkout 加 global/user sources，符合從該處啟動 agent 實際會讀到的內容，
不再混入無關 projects。在 Git 外，startup context 保留 cross-repository inventory，掃描
所有 accepted REPOS targets 加 ordinary startup directory。Uppercase `A` 會在本次 TUI
session 讓兩個 view 共用 context/all-repositories scope；切換時先清掉舊 scope rows，再以
generation guard reload，而未顯示的另一個 capability view 仍維持 lazy。Refresh 時會保留
可用 rows；warning-only partial inventory 仍維持 fresh，且 visible capability view 會在
REPOS recovery 後自動繼續載入。

SKILLS 直接讀取 versioned `skills@1.5.23` 77-agent path registry 與 lock files；
不會執行 Node、`skills`、npm、`npx`、agent detector 或 project code。同名
project/global/repository rows 保持分開。Presence 與 embedded `dev-cli` integrity
是 local facts；update state 則是獨立的 lock-recorded upstream comparison。`c`
是明確且 grouped 的 Git source check；`a` 開啟 upstream interactive installer；
`u` 先確認，再於該 row 的 checkout 更新選取且受 lock 管理的 skill。Check 直接
hash Git object bytes，不建立 checkout；依 locale 排序的 non-ASCII folder hash
維持 unverifiable。Mutation 需要直接安裝的 `skills` executable，會跳過
repository-local npm shim、拒絕 source-less lock，並在 cooperating `dev` processes
之間 serialized。Filters 包含 `repo:`、`scope:`、`agent:`、`update:`、
`presence:` 與 `integrity:`。按 `e` 會開啟 row 的 primary installed `SKILL.md`（missing row
則開 lock file）；`y` menu 可複製 file path (`p`)、safe summary (`s`)、sanitized
source URL (`u`) 或整份 raw file (`f`)。

MCP 會讀取 Claude Code、Codex、Cursor、Gemini CLI 與 OpenCode 的 static
declarations。它保留 file/scope rows 與 exact Claude local project key，不猜測
一般化的 effective config；只有 Claude documented user/project/local/managed
project approvals 會被解析。Absolute `CLAUDE_CONFIG_DIR` 會搬移 Claude user
sources。Configured/enabled/disabled 不代表 connected 或 healthy。Provider-specific
environment reference names 與有限 OAuth facts 會保留；其 values、raw arguments、
URL credentials/path/query/fragment 與 indirect file content 都會在 row 進入 model
前被丟棄。Scanner 不會執行 server、helper、URL 或 agent MCP command。Filters 包含 `repo:`、`agent:`、`scope:`、`transport:`、
`managed:` 與 `state:`；`r` 只重讀 static files。按 `e` 會開啟 selected declaration
的 `ConfigPath`；`y` menu 可複製 path (`p`)、sanitized declaration (`s`) 或整份 raw
file (`f`)。Raw copy 只讀 local regular file、上限 1 MiB 且不使用 network，但可能把
同檔的 credentials 與其他 declarations 放進 system clipboard；rows 與 structured
output 仍維持 sanitized。`e` 會編輯 private working copy，在 atomic replacement
前立即 revalidate observed source，並在偵測到 conflict 時保留 working copy。

## External tools

```bash
dev tui tools
```

設定的 tool 會在暫停 alternate screen 後，透過 `$SHELL` 於選取 checkout 執行。`interactive = true` 使用 `$SHELL -lic`，讓 local aliases/functions 能解析；跨機器需要 portability 時應使用 `PATH` 中的真實 executable。Availability probe 會在 first view 後以 bounded background load 執行；rendering 不會啟動 login shell，尚未解析的 binding 會 fail closed。

```toml
[[tui.tools]]
key = "L"
name = "lazygit"
run = "lazygit"
```

Key 區分大小寫，且不能覆蓋 globally owned dashboard binding。為相容既有設定，`A` 仍可配置，但在 SKILLS/MCP view 會優先執行 scope toggle。離開 editor 後可 reload 多數 config；更換 runtime backend 需要重啟 TUI。

Configured tool 是刻意保留的 escape hatch：它在選取 checkout 執行任意 configured command，不會自動繼承 `dev flow` 的 PlanID、conditions、agent occupancy 或 revalidation guards。Raw Git／forge command 也是同樣邊界；operator 必須自行確認其 safety。

## Inventory 現有機器

先產生 report：

```bash
dev bootstrap ~/code /mnt/work
dev bootstrap ~/code --json
```

Scanner 會辨識 canonical checkout、linked worktree、bare repository 與 symlink alias，再依 Git identity 去重。

建議的 organization layer 是 non-destructive symlink index：

```bash
dev bootstrap ~/code --index ~/Projects --layout flat
dev bootstrap ~/code --index ~/Projects --layout flat --apply
```

Physical move 是另一個更嚴格的 mode。Move plan 會阻擋 dirty repository、linked worktree、live session/current working directory、會損壞的 alias、occupied destination 與 cross-filesystem rename；任一 row blocked 時，apply 不會移動任何 repository。

## Adopt 進行中的工作

Bootstrap 回答 **repository 在哪裡**；adoption 回答 **哪些既有 branch、worktree 與 session 正在工作**：

```bash
dev adopt
dev adopt --apply
```

Adopt 預設只回報；只有 `--apply` 加確認後才寫 task entry。它不會移動、改名或刪除 checkout，也會排除已辨識的 harness-ephemeral worktree。

## 來源

- [`skills@1.5.23` agent path registry](https://github.com/vercel-labs/skills/blob/v1.5.23/src/agents.ts)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
- [Cursor MCP configuration](https://cursor.com/docs/mcp)
- [Gemini CLI MCP configuration](https://google-gemini.github.io/gemini-cli/docs/tools/mcp-server.html)
- [OpenCode MCP configuration](https://opencode.ai/docs/mcp-servers/)
- [`internal/help/topics/tui.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/tui.md)
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/flowtui`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/flowtui)
- [`internal/help/topics/notes.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/notes.md)
- [`internal/cli/note.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/note.go)
- [`internal/help/topics/bootstrap.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/bootstrap.md)
- [`internal/cli/adopt.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/adopt.go)
- [`internal/cli/bootstrap.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/bootstrap.go)

## Dashboard 生命週期新增功能

[Dashboard 生命週期操作](dashboard-actions.md) 說明 task 完成／恢復／retire／recovery、完整 start wizard、Trash 與 repository browser actions。`Ctrl+O` 開啟所選 row 的選單；TASKS 的 `a` 只顯示已完成 tasks。

## Dashboard 導覽與整理入口

Dashboard Enter 開啟 repository／task 列或進入 FLEET 主機導覽；Space 展開／收合 REPOS／FLEET 樹，平面列表不使用 Space。REPOS／TRY 的 `Ctrl+O` 提供整理目前項目、篩選結果或全部本地工作，多選集中在獨立 triage。`1–7` 依序切換 TASKS、REPOS、FLEET、TRY、REMOTE、SKILLS、MCP。TASKS 狀態篩選移到 action menu，`a` 仍顯示 done。點資料欄標題循環升序 → 降序 → 預設，例如 FLEET 的 HOST 可按主機聚集。排序只操作當前快照、每頁獨立保留於 session，未知值置底。Footer 只保留兩行主要操作與導覽，工具／狀態篩選／排序放進 `Ctrl+O`，`?` 開啟目前分頁的 Keys／Guide／Manual。既有 `4–7` 自訂工具需改綁；`x`／Ctrl+A 不再是 dashboard 選取保留鍵。

Triage 用 repo／Try 群組 checkbox，支援滑鼠與 Ctrl+A 全選／取消篩選結果。返回 dashboard 時保留失敗摘要。Git 同步診斷包含類別、exit code、截長且遮罩的輸出與下一步；舊 receipt 無法還原已丟棄的原因。重試必須重新 preview，不會暗中登入、fetch 或 rebase。詳見[本地整理](local-triage.md)。


## 載入、選單搜尋與多年活動

REPOS 先讀取附時間的顯示 cache，再逐批加入 discovery 與 Git observations。
Runtime 較慢時，其他 repo 資料仍會出現；pending／cached rows 不提供操作授權。
完整 discovery 才移除消失項目，失敗保留 stale／unknown。`r` 保持本地 refresh；
`dev cache clear repos` 可清除快照，SIZE 沿用其獨立 cache。

Ctrl+O 選單與子選單支援 `/` 篩選、方向鍵及 Enter；Esc 先清除搜尋，再關閉。
`H` 先顯示 stats，再自動補齊所選 repo 的完整本地 Git 歷史，refs 未變時使用
checkpoint。每年一張圖，由最早年份往下排列，支援滾輪、上下鍵、PgUp/PgDn、
Home/End；窄畫面按週分段。`r` 更新，`b` 強制 backfill，不 fetch。
Git 活動仍是每個非 merge commit 估算 20 分鐘。

REPOS／SKILLS 的 action menu 可開啟獨立
[Skills 管理 wizard](skills-management.zh-TW.md)，提供 scopes、多選、檢查與預覽，
不增加大頁籤。


### 本地載入測量

macOS、Go 1.26.4、60-repository 隔離 fixture，各情境跑三次的中位數如下。
時間取自 trace 接受資料的時刻，不是 terminal 完成繪製的時刻；Git 仍在背景更新。

| 情境 | 首批 repository 資料 | 完整本地 snapshot |
|---|---:|---:|
| 原 dashboard | 3,896 ms | 3,896 ms |
| 新版，無顯示 cache | 14 ms | 3,911 ms |
| 新版，有顯示 cache | 15 ms | 2,819 ms |

首批顯示不再等待完整掃描；完整掃描仍受檔案系統與程序負載影響，不把 cached Git
事實視為即時狀態。Recovery 詳情在選到 repo 時讀取；已完成觀察的 rows 可先操作，
其他 repo 繼續載入。

## SSH connection view

第八個 dashboard tab 獨立於 projects，集中顯示 machine connections。可按 `8`，
但已有 custom tool 使用該鍵時保留原綁定；仍可用 tab／click 導覽。每台 machine
保留 exact SSH profiles、provider membership 與 stale／unresolved source state。
`r` 只重讀 local config、registry、provider membership 及 discovery cache，不掃 LAN、
不做 SSH authentication。

`Enter`／`o` 使用 system SSH 開啟 exact alias；多個 profiles 時先選擇。`n` 開啟逐台
setup wizard，`c` 明確選擇 discovery source／scope，`p` 對選定 alias 做 fresh ordinary
login probe。`Ctrl+O` 提供 connection 與 machine-mapping actions。Discovery 和 provider
registration 都必須明確選擇；開啟 machine row 不會自動 enroll 或加入 fleet／Herdr。
Trust／identity 限制請參考 `dev help ssh`。
