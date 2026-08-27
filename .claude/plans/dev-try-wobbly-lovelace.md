# `dev try` TUI、asset metadata 與安全回收

## Context

目前 `dev try`（`internal/cli/try.go`）只掃描 `paths.tries_root` 的第一層目錄，以 directory mtime 排序，並把 create/clone/open/render 全放在 Cobra handler；`dev graduate`（`internal/cli/graduate.go`）也自行處理 resolve、move、Git 初始化與 remote。這足以快速開實驗，卻無法可靠回答「最近用過什麼、哪些值得保留、哪些可安全釋放空間」，也沒有能在 local directory 被移除後繼續存在的索引。

目標是把 TRY 作為主 TUI 的第四個 view，同時保留 `dev try <name>` 的低摩擦與可腳本化 CLI。資料與安全規則放在共用 domain service，不放進 Bubble Tea；普通 repository 與 Try 共用 tags／notes／穩定 ID，但只有真正的 Try entry 具有 Try lifecycle。容量預設代表刪除該 local checkout 真正擁有、可回收的 logical bytes，而不是含糊地選擇是否「扣掉 `.git`」。

採用以下產品語意：

- `active`／`deprecated`／`graduated` 是人為 lifecycle phase。
- `present`／`archived`／`evicted` 是每台機器的 local disposition；archive 不等於刪除，evict 不等於 deprecated。
- `important`、`keep`、`experiment` 等 tags 可組合；普通 repo 的 `experiment` tag 只分類，不會建立 Try lifecycle。
- Git tries 預設只顯示於 TRY，避免與 REPOS 重複；可用 `--include-tries`／TUI「顯示全部」查看。Graduated entry 保留歷史與 ID，並改在 REPOS 顯示。
- Archive 是 `TriesRoot` 內同 filesystem 的可逆隱藏搬移；evict 才釋放空間。Remote create/push 永遠需要明示，且「有 remote」不等於「資料可恢復」。

## 1. 建立共享 asset catalog 與安全 path primitives

新增 `internal/catalog`，不要擴充 `task.Task`：Try 可以是 non-Git，也沒有 branch/task/runtime，而 `task.State` 的 hot/warm/cold/done 是另一種生命週期。

- `Entry` 以隨機穩定 ID（檔名）保存 `schema_version`、kind (`try|repository`)、name/note/tags、可選的 `Experiment` metadata、timestamps、per-host `Location`、已驗證的 `RecoveryReceipt` 與短暫 `MoveIntent`。
- `Experiment` 保存 phase、slug/origin、deprecated/graduated timestamps 與 from/to；`Location` 保存 host、disposition、目前/還原 path 與 changed time。
- Store 位於 `Config.AssetsDir()`（`<state_dir>/assets/<id>.toml`），沿用 `task.Store` 的一筆一檔、temp+close+chmod+rename 原子更新與壞檔隔離模式；diagnostics 由 API 回傳或注入 logger，不直接寫 `os.Stderr`。
- Registry 提供 `List/Get/Update/FindByPath/EnsureRepository/Attach/Patch`。普通 repo 只在第一次 mark/note/track 時建立 entry，不為所有 scan roots 預先落盤。
- Match 先用 host + canonical path／Git common-dir；remote identity 只作 relocate 提示，遇到同 remote 多 clone 時不得自動合併。
- Locations 按 `config.Hostname()` 分開，讓 syncable state 不會把「A 機已 evict」誤當成所有機器都沒有 checkout。
- Live Git status、runtime、size 不寫入 catalog；recovery receipt 只記錄某次 backup 驗證的 remote、HEAD/ref digest 與時間，apply 前仍須重驗。

新增 `internal/pathx` 給 graduate/archive/reclaim 共用：以 `filepath.Abs/Clean/Rel` 與可解析部分的 `EvalSymlinks` 做 separator-aware containment，拒絕 root 本身、`..`、symlink escape 與 `/tries/foo` 對 `/tries/foobar` 的 prefix 誤判；category/name 僅允許單一 path component。

## 2. 抽出 `internal/experiment`，讓舊 CLI 與 TUI 共用

把 `internal/cli/try.go`、`internal/cli/graduate.go` 的 policy 移入 `experiment.Service`；Cobra 只解析 flags、呼叫 service、render 結果。核心 API 包含：

- `Reconcile/List/Resolve/ResolveOrCreate/Touch`
- `Deprecate/Reactivate/Archive/Restore/Graduate`
- request/result types 明確描述 dry-run plan、move、catalog transition 與需要重新載入的 inventory。

Reconcile/backfill 規則：

1. 掃描 `TriesRoot` 第一層並跳過隱藏目錄（含 `.dev`）；首次看到既有目錄時配置 ID，不搬、不改名、不寫 repo sidecar。
2. 日期前綴只用來回填 `CreatedAt`；不再把 directory mtime 當 last-used。Open 成功後 `Touch`，列表 activity 取 `max(LastOpenedAt, last Git commit)`，都未知就顯示 unknown。
3. Resolve 順序為 exact ID、完整 basename、去日期後完整名稱、唯一 substring；多個 substring 必須回 ambiguity，不再靜默挑最新。
4. Clone 預設採 `owner-repo` slug；同日碰撞以 `-2/-3` versioning，不覆寫也不誤開另一個 Try。
5. 外部手動搬移且無唯一身份時，以 `dev tries attach <id> <path>` 明確重新連結。

Lifecycle filesystem 行為：

- `deprecate/reactivate` 只改 phase，不搬檔。
- `archive` 搬到 `<tries_root>/.dev/archive/<id>/<basename>`，記住 restore path；`restore` 拒絕覆蓋已存在目的地。
- `graduate` 保留 ID/tags/note/Experiment history，將 kind 改為 repository、更新 location；normal directory 用 `os.Rename`，linked worktree 新增並使用 `gitx.MoveWorktree`（`git worktree move`）。MVP 對跨 filesystem move 明確拒絕。
- Non-Git Try 的 Git init/initial commit 在搬移前完成，失敗時仍留在原處；remote create/push 在本機 graduation 成功後才做，失敗不謊稱 remote 成功，也不回滾已完成的本機 graduation。
- Archive/graduate 先寫 `MoveIntent`、執行 move、再 finalize；`Reconcile` 根據 from/to 實際存在性修復中斷狀態。Catalog finalize 失敗時嘗試搬回並回報。
- Evicted tombstone 保留最後 path、phase、tags、remote/recovery receipt 與時間；首版不自動 GC/forget catalog history。

## 3. 保持 CLI 相容並加入管理入口

保留既有語法與行為：

- `dev try [name] [--clone URL] [--no-git]`、`dev try --list`
- `dev graduate [try]` 與現有 flags

新增 sibling `dev tries` group，避免把今天合法的 `dev try archive` 從「開啟名為 archive 的 Try」變成 subcommand：

- `list [query] [--all] [--json] [--sizes]`
- `open/touch/attach/mark`
- `deprecate/reactivate/archive/restore`
- `graduate`（與 top-level `dev graduate` 共用同一 service）
- `backup --remote <name> [--push] [--create --private]`
- `evict <ref...> [--apply]` 與明確 data-loss acknowledgements

Repository 共用 metadata：

- 在現有 `dev repo` group 加 `mark <repo> --add/--remove/--note`。
- `repo list` 加 `--include-tries`、`--json`、`--sizes`，透過 catalog join 顯示 tags/mark；不改 `repo.Discover` 的通用掃描與 common-dir 去重。
- 新增 `dev reclaim` 作跨 Try/repo 的 read-only 候選報告（age/tag/size/kind filters）；只有 `--apply` 才執行，且共用 `reclaim.Service`。
- `important` 置頂並加強 destructive warning；`keep` 排除預設 reclaim 候選且需 `--override-keep`；`task.Task.Tags` 維持 task/change-stream metadata，不自動同步。
- JSON 使用可向後擴充的 nested `asset`、`size`、`reclaim` objects；未知 size 用 `null`，不以 `0` 冒充。

## 4. 實作一致的容量模型

新增 `internal/diskusage`；在 `gitx.Repo` 的同一次 `rev-parse` 補取 checkout-specific `GitDir`，搭配既有 `GitCommonDir/Key/MainRoot/IsLinkedWorktree` 判斷 ownership。

`Usage` 至少包含：

- `checkout_bytes`：checkout root 內 logical `Lstat.Size()`，跳過 root `.git` entry；不 follow symlink，symlink 只算自身，sparse file 以 logical length 計。
- `private_git_bytes`：只屬於此 checkout、刪除此 checkout 可回收的 Git admin data。
- `shared_git_bytes`：shared common-dir 的非重疊 logical bytes，只供 detail/context，不歸到每個 worktree。
- `owned_bytes = checkout + private_git`：TRY/REPOS `SIZE` 欄的預設值。
- `total_bytes`：僅在 standalone ownership 完整、沒有 shared ambiguity 時提供。
- `complete/unreadable_entries/measured_at/cached`；partial 顯示為 `≥N`，不可包裝成精確值。

Ownership 規則：standalone clone 且 common-dir 位於目標內時，`.git` 算 private；linked worktree 只算自己的 checkout 與可唯一歸屬的 gitdir，common objects/refs 算 shared；main checkout 仍有 linked worktrees或 external git-dir 時，common-dir 不算其 reclaimable bytes。各欄位不得重複計數。

效能與快取：

- Portable Go walker、context cancellation、最多 2 個背景 scanner；列表/TUI startup 不同步遞迴所有 repo。
- `$XDG_CACHE_HOME/dev/sizes-v1.json` 使用 versioned key（canonical checkout/git/common dirs）與原子 `0600` 寫入，TTL 10 分鐘；nested 變更主要依 TTL，不假裝 root mtime 能完整 invalidation。
- Create/clone/archive/restore/graduate/evict、外部 TUI tool 返回後精準 invalidate；`r` 強制重測當前 local view。
- TUI 先套 fresh cache，再以 `loadID + sizeMsg` 流式逐列更新；切 view/reload 時 cancel 舊 batch，晚到訊息因 load ID 不符而忽略。每個 `sizeMsg` 後排下一個 channel receive command，避免阻塞 Tea loop。

## 5. 安全 backup／reclaim／evict

新增 `internal/reclaim`，將 inspect/plan、backup、evict、restore 與 finding codes 放在 domain；CLI/TUI 只呈現 plan 並收集明確意圖。`Apply` 必須在真正 move/remove 前重新 inspect，不能信任幾秒前的畫面。

Git preflight 除了重用 `gitx.StatusOf/Discover/Worktrees`，新增 `gitx.RecoverySnapshot` 檢查：

- staged/unstaged/untracked/conflicted 與 merge/rebase/cherry-pick/bisect 等進行中狀態；
- ignored files（尤其 `.env`、dependencies/build artifacts）及 unreadable content；
- local heads/tags/notes、stash，以及指定 remote 的實際 refs/OIDs；
- linked/locked/prunable worktrees、shared common-dir ownership；
- submodule、nested Git repo、Git LFS；首版無法完整證明時直接阻擋 safe eviction；
- catalog `keep`、未完成 task、live runtime、目前 cwd 位於 target、alias/symlink containment。

操作語意：

- `backup --push` 明確推送選定 heads/tags/notes，再以 `ls-remote` 驗證並寫 receipt；stash 不自動藏到特殊 ref。`forge.CreateRepo(...Push:true)` 不能單獨當完整 backup proof。
- Standalone Git 只有在 worktree clean、ignored 已處理、所有受保護 refs 已由指定 remote 驗證且無 blocker 時可 safe-evict。
- Linked worktree 若 clean 且 branch/commit 仍由可用 shared common-dir 保留，使用 `git worktree remove`；永不刪 branch/common-dir。Main clone 仍有 linked worktree 時阻擋整個 clone eviction。
- Non-Git 預設只能 archive；要永久丟棄須明示 `--discard-without-backup`。Dirty/untracked/ignored/keep 各自使用名稱精確的 `--discard-dirty`、`--discard-ignored`、`--override-keep`，不提供含糊 `--force`，`--yes` 也不能代替 data-loss flags。
- 普通 directory eviction 先寫 intent、rename 至同 parent staging name、再 remove；這提供可 reconcile 的 metadata，不宣稱未備份資料在中途刪除後可復原。Safe eviction 已有 remote receipt；unsafe discard 明確顯示不可恢復。
- 批次操作預設 report-only；apply 前顯示每筆 canonical path、owned bytes、recovery source、blockers/warnings，要求完全大寫 `YES`。只有命令列明確列出 refs 時 `--yes` 可略過互動，廣泛 filter 不可靜默批次刪除。

首版刻意不做：跨 filesystem copy-verify-delete、non-Git/dirty 自動 tar/object-store backup、LFS/submodule/nested-repo 完整備份、allocated blocks/APFS clone/hardlink dedup、daemon/watch/automatic GC、remote repo deletion或 forge archive。

## 6. 整合主 TUI，而不是建立第二套 TUI

在既有 `internal/tui` model 增加 `ViewTries`，順序為 `TASKS | REPOS | TRY | REMOTE`；保留 header/filter/window/footer、runtime handoff、external tools 與 lazy REMOTE。Try-specific row/render/update 拆到獨立檔案，避免繼續膨脹 `model.go`/`view.go`。

- `TryRow` 帶 catalog summary + live experiment/Git/runtime/size facts；Model 增加 `tries`, `tryCursor`, per-view visibility/sort state，並讓 `currentDir()` 只對 present Try 回傳路徑。
- 將新增 callbacks 分組為 `TryActions`、`SizeActions`、`ReclaimActions` 嵌入 `Actions`，不要再加一長串平坦欄位；`internal/cli/tui.go` 注入同一批 experiment/catalog/reclaim services。
- 使用 `triesMsg/reposMsg/sizeMsg/tryActionMsg` 做 targeted refresh。Graduate/archive/restore/evict 更新 TRY + REPOS + local remote matching，但絕不因此重新查 gh/glab。
- TRY table 顯示 mark/name、phase·where、Git、last activity、`SIZE=owned_bytes`；detail 分列 checkout/private/shared Git、tags/note、origin/recovery 與 reclaim findings。REPOS 加 mark/size，窄畫面優先隱藏較次要 WT/task tally，不隱藏名稱/Git/size。
- 預設排序：important/keep、activity、name；提供 activity/size/name 切換。Reclaim overlay 固定按 eligible owned bytes 由大到小。
- 擴充 filter 為一般 AND terms 加 `tag:`、`phase:`、`where:`、`reclaimable:`、`size:>…`；`a` 採 view-specific「顯示全部」（TASKS=done、REPOS=tries、TRY=archived/deprecated/graduated/evicted）。
- 新增可重用 full-screen overlay/form state，沿用 stats overlay 的模式；create/graduate/backup/action palette 與 reclaim multi-select/review/exact-YES 不再塞進現有單一 `textinput` mode switch。
- 實作已被 reserved/docs 宣告但目前缺少的 `?` help overlay，集中顯示 context keys；這是新 view 可發現性的一部分。現有 cold-task Enter 文案落差與 taskflow callback 重構留在獨立工作，不混入本功能。
- REMOTE local matching 同時考慮 repository 與 Try，標示 `LocalKind`；即使 Git Try 預設不在 REPOS，也不能被 REMOTE 誤顯示成未 clone。

## 7. 實作順序

1. **Catalog/path 基礎**：`Config.AssetsDir`、schema/store/registry、canonical containment、multihost locations、tests；`App.Load` 初始化 catalog。
2. **Experiment vertical slice**：backfill/resolve/create/open/touch，讓既有 `dev try` 成為薄 adapter，所有舊語法與測試先保持通過。
3. **Lifecycle + graduate**：deprecate/archive/restore、move journal、`gitx.MoveWorktree`、graduate service；top-level `dev graduate` 與新 `dev tries graduate` 共用。
4. **Management/annotations**：`dev tries` group、`repo mark`、catalog inventory join、REPOS suppression/REMOTE LocalKind、JSON contracts。
5. **基本 TRY TUI**：第四 view、open/create/mark/deprecate/archive/graduate、help/form overlays與 targeted reload，先交付可用的互動流程。
6. **Disk usage**：Git ownership、scanner/cache/CLI flags，再接入 TRY/REPOS streaming size、filter/sort/detail。
7. **Reclaim**：RecoverySnapshot、backup receipts、report-first CLI、safe evict/restore與 explicit discard；最後接入 TUI multi-select review/apply。
8. **文件/e2e**：README、`internal/help/topics/tui.md`、bundled skill/command reference、reserved keys、`scripts/e2e.sh`。

目前 Go 基線已包含 direct tasks、rich Git status、heatmap與 config reload（HEAD `90276d5`）；直接建立其上，不重做已解決的 config wiring，也不回退那些型別。現有 `.specstory` 修改/未追蹤檔不屬於本功能，實作時不得覆寫或混入功能變更。

## Verification

逐 slice 驗證，最後做完整 end-to-end：

- `go test ./internal/catalog ./internal/pathx ./internal/experiment`：UUID/host locations、atomic/corrupt store、idempotent backfill、exact/ambiguous resolve、owner-repo collision、symlink/`..`/sibling-prefix containment、archive/restore/graduate ID 保留、move intent recovery、cross-filesystem refusal。
- `go test ./internal/gitx ./internal/diskusage ./internal/reclaim`：所有 dirty states、ignored `.env`、額外 local branch/tag/note/stash、remote OID mismatch、receipt invalidation、linked/main/shared worktrees、LFS/submodule/nested repo、preflight-to-apply race；`.git` dir/file/external gitdir、private/shared non-overlap、symlink/unreadable/partial/cancel/cache TTL。
- `go test ./internal/cli ./internal/tui`：`dev try archive` 仍是 Try 名稱、舊 clone/no-git/graduate 相容、JSON null/units、Try 僅在正確 view、每 view cursor/filter/sort、present-only tool handoff、targeted refresh、晚到 size load 被忽略、forms/multi-select/blockers/exact `YES`、CJK/窄 terminal rendering。
- `go test -race ./internal/diskusage ./internal/tui`，再執行 `go test ./...` 與 `go vet ./...`。
- 擴充 `scripts/e2e.sh`，只用隔離 HOME + local bare remote：建立 Git/non-Git tries、touch/list、archive/restore、graduate、push+verify+evict+clone restore，並確認 dirty/ignored/unpushed/default non-Git deletion 都被拒絕；不碰真實 forge。
- 最後以隔離 config 啟動實際 TUI，確認首畫面不等容量掃描、cache 先顯示、背景逐列更新、TRY/REPOS/REMOTE mapping、完整 reclaim review 與取消流程。

## Critical files

- 新增：`internal/catalog/{entry,store,registry}.go`、`internal/pathx/contain.go`
- 新增：`internal/experiment/{service,inventory,lifecycle}.go`
- 新增：`internal/diskusage/{usage,scanner,cache,manager}.go`
- 新增：`internal/reclaim/{plan,recovery,service}.go`
- CLI：`internal/cli/{app,root,try,tries,graduate,repo,tui}.go`
- Git/repo：`internal/gitx/{repo,status,worktree,recovery}.go`、`internal/repo/discover.go`
- TUI：`internal/tui/{model,rows,view,try,overlay}.go`
- Tests/docs：對應 package tests、`internal/cli/cli_test.go`、`internal/tui/tui_test.go`、`scripts/e2e.sh`、README/help/skill references
