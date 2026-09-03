---
description: 渲染 deterministic operational context，或交給 configured local agent，而不建立第二個 lifecycle authority。
authority: project
status: stable
verified_on: 2026-09-02
tested_with: OpenCode 1.18.25
lang: zh-TW
---

# Prompt handoff

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev prompt` 收集 read-only snapshot，放進 built-in prompt，再印出或交給你
設定的 local command。它是 context handoff，不是 agent loop、scheduler、
permission manager 或 lifecycle engine。

!!! info "時效"
    **Authority：**`internal/cli/prompt_command.go`、`internal/promptkit`、
    `internal/handoff`、`internal/closeout` 與 `internal/retire/audit.go` ·
    **Status：**stable · **Verified：**2026-09-02。

## 只升級到需要的層級

```text
dev status / dev sweep / dev repo context
    |
    +-- facts 已足夠 --> 使用 done、park、sweep 或 retire
    |
    +-- dev prompt render <recipe> --> 檢查或複製 exact prompt
    +-- dev prompt run <recipe>    --> bounded one-shot analysis，沒有 user stdin
    +-- dev prompt open <recipe>   --> 在目前 terminal 進行 foreground conversation
```

若下一步已清楚，優先使用 deterministic lifecycle command。只需要 context
供人或另一個工具閱讀時用 `render`；需要 finite unattended answer 時用 `run`；
只有 semantic question 需要對話時才用 `open`，例如決定 conflicted change 應有的意義。

三種 mode 收集相同 recipe context。接收端 agent 可以解釋或排序，但 `dev` 不會
parse reply、不會再跑下一輪，也不會把回答轉換為 mutation approval。

## 三個 recipe

```bash
dev prompt list
dev prompt list --json
dev prompt render pr-triage
dev prompt render session-close
dev prompt render workspace-closeout [repo-or-checkout]
```

| Recipe | Scope | Read-only result |
|---|---|---|
| `pr-triage` | account/local forge inbox | 將你開的或等你 review 的 request 排到 merge/review/fix/wait/inspect。接受與 `dev pr list` 相同的 query，以及 `--scope`、`--role`、`--state`、`--repo`、`--all-repos`、`--linked`、`--limit` filters。 |
| `session-close` | 這台機器目前選定的 runtime | 將 session 分成 runtime closure、先 checkpoint/park、保持開啟或 inspect。可用時包含 session/pane identity、recognized activity、matching task status 與 artifact status。 |
| `workspace-closeout` | 一個 repository | Join tasks、所有 known checkouts、live Git status、runtime coverage、artifacts 與 pull requests，並為每個 checkout 加入完整 read-only retirement audit。`--base` 為 unmanaged linked worktree 提供 audit base。 |

每份 rendered prompt 都有 JSON envelope，含 `schema_version: 1`、recipe/context
version、generation time、host、scope、適用時的 target、capabilities、warnings 與
recipe-specific context。收集到的 repository text、branch name、request title 與
note 都是 data，不是 instruction。Missing 或 failed evidence 會維持明確 capability
gap／warning，絕不變成看似安心的 clean/empty value。

## 選擇 render、run 或 open

| Mode | 需要 agent？ | Prompt transport | User stdin | Default timeout | Runtime behavior |
|---|---:|---|---|---|---|
| `render` | 否 | stdout | 不碰 | 無 | 不啟動 process，也不碰 runtime |
| `run` | 是 | `stdin`、private `file` 或單一 `argv` element | 絕不傳給 child | 10 分鐘 | 在 recipe working directory 等待一個 batch process |
| `open` | 是 | private `file` 或單一 `argv` element | attach 給 child | 無 | 在目前 terminal/TTY 跑一個 foreground process |

範例：

```bash
dev prompt render pr-triage --role reviewer --repo owner/api
dev prompt run session-close --agent my-agent
dev prompt run pr-triage --dry-run
dev prompt open workspace-closeout . --agent my-agent
dev prompt open workspace-closeout . --dry-run
```

`--dry-run` 解析 agent、working directory、transport、timeout 與 safe command
preview，再印出完整 rendered prompt，但不啟動 process，因此不建立 writer claim。
真正的 `run` 或 `open` 若 working directory 是 checkout，即使 recipe 要求 read-only
analysis，也會被視為 writer claim；shared-checkout occupancy guard 仍適用。這不是目前
agent 繼續自己的工作，而是新 agent claim，因此 invoking agent 的 pane **不會**被排除。
只有在協調好 disjoint ownership 後才可使用 `--allow-shared-checkout`。

除 dry-run 外，`open` 必須有 interactive terminal，而且不接受 timeout。只有 `run`
可設定 timeout，並在 absent/zero 時取得 10 分鐘 default。Batch timeout 到期時，`dev`
會終止 launcher process tree，避免 descendant agent 在 handoff 返回後仍繼續修改 checkout。

## Host-only agent configuration

沒有 built-in agent、vendor 或 default launcher。在 `dev config path` 回傳的 user
config 中設定一個或多個 local command：

```toml
[[agent]]
name = "my-agent"
default = true

[agent.run]
command = ["my-agent", "--batch"]
input = "stdin"
timeout = "10m"

[agent.open]
command = ["my-agent", "{{prompt_file}}"]
input = "file"
```

### 實際 OpenCode 範例

OpenCode 1.18.25 提供 `opencode run [message..]`；加上 `--interactive` 會保留可直接對話的 split-footer 模式。Workspace snapshot 可能很大，因此 file input 比把整份 prompt 放進 argv 更穩定：`-f/--file` attach 私有 prompt file，再用短 message 告訴 OpenCode 如何處理：

```toml
[[agent]]
name = "opencode"
default = true

[agent.run]
command = ["opencode", "run", "--file", "{{prompt_file}}", "Read the attached dev prompt and follow its instructions."]
input = "file"
timeout = "10m"

[agent.open]
command = ["opencode", "run", "--interactive", "--file", "{{prompt_file}}", "Read the attached dev prompt, explain the evidence, and ask before changing anything."]
input = "file"
```

這只是文件範例，不是 built-in default。OpenCode 升級後請重新檢查 `opencode run --help`；不要加 `--auto`，那會改變 configured agent 的 approval policy。

Selection 是 deterministic：

1. `--agent NAME` 選擇該 name（case-insensitive match）。
2. 省略時選唯一 `default = true` 的 entry。
3. 沒有 default 時，若只設定一個 `[[agent]]`，就選它。
4. 多個 entries 且沒有 default 時視為 ambiguous，列出 names 後失敗。

Name 必填、不能有 surrounding whitespace，並且 case-insensitively unique；最多一個
entry 可為 default。每個 agent 至少定義 `[agent.run]` 或 `[agent.open]` 其中之一；
要求未定義的 mode 會失敗，不會借用另一個 launcher。

Launcher fields：

| Field | Constraint |
|---|---|
| `command = ["program", "arg", ...]` | Direct argv；第一個 element 不可空白。`command` 與 `shell` 必須二選一。因不會呼叫 shell，所以優先使用。 |
| `shell = "static command"` | 透過 `$SHELL -c` 執行；必須是 static，且不能含 prompt placeholder。 |
| `input = "stdin"` | 僅限 `run`。Prompt 是 child 的 finite stdin；不能有 placeholder。 |
| `input = "file"` | `command` 必須有且只有一個完整 argv element 為 `{{prompt_file}}`；`shell` 必須引用 `$DEV_PROMPT_FILE` 或 `${DEV_PROMPT_FILE}`。 |
| `input = "argv"` | 僅限 `command`，且必須有且只有一個完整 argv element `{{prompt}}`。Rendered prompt 上限 100 KiB。 |
| `timeout = "10m"` | 僅限 `run`；optional non-negative duration，absent/zero 為 10 分鐘。`open` 拒絕 timeout，因為安全終止 interactive process tree 需要 terminal job control。 |
| `load_shell_rc = true` | Optional，僅可搭配 `shell`；改用 `$SHELL -lic` 而非 `-c`。 |

Placeholder 必須是完整 command element；`--prompt={{prompt}}` 這類 embedded form
會被拒絕。File transport 會建立 0700 temporary directory 與 0600 `prompt.md`，child
結束後移除。Prompt content 絕不 interpolated into shell text。

`[[agent]]` 是 machine-owned executable policy。Repository 的
`.dev-cli/config.toml` 不得定義它，因此 checkout project 不能替 `dev` 選擇要執行的 command。

## Permission 與 mutation boundary

Built-in recipe 要求接收端分析並 quote 可能的 next command，不要 approve、merge、
rebase、close、delete 或 retire。但 `dev prompt` 不是 sandbox：configured child 保留
其 command 原本給予的 filesystem、network、tool 與 approval policy。`dev` 不會增加
permissive mode、不會降低 permission、不會代答 approval prompt，也不會修改 config。

任何 agent answer 都不是 authority。Review 後由 operator 自己呼叫 `dev done`、
`dev park`、`dev sweep` 或 `dev retire`；這些 command 會在 mutation boundary 重新收集
並驗證 fresh state。

## Current-terminal 與 Herdr boundary

`dev prompt open` **不會**建立、focus、reuse 或 inject Herdr、tmux 或 Zellij
surface。它在呼叫它的 terminal foreground 啟動 configured process。在 Herdr 裡，
自然就是留在目前 pane。若想用另一個 Herdr pane，請手動建立或 focus 該 pane、在其中
進入 exact checkout，再從該 terminal 執行 `dev prompt open ...`。

這與 `dev start --run '<shell command>'` 分開。後者只可將 shell text dispatch 到
本次新建 first-class Herdr worktree 回傳的 exact root pane；reused、fallback、
unverified 與 non-Herdr surface 都 fail closed。`prompt open` 不會擴大或 reuse 這份
exact-pane contract。

## Closeout classification 的意義

`session-close` 只回報 **runtime closure**。Recognized covering agent 為 `idle` 或
`done` 時可能通過 activity gate，但只代表 current turn settled；它不證明 changes 已
commit、artifacts 已 finalize、review 已完成或 task intent 已 done。Caller-contained、
mixed-purpose、active、unrecognized 與 observation 不足的 session 都不會成為 close candidate。

`workspace-closeout` 範圍較完整，但仍只是 advisory。Retirement audit 會檢查 target
kind、worktree registration/path、status availability、cleanliness、in-progress Git
operation、known base/branch containment、task state、artifact reachability/finalization
與 runtime eligibility。只有 deterministic `retirement.status` 為 `eligible` 才能被建議
retire；即使如此也不是 authorization：`dev retire` 必須在 mutation 前重新收集並驗證。
Merged pull request 只是 evidence，不能替代任何 gate。

## Rebase conflict

先使用 deterministic transaction，不要先找 agent：

```bash
dev done <task> --ff
# 或更新目前 branch
dev git pull-rebase
```

若 Git 停在 conflict，留在該 exact checkout，從那裡開啟完整 workspace context：

```bash
dev prompt open workspace-closeout . --agent my-agent
```

用對話決定 semantic resolution。只有 operator 選擇後才 continue 或 abort Git rebase，
之後重新執行原本 lifecycle command，讓它取得 fresh state。Prompt launcher 絕不會自行
resolve conflict、選 continue/abort、force-push 或授予 cleanup permission。

## Scheduling 與 statelessness

`dev` 沒有 daemon、queue 或 scheduler。每次 `render`、`run`、`open` 都收集 fresh
snapshot，process 結束後不保存 agent answer。Scheduler 可以呼叫 non-interactive
`run`，但必須選 configured batch command，並接受其 timeout/error behavior。`open` 是
foreground TTY operation，不是 scheduled-job surface。

## 相關頁面

- [Pull request inbox](pull-request-inbox.zh-TW.md)
- [Agent-safe retirement](agent-safe-retirement.zh-TW.md)
- [平行 Agent 與 Runtime](parallel-agents-runtimes.zh-TW.md)
- [命令與設定](../reference/commands-config.zh-TW.md)
- [相容性與已知限制](../reference/compatibility.zh-TW.md)
