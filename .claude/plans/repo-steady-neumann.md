# REPOS 初次載入順暢度：凍結排序、批次套用、快取排序提示、SIZE 即時顯示

## Context

使用者回報：開啟 dashboard 進入 REPOS（106 repos）時，背景掃描（Git 狀態、Herdr 狀態、latest、size）陸續回來，列表不斷重排、SIZE 欄位 `—` → `…` → 數值 閃動，體驗不順。

已追出的成因（讀碼確認）：

- **排序鍵在串流期間一直變**：`internal/tui/model.go` `visibleRepos` 每次 render 都用 `compareRepos` 重排；預設 `activity` 依序比 HotTasks → Live → Dirty → len(Tasks) → LastActivity → name。快取列（`Pending=="cached"`，`$XDG_CACHE_HOME/dev/repos-v1.json`）只有 Repo/Status/LastActivity，**沒有 Tasks/Live/Worktrees**。之後每個 repo 各一筆 `enrichment`（補 tasks）與 `runtime`（Herdr `rt.List` 回來後補 Live）訊息都會改該列排序鍵，列就往上跳；可視視窗以游標為中心，被選列一動整頁跟著捲。
- **沒有訊息合併**：`internal/cli/tui_repo_load.go` `collectTUIRepoStream` 每個 repo 各 publish 一筆；`internal/tui/local.go` `waitForLocal` 一次只收一筆 → 一次 Update（含數次 `visibleRepos` 全排序）。106 repos ≈ 200+ 次。
- **SIZE 起步晚**：`beginSizeLoad` 只在 local load `done` 後啟動（`model.go` localMsg done 分支）；快取列沒 `SizeTarget` 顯示 `—`，enrichment 後 `…`，等 `sizeMsg` 才有值。`internal/diskusage` 本身有 10 分鐘 TTL 的 `sizes-v1.json` 快取，命中純記憶體查表，只是沒在首幀用上。
- 游標已依 path 保留（`applyRepoProgress` → `restoreRepoFocus`），不是問題來源。

目標：首幀順序 ≈ 最終順序；串流期間順序不動、完成時最多重排一次；SIZE 快取命中首幀即顯示；訊息批次套用。不新增 config 旗標。

## 設計決策

- 凍結順序以 `Repo.Path` 為鍵（discovery 候選列在 Git identity 未知前就發出，`repoKey()` 的 CommonDir 之後才會補上，不穩定）。
- 快取階段直接 join **真實** tasks（tasks future 是本機 TOML 讀取，已平行啟動），不另造 task-count 提示欄位；只有 Live 需要提示欄位 `CachedLive`（僅供排序，永不渲染、永不授權動作；動作門檻仍靠 `Pending != ""`，見 `action_registry.go:128,134`）。
- `repo.Snapshot` 維持 Version 1，新欄位零值相容（檔名 `repos-v1.json` 被 `dev self cache clear repos` 與文件引用，升版只會讓所有人多一次冷啟動）。
- SIZE 不在快取階段開 `Manager.Start`（會對 miss 啟動真實掃描、和 Git enrichment 搶 IO、又被完成後的 `beginSizeLoad` 取消）；改加同步、只查快取的 `Manager.Lookup`。
- 篩選不需要重新凍結（篩選保留相對順序）；只有排序鍵改變（`O`/`R`、欄位排序、config reload）才立即重排並重新凍結。

## 實作步驟

### 1. 快取帶 Worktrees + Live 提示；快取列補 Tasks 與 SizeTarget
檔案：`internal/repo/cache.go`、`internal/cli/tui_load.go`、`internal/cli/tui_repo_collect.go`、`internal/tui/rows.go`、`internal/tui/model.go`

- `repo.CachedRow` 加 `Worktrees int`、`Live bool`（註解：僅供呈現順序提示，非授權、不顯示為 live 狀態）。
- `tui.RepoRow` 加 `CachedLive bool`，只在 `Pending=="cached"` 列設定；`prepareRepoRowCollector` 不設，merge 後自然清掉。
- `tui_load.go` cache 階段（~229-240）：把 `tasks.wait(ctx)`（原 ~241）移到建列之前；用與 `tui_repo_collect.go:34-37` 相同邏輯（含 RealPath）建 `byRepo`，設 `row.Tasks`、`row.Worktrees`、`row.CachedLive`、`row.SizeTarget`。把 `tui_repo_collect.go:89-96` 的 `diskusage.FromGit`/`Plain` 分支抽成 `repoSizeTarget(r repo.Repo, worktrees int)` 共用。`targetSignature` 會 Lstat，留在 loader goroutine。
- 寫快照（~284）加 `Worktrees: r.Worktrees, Live: r.Live`。
- `compareRepos`（`model.go` ~1680）Live 比較改用 `liveHint(r) = r.Live || (r.Pending=="cached" && r.CachedLive)`；其他地方不讀 `CachedLive`。

### 2. 串流期間凍結順序
檔案：`internal/tui/model.go`、`internal/tui/repo_progress.go`、`internal/tui/local.go`、`internal/tui/readiness.go`、`internal/tui/action_shortcuts.go`、`internal/tui/table.go`

- Model 加 `repoOrder []string`（slice，Bubble Tea 值複製），存 `Repo.Path`。
- `visibleRepos` 拆成 `filterRepos()`（篩選 + `withRepoFavorite`）與 `sortRepos(rows)`（現有 comparator + `applyColumnSort` + favorites 分組）。`visibleRepos`：若 `0 < m.repoProgressPhase < 3 && len(m.repoOrder) > 0`，依 `repoOrder` 索引排名（本地 map），未知 path 依 `m.repos` 到達順序排在後面，再做 favorites-first 穩定分組；否則走 `sortRepos`。
- `(m *Model) freezeRepoOrder()`：`m.repoOrder` = `sortRepos(所有非 Try 列, 不套篩選)` 的 path 清單；僅在 phase 1/2 生效。呼叫點：
  - `applyRepoProgress`：`Phase=="cache"`/`"favorites"` 後；第一筆 phase-2 結果到達且 `repoOrder` 為空（無快取冷啟動 → 凍結到達順序）。
  - `action_shortcuts.go:109/125` 的 `listActionCycleSort`/`listActionReverseSort`（ViewRepos 分支）、`table.go:86` `cycleTableSort`（`m.view==ViewRepos`）、`model.go:2882` config 更新分支。
- 清空：`local.go:110` 完成快照（`repoProgressPhase=3` 旁）與 `readiness.go:91` `beginViewLoad` 重置。`reposMsg`/`reloadMsg` 無 phase，不會碰到凍結。
- 完成時的一次重排由既有 `restoreRepoFocus` 保游標。

### 3. 批次套用 local / size 結果
檔案：`internal/tui/local.go`、`internal/tui/size.go`、`internal/tui/model.go:2549`

- `localMsg{load; results []LocalResult; done bool}`。`waitForLocal`：阻塞收第一筆，再 drain：非阻塞接收 + 單一 `time.NewTimer(localBatchWindow)`（~20ms，package var 供測試歸零）+ 上限 64；drain 中 channel 關閉則同一訊息 `done=true`。抽成 `drainLocal(ch, window, cap) localMsg` 供單元測試。
- handler：迴圈 `applyLocalResult`，收集最終 phase 結果的 clone/fleet 指令；若 `done` 走既有 done 分支（generation 檢查、`finishLocalLoad`、`beginSizeLoad`），否則 `waitForLocal(load)`。
- `sizeMsg{loadID; results []diskusage.Result; done bool}` 同樣處理；`updateSize` 逐筆做 owner 檢查 + `applySizeResult`，最後處理 `done`。
- 更新建構訊息的測試：`dashboard_improvements_test.go:404`、`startup_repo_test.go:70-82`、`readiness_internal_test.go:123`、`triage_test.go:117-127`。

### 4. SIZE 首幀讀 diskusage 快取
檔案：`internal/diskusage/manager.go`、`internal/tui/size.go`、`internal/cli/tui.go:538`、`internal/tui/repo_progress.go`

- `Manager.Lookup(targets []Target) []Result`：每個 unique target 做 `Cache.Get`，不掃描，只回命中（`Cached=true`）。
- `SizeActions.Cached func([]diskusage.Target) []diskusage.Result`，在 `tui.go` 與 `Start` 一起接線。
- `applyRepoProgress` 在 `mergeRepoProgress` 後：收集 `SizeTarget.Checkout!="" && Usage==nil && SizeError==nil` 的列，呼叫 `Cached`，用 `applySizeResult` 套用；`m.forceSizeReload` 時跳過。
- `mergeRepoProgress` 第 28 行：僅在 `old.SizeTarget.Key == row.SizeTarget.Key` 或舊 key 為空時保留舊 Usage（worktree 數改變會換 key）。
- 完成後的 `beginSizeLoad` 不變：命中即時重送、miss 才掃。

### 5. 測試
- `internal/tui`（仿 `TestRepoProgressRejectsLateCacheAndKeepsFocus`）：快取列 A、B（B 有 `CachedLive`），A 的 enrichment 帶 hot task 到達時順序不變；完成快照只重排一次；串流中 `O`/`R`/欄位排序立即重排；無快取啟動凍結到達順序；`beginViewLoad` 清空順序；串流中篩選保留相對順序。
- `drainLocal`：預備 channel 放 N 筆再關閉 → 一則訊息且 `done==true`（window 歸零）；size 同樣；`Update` 套用全部並只啟動一次 `beginSizeLoad`。
- `applyRepoProgress` 由 `Cached` 填 `Usage` 且不呼叫 `Start`；key 不符時丟棄舊 usage。
- `internal/repo/cache_test.go`：`Worktrees`/`Live` round-trip；舊 v1 檔無欄位仍可讀。
- `internal/cli/tui_repo_load_test.go`（仿 `TestTUILocalLoaderPublishesCacheBeforeDiscoveryCandidates`）：cache 結果列帶 `Tasks`、`SizeTarget`、`CachedLive`；寫出的快照含提示；enrichment 列 `CachedLive==false`。
- `internal/diskusage`：`Lookup` 只回新鮮命中且不呼叫 `Scan`。
- 執行 `go test -race ./internal/tui ./internal/cli ./internal/repo ./internal/diskusage`，最後 `make`（fmt → vet → test → build）。

### 6. 文件 / skill / changelog（CLAUDE.md 要求）
- `CHANGELOG.md` `[Unreleased]` > Changed：REPOS 漸進載入期間順序穩定、批次更新、SIZE 快取即時顯示；快取多存 worktree 數與 live 提示（僅供呈現，非授權）。
- `docs/guides/tui-repos-bootstrap.md` 「Progressive loading…」（~521）與 `docs/guides/tui-repos-bootstrap.zh-TW.md` 「載入、選單搜尋與多年活動」（~452）：一段說明順序凍結到完成快照、排序鍵變更立即生效、cached live 提示不是 live 狀態、SIZE 首幀讀快取。
- `README.md` ~1409（`O`/`R` 段落）：一句說明每次載入順序只定一次、載入中改排序立即套用。
- `internal/skill/dev-cli/references/command-navigation.md` ~46-50：快取列可能帶 task 數/size 與順序提示，皆不授權動作。skill 為 `go:embed`，改後 `make build`；無 Cobra 變更，`make skill-check` 不受影響。
- 改文件後跑 CLAUDE.md 的 strict docs 檢查（`uv run python scripts/check-docs.py --source`、`mkdocs build --strict`、`--site site`；`docs/llms*.txt` 漂移則 `--generate-llms`）。

## 驗證

1. `make build && DEV_TUI_TRACE=/absolute/new/path.json ./dev`，進 REPOS（106 repos）：狀態列仍顯示「Refreshing local repositories…」期間列不重排；快取 repo 首幀 SIZE 顯示數值而非 `—`；載入中按 `O` 立即重排。
2. 檢查 trace JSON：`tui.view.snapshot_accepted` 次數應降到數十次；`tui.cache.repos_read` 到第一次 `snapshot_accepted` 的延遲增加量不超過 task TOML 讀取時間。
3. `dev self cache clear repos` 後重跑，確認無快取路徑：到達順序凍結、完成時一次重排。
4. `make`、`go test -race ./...`（CI 指令）全綠。

## 風險

- 快取 vs 事實：提示只影響順序；`Pending=="cached"` 仍擋動作、LIVE 欄只由 `Live` 渲染。`CachedLive` 不得進入 renderer 或 capability 檢查。
- cache 階段先等 tasks 會把 task store 讀取時間加到首幀（通常毫秒級）；`TestTUILocalLoaderPublishesCacheBeforeDiscoveryCandidates` 守住順序。
- `sort = "size"` 時完成後的 `sizeMsg` 批次仍會重排，屬該排序本質，批次後次數變少。
- 訊息型別改變，四個測試檔需更新建構方式。
