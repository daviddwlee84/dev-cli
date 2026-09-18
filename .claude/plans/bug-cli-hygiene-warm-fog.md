# Amend blocker、受保護的 co-commit 收尾與 dashboard UX

## Context

v0.2.39 已發布，dev-cli 本機 branch/worktree/task 清理已完成；本計畫取代舊 release 計畫。使用者要求：查明 main 上指定 transcript amend 到 Backup chat history 的阻擋原因、修正前次收尾發現的工具缺口，以及 REPOS `n` 和 `/` filter 的兩個 UX。

### 已證實的 amend 診斷

- `HEAD == main == cached origin/main == 4dc2433`，subject 為 `Backup chat history`；未查詢遠端。
- 唯一 staged 檔為 `.specstory/history/2026-09-17_04-44-06Z-git-commit-amend-no.md`，狀態 `AM`；其 staged 與 working bytes 不同，而且本 session recorder 仍在執行。
- 既有 report `720c76f6-e90b-4063-99a0-0a30d75180be` 與 staged bytes 完全匹配：7,272,809 bytes、UTF-8 正常、coverage complete、0 gaps。
- **47 個 blocking findings／136 次出現**：41 個 Sourcegraph matches 已證實為本機 Git commit objects（28 reachable from cached origin/main、1 reachable from other refs、12 locally present but unreachable）；1 個 generic-api-key 是已存在精確例外的非憑證專案名稱；其餘 **5 個值尚未證明來源**，繼續阻擋。
- 未定 findings 的 staged line：85635、120027、126725、142626、142682。另有 98 個 privacy warning，不是阻擋原因。
- 有效 hook chain：global pre-commit → `.pre-commit-config.yaml` → `dev hygiene scan --scope staged`。匹配的掃描結果足以說明這份 index 為何不過 gate；沒有找到可證明該 report 正是 amend hook 產生的最新 log。
- 不是 encoding／覆蓋不足 bug。既有 exact exceptions 只覆蓋其他 transcript paths，沒有自動套用是正確行為。Live writer 與改寫已存在於 cached origin/main 的 commit 是另外的安全／歷史問題，不冒充 hook 的失敗原因。

## 範圍與資料保留

- main 僅做上述診斷；**不在 live recorder 執行中 stage／redact／amend 該 transcript，不重寫既有已發布 commit**。日後備份建議新 commit；改写舊 commit 需另外明確授權。
- 實作使用兩個獨立的外部 worktree／修正 branch，以各 repo 核對過的明確 base 建立：dev-cli 從當前 main；agent-skills 從已核對的 main（目前 `82bb367`）。不切換／重設原 checkout，不攜帶原 index。
- 保留 agent-skills 原 `fix/nautilus-trader-eval` 的六個 staged 檔、短 alias、prepared journal、草稿及私有備份。不直接在該 prepared checkout 編輯 helper。
- 修改 canonical `agent-skills/skills/local/agent-history-hygiene/`，不修改 dev-cli 的 installed skill copy 冒充上游修正。
- 本計畫不含 push、PR、release、安裝覆蓋、再啟動／關閉 agent 或自動恢復舊 commit。所有 raw findings／before-images／review 都留在 private state；不放進 Git、公開 JSON 或測試 log。

## 1. 目前 hygiene findings 的窄化處理

重用 `internal/hygiene/rules.go` 的 `PreviewAllow`／`PreviewRules` 和既有 guarded Apply，而不是關閉 Sourcegraph rule 或新增 blanket hex/transcript exemption。

- 私下核對五個未定值的明確來源；只使用其引用的 repo／記錄，不進行廣泛檔案系統搜索，也不把「40 位 hex」當作安全證明。
- 對已審閱 noncredentials，以 report finding ID 或 exact rule＋path＋value＋reason 產生**單一 local policy preview**；未知值不納入。預定 exceptions 留在既有 private repo policy，不必把原始值提交成產品設定。本次 codefix 不自動套用這份 preview。
- 後續經審閱並明確授權 Apply 時，重新核對 report 與當前 index／policy；變更後另掃描 exact staged snapshot 驗證結果，不執行 commit hook 的 stash/restore，也不 amend。
- 若五個值仍未定，明確回報剩餘 blocker，不稱整份 transcript 已通過。這是個別 findings 處置，不改 detector 的預設安全語意。

## 2. TUI：REPOS `n` 不被無關 selected-row refresh 擋住

檔案：`internal/tui/list_actions.go`、`model.go` 與現有 TUI tests。

根因：`runListAction` 對 `RepoRow.Pending` 的 guard 包含不依賴任何 row 的 `listActionRepoCreate`；`Repos.Create()` 本來就是啟動 `repo new --handoff stay`。

- 只讓 `listActionRepoCreate` 不受 selected-row Pending gate 影響，保留 callback availability／error handling。
- Ctrl+O 同樣把 Create 視為 row-independent，並在空／無選取的 REPOS menu 顯示；原 row 消失不阻擋這個 action。
- 保留 active-clone 限制、其他 row-dependent action 的 freshness／selection guards，以及 REMOTE clone 的 inventory 重驗。
- 沿用 `internal/cli/tui.go` 的既有 callback；真正建立仍經 `repo_create_wizard.go`、`repo_create.go`、`repo.Acquire` 的確認與即時 destination／nested-repository／exclusive-create 檢查。不是讓 stale inventory 變成 mutation authority。

## 3. TUI：filter 中上下鍵選取，Enter 語意不變

檔案：`internal/tui/model.go:updateFilter`、`view.go:renderDetail`、`help_browser.go`、`help_content.go`。

- Dashboard 八個 views 的 live filter，先攔截 Up/Down，以既有 `at()`／`count()`／`setAt()` 移動目前可見結果；保留 mode、focus、query 與文字 cursor。
- 只有 input **值真的變更**才重新套用 filter 並選第一個結果；Left/Right/Home/End／無效刪除等不再把選取歸零。
- `j/k`、字母、数字仍是搜尋文字；不把 filter keys 整批送入 `updateList`。
- Enter 保留 query／選取並退出輸入，**不立即開啟 item**；之後正常 list Enter 才執行。Esc 維持清除 filter 的既有行為。
- Pending row 的 passive detail 不蓋住 active filter input/help；只調整這個 rendering priority。
- Help 的 live 索引／結果 filter 同步支援 arrows，重用 `helpMove`；文章內搜尋／scroll、Notes 的 Enter-submit search、SSH dialogs、forms、已支援 arrows 的 Ctrl+O 不改成同一套行為。
- 沿用 per-view cursor、selection token、repo progress identity restoration；不把 flowtui／triagetui 併入 dashboard。

## 4. 原生 artifact 的 exact selection、writer guard 與 stale protection

主要檔案：`internal/cli/artifact.go`、`internal/artifact/{service,intent,store,transcript}.go`、`internal/agenthistory/{handoff,archive}.go`。

- 新增 additive `dev prepare --specstory-path <exact-path>`；必須與 `--session provider:uuid`、checkout／capture root 一致。重用 `ReadTranscriptSession` 的 bounded regular-file preamble proof，拒絕 symlink／escape／body UUID。
- 新 intent 保存所選 path，finalize 重驗它，不重新按「最新」或任意同 UUID alias 選擇。未提供 path 的 ambiguity refusal、舊 intent 的相容處理保留。
- Archive 的 `LocateSession`／prepared archive plan matching 同步綁定 selected path；不能以同 UUID 另一個檔取代。
- 修正 tracked `finalizeLocked` 未使用 supplied `Guard`：在 artifact staging／任何來源變更前，以及長時間處理後、commit 前執行相應 guard；explicit writer-stopped 不能壓過已觀察到的 live writer。
- 參考 `task.Store.GetRecord/Update` 的 exact-record revisions，為 artifact Store 增加 compare-and-update transitions；Prepare／Observe／Discard／Finalize 的 stale authority 不得覆蓋較新的狀態。
- 既有 product-first 的 empty-index／committed-product 規則與 archive lane 保留；不讓 staged products 偷渡到 legacy directory-level `--fix`／restage finalizer。

## 5. Canonical helper：完整證據、fixture review 與 exact sync

主要檔案（agent-skills repo）：
`skills/local/agent-history-hygiene/scripts/{_post_session.py,run-specstory-session.sh,queue-agent-commit.sh,finalize-agent-commit.sh,stage-agent-artifacts.sh}`、`assets/redact_secrets.py`。

### 5.1 新 run 的 versioned evidence

- 新 run 使用明確 v2 protocol／journal，提供 side-effect-free capabilities；v1 的嚴格 parser、existing state 與恢復語意保留，不默默加欄位或升級。
- 重用目前 alternate-index sanitation／materialization／frozen-commit transaction，在改動前持久化 private sanitation receipt：完整 post-sync before-images、pre/post blob IDs、每個 finding/occurrence 與完整 transformation、path/native identity、request/session/root/ref/parent/index/prepared tree、scanner/config identity、coverage 與 revision。
- 證據寫入失败／不完整時不得發布 sanitized index／來源或宣稱 clean。Raw bytes 與 full values 不進公開輸出。

### 5.2 真實的 run-scoped finding review

- Finalizer 增加只讀 `--preview-review --json`：輸出 masked findings、receipt revision、可審閱狀態與 private review 位置，不 commit。
- `--review-file <private versioned decisions>` 對 exact finding IDs 填寫 reason／disposition，與 receipt、prepared state 綁定；仍須新的明確 `--allow-commit` 才可提交。
- 區分 `reviewed_noncredential`、`credential_rotation_required`、`unresolved`；sanitation 本身不證明真實 credential exposure。全部 occurrence 必須有完整證據，未知／partial／stale／mixed unresolved 繼續阻擋。
- 已審閱 fixture 只解除**本次已 sanitized snapshot**不適用的 rotation gate；不還原 raw bytes、不豁免未來掃描、不略過 hooks，也不假裝 `--rotation-confirmed`。
- Apply 在既有 finalizer lock 內重驗 exact receipt/review/request、來源／policy、HEAD/ref/index/tree；既有 uncertain commit 只 reconcile，絕不第二次 commit。
- 與 native `PreviewAllow` 的持續性 policy exceptions 保持區別，不再造一套 scanner。
- 提交前重用既有 staged checker，確認整個 frozen prepared index 的適用檔案都有 policy/scanner coverage；sanitation 只改 exact selected artifacts，product findings 只阻擋、不順便重寫。Global hook 因缺少 repo config 而跳過不能當作保護已生效；本輪不自動安裝／修改既有 hook chain。

### 5.3 Sync freshness 與禁止 cloud publication

- Run 的 no-cloud 選擇明確傳至真正 post-exit sync／export；使用原生支援 flags，不透過臨時改寫使用者 config。v2 的自動交接預設 no-cloud；不默默啟用 cloud。
- 保留真實 child exit／process-group quiescence proof；UUID、mtime 或「bytes 有變」都不能代替 freshness。
- 在同一明確 UUID／root、已停止 native source 下，以原生 exact export 證明所選檔內容：正常 sync 後，使用支援的 `sync --print`（加 silent/no-cloud/no-stats 等受支援選項）取得 bounded private candidate，核對 native source identity 與所選 alias 的完整 rendered digest。這是一次實際 sync 加只讀 export verification，不是重试 sync。
- 凍結／核對 native source generation，防止 sync 與 export 之間改變；候選必須通過 exact preamble／root/session proof。若原生版本無法提供已驗證的 export，明確 `freshness_unproven`，不退回 mtime／nonce 字串猜測。
- 若 sync 更新另一個 alias 而 selected path stale，就停下保留資料；不自動改 selector、不刪其他 alias、不手造 recorder 證據。
- 原生 flag／output 真實行為需以隔離 fixture integration test 核實；不把 fake-provider unit test 當成實際版本相容性證明。

## 6. 薄型 dev co-commit adapter：不建立第二個 commit engine

在上述 guard/revision 與 canonical v2 完成後加入；per-invocation opt-in，**不新增全域預設政策**。

建議介面：

```text
dev prepare [task-or-worktree] --closeout co-commit \
  --session provider:uuid --specstory-path <exact-path> \
  (--plan <one-path> | --no-plan) --message-file <base-message> [--json]
```

- `--closeout` 預設 `product-first`，舊命令不變；co-commit 與 archive policy 不混用。
- 明確解析已安裝 canonical helper、核對 v2 capability／tool/source identity；缺少或不支援就回報可操作的 blocker，不下載／安裝／执行未知 helper。
- 只在真實 wrapper context 下 queue。無 authentic run 时回報 `requires_wrapper` 及正確啟動方式，不猜 run ID、不生成 parent token／journal、不自动關閉或重啟 agent。
- Helper 繼續獨占 staging／sanitation／commit／reconciliation。Native 只記錄 expected-revision binding，包含 helper request/revision、repo/root/ref/HEAD／staged product tree、exact selectors；不呼叫 legacy scanner/restage。
- Native status/readiness 讀取并核對 helper receipt；不是看 exit 0、trailer 或 draft 就當成功。Finalize 的 co-commit path 僅委派 canonical backend，帶入真正的外部 approval／writer guard；不得落入 product-first executor。
- Queue 前要求 feature-only index；已 staged artifact 不會被偷偷移出。相同 request 可重入查證，不同 request 衝突。
- 明確處理 partial outcomes：helper 已 queued 但 native binding 未寫入，回傳 `queued_but_not_bound`，保留 request並停止子 agent 的 repo 操作；只允許 idempotent bind repair，不另 queue。Helper 已 commit 而 native persistence 失敗，只驗證 exact commit並補 receipt，不重 commit。
- 定義 repo/run/native-store lock ordering，不持有 native store lock 跨 helper／hooks；返回時以 expected revision CAS，stale 即拒絕新作用。
- 新 JSON 輸出使用獨立 versioned schema，既有 documented JSON／human defaults 不改名移除。

## 7. 舊 agent-skills prepared run：本輪只做恢復預覽，不自動提交

保留 run `d13e3cc2-1bf4-48ad-b488-6169f8ec8759`、parent `82bb367`、tree `e7b9a23a2244d0a6df04668ab7373a7b396ea98e`。

- 它只有 v1 boolean sanitation outcome，不能直接套用新的 fixture approval。
- 實作／測試完成後，最多建立隔離的 legacy recovery preview：先證明完整 post-sync 原文（必要時由 private backup＋prepared bytes 重建，必須吻合先前記錄的完整 SHA256），再確定重放原 scanner/policy 可完整解釋每一處 original→prepared 轉換。
- 審閱結果必須涵蓋全部 finding／occurrence，綁定原 request/journal 的 exact revision、selected paths、parent/tree/policy，並區分舊的真實 lifecycle proof 與新建的 review proof。
- 使用明確 versioned import/recovery receipt；不修改舊 journal 來「補出」過去未有的證据。不足就保留 blocked。
- 真正消費恢復計畫／commit、或改用新 wrapper session，需看到 preview 後的另一個明確授權；本次 codefix approval 不代表已核准未知恢復結果。

## 驗證與文件

### Focused regressions

- TUI：擴充 `TestRepoNLaunchesNewRepositoryWizardWithoutASelectedRow`、repo progress／selection tests、filter／Notes tests；涵蓋 cached/loading/runtime-pending、empty/filtered-empty、menu row 消失、callback失敗、active clone、其他 stale-row mutation仍阻擋。
- Filter：八 views＋Help索引，多筆／零結果／邊界、arrows不送出任何action/network、j/k是文字、cursor-only keys不reset、Enter/Esc、tree child與async focus preservation；Notes在Enter前不query。
- Native artifact：alias重複／錯誤provider/path／symlink、archive selector替換、Guard在stage前拒絕、source變化、Discard/Observe/Finalize races、v1 compatibility。
- Helper／adapter：完整before-image與多occurrences、fixture/credential/unresolved混合、review tamper/stale/replay／policy drift、缺失舊證據、sync0但selected alias stale、source generation改變、no-cloud對run/sync/export一致、unsupported native版本、partial bind/persistence、uncertain commit no retry。
- 全部 destructive/finalization tests 使用 synthetic isolated repos；不拿目前 live transcript 或 prepared index 當 fixture。

### Repository gates

- dev-cli：focused `internal/tui`, `internal/cli`, `internal/artifact`, `internal/agenthistory`, `internal/hygiene`, store tests → `go test -race -timeout 20m ./...`、vet、format check、E2E、isolated hygiene hook test。
- Canonical helper：既有 `test_post_session_finalize.sh`／stage/redactor/metadata/contracts 與 `make test-skill`、skill lint、`make validate`。Native platform不能測到的能力明確標記未驗證，不推定 Windows支援。
- dev-cli `[Unreleased]`、README／embedded help／authored skill references，以及相關 EN/zh-TW artifact／retirement／hygiene／dashboard guides成對更新；canonical helper更新SKILL與post-session runbook及其changelog。
- 命令介面新增執行 `make skill-sync`、檢查 generated commands、`make skill-check`；skill改動後重建。文件執行 source check／strict MkDocs build／site check，必要時 regenerate llms。

## 實作次序

1. 隔離工作環境並保留兩個現有 checkout；建立 synthetic regressions。
2. 先完成兩個 TUI UX 與對應文件。
3. Native exact selector／Guard／revision corrections。
4. Canonical v2 receipt/review／sync/cloud correctness。
5. 薄型 co-commit adapter與partial-result處理。
6. 執行全套驗證，產出目前 hygiene findings 的審閱 plan，以及舊run的唯讀恢復預覽／明確 blockers；不自動amend／commit舊run／push／release。
