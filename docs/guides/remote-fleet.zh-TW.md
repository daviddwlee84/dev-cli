---
description: 透過 merged user-authored 與 dev-managed fleet configuration，盤點 POSIX 或 Windows SSH host 上的 repository、task 與 runtime activity。
authority: project
status: stable
verified_on: 2026-09-01
lang: zh-TW
---

# 遠端 Repository Fleet

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev fleet` 透過 SSH fan out 到其他執行自己 `dev` 的機器，讓你 inspect/open 它們的 repository 並安全傳播 branch，而不會集中管理它們的 path、task registry 或 runtime state。

[SSH Host 設定與佈建](ssh-hosts.zh-TW.md)是 optional entry point，可探索 alias、bootstrap public key，並明確產生已驗證的 fleet registration。Fleet 本身仍接受 user-authored profile。

## Fleet 是什麼

Fleet 是 controller-side merged 的其他 host 清單。每個 remote 執行自己的 `dev`，使用自己的 XDG config、scan roots、task registry 與 runtime。Fleet 只要求 read-only snapshot，並傳送狹窄 allowlist 內的 sync/open helper；不會把 remote configuration 複製到 controller。Remote 缺少 `dev` 或無法連線只會讓單一 row degrade，不阻擋其他 hosts。

這與 REMOTE TUI view 及 repository publishing/PR flow 不同。後者使用 authenticated `gh`/`glab`；configured Azure DevOps inventory/PR 則使用 Azure CLI。Fleet 透過 ordinary OpenSSH 與機器溝通，查看那些機器上的 local checkout。

## 兩層 configuration ownership

```bash
dev fleet config init
dev fleet config edit
dev fleet config show
dev fleet config path
```

Fleet 載入兩層 durable configuration：

1. **Primary user-authored config：**`$XDG_CONFIG_HOME/dev/remotes.toml`，或 root `--remotes <path>` override。`dev fleet config init` 寫 starter（`--force` overwrite、`--stdout` 只 print）；`config edit` 只開這個 file；`config path` 也只輸出此 path。
2. **Generated dev-owned fragments：**sibling `remotes.d/ssh-<alias>.toml` files。它們只有在 explicit `dev ssh setup <alias> … --fleet` 的 fresh ordinary alias login 成功後才建立；`dev ssh remove <alias> --fleet` 才是 removal owner。

Directory derivation 是 deterministic：

| Primary path | Generated directory |
|---|---|
| default `$XDG_CONFIG_HOME/dev/remotes.toml` | `$XDG_CONFIG_HOME/dev/remotes.d` |
| `--remotes /srv/dev/lab.toml` | `/srv/dev/lab.d` |
| `--remotes /srv/dev/lab` | `/srv/dev/lab.d` |

Primary file 可以不存在；valid generated fragments 仍會載入。Loader 先 decode primary，再依 filename lexical order 載入 managed fragments，最後才 apply defaults。Load 過程絕不 rewrite primary bytes 或 comments。

每個 generated file 有 fixed v1 header、`schema_version = 1`，以及 exactly one `[host]`，只含 `name`、`ssh_alias` 與 `remote_os`。Defaults、password、explicit hostname/user/port/identity 與 arbitrary field 都禁止。Directory/file 必須滿足 private Unix mode 或 protected Windows DACL，以及 no-link/reparse ownership check；drift 是 conflict，不是 overwrite permission。

`dev fleet config show` 會印出 effective merged config、redact plaintext password value，並在 generated entry 前加入 source/ownership comment，要求使用 `dev ssh setup/remove`。`config edit` 與 FLEET TUI 的 `e` key 仍只開 primary `remotes.toml`；generated entry 存在時會顯示 warning。

## Primary host profiles

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
remote_os = "posix"

[[hosts]]
name = "winlab"
ssh_alias = "winlab"
remote_os = "windows"

[[hosts]]
name = "vps"
hostname = "203.0.113.10"
user = "dev"
port = 22
identity_file = "~/.ssh/id_ed25519"
remote_os = "posix"
ssh_login_password_source = { type = "bitwarden", item = "ssh-vps-login" }
```

Primary 存在時必須有 `schema_version = 1`。`[defaults]` 提供 timeout、cache TTL、fan-out concurrency 與 `dev_path`；每個 primary host 都 inherit 未設定的 value。Host 需要 `name`，並搭配 `ssh_alias`（優先，因為 ordinary OpenSSH configuration 可保留 `ProxyJump`、`IdentityAgent` 與 host-key policy）或 `hostname` 加 optional `user`、`port`、`identity_file`。

`remote_os` 接受 `posix` 或 `windows`；省略時為了 backward compatibility 仍代表 POSIX。它決定 remote command launcher 與 target path semantics，也會納入 endpoint cache identity。

`ssh_login_password_source.type` 可為 `none`（default）、`prompt`、`plain` 或 `bitwarden`。Fleet 一律先嘗試 key/agent BatchMode；只有 permission denied 且設定 password source 才重試。Primary 若含 plaintext password 必須是 mode `0600`，否則 load 失敗。Generated fragment 不能含任何 password source。

## Merge 與 collision rules

- Host name 在 primary 與 generated layers 全域唯一。
- Existing primary profiles 可繼續共用同一 SSH alias，保留先前接受的 configuration。
- 任何 generated fragment 參與時，SSH alias collision 就是 error。Generated registration 不能靜默與 primary profile 或另一個 managed entry 競爭。
- Generated entry 參與的 alias comparison 不分大小寫。
- Malformed、insecure、手動 edited 或 noncanonical generated fragment 會 block merged load。
- 所有 host merge 完成後才 apply defaults；因此 generated fragment 可 inherit controller defaults，不需 duplicate。

Primary collision 請用 `dev fleet config edit` 解決。Valid generated fragment 請用 `dev ssh setup <alias> … --fleet` reconcile；不要手動 edit。

## 透過 `dev ssh` 明確 registration

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup winlab --key ~/.ssh/id_winlab --target-os windows \
  --fleet --fleet-name windows-builder
```

Alias discovery 或成功 SSH bootstrap 都不會 imply `--fleet`。Setup 最後才寫 fleet fragment，且必須先通過 exact-key verification 與第二次 fresh ordinary alias login。Registered platform 是已驗證的 `--target-os`；managed Windows entry 使用 `dev_path = "auto"`。Remote 缺少 `dev` 仍是 valid SSH onboarding result，安裝前 fleet 顯示 `no-dev`。

Partial/unknown bootstrap、failed ordinary gate 或 fleet-fragment collision 都會保留 valid local SSH config/generated keys，但 skip registration；remediation 後重新執行 setup。

## `dev fleet` commands

| Command | Flags | Purpose |
|---|---|---|
| `dev fleet list` | `--host <name>`（repeatable）、`--repo <query>`、`--json`、`--cached`、`--strict` | 列出本機與 merged configured hosts 的 repository/activity |
| `dev fleet status` | `--json`、`--strict` | probe configured hosts 並回報 snapshot health |
| `dev fleet sync <repo>` | `--push`、`--remote <name>`、`--host <name>`（repeatable）、`--json` | optional publish，然後安全 fast-forward clean matching checkout |
| `dev fleet open <host> <repo>` | — | 透過 Herdr 或 SSH login shell 開啟 remote repository |
| `dev fleet config init` | `-f`/`--force`、`--stdout` | 寫入／印出 starter primary `remotes.toml` |
| `dev fleet config edit` | `--editor <cmd>` | 只開 primary `remotes.toml` |
| `dev fleet config show` | — | 印出 effective merged config，redact password 並標示 generated ownership |
| `dev fleet config path` | — | 印出 primary config path |

`list --repo` 依 name、remote identity、branch 或 path filter。`--cached` 不會使用 network。`--strict` 會讓 unreachable/timeout/incompatible/invalid/stale-error host 回傳 non-zero；乾淨的 `no-dev` 只是資訊。`sync` 在本機 resolve repository；沒有 `--push` 時，source `HEAD` 必須已等於 fetched upstream。`--remote` 選擇 cross-host Git identity（default 依序為 branch upstream remote、`origin`）。

四個 hidden helper——`_snapshot`、`_sync`、`_open-herdr`、`_shell`——構成 remote wire surface，使用者不應直接呼叫。

## POSIX 與 Windows transport

每個 fleet operation 都 shell out 到 controller 的 system `ssh`，帶 connection/server-alive bounds，並以 `BatchMode=yes` 開始。`fleet open` 會為 interactive login shell 配置 PTY。Fleet launcher 不會削弱 user host-key 或 known-hosts policy。

POSIX target 使用既有 injection-safe shell launcher。`dev_path = "auto"` 會檢查常見 local user/package-manager locations 與 `PATH`，沒有 `dev` 時回傳 exit `127`。Explicit path 會 quote，並依 POSIX target semantics interpret，絕不用 controller environment expand。

Windows target 使用 `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand …`。Wrapper 會：

- 將 helper arguments 當 data decode，只允許四種 hidden fleet command shape；
- `dev_path = "auto"` 時在常見 user install/shim locations 或透過 `Get-Command` 尋找 `dev.exe`；
- executable 不存在時回傳 `127`，並 propagate remote dev exit code；
- 不 consume stdin，因此 `_sync` 可原封不動收到 JSON request；
- 以 target-OS semantics 驗證 explicit Windows drive/UNC path，不套 controller 的 `filepath` rules。

Generated Windows registration 要求 `dev_path = "auto"`。User-authored Windows profile 可使用 absolute Windows target path。Encoded wrapper 是 transport boundary，不是讓 caller 執行 arbitrary PowerShell 的 permission。

BatchMode 被拒絕且 primary profile 有 password source 時，fleet 會 retry 一次。Password 不會放入 SSH argv 或 environment：一次性的 self-executed `SSH_ASKPASS` helper 透過 inherited descriptor 接收。`prompt` 讀 hidden terminal input；`plain` 來自 protected primary config；`bitwarden` 執行 `bw get password <item>`。SSH-host bootstrap 本身沒有 password backend——interactive setup 將 native prompt 留給 OpenSSH，noninteractive setup 維持 batch-only。

## Cache 與 durable state

| Data | Role |
|---|---|
| primary `remotes.toml` | durable user-authored fleet intent |
| sibling `remotes.d/ssh-<alias>.toml` | durable dev-owned fleet intent，只由 explicit SSH commands create/remove |
| each remote 的 config/tasks/repositories/runtime | host-local authority；絕不 centralized |
| `$XDG_CACHE_HOME/dev/fleet/v1/*.json` | disposable controller snapshots |

成功 probe 會寫 private per-host JSON snapshot。Endpoint ID 含 connection fields、SSH port、timeouts、`dev_path` 與 `remote_os`；改變 target 會讓舊 cache identity 失效。Oversized/malformed snapshot、future timestamp、invalid count 與 unsafe field 都忽略。`dev cache clear fleet` 或 `dev cache clear all` 可移除；下一次 fleet request 會重建。

Cache 讓 unavailable host 可以 `stale` 保留 last-known state；`--cached` 只讀 cache。它永遠不會成為 remote path 或 task authority。

## TUI 中的 FLEET

FLEET 是六個 view 之一（`TASKS`、`REPOS`、`FLEET`、`TRY`、`REMOTE`、`SKILLS`）。Live remote lazy-load，valid cache 則在 initial view 後 decode。預設隱藏本機，因為 REPOS 有較完整 local data；`a` toggle。Local row 重用 accepted REPOS generation，不 rescanning。`r` supersede prior work，refresh 所有 merged configured hosts。Noninteractive `dev fleet list` 仍包含 local 加 remote。

Table 顯示 host/state/repository/branch/Git/runtime/task/path facts。Enter 在符合資格的 POSIX-style profile 回報 Herdr 且不需 password step 時使用 native Herdr remoting；否則透過 SSH 與 remote login shell 開啟。Windows controller 的 local fallback 會啟動 child `%COMSPEC%` shell，因為 Windows 沒有 `exec(2)`。

`e` key 只開 primary `remotes.toml`。返回後，dev reparse 完整 primary-plus-generated merge。Invalid primary 或 generated fragment 會回報 error 並保留之前 usable rows；valid merge 觸發 live reload。

## 安全 branch propagation 與 degradation

```bash
dev fleet sync api --push
dev fleet sync api                 # HEAD 必須已等於 fetched upstream
dev fleet sync api --host lab
```

Source 必須 clean 且 attached。Target 依 normalized Git remote identity match，不依 directory name。每個 target 先 fetch；只有同一 branch、clean 且 strictly behind 的 checkout 會 fast-forward。不同 branch 不會 switch。Dirty、ahead、divergent、ambiguous 或 unreachable target 保持不動，並讓 sync non-zero。缺少 `dev` 或該 repository 的 host 會明確回報並 ignore。

Per-host states 是 `ok`、`stale`、`no-dev`、`unreachable`、`timeout`、`incompatible`、`invalid-response`。沒有 automatic rebase、force push、background hook 或 all-repository pull。

## 來源

- [`internal/cli/fleet.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet.go)
- [`internal/fleet/config.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/config.go)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/fleet/transport.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/transport.go)
- [`internal/fleet/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/cache.go)
- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/help/topics/fleet.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/fleet.md)
