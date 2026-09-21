---
description: 檢查 repository hooks、掃描 secret 與個人資訊，並以私人恢復紀錄套用已檢視的文字替換。
lang: zh-TW
authority: project
status: evolving
verified_on: 2026-09-21
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
已有的 gitleaks 設定預設保留；`--migrate-hooks` 可精準遷移已辨識規則，
`--migrate-rules` 則提議整份替換，兩者都須先比對自訂規則。`dev repo setup --enable agent-history-hygiene` 共用此服務。

Hook 只檢查 index，不改檔、不自動 stage。缺少支援 hygiene 的 dev／gitleaks、
scanner 失敗或輸出損壞都會阻擋。

Setup 將設定檔寫入 project，先尊重 Git 生效的 `core.hooksPath`，沿用可辨識的
既有 hook（包含全域 hook），不安裝或改寫全域 hook。只有未設定 hooksPath、
且 common Git directory 沒有 pre-commit hook 時才安裝 repository hook。
已設定的 hook 缺少或無法辨識時需人工整合，不會自動用本地設定蓋過。
目前沒有 `--global` setup 選項。

## 批次設定與分階段遷移

一般 commit 的呼叫鏈是 `Git -> pre-commit -> dev hygiene scan -> gitleaks +
隱私政策檢查`。Setup 是獨立設定步驟；commit 不會再呼叫 setup，也不需要 agent。

```bash
dev hygiene manage --all
dev hygiene manage /path/to/a /path/to/b --json
dev hygiene setup --migrate-hooks --json
```

REPOS → Ctrl+O → hygiene 可查看設定狀態、最新既存掃描報告、明確選擇
worktree／staged／本機 history 掃描，以及設定目前 checkout 或篩選後的 repo。
worktree 子項使用精確 checkout；報告不會改讀其他 checkout，也不會自動重掃。
掃描沿用目前政策與 20 分鐘上限，不 fetch refs 或擷取原始值。操作暫停 dashboard
並進入共用 CLI 流程，按 Enter 返回；setup 先預覽，再確認套用。

設定流程與 CLI 共用，repo 不必有 skills lock。預覽列出
設定差異、保留的依賴與阻擋，再勾選要套用的 plans。共享 Git repository 只處理
一次；新裝 common-directory hook 前，其他 worktree 必須有可讀設定，套用時
再次確認。自訂／未知 scanner 命令保留並要求人工檢視。

遷移只替換已辨識且功能等價的 `gitleaks-system` hook；artifact 專用檢查、
finalizer、provenance 和其他 hooks 保留。只精準更新已知舊 password expression，
不覆蓋其他自訂規則或註解；自訂 scope／expression 保留。`--migrate-rules` 仍是
另外選擇的整份設定替換。規則更新後需重新掃描；detector ID 改變時需重新確認
fixture 例外。

Setup 與 scan 要求相容的 gitleaks 8.x，最低 8.30.0；CI 固定 8.30.1。被動 status
只檢查工具存在，不執行 scanner。Hook 的 PATH 必須找到支援 hygiene 的 dev；
執行 `./dev` 不會升級 PATH 上的舊版 dev。工具不會自動安裝。

每個 repo 分別記錄 completed、blocked、skipped、stale、partial 或 unverified，
結果保存在私人 receipt；完成的 setup 重跑不改檔。中斷不會回滾其他 repo，也不能
盲目重試。JSON 只預覽，可用各子 plan 的 ID 及既有
`hygiene setup --repo PATH --apply --plan ID --yes` 套用。

Staged checker 本身不改檔，但 pre-commit 可能暫存未 stage 的變更，所以 recorder
退出後才對其 checkout commit。目前不完整取代 agent-history-hygiene：
`dev artifact finalize` 的既有 tracked-history handoff 仍依賴其 scripts；外部 archive
使用 native snapshot 檢查。只要仍有 tracked workflow，這些安裝和 wiring 就保留。

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

`rules import --from machines` 只讀已有的 Tailscale／LAN／Fleet inventory cache，
將 IP、hostname、remote username 列為可勾選的私人候選；不讀 credential store、
keys，不執行探索或連線。保留觀測時間及 stale 標記；缺少快取不會建立新資料，
不完整／損壞會阻擋匯入。預覽後來源內容改變，舊 import plan 即失效。

Source 中加引號的 password literal 仍會被偵測；未加引號的 assignment 限於設定、
script 與文字格式，以避免 Go 布林欄位／變數運算誤判。格式完整的示範／測試 key
仍先阻擋，經確認後才建立精確例外；不要略過整個 tests／docs 或同一行其他 key。
一般示範用 inert placeholder，detector 測試可於執行時組出 fixture。


預設 generic password／API-key detector 也會核對 Go 原始碼位置。只有整份檔案
可解析，且所有命中位置都位於 literal 與 comment 之外，才排除該候選。核對的是
掃描當下的 index 或歷史 Git object，並同樣適用於 artifact snapshot；不拿目前
工作檔代替舊版本。原始碼無法讀取／解析、位置不明、自訂 detector 或外部規則
include 均保留原命中。加引號的環境變數名稱不會自動放行，需使用已檢視的精確例外。

內建 generic privacy 略過 loopback／未指定 IP（含 mapped IPv4），以及結尾為
`.test`、`.example`、`.invalid` 的 email 網域與 `example.com`、`example.net`、
`example.org` 及其子網域。私人／LAN IP、一般 email 網域與個人 home path 仍會
偵測；明確設定的私人規則也仍可命中上述省略值。這是一般與 audit 掃描共用的
偵測語意；`--audit` 仍負責忽略 finding 例外，不切換 detector 行為。

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

History 掃描檔案內容，不包含 commit message 或 author／committer 身分資訊；
公開 repo 前需另行檢視這些 metadata。

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
Finding 可另含 `value_id`：在同一規則內以私人 key 計算的命中值摘要，相同值可跨檔
彙總而不暴露原值。新增的 `file_id` 以私人 key 識別確切路徑，即使不同檔案的
顯示路徑遮罩後相同，也不會混為同一檔。

CI 僅使用公開 repo 規則，不具本機 SSH 字典。PR／push 掃變更的 commit range，
人工 workflow dispatch 掃本機已取得的全部歷史。

## 掃描摘要

```bash
dev hygiene report                                 # 此 checkout 最新保存的掃描
dev hygiene report --scope staged --by rule --top 5 --findings
dev hygiene report --report <report-id> --disposition block,warn --path 'docs/**'
dev hygiene report --rescan --scope history --range <full-from-oid>..<full-to-oid>
dev hygiene report --rule privacy-email --values  # 遮罩值；隱含 --rescan
dev hygiene report --json                          # hygiene_summary schema 1
```

`report` 彙總單次掃描，不逐筆列出所有 finding。預設讀取此 checkout 最新保存的
掃描、不重新掃描，因此 commit 被 pre-commit hook 擋下後可立即查看該次 staged
掃描。最新指標以 checkout 為單位：linked worktrees 共用 hygiene 狀態，但預設查詢不會
選到其他 checkout 的掃描；snapshot 報告不會成為最新。`--scope` 選該範圍的最新掃描，
`--report ID` 指定已保存的報告，`--rescan` 先重新掃描；`--range`、`--file`、
`--timeout`、`--audit` 必須搭配 `--rescan`。掃描後政策已變更，或報告來自其他
checkout 時會顯示警告。已保存的報告是歷史觀測：`checkout_current` 只比對
checkout 根目錄，不驗證目前的來源 bytes。已保存摘要會顯示掃描日期，並提示
「source bytes were not rechecked」（未重新檢查來源 bytes）。需要新觀測時用
`--rescan`；不能與 `--report` 同時使用。

`--by` 選擇 `severity`、`rule`、`file`、`category` 分組（預設
`severity,rule,file`）。`--top N` 限制每組列數（預設 10，`0` 顯示全部）並計算
省略數。`--disposition`、`--rule`、`--category` 與可重複的 `--path <glob>` 在
彙總前篩選 finding；`--findings` 逐筆列出。排序依 block、warn、accepted，再依
occurrences 由多到少、finding 數由多到少、名稱升冪排列。彙總已保存的報告一律以 0 結束；重新掃描若有
coverage gap，會先輸出摘要再以失敗狀態退出。

`--json` 輸出 `kind: "hygiene_summary"`、`schema_version: 1`：`report_id`、
`report_kind`、`scope`、`status`、`created`、`policy_current`、
`checkout_current`、`audit`、`public_only`、`rescanned`、`sections`、`top`、
`filters`、`totals`、`file_counts_complete`、選定的
`severities`／`rules`／`files`／`categories`（file 列含 `file_id`）、可選的
`findings`、`gaps`、`skipped`、`omitted` 計數與 `values_shown`。只有每筆 finding
都有 `value_id` 時，rule 才回報 `distinct_values`。`--values` 另加入遮罩後的
`values` 與 `omitted_values`；`values_truncated` 表示擷取已達上限。
`values_status` 為 `complete`、`truncated` 或 `failed`（未擷取或舊資料為空／省略）。
舊 finding 沒有 file ID 時，`file_counts_complete` 為 false；檔案數只是遮罩路徑
分組的下限。私人路徑 metadata 讓 `--path` 可比對原始檔名，而回報的 filter 值
仍依政策遮罩；無效 path glob 會被拒絕。Agent 應優先
使用 `dev hygiene report --json`，不要解析 `scan` 輸出或表格。

`--values` 顯示各規則遮罩後的相異值；除非 `--report` 指定的掃描已擷取值，否則
隱含 `--rescan`。Secret 保留前後各兩個字元與長度（少於 12 字元只顯示長度）；
私人規則顯示 `[private:N]`，email 為 `a•••@d•••.tld`，IPv4 為 `a.b.•.•`，IPv6
為 `first:•••`，home path 為 `Users/x•••`。原始值與所在行內容不會進入 stdout、JSON
或掃描紀錄，只寫入權限為 0600 的私人 `<report-id>.values.review.txt`；
`dev hygiene review-path <report-id>` 只印出其位置。勿將該檔貼到聊天、Git 或
CI logs。最多擷取 10,000 個值、每值 20 筆樣本；單一原始值上限 64 KiB，
原始值／所在行內容／位置／規則 metadata 合計上限 16 MiB，產生的檢閱檔上限
64 MiB。過大的值整個略過，不截取片段；達上限會標記 `values_truncated`。
值的附屬檔寫入失敗時，仍保存 partial 報告並加入 `values_capture_failed` gap，
不會顯示私人 review-path 提示。

## 修復無效 UTF-8

Warning 本身不會阻擋 commit。`unsupported_text_encoding` 等 coverage gap
代表掃描不完整，即使 blocking findings 為零仍會失敗。編碼 gap 保留原 code，
新增 `encoding` 物件：`reason`、從零起算的 `byte_offset`、從一起算的 `line`、
`invalid_bytes`、`invalid_sequences`（連續壞 bytes 段數）及可選的 `nul_bytes`。
歷史 gap 也附上觀察到的 `commit`。診斷不輸出原文；修復工作檔不會改寫舊 Git objects。

```bash
dev hygiene repair-encoding --file .specstory/history/session.md --json
# 明確選擇刪除；預設則替換成 �：
dev hygiene repair-encoding --file notes.txt --invalid remove --json
# 檢閱私人提案，且對應 artifact writer 已退出後：
dev hygiene repair-encoding --apply --plan <id> --yes --writer-stopped
# 審閱並只 stage 所需修補，再檢查實際 index：
dev hygiene scan --scope staged --json
```

預設 `--invalid replace` 將每段連續無效 bytes 替換成一個 `�`；`remove` 刪除該段。
有效 bytes、既有 `�`、UTF-8 BOM 與換行保持原樣。`--file` 可重複指定；每份
輸入與輸出上限 128 MiB，每份 plan 最多 256 檔、192 MiB 提案輸出。合法文字不需改動。
已知二進位副檔名、NUL、UTF-16/32 BOM 需另行檢視，不猜測舊編碼或原始字元。

修復使用簽署 plan、即時檔案身分檢查及私人原始 bytes 備份，可經
`dev hygiene restore` 還原，不依賴 secret scanner。Artifact 修改仍要求 writer
已停止並通過下方的 writer guard，涵蓋大小寫變體與巢狀 artifact 目錄。編碼修復
拒絕尾端句點／空白與 DOS 短檔名形式，必須使用標準長路徑；hook 不會自動修復。Apply 只修改工作檔、完整保留
index，因此部分 staging 的檔案在重新審閱並 stage 修補前，staged scan 仍可能失敗。
不要順便 stage 新增的整份聊天。編碼修復不等於 secret redaction 或 hygiene 掃描通過。

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
ledger，不能盲目重跑；apply 與恢復錯誤會保留底層原因。macOS kernel 會為每個新檔
加上寫入程序的 `com.apple.provenance`，並忽略複製該值的要求，因此替換後的檔案
帶有 writer 的標記而非來源的；其他 extended attributes 仍必須完整保留，來源檔案
的標記若改變仍視為過期。Index、Git 歷史與安裝的 binary 均不由這個流程修改。
Windows 保護僅適用明確 opt-in 的文字交易，其他 configedit 功能維持原平台契約。Windows 保留 owner／group／DACL 語意；
alternate streams、明確 integrity label 或特殊 attributes 需人工保存。

Restore 拒絕操作後又被修改／替換的檔案；artifact 恢復也需要 `--writer-stopped`，
`restore --apply` 會對 receipt 記錄的確切路徑執行 writer guard。停用或不可用的
runtime 無法證明程序不存在。Post-writer 聲明與 source revalidation 仍不可省略，
raw 外部 writer 不受 dev 鎖保護。

## Artifact writer guard

Redact、encoding repair、restore、批次 `manage` 與 `dev artifact`
finalize／archive／migrate 共用同一個 live writer 檢查，針對審閱的確切目標檔案；
archive 仍不修改來源 bytes。
任何涵蓋此 checkout 的其他已辨識 agent，無論狀態為何都會阻擋 artifact 修改。
呼叫者自己的 agent pane 只有在下列情況可豁免：

- Herdr 回報其確切 agent session ID（`agent_session` kind 為 `id`，不是 title），
  且每個 artifact 目標都是 `.specstory/history/*.md`
  transcript，實際由 SpecStory 產生的固定 preamble 中有合法 UUID，證明屬於
  不同 session；或
- 呼叫者確認 writer 已退出後，傳入全域 `--allow-shared-checkout`，聲明檔案歸屬
  互不重疊（例如 plans，或無法以 preamble 證明歸屬的 transcript）。

已識別為呼叫者自己的 live transcript，即使加上 override 也拒絕。若呼叫者
身分未知，仍須明確聲明檔案歸屬互不重疊。
`--writer-stopped` 仍用來聲明確切 recorder 已退出；兩個 flag 都不會略過來源重新驗證。

此 repo 的最後清理步驟見[人工 dogfood 檢查表](../reference/hygiene-dogfood.zh-TW.md)。
