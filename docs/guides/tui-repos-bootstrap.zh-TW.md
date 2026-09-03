---
description: 在 dashboard 瀏覽 tasks、repositories、fleet hosts、experiments、remotes 與 agent skills、記錄 quick notes、inventory/adopt 現有工作，並以獨立 dev flow 檢查 guarded lifecycle。
authority: project
status: evolving
verified_on: 2026-09-01
lang: zh-TW
---

# TUI、Repository、Quick Notes 與 Bootstrap

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Standard input/output 都是 terminal 時，直接執行 `dev` 會開啟 interactive dashboard；透過 pipe 執行時會輸出 plain task listing，讓 shell composition 保持可預期。

`dev flow [repo]` 是另一個獨立、僅限 TTY 且標示為 preview 的全螢幕介面，不是 dashboard 的第七個 view。它聚焦單一 canonical repository 的所有 registered worktrees 與 task-only rows，並把 lifecycle intent、live evidence 與 plan-first actions 並列；完整說明見 [Repository Flow 預覽](repository-flow.zh-TW.md)。

## 六個 view

| View | 回答問題 | 來源 |
|---|---|---|
| TASKS | 我正在處理什麼？ | task registry 加即時 Git/runtime facts |
| REPOS | 本機有哪些持久 repositories？ | 設定的 scan roots 與 local catalog |
| FLEET | 設定的機器上有哪些 repository 與 active work？ | 已接受的 local REPOS snapshot 加上透過 SSH 取得的 remote `dev` snapshots |
| TRY | 哪些 experiments 能 resume、archive 或 graduate？ | experiment catalog 加 live facts |
| REMOTE | 有哪些 repository 能 open 或 clone？ | authenticated `gh`/`glab` inventories 與 cache |
| SKILLS | Project 與 global scope 安裝了哪些 agent skills？ | upstream `skills` JSON 加 project/global locks |

初始 TASKS frame 會先建立，不等待 runtime auto-detection、project-root lookup、
cache decode、shell tool probe 或 optional release refresh 完成。TASKS、REPOS 與
TRY 接著由同一個 shared local cycle 獨立發布；REMOTE、FLEET 與 SKILLS 維持
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

用 `tab`、`h`/`l` 或左右鍵切換；`j`/`k`、`g`/`G`、`ctrl+d`/`ctrl+u` 移動；`/` filter；`?` 開 help；`esc`/`q` 離開目前 mode。

## 常用 actions

### TASKS

```text
enter/o   開啟選取的 task
p         park warm 並輸入 next action
c         編輯 next action
n/N       quick-add／瀏覽 repository notes
1/2/3     HOT/WARM/COLD filters
```

COLD worktree task 必須透過 `dev resume` 重建；TUI 不會用 generic open action 靜默重建。若 worktree 已遺失或不再由 Git 註冊，必須先執行 `dev sweep`，讓它在 resume 或 reap 前回報需要 salvage 的 agent artifacts。Enter 不會開啟只剩 artifacts 的 abandoned directory。

### REPOS

```text
enter/o   ad-hoc open，不建立 task
space     展開 linked worktrees
m         編輯 repository tags/summary
n/N       quick-add／瀏覽 repository notes
d         追蹤目前 branch 的 direct work
s         啟動 isolated work：branch + worktree + provisioning + runtime
H         開啟 repository activity heatmap
y         開啟 copy/context actions
```

展開後會顯示每個 linked worktree，包括 harness-owned `(ephemeral)` 與未受管理的 `(external)` checkout。LIVE column 將 runtime activity 與 task state 分開。

`dev repo context [repo]` 會輸出 TUI copy menu 相同的 agent-ready Markdown context，包含 paths、Git/worktree/runtime facts 與 tasks。

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
推測 host；成功但為空的 inventory 會清除舊 rows。Enter 開啟 local clone；`c` 在
clone 缺少的 repository 前確認，且 destination 受限於 `project_root`；`r`
強制更新 forge inventories。使用 `/vis:private` 可精確過濾 visibility。只有 REMOTE
row 能解析到 local clone 時才能使用 notes。TRY 保留 lowercase `n` 建立新 Try，
不會改成 repository note。

### FLEET

FLEET 同樣延遲載入。等待目前的 REPOS generation 被接受時，仍可使用 valid
cached rows；接受後會重用該 snapshot 作為 local host 資料，不再重掃
repositories，接著才 fan out 到設定的 SSH hosts。它預設隱藏本機，因為 REPOS
已提供較完整的 local inventory；按 `a` 可顯示或隱藏 local rows。按 `r` 會
supersede 舊 request 並強制執行 live reload。任何 endpoint field（包含 SSH
port）改變都會讓該 host cache 失效。這不會改變 `dev fleet list`：其
non-interactive output 仍包含本機與所有已設定的 remote hosts。

## Repository quick notes

在 TASKS 與 REPOS，lowercase `n` 開啟單行 quick-add prompt，uppercase `N` 開啟選取 repository 的 notes overlay。Child worktree 會透過 catalog identity 解析到同一個 canonical repository。

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

SKILLS 同樣延遲載入，因此 dashboard startup 不會啟動 Node。同名 skill
在 project 與 global scope 會保留為不同列，並顯示 target agents、source、
path、manager 與 update state。`r` 只重讀 local state；local snapshot ready 後，
`c` 才明確執行唯讀 source check（更早按 `c` 會等待，不會取消 inventory）；`a` 以 `daviddwlee84/agent-skills/skills` 為預設 source 開啟
upstream interactive installer；`u` 先確認，再只更新選取且受 lock 管理的
skill。Structured filters 包含 `scope:`、`agent:` 與 `update:`。

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

Key 區分大小寫，且不能覆蓋 dashboard-owned binding。離開 editor 後可 reload 多數 config；更換 runtime backend 需要重啟 TUI。

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

- [`internal/help/topics/tui.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/tui.md)
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/flowtui`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/flowtui)
- [`internal/help/topics/notes.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/notes.md)
- [`internal/cli/note.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/note.go)
- [`internal/help/topics/bootstrap.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/bootstrap.md)
- [`internal/cli/adopt.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/adopt.go)
- [`internal/cli/bootstrap.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/bootstrap.go)
