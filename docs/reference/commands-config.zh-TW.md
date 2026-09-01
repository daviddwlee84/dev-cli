---
description: 尋找 dev-cli command groups、產生式精確 flags、configuration layers 與穩定 automation surfaces。
authority: project
status: generated-plus-authored
verified_on: 2026-08-31
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
| agent artifacts | `prepare`、`artifact finalize`、`artifact list`、`artifact discard` |
| guarded Git transactions | `git uncommit`、`git recommit`、`git pull-rebase`、`git amend-all`、`git setup` |
| linked worktrees | `wt list`、`wt create`、`wt open`、`wt rm`、`wt plan`、`wt provision` |
| repositories/remotes | `repo list`、`repo context`、`repo new`/`repo create`、`repo clone`、`repo setup`、`repo open`、`repo sync`、`repo remote`、`repo mark` |
| repository quick notes | `note add`、`note list`、`note show`、`note search`、`note edit`、`note delete`、`note path`、`note reindex` |
| machine inventory | `bootstrap`、`adopt`、`doctor` |
| experiments | `try`、`tries …`、`graduate` |
| terminal UI | `tui`、`tui tools` |
| configuration/shell | `config init/show/path/edit/trust`、`config scaffolds init/show/path/edit`、`shell-init`、completion |
| remote fleet | `fleet list`、`fleet status`、`fleet machine-id`、`fleet sync`、`fleet files`、`fleet open`、`fleet config …` |
| agent skills | `skill list`、`skill add`、`skill update`、`skill install`、`skill sync`、`skill print` |
| generated policy/assets | `gitignore`、`skill install/sync` |
| activity/data | `summary`、`journal`、`stats …`、`cache …` |
| help | `help [topic]` |

已安裝 binary 的精確資訊請執行 `dev <command> --help`；本站描述的是 freshness metadata 指定的 repository version。

## 高價值 structured interfaces

```bash
dev ls --json
dev repo list --json
dev repo context [repo] --json
dev repo remote --json
dev fleet machine-id <host> --json
dev fleet files [repo-or-path] --to <host> --json
dev repo new NAME --json
dev repo clone <ref> --json
dev repo setup [repo-or-path] --preset PRESET --json
dev note list [repo] --json
dev note search <query> --json
dev note show <note-id> --json
dev bootstrap --json
```

不要解析 human table，應優先使用 JSON 或 agent-ready Markdown context。Table 針對 terminal 最佳化，columns/width 可能變化，但 structured contract 不一定改變。`repo context --json` 是 additive schema-v1 report：unavailable facts 保持 null/error entries 並附 explicit provenance，不會變成 zero values。`fleet files --json` 是 content-free output，絕不包含 file hash 或 body。

每個 `dev repo list --json` row 都包含 `notes.count`。最新 note 存在時，同一 object 會加入 `notes.latest_id`、`notes.latest_preview` 與 `notes.latest_updated`；count 為零時省略這些 optional fields。`dev note list --json` 與 `dev note search --json` 回傳完整 note records 的 arrays，`dev note show --json` 則回傳一筆完整 record。

## Repository bootstrap

Repository setup 仍與 acquisition 分開。Acquisition 部分，`new`/`create` 會區分
plain repository name 與清楚的 clone reference：

| Command | 行為 |
|---|---|
| `dev repo new` | interactive wizard；第一個欄位可輸入新 name 或 clone reference |
| `dev repo new NAME` / `dev repo create NAME` | 建立新的 local repository；若輸入清楚的 Git URL、local Git path 或 owner/name，則改走 clone 並保留 history/remote |
| `dev repo clone [owner/name\|url]` | clone 至 configured destination，再選擇是否套 preset；setup 預設關閉 |
| `dev repo setup [repo-or-path]` | repeat-safely merge native initializers 與 preset files 至既有 clean checkout；預設目前 repository，沒有明確要求時不 commit |

改走 clone 的 `new` 會保留 source `origin`，因此拒絕 new-upstream creation flags；也會
拒絕 `--template*`，因為後者的明確語意是「把 content 複製進 fresh history」。

這些 commands 的 controls 包含 `--preset`、`--path`、用 `--set` 傳入 typed input、用
`--enable`/`--disable` 選擇 item、`--check-in <auto|commit|stage|none>`、
`--dry-run`、`--yes`、`--json`，以及 `--handoff <stay|cd|open|start>`。
`repo new` 另支援 `--template`、`--template-ref`、`--template-subdir`。JSON mode
不互動，也不會改變 directory 或開啟 runtime。Dry-run 不會修改 target repository；
clone setup 必須等 clone 存在後才能產生詳細 plan。每個 command 實際支援的精確 flags
請看下方 generated reference。

Wizard 會在 target repository mutation 前顯示 selected scaffold 與 workflow
summary。Built-in presets 為：

- `minimal`：`main`、README 與 initial commit；保留既有 scripted
  `repo new NAME` behavior。
- `agent-ready`：在 `minimal` 上加入 common ignores、starter `AGENTS.md`，以及
  project-scoped `.claude/settings.json` 與 `.claude/plans/`。
  `agent-history-hygiene`、`project-knowledge-harness` 會出現在選項中，但不會靜默
  啟用。選取後 dev 會安裝 skill，並在 initial commit 前執行經 review 的內建
  initializer 來建立對應 project surfaces；這兩個 built-ins 不會執行剛下載的
  skill scripts。同一 source 且 agent targets 完全相同的 skills 會共用一次 installer
  invocation，各 skill 的 setup phase 仍分別執行。History initializer 會建立
  `.pre-commit-config.yaml`、`.gitleaks.toml`，再確保 `.specstory/.gitignore` 含有
  SpecStory 的 `.project.json`、`statistics.json` 規則，不會忽略應納入追蹤的
  `.specstory/history/`。既有 custom ignore content 與 mode 會保留，只補上缺少的
  managed rules。

選完 preset 後，new-repository wizard 會詢問預設為 no 的「Customize preset and
template options?」。一般 `agent-ready` flow 會直接使用 reviewed template、file、input
與 skill defaults，不逐一顯示問題；回答 yes（或傳入 customization flags）才會展開。

Preset 可加入 `string`、`bool`、`choice` typed inputs、text templates、hooks 與
project skills。Hooks 依固定 `before_commit`、`after_commit`、`after_remote` phases
執行。安全形式是 argv `command`；shell `run` 必須明確宣告，且只有
`interactive = true` 才載入 interactive shell。Required failure 會停止後續
commit/remote steps；optional failure 只回報 warning。已產生的 local files 會保留供
recovery。Native initializers 與 preset files 可 repeat-safe 套用；custom hooks 與
skill setup 必須自行保證 idempotency。

### Snapshot templates

`dev repo new NAME --template SOURCE` 會用 content snapshot 建立全新 repository。
`SOURCE` 可為 local directory/repository、Git URL 或 owner/name。Git source 可用
`--template-ref` 指定任意 branch、tag 或 commit；`--template-subdir` 則指定作為新
repository root 的乾淨 relative directory。未指定 ref 時，local Git working tree 會
包含現存 tracked files，以及未被 Git ignore 的 untracked files；ignored build/cache
content 會省略。Non-Git directory 則 snapshot 完整 current tree。

新 repository 不會繼承 source history 或 remotes。Dev 會排除每個 source `.git`
entry、拒絕 traversal、symlink 與 special files、保留 regular-file modes，並在建立
destination 前驗證完整 snapshot。若 snapshot 與 selected scaffold 寫入同一路徑，
snapshot 優先；scaffold 仍會補齊缺少的 files 並執行 selected initializers。
Source 與 destination traversal 都相對於 held `os.Root` handles 進行；file bytes 來自
已開啟且驗證過的 source handle，不會再次依 mutable pathname lookup。

Confirmation 與 human dry-run output 會顯示 bounded selected-path preview；local source
若是 live working-tree/directory snapshot 而非 commit，也會明確 warning。含 credential
的 URL userinfo 會從 summaries、structured output 與 clone/template errors 移除。

Preset 可用 scalar `template`、`template_ref`、`template_subdir` 宣告同一操作。它們依
一般 inheritance 規則 resolve，explicit CLI flags 優先，因此可以一個 starter catalog
repository 搭配每個 subfolder 各一個 child preset。

### Check-in policy

Interactive wizard 提供 `commit`、`stage`、`none`；script 也可傳 `auto`：

| 值 | 行為 |
|---|---|
| `commit` | `git add -A`，以 `--message`／preset message commit，再執行 `after_commit` setup |
| `stage` | 執行 `before_commit` setup 後 `git add -A`；保留 staged checkout，不執行 `after_commit` |
| `none` | generated changes 保持 unstaged、uncommitted |
| `auto` | `repo new` 使用 `initial_check_in` 與相容的 `initial_commit`；clone/setup 則不自動 check-in |

`stage` mode 會 best-effort 在正確的 worktree Git directory 寫入
`LAZYGIT_PENDING_COMMIT`。[Lazygit v0.59.0 會讀取此
file](https://github.com/jesseduffield/lazygit/blob/v0.59.0/pkg/gui/controllers/helpers/working_tree_helper.go#L191-L216)，並將它作為小寫 `c` 的 initial message；大寫
`C` 與 Git 本身不使用此 integration。不同內容的既有 draft 會保留並回報 warning，
不會被覆寫。這是 implementation-detail adapter，不是 `commit.template`。

Staging 才是 durable outcome：optional lazygit draft 若無法寫入，dev 會發出 warning
並保留 staged index，不會 rollback。

Staged setup 不能建立 upstream；`handoff=start` 也要求已 commit 的 setup，因為新
worktree 會漏掉 staged files。`stay`、`cd`、`open` 仍可供 review。`repo setup
--commit` 保留為 `--check-in=commit` 的 compatibility alias；`--message` 同時適用於
commit 與 stage。Structured result 保留 `committed`，並在適用時加入 `staged`、
`staged_paths`、`commit_message`、`commit_draft_provider` 與不含 file content 的
`template` summary。

### Upstream publishing

提供 publishing 選項前，dev 會以 read-only 方式 probe `gh` 與 `glab`。只有 CLI 已
安裝且完成 authentication 的 provider 才會出現；否則 wizard 顯示對應的安裝或 login
指引。預設 local-only，新 publish 的 repository 預設 private。

Publishing 會沿用 local repository name 與 configured description，再詢問 provider
namespace/owner、visibility，以及是否 push initial/current branch。Dev 會在必要的
local setup 與 commit steps 成功後建立空的 GitHub 或 GitLab repository，再新增／驗證
`origin`，並可選擇以 upstream tracking push。Provider、name-conflict 或 push failure
絕不刪除 local checkout，也不會刪除已建立的 upstream。

從 `repo setup` publish 時必須使用 `--check-in=commit`（或相容的 `--commit` flag），
避免新建 upstream 漏掉本次產生的 setup changes。`--check-in=stage` 與任何新 upstream
不相容；對新 repository 而言，`none` 只有搭配 `--push=false` 才能建立刻意保持空白的
upstream。

### Handoff

`stay` 只輸出結果；`cd` 透過受信任的 `shell-init` wrapper 改變 parent shell；
`open` 開啟 configured Herdr/tmux/Zellij runtime，runtime 為 `none` 時退回 `cd`；
`start` 會固定此 repository 並接續現有 task wizard。Setup 留下 uncommitted files、
導致新 worktree 會遺漏它們時，`start` 不可用。Repository bootstrap 與
`dev start` 都不會啟動 coding agent。

Repository、task-start 與 finish wizard 共用的 TTY text fields 都使用 inline editor：
Left/Right、Home/End、Delete/Backspace、cursor 位置插入與 Esc/Ctrl-C cancellation 會
被解讀為 terminal actions，不會成為 raw escape bytes。Buffered 或 piped non-TTY input
仍維持 line-oriented behavior。

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
dev config scaffolds init
dev config scaffolds show
dev config scaffolds path
dev config scaffolds edit
```

`config init` 偵測 local roots 並寫出 explicit defaults。Config file 不存在仍可運作；built-in defaults 讓核心 Git behavior 可用，但 generated config 能讓 machine policy 可 review，因此較推薦。

主要 sections：

| Section | 控制內容 |
|---|---|
| `[paths]` | scan roots、project/tries/worktree roots、worktree template、state path |
| `[runtime]` | `auto`、Herdr、tmux、Zellij 或 none，以及 metadata settings |
| `[worktree]` | local ignored-file provisioning、linked dirs、setup commands、strategies、timeout |
| `[local_files]` | portable files 的 host ceilings；project overlay 提供獨立的 off-machine candidate allowlist |
| `[forge]` / `[[forge.azure_devops]]` | 完整 remote inventory 的 cache TTL，以及 opt-in Azure organization/project targets |
| `[bootstrap]` | recursion、symlink handling、index/layout policy |
| `[tui]` / `[[tui.tools]]` | columns、sorting 與 external-tool bindings |
| `[stats]` | sampler 與 optional WakaTime import |
| `[update]` | `check`（預設 `true`）— 允許每天一次的「有新版」提示與其背景 cache refresh；`DEV_NO_UPDATE_CHECK` 可覆寫 |

Repository quick-note Markdown 是 configured `paths.state_dir/notes` 下的 durable data；該路徑預設為 `$XDG_DATA_HOME/dev/notes`。`$XDG_CACHE_HOME/dev/notes.db` 的 full-text index 是 disposable，會從 Markdown 重建；調整 `paths.state_dir` 不會移動 cache。

### Scaffold presets

Global repository recipes 位於 `$XDG_CONFIG_HOME/dev/scaffolds.toml`（或 root
`--scaffolds` override）。每個 authored file 都必須宣告 `version = 1`。精簡 preset
範例：

```toml
version = 1
default_preset = "team"
default_agents = ["claude-code", "codex"]

[presets.team]
extends = "agent-ready"
handoff = "cd"
initial_check_in = "stage"
template = "acme/starter-catalog"
template_ref = "v2"
template_subdir = "services/go"

[[presets.team.inputs]]
id = "deployment"
type = "choice"
choices = ["none", "docker"]
default = "none"

[[presets.team.files]]
id = "service-readme"
source = "service/README.md" # 此檔案旁的 templates/service/README.md
destination = "docs/service.md"

[[presets.team.hooks]]
id = "verify"
phase = "before_commit"
command = ["make", "test"]
required = true

[[presets.team.skills]]
id = "knowledge"
source = "daviddwlee84/agent-skills/skills"
name = "project-knowledge-harness"
agents = ["claude-code", "codex"]
default = true

[presets.team.skills.setup]
phase = "before_commit"
interpreter = "bash"
script = "scripts/init.sh"
args = ["--target", "{{path}}", "--project-name", "{{name}}"]
required = true
```

Skill setup 一般會指定 installed skill 內的 project-local script。內建推薦項目則使用
`builtin = "agent-history-hygiene"` 或
`builtin = "project-knowledge-harness"`；這些固定且經 review 的 initializer 不會執行
下載回來的 skill code。

Preset 最多 extend 一個 parent。Scalar 會 override；simple lists 會 replace；files、
hooks 與 skills 依 `id` merge，inherited item 可用 `enabled = false` 停用。
`[[presets.*.files]]` 的 rendering source 必須留在 config source 旁的 `templates/`
tree；這個 file-level mechanism 與前述 repository snapshot `template` scalar 不同。
Destination 必須留在 repository 內，skill setup script 也必須位於 installed skill
directory 內。`initial_check_in` 接受 `commit`、`stage`、`none`；legacy
`initial_commit` boolean 仍可讀，但同一 preset 不可同時設定兩者。

### Safe project overlays

Repository 可 commit 兩個固定檔案：

- `.dev-cli/config.toml`：allowlisted worktree provisioning、分開提出的 portable
  local-file patterns 與 repository setup wizard defaults。
- `.dev-cli/scaffolds.toml`：使用同一 versioned schema 的 project presets、
  templates、hooks 與 skill setup。

```toml
# .dev-cli/config.toml
version = 1

[worktree]
include = [".env.example"]
strategy = "reinstall"

# 只提出 candidates；export 仍需 explicit fleet files --to。
[local_files]
include = [".env", ".mcp/**"]

[repo.setup]
preset = "team"
handoff = "cd"
check_in = "stage"
```

Effective precedence 由低至高為 built-ins、global config/scaffolds、legacy
`.dev.toml`、target repository 的 `.dev-cli/*`，最後是 explicit CLI 或 wizard
choices。`.dev.toml` 為相容性仍可讀；新的 project configuration 應使用
`.dev-cli/config.toml`。Global `default_preset` 與 project `[repo.setup]` 的 preset、
handoff、check-in fields 只用來預填 interactive wizard choices；它們不會改變
scripted defaults，後者由對應 flags 控制。Legacy `commit` boolean 仍可讀，但同一
layer 不可與 `check_in` 並用。

`[local_files].include` 絕不繼承 `[worktree].include`：佈建 local checkout 不等於授權 export secret。Project overlay 只能提出 portable include list；host-owned count/size/path ceilings 來自 global config，repository 不能提高。Command 仍保持 report-only，直到 explicit `--apply`、target pin 與 confirmation 都具備。

Project files 不能 override host paths、state location、runtime backend、forge
inventory 或 credentials、stats、update、bootstrap 或 TUI policy，也不能靜默 publish
repository。執行來自 `.dev-cli/config.toml` 的 post-create command，或來自 project
`.dev-cli/scaffolds.toml` 的 hook／skill setup 前，dev 會依 canonical repository 加
execution-content hash 詢問 trust；executable content 變更後必須重新同意，
non-interactive 且沒有 matching trust record 時 fail closed。Legacy `.dev.toml` 保留其
compatibility behavior。Credential 與 host-specific path 應放 user config 或 ignored
environment file，絕不放 committed project overlay。

Project-authored skill setup 必須使用 local source，讓實際 bytes 能納入 trust hash。
Remote project skill 仍可安裝，但不能宣告 executable setup；global preset 則仍屬於
host-owned policy。

## 彩色輸出

所有 human-readable 介面都透過一組 semantic role 套用 ANSI color：`title`/`header`/`prompt`（bold cyan）、`label`/`dim`（dim）、`success`（green）、`warning`（yellow）、`danger`（bold red）、代表 PR/review handoff 的 `review`（magenta），以及 Markdown 用的 `strong`/`code`。本身帶有意義的值依其意義上色，而非固定 role：

| 值 | 綠色 | 黃色 | 紅色 |
|---|---|---|---|
| Git status | `clean` | `dirty`、`ahead`、`behind`、`no checkout` | `conflict`、`error` |
| Task state | `hot`、`done` | `warm`、`cold`、`parked` | — |
| Fleet host | `ok` | `stale`、`no-dev` | `unreachable`、`timeout`、`incompatible` |
| Skill update | `current` | `update` | `missing`、`failed` |
| Artifact intent | `finalized` | `armed`、`finalizing` | `failed` |

`dev journal` 與 `dev summary` 輸出 Markdown，因此其 heading 與 fenced code block 的樣式與 `dev help <topic>` 的 quick-reference 頁面相同。在 command help 中只有你會實際輸入的名稱 —— command 名稱與 flag spec —— 會上色；description 保持原色，且 cobra 計算出的欄位對齊不受影響，因為 terminal 不會給 escape sequence 任何寬度。

用全域的 `--color <auto|always|never>` flag 控制（預設 `auto`）。`auto` 在 output 未連接 terminal、`NO_COLOR` 被設為任何非空值，或 `TERM=dumb` 時會停用 color。此設定同樣會傳到 interactive dashboard，因此 `dev --color never` 也會讓儀表板不上色。`--json` output 不論 mode 為何都不會上色。目前沒有對應的 config-file 欄位 —— `--color` 與 environment 是僅有的控制方式，因此 pipe `dev` 的輸出不需要額外傳 `--color never` 就是乾淨的。
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
- [`internal/scaffold/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/scaffold/types.go)
- [`internal/projectconfig/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/projectconfig/types.go)
- [`internal/skill/dev-cli/references/commands.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/commands.md)
- [`internal/cli/color.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/color.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
