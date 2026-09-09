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
| w、c、t、x | 選擇 Park Warm、Park Cold、Retire、Remove Checkout |
| Enter | 建立精確 plan，檢查 effects 與 blockers |
| y | 預覽後核准非移除批次 |
| PgUp/PgDn | 捲動完整預覽 |
| ?、b | 完整項目資訊；上一批結果 |
| o、e、v | 個別 flow／Try 操作；shell；精確 runtime activation |
| L、s、U | 刻意保留本地；暫緩七天；清除意圖 |
| R | 取代此 clone 的可重建目錄清單；空白表示清除 |
| d、r、q | 顯示暫緩項目；重新讀取本地狀態；離開 |

個別 shell 從所選路徑啟動，不切換分支。可使用既有 Git/editor 工具，exit 後重新整理 triage。尚未有 catalog 身分的 Try，需明確執行 `dev tries list` reconcile，才進行 catalog lifecycle 操作。

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
