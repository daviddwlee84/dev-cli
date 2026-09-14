---
description: 探索 OpenSSH alias、只管理 dev-owned host fragment、佈建 public-key access，並將驗證成功的 host 明確登記到 dev fleet。
authority: project
status: stable
verified_on: 2026-09-13
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
| `~/.ssh/dev.d/<alias>.conf` | `dev ssh setup/remove` | 只 create、reconcile 或 remove canonical v1／v2 file，其內容是 allowlisted single `Host` block |
| local key files | user + native `ssh-keygen` | 驗證 explicit key、經確認後 derive 缺少的 `.pub`，或以 no-replace 方式產生 Ed25519 pair／FIDO stub-public pair；絕不複製 private bytes |
| remote `authorized_keys` | remote OpenSSH account | idempotently append 一筆 bounded normalized public record；絕不 remove 或 revoke |
| primary `remotes.toml` | user via `dev fleet config` | read/merge；SSH setup 絕不 rewrite |
| sibling `remotes.d/ssh-<alias>.toml` | `dev ssh setup/remove --fleet` | 只有 fresh ordinary login 成功後才 create；只有明確要求才 remove |

Foreign alias 仍可供 `list`、`show`、`probe`、key bootstrap 與 fleet registration 使用，但 `setup` 會拒絕 connection flags；dev 不會與既有 definition 競爭。新的 dev-managed alias 使用 portable lowercase exact-name grammar，且只有一個 deterministic file，內容可包含 `HostName`、optional `User`、`Port`、`ProxyJump`、`IdentityFile` 與 `IdentitiesOnly`。還需要 `IdentityAgent` 或 `SecurityKeyProvider` 的檔案改用 `v2` header；dev v0.2.37 及更舊版本會把 v2 file 視為非 dev 管理並拒絕 setup/remove，因此選用 agent key 前請先升級所有共用 `~/.ssh` 的 dev。Arbitrary directives 與 wildcard/`Match` blocks 不在 managed scope。

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

Public-key bootstrap 二選一使用 `--key` 或 `--generate-key`。在互動 terminal 執行 `dev ssh setup <alias>` 而未帶這兩個 flag 時，會開啟 key picker：列出本機 keys、**+ Generate a new key**（預設 `~/.ssh/id_ed25519_dev_<alias>`）與 **Enter a key path…**；noninteractive setup 仍須提供其中一個 flag。JSON mode 即使在 terminal 上也不互動；任何 noninteractive full setup 還需要 `--target-os`，local mutation 則需要 `--yes`。`--yes` 只批准 local plan。Password/passphrase 與 host-key interaction 仍由 native OpenSSH 負責；batch mode 會回傳 `interaction_required`，不會自行發明 credential path。

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

`--generate-key` 預設透過 native `ssh-keygen` 產生 Ed25519。`--key-path` 可指定 `~/.ssh` 底下任何檔案（含子資料夾）；`~/.ssh` 下一層若不存在，dev 會以 `0700` 建立並顯示在 plan 中，更深的資料夾需先用 `mkdir -m 700` 建立。位於 `~/.ssh` 以外、使用不支援的展開，或以 `.pub` 結尾的名稱會被 block 並說明原因。互動 picker 的 **+ Generate a new key** 會詢問路徑（只填名稱時放在 `~/.ssh`）與可選 comment。Interactive mode 將 hidden passphrase prompt 交給它；noninteractive Ed25519 generation 必須明確使用 `--no-passphrase`；security-key generation 則要求原生互動確認。兩個 half 先在 private staging basename 產生，依 fingerprint 確認相符、harden，再以 no-replace semantics publish；任何 destination collision 都會 block，不會 overwrite。成功產生的 pair 在後續 route/bootstrap/fleet failure 後仍保留，`dev ssh remove` 也絕不移除。

所有 output 都是 content-safe：可以包含 fingerprint、algorithm、path、digest 與 boolean；不包含 private bytes、passphrase、password、完整 public-key line、agent payload 或 unredacted command-like SSH option。

### FIDO security key（YubiKey 與相容 authenticator）

搭配 `--generate-key` 選擇 `--key-type ed25519-sk` 或 `ecdsa-sk`，會要求建立新的
security-key identity，而不是上傳現有私鑰。使用原生 FIDO authenticator 時，簽章私鑰
不會被匯出；本機 identity file 是受保護的 key handle（stub），另有 public companion。
自訂 provider 的實際儲存方式由該 provider 定義。Stub 仍需保護與備份；non-resident key
無法只靠 authenticator 還原。

```bash
# 原生互動 touch／PIN 流程；建立前先審閱。
dev ssh setup lab --generate-key --key-type ed25519-sk \
  --key-path ~/.ssh/security/id_lab --sk-application ssh:lab --target-os posix

# 支援較多 FIDO2 裝置，並儲存 resident handle、要求 user verification。
dev ssh setup lab --generate-key --key-type ecdsa-sk \
  --key-path ~/.ssh/security/id_lab_resident \
  --sk-resident --sk-verify-required --target-os posix
```

- `ed25519` 仍是預設的軟體 key。`ed25519-sk` 需要相容 authenticator（YubiKey firmware
  5.2.3+）；`ecdsa-sk` 支援較多 FIDO2 裝置。
- `--sk-provider internal` 是預設值，也可明確指定 provider library 的絕對路徑。
  審閱過的 generator／client／provider 路徑會綁定此次操作；選 provider 不會更改 PATH
  或安裝軟體。
- `--sk-resident` 也把 handle 儲存在 authenticator，可能佔用持久 credential slot，
  通常要求先設定 PIN。
- `--sk-verify-required` 要求原生 key 在簽章時做 user verification；與 resident 儲存
  是不同選項，也不會改寫遠端 `authorized_keys` options 或認證 policy。
- `--sk-application` 是有長度限制、以 `ssh:` 開頭的 application name。Generation
  options 不可與既有 key、named agent 或既有認證模式併用。硬體產生要求原生互動確認；
  `--yes`、`--no-passphrase` 都不能把它變成無人值守的 provisioning。

Capability observation 只讀 stat／路徑資訊，不執行 `ssh-keygen -K`、載入 provider、
列舉裝置，也不把 `ssh -Q key` 當成硬體證明。已知 Apple 系統 OpenSSH 沒有內建 USB
FIDO backend；請讓 **`ssh` 與 `ssh-keygen` 兩者**都使用支援 FIDO 的安裝版本，例如
含 libfido2 的 Homebrew OpenSSH。只找到自訂 binary／library 時，能力仍是 **unknown**；
使用者明確審閱的互動嘗試可帶著警告繼續，但不代表已找到裝置或一定能完成認證。
Native Windows controller 尚未實作工具身分驗證，因此會擋下新的 hardware generation；
既有原生 key／認證流程維持不變。這不禁止受支援的 controller 連往 Windows SSH server，
server 是否接受該演算法仍由實際認證驗證。

Managed alias 會在 v2 fragment 記錄 `SecurityKeyProvider`。Foreign alias 不會被改寫，
原生 provider policy 必須已相符。從遠端 `fleet:HOST` 匯入 profile／hop 時不允許產生
新的 hardware key；請先匯入，再另外設定本機 alias。把一般或 LAN alias 註冊**到**
fleet／Herdr 仍受支援。

Touch、PIN 與 stub passphrase 由原生 `ssh-keygen` 處理。Exact-key proof 可使用獲授權
的原生互動，但仍限 publickey、保留 host-key policy，且不在 selected-key proof 內改用
密碼；其他 proxy hop 保留原有 batch policy。互動 setup 成功不保證無人值守的 fleet／
背景存取可用：需要 PIN、passphrase 或 touch 的 key，之後仍可能需要原生互動。
Dev 不會為了自動化而停用這些要求。Enrollment 一旦開始，即使後續步驟失敗，
也可能已留下硬體 credential。
Dev 會分別回報 hardware created／unknown 與 local-file creation，只保留已驗證的 SK
stub／public recovery pair 並列出路徑；不會自動重試 enrollment、刪除硬體 credential，
或暗中執行 PIV／OpenPGP provisioning。重新產生前請先檢查保留的結果。

**macOS Secure Enclave：**自動 `apple-secure-enclave` 建立尚不可用，必須先驗證精確
identity-to-stub mapping 與原生輸出 contract。該選項會在 `sc_auth` 或硬體作用前停止；
library 存在不代表已就緒。可使用原生設定正確的既有 stub，或 Secretive agent key。
Apple Passwords 沒有文件化的 SSH-key item／SSH agent；Keychain passphrase 儲存
（`ssh-add --apple-use-keychain`）仍會留下 private-key file，不是 hardware-only backend。

參考 [Yubico FIDO2 SSH](https://developers.yubico.com/SSH/Securing_SSH_with_FIDO2.html)、
[Secretive](https://github.com/maxgoedjen/secretive)，以及原生
[`sc_auth`](https://keith.github.io/xcode-man-pages/sc_auth.8.html)／
[`ssh-keychain`](https://keith.github.io/xcode-man-pages/ssh-keychain.8.html) 手冊。
實際 enrollment 與 Touch ID 是另外的使用者協助驗證；fake runner 或交叉編譯不能證明它們成功。

### 先建立 vault key，再選取 agent identity

`dev ssh key create` 是獨立的 vault-creation 流程，不會遠端安裝 SSH key、改寫 SSH
設定、註冊 fleet／Herdr、匯入舊私鑰，或刪除來源／vault item。Setup key picker 也提供
vault creation；明確選取 key 後才回到 SSH 表單。Vault 建立有自己的審閱／確認；之後
取消 SSH 表單**不會**撤銷已完成的 vault creation。

`--dry-run` 只驗證／顯示意圖，不查詢 provider CLI 或 agent；account／vault／agent
觀測維持 unknown，也不是可重用的 guarded provider plan。實際建立前仍會取得新的
service-bound plan。1Password 使用確切 vault ID；互動執行可審閱目前原生 account，
但使用 `--yes` 或非互動的實際 RPC creation 必須指定確切 `--account` ID。
Title（預設 `SSH key`）只是標籤，不用於去重或識別 item；command 不接受 positional arguments。

**1Password：**穩定版原生 `op` 2.x（至少 2.20.0）在選定 vault 產生預設 Ed25519 item；
dev 保留確切回傳 item ID，再另外驗證 public key。Item 可能已存在，但 desktop agent 尚未提供它：請自行
解鎖／同步原生 app、設定 agent 的 vault selection，再選取 fingerprint。Dev 不編輯
`agent.toml`，不重試結果模糊的 create，也不暗中改選其他 identity。

**Bitwarden desktop handoff：**可由 picker 選取，或執行 `dev ssh key create --provider
bitwarden --desktop`。不能搭配 account／vault scope、experimental／native-context flags
或實際 JSON execution。即使 `--yes` 也需要互動確認 GUI 完成並明確選取 fingerprint
（仍可用 `--dry-run --json`）。先從一個確切 Bitwarden agent socket 取得完整 public-key
inventory，接著用原生 desktop UI 建立 key，再明確 refresh、選取新**可見**的 fingerprint。
這不是新 vault item 已建立的證明：也可能是既有 key 剛變成可見。Baseline 不可用不能當成
空 vault；agent 重啟／socket 改變會讓前後比較失效。Dev 不操作 GUI，也不猜測 item ID。

**Bitwarden 記憶體／native-context 模式（experimental）：**CLI schema 目前限定
`2026.3.0`，需要兩個分開的同意：

- `--experimental`：允許新的 Ed25519 私鑰短暫存在 dev 記憶體，並透過 stdin 交給原生 CLI。
- `--native-context`：將 endpoint／設定 authority 委由審閱過的原生 Bitwarden account／
  profile 處理。這**不是 endpoint attestation**。

一般 `--yes` 不能代替其中任何一項。審閱會列出觀測到的 user、profile、CLI entrypoint／
runtime 與工作目錄。`BW_SESSION` 只來自原生 environment；不要把 session、master password
或 key 貼進 agent／聊天。Session 是解密 credential，不是 account／server ID。
Plan 只保留私有比較資料，不匯出 session 值或 hash。Apply 重新捕捉／驗證執行 context，
然後 metadata、create、post-check 都使用同一份明確固定的 environment／cwd。
不更改全域 environment、不 login／unlock、不改 provider 設定，也不解析原生 vault data。
Metadata／create 強制原生 no-interaction，避免把 private JSON stdin 當成 credential prompt
的輸入。

Entrypoint／profile 必須能安全綁定，包括受支援的 Node package／runtime 形式與 portable
profile 優先順序。不支援的 script wrapper／shebang、不安全／改變的 profile，或尚未支援的
原生 Windows attestation 都會 fail closed。Native executable 檢查綁定受保護的 filesystem
identity 與 ELF／Mach-O 結構，不能據此辨別所有 compiled runtime shim。原生 executable
行為與 dependencies 仍屬信任邊界，原生設定也不會變成 transactional。Bitwarden base `serverUrl` 只是 advisory 顯示值，
不能用它推斷 cloud region 或獨立 API 設定。因此結果一律維持 `endpoint=unverified`，並把
`native_context=observed_consistent` 與 creation status 分開。要求 endpoint attestation 的
Bitwarden 模式仍不支援。

權限擷取要求 inode、owner、mode 相符，且兩次安全 ACL 觀測一致。同層無關目錄的變動
不會只因目錄彙總 ctime 改變就讓 profile 失效；regular file 與 symlink 的 change-time
檢查仍嚴格保留，ACL 與 replacement 檢查也沒有放寬。

私鑰產生／編碼只使用記憶體與 stdin，不寫 plaintext key file、不用 clipboard 或 private-key
argv。Owned buffers 會盡力清除，但不保證 RAM／swap／core dump 完美抹除；原生 provider
的儲存也仍由它管理。CLI／TUI 只輸出 metadata、fingerprint、item ID，不輸出 private／session
bytes 或完整 public-key line。

若 item 已建立但 agent 尚不可見，狀態仍是 **created**，並保留 receipt 與指引。回應遺失、
public result 格式錯誤、post-check 改變，都可能留下 readiness／context 不確定的 item；
保留已知 item ID，檢查後才開始新的 creation plan。Controller-local attempt ID 可區分
account／vault／title 相同的兩次 unknown 嘗試，但不代表猜測出的 provider item ID。
Receipt 不依附暫存 UI dialog，延遲結果不能取消另一個 action，也不會隨表單替換而遺失。
Plan 只能嘗試一次；item creation 或 public agent listing 都不是 SSH authentication proof，也不是 password-save authority。只有後續
一般 SSH setup 的審閱／proof，才授權它自己的設定、bootstrap 與選用 registration。

Bitwarden type-5 與 1Password 的真實整合測試需要另外取得使用者同意；fake runner 不能取代。
Diagnostics、文件檢查或測試套件，不會暗中建立或刪除真實 vault item。

### 選擇簽章 key 的存放方式

| Backend | Key 儲存／互動 | Dev 的支援邊界 |
|---|---|---|
| Bitwarden | 加密 vault；解鎖後 agent／CLI 可在記憶體使用解密資料 | Named Unix agent；明確 desktop handoff，或另行同意的 experimental native-context creation |
| 1Password | 加密 vault；原生 approval／session 與 agent policy | Named Unix agent 與明確的原生 vault creation；agent 是否可見仍是另外的觀測 |
| YubiKey／FIDO2 | Authenticator 簽章；本機保護 handle，可選 resident | 受支援 controller 的明確互動 FIDO generation；不匯入舊私鑰，也不自動移除 credential |
| Secretive（macOS） | Secure Enclave non-exportable key；原生 UI／approval | 選取既有 Secretive agent；dev 不替它 provisioning |
| Apple CTK／Secure Enclave | Non-exportable P-256 CTK identity 與本機 SK handle | 使用已配置的原生 stub；自動建立須先驗證 identity mapping，目前停用 |
| Apple Passwords／Keychain | Passwords 沒有文件化 SSH agent；Keychain 可存 key-file passphrase | 原生 passphrase 整合不等於 hardware-only，也不會消除 private-key file |
| KeePassXC | 加密資料庫與 SSH-agent integration | 先設定原生工具，再選取已驗證 socket；不代表私鑰不可匯出 |
| ssh-tpm-agent（Linux-oriented） | TPM 2.0 key generation 與本機 sealed `.tpm` files | 只整合既有 custom agent socket；dev 不做 TPM provisioning／migration |
| gpg-agent／OpenPGP card | 依原生設定使用軟體或卡片 key | 既有 custom agent socket；不做 OpenPGP provisioning |
| YubiKey PIV | 獨立的 generation／import 與 slot lifecycle | 不寫入／覆蓋 slot、不做 PIV provisioning；使用既有原生認證 |
| Windows OpenSSH／Hello | 依已安裝 build／provider 決定原生支援 | 遵循 upstream 原生指引；不宣稱 Windows Hello 是 dev backend，明確 agent／hardware creation 仍須 native attestation 才能開放 |

Vault key 通常仍可由原生 provider 匯出；「dev 不寫 private file」不等於不可匯出。
Agent 記憶體、OS swap／crash 處理與原生 provider 儲存，也不在完美抹除保證內。
不要把匯入舊 key 後自動刪除本機來源，當成硬體產生的替代方案；能否無人值守運作，
還取決於 PIN／touch 與 agent approval policy，而不只是 key 放在哪裡。

參考：[Bitwarden SSH agent](https://bitwarden.com/help/ssh-agent/)、
[1Password SSH](https://www.1password.dev/ssh/agent/)、
[KeePassXC agent integration](https://keepassxc.org/docs/KeePassXC_UserGuide#_ssh_agent_integration)、
[ssh-tpm-agent](https://github.com/Foxboron/ssh-tpm-agent)、
[GnuPG agent documentation](https://www.gnupg.org/documentation/manuals/gnupg/Agent-Options.html)、
[Windows FIDO/U2F](https://github.com/PowerShell/Win32-OpenSSH/wiki/FIDO---U2F-usage)。
Windows 來源描述 FIDO／U2F 流程，不是 Windows Hello key storage；upstream capability
也不能證明 dev 的原生 Windows gates 已通過測試。

## ProxyJump 與 remote operating system

Dev 會對 target 與每個 discovered jump 執行 plain `ssh -G`，將 nested/comma-separated `ProxyJump` route 依 outermost-first flatten，並支援 alias、`user@alias`、`alias:port` 與 bracketed IPv6 forms。Cycle、repeated hop、unsupported URI/`ProxyCommand` route 與 ambiguous override 都會被拒絕，不會猜測。

```bash
dev ssh setup lab --key ~/.ssh/id_ed25519 --target-os posix \
  --hop-os bastion=posix --hop-os winjump=windows
```

`--target-os` 只套用 final target。Ambiguous jump 請使用 repeatable `--hop-os`；interactive mode 會詢問 `Remote OS for <alias> (posix/windows) [posix]`（也接受 `p`／`w`），noninteractive mode 則要求每個 unknown hop 都指定。每個 hop 會先以 ordinary fresh BatchMode authentication probe。已能運作的 jump 不會被修改，除非明確加上 `--install-on-working-jump`。

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
| `ssh discover --json` | `ssh_discovery` | source 狀態、scope、candidates、觀察時間與完整性；LAN report 另含 `probes` 計數與 `warnings` |
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
- 既有 private-key migration／import、vault-item deletion、automatic password fallback、private-key copying 或 weakened host-key check（明確的新 vault-key creation 與 credential-provider password storage 是另外支援的流程）；
- cloud/chezmoi fleet import、mDNS、IPv6 range scanning 或 background probing。

Server policy 若超出 verified POSIX/Windows installer contract，dev 會回報 manual remediation，不會靜默削弱 protection。

## 來源

- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/sshhost`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/sshhost)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/help/topics/ssh.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/ssh.md)

## 主機管理與設定整理

在終端執行 `dev ssh` 會開啟選單；管線仍顯示 help。設定整理預設只做
formatting，群組重整必須另外選擇。**Set up or install an SSH key for a host**
可從已設定的 alias 選擇（或輸入 alias），再進入 setup 的 key picker。

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
不查詢 daemon。Discovery 不會 install、login、enable Tailscale SSH、修改 DNS 或 tailnet
access policy。

LAN discovery 只接受選定且直接連接的 IPv4 ranges；若無法唯一對應 eligible
interface，就必須指定 `--interface`。互動 wizard 可選介面及較小範圍。上限為
256 addresses、16 ports、4,096 endpoints、32 workers，整次最多 30 秒；預設 port
22。探索只做 bounded TCP/banner check 與 reverse-DNS lookup，不嘗試 SSH 登入。
Port open 與 SSH identification banner 是不同 observation。名稱只供編輯建議；
raw banner 不能成為 hostname、OS proof、host key 或設定。目前沒有 IPv6 range
scan、mDNS 或 background scan。

`ready` 代表所有規劃的 probe 都已完成，不代表找到主機。LAN report 會新增 `probes`
（attempted、open、refused、timeout、unreachable、other 計數）；若完整掃描沒有找到
任何 candidate 且有 unreachable probe，會加上 `no_reachable_endpoints` warning。
macOS 上常見原因是區域網路（Local Network）隱私權限：終端機 App 未獲授權時，連線會
以 unreachable 失敗，即使同一個 shell 裡的 `nc` 可以連上。請在「系統設定 > 隱私權與
安全性 > 區域網路」允許該終端機 App 後重試。由 launchd 直接啟動、而非由 App 啟動的
multiplexer 或 shell 可能無法取得授權，請改從 App 終端機執行探索。

`$XDG_CACHE_HOME/dev/ssh-discovery/` 的 cache 五分鐘內視為 fresh，過期後仍保留
observation time。`--refresh` 跳過符合條件的 fresh LAN cache。CLI 與 wizard 只重用
有 candidates 的 fresh LAN 掃描，空的結果會重新掃描。讀取 cache 不會重新掃描，也不能
證明 endpoint 仍指向同一台 machine。

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
dev ssh key list --agent bitwarden
dev ssh setup lab --identity-agent 1password --key SHA256:… --target-os posix
```

預設 key listing 有界地掃描 `~/.ssh` 下的 public-key files 與目前 SSH agent，不評估
alias。以 fingerprint 去重，顯示 algorithm、comment、source paths、source provenance
與可用 signer。`--no-agent` 跳過 agent enumeration。只有明確 `--alias` 才執行 plain
`ssh -G` 並使用該 alias 的 identity／agent settings；configured Match exec 或 resolver
可能執行。Listing 不讀 private-key contents、不 derive／generate key、不修權限，
也不嘗試 remote authentication。`--json` 輸出一個 `ssh_key_list` document，包含
candidates、completeness 與 source diagnostics；來源缺失或不可用不會冒充空的成功結果。

Key 也可以留在密碼管理器的 SSH agent，而不是 private file。
`--agent bitwarden|1password|secretive|<absolute socket>` 會把該 agent 加入列表；
setup key picker 會加入所有 socket 存在的 provider。Dev 只用 `stat` 尋找 socket：
Bitwarden（App Store、.dmg、Linux、Snap、Flatpak 路徑）、1Password 與 Secretive；
已安裝但找不到 socket 的 provider 會提示啟用 agent 的步驟（Bitwarden：Settings →
啟用 SSH agent），`dev doctor` 也會顯示同樣的警告。找不到 socket 不代表已證明
agent 被停用；app 可能未開啟，或正在使用其他路徑。Provider 路徑偵測是被動的；
明確選取／列出 key 才會查詢 SSH agent，而不會執行 provider 的 vault CLI。Setup dry-run
不寫檔，也不連線到 host，但選取 agent key 時仍會查詢該確切 agent。Windows 共用的
`\\.\pipe\openssh-ssh-agent` 不會歸屬給任何廠商。Native Windows 在能驗證 pipe 身分與
擁有者之前，會停用明確 named／custom agent 選取；既有原生 OpenSSH ambient 認證
流程維持不變。多個 agent 提供同一把 key 時，
優先順序為 alias 的 `IdentityAgent`、指定順序、`SSH_AUTH_SOCK`。選擇 named-agent key，
或執行 `dev ssh setup <alias> --identity-agent <agent> --key <SHA256 fingerprint 或 .pub path>`，
只會把 public line 以 no-replace 寫入 `~/.ssh/dev_agent_<provider>_<alias>.pub`，並寫入含
`IdentityAgent`、該 `IdentityFile` 與 `IdentitiesOnly yes` 的 v2 managed alias，讓之後的登入
與 bootstrap proof 都使用這把 agent key。Foreign alias 絕不會被修改：若重新取得的
有效設定已符合所選 agent policy，可以直接 bootstrap，不需改寫。否則 setup 會以
`identity_agent_manual` 停止並列出要手動加入的設定。

Named-agent setup 可以把一般或 LAN 發現的 alias 註冊**到** fleet 或 Herdr。
但**從** `fleet:HOST` 匯入連線 profile（包含 hop key）尚未支援攜帶 controller 選定的
named-agent 計畫：這些選項不可用，`--identity-agent` 會在 import 前被拒絕。可先使用
controller 既有認證或只配置模式匯入，再另外 setup 已匯入的 alias。
Remote provider 路徑絕不會被複製到 controller。

Setup wizard 的 authentication 選單提供 configure only、existing authentication 或
**Install an SSH key (existing or new)**；後者開啟 catalog picker，另有
**+ Generate a new key** 與 **Enter a key path…**。Fleet import 與 per-hop key 也使用
同一個 picker。
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

## SSH key doctor

```bash
dev ssh key doctor
dev ssh key doctor --json
dev ssh key doctor --fix
dev ssh key doctor --key ~/.ssh/custom-key --key ~/.ssh/other-key.pub
dev ssh key doctor --key ~/.ssh/custom-key --fix --yes --json
```

預設 report 在 `~/.ssh` 下進行 bounded metadata-only scan，檢查 public-key paths
及已存在的 private companions，也檢查沒有 `.pub` 的 exact standard private-key
filenames（直接位於 `~/.ssh`）；不會把每個無副檔名檔案都猜成 private key。只走訪
直接、自己擁有的 directories；非 candidate 的 symlink 略過且不跟隨，recognized
key-file symlink 則使 scan incomplete。Canonical SSH setup paths 另由 permission
plan 驗證。Repeat `--key PATH` 可把
scope 限定為選定 path／companions，加上 canonical `~/.ssh`、root config、`dev.d`；
也適用於沒有 public companion 的 custom private key。兩種模式都不讀 key contents、
不查詢 agent、不執行 `ssh`／`ssh-keygen`、不評估 alias，也不登入遠端。

沒有 `--fix` 時只報告 observations 與 proposed permission changes。Repairable
findings 回傳成功，可先看 report 再明確修復。Blocked 或 incomplete scan 回傳錯誤，
即使有 `--fix` 也不授權任何 write。不建立 missing paths；unsupported ownership、
links/hardlinks、ACLs 或 security metadata 保留 manual remediation。

`--fix` 先顯示具體 path／mode preview，再要求 confirmation。`--yes` 只能搭配
`--fix`，noninteractive 或 JSON repair 必須提供。選定 repairs 形成一個 source-bound
plan，在既有 SSH operation lock 下套用，改變前重新驗證，完成後再檢查。macOS/Linux
只可收緊支援的 modes；Windows 只驗證既有 ACL，不重寫。中斷或部分完成時保留已
完成 tightening 並回報 outcomes，不放寬權限來模擬 rollback。Setup wizard 使用
相同 permission core，但仍只檢查 baseline paths 與選定 key，不自動修全部其他 keys。

`ssh key list` 保留 `public_key_unreadable`／`private_key_permissions` 等 stable
codes，另提供實際原因及適用的下一步。Permission／path failure 提示執行
`dev ssh key doctor`；missing／malformed public file 則有具體 path／format guidance。
Configured `.pub` 缺少
可能只是未使用的 OpenSSH default，不能推定 private key 存在。Doctor 檢查 path／
permission safety，不修復格式錯誤的 public-key contents，也不證明 signer 或 remote
login 可用。

JSON 使用 `schema_version: 1`，kind 為 `ssh_key_doctor_plan`／`ssh_key_doctor_result`。
內容包含 `scope`（`discovered`／`selected`）、`complete`、`key_paths`、exact permission
`plan`；apply result 保留 per-path outcomes，完成 repair 後驗證時另有 `recheck`。


## Fleet source profiles 與本機 routes

```bash
dev ssh discover --source fleet --host gateway --host lab --refresh
dev ssh list --fleet --json
dev ssh setup internal-api --from fleet:gateway/api --config-only
dev ssh setup internal-api --from fleet:gateway/api --auth existing
dev ssh setup internal-api --from fleet:gateway/api --dry-run --json
```

Fleet discovery 只讀明確選定的 sources（最多 16 個）。沒有 `--host` 時由互動
picker 選取；非互動模式必須提供 host names。不會遞迴探索 source 自己的 fleet。
Metadata 先使用 BatchMode；只有既有 fleet password source 可授權 password retry，
configured prompt 也需要互動 controller。`--refresh` 略過 fresh cache。
`ssh list --fleet` 只顯示 cached source profiles、不連線；預設 `ssh list` JSON／TSV
仍維持 static contract。

相容的 remote `dev` 匯出 bounded static alias inventory，不執行遠端 `ssh -G`、
resolver 或 agent。第一次明確 capability exchange 可建立該 remote user 的 dev UUID；
observed UUID 只作回報，不會自動寫入 fleet `machine_id` pin 或合併 machines。
沒有／版本較舊的 dev、驗證失敗、timeout、source identity 變更與 incomplete response
保持不同狀態；既有 metadata cache 可以保留顯示，但會標示 stale。

Remote profile ID 由 source UUID、login user、SSH root、alias 決定；fingerprint
表示觀察到的 configuration revision。可讀 selector 是 `fleet:HOST/ALIAS`，名稱含
分隔符時使用 percent encoding；automation 也可使用 discovery 回傳的精確
`fleet-ssh:` ID。不同 source 的同名 alias 不代表同一條連線。

Setup 只對選定 remote route 執行原生 `ssh -G`，再預覽本機 managed aliases 與完整
ProxyJump route；這個明確 resolution 可能執行既有 Match exec／resolver。Local
gateway 與每個 hop 保留自己的 user、port、credential context。相容 local alias
可重用，foreign definitions 不會改寫。無法移植的 routing／source-local command
policy 必須明確設定本機 profile。Remote IdentityFile／IdentityAgent 與 trust-file
paths 不會複製；imported route 由本機 SSH configuration 與選定 controller keys 控制。

預設仍只設定 configuration。Key installation、per-hop key choices
（`--hop-key local-alias=key-path`）與 provider registration 需明確選取；選 target key
不代表授權把它安裝到每個已可登入的 jump。套用前會重新確認 source identity、
fingerprint 與 route facts。`--dry-run` 只使用 cached remote inventory／resolution
及 static local facts；缺少 resolution 就回報，不會因此執行 SSH 或寫入。

## 選擇 SSH 在哪台機器執行

```bash
dev ssh connect internal-api
dev ssh key list --on fleet:gateway --alias api --json
dev ssh connect api --on fleet:gateway
dev ssh connect api --on fleet:gateway --key-id SHA256:FINGERPRINT
```

一般 connect 在 controller 執行 SSH。`--on fleet:HOST` 使用 source host 自己的
native SSH、alias、agent 與 key files。Remote key listing 只是 display metadata，
其中 paths 不會當成本機 paths。`--key-id` 會在真正執行 SSH 的 host 重新選取並驗證，
包含 agent policy。Private keys 不會轉移。此命令只開互動 session、關閉 agent forwarding、
保留 child exit status；不重試已啟動的 session，也不接受額外 remote command args。

一般 native connection 保留其餘 user-authored SSH behavior。Password／exact-key workflow
若需要 private temporary configuration，會保留 supported settings；不支援的 LocalCommand、
port forwarding、SetEnv 或含 `%` expansion 的 RemoteCommand 會拒絕，不會靜默省略。

沒有 selected key 或 managed password context 時，opaque ProxyCommand alias 可使用
guarded native-only connection：重新檢查完整 user Include closure 與 native effective
settings，不虛構 route hops 或 exact-key proof。ProxyJump cycles 與不支援的 exact-key
操作仍拒絕；opaque route 也不能匯入為本機 ProxyJump profile。

## 衍生缺少的 public companion

```bash
dev ssh key derive ~/.ssh/custom-key
dev ssh key derive ~/.ssh/custom-key --apply
dev ssh key derive ~/.ssh/custom-key --apply --yes --json
```

Derive 接受 `~/.ssh` 內的 private identity，預設只預覽缺少的 `.pub`，不執行
ssh-keygen 或讀 private contents。`--apply` 確認後才執行 native `ssh-keygen -y`；
非互動／JSON apply 需要 `--yes`。Encrypted-key prompt 由 native ssh-keygen 處理。
已有 companion 不覆寫。操作會在 SSH operation lock 內重驗 source path，只發布
public companion，不安裝 key 或修復 mode；permissions 問題先用 `ssh key doctor`。

## 記住成功登入的 SSH password

Controller-driven password login 有相符的 authentication evidence 後，dev 提供
**Yes / No / Never**，預設選 **No**。Yes 將 password 存入選定 provider；No 只保留在
本次 operation memory；Never 只針對該 origin/profile/route/host/user/port context
持久停止詢問，不是全域偏好。Unknown、MFA、passphrase、host-key prompts 不會
被當成可重用的 account password。

Setup／connect 的 `--password-store system|bitwarden` 選擇保存 provider，預設 system。
macOS 使用 Security framework、Windows 使用 Credential Manager，Linux 使用可用的
Secret Service。Bitwarden 需要已安裝且解鎖的 CLI，create/edit payload 只走 stdin。
Password 不進 argv、environment、一般檔案、logs 或 JSON。Provider unavailable／
denied 不會退回明文檔；不確定的 write 保留 pending／unknown，不自動重試。

`$XDG_CONFIG_HOME/dev/ssh-credentials.toml` 只存 context、ask/never policy、provider
reference 與 write-state metadata。可編輯 policy 重新啟用 Never context。移除
reference 會停止 dev 重用，但不刪除 vault item 或修改 remote password；vault
清理由 provider 原生介面處理。既有 explicit fleet password source 保有優先權。
Discovery 不啟用 saved-reference lookup。`connect --on` 的 remote source-to-target
password 不屬於 controller save workflow。

Password-save 功能不包含將 SSH private key 匯入 vault、YubiKey provisioning，或匯出 Apple
Passwords；它們是獨立的未來 migration workflows。

## SSH connection view

在原生 Android／Termux 中，SSH 設定與 dev state／cache 應放在 app 的私有家目錄。
dev 會驗證 Termux app 的目錄邊界，不要求讀取 `/` 或擁有 Android 系統管理的
`/data` 目錄。SELinux 標籤與檔案加密 metadata 必須維持一致；因 Android 禁止
hard link，新檔案改用不覆寫既有目標的原子 rename。無須 root 或修改 Android
權限；共享儲存空間與任意 app-data 路徑不適用這個例外。

第八個頁籤以每台機器為父列，Space 展開各 SSH profile。已配置連線優先，
組內依最近透過本機 `dev ssh connect` 或 dashboard 發起連線的時間排序；
沒有紀錄者依 alias 字母排列。CONNECTION、ENDPOINT、SOURCES、CHECK、USED
使用一致欄寬；窄畫面隱藏補充欄位，詳情保留完整來源、身分狀態及時間。
排序、篩選與 discovery 重新分組時保留選取的 profile。

`c` 全程留在 dashboard：選 Tailscale，或 LAN 介面、明確 IPv4 範圍及 ports。
大寫 `L` 預設啟動配置中的 lazygit 工具，並非 LAN 快捷鍵。
工具不可用時，錯誤訊息顯示該工具名稱，而非用來啟動它的 shell。
LAN 預設 port 22，維持最多 256 個位址、16 ports、30 秒的限制；畫面顯示
已完成端點與發現數。取消保留已取得的觀測。結束後清單切到「本次發現」，
避免舊篩選藏住結果；回到全部連線時恢復先前篩選。Cache 寫入失敗仍保留
本次資料並提供處理入口。缺少 SSH config、registry、fzf 或選用 provider
不妨礙 LAN discovery。

在尚未配置的候選按 Enter，開啟帶入確切 endpoint 的原生設定表單。確認 alias、
remote user、port 及認證方式；預設只存 SSH config，Fleet 與 Herdr 分別勾選。
預覽包含首次初始化及明確 machine bindings，確認後才交還終端處理 SSH/key
或 Herdr 的原生互動。返回時顯示各階段完成、失敗或 unknown，並選取新增的
profile；後續認證／provider 失敗保留已完成設定。LAN scope 改變須重新探索／審閱。

選項欄位會列出所有選項並以括號標出目前值，例如 `config · existing · [key]`；
←／→ 或 Space 切換。在 **Key** 欄位按 Enter 或 Space 會開啟 picker，列出有檔案的
本機 keys、**+ Generate a new key** 與手動輸入路徑；Esc 回到表單，選取 key 會把
認證方式設為 key。**+ Generate a new key** 會先開啟小表單填寫新 key 路徑與可選
comment，再回到原表單。可用的 Bitwarden、1Password、Secretive agent 與已驗證的
`SSH_AUTH_SOCK` 內的 key 也會列出。Agent-only 選項保留確切 fingerprint 與 socket；
不可用的 provider 則顯示操作指引。Security-key generation 另可設定 type、provider、
resident handle、verify-required 與 application；審閱會說明原生 touch／PIN 流程與
可能保留的硬體作用。自動 Secure Enclave 建立仍不可用。
Vault 選項會先做獨立的 creation 審閱，再回到 SSH setup；之後取消 SSH 表單，
仍會保留 item receipt。Bitwarden desktop handoff 只顯示新可見的 agent key，
不宣稱已觀測到 vault item 建立。

在有 LAN 或 Tailscale 候選的列按 `Ctrl+O`，可選 **set up this discovered target…**，
表單會帶入該觀測及相符 profile 的 user。在已配置的 profile 按 `Ctrl+O`，可選
**set up / install an SSH key…**：有多個 profile 時先選確切 profile，再選 key、
設定 Remote OS 與選用的 Fleet／Herdr，然後審閱。連線設定不可修改；foreign alias
只安裝 public key，managed alias 另會在審閱中顯示記錄為 IdentityFile。

`p` 提供 profile、整台機器或篩選結果的快速網路測試／完整 SSH 驗證；開始前
先確認確切目標。DNS／route、Ping、TCP、SSH banner、handshake、host-key、
authentication、session 分層呈現。Ping 不通仍測 SSH，proxy 不改成繞路直連。
最多同時測四個 profiles，每筆 30 秒，Ping 2 秒，SSH 階段 15 秒；取消保留
已完成結果。Network-only 成功不代表登入成功。

最近使用與測試結果分別保存在 `paths.state_dir/ssh/activity/` 的私有 durable
紀錄；測試不更新 USED。使用時間記錄程序真正啟動，包含失敗嘗試，只涵蓋本機
透過 dev 的連線，不匯入 shell history。測試保留 profile revision、觀测時間及
可取得的 route context；舊結果是歷史觀測，不是即時連通或刪除設定的依據。
清除 discovery cache 不刪活動紀錄。

首畫面先讀本機資料與 cache。此頁啟用時，預設 `[tui.ssh].background_refresh = true`
每五分鐘最多更新一次缺少／過期的 Tailscale 觀測，查詢最多五秒；失敗保留原時間。
可關閉以只讀 cache。`r` 重讀本機狀態，LAN 掃描與 SSH 測試仍須使用者觸發。
即使清單空白，`Ctrl+O` 也可開啟問題與建議動作。
