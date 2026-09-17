# Cleanup feedback 修正與 v0.2.39 PR／merge／release

## Context

先前的 4 個 bug 修正與 `dev hygiene report` 已完成，在
`fix/retire-base-sweep-batch-hygiene-report`，HEAD `9eb43a9`，worktree 乾淨；
完整 race suite、E2E、文件與 skill 檢查已通過。

本次使用者授權開 PR、merge、發新版，並修復 feedback
`d5d70d98-94f7-4c0d-9a18-1819fd7d4934` 的兩個 cleanup blocker：
1. 無關 repository 的 artifact intent 錯誤阻擋 detached checkout。
2. 從未初始化／完全空的 submodule worktree 缺乏安全 cleanup 路徑。

目前 GitHub `origin/main` 是 `68aa255`，最新 release 是 `v0.2.38`；
`v0.2.39` 尚未存在，現有 feature branch 尚未有 PR。
所提供的 Herdr pane 仍有 agent；只讀取過 pane 身分，沒有送入指令或執行 cleanup。

## 交付與資料保留

- 將本次兩個修正與已完成的功能以**同一個 PR、一次 patch release**交付。
- 現有 feature branch 繼承了未發布的 `2f93517 Backup chat history`；本機
  `main` 另外還有 `c5a57df` 聊天備份。**不把這些備份、`.specstory`、
  私有 feedback、pane／socket 資訊或新 plan 一起發布。**
- 實作時重新 fetch／確認 remote main 與最新 release，建立乾淨的
  `release/v0.2.39` 外部 worktree（名稱隨實際可用版本調整）。
  從 remote main 依序重播既有 9 筆程式碼／測試／文件提交：
  `82a5c99`, `d749218`, `07de875`, `df892cb`, `ad09f66`, `bc1eb62`,
  `67a54f2`, `2c5d862`, `9eb43a9`；不重播聊天備份提交，再加入兩個 feedback fixes。
- 保留原 feature worktree、本機 main、未提交／暫存檔及所有 remote branches。
  不 force-push、不改已發布 tag、不覆寫系統安裝的 dev，也不清理另一 session 的 repository。

## 修正 1：先以 repository 身分篩選 artifact，再檢查 moved-intent branch

關鍵檔案：`internal/artifact/service.go`、`service_test.go`、
`internal/taskflow/submodules.go`、`submodules_git_test.go`。

- 保留 `Store.List()` 完整性檢查、exact canonical checkout matching 與錯誤累積。
- 將 `inspectReadinessCheckoutIdentity` 的 Git common-directory discovery 與
  branch observation 分開。先 canonicalize 所選 checkout 與 unmatched intents
  的 common directories，排除已證明不屬於此 repository 的記錄。
- 只有仍有同一 common-directory 的 moved-intent 候選時才要求 branch 身分；
  此時 detached／不明 branch 繼續 fail closed。
- 不依 finalized/pending status 猜測某記錄是否無關；不跳過
  `inspectIntentReadiness`、`InspectHistoryReadiness`、receipt verification。
- `submoduleClaims` 分開「artifact observation error」與「已觀察到未完成 artifact」；
  保留前者的具體原因與 child path，讓 plan evidence 與 apply diagnostics 有用。
  公開輸出不得新增私有內容傾印。

測試沿用 `createReadinessIntent`、`isolateGitConfig`、
`submoduleLifecycleFixture`、`createPendingArtifact`：
- detached + unrelated finalized/pending intents：ready、zero matches、known empty。
- relevant exact pending、same-repo moved ambiguity、unreadable/corrupt store：仍阻擋。
- canonical aliases／branch matching 不改變 repository 邊界。
- detached child 的 submodule claims 不受無關 intent 影響；實際 observation error 保留原因。
- 可重用 feedback 的 `repro/artifact_test.go`，只移植成獨立測試；不提交私有報告檔。

## 修正 2：安全區分空 gitlink 與實際保留的 child repositories

**保留 `--recursive` 明確授權。** 安全空節點只豁免不存在之 child repository 的
復原／遠端證明，不豁免 cleanup 的 Plan／Apply。無 `--recursive` 仍顯示可操作的 blocker。

關鍵檔案：`internal/submodule/removal.go`、`removal_test.go`、
`internal/taskflow/submodules.go`、`completion_runtime.go`、`submodules_git_test.go`，
以及 `internal/safefile` 的 directory-only removal helper 與 native platform tests。

### Local observation 與 sealed authority

- 在 `InspectRemoval` 中建立 removal-local layout snapshot，分開觀察與授權。
  非 recursive 預覽可以提供 layout authority，但不可提供可執行的 disposal authority；
  `VerifyRemote`／`Apply` 必須仍拒絕未授權的 removal，不能依賴呼叫者忽略 error 來守住界線。
  記錄 checkout／Git-dir roots、empty child 的 exact identity 或 observed absence、
  administration directory 的路徑／身分／排序 listing，以及 initialized disposal subset。
- 分類為：owned initialized、safely empty、uninitialized with retained data、unknown/unsafe。
  安全空必須是缺席或真正空的一般目錄，且整個 private module storage 沒有 retained child store。
  任何 local/ignored file、`.git` marker、nested user directory、symlink/reparse point 或觀察錯誤都不算空。
- `inspectModuleStorage` 的 known repository set 只包含 initialized nodes；
  仍掃描全部 private administration，包括 nested `modules/`，拒絕 retained／orphan stores 與任意檔案。
  缺席的 modules root 是事實而非錯誤，不建立它；只有已審閱的 directory-only scaffolding 可待移除。
  零 gitlink 時也先檢查 storage：缺席為 no-op，空 scaffolding 需 recursive plan，實際 orphan data 阻擋。
- 用 deterministic `LayoutFingerprint` 封入 `submodule-removal` authority；
  `decorateSubmodules`、`prepareSubmodules` 及 `RetirementPreviewAuthority` 同步保存／重驗。
  現有 graph、workspace intent、claims 與 recursive authority 不移除。
- all-empty preflight 是 local/non-network，描述不得聲稱將處置 private refs/objects 或需要 push。
  `InspectPublication` 的原有整合／發布條件不變。

### Claims、Apply 與目錄移除

- 空／缺席 child 不呼叫會向上找到 parent repository 的 Git readiness discovery。
  task claims 保持 exact/subtree path matching；artifact claims 由 strict `Store.List()`
  和 canonical exact/subtree path matching 觀察，保留相關 intent identity/status/receipt
  與 unreadable inventory blockers。空路徑的 matching non-discarded intent 一律作為 ownership
  blocker（包含 finalized，不能借 parent Git 驗證）；discarded 不阻擋但仍封入 authority。
  不使用會吞觀察錯誤的便利 listing，initialized child 的正常 finalized receipt 語意不變。
  initialized child 保留完整 artifact receipt/readiness 檢查，runtime coverage 仍涵蓋整個 subtree。
- `VerifyRemote`／`Apply` 的 proof cardinality、origin queries、leases、private-state
  checks、moves 只處理 initialized subset；不使用空的 `CommonDir` 取得鎖。
- **all-empty：** 不建遠端 proof repository、不 quarantine、不搬 child，不要求 child push。
  仍重驗 graph／layout／workspace revision／claims，且 final claim callback 即使 zero initialized
  nodes 也至少執行一次。在 parent repository/task locks 內、第一次變更前及最終 removal 前驗證。
- **mixed：** 只 stage initialized children，保留 deepest-first 次序、完整 modules quarantine、
  journal/rollback/recovery；empty nodes 的 layout 也需重驗。沿用 `recover.go`，不重做復原架構。
- 保留 `gitx.RemoveWorktree` 對 modules storage 的保守拒絕；只有 recursive removal 層
  能 bottom-up prune 已審閱、身分仍相同且仍空的 administration directories，再呼叫
  普通 `git worktree remove`（force=false）。
- 在 safefile 新增窄化的 `RemoveEmptyChildDir(ctx context.Context, parent *os.Root, name string, expected fs.FileInfo) error`
  directory-only primitive：驗證 portable child name、`OpenChildRoot`／`VerifyChildRoot`／empty listing，
  再做 native directory-only delete（Unix dirfd `unlinkat(..., AT_REMOVEDIR)`；
  Windows relative-to-held-parent、verified non-reparse directory handle，驗證前不得設定 delete-on-close）。
  不使用會 unlink 替換檔案的 `os.Remove`／`os.Root.Remove`，不使用 `RemoveAll` 或 force；
  不具已驗證 backend 的平台 fail closed。
- Plan 後 absence→presence、初始化、新檔案、root 替換、symlink／claim 變更皆 stale；
  late nonempty content 必須由 directory-only primitive 拒絕。raw writers 仍不受 dev lock 保證。
  已 prune 部分空目錄後失敗，typed error／taskflow ledger 要列明 partial effect，
  不宣稱 worktree 已刪或 task 已 retired、不默默重建 administration。
- `objects/info/alternates` 及 squash ancestry 限制不是本次要繞過的 bug。

## 文件與驗證

- 在 `[Unreleased]` 增加兩個 fixes，更新 affected help／skill／英文及 zh-TW
  artifact、submodule、worktree／retirement 與 compatibility 安全語意。
- 納入 feedback 的隔離 repro（只處理 fixture），先驗證舊邏輯失敗，再驗證修正成功。
- 測試矩陣：unrelated/relevant artifact、never-initialized child、absent/empty modules root、
  mixed initialized/empty nodes、orphan/retained stores、ignored files、symlink 與重驗 race。
- 使用一致的 Go toolchain（此 session 可用 Go 1.26.4，勿混用其他 session 的 GOROOT）。
- 執行 focused artifact/submodule/gitx/taskflow/CLI tests、`go test -race -timeout 20m ./...`、
  `go vet ./...`、format check、`make e2e`、`make skill-sync`／`skill-check` 與 strict docs checks。
- 在 `.github/workflows/ci.yml` 加入 focused、non-advisory 的 Windows 原生 cleanup regression gate，
  驗證 directory-only deletion 與 empty-submodule 路徑；交叉編譯或 broad advisory job 的成功圖示不能代替。

## PR、merge 與 release

1. 更新 `[Unreleased]` 為 `[0.2.39] - 實際發布日期`、comparison links、
   AGENTS.md 的 published baseline 與推薦版本的 pinned install examples；不改 docs tooling 的 `0.0.0`。
2. 確认 publication branch diff／commit 範圍不含聊天備份和私有 inputs，再 push branch。
3. 用 `gh pr create --repo daviddwlee84/dev-cli --base main` 建立單一 PR，
   body 僅包含已審阅的功能／fix 摘要、repro 與實際測試結果。
4. 等待 CI／Docs／Repository hygiene 的當前 PR head checks，處理真正的失敗後，
   用 merge commit（保留 ancestry；不 squash、不 admin bypass、不刪 remote branch）合併。
5. 確認 exact merged commit 已存在 remote main，再建立全新 immutable `v0.2.39` tag 並 push tag。
   本機 main 的聊天備份與暫存狀態不因發布而被 reset／重寫。
6. 等待 Release workflow 完成，核對版本、平台 binaries、compact source archive、
   `SHA256SUMS` 和必需的 Homebrew formula publication；只有真正通過才回報發布完成。
   失敗就保留現有 tag 與 receipt，修正／rerun，不移動 tag 或假裝已發完。
