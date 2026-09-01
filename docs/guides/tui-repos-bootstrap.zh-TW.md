---
description: 在 TUI 瀏覽 tasks、repositories、experiments、remotes 與 agent skills、記錄 repository quick notes，並安全地 inventory 或 adopt 現有工作。
authority: project
status: evolving
verified_on: 2026-08-31
lang: zh-TW
---

# TUI、Repository、Quick Notes 與 Bootstrap

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Standard input/output 都是 terminal 時，直接執行 `dev` 會開啟 interactive dashboard；透過 pipe 執行時會輸出 plain task listing，讓 shell composition 保持可預期。

## 六個 view

| View | 回答問題 | 來源 |
|---|---|---|
| TASKS | 我正在處理什麼？ | task registry 加即時 Git/runtime facts |
| REPOS | 本機有哪些持久 repositories？ | 設定的 scan roots 與 local catalog |
| FLEET | 這份工作位於哪些 configured machines？ | private cached/live SSH snapshots |
| TRY | 哪些 experiments 能 resume、archive 或 graduate？ | experiment catalog 加 live facts |
| REMOTE | 有哪些 repository 能 open 或 clone？ | authenticated `gh`/`glab` inventories 與 cache |
| SKILLS | Project 與 global scope 安裝了哪些 agent skills？ | upstream `skills` JSON 加 project/global locks |

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

COLD task 必須透過 `dev resume` 重建；TUI 不會用 generic open action 靜默重建。

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

`dev repo context [repo]` 會輸出 TUI copy menu 相同的 agent-ready Markdown context，包含 paths、Git/worktree/runtime facts 與 tasks。`--json` 加入 schema-v1 evidence/readiness contract；只有 `--refresh` 會 live-probe optional forge 與 configured fleet sources。

### TRY 與 REMOTE

TRY 管理低成本 experiment、可逆 archive/restore、mark 與 graduation。Archive 是整理，不是 deletion 或 disk reclamation。

REMOTE 延遲載入，因此 startup 不等待 network。Private XDG cache 保存完整的
paginated inventory；fresh rows 不需要 network，stale rows 仍可搜尋並在背景
refresh。Enter 開啟 local clone；`c` 在 clone 缺少的 repository 前確認；`r`
強制更新 forge inventories。使用 `/vis:private` 可精確過濾 visibility。只有 REMOTE
row 能解析到 local clone 時才能使用 notes。TRY 保留 lowercase `n` 建立新 Try，
不會改成 repository note。

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
path、manager 與 update state。`r` 只重讀 local state；`c` 明確執行唯讀
source check；`a` 以 `daviddwlee84/agent-skills/skills` 為預設 source 開啟
upstream interactive installer；`u` 先確認，再只更新選取且受 lock 管理的
skill。Structured filters 包含 `scope:`、`agent:` 與 `update:`。

## External tools

```bash
dev tui tools
```

設定的 tool 會在暫停 alternate screen 後，透過 `$SHELL` 於選取 checkout 執行。`interactive = true` 使用 `$SHELL -lic`，讓 local aliases/functions 能解析；跨機器需要 portability 時應使用 `PATH` 中的真實 executable。

```toml
[[tui.tools]]
key = "L"
name = "lazygit"
run = "lazygit"
```

Key 區分大小寫，且不能覆蓋 dashboard-owned binding。離開 editor 後可 reload 多數 config；更換 runtime backend 需要重啟 TUI。

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
- [`internal/help/topics/notes.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/notes.md)
- [`internal/cli/note.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/note.go)
- [`internal/help/topics/bootstrap.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/bootstrap.md)
- [`internal/cli/adopt.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/adopt.go)
- [`internal/cli/bootstrap.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/bootstrap.go)
