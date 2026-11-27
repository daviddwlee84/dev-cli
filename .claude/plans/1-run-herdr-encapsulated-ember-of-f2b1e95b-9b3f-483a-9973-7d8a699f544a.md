# herdr-core 支援：宿主偵測、Open target picker、Go-to-folder、`herdr-core` runtime backend

## Context 背景

現在的 Herdr 生態有三個不同的產品，但 dev 只認得第一個：

| 產品 | 環境標記 | 控制介面 |
|---|---|---|
| herdr（classic TUI multiplexer，v0.9.5） | `HERDR_ENV=1`、`HERDR_PANE_ID`… | `herdr workspace …`（現有 `internal/runtime/herdr.go`） |
| herdr-core（`hc`，v0.1.17）+ 官方 **Herdr Desktop**（`herdr-app`，v0.1.18） | `HC_TERMINAL_ID`、`HC_TERMINAL_SOCKET`、`HC_SCOPE`；terminal labels 有 `herdr.space` / `herdr.tab` | `hc ls/get/run/label --json`、`herdr-app ls`、`herdr-app do space.new/space.select/…` |
| **Sheprd**（第三方 GUI，跟 Herdr Desktop 是不同產品） | 自己的 control socket `~/.local/state/herdr/gpui/control.sock` | `sheprd open DIR`、`sheprd status --json` |

問題：
1. 在 herdr-core terminal 裡（沒有 `HERDR_ENV`），TUI 按 Enter 時，`runtime.Select("auto")` 會選到 herdr，先建立 herdr workspace，然後 `Herdr.Activate`（`internal/runtime/herdr.go:370`）因為 `HERDR_ENV` 是空的，會**在 GUI pane 裡 attach 一個 herdr TUI client**。此外 Enter 只會開到一個目標。
2. TUI 沒有獨立的「cd 到資料夾」action。只有 runtime 為 `none` 時，Enter 才會透過 `m.chosen` → `app.cdDirective` 進行 cd。
3. 完全沒有 hc 整合：不會用到 hc 的 terminal/agent 觀察，也不會用到 Herdr Desktop 的 space。

目標：正確偵測宿主；Enter 改成 picker，可選 runtime、Herdr Desktop、herdr TUI、Sheprd 或 cd；新增 Go-to-folder 快捷鍵；新增完整的 `herdr-core` runtime backend。

## Phase 1 — 宿主偵測 Host context detection

新檔 `internal/runtime/host.go`：

```go
type HostKind string // "herdr", "herdr-core", "tmux", "zellij", "plain"
type HostContext struct {
    Kind       HostKind
    GUI        string // "herdr-desktop", "sheprd", "unknown", ""
    TerminalID string // HC_TERMINAL_ID
    Socket     string // HC_TERMINAL_SOCKET
    Evidence   []string
}
func DetectHost(ctx context.Context) HostContext
```

- 判斷順序：`HERDR_ENV=1` → herdr；`HC_TERMINAL_ID` 有值 → herdr-core；`TMUX` → tmux；`ZELLIJ` → zellij；都沒有 → plain。若同時有多個標記（例如 herdr-core 裡又開 tmux），以最內層為準，並把所有證據記進 `Evidence`。
- 判斷 GUI：只跑一次 bounded `hc get $HC_TERMINAL_ID --json`（timeout 約 1s）。labels 有 `herdr.space` 就是 `herdr-desktop`。若 Sheprd control socket 存在（只做 stat，不執行任何東西），且 `sheprd status --json` 能回報這個 terminal，就是 `sheprd`。其他情況一律是 `unknown`，不猜測（fail closed，unknown 不會變成某個 fact）。
- 呈現：`dev doctor` 的 runtime 區段加一行 `host: herdr-core (Herdr Desktop, t2)`。TUI status/header 也顯示 host。
- 測試：用注入 env 加 fake `hc` binary 的 table test（沿用 `internal/runtime/native_fixture_test.go` 的 fixture 方式）。

## Phase 2 — `herdr-core` runtime backend

新檔 `internal/runtime/herdrcore.go`，實作 `Runtime`、`Activator`、`AgentActivityLister`、`CurrentPaneResolver`。

- `Name()`：`"herdr-core"`。`Available()`：PATH 上有 `herdr-core` 或 `hc` binary（注意使用者的 `hc` 可能是 shell alias，所以優先找 `herdr-core`），而且 `HC_TERMINAL_SOCKET` 存在，或 `hc server status --json` 回報正在執行。
- `List`：`hc ls --json`，terminal 依 label `herdr.space` 分組成 `Session`。`Handle` 是 space id；沒有 space 的 terminal 自成一個 session，`Handle` 用 terminal address。`Dirs` 包含 `cwd` 和 `foreground.cwd`。`Panes[].TerminalID` 用 address，`AgentStatus` 用 `status.state`。
- `Open(dir,label)`：
  - 有 Herdr Desktop 時，用 `herdr-app ls` 找 folder 與 dir 相同（以 `pathx` 正規化比較）的 space 重用，`Created=false`。找不到就跑 `herdr-app do space.new --name <label> --folder <dir> --focus false`，`Created=true`，handle 是 space id。
  - 沒有 GUI 時，跑 `hc run --bg --no-parent --outlive-parent --keep --name dev-<slug> --cwd <dir>` 開一個 shell terminal，handle 是 terminal address。
- `Activate(handle)`：space 用 `herdr-app do space.select --id <id>`。terminal handle 在 HC terminal 內時回報位址、不 attach（避免巢狀）；在外面時用 `runInteractive(hc attach <t>)`（`internal/runtime/runtime.go` 現有 helper）。
- `Close`：只關 dev 開的 space，必須有 `dev.*` label 證明。執行 `herdr-app do space.close --id … --confirmed`，前置 agent 檢查沿用 taskflow 現有的 guard。
- `Annotate`：對 session 內的 terminal 執行 `hc label <t> dev.state=… dev.next=…`。
- `AgentActivities`：讀 `hc ls --json` 的 `agent`/`status`。任何被辨識的 agent 都算占用，跟 Behavioral contracts 一致。
- `CurrentPaneID`：做一次 fresh `hc get $HC_TERMINAL_ID`，確認 terminal 存在後回傳。
- **刻意不實作** `WorktreeOpener`、`ExternalCoordinatorOpener`、`PaneRunner`、`PaneCloser`。parallel launch 繼續 fail closed，文件要寫明。
- `Select`（`internal/runtime/runtime.go:184`）：`case "herdr-core"` 回傳新 backend。`auto` 的新順序是：在 HC terminal 內且 herdr-core 可用時優先選 herdr-core，`HERDR_ENV=1` 時選 herdr，其他情況維持原順序。`All()` 也加入它。
- `internal/config/config.go:511` 的 validation 加上 `herdr-core`。`--runtime` flag 的說明字串要同步，然後跑 `make skill-sync`。
- 測試：參考 `herdr_test.go`，用 fake `hc`/`herdr-app` script 驗證 argv 與 JSON 解析、space 重用與新建、Close 的 label guard、HC 內 Activate 不會 attach。

## Phase 3 — Enter 的 Open target picker

**目標清單**由 CLI 計算，新檔 `internal/cli/open_targets.go`：

| ID | 條件 | 動作 |
|---|---|---|
| `runtime` | 永遠存在 | 現有 `openResolved` 路徑（`internal/cli/tui.go:382`） |
| `herdr-desktop` | `herdr-app` 存在且 herdr-core 可用 | herdr-core backend 的 Open 加 Activate |
| `herdr` | herdr binary 存在 | 強制使用 herdr backend；不在 HERDR_ENV 內時，提示會 attach client |
| `sheprd` | `sheprd` binary 存在 | `sheprd open DIR`；exit code 3/7 轉成可讀的錯誤訊息 |
| `cd` | 永遠存在 | `OpenResult{Directory: dir}` |

與 `runtime` 解析結果相同的目標要合併，不重複列出。

**設定**（`internal/config` 的 `TUI` struct 新增 `Open OpenTable`）：

```toml
[tui.open]
mode = "ask"        # ask | default；ask 只在有 ≥2 個不同目標時才跳 picker
default = "runtime" # picker 的預選項，也是 mode=default 時使用的目標
```

**TUI 改動**：
- `tui.Actions`（`internal/tui/model.go:177-183`）新增 `OpenTargets func(dir string) []OpenTarget`。`Open`、`OpenRepo`、`OpenCheckout`、`OpenRemote` 多一個 `target string` 參數（這是內部 API，可以直接改）。
- `openSelected()`（`internal/tui/model.go:4302`）：先算出目標清單。只有一個目標，或 mode=default 時直接執行；否則開 chooser。chooser 沿用 `listActionCopy`（"copy repository data…"）的選單實作，數字鍵直選，Enter 確認，Esc 取消。
- Sheprd 與 Herdr Desktop 是 GUI 端切換，行為維持一致：完成後走現有 `actionMsg{activate}`，退出 TUI 再 activate。Sheprd 不需要 handle，所以直接在 callback 裡執行，然後 status 回報。
- Fleet row 的 Enter 不變，仍走 `fleetnav`。

## Phase 4 — Go-to-folder action

- `internal/tui/action_definitions.go` 新增 `listActionCd`，標籤是「cd to folder」，按鍵 `C`（已確認沒被占用；小寫 `c` 已用於 edit next 和 clone，`g` 已用於 home）。
- 適用範圍：Tasks（checkout 存在）、Repos（repo 或 worktree child）、Tries（`Present()`）、Remote（`Cloned()`）。
- 執行：重用 `repo.ValidateNavigation` 與 `checkoutOf` 驗證路徑，然後回傳 `actionMsg{cd: dir}`。之後的 `m.chosen` → `app.cdDirective`（`internal/cli/app.go:188`）都是現有機制。
- 沒有 shell wrapper（沒有 `DEV_SHELL_CD_FD`/`DEV_SHELL_CD_FILE`）時，`cdDirective` 會印出 `cd '<dir>'`。action 的 Description 註明需要 `dev shell-init` wrapper。
- help browser（`help_content.go`）與 action description 都要補上。

## Phase 5 — 文件、skill、changelog 同步

- `internal/skill/dev-cli/references/runtime-herdr.md`：新增 herdr-core / Herdr Desktop / Sheprd 段落、偵測規則、不支援 parallel launch 的說明。`references/parallel-agents.md` 註明 herdr-core 會 fail closed。
- `SKILL.md`、README、英文與 zh-TW 的 runtime/compatibility/config/TUI 頁面成對更新。config 範例加上 `[tui.open]`。
- `CHANGELOG.md` `[Unreleased]`：host detection、herdr-core runtime、open picker、`C` cd action。
- 這是一組新相容行為，收尾時發 minor `v0.4.0`（依 CLAUDE.md 的 release 流程）。

## Verification 驗證

```bash
go test ./internal/runtime -run 'HerdrCore|DetectHost'
go test ./internal/tui -run 'OpenTarget|ActionCd'
go test ./internal/cli -run 'OpenTargets|RepoCd'
make skill-sync && make skill-check
make vet && files="$(gofmt -l .)" && test -z "$files"
go test -race -timeout 20m ./...
uv run python scripts/check-docs.py --source && uv run mkdocs build --strict && uv run python scripts/check-docs.py --site site
```

手動 end-to-end 驗證，在目前這個 Herdr Desktop terminal（t2）裡跑：
1. `./dev doctor` 顯示 `host: herdr-core (Herdr Desktop, t2)`，runtime auto 解析為 `herdr-core`。
2. `./dev` TUI，在 Repos 對某個 repo 按 Enter，picker 列出 runtime/Herdr Desktop、herdr、Sheprd、cd。
   - 選 Herdr Desktop：`herdr-app ls` 出現新 space（或重用既有 space）並被選取，dashboard 正常退出，沒有巢狀 herdr client。
   - 選 cd：shell 會切到該目錄（需要 shell wrapper）。
3. 按 `C` 直接 cd，不跳 picker。
4. `HERDR_ENV=1` 與 plain terminal 底下行為維持不變（回歸測試）。
