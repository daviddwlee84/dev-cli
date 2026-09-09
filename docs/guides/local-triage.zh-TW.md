---
description: 找回普通 repository 與 Try 中遺漏的本地工作，預覽批次同步，並在清理 checkout 時保護 ignored 資料。
authority: project
status: evolving
verified_on: 2026-09-09
lang: zh-TW
---

# 本地工作整理

`dev triage` 是獨立的跨 repository 介面。首頁先找回可能遺漏的工作，Tab 切換成依所選操作分組的快速批次檢視。

```bash
dev triage
dev triage --report
dev triage --json
dev triage --kind try
dev triage --root ~/other-projects --stale-days 30
dev triage --all                 # 包含 catalog 保留的歷史位置
```

重新導向輸出時會產生唯讀文字報表。`--json` 提供 schema version 1，包含來源完整度、本機 identity、問題、操作候選、runtime 關聯與整理意圖；既有 `dev ls --json` 維持相容。

## 盤點內容

掃描合併 configured discovery roots、額外 roots、目前 checkout、catalog/task 的本機路徑，以及支援且可用的 runtime 所觀察到的路徑。盤點所有 registered worktree 與本地分支，包括未 checkout 的分支。以 Git common directory 去重，保留 catalog 中 Try checkout 的獨立身分。Tries root 的直接子目錄只做觀察，不會順便登記 catalog。

普通 repo 與 Try 可分開篩選。預設包含本機 active/deprecated Try；`--all` 可查看歷史。Git Try 可參與同步，非 Git Try 仍列出，供個別保存、archive、graduate 或 disposal。

問題保留未提交修改、無 upstream、upstream ref 不可用、ahead/behind/diverged、stash/tags/notes、ignored paths、task drift 與 runtime occupancy。無 upstream 不等於 commits 從未上傳。Runtime 失敗與不完整觀察維持可見，不當成 clean/closed。`--no-runtime` 停用 multiplexer 探測，需要 runtime 證據的 checkout mutation 會被阻擋。

啟動與 `r` 不會 fetch、查詢 forge/fleet、更新 release 資訊、寫入 task/catalog，或啟動 runtime。遠端比較使用本地 cached tracking refs；fetch 是另一個明確核准的操作。

## 檢視與按鍵

首頁優先排列未提交工作，其次是未同步分支與其他待處理問題，最後是閒置候選。同類內較舊的活動排前。預設閒置門檻為 14 天；年齡只影響排序，不證明備份或授權刪除。四格摘要以「待保存／同步工作或其他工作」及「批次候選或需個別處理」分類，不推斷個人重要性。

| 按鍵 | 操作 |
|---|---|
| Tab | 遺漏工作／快速批次 |
| g、/ | 篩選普通 repo／Try；文字搜尋 |
| j/k、方向鍵 | 選擇項目 |
| Space、a、n | 多選切換；選取可見候選；清除選取 |
| f、p、u | 選擇 fetch、push、fast-forward |
| w、c、t | 選擇 Park Warm、Park Cold、Retire |
| A、x | 可用動作選單；Remove Checkout／Try 垃圾桶或忘記 |
| Enter | 建立精確 plan，檢查 effects 與 blockers |
| y | 預覽後核准非移除批次 |
| PgUp/PgDn | 捲動完整預覽 |
| ?、b | 完整項目資訊；上一批結果 |
| o、e、v | 個別 flow／Try 操作；shell；精確 runtime activation |
| L、s、U | 刻意保留本地；暫緩七天；清除意圖 |
| R | 取代此 clone 的可重建目錄清單；空白表示清除 |
| d、r、q | 顯示暫緩項目；重新讀取本地狀態；離開 |

個別 shell 從所選路徑啟動，不切換分支。可使用既有 Git/editor 工具，exit 後重新整理 triage。尚未登錄的 Try 可在核准 Trash 的 Apply 中登錄；其他 catalog lifecycle 操作仍可明確執行 `dev tries list` reconcile。

## 批次核准與執行

每輪只選一種動作：選取、預覽、核准、執行、重新分類。Fetch 後再選 push、快轉或清理，新符合條件的項目不承接舊 approval。候選只是可建立 plan，仍需 fresh guarded plan 才能得到 READY。

- Fetch 更新選定 remote 的 tracking branch namespace，包括 prune 已刪除的遠端分支，不抓 tags，也不遞迴抓取 submodule。
- Push 只把預覽的確切 commit/branch 推送至單一明確 endpoint，不 force、不附帶其他 branches/tags。Triangular/multiple destinations、首次 publish 或 diverged 分支需個別處理。Push 後仍顯示未提交修改。
- Fast-forward 要求精確、乾淨、已 checkout 的分支與 runtime 證據，使用 `--ff-only --no-overwrite-ignore`。Ignored file 碰撞會停止，不切換分支、stash 或 rebase。
- Task 操作沿用既有 mode/state graph。清理保留 branch 與 Git common directory，不移除整個 repo。

Plan 在 effects 前鎖定並重新驗證 repository、refs、checkout、task revision、runtime 與相關內容。真正移除前再檢查 contents 與可重建規則。Triage 批次不關閉 recognized agent，包含 idle/done agent。Canonical、harness-owned、conflicted、locked/prunable 或觀察不完整的 checkout 都不能透過此介面移除。

移除批次必須先檢查確切路徑與 effects，再輸入畫面上的 `CLEAN N`。單項失敗後繼續其他獨立項目，同 repo 串行執行。Apply 中按 Esc，會等目前操作返回後停止剩餘佇列；離開也會等待 ledger。

`<state_dir>/triage/runs/` 保存 completed、skipped、canceled、failed、stale、partial 結果。中斷後留下的 `running` 表示 unknown，需重新觀察，不推定 rollback，也不自動重播。

## 整理意圖與 ignored files

意圖保存在 `<state_dir>/triage/`，不把觀察到的 Git 狀態存成真相。刻意保留本地與暫緩綁定當時 item fingerprint，新 commits、修改或問題會重新提醒；嚴重觀察錯誤仍可見。暫緩永遠不授權清理。

可重建目錄預設為空。`R` 接受 checkout-relative 的明確目錄，例如 `node_modules`、`.venv`，以逗號分隔。這表示你明確同意：核准移除 linked checkout 時，可丟棄這些目錄中的 ignored 內容。規則僅適用於該 clone 及其 worktree，不承接同路徑後來替換的 clone。

其餘 ignored files 仍阻擋移除。預覽列出受影響路徑與大小。路徑逃逸、glob、Git administrative paths、symlink roots、nested repo 或 submodule 不會因規則而被略過；不追蹤 checkout 外的 symlink 目標。缺少穩定 filesystem identity 的平台不能登記此規則或執行 protected cleanup。

完整 repo 的可驗證備份／restore、外部引用分析、永久 repo 刪除與自動 commit/rebase 留待另外處理。目前分支已同步不足以證明整個 clone 可刪。

## 從 REPOS 或 TRY 多選

Dashboard 的 `x` 切換 repo／Try 選取，`Ctrl+A` 選取目前可見項目。篩選保留隱藏的選取，footer 會顯示數量。`Ctrl+O` 提供清除選取與單項整理入口。有選取時 Enter 開啟限定範圍的獨立 triage；沒有選取時保持原本的開啟行為。`o` 永遠開啟目前列，REPOS Space 仍展開 worktrees。選 repo 包含其所有本地分支與 registered worktrees；`A` 顯示可用動作及候選數量，選擇後可取消預選，再按 Enter 預覽。

交接復用 dashboard 已接受的 metadata 與 task／worktree 觀察，只深入檢查選中 repo 的 Git／contents，不重新探索其他 roots。第一輪可復用記憶體 metadata 快照，之後 refresh 重新檢查選中範圍。操作返回後只更新受影響列並失效其 SIZE cache；SIZE 延用既有 10 分鐘快取。本次沒有新增跨啟動的 repo 快照或 watcher，獨立 `dev triage` 啟動／refresh 仍盤點全域。快取不授權 Apply，本地 refresh 不 fetch。Dashboard TRY 的讀取不會登錄目錄或 reconcile move；明確的 CLI lifecycle 操作維持既有 reconciliation。

## Try：垃圾桶或忘記條目

`A` 對存在且獨立的 Try 提供 `trash-try`，對確認遺失的目錄提供 `forget-try`。`x` 選擇對應 Try 動作，普通 checkout 則選 Remove Checkout。活動時間不明顯示 `—`；presence 另外區分 `missing` 與 `unavailable`。存取失敗或 root 不可用，不能授權忘記。

垃圾桶批次要求輸入 `TRASH N`，保留**完整目錄**，包含 ignored／untracked 內容。垃圾桶在清空前仍佔用空間。Task／runtime 佔用、shared／external Git、安全路徑或來源身分／內容變動都會阻擋。垃圾桶不可用時不會改成永久刪除。尚未登錄的 Try 會在預覽明列 Apply 時只登錄所選目錄；取消不寫入，登錄成功而 Trash 失敗時保留 catalog 與剩餘內容，回報部分完成。其他主機位置及既有 Trash recovery 歷史維持保留。

確認遺失的意外 Try 可透過 `FORGET N` 批次完全忘記，也可使用共用 guarded service：

```bash
dev tries forget <ref> --dry-run --json
dev tries forget <id> --confirm-forget <id> --json
```

要求只有本機位置、預期 parent 可存取、目錄確實不存在，而且沒有 task、runtime、agent artifact、筆記來源、其他 catalog 或 recovery 引用。Catalog 的個人 note／tags 也必須先檢閱清除。有第二個主機位置就不能整筆忘記，即使路徑文字相同。Apply 加鎖重讀精確 catalog 記錄，路徑重現或權限依據改變就拒絕。只移除該 catalog 記錄並留下操作結果，不刪專案檔案、筆記 Markdown 或 stats。不提供批次永久刪除，也不刪普通 canonical repo。
