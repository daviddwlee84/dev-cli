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
| task lifecycle | `start`、`park`、`resume`、`done`、`sweep`、`ls`、`status` |
| linked worktrees | `wt list`、`wt create`、`wt open`、`wt rm`、`wt plan`、`wt provision` |
| repositories/remotes | `repo list`、`repo context`、`repo clone`、`repo open`、`repo new`、`repo sync`、`repo remote`、`repo mark` |
| machine inventory | `bootstrap`、`adopt`、`doctor` |
| experiments | `try`、`tries …`、`graduate` |
| terminal UI | `tui`、`tui tools` |
| configuration/shell | `config init/show/path`、`shell-init`、completion |
| generated policy/assets | `gitignore`、`skill install/sync` |
| activity/data | `stats …`、`cache …` |
| help | `help [topic]` |

已安裝 binary 的精確資訊請執行 `dev <command> --help`；本站描述的是 freshness metadata 指定的 repository version。

## 高價值 structured interfaces

```bash
dev ls --json
dev repo list --json
dev repo context [repo]
dev repo remote --json
dev bootstrap --json
```

不要解析 human table，應優先使用 JSON 或 agent-ready Markdown context。Table 針對 terminal 最佳化，columns/width 可能變化，但 structured contract 不一定改變。

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
| `[forge]` / `[[forge.azure_devops]]` | remote result limit、cache TTL，以及 opt-in Azure organization/project targets |
| `[bootstrap]` | recursion、symlink handling、index/layout policy |
| `[tui]` / `[[tui.tools]]` | columns、sorting 與 external-tool bindings |
| `[stats]` | sampler 與 optional WakaTime import |

Repository 可 commit `.dev.toml`，保存應跟著 project 移動的 worktree provisioning overrides。Host-specific path 與 credential 應放 user config 或 ignored environment file，不放 repository override。

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
