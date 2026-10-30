# REPOS 收藏、優先更新與快速開啟

## Context

使用者常透過 `dev → 2 (REPOS) → / 搜尋 repo → Enter` 在 Herdr 開啟日常開發的 repository，但選到目標仍會遇到掃描等待。這次改善分成兩件事：**常用 repo 容易找到**，以及 **開啟已選目標不依賴無關 repo 的掃描**。只加入置頂不能完整解決問題。

程式碼已確認：
- 現有載入會先顯示 cache／候選列，但 `Pending` 同時被鍵盤與 action registry 用來阻擋開啟（`internal/tui/model.go:3174`、`action_registry.go:134`）。刷新也會把既有列標為 pending。
- REPOS enrichment 要先等完整 discovery；部分 `runtime pending` 列直到最後整批結果才更新（`internal/cli/tui_load.go:174–185, 233–271`）。
- 普通開啟回呼只用選中路徑呼叫 runtime，不需要完整 inventory，但目前缺少獨立的目標新鮮驗證（`internal/cli/tui.go:382–400`）。不能只刪除 pending guard。
- Catalog 已有穩定 ID、個人 metadata 與 host location，尚無 favorite；普通 repo 也沒有完整的最近開啟紀錄。

這些是程式碼上的等待來源，尚未實測使用者那一次的延遲；Herdr 自身的 open/focus 耗時不一定會隨此修正消失。本輪只規劃，未修改產品程式或操作使用者的 repo/runtime。

## 使用體驗與範圍

- REPOS 父層 repository 列以 **`f` 切換收藏**，顯示 `★`，動作選單也提供加入／取消收藏。
- 收藏永久保存在本機 catalog，與 tags／note 並列；一個旗標同時代表置頂及優先更新。不建立 task、不改 Git、不記錄自動使用頻率。
- 先套用搜尋及既有排序，再把收藏穩定排到前面。反向／欄位排序仍在各群組內生效，worktree children 跟隨 parent；不匹配搜尋的收藏不出現。
- 啟動及明確刷新優先顯示、更新收藏；其餘 repo 繼續背景發現及更新。
- **所有 REPOS 項目的純開啟**都可走目標專屬驗證，不必等全域掃描；不僅限收藏。
- 保留既有搜尋語意：搜尋中的第一次 Enter 結束輸入，回到列表後 Enter／`o` 開啟。不擴及 FLEET／TRY 收藏、單獨 worktree 收藏、首頁捷徑、自動排名或新 CLI 子命令。

## 實作步驟

### 1. 先拆開「導航」與「完整觀測」的 readiness

主要檔案：`internal/tui/action_registry.go`、`action_definitions.go`、`model.go`、`internal/cli/tui.go`；在 `internal/repo/` 放可獨立測試的單目標解析／驗證 helper，CLI 負責接線。

- 讓 REPOS `listActionOpen` 通過 pending 的兩層 gate，且 keyboard／menu 使用同一規則。仍檢查 callback、child missing/prunable；不清掉 `Pending`，不改 start、cleanup、batch 等既有 guard。
- 開啟請求綁定選中的 path、已知 canonical/common-dir identity，以及 child checkout identity；在 runtime 呼叫前重新檢查該目標。重用 `pathx.Canonical`、`gitx.Discover` 與既有 worktree lookup，不呼叫會 fallback 到全域 discovery 的名稱解析。
- Git repo 檢查目錄、checkout root 與已知 Git 身分是否一致；child 另確認仍屬於同一 repository 且可用。失敗留在 TUI 顯示錯誤，不默默改開其他路徑。
- 保留既有非 Git 目錄支援：明確觀測為 plain directory 的項目只需要相應目錄驗證；unknown／Git 讀取失敗不能降級成 plain directory 來放行。
- 驗證不排在背景 enrichment limiter 後，也不等待全域 task、status、runtime `List`。仍解析實際 runtime 並沿用 `openResolved` → `openCheckout` → Herdr no-focus open → 退出 TUI → activate 的流程。
- 沿用既有 canonical/common-dir 身分保證，不為收藏另建 repository 身分系統；不宣稱能僅靠路徑字串辨識同一路徑刪除重建。已可觀察的路徑／身分不一致必須拒絕。

### 2. 收藏以 catalog intent 持久化，更新只影響該列

主要檔案：`internal/catalog/catalog.go`、`registry.go` 與 tests；`internal/tui/rows.go`、action registry／list actions；CLI 增加小型 favorite adapter（可獨立為 `internal/cli/repo_favorite.go`）。

- `catalog.Entry` 增加 additive `Favorite bool`，舊資料缺欄位等於 false；不改 schema 既有欄位名稱。掃描仍是 read-only，不因顯示列表而替所有 repo 建 catalog。
- `RepoActions` 新增明確的 `SetFavorite(ctx, row, desired bool)`，回傳更新後的 asset。保留現有 tags/note `Patch` callback 不變；沿用 `Registry.Patch` 的 locked mutation closure，只改 Favorite。
- 有 catalog ID 時操作該 asset 的收藏 intent；沒有 ID 時先做選中目標的新鮮解析，再於明確收藏動作中使用 `EnsureRepository`。不得把 `patchRepositoryCatalog` 現有的容錯 Git lookup 當成新鮮驗證。
- 收藏是新 metadata action，不放寬其他既有動作。已綁定 asset 的取消收藏不應因 repo 暫時不可用而被永久卡住；未綁定候選不得未驗證就建立身分。
- 使用專用結果訊息，成功才局部更新標記／排序，不觸發 generic `actionMsg` 的全量 reload。以 asset ID 套用結果、序列化同項目的修改，錯誤不可假裝成功。
- 在兩層既有排序後 stable-partition 收藏，沿用 `currentToken`／`repoFocusPath`／`restoreRepoFocus` 保持選中目標，避免重新排序後 Enter 開錯列。

### 3. 收藏先載入、先排入更新，不只是把顯示排序改掉

主要檔案：`internal/cli/tui_load.go`、`tui.go` 的 `collectReposWithOptions`、`repo_catalog.go`；`internal/tui/repo_progress.go` 與 local-result handling。

- 每次 load generation 先讀 catalog 的收藏及目前 host location。以 `Cfg.DiscoveryRoots()` 和既有 `repo.PathDiscoverableFromRoot` 套用深度、排除目錄、symlink alias 規則；收藏不能擴張掃描範圍。
- 在 cache 不存在時也能發布收藏的 provisional rows，明示 pending／unavailable；對這些已知路徑直接執行 target-only discovery/enrichment，不等完整 `repo.Discover` 回傳。
- 全量 discovery 照常並行。抽出可重用的單 repo enrichment，使用 bounded dispatcher 明確讓收藏先取得 REPOS 工作名額，沿用現有資源上限；不能只把 slice 排序後讓所有 goroutine 搶 limiter。
- 優先指尚未執行工作的排程順序，不保證完成順序，也不搶占已執行工作。收藏失敗／慢速不能變成所有普通 repo 的額外整批 barrier；保留取消和逐列錯誤。
- 同一 generation 以已驗證的 common-dir／canonical identity 去重收藏與全量結果，避免重複列、重複 enrichment 和 alias 重複。Runtime observation 就緒時更新已完成的列，不必等所有 enrichment 完成。
- Catalog preference 與 observation snapshot 分開合併：cache、舊 generation、較晚到達的 final snapshot 不能蓋回成功的收藏操作；新 generation 也不能讓成功的 intent 修改遺失。
- 暫時 missing／掃描不完整不刪除 durable favorite；適用範圍內保留 unavailable 狀態，不能假裝 clean。離開設定掃描範圍的 asset 不列入本次 REPOS。Final snapshot 的替換邏輯須保留這些區別。
- Snapshot cache 仍是可丟棄的展示資料，不作為收藏的唯一來源，也不授權任何 destructive action。

### 4. 同步使用說明

- `CHANGELOG.md` 的 `[Unreleased]` 加入收藏與 pending-row navigation 改善。
- 更新 README、`internal/tui/help_content.go`／action guidance、相關 CLI embedded help，以及 `internal/skill/dev-cli/SKILL.md`／`references/command-navigation.md` 的受影響描述。
- 同步 `docs/guides/tui-repos-bootstrap.md` 與 `.zh-TW.md`，及其他描述 pending action authority 的 paired TUI／compatibility 頁面：明確區分「目標驗證後導航」與「仍需完整觀測的操作」。
- 不新增 CLI syntax；若實作實際改到 Cobra help／flags，才跑 `make skill-sync`，不可手改 generated commands block。任何新增 structured field 都只做 additive 變更並測相容性。

## Verification

### 決定性回歸測試

擴充 `internal/cli/tui_load_test.go`、`internal/tui/list_actions_internal_test.go`、`action_registry_test.go`、`dashboard_improvements_test.go`、catalog／repo tests。以 channel barrier 控制時序，不靠 sleep 或易抖動的毫秒閾值：

1. 卡住完整 discovery、無關 repo enrichment 或全域 runtime listing，驗證選中 repo 仍可驗證並呼叫 open；刷新期間也一樣。Keyboard／menu 一致，第一次 Enter 結束搜尋的契約不變。
2. 冷啟動無 cache 時收藏先顯示；普通 enrichment 尚未完成時收藏已排程並發布。檢查並行上限、去重、取消與 runtime-ready 增量更新。
3. 缺失路徑、已知 canonical/common-dir 不一致、不可用 child 拒絕開啟；plain directory 繼續支援，Git lookup 失敗不能偽裝為 non-Git。
4. Favorite default false、true/false round-trip、重新啟動持久化；修改保留 tags/note，普通掃描不建立 asset，取消收藏不全量 reload。
5. Cache／final snapshot 晚到、刷新期間收藏修改、partial failure、reverse／column sort、filter、展開 children 都不丟失收藏或跳錯 focus。
6. 優先載入不越過原本 discovery roots/depth/skip/symlink 規則；其他非導航 actions 的 pending guard 仍有效。

### 手動驗收與專案 gates

- 用隔離 HOME／臨時 repo 與 fake runtime 跑實際 TUI：收藏、重啟、`2 → / → 名稱 → Enter → Enter`、刷新中開啟，以及取消收藏。不要用使用者的真實 catalog／workspaces 做自動測試。
- 如需確認實際 Herdr 體驗，另做使用者選定 repo 的 smoke test；可用既有 `DEV_TUI_TRACE` 區分 discovery、runtime、open/focus 耗時，不把未量測的速度提升寫成事實。
- Go：先跑 affected package tests，再 `go vet ./...`、`go test -race -timeout 20m ./...`；檢查 gofmt，重建 embedded skill 後 `make skill-check`。
- Docs：`uv sync --frozen --extra docs` → `uv run python scripts/check-docs.py --source` → `uv run mkdocs build --strict` → `uv run python scripts/check-docs.py --site site`；若 llms 檔漂移，依專案流程重生再檢查。

**完成標準：** 常用 repo 跨重啟保留收藏、在 REPOS 置頂且優先更新；即使其他 repo 還在掃描，使用者也能經目標新鮮驗證開啟選中的 repo，且沒有放寬需要完整觀測的操作。
