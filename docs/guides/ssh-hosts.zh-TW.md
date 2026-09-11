---
description: 探索 OpenSSH alias、只管理 dev-owned host fragment、佈建 public-key access，並將驗證成功的 host 明確登記到 dev fleet。
authority: project
status: stable
verified_on: 2026-09-11
lang: zh-TW
---

# SSH Host 設定與佈建

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev ssh` 在既有 OpenSSH configuration 外加上一層保守的 lifecycle。OpenSSH 仍是 connection authority；dev 提供 static provenance、小型 owned fragment namespace、public-key bootstrap、fresh authentication proof，以及通往 [dev fleet](remote-fleet.zh-TW.md) 的明確 bridge。

## Authority 與 ownership

| Surface | Authority / owner | Dev 可以做什麼 |
|---|---|---|
| `~/.ssh/config`、其中的 foreign Includes 與 foreign `Host`/`Match` blocks | user + OpenSSH | static read；透過 plain `ssh -G` evaluate；明確 format/organize 可整理選定的使用者檔案 |
| root config 中的 `Include ~/.ssh/dev.d/*.conf` | dev，且必須明確執行 `ssh init --apply` | 在第一個 `Host`、`Match` 或更早的 Include 前安裝一次；絕不自動移除 |
| `~/.ssh/dev.d/<alias>.conf` | `dev ssh setup/remove` | 只 create、reconcile 或 remove canonical v1 file，其內容是 allowlisted single `Host` block |
| local key files | user + native `ssh-keygen` | 驗證 explicit key、經確認後 derive 缺少的 `.pub`，或以 no-replace 方式產生 Ed25519 pair；絕不複製 private bytes |
| remote `authorized_keys` | remote OpenSSH account | idempotently append 一筆 bounded normalized public record；絕不 remove 或 revoke |
| primary `remotes.toml` | user via `dev fleet config` | read/merge；SSH setup 絕不 rewrite |
| sibling `remotes.d/ssh-<alias>.toml` | `dev ssh setup/remove --fleet` | 只有 fresh ordinary login 成功後才 create；只有明確要求才 remove |

Foreign alias 仍可供 `list`、`show`、`probe`、key bootstrap 與 fleet registration 使用，但 `setup` 會拒絕 connection flags；dev 不會與既有 definition 競爭。新的 dev-managed alias 使用 portable lowercase exact-name grammar，且只有一個 deterministic file，內容可包含 `HostName`、optional `User`、`Port`、`ProxyJump`、`IdentityFile` 與 `IdentitiesOnly`。Arbitrary directives 與 wildcard/`Match` blocks 不在 managed scope。

## Command map

| Command | 精確 local flags | 邊界 |
|---|---|---|
| `dev ssh init` | `--apply`、`--yes`、`--json` | 預設只 plan；只有 `--apply` 可安裝 dedicated Include |
| `dev ssh list` | `--json`、`--format tsv`；明確選用 `--tailscale`、`--lan` | 預設 static；可加入 machine observations |
| `dev ssh show <alias>` | `--json` | static definitions，加上 plain `ssh -G <alias>` 的 effective values |
| `dev ssh setup <alias>` | 下方列出的 connection、key、route、fleet、plan、confirmation、JSON flags | owned local config、public-key bootstrap、optional fleet registration |
| `dev ssh probe <alias>` | `--json` | sharing disabled 的單次 fresh ordinary BatchMode login |
| `dev ssh remove <alias>` | `--fleet`、`--dry-run`、`--yes`、`--json` | 只移除 canonical dev-owned SSH/fleet fragments |

`dev doctor` 也會回報 local `ssh`/`ssh-keygen` 與 optional `tailscale` capability、static Include reachability、managed namespace permission/ACL，以及 generated fleet-fragment health。它不會執行 `ssh -G`、聯絡 host 或進行 repair。

## 一次性 initialization 採 report-before-apply

```bash
dev ssh init
dev ssh init --json
dev ssh init --apply
dev ssh init --apply --yes
```

沒有 `--apply` 時，init 只回報 root path、managed directory、exact Include，以及 `create`/`update`/`noop`/`blocked` action，不會寫入。`--yes` 只能與 `--apply` 一起用；它只確認 local plan，不提供 credential，也不接受 host key。

Dev 唯一會插入的 directive 是：

```sshconfig
Include ~/.ssh/dev.d/*.conf
```

Dev 會保留 insertion 以外的 root bytes、BOM/newline style，以及受支援的 Unix metadata 或 Windows owner/DACL。Unsafe path component、link/reparse point、special file、hardlink、concurrent source change、無法表示的 metadata，以及 `dev.d` 中 foreign 或 drifted content 都會被拒絕。Plan 若 blocked，dev 不做變更，並回報 exact Include 應手動放在哪裡。

## Static list 與 OpenSSH/network evaluation 的界線

```bash
dev ssh list
dev ssh list --format tsv
dev ssh list --json
dev ssh show lab
dev ssh show lab --json
dev ssh probe lab
```

`list` 與 SSH alias completion 會 walk 以 `~/.ssh/config` 為 root 的 active user Include closure。它們**不會**執行 `ssh`、resolver、`Match exec`、SSH agent 或 network。Scanner 會依 bounded lexical Include expansion 追蹤來源，記錄 source line 與 Include provenance，並將每個 exact positive alias 分類為 active、inactive、unknown 或 conflicting。Wildcard-only declaration 只作為 collision diagnostic，不是 selectable alias。Dynamic/unsupported guard、cycle、limit 或無法證明的 Include behavior 會讓 `complete: false`；不確定的 declaration 絕不會被提升成 usable host。

`--format tsv` 每個 discovered definition 輸出一列，沒有 header，依序是六個 tab-separated fields：

```text
alias  status  ownership  source  line  comma-separated-fleet-names
```

Fields 會 sanitize 成單一 physical line。Selector contract 刻意保持小型；完整 definition/provenance/diagnostic data 請使用 JSON。

`show` 先保留 static definitions，再刻意執行 plain `ssh -G <alias>`；它不會用 `-F` 取代 user config，也不解析不穩定的 `ssh -vv` prose。因此 system configuration 與 OpenSSH 的 scalar/additive semantics 都會生效，但 configured resolver behavior 與 user-authored `Match exec` 也可能執行。

`probe` 會跨越 network boundary。它執行一次 ordinary alias login，等價於 fresh `ssh -S none -o BatchMode=yes …`，因此既有 ControlMaster 不會造成 false success。它不會 override `StrictHostKeyChecking`、`UserKnownHostsFile` 或相關 policy；user-configured `KnownHostsCommand`、`UpdateHostKeys`、resolver 與 `Match exec` 仍可能執行。

## Setup modes 與 flags

Setup 處理三種 alias class：

1. **new：**interactive HostName prompt 以外的情況需要 `--hostname`，並建立一個 dev-owned fragment；
2. **managed：**只 reconcile 既有 canonical fragment；
3. **foreign：**保留所有 connection policy；任何 connection-field flag 都會 block，但 explicit key bootstrap 與 `--fleet` 可繼續。

Managed alias 的 connection fields 是：

```text
--hostname --user --port --proxy-jump --identity-file --identities-only
```

Operational flags 是：

```text
--config-only
--key <public-or-identity-path>
--generate-key [--key-path <identity>] [--comment <text>] [--no-passphrase]
--target-os <posix|windows>
--hop-os <alias=posix|windows>       # repeatable
--install-on-working-jump
--windows-admin-authorized-keys
--fleet [--fleet-name <name>]
--dry-run --yes --json
```

`--config-only` 不能與 key、route、bootstrap 或 fleet flags 併用。它會在 local managed-config publication/verification 後停止；若是 foreign alias，則只執行 plain `ssh -G` verification。

`--dry-run` 沒有 side effect：不 generate key、不寫 file、不執行 `ssh -G`、不碰 `known_hosts`、不 probe network，也不啟動 remote installer。Remote 與 route action 會誠實保留為 `unknown`。它可進行驗證 explicit named key 或 existing config 所需的 bounded local reads。Dry run 使用 `--fleet` 時仍須提供 `--target-os`，讓 proposed fragment 可確定。

Public-key bootstrap 必須明確且二選一使用 `--key` 或 `--generate-key`。JSON mode 即使在 terminal 上也不互動；任何 noninteractive full setup 還需要 `--target-os`，local mutation 則需要 `--yes`。`--yes` 只批准 local plan。Password/passphrase 與 host-key interaction 仍由 native OpenSSH 負責；batch mode 會回傳 `interaction_required`，不會自行發明 credential path。

## Existing key 與 generation

```bash
# 只建立 local managed alias。
dev ssh setup lab --hostname 192.0.2.20 --user dev --config-only

# 以 existing identity 或 public file 進行 bootstrap。
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix

# 在預設 ~/.ssh/id_ed25519_dev path 產生 Ed25519 pair。
dev ssh setup lab --generate-key --target-os posix

# Fully specified noninteractive generation。
dev ssh setup winlab --hostname 198.51.100.30 \
  --generate-key --key-path ~/.ssh/id_winlab --no-passphrase \
  --target-os windows --yes
```

`--key` 接受 validated `.pub` record、具有 companion `.pub` 的 private identity，或具有 companion `.pub` 的 security-key stub。Identity 缺少 public companion 時，dev 會先詢問，再執行 `ssh-keygen -y`；script 可用 `--yes` 提供該 local confirmation。Encrypted noninteractive derivation 會以 `interaction_required` 失敗，不會把 passphrase 放進 argv 或 environment。

`--generate-key` 透過 native `ssh-keygen` 產生 Ed25519。Interactive mode 將 hidden passphrase prompt 交給它；noninteractive generation 必須明確使用 `--no-passphrase`。兩個 half 先在 private staging basename 產生，依 fingerprint 確認相符、harden，再以 no-replace semantics publish；任何 destination collision 都會 block，不會 overwrite。成功產生的 pair 在後續 route/bootstrap/fleet failure 後仍保留，`dev ssh remove` 也絕不移除。

所有 output 都是 content-safe：可以包含 fingerprint、algorithm、path、digest 與 boolean；不包含 private bytes、passphrase、password、完整 public-key line、agent payload 或 unredacted command-like SSH option。

## ProxyJump 與 remote operating system

Dev 會對 target 與每個 discovered jump 執行 plain `ssh -G`，將 nested/comma-separated `ProxyJump` route 依 outermost-first flatten，並支援 alias、`user@alias`、`alias:port` 與 bracketed IPv6 forms。Cycle、repeated hop、unsupported URI/`ProxyCommand` route 與 ambiguous override 都會被拒絕，不會猜測。

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix \
  --hop-os bastion=posix --hop-os winjump=windows
```

`--target-os` 只套用 final target。Ambiguous jump 請使用 repeatable `--hop-os`；interactive mode 可以詢問，noninteractive mode 則要求每個 unknown hop 都指定。每個 hop 會先以 ordinary fresh BatchMode authentication probe。已能運作的 jump 不會被修改，除非明確加上 `--install-on-working-jump`。

POSIX hop 使用 constant `sh` installer 驗證／建立 `~/.ssh/authorized_keys`、套用 `0700`/`0600`，再 idempotently append 從 stdin 收到的一筆 public record。Windows OpenSSH 則是：

- standard account 使用 `%USERPROFILE%\.ssh\authorized_keys`，並設定 protected current-user + SYSTEM ACL；
- administrator-group account 必須明確使用 `--windows-admin-authorized-keys`，dev 才會 target `%ProgramData%\ssh\administrators_authorized_keys`，其 ACL 為 SYSTEM + BUILTIN\Administrators；
- dev 會偵測 group membership、拒絕 reparse target，且絕不自動授與 UAC/elevation。Elevation 或 non-default server policy 仍可能需要 manual remediation。

macOS、Linux 與 Windows controller 都使用 system `ssh` binary。PowerShell 是 Windows installer 的 target capability，不是 controller-side host database 或 password backend。

## Fresh proofs、partial outcome 與 fleet registration

Setup 對每個 route hop 依序執行 ordinary fresh probe、exact selected-key proof、必要的 public-key installation、第二次 exact-key proof，以及獨立的 ordinary alias gate。每個 proof 都使用 `-S none`；exact proof 另加 `IdentitiesOnly=yes` 與 selected identity。

Remote installation 無法變成 transaction。Installer 一旦開始，timeout/cancellation 或 non-zero result 會回報 `unknown`，因為 remote 可能已 append public key。Dev 絕不嘗試不安全的 compensating deletion。Local managed config 與 generated key pair 會保留，已完成的 hop facts 會回傳，後續 hops 與 fleet registration 會 skip；重新執行 setup 會 converge。

Fleet registration 永遠是 opt-in：

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix --fleet
dev ssh setup winlab --key ~/.ssh/id_winlab --target-os windows \
  --fleet --fleet-name windows-builder
```

只有 final fresh ordinary alias login 成功後，dev 才寫 generated sibling fragment。Default primary path 對應 `$XDG_CONFIG_HOME/dev/remotes.d/ssh-<alias>.toml`；`--remotes /srv/dev/lab.toml` 對應 `/srv/dev/lab.d`，不是 `.toml` 結尾的 path 則 append `.d`。Fragment 只包含 `name`、`ssh_alias` 與已驗證的 `remote_os`，不含 password 或另一套 connection policy。Remote 缺少 `dev` 不會讓 SSH onboarding 失敗，之後 fleet 會顯示 `no-dev`。

## Structured output

所有 public SSH JSON 在 stdout 都是 exactly one schema-versioned object。Operational failure 仍會輸出該單一 safe result object；CLI syntax/usage error 不會輸出 partial JSON document。Diagnostic 與 child progress 會寫到 stderr。

| Command | `kind` values | 重要 fields |
|---|---|---|
| `ssh init --json` | `ssh_init_plan`、`ssh_init_result` | `status`、source-bound `plan`、optional `result`、`error_code` |
| `ssh list --json` | `ssh_list` | `complete`、root/include state、aliases、definitions、fleet membership、diagnostics |
| `ssh show --json` | `ssh_show` | alias status、definitions、safe effective subset、fleet membership |
| `ssh setup --json` | `ssh_setup_plan`、`ssh_setup_result` | alias class、local/key/bootstrap plans/results、per-hop state、fleet action、partial/error code |
| `ssh probe --json` | `ssh_probe` | safe `ready`/`not_ready` status、code、exit code |
| `ssh remove --json` | `ssh_remove_plan`、`ssh_remove_result` | owned plan/result、explicit fleet action、status/error code |
| `ssh discover --json` | `ssh_discovery` | source 狀態、scope、candidates、觀察時間與完整性 |
| source-aware `ssh setup --json` | `ssh_onboarding_plan`、`ssh_onboarding_result` | connection plans、stage outcomes、保留的 keys 與逐 hop bootstrap 結果 |
| `ssh machine … --json` | `ssh_machine_snapshot`、`ssh_machine_plan`、`ssh_machine_result` | canonical UUID、來源 bindings 與 revision-bound 變更 |

Consumer 應依 `schema_version`、`kind`、machine-readable `status`/`action`/`code` 與誠實的 `partial`/`unknown` state 分支，不應解析 human table 或 stderr。

## Removal limits

```bash
dev ssh remove lab --dry-run
dev ssh remove lab --yes
dev ssh remove lab --fleet --yes
```

Removal 只接受 portable managed alias，且 expected file 必須仍是 canonical、secure、structurally dev-owned。Generated fleet fragment 若存在，省略 `--fleet` 會 block，不會靜默刪除第二份 durable intent；加上 flag 時，fleet removal 先執行。Primary user-authored `remotes.toml` 中的 reference 一律 block，並指向 `dev fleet config edit`。Manual drift、link/reparse point、changed source、unsafe metadata 與 ambiguous ownership 也全部 fail closed。

Removal 絕不刪除 shared Include、local private/public key file、`known_hosts`、remote `authorized_keys` 或 foreign configuration。

## Security boundary 與 deferred scope

已實作的 safety properties 包含 source-bound plan、no-follow/reparse check、private Unix mode 或 protected Windows DACL、atomic/no-replace publication、concurrent-source revalidation、managed config 寫入後的 plain-`ssh -G` verification、只有在剛寫入的 local identity 仍可證明為 owned 時才 rollback、process-group/Job-Object cancellation，以及 material-safe structured output。

刻意 deferred 的項目：

- key rotation、expiry、revocation，或從 remote `authorized_keys` 刪除 key；
- 刪除 local key 或 `known_hosts` repair/removal；
- alias rename/adoption、managed wildcard/`Match`、arbitrary SSH directive 或 SSH config editor；
- 自動化 `ProxyCommand`、certificate/CA、forwarding、custom `AuthorizedKeysFile` 或 forced-shell policy；
- password/vault storage、automatic password fallback、private-key copying、direct Bitwarden integration 或 weakened host-key check；
- cloud/chezmoi fleet import、mDNS、IPv6 range scanning 或 background probing。

Server policy 若超出 verified POSIX/Windows installer contract，dev 會回報 manual remediation，不會靜默削弱 protection。

## 來源

- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/sshhost`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/sshhost)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/help/topics/ssh.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/ssh.md)

## 主機管理與設定整理

在終端執行 `dev ssh` 會開啟選單；管線仍顯示 help。設定整理預設只做
formatting，群組重整必須另外選擇。

```bash
dev ssh manage                          # 聯合清單與多選 wizard
dev ssh manage --json                   # 本機 inventory，不做 SSH 登入
dev ssh manage --action register --alias lab --alias build --to both --target-os posix
dev ssh manage --action register --alias lab --to herdr --herdr-session agents --apply --yes
dev ssh manage --action rename --fleet-host lab --name workstation --apply
dev ssh manage --action disable --herdr-profile <profile-id> --apply
dev ssh manage --action remove --fleet-host lab --fleet-host build --apply

dev ssh format                          # 預覽四格縮排
dev ssh format --indent 2 --file ~/.ssh/config.d/work/lab.conf
dev ssh format --apply                  # 確認並保存復原紀錄
dev ssh organize                        # 多選完整 Host 區塊並指定群組
dev ssh organize --group lab=work --group nas=personal --numbered --json
dev ssh restore <receipt>               # 預覽還原；--apply 才套用
```

明確 action 與檔案操作預設只產生計畫，`--apply` 才執行；非互動套用須加
`--yes`。wizard 先顯示所有勾選動作再確認。`manage --json` 輸出版本化聯合
inventory，有 `--action` 時輸出計畫或結果。註冊計畫的 `fleet_registration`
會列出要寫入的名稱、SSH alias 與 target OS。既有 `ssh list` JSON／TSV
不變，也不執行 subprocess；`manage` 另外呼叫本機的
`herdr machine list --json`，但列出及規劃都不做 SSH 登入或執行 Match exec。
無法讀取的 provider 維持 unavailable，不當成可信的空清單。

SSH alias、fleet profile 名稱及 Herdr label 分開保存。新項目預設使用 alias，
既有自訂名稱保留；單一 alias 可指定 `--fleet-name`、`--herdr-label`。
`Host a b` 仍是同一設定區塊的兩個可選入口，選其中一個不會移除另一個。
不按 IP 合併 alias，因為不同入口可能選用不同 SSH 選項。Herdr 依精確 target
加 session 比對，修改時使用原生 profile ID；註冊時不會順便啟用既有停用項目。

Herdr 0.9.0 或相容的 machine CLI 是選用整合。原生 `machine add` 會準備遠端
安裝、啟動指定 server 再儲存，開啟中的 client 隨後會自動連線；計畫會列出
這些影響。原生安裝／server replacement 確認仍交由 Herdr，即使 dev 使用
`--yes` 也不代答。非互動缺少必要確認會失敗，不假裝已新增 profile。
remove／disable 只影響註冊及 client 連線，遠端 session 繼續運行。遠端
server 必須符合 Herdr 的 Linux／macOS 支援範圍。fleet 仍負責 repo／task
inventory，原有 remote-open 行為保持相容。

已能登入的 alias 通過新的普通登入驗證即可加入 fleet，不必重新安裝 key。
各 provider 動作分別記錄結果，失敗保留先前完成的動作；重跑先比對現況。
fleet 改名／移除支援 canonical generated fragments 與主檔正常的
`[[hosts]]` 表格，保留註解、子表及其他內容；同一來源的批次移除會合併成
一筆檔案交易。無法安全定位的 inline hosts 需原生編輯。移除註冊不會刪除
SSH 定義或遠端 repository。

formatting 僅修改行首縮排，預設四格，可改 `2` 或 `tab`；選項值、引號、
順序及換行格式保留。可選主檔或 `config.d` 中明確的使用者檔案；dev／其他
provider 管理的片段仍由原 owner 處理。預覽遮蔽命令型及敏感內容。

群組重整把完整 Host 區塊及相鄰前置註解搬到
`~/.ssh/config.d/<group>/<host>.conf`，多 alias 留在同檔。主檔使用明列的
Include 保持原始順序，即使 work／personal 交錯也不重新排序。預設不加檔名
編號；`--numbered` 可加上原始序號方便閱讀。`--group alias=group` 或
`--group @block-id=group` 指定區塊；未指定者保留既有群組或使用 `ungrouped`。
之後可再次用 wizard 移動群組，不必手動開多個檔案複製貼上；未選編號時，
單一區塊片段保留原檔名。

首版處理主檔內 Host 區塊及直接引用的群組片段；其他 Include 保留位置。
Match、動態／不完整 Include、重複引用的選定片段需手動整理，仍可先做
formatting。不加入廣泛 wildcard 啟用原本休眠的檔案。若新的檔案或目錄已被既有
Include glob 涵蓋，會拒絕搬移，避免入口切換前就生效；目的地碰撞也不覆蓋。

計畫綁定原始內容及檔案身分，套用時使用合作式 owner lock 並重新驗證。
先建立新片段、切換主檔，再移除退役來源。中斷時保留私有 receipt，位於
Git 與 cache 外的 `$XDG_DATA_HOME/dev/ssh-recovery/`（未設定時使用 XDG
預設）。`restore` 反向還原已驗證的完成步驟，若後來被修改或替換則拒絕覆蓋。
若程序在記錄 after-state 前被強制終止，須使用保留的私有原始內容手動復原。
可還原的 metadata 會保留；security attributes、不支援的 inode flags、
不安全的連結或其他使用者可寫的祖先目錄需原生處理。新的本機寫入僅支援
macOS／Linux backend；Herdr 原生 CLI 與一般 editor 不受 dev 合作式鎖控制。

只有明確的 format／organize／manage 操作擴充使用者選定內容的修改範圍；
一般 setup／remove 維持原本 ownership 規則。key vault 匯入與硬體 key
provisioning 留待獨立後續工作，主機註冊不傳遞私鑰內容。

## 分層連線診斷 {#ssh-diagnosis}

```bash
dev ssh diagnose lab
dev ssh diagnose 192.0.2.30 --json
dev ssh diagnose lab --compare-qos --timeout 60s
```

TCP 能連線但 SSH 失敗，或普通 probe 無法解釋問題時，使用明確的診斷指令。
診斷接受 alias、hostname、IPv4／IPv6 literal，不要求有唯一可選的 Host 宣告；
裸 `dev ssh` 選單也提供入口。既有 `probe` 行為及 JSON 不變。

報告區分 effective config、DNS、route/interface、TCP、SSH banner、handshake、
host identity、authentication 與 remote exit。原生路由 collector 使用 macOS
`route -n get`、Linux `ip -j route get`，以及固定非互動 PowerShell 程式中的
Windows `Find-NetRoute`。不提升權限、安裝工具、修理網路／設定、更新 known_hosts
或匯入 host key。OpenSSH 評估設定及連線時仍可能執行使用者設定的 Match exec、
proxy 與 key helper。找到路由不等於 VPN 正常。

總期限預設 60 秒。設定與 DNS 各最多 5 秒，路由共 10 秒，TCP 共 5 秒，banner
最多 3 秒，每次 SSH 最多 15 秒；最多觀察四個解析位址。總期限優先，取消時保留
已完成階段。缺少工具、不支援的 scope／bind、無法辨識或截斷的證據保持
unknown／unsupported。Proxy 路徑跳過直接的本機 DNS／route／TCP／banner，避免
繞過設定的代理後作出錯誤比較。

診斷使用 fresh connection、BatchMode、strict host-key checking，不更新 host key、
不 forwarding、不執行 LocalCommand，遠端命令固定為 `exit 0`。Host key 拒絕與
認證失敗分開；TCP 或 banner 成功不代表登入成功。已認證但遠端命令失敗，列為
session failure。因此相較於設定成接受新 key 的普通 SSH，診斷可能在較嚴格的
host-key 邊界停止。

`--compare-qos` 明確允許 transport timeout 後的一次 `IPQoS=none` fresh 對照。
必須有 client marking 證據、未變的有效設定與路由觀察、相同 endpoint。Windows
stock OpenSSH 可能接受選項卻沒有 marking 能力，此時顯示無法比較。結果改善只能
說明相關性，不能歸因於某個 VPN 供應商，也不能證明線上 DSCP 為零。不修改全域
QoS，也不執行 raw DSCP socket probe。

JSON 使用 `schema_version: 1`、`kind: ssh_diagnosis`、`privacy: local`，包含本機
選定的 endpoint／user／identity path、階段原因碼、有限路由觀察、attempts 與
next-action codes。原始 debug log、server banner、config comments、proxy command
不輸出。不要直接公開此 JSON；feedback 使用獨立的白名單公開投影。操作失敗仍
輸出一份完整 JSON，並傳回非零 exit status。

Proxy 連線失敗時，最終目標的階段保持 unknown：helper 可能輸出自己的 jump-host
認證成功日誌，這不能證明最終目標也已通過認證。

SSH 指令失敗時，handshake／host-key／authentication 的成功日誌只保留為 reported
提示，狀態維持 unknown；只有 fresh login 以零 exit code 完成才確認整體登入。
Server banner／debug 訊息不能提供正向 client 證據。QoS 對照使用連線前的 marking、
實際連線進展或已確認成功的登入。

## 探索與 canonical machines

不帶 alias 執行 `dev ssh setup` 會開啟 host picker。可一次選多台 Tailscale peer、
LAN candidate 或既有 alias，再逐一設定 alias、remote user、port、authentication
及 optional registration。同一台 machine 可保留不同 user、key、route 的多個
alias。最後先預覽，再套用本機設定、registry 綁定及選定的 remote actions。

```bash
dev ssh list --tailscale --lan
dev ssh list --tailscale --lan --json
dev ssh discover --source tailscale --json
dev ssh discover --source lan --interface en0 --cidr 192.168.1.0/24
dev ssh discover --source lan --interface en0 --cidr 192.168.1.0/24 --ports 22,2222 --refresh

dev ssh setup lab --from tailscale:lab --user dev --config-only
dev ssh setup lab --from tailscale:lab --user dev --auth existing --to both
dev ssh setup lab --from lan:192.168.1.20:22 --user dev \
  --key ~/.ssh/id_ed25519 --target-os posix --to fleet
```

普通 `ssh list`、原有六欄 TSV 及 alias completion 仍只讀取靜態設定。
`--tailscale` 明確讀取 optional local `tailscale status --json`；`--lan` 加入
LAN cache，絕不掃描。Combined table 顯示 MACHINE、SSH ALIASES、TAILSCALE、
LAN、FLEET、HERDR、STATE。JSON 保留既有 alias document，新增 `machines`、
`sources`、`observed_at`；references 提供綁定指令使用的 exact selector。
搭配 discovery flags 的 TSV 是另一個四欄 machine projection：row ID、label、
state、comma-separated aliases。來源失敗、stale cache、disabled Herdr profile
都保持可見。

Tailscale 探索上限五秒、排除本機，保留 offline／unknown 狀態。CLI 缺少、daemon
不可用或 status 資料不完整只影響該來源；`doctor` 只檢查 executable 是否存在，
不查詢 daemon。Dev 不會 install、login、enable Tailscale SSH、修改 DNS 或 tailnet
access policy。

LAN discovery 只接受選定且直接連接的 IPv4 ranges；若無法唯一對應 eligible
interface，就必須指定 `--interface`。互動 wizard 可選介面及較小範圍。上限為
256 addresses、16 ports、4,096 endpoints、32 workers，整次最多 30 秒；預設 port
22。探索只做 bounded TCP/banner check 與 reverse-DNS lookup，不嘗試 SSH 登入。
Port open 與 SSH identification banner 是不同 observation。名稱只供編輯建議；
raw banner 不能成為 hostname、OS proof、host key 或設定。目前沒有 IPv6 range
scan、mDNS 或 background scan。

`$XDG_CACHE_HOME/dev/ssh-discovery/` 的 cache 五分鐘內視為 fresh，過期後仍保留
observation time。`--refresh` 跳過符合條件的 fresh LAN cache。讀取 cache 不會
重新掃描，也不能證明 endpoint 仍指向同一台 machine。

### Tailnet 上的 authentication

Dev 都使用 system OpenSSH：可以連到經 Tailscale 網路存取的普通 sshd，也可以
連到 Tailscale SSH server。普通 sshd 可接現有 public-key bootstrap；Tailscale SSH
使用 tailnet identity 與 policy，請用 `--auth existing` 做 fresh ordinary alias
login，不安裝 key。Host-key advertisement 只是提示，不是登入成功或 authentication
mode 的保證。已探索到且 advertised 的 Tailscale SSH port-22 endpoint 不接受 key
安裝；明確選定其他 port 的普通 sshd 才可走 key bootstrap。未使用選定 public key
的登入不能滿足 exact-key proof。

Optional `tailscale ssh` wrapper 額外提供 MagicDNS resolution、透過 `tailscaled`
的 userspace networking，以及 advertised SSH host-key 驗證。Dev 產生普通 OpenSSH
alias，不產生該 wrapper 的 ProxyCommand。Source setup 預設使用可用 IP，IPv4
優先；system DNS 可解析時，可用 `--hostname` 指定 MagicDNS FQDN。詳見
[Tailscale SSH](https://tailscale.com/kb/1193/tailscale-ssh) 與
[CLI wrapper reference](https://tailscale.com/kb/1080/cli#ssh)。

Source-aware setup 接受 `--from tailscale:<peer>`、`--from lan:<ip:port>`，或
`discover` 顯示的完整 candidate ID。LAN 地址若對應多個快取網路 scope，必須指定
精確 ID。過期或網路已變更的快取不會自動成為目前 endpoint 的身分；明確選取舊 ID
會保留 stale 標示。Foreign alias 必須有已知且匹配的 endpoint；不同 LAN／Tailscale
路徑的同機關係請使用 `machine link` 明確綁定。
Tailscale selector 可用 peer ID、唯一名稱或地址；同名時必須提供更精確 selector。
Noninteractive 新 discovery alias 必須明確指定 remote `--user`。沒有選 authentication
時只建立 connection 與 machine mapping；remote work 請選 `--auth existing`、
`--key` 或 `--generate-key`。`--to fleet|herdr|both` 必須明確要求；既有 `--fleet`
仍相容。`--herdr-label`／`--herdr-session` 設定 native profile。Herdr 安裝確認仍由
原生程式處理，remote server 仍需 Linux/macOS。Key generation 保留原本 passphrase
規則。

`--dry-run` 不寫入 config、registry、key 或 cache，也不做 SSH login；明確指定
Tailscale source 時仍可讀取 local daemon status。後續失敗會保留已完成 stage 的
結果；remote key installation 中斷仍是 unknown，應檢查目前來源後再重跑。

### 持久 machine identity

`paths.state_dir/machines/registry.db`，預設
`$XDG_DATA_HOME/dev/machines/registry.db`，保存 controller-local UUID 及明確
provider bindings。Discovery／listing 不會建立它。Canonical ID 與 `remotes.toml`
中的 remote `machine_id` pin 完全獨立；合併本機列不會寫入或驗證該 pin。
Connection settings 的 authority 仍是原來的 source config／provider catalog。

```bash
dev ssh machine show --json
dev ssh machine adopt --label lab --source <reference-id> --json
dev ssh machine adopt --label lab --source <reference-id> --apply --yes
dev ssh machine link --machine <uuid> --source <reference-id> --apply
dev ssh machine unlink --machine <uuid> --source <reference-id> --apply
dev ssh machine merge --machine <source-uuid> --into <survivor-uuid> --apply
```

TTY 的 `adopt` 也有 multi-select wizard。Registry actions 預設只有 preview，
`--apply` 加 confirmation 才提交 revision-bound transaction。`setup --machine
<uuid>` 可把 connection 接到既有 canonical machine。Native IDs 各自有 provider
scope；SSH alias 保存 declaration/source fingerprint。來源改變或缺失時保留
stale／unresolved，不會靜默重配 machine。Exact static IP/FQDN association 會標示
來源，短 hostname 相同不會授權 merge。

Unlink 保留 suppression，防止下次 discovery 自動連回。Merge 保留 survivor 的
label／preferred profile，舊 ID 留作 redirect。兩者不修改 provider config 或停止
remote session。Private registry 是 durable data；`dev cache clear ssh-discovery`
與 `cache clear all` 只清 observations，不刪 canonical identities 或 manual bindings。

## Key selection 與 optional registration

```bash
dev ssh key list
dev ssh key list --json
dev ssh key list --no-agent
dev ssh key list --alias lab --json
```

預設 key listing 有界地掃描 `~/.ssh` 下的 public-key files 與目前 SSH agent，不評估
alias。以 fingerprint 去重，顯示 algorithm、comment、source paths、source provenance
與可用 signer。`--no-agent` 跳過 agent enumeration。只有明確 `--alias` 才執行 plain
`ssh -G` 並使用該 alias 的 identity／agent settings；configured Match exec 或 resolver
可能執行。Listing 不讀 private-key contents、不 derive／generate key、不修權限，
也不嘗試 remote authentication。`--json` 輸出一個 `ssh_key_list` document，包含
candidates、completeness 與 source diagnostics；來源缺失或不可用不會冒充空的成功結果。

Setup wizard 選 existing key 時會開 catalog picker，另保留 **Enter a key path…**。
只有 public file 而找不到 signer 的候選仍可見，但不能直接滿足 bootstrap。可改選
其他 identity、把 signer 載入 agent，或提供對應 private-key path。Selected key 會先
驗證，再進入後續 registration prompts；generation 與 remote installation 保留明確
選擇及 native prompts。

不帶參數的 setup wizard 會在 host picker 之前檢查已存在的 `~/.ssh`、root config
與 `dev.d` 權限。選擇 key 後，再加入該 private/public companion 與必要 parent。
macOS/Linux 的簡單 tightening 使用 directory 0700、config/private file 0600；public
companion 只移除 group/world write bits，不新增權限或建立缺失檔案。ACL 或不支援的
security metadata 需手動處理；Windows 只驗證既有 ACL，不重寫。Repair preview
列出 exact paths／mode changes，另外確認後才收緊權限；即使後續
取消 wizard，已完成 tightening 也保留。它不是 recursive chmod：無關 Include files、
其他 keys、ownership changes、links/hardlinks 或不支援的 metadata 都需手動處理。
拒絕 repair 會在 config／authentication 前停止；一般 listing／dry run 不修權限。
主要 onboarding preview 仍先於 alias、registry binding、generated key 與 remote change。

Registration 改用獨立的 **Fleet**／**Herdr** checkboxes，預設皆不勾選。Space 切換，
Ctrl+A 全選／清除，Enter 確認，Esc 取消。零選擇代表不註冊；一項選對應 provider，
兩項則都註冊。Setup 零選擇會繼續且略過 provider-specific prompts；獨立 manage／
dashboard registration 則直接 no-op，不 authentication 或修改 provider。
CLI `--to fleet|herdr|both` 保持不變。

`[picker].command` 設定 external picker，單選（包含 key selection）預設使用 fzf；
executable 缺失或 command 為空時使用 built-in picker。多選一律使用 built-in
Bubble Tea picker，即使已安裝 fzf 也一樣。必要的 host／source selectors 仍不接受
空選擇；只有 optional registration 把零選擇視為確認略過。
