# 修 4 個 dev bug + 新增 `dev hygiene report`

## Context

上一輪清理 commit／branch 時實際踩到 4 個 bug：

- **A. macOS provenance xattr：** `dev hygiene repair-encoding --apply` 在「由其他 app 建立的檔案」上必定失敗。原因是 `com.apple.provenance` 由 kernel 管理，寫不回去，而 configedit 的 round-trip 檢查又要求它要寫回去。真正的錯誤還被 `plan.go` 吞掉了。
- **B. 無法解析的 task base：** base 是 commit OID 或 `origin/main` 時，retire／remove 會把它硬接成 `refs/heads/<base>`，結果永遠被擋。另外 `sweep --base` 不會傳給 managed task，`dev retire` 也沒有 `--base`。
- **C. sweep 批次 stale：** `sweep --apply --yes` 刪掉第一個 worktree 之後，其餘 plan 全部 stale。原因是 authority 雜湊了整個 repo 的 worktree 清單。
- **D. artifact guard 擋呼叫者：** hygiene 的 artifact writer guard 連呼叫它的 agent 自己也擋，`--allow-shared-checkout` 沒有作用，最後只能用 `--no-runtime` 整個略過（更危險）。

另外使用者想要快速看 hygiene 統計：按嚴重度、出現次數倒序，要有 `--json` 給 agent 用，也要能看每條 RULE 實際抓到的內容（遮罩後）。

### 已確認的決策

- **交付：** 全部放在同一個 branch。
- **報告形式：** 新增 `dev hygiene report` 子命令，預設讀最近一次掃描，不重掃。
- **值的展示：** 終端和 JSON 只顯示遮罩後的值；完整值只寫進 0600 的私有 review 檔，輸出只提示 `review-path` 指令。
- **OID base：** 不自動退回預設分支。無法證明 containment 時就 block，提示使用 `--base <branch>`。

## 分支與 commit 順序

- **建立方式：** `dev start fix-retire-base-hygiene-report --base main`（worktree 模式），branch 名 `fix/retire-base-sweep-batch-hygiene-report`。
- **Commit 順序：**
  1. `fix(configedit,sshhost): ignore macOS kernel provenance on staged writes`（A）
  2. `fix(taskflow): scope destructive worktree authority to the target`（C，先做，範圍小，而且跟 B 改同一個檔案）
  3. `fix(taskflow,cli): resolve retire bases as branch, remote-tracking ref or commit; add retire --base`（B）
  4. `fix(cli): allow the calling agent to repair another session's stopped artifacts`（D）
  5. `feat(hygiene): add report summaries, value IDs and private value review`（E 的 domain 部分）
  6. `feat(cli): add dev hygiene report; docs, skill, changelog`（E 的 CLI 與文件）
- **Release：** land 之後依 AGENTS.md 打 patch tag（v0.2.39）。push／tag 前會先問使用者。

---

## A. macOS provenance（`com.apple.provenance`）

1. **`internal/platformfs/anchor.go`：** 新增 `KernelProvenance(name, value)` 和可測試的 `kernelProvenance(goos, name, value)`。
   - 只在 `goos=="darwin"`、`name=="com.apple.provenance"`、`len==11`、`value[0]==0x01` 時回傳 true。
   - 格式不符時走原本的嚴格路徑（fail closed）。
   - 寫法參考同檔的 `KernelLabel`。
2. **`internal/configedit/metadata_unix.go` 的 `Metadata.prepare`：**
   - 在設定迴圈（Android 判斷之前）和驗證迴圈都略過 provenance。
   - round-trip 錯誤訊息帶出 attribute 名稱。
   - `readAttributes`／`captureMetadata` 不動，所以 `current()`／`stateOf`／`SourceToken` 仍會偵測同一個 inode 上的變更。
   - 新增 test hook：`kernelProvenance` predicate 變數和 `setAttribute` setter 變數。
3. **`internal/sshhost/xattr_supported_unix.go` 的 `platformWriteXattrs`：** 略過 provenance。
4. **`internal/sshhost/securefs_unix.go`：**
   - 新增 `platformVerifyStagedMetadata`：兩邊都先移除 provenance 再比對 map（純 helper `xattrsWithoutKernelProvenance`）。
   - 只給 `createStagedFile`（`securefs.go` ~L384）用。
   - 同一檔案的 capture 穩定性檢查（~L245）和 `permissions.go` 的比對維持嚴格。
   - Windows 版本直接委派給原本的檢查。
5. **不再吞錯誤：**
   - `internal/hygiene/plan.go` ~L400 改成 `fmt.Errorf("file apply interrupted: %w", e)`。
   - `Restore` ~L447 改成 `fmt.Errorf("recovery unavailable or source changed: %w", err)`。
   - `internal/agenthistory/setup.go` ~L383 同樣處理。
   - CLI 已經會經過 `feedback.Sanitize`。
6. **測試：**
   - `internal/platformfs/provenance_test.go`（無 build tag）：`TestDarwinKernelProvenanceIsNarrowAndUnavailableElsewhere`。
   - `internal/configedit/metadata_receipt_test.go`：
     - `TestPrepareNeverRestoresKernelProvenance`
     - `TestPrepareStillRejectsIgnoredOrdinaryAttribute`
     - `TestKernelProvenanceStillBindsSourceIdentity`（改 attr 之後 `Plan.Check` 回 `ErrStale`）
   - `internal/sshhost`：`TestStagedXattrsIgnoreOnlyKernelProvenance`。
   - `internal/hygiene`：`TestRestoreRetainsRecoveryCause`（`errors.Is(err, configedit.ErrStale)`）。

## C. sweep 批次 stale：縮小 worktree-list authority 範圍

1. **`internal/taskflow/destructive.go`：**
   - `inspectDestructive` 取得 `gitWorktrees` 之後，算出 `scopedWorktrees`：非 bare 且符合下列任一條件的項目。
     - branch 等於 locator branch；
     - canonical path 等於、位於或包含 checkout。
   - 沒有 checkout 時只比 branch。
   - canonical 化用 `s.canonicalPath`，prunable 項目退回 lexical clean。
2. **`worktreeListAuthority()`：** 改為 `taskflow-destructive-worktrees-v2`，雜湊內容是：錯誤、範圍內的數量、依 path 排序後的範圍內項目（欄位與現在相同）。
   - `reinspectRetire`、`reinspectRemoveCheckout`、`RetirementPreviewAuthority`、adopt、triage batch 都會自動受益。
3. **測試：**
   - `retire_remove_test.go`：`TestDestructiveWorktreeListAuthorityIgnoresUnrelatedEntries`（table test）。
   - `retire_remove_git_test.go`（沿用 `newLifecycleGitFixture`、`addUnmanagedCheckout`）：
     - `TestRemoveCheckoutRealGitSiblingRemovalKeepsReviewedPlanCurrent`
     - `TestRetireRealGitSiblingRemovalKeepsReviewedPlanCurrent`
     - `TestRetireRealGitSameBranchCheckoutAfterPlanIsStale`
   - `internal/cli/cli_test.go`：`TestSweepMergedWorktreesApplyYesRemovesEveryEligibleSibling`。
     - 建 3 個 merged 的 unmanaged worktree 和 1 個 DONE 的 managed task，執行 `sweep --merged-worktrees --apply --yes --assume-no-runtime`。
     - 斷言：沒有 stale 警告，只剩 main。

## B. base 解析與 `dev retire --base`

1. **`internal/gitx/worktree.go`：**
   - 新增 `CommitishState(ctx, dir, rev) (oid, exists, err)`，執行 `rev-parse --verify --quiet --end-of-options rev^{commit}`。
   - 結果分類方式同 `RefState`。
   - 在 `taskflow/lifecycle.go` 的 `LifecycleHooks` 加上 `GitCommitishState` hook。
2. **新增 `internal/taskflow/base_ref.go`：**
   - 定義 `baseResolution{input, ref, kind(local-branch|remote-tracking|commit), exists, oid, err}`。
   - `resolveBaseRef` 的規則：
     - 拒絕未 trim、含 NUL、或以 `-` 開頭的輸入。
     - 已經是 `refs/heads/…` 或 `refs/remotes/…` 就直接採用。
     - 否則依序嘗試 `refs/heads/X`、`refs/remotes/X`、commit-ish。
     - probe 發生錯誤時就停止，不往下一種嘗試。
   - `baseAuthority(o)`：雜湊上述所有欄位。
   - `baseAliasesBranch(o, branch)`：判斷 base 是否就是 branch 本身（參考 `ephemeral.localBranchAlias`）。
3. **`destructive.go`：**
   - `observeRefs` 改用 resolver，新增 `baseInput`／`baseKind` 欄位。
   - `refsAuthority` 改為 `taskflow-destructive-refs-v2`，並加入 input 和 kind。
   - base 那側不再用 `localBranchRef`（branch 側保留）。
4. **`retire.go`：**
   - base 取 `options.Base`，沒有才用 `candidate.Base`。
   - authority 加上 `retire.base-override`；`EffectDeleteBranch` 的 details 加入 base-kind 和 input。
   - L125／L137／L524 的 `candidate.Branch == candidate.Base` 改用 `baseAliasesBranch`。
   - `revalidateRetireRefs`／`revalidateRetireBase`（~L840-867）改傳 `baseline.baseInput`，不再用 TrimPrefix 繞回來，並比對 `baseAuthority`。
   - `retireRefConditions` 的文字：
     - 通過：「base X resolves to <kind> <ref> at <oid>」。
     - base 不存在：「base "X" is not a local branch, remote-tracking branch or commit」，提示 restore 或 `--base <branch>`。
     - commit base 且未被包含：「not contained in recorded fork-point base X」，提示 `--base <branch>`。
     - **絕不**自動換成預設分支。
   - `retireFallback` 附上 `--base`。
5. **`remove.go`：**
   - L645 的 revalidate 同樣修正。
   - L347 改用 `baseAliasesBranch`。
   - 條件文字改為「containment base X resolves to …」。
   - `removeCheckoutFallback` 附上 `--base`。
6. **`options.go`：**
   - 新增 `RetireOptions.Base`（驗證 normalized、無 NUL、不以 `-` 開頭），並加進 request identity。
   - RemoveCheckout 的訊息改成「branch, remote-tracking ref or commit」。
7. **CLI：**
   - `internal/cli/retire.go`：新增 `--base` 旗標，說明文字「containment base override: local branch, remote-tracking ref such as origin/main, or commit」。
     - unmanaged 路徑：有 `options.Base` 就用，否則用 DefaultBranch。
     - 傳給 `launchExternalRetireCoordinator`。
   - `retire_preview.go`：capture／validate 都帶 base。
   - `done_cleanup.go`：
     - `retireHandoffIntent.Base`（`json:"base,omitempty"`，additive）。
     - `runRetireCoordinator` 設定 `Base`。
     - `done --merged --base-ref X` 的 cleanup wizard 把 X 傳下去。
   - `sweep.go`：
     - `sweepRetireOptions.base`：`--merged-worktrees` 把已驗證 is-ancestor 的 base 傳進 `suggestFor` 的 `RetireOptions.Base`。
     - 一般模式下，明確指定的 `--base` 也傳給 DONE 的 retirement，並更新 flag 說明。
8. **測試：**
   - `TestResolveBaseRefOrderAndFailClosed`（fake hooks）。
   - `retire_remove_git_test.go`：
     - `TestRetireRealGitCommitBaseIsResolvedAndRevalidated`
     - `TestRetireRealGitRemoteTrackingBase`
     - `TestRetireRealGitForkPointBaseRequiresExplicitBaseOverride`
     - `TestRetireRealGitBaseKindChangeIsStale`
     - `TestRemoveCheckoutRealGitRemoteTrackingContainmentBase`
   - `cli_test.go`：
     - `TestRetireBaseFlagOverridesForkPointBase`（`start --base <oid>` → commit → ff → `done --merged --base-ref main` → retire 失敗且提示 `--base` → `retire --base main` 成功）
     - `TestSweepMergedWorktreesForwardsVerifiedBaseToManagedRetirement`
   - 後續另開項目（記進 TODO，不在本 branch）：`remote.go` 的 RefreshRemote 也假設 base 是 branch。

## D. artifact guard 放行呼叫者（需證明或明確 override）

1. **`internal/runtime/runtime.go`：** 新增 `AgentActivity.Session`（`provider:uuid`），不加進 occupancy authority。
2. **`internal/runtime/herdr.go`：**
   - 解析 `agent_session.kind`，只接受 `id` 或空值。
   - occupancy fallback（occupancy.go ~L185）改成複製 `pane.AgentSession`。
   - 擴充 `herdr_test.go` 的 fixture。
3. **`internal/artifact/transcript.go`：**
   - 新增 export 函式 `ReadTranscriptSession(path)`：先用 Lstat 確認是一般檔案（拒絕 symlink）。
   - 讀取沿用 `transcriptPreamble`（前 12 行／16 KiB），解析 `<!-- Claude Code Session <uuid> -->` 這類標頭。
4. **新增 `internal/hygiene/writer_guard.go`（純政策邏輯）：** `CheckWriters(agents, targets, allowCallerShared)`。
   - 非呼叫者的 agent：維持現狀，一律擋。
   - 目標標頭的 UUID 等於呼叫者 session：即使有 override 也拒絕。
   - 呼叫者 session 已知，且所有 artifact 目標的標頭都是其他 UUID：放行。
   - 其他情況：只有 `--allow-shared-checkout` 才放行呼叫者。
   - 錯誤訊息保留原本的前綴，不帶路徑。
5. **`internal/cli/hygiene.go` 的 `guard`：**
   - 建立 targets（只讀 `.specstory/history/*.md` 的標頭）。
   - 呼叫者 session 取自 `Activity.Session`，或 `CallerPaneID` 對應的 pane。
   - 傳入 `h.app.allowSharedCheckout`。
   - restore 的預檢（~L351）改成先預覽 receipt，再 guard 精確的路徑，不再 guard 整個 `.specstory/`。
   - 更新 `--writer-stopped` 的 help 文字。
   - 共用這個 guard 的 `artifact finalize/archive/migrate`、`hygiene manage` 會一併受益。
6. **測試：**
   - `internal/hygiene/writer_guard_test.go`：table test。
   - `internal/cli/hygiene_artifact_guard_test.go`（沿用 `activityRuntime`／`currentPaneRuntime`）：
     - 原有測試保留並調整語意。
     - `TestHygieneCLIWriterGuardExemptsCallerForAnotherSessionTranscript`
     - `TestHygieneCLIWriterGuardRefusesCallersOwnTranscriptEvenWithOverride`
     - `TestHygieneCLIWriterGuardUnprovenCallerRequiresAllowSharedCheckout`
     - `TestHygieneCLIWriterGuardOverrideNeverExemptsOtherAgents`
   - `internal/artifact`：`TestReadTranscriptSessionRejectsSymlinkAndLateMentions`。

## E. `dev hygiene report`

### Domain（`internal/hygiene`）

1. **`service.go`／`scan.go`：**
   - 新增 `Finding.ValueID`（`json:"value_id,omitempty"`，additive），值為 `keyedID(key,"value",category,rule,value)`。這樣同一個值跨檔案可以聚合。
   - `scanRecord` 新增 `Values []ValueSummary`（只存遮罩後的值）、`ValuesReview`、`ValuesDigest`。
   - 新增 `ScanOptions.CaptureValues`。
   - 值的收集器掛在 `native()` 和 worktree／staged 的 secret loop，前後文為 ±120 bytes 並 escape 控制字元。history 不帶前後文，snapshot 不收集。
   - 上限：10k 個 distinct 值、每個值 20 筆樣本；超過就標記 `values_truncated`。
   - 完整值只寫進 `<id>.values.review.txt`：用 `configedit.WritePrivate`，標頭沿用 `savePlan` 的「PRIVATE REVIEW … Never paste…」，digest 用 keyed。
2. **latest pointer：**
   - 成功儲存 `hygiene_scan` 後，以 best-effort 更新 `<Dir>/latest.json`，失敗不影響 scan 和 hook。
   - 使用獨立的 `lockx.WithFile(<Dir>/latest.lock)`，不共用 plan apply 的目錄鎖。
   - key 是 `keyedID(key,"root",Root)`：因為 RepoID 由所有 worktree 共用，必須依 checkout 區分。
   - 每個 checkout 記錄 any／staged／worktree／history 各自最新的 `{id, created}`，只接受 created 較新的更新，最多 256 個 checkout。
   - 排除 snapshot 報告。
3. **`values.go` 的 `maskValue(category, rule, value)`：**

   | 類別 | 遮罩方式 |
   |---|---|
   | secret | 前後各 2 字元加長度；長度 < 12 時只顯示長度 |
   | known | `[private:<len>]` |
   | privacy-email | `a•••@domain` |
   | privacy-ip | `192.168.•.•`；IPv6 為第一組加 `:•••` |
   | privacy-home-path | `/Users/•••` |
   | 其他 | 只顯示長度 |

   最後再經過一次 `s.displayPath`。
4. **`summary.go` 的 `Summarize(ctx, SummaryRequest{ReportID, Scope, By, Top, Dispositions, Rules, Categories, Paths, Findings, Values})`：**
   - 載入 record，並驗證 `Report.ID==id`、RepoID 相同、kind 為 scan 或 snapshot。這樣 plan 的 ID 會被拒絕。
   - 計算 `policy_current`／`checkout_current`。
   - 先過濾再聚合。
   - 排序：嚴重度（block > warn > accepted）→ occurrences 倒序 → findings 倒序 → 名稱。
   - `--top` 之外的項目回報 omitted 數量。
   - 要看 values 但該報告沒有收集時，提示用 `--rescan --values` 重跑。
5. **`plan.go` 的 `ReviewPath`：**
   - 先判斷 ID 是 plan 還是 report。
   - report 必須符合 `ValuesReview == id+".values.review.txt"`、RepoID 相同、keyed digest 相符，否則回 `ErrStale`。

### JSON 合約：`kind: hygiene_summary`、`schema_version: 1`（additive）

- **頂層：** `report_id, report_kind, scope, status, created, policy_current, checkout_current, audit, public_only, rescanned, filters{}`
- **`totals`：** `{findings, occurrences, blocked, warnings, accepted, gaps, files, scanned_files, skipped, rules}`
- **`severities[]`：** `{disposition, findings, occurrences}`
- **`rules[]`：** `{rule, category, disposition, findings, occurrences, files, distinct_values?, values?[]{value_id, masked, length, occurrences, files, findings}}`
- **`files[]`：** `{file, disposition, findings, occurrences, rules[]}`
- **其他：**
  - `findings[]`：只有加 `--findings` 才輸出。
  - `gaps[]`、`omitted{rules, files}`、`values_truncated?`
- **永遠不包含：** 原始值、review 檔的路徑。

### CLI（新檔 `internal/cli/hygiene_report.go`，在 `hygiene.go` ~L377 註冊）

- **選擇報告：**
  - `--report <id>`
  - `--scope`：取該 scope 最新的報告；搭配 rescan 時就是掃描的 scope。
  - `--rescan`，搭配 `--range`／`--file`／`--timeout`／`--audit`。這四個都只能跟 `--rescan` 一起用。
- **呈現：**
  - `--by severity,rule,file,category`，預設 `severity,rule,file`。
  - `--top N`，預設 10，0 表示全部，負數拒絕。
- **過濾：** `--disposition`、`--rule`、`--category`、`--path <glob>`（沿用 `pathMatches`）。
- **細節：**
  - `--findings`：列出每一筆 finding（file:line ×occ、id、commit）。
  - `--values`：遮罩後的 distinct 值表格。沒有指定 `--report` 時等於加了 `--rescan`。
- **Exit code：**
  - 讀取已存報告：exit 0。
  - rescan 後掃描不完整：仍然印出摘要，再回傳 incomplete 錯誤。
  - 有 blocked findings 不算 report 失敗。
- **`h.output` 新增 `case hygiene.Summary`：**
  - 照使用者確認過的 preview 排版，使用 `app.newTable`（color.go:174）和 `outStyle`：block 用 danger 色、warn 用 warning 色、accepted 用 dim。
  - 新增千分位 helper。
  - 另外輸出完整的 Report ID、gaps／coverage 區段；partial 時顯示「Scan incomplete」。
  - 有 `--values` 時加上 VALUES 表，並印出「Private full values: dev hygiene review-path <id>」。
  - `textFields` 補上 `rules`、`masked`、`commit`。
- **`review-path`：** `Use` 改為 `review-path <plan-or-report-id>`。

### 測試

- **`internal/hygiene/summary_test.go`（沿用 `testService`／`put`）：**
  - `TestSummaryAggregatesSeverityRuleAndFileDeterministically`
  - `TestSummaryCountsAcceptedAndKeepsPartialVisible`
  - `TestLatestPointersArePerCheckoutAndIgnoreSnapshots`（包含 linked worktree）
  - `TestSummaryRejectsPlanAndForeignRecords`
  - `TestFindingValueIDStableAcrossFiles`
  - `TestMaskValueByCategory`
  - `TestValuesCaptureNeverPersistsRawValuesOutsidePrivateReview`：
    - summary JSON 和 record `.json` 裡都 grep 不到原始值；
    - review 檔權限是 0600；
    - 竄改 review 檔後 `ReviewPath` 回 `ErrStale`。
- **`internal/cli/hygiene_test.go`（harness 模式）：**
  - `TestHygieneCLIReportSummarizesLatestScanWithoutValues`
  - `TestHygieneCLIReportValuesUsePrivateReviewPath`
  - `TestHygieneCLIReportFlagContracts`

---

## 文件同步（依 AGENTS.md 的同步表，英文和 zh-TW 成對更新，`verified_on` 一致）

- **產生的檔案：**
  - `make skill-sync` → 檢查 `internal/skill/dev-cli/references/commands.md` → `make skill-check`。
  - 用 `uv run python scripts/check-docs.py --source --generate-llms` 重新產生 `docs/llms*.txt`。
- **內嵌 help：**
  - `internal/help/topics/hygiene.md`：report、values 隱私、review-path。
  - `internal/help/topics/retirement.md`：base 種類、`--base`、sweep 會傳遞 base。
  - `internal/help/topics/ai-artifacts.md`：呼叫者豁免規則。
- **Skill：**
  - `references/hygiene.md`：agent 應優先使用 `report --json`。
  - `references/agent-retirement.md`、`references/task-lifecycle.md`。
  - `references/ai-artifacts.md`、`references/parallel-agents.md`。
- **Guides（English + zh-TW）：** `docs/guides/hygiene`、`agent-safe-retirement`、`ai-artifacts`。
- **Reference（English + zh-TW）：**
  - `docs/reference/commands-config`。
  - `docs/reference/compatibility`：`hygiene_summary` v1、additive 的 `value_id`、`retire --base`、provenance 的 metadata 行為。
- **README：** hygiene 段落和 retire 的說明。
- **CHANGELOG `[Unreleased]`：**
  - Added：`dev hygiene report`、`dev retire --base`。
  - Fixed：A、B、C、D。
- **AGENTS.md：** `platformfs` 那一條補上 macOS provenance；新的 JSON 合約補一句說明。

## 驗證

```bash
files="$(gofmt -l .)" && test -z "$files"; make vet
go test ./internal/platformfs ./internal/configedit ./internal/sshhost ./internal/hygiene ./internal/agenthistory
go test ./internal/taskflow -run 'Retire|RemoveCheckout|Base|Worktree|Adopt'
go test ./internal/runtime ./internal/artifact ./internal/triage
go test ./internal/cli -run 'Sweep|Retire|Hygiene|Lifecycle'
GOOS=windows go build ./... && GOOS=android GOARCH=arm64 go build ./...
go test -race -timeout 20m ./...
make skill-sync && make skill-check
uv sync --frozen --extra docs && uv run python scripts/check-docs.py --source --generate-llms
uv run python scripts/check-docs.py --source && uv run mkdocs build --strict && uv run python scripts/check-docs.py --site site
make e2e
```

### 手動驗證

1. **A（無法自動化，因為測試行程共用同一個 provenance）：**
   - 在 Terminal.app 建 `printf 'ok\n\xe4\n' > cut.md`。
   - 在 Herdr pane 用新的 `./dev` 執行 `repair-encoding` 的 preview → apply → `restore --apply`，三步都應成功。舊 binary 應該重現「file apply interrupted」。
   - 在一份拋棄式的 HOME 副本上重複 `dev ssh init`。
2. **E：**
   - 執行 `dev hygiene scan --scope staged` → `dev hygiene report` → `report --by rule --top 5 --findings`。
   - `dev hygiene --json report | jq '.kind,.totals,.rules[0]'`。
   - `report --values` 之後執行 `review-path <id>`：確認 stdout 和 JSON 都沒有原始值，review 檔是 0600。
   - 在 linked worktree 裡跑 `report`，不應看到主 checkout 的掃描。
3. **D：**
   - 在 Herdr pane 對「其他 session」已停止的 transcript 執行 `repair-encoding --apply --writer-stopped`：不需 override 就應成功。
   - 對目前這個 session 自己的 transcript 執行：應被拒絕。
4. **C／B：**
   - 準備 2 個以上 merged worktree，執行 `dev sweep --merged-worktrees --apply --yes`：一次就全部清完。
   - task base 設為 OID 時，`dev retire <task>` 應提示 `--base`；`dev retire <task> --base main` 應成功。

## 風險與注意事項

- **provenance 格式：** 判斷條件寫死 11 bytes 且開頭為 0x01，Apple 改格式時會 fail closed。檔案被替換後，provenance 變成寫入者的值，這點要寫進文件。
- **authority 與 plan 失效：**
  - v2 hash tag 會讓升級前核准的 plan 變 stale，這是可接受的。
  - nested worktree 會被算進範圍內，結果是 fail closed。
- **retire 用非 HEAD 的 base 刪 branch：** `git branch -d` 可能因為 Git 自己的 merged 判斷而拒絕。維持現有行為並寫進文件。
- **呼叫者豁免：** 依賴 Herdr 的 `agent_session` id 與 SpecStory 標頭的 UUID 一致。resume／fork 的情境無法證明時，會退回要求 `--allow-shared-checkout`；自己的 transcript 永遠拒絕。
- **values 遮罩：** email 的 domain 和 IPv4 前兩段仍可見，之後可以考慮 `--mask strict`。values review 檔的敏感程度等同 redact 的 review 檔。
- **既有報告：** 舊報告沒有 `value_id`，distinct 值的統計會省略。掃描紀錄的清理（pruning）不在本次範圍。
