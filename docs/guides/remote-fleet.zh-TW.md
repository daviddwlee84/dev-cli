---
description: 透過 dev fleet 跨 SSH 盤點 repository、pin remote machine identity、安全 fast-forward branch，並明確傳送 bounded ignored files。
authority: project
status: stable
verified_on: 2026-08-31
lang: zh-TW
---

# Remote repository fleet

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev fleet` 透過 SSH 連到其他執行自己 `dev` 的機器，讓你能查看並開啟它們的 repository、安全地傳播 branch，而不會把它們各自獨立的設定合併進這台機器的視角。

## Fleet 是什麼

Remote fleet 是一份其他 host 的清單，每個 host 都執行自己的 `dev` binary，使用自己的 `$XDG_CONFIG_HOME`、自己的 scan roots、自己的 task registry 與自己的 runtime。Inventory 維持 decentralized：每個 host 的 `dev` 透過 SSH 產生自己的 read-only snapshot，一個 unreachable host 不會擋住其他 rows。Mutation 只存在於分開命名的 commands，例如 `fleet sync` 與 `fleet files --apply`；兩者都不會讓這台機器成為 target paths、task registry 或 runtime 的 authority。

這與 REMOTE TUI view 及 publishing/PR flow 是不同概念——GitHub/GitLab
repository publishing 與 PR 使用 `gh` 或 `glab`，Azure DevOps inventory 與 PR
則使用 Azure CLI。Fleet 溝通的對象是*你自己的機器*上*你自己的 local
checkout*。

## 設定 hosts

```bash
dev fleet config init
dev fleet config edit
dev fleet config show
dev fleet config path
```

Host 設定放在 `$XDG_CONFIG_HOME/dev/remotes.toml`（可用全域 `--remotes <path>` flag 覆寫路徑）。`config init` 會寫入一份 starter file（`--force` 覆寫既有檔案、`--stdout` 只印出不寫入）；`config edit` 會用 `$VISUAL`/`$EDITOR` 開啟它（`--editor` 可覆寫）；`config show` 印出目前生效的設定，明文密碼會被遮蔽；`config path` 印出解析後的路徑。

```toml
schema_version = 1

[defaults]
connect_timeout = "15s"
command_timeout = "5m"
cache_ttl = "15m"
max_parallel = 4
dev_path = "auto"

[[hosts]]
name = "lab"
ssh_alias = "lab"
# 執行 dev fleet machine-id lab 並透過獨立管道驗證後才加入：
machine_id = "00000000-0000-4000-8000-000000000000"

[[hosts]]
name = "vps"
hostname = "203.0.113.10"
user = "dev"
port = 22
identity_file = "~/.ssh/id_ed25519"
ssh_login_password_source = { type = "bitwarden", item = "ssh-vps-login" }
```

`schema_version = 1` 為必填。`[defaults]` 提供 `connect_timeout`、`command_timeout`、`cache_ttl`、`max_parallel`（fan-out 並行度）與 `dev_path`（`"auto"` 會搜尋 `PATH`）；每個 `[[hosts]]` 項目未明確設定的欄位都會沿用這些預設值。一個 host 需要 `name`，並搭配 `ssh_alias`（優先——它會沿用 `~/.ssh/config` 的 `ProxyJump`、`IdentityAgent` 與 host-key policy，行為與單純的 `ssh` 呼叫完全一致）或 `hostname`（可搭配 `user`、`port`、`identity_file`）其中之一。Optional `machine_id` 是 durable UUID pin：read-only inventory 可在未設定時警告或繼續，但 mutating portable-file apply 必須 exact match。`ssh_login_password_source.type` 可為 `none`（預設）、`prompt`、`plain`（內嵌 `value`）或 `bitwarden`（用 `item` 透過 `bw` CLI 查詢）。設定檔若含 `plain` 密碼，檔案權限必須是 `0600`，否則 `dev fleet` 會拒絕載入。

用 `dev fleet machine-id <host>` 取得 target UUID，在該機器上透過獨立管道驗證後，再複製到 `remotes.toml`。這個 command 是 read-only，只會回報 `unpinned`、`match` 或 `mismatch`，絕不代替使用者修改設定。

## `dev fleet` 指令

| Command | Flags | Purpose |
|---|---|---|
| `dev fleet list` | `--host <name>`（可重複）、`--repo <query>`、`--json`、`--cached`、`--strict` | List repositories and activity across this machine and configured hosts |
| `dev fleet status` | `--json`、`--strict` | Probe configured hosts and report snapshot health |
| `dev fleet machine-id <host>` | `--json` | 顯示 observed durable UUID 並比較 configured pin |
| `dev fleet sync <repo>` | `--push`、`--remote <name>`、`--host <name>`（可重複）、`--json` | Push optionally, then safely fast-forward clean matching checkouts |
| `dev fleet files [repo-or-path]` | `--to <host>`、`--file <pattern>`（可重複）、`--apply`、`--replace`、`--yes`、`--json` | Plan 或 apply 明確 ignored files 的 one-way transfer |
| `dev fleet open <host> <repo>` | — | Open a remote repository through Herdr or an SSH login shell |
| `dev fleet config init` | `-f`/`--force`、`--stdout` | Write a starter `remotes.toml` |
| `dev fleet config edit` | `--editor <cmd>` | Open `remotes.toml` in `$VISUAL`/`$EDITOR` |
| `dev fleet config show` | — | Print the effective configuration (passwords redacted) |
| `dev fleet config path` | — | Print the resolved `remotes.toml` path |

`list` 的 `--repo` 會依 name、remote identity、branch 或 path 過濾；`--cached` 完全依最後一次儲存的 snapshot 回答，不會有任何 network activity；`--strict` 會讓 `list` 與 `status` 在 host 為 unreachable/timeout/incompatible/invalid 時回傳非零結果。`sync <repo>` 對 `<repo>` 的解析方式與其他 repository reference 相同；沒有 `--push` 時，其 `HEAD` 必須已等於 fetch 後的 upstream；`--remote` 決定用哪個 Git remote 在各 host 間識別這個 repository（預設：branch 的 upstream remote，其次是 `origin`）。

## 明確 portable local files

Repository 可用與 worktree provisioning 分離的設定提出 export candidates：

```toml
# .dev-cli/config.toml
version = 1

[local_files]
include = [".env", ".mcp/**"]
```

`[worktree].include` 是 local provisioning policy，絕不會被這裡繼承。
`[local_files].include` 也不代表 standing permission：每次都必須用 explicit invocation
選擇恰好一個 target，repeatable `--file` 只為該次 invocation 加入 ad-hoc pattern。
Patterns 會在 source 展開成 sorted exact paths；wire 上不傳 glob。

```bash
dev fleet files api --to lab                    # 只產生 report
dev fleet files api --to lab --apply --yes      # 建立 target 不存在的 files
dev fleet files api --to lab --replace --apply  # 分開授權不同 bytes
```

Source 與 target 必須已解析到一個具有相同 normalized **fetch** identity、attached
branch 與 exact commit 的 clone；只有 push URL 相同不足以授權。兩端依自己的 Git
configuration 分別證明每個 exact path 都是 untracked 且 ignored。只有 regular files
可通過：不接受 symlink/reparse point、directory、socket、device、FIFO、`.git`、nested
repository 或 submodule boundary。Compiled ceilings 是 128 files、每個 8 MiB、合計
32 MiB，且 path length/depth 有界；host policy 只能調低，不能提高。Source branch、
HEAD 與 fetch identity 會在 payload read 前後重驗；target apply 與 `fleet sync` 共用
canonical Git-common-directory lease。

預設 plan，不送 file body。Apply 另外要求 configured `machine_id` 與 content-free
capability probe 一致。Target 不存在時以 owner-only mode atomic create；內容相同是
no-op；內容不同預設 blocked，只有 displayed plan 明確帶 `--replace` 才可取代。
Replacement 綁定 observed target digest/mode、保留 private rollback copy、重驗兩端
roots，失敗時 rollback file changes。`--yes` 只回答 confirmation，絕不隱含
replacement。Public human/JSON output 只有 path、size、mode 與 state，不含 content
或 hash。

Transaction journal 會先於 manifest/payload staging durable，因此 interrupted request ID
可 resume 或 reconcile。Rollback 在 crash 後無法證明空 parent directory identity 時會
刻意保留它；刪到其他 process 的 replacement 更糟。Native Windows payload transfer
會在 content 傳送前被 capability-block。

這個 command 不是 repository/task ownership transfer、clone acquisition、provisioning、
backup、restore 或 eviction；它不會 switch branch、複製 task/catalog/note state、監看
變更、傳播 deletion，或移除 source。

`dev fleet` 除了 `_snapshot`、`_sync`、`_open-herdr` 與 `_shell`，也註冊 bounded hidden
capability/file commands。它們只供兩端 `dev` 透過 SSH 互相呼叫，是 wire protocols，
不是 user-facing surface。

## Transport 與認證

每個 fleet command 都透過呼叫系統的 `ssh` binary 連到 host，帶上 `ConnectTimeout`、`ServerAliveInterval=15`、`ServerAliveCountMax=2`，並且第一次嘗試一律用 `BatchMode=yes`——只用 key 或 agent authentication，行為就像一次適合腳本化的 `ssh` 呼叫。Fixed protocols 使用 non-PTY `-T`，並停用 agent、X11、local-command 與所有 port forwarding，同時保留使用者原本的 host-key policy。`dev fleet open` 則另外為 interactive login shell 配置 PTY（`-t`）。

若該次嘗試因 "permission denied" 被拒絕，且該 host 設定了 `ssh_login_password_source`，`dev` 會再重試一次、改用密碼驗證。密碼本身不會出現在 argv 或環境變數中：`dev` 會把自己重新以一次性的 `SSH_ASKPASS` helper 執行，密碼透過一個繼承的 file descriptor 交給那個 helper。`prompt` 會在執行當下從 `/dev/tty` 讀取隱藏輸入；`plain` 與 `bitwarden` 則分別從設定檔或 `bw get password <item>` 取得。

在遠端那一側，預設的 `dev_path = "auto"` 會搜尋常見安裝位置（`~/.local/bin`、`~/go/bin`、mise shims、Homebrew/Linuxbrew、`/usr/local/bin`、`/snap/bin`）再查 `PATH`，找不到 `dev` 就以 `127` 結束；明確指定絕對路徑的 `dev_path` 則會跳過搜尋，直接 exec 該路徑。

## 哪些是 cache、哪些是 durable

`$XDG_CONFIG_HOME/dev/remotes.toml` 是 durable、由使用者撰寫的設定，與 `config.toml` 地位相同——由 `dev fleet config init`/`edit` 管理，不會被自動重新產生。

每次成功探測都會在 `$XDG_CACHE_HOME/dev/fleet/v1/<host-name-slug>.json` 寫入該 host 的 JSON snapshot。這份 cache 是可拋棄的加速機制，不是 durable data：它會依 host 的連線欄位與 timeout 產生一個「endpoint ID」指紋，因此修改 host 的 `machine_id`、`ssh_alias`、`hostname`、`user`、`port`、`identity_file`、`dev_path` 或 timeout，下次 `dev` 讀取時會自動讓舊 cache 失效。Oversized snapshot、future timestamp、invalid count，以及過大或含 NUL 的 identity/path field 都會被忽略，不會顯示。`dev cache list` 會顯示它的路徑、大小與存在時間；`dev cache clear fleet`（或 `dev cache clear all`）可直接移除它。沒有需要手動「重建」的步驟——下一次 `dev fleet list`、`dev fleet status` 或 TUI 的 FLEET reload 就會用一次新的探測重新產生它。

這份 cache 存在的目的，是讓 unreachable、timeout、incompatible 或 invalid-response 的 host 仍能回報上一次已知的狀態（標記為 `stale`，並設定 `FromCache`），而不是直接從清單消失；`--cached` 只會使用這份 cache，完全不連網路。每個 remote host 自己的 `config.toml`、task registry 與 repository path，在該 host 上仍是唯一權威來源——cache 只保存它們的 read-only snapshot。

## TUI 中的 FLEET

FLEET 是 TUI 六個 view 之一（`TASKS`、`REPOS`、`FLEET`、`TRY`、`REMOTE`、`SKILLS`，用 `tab`/`h`/`l` 切換）。與 REMOTE 一樣採延遲載入——view 第一次開啟前不會開始 live probe——但 cache 會在初始 TASKS view 後於背景 decode，讓仍在 `defaults.cache_ttl` 期限內的 valid snapshot 先填入 table。TUI 預設隱藏本機，因為 REPOS 已提供較完整的 local inventory；`a` 可顯示或隱藏 local rows。Local-host snapshot 會重用目前已接受的 REPOS generation，不再重跑 repository/task/runtime discovery；若該 generation 仍在 loading，FLEET 會保留 cached rows 並顯示 waiting。`r` 會 supersede 舊 request，並強制對所有已設定的 host 做一次 live reload。Non-interactive 的 `dev fleet list` 仍會同時列出本機與 remote hosts。

它的表格欄位是 `HOST`、`STATE`、`REPO`、`BRANCH`、`GIT`、`LIVE`、`TASKS`、`PATH`。`enter` 會開啟選取的 repository：明確顯示出來的 local host row 使用一般的 local open；remote row 則在該 host 的 snapshot 回報 `herdr` runtime，且透過 `ssh_alias` 連線、不需要密碼步驟時，優先使用原生 Herdr remoting，否則退回在該 repository 目錄下開啟 interactive SSH login shell。這個 view 中 Git 的變更是唯讀的——FLEET 是用來檢視與開啟工作，不是在原地編輯它。

`e` 會用 `$VISUAL`/`$EDITOR` 開啟 `remotes.toml`——也就是這個 view 實際對應的檔案，而不是其他 view 中 `e` 所開啟的 dev 自身 `config.toml`。離開編輯器後，`dev` 會先重新解析該檔案才使用它。解析失敗、出現未知欄位，或含有明文密碼的檔案權限有問題時，都會被回報並保留原本的資料列，因此打錯字不會安靜地讓某台 host 從 fleet 中消失。檔案解析成功則會立即對所有已設定的 host 觸發一次 live reload。

## 優雅降級

每個 host 都是獨立探測的，所以一個壞掉的 host 不會讓整個 fleet 失敗。每個 host 的狀態有 `ok`、`stale`（重用 cache，可能附帶說明原因的 error）、`no-dev`（remote 的 `PATH` 上沒有 `dev`——僅回報，不視為 error）、`unreachable`（SSH 本身失敗）、`timeout`、`incompatible`（remote 的 `dev` 版本過舊或無法辨識）與 `invalid-response`（snapshot JSON 格式不正確）。`ok` 與乾淨的 `no-dev` 不會讓 `--strict` 失敗；其餘狀態都會，包括帶有 error 的 `stale` 結果。

另一方面，forge integration 是 optional：`gh` 與 `glab` 提供 GitHub/GitLab
inventory、publishing 與 pull/merge request；`az` 提供 Azure DevOps inventory 與
pull request。缺少 CLI 時，`dev doctor` 只回報 warning，local Git workflow 仍可用；
但 explicit non-interactive publication request 會附 login/install 指引失敗，不會靜默
改變操作意義。Azure DevOps inventory 還額外是 opt-in：未設定
`forge.azure_devops` targets 前一律停用。

## 來源

- [`internal/cli/fleet.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet.go)
- [`internal/fleet/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/config.go)
- [`internal/fleet/types.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/types.go)
- [`internal/fleet/transport.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/transport.go)
- [`internal/fleet/protocol.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/protocol.go)
- [`internal/fleet/sync.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/sync.go)
- [`internal/localfiles`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/localfiles)
- [`internal/machineid`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/machineid)
- [`internal/cli/fleet_files.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet_files.go)
- [`internal/fleet/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/cache.go)
- [`internal/help/topics/fleet.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/fleet.md)
- [`internal/cli/tui.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/tui.go)
- [`internal/tui/model.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/model.go)
- [`internal/tui/view.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/view.go)
- [`internal/tui/rows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/tui/rows.go)
- [`internal/cli/doctor.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/doctor.go)
- [`internal/cli/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/cache.go)
- [`internal/forge/forge.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/forge.go)
