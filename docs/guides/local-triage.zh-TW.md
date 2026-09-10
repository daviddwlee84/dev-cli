---
description: 找回普通 repository 與 Try 中遺漏的本地工作，預覽批次同步，並在清理 checkout 時保護 ignored 資料。
authority: project
status: evolving
verified_on: 2026-09-09
lang: zh-TW
---

# 本地工作整理

`dev triage` 是獨立的跨 repository 介面。首頁按 repo／Try 摘要找回遺漏工作，再選動作。

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

獨立 triage 從「選項目」開始，每個 repo／Try 一列，顯示未保存、待同步、佔用與待確認徽章，展開才列出分支／checkout。勾選父列涵蓋所有底下工作；子列可個別取消，部分選取顯示 `[-]`。顏色搭配符號與短文字，`--color never`／`TERM=dumb` 仍能辨識狀態。

| 輸入 | 操作 |
|---|---|
| Space／點 checkbox | 切換目前項目或群組選取 |
| Ctrl+A（或 a） | 全選篩選結果；已全選則取消，保留篩選外選取 |
| 左右方向鍵／展開符號 | 收合／展開 repo |
| Enter／點列 | Enter 看詳細資訊，點列只聚焦 |
| Ctrl+O／A／Choose action 按鈕 | 對明示的目前項目或所選範圍挑選動作 |
| Tab | 聚焦主要操作按鈕 |
| /、g、d | 文字篩選、repo／Try 篩選、暫緩項目 |
| n | 清除選取 |
| o、e、v | 個別 flow、shell、runtime |
| L、s、U、R | 保留本地、暫緩、清除意圖、disposable 目錄 |
| r、b、?、q | 本地 refresh、上次結果、完整說明、返回 |

右鍵開啟該列動作，滾輪捲動列表／詳細資訊。流程固定「選項目 → 選動作 → 預覽 → 結果」，挑選動作只建立 plan；精確核准後才執行。結果頁先顯示完成／失敗／略過數量及短原因，Enter 才展開診斷、下一步、exact effect 和 receipt。`e` 開啟該結果目標的 shell；`Ctrl+O` 選新動作時一定重新 plan。舊 receipt 沒有診斷時會明示無法還原原因。

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

## 從 REPOS 或 TRY 進入

Dashboard 維持瀏覽：Enter／`o` 永遠開啟目前項目，Space 保留原功能，不再常駐勾選框或保留整理選取。`Ctrl+O` 可選「整理目前項目」「整理目前篩選結果」「整理全部本地工作」。Triage header 明示範圍，先顯示項目供選取。

交接繼續復用 dashboard metadata 與 task／worktree 觀察，只深入檢查限定範圍，返回只刷新受影響列／SIZE。既有 SIZE 10 分鐘 cache 不變，沒有新增跨啟動 inventory cache 或 watcher。獨立 `dev triage` 仍盤點全域，本地 refresh 不 fetch，也不登錄 Try。

## Try：垃圾桶或忘記條目

`A` 對存在且獨立的 Try 提供 `trash-try`，對確認遺失的目錄提供 `forget-try`。動作選單明確標示各操作。活動時間不明顯示 `—`；presence 另外區分 `missing` 與 `unavailable`。存取失敗或 root 不可用，不能授權忘記。

垃圾桶批次要求輸入 `TRASH N`，保留**完整目錄**，包含 ignored／untracked 內容。垃圾桶在清空前仍佔用空間。Task／runtime 佔用、shared／external Git、安全路徑或來源身分／內容變動都會阻擋。垃圾桶不可用時不會改成永久刪除。尚未登錄的 Try 會在預覽明列 Apply 時只登錄所選目錄；取消不寫入，登錄成功而 Trash 失敗時保留 catalog 與剩餘內容，回報部分完成。其他主機位置及既有 Trash recovery 歷史維持保留。

確認遺失的意外 Try 可透過 `FORGET N` 批次完全忘記，也可使用共用 guarded service：

```bash
dev tries forget <ref> --dry-run --json
dev tries forget <id> --confirm-forget <id> --json
```

要求只有本機位置、預期 parent 可存取、目錄確實不存在，而且沒有 task、runtime、agent artifact、筆記來源、其他 catalog 或 recovery 引用。Catalog 的個人 note／tags 也必須先檢閱清除。有第二個主機位置就不能整筆忘記，即使路徑文字相同。Apply 加鎖重讀精確 catalog 記錄，路徑重現或權限依據改變就拒絕。只移除該 catalog 記錄並留下操作結果，不刪專案檔案、筆記 Markdown 或 stats。不提供批次永久刪除，也不刪普通 canonical repo。
