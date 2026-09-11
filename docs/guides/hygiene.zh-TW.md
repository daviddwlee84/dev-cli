---
description: 檢查 repository hooks、掃描 secret 與個人資訊，並以私人恢復紀錄套用已檢視的文字替換。
lang: zh-TW
authority: project
status: evolving
verified_on: 2026-09-11
---

# Repository hygiene

`dev hygiene` 將可選的 gitleaks、pre-commit、repo 政策、本機私密規則與安全文字
交易整合。Scanner 命中是候選，並不直接代表已確認的 credential 洩漏。

## 檢查與設定

先安裝 `gitleaks`、`pre-commit`。Setup 會回報缺少的工具，不暗中安裝套件或覆寫
全域 hook。

```bash
dev hygiene status
dev hygiene status --check-remote
dev hygiene setup --json
dev hygiene setup --apply --plan <id> --yes
```

Status 讀取有效的 `core.hooksPath`、hook 與設定；辨識到 hook 只證明設定存在，
不保證過去每次 commit 都執行過。Raw Git 或外部工具仍可繞過 hooks。

Setup 加入阻擋式 `dev-hygiene` hook、安全 gitleaks 規則與 `.dev-cli/hygiene.toml`，
保留其他 hooks 及 YAML 註解。同名但命令不同的 hook、未知的 hook 鏈需人工整合。
已有的 gitleaks 設定預設保留；只有在 setup 預覽加入 `--migrate-rules` 才會提議
替換，須先比對自訂規則。`dev repo setup --enable agent-history-hygiene` 共用此服務。

Hook 只檢查 index，不改檔、不自動 stage。缺少支援 hygiene 的 dev／gitleaks、
scanner 失敗或輸出損壞都會阻擋。

## 政策與私密規則

優先序：內建預設 → `$XDG_CONFIG_HOME/dev/hygiene.toml` → repo 政策 → 本機 repo
覆寫 → 明確 scan/status flags。建議值是 secret 阻擋、已確認的私密值阻擋、通用
隱私候選警告；各類皆可設為 `block`、`warn` 或 `off`。規則可調整已啟用類別的
處置，但類別 `off` 會停用該類所有規則。

```toml
version = 1
secrets = "block"
known = "block"
generic = "warn"
```

三類全部設為 `off` 會回傳明確的 `skipped`，不讀 repo 檔案內容、不執行 scanner；
這是停用，不是 clean scan。

Private 可見性只提供建議，不會自動弱化政策；未知也不當成 private。GitHub
查詢僅在 `status --check-remote` 發生，`--remote` 預設選 origin。

```bash
dev hygiene rules policy --known warn --generic off --json
dev hygiene rules apply --plan <id> --yes
dev hygiene rules import --from ssh --json
dev hygiene rules import --from ssh --select <candidate-id> --json
dev hygiene rules import --from local --json
dev hygiene rules add --id private-network --kind cidr --value-file network.txt \
  --replacement 192.0.2.1 --json
```

先明確選候選才匯入。SSH 只讀取 bounded static Include closure 中的 literal
alias、HostName、User 與 key-path reference，不執行 `ssh -G`、`Match exec`、DNS
或連線，也不讀 key material。Include 不完整會阻擋匯入。常見公開名稱與通用帳號
建議警告；local 來源提供 home path、帳號、Git 姓名與 email。

支援 literal、CIDR、Go RE2 及相對路徑 glob；`directory/**` 選取其下內容。
私密值用 `--value-file` 或 stdin 輸入，不放在命令參數。可用同 ID 的規則 plan
調整／停用規則，或編輯本機私人政策。Scan override 不會持久化；建立 redact plan
前需以相同的持久政策重新掃描。

本機規則、HMAC key、報告、plan 與恢復資料放在 Git 外的
`paths.state_dir/hygiene/repos/<native-repo-id>/`。Linked worktrees 共用本機政策，
但 plan 綁定特定 checkout。Unix 使用私人權限與 ownership；Windows 使用受保護的
目前 user／SYSTEM ACL。不會自動刪除或匯出。

```bash
dev hygiene rules allow --report <report-id> --finding <finding-id> \
  --reason 'Reviewed synthetic test fixture' --json
```

本機例外綁定精確 value／rule／path。可分享的 TOML 例外需指定 `rule`、`path`、
有頭尾錨點的 `pattern` 與原因。`.gitleaksignore` 是 fingerprint 清單，不是 path
移除規則。`--audit` 忽略本機 finding 例外、該檔及 inline `gitleaks:allow`；scanner
設定中的 allowlist 仍有效，必須另行檢視。Placeholder 不會自動放行同一行的其他 key。

## 掃描範圍

```bash
dev hygiene scan --scope worktree --json
dev hygiene scan --scope staged --json
dev hygiene scan --scope history --audit --timeout 40m --json
dev hygiene --public-only --known off --generic off scan --scope history \
  --range <full-from-oid>..<full-to-oid> --json
```

Worktree 掃已追蹤檔與未被 ignore 的未追蹤檔；後來加入 `.gitignore` 的已追蹤檔仍
會掃。Staged 使用真正的 index bytes，包含 hidden files 與 partial staging。
`--file` 只能選該範圍內的檔案。

明確的 range 只檢查選定 commits 所變更的檔案版本，未變更的舊檔留給全量稽核。
History 固定本機 refs 或明確的完整 OID range，涵蓋 merge-only、已刪除與後來
被 ignore 的內容，不暗中 fetch。不掃 reflog、unreachable objects、其他 checkout
未提交內容或遠端 submodule repo。非一般檔與已知 binary 格式列為文字掃描的排除項；
不支援的文字編碼、讀取失敗、超過每檔 128 MiB、來源／ref 變動、shallow history、
取消或 scanner 失敗則回報 incomplete，不能當成 clean。

JSON schema 1 包含狀態、範圍、固定 refs、數量、遮罩後的位置、處置、排除項與缺口，
不含原始 snippet／credential。Finding ID 與公開檔案摘要使用私人 HMAC。
`complete` 只描述宣告的文字範圍；阻擋命中或不完整皆以失敗狀態退出。不代表驗證過
credential 有效性。未被選取規則涵蓋的檔名仍可能辨識個人，分享前須檢視。

CI 僅使用公開 repo 規則，不具本機 SSH 字典。PR／push 掃變更的 commit range，
人工 workflow dispatch 掃本機已取得的全部歷史。

## 預覽、套用與恢復

```bash
dev hygiene redact --report <report-id> --file notes.md --json
dev hygiene redact --report <report-id> --finding <finding-id> --json
dev hygiene review-path <id>  # 私下開啟提案，勿貼回聊天
dev hygiene redact --apply --plan <id> --yes
# 只有在確切 artifact writer 已退出後：
dev hygiene redact --apply --plan <id> --yes --writer-stopped
dev hygiene restore --receipt <receipt-id> --json
dev hygiene restore --receipt <receipt-id> --apply --yes
```

所有支援的一般文字檔皆可選。Plan 綁定 checkout、HEAD／branch、政策、scanner
輸入、native identity 與內容，並附私人簽章。來源或 plan 變動需重做預覽。
重疊命中依 secret → known → generic 優先，同類優先較長 span；預覽只顯示位置、
opaque 摘要與替換數，不重新輸出原 secret。`review-path <id>` 可取得私人完整提案
的位置，完整性也綁定 plan；勿將其內容貼回聊天、Git 或 CI logs。

Apply 先驗證整批，再逐檔交易，保留 metadata 與私人恢復紀錄。中斷保留 partial
ledger，不能盲目重跑。Index、Git 歷史與安裝的 binary 均不由這個流程修改。
Windows 保護僅適用明確 opt-in 的文字交易，其他 configedit 功能維持原平台契約。

Restore 拒絕操作後又被修改／替換的檔案；artifact 恢復也需要 `--writer-stopped`。
Runtime 觀察會阻擋可辨識 writer，但停用或不可用的 runtime 無法證明程序不存在。
Post-writer 聲明與 source revalidation 仍不可省略，raw 外部 writer 不受 dev 鎖保護。

此 repo 的最後清理步驟見[人工 dogfood 檢查表](../reference/hygiene-dogfood.zh-TW.md)。
