---
description: 尋找 dev-cli command groups、產生式精確 flags、configuration layers 與穩定 automation surfaces。
authority: project
status: generated-plus-authored
verified_on: 2026-08-28
lang: zh-TW
---

# 命令與設定

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

用人工整理的 map 理解 intent，再用 embedded generated reference 查精確 flags。Generated block 來自 binary 的 Cobra command tree，並由 `dev skill sync --check` 驗證。

## Command map

| 目標 | Commands |
|---|---|
| task lifecycle | `start`、`park`、`resume`、`done`、`retire`、`sweep`、`ls`、`status` |
| agent artifacts | `prepare`、`artifact finalize`、`artifact list` |
| guarded Git transactions | `git uncommit`、`git recommit`、`git pull-rebase`、`git amend-all`、`git setup` |
| linked worktrees | `wt list`、`wt create`、`wt open`、`wt rm`、`wt plan`、`wt provision` |
| repositories/remotes | `repo list`、`repo context`、`repo clone`、`repo open`、`repo new`、`repo sync`、`repo remote`、`repo mark` |
| repository quick notes | `note add`、`note list`、`note show`、`note search`、`note edit`、`note delete`、`note path`、`note reindex` |
| machine inventory | `bootstrap`、`adopt`、`doctor` |
| experiments | `try`、`tries …`、`graduate` |
| terminal UI | `tui`、`tui tools` |
| configuration/shell | `config init/show/path`、`shell-init`、completion |
| remote fleet | `fleet list`、`fleet status`、`fleet sync`、`fleet open`、`fleet config …` |
| agent skills | `skill list`、`skill add`、`skill update`、`skill install`、`skill sync`、`skill print` |
| generated policy/assets | `gitignore`、`skill install/sync` |
| activity/data | `journal`、`stats …`、`cache …` |
| help | `help [topic]` |

已安裝 binary 的精確資訊請執行 `dev <command> --help`；本站描述的是 freshness metadata 指定的 repository version。

## 高價值 structured interfaces

```bash
dev ls --json
dev repo list --json
dev repo context [repo]
dev repo remote --json
dev note list [repo] --json
dev note search <query> --json
dev note show <note-id> --json
dev bootstrap --json
```

不要解析 human table，應優先使用 JSON 或 agent-ready Markdown context。Table 針對 terminal 最佳化，columns/width 可能變化，但 structured contract 不一定改變。

每個 `dev repo list --json` row 都包含 `notes.count`。最新 note 存在時，同一 object 會加入 `notes.latest_id`、`notes.latest_preview` 與 `notes.latest_updated`；count 為零時省略這些 optional fields。`dev note list --json` 與 `dev note search --json` 回傳完整 note records 的 arrays，`dev note show --json` 則回傳一筆完整 record。

## `dev done` finish flags

`dev done` 對 branch/worktree task 只透過下列其中一種方式 integrate：`--ff`（rebase 到 base 再 fast-forward）或 `--pr`（push 並開啟 pull/merge request）。兩者都省略時，在 TTY 上會開啟 interactive finish wizard —— 提示內容見[變更流工作流程](../guides/change-stream-workflow.zh-TW.md)。

Dirty checkout 由 `--dirty <auto|fail|commit|discard>` 處理（預設 `auto`）：

| 值 | 行為 |
|---|---|
| `auto` | interactive：提示 commit 或 discard；non-interactive：直接失敗，等同 `fail` |
| `fail` | dirty checkout 時拒絕 finish |
| `commit` | 用 `--message`/`-m` commit 全部變更（未指定時 interactive 會提示輸入） |
| `discard` | reset tracked 變更並移除 untracked files；具破壞性，沒有 TTY 時需要 `--yes` |

`--yes`/`-y` 用來確認選定的 finish plan；non-interactive 的 `--dirty discard` 必須要有它，其他情況則是跳過 interactive 確認步驟。`--keep-worktree` 讓 `--ff` integration 後仍保留 worktree（預設 merge 後移除），`--push` 會一併 push 產生的 branch，`--delete-branch` 只在 branch 的 commits 已被 base 包含時才刪除它 —— 有 unpushed commits 的 branch 永遠不會被刪除。

## Configuration

```bash
dev config init
dev config show
dev config path
```

`config init` 偵測 local roots 並寫出 explicit defaults。Config file 不存在仍可運作；built-in defaults 讓核心 Git behavior 可用，但 generated config 能讓 machine policy 可 review，因此較推薦。

主要 sections：

| Section | 控制內容 |
|---|---|
| `[paths]` | scan roots、project/tries/worktree roots、worktree template、state path |
| `[runtime]` | `auto`、Herdr、tmux、Zellij 或 none，以及 metadata settings |
| `[worktree]` | ignored includes、linked dirs、setup commands、strategies、timeout |
| `[forge]` / `[[forge.azure_devops]]` | 完整 remote inventory 的 cache TTL，以及 opt-in Azure organization/project targets |
| `[bootstrap]` | recursion、symlink handling、index/layout policy |
| `[tui]` / `[[tui.tools]]` | columns、sorting 與 external-tool bindings |
| `[stats]` | sampler 與 optional WakaTime import |

Repository quick-note Markdown 是 configured `paths.state_dir/notes` 下的 durable data；該路徑預設為 `$XDG_DATA_HOME/dev/notes`。`$XDG_CACHE_HOME/dev/notes.db` 的 full-text index 是 disposable，會從 Markdown 重建；調整 `paths.state_dir` 不會移動 cache。

Repository 可 commit `.dev.toml`，保存應跟著 project 移動的 worktree provisioning overrides。Host-specific path 與 credential 應放 user config 或 ignored environment file，不放 repository override。

## 彩色輸出

Human-readable output（tables、`dev status`、`dev done` finish wizard、warnings 與 cobra help）會透過一組 semantic role 套用 ANSI color：`title`/`header`/`prompt`（bold cyan）、`label`/`dim`（dim）、`success`（green）、`warning`（yellow）、`danger`（bold red），以及代表 PR/review handoff 的 `review`（magenta）。Git-status 與 task-state 字串則依它自身的意義上色，而非固定 role —— `clean` 是 green，`dirty`/`ahead`/`behind`/`conflict` 依情況為 yellow 或 red。

用全域的 `--color <auto|always|never>` flag 控制（預設 `auto`）。`auto` 在 output 未連接 terminal、`NO_COLOR` 被設為任何非空值，或 `TERM=dumb` 時會停用 color。`--json` output 不論 mode 為何都不會上色。目前沒有對應的 config-file 欄位 —— `--color` 與 environment 是僅有的控制方式，因此 pipe `dev` 的輸出不需要額外傳 `--color never` 就是乾淨的。

## Shell integration

```bash
eval "$(dev shell-init zsh)"
dev shell-init fish | source
```

Child process 不能改變 parent working directory，因此受信任的 `shell-init` output 會定義 wrapper。Navigation command 執行時，wrapper 從 private child-only file descriptor 讀取 NUL-terminated path，再呼叫 `builtin cd`；它不會把一般 `dev` command output 當成 shell code evaluate。

## 完整 generated command reference

以下內容由 `internal/skill/dev-cli/references/commands.md` include，並跳過 generated-file preamble。CLI flags 與 command names 保留英文：

--8<-- "internal/skill/dev-cli/references/commands.md:7"

## 保持同步

```bash
go run ./cmd/dev skill sync --check
```

Command help 改變時透過 `dev skill sync` regenerate；不要手動修改 generated block。

## 來源

- [`internal/cli/root.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/root.go)
- [`internal/config/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/config/config.go)
- [`internal/skill/dev-cli/references/commands.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/commands.md)
- [`internal/cli/color.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/color.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
