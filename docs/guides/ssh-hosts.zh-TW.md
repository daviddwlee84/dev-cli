---
description: 探索 OpenSSH alias、只管理 dev-owned host fragment、佈建 public-key access，並將驗證成功的 host 明確登記到 dev fleet。
authority: project
status: stable
verified_on: 2026-09-01
lang: zh-TW
---

# SSH Host 設定與佈建

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev ssh` 在既有 OpenSSH configuration 外加上一層保守的 lifecycle。OpenSSH 仍是 connection authority；dev 提供 static provenance、小型 owned fragment namespace、public-key bootstrap、fresh authentication proof，以及通往 [dev fleet](remote-fleet.zh-TW.md) 的明確 bridge。

## Authority 與 ownership

| Surface | Authority / owner | Dev 可以做什麼 |
|---|---|---|
| `~/.ssh/config`、其中的 foreign Includes 與 foreign `Host`/`Match` blocks | user + OpenSSH | static read；只透過 plain `ssh -G` evaluate alias；絕不 rewrite |
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
| `dev ssh list` | `--json` 或 `--format tsv` | bounded static user-config scan；沒有 subprocess 或 network |
| `dev ssh show <alias>` | `--json` | static definitions，加上 plain `ssh -G <alias>` 的 effective values |
| `dev ssh setup <alias>` | 下方列出的 connection、key、route、fleet、plan、confirmation、JSON flags | owned local config、public-key bootstrap、optional fleet registration |
| `dev ssh probe <alias>` | `--json` | sharing disabled 的單次 fresh ordinary BatchMode login |
| `dev ssh remove <alias>` | `--fleet`、`--dry-run`、`--yes`、`--json` | 只移除 canonical dev-owned SSH/fleet fragments |

`dev doctor` 也會回報 local `ssh`/`ssh-keygen` capability、static Include reachability、managed namespace permission/ACL，以及 generated fleet-fragment health。它不會執行 `ssh -G`、聯絡 host 或進行 repair。

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

非 dry-run 的 full setup 必須明確且二選一使用 `--key` 或 `--generate-key`。JSON mode 即使在 terminal 上也不互動；任何 noninteractive full setup 還需要 `--target-os`，local mutation 則需要 `--yes`。`--yes` 只批准 local plan。Password/passphrase 與 host-key interaction 仍由 native OpenSSH 負責；batch mode 會回傳 `interaction_required`，不會自行發明 credential path。

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
- bulk/cloud/Tailscale/chezmoi fleet import、dedicated SSH TUI 或 background probing。

Server policy 若超出 verified POSIX/Windows installer contract，dev 會回報 manual remediation，不會靜默削弱 protection。

## 來源

- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/sshhost`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/sshhost)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/help/topics/ssh.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/ssh.md)
