---
description: 在 dashboard 瀏覽 tasks、repositories、fleet hosts、experiments、remotes、agent skills 與靜態 MCP declarations、記錄 quick notes、inventory/adopt 現有工作，並以獨立 dev flow 檢查 guarded lifecycle。
authority: project
status: evolving
verified_on: 2026-09-04
tested_with: skills 1.5.23; Claude Code 2.1.252; Codex/Cursor/Gemini CLI/OpenCode docs 2026-09-01
lang: zh-TW
---

# TUI、Repository、Quick Notes 與 Bootstrap

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Standard input/output 都是 terminal 時，直接執行 `dev` 會開啟 interactive dashboard；透過 pipe 執行時會輸出 plain task listing，讓 shell composition 保持可預期。

`dev flow [repo]` 是另一個獨立、僅限 TTY 且標示為 preview 的全螢幕介面，不是 dashboard 的 tab 或 view。它聚焦單一 canonical repository 的所有 registered worktrees 與 task-only rows，並把 lifecycle intent、live evidence 與 plan-first actions 並列；完整說明見 [Repository Flow 預覽](repository-flow.zh-TW.md)。

## 七個 view

| View | 回答問題 | 來源 |
|---|---|---|
| TASKS | 我正在處理什麼？ | task registry 加即時 Git/runtime facts |
| REPOS | 本機有哪些持久 repositories？ | 設定的 scan roots 與 local catalog |
| FLEET | 設定的機器上有哪些 repository 與 active work？ | 已接受的 local REPOS snapshot 加上透過 SSH 取得的 remote `dev` snapshots |
| TRY | 哪些 experiments 能 resume、archive 或 graduate？ | experiment catalog 加 live facts |
| REMOTE | 有哪些 repository 能 open 或 clone？ | authenticated `gh`/`glab` inventories 與 cache |
| SKILLS | 從目前 context 啟動 agent 時會讀到哪些 skills？ | context-first targets 加 `A` all-repositories toggle |
| MCP | 該 context 會暴露哪些 MCP declarations？ | 使用相同 shared scope 的 sanitized static config |

初始 TASKS frame 會先建立，不等待 runtime auto-detection、project-root lookup、
cache decode、shell tool probe 或 optional release refresh 完成。TASKS、REPOS 與
TRY 接著由同一個 shared local cycle 獨立發布；REMOTE、FLEET、SKILLS 與 MCP 維持
lazy。每個被請求的 view 都有自己的 generation：`r` 會 supersede 舊讀取，晚到
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
press 只選取 visible row，不會直接 open；wheel 每次移動三列；right-button press
會先選取 row，再開啟該 row 可用的 actions。Modified click、motion、release 與
repeated-click activation 都會被忽略，Enter/`o` 仍是 explicit open。Mouse tracking
啟用時，部分 terminal 的原生文字選取需要按住 Shift/Option。原有鍵盤移動、filter、
help 與 mode exit 都不變。

## 常用 actions

### TASKS

```text
enter/o   開啟選取的 task
p         park warm 並輸入 next action
c         編輯 next action
n/N       quick-add／瀏覽 repository notes
1/2/3     HOT/WARM/COLD filters
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

FLEET 同樣延遲載入。等待目前的 REPOS generation 被接受時，仍可使用 valid
cached rows；接受後會重用該 snapshot 作為 local host 資料，不再重掃
repositories，接著才 fan out 到設定的 SSH hosts。它預設隱藏本機，因為 REPOS
已提供較完整的 local inventory；按 `a` 可顯示或隱藏 local rows。按 `r` 會
supersede 舊 request 並強制執行 live reload。任何 endpoint field（包含 SSH
port）改變都會讓該 host cache 失效。這不會改變 `dev fleet list`：其
non-interactive output 仍包含本機與所有已設定的 remote hosts。

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
