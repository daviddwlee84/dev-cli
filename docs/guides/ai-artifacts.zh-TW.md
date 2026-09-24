---
description: 選擇 agent 歷史的保存位置、保留有效決策，並避免聊天進入原始碼發行包。
lang: zh-TW
authority: project
status: evolving
minimum_version: v0.2.33
verified_on: 2026-09-24
---

# AI 產物：保存、封存與發行

保留有用的決策，選擇聊天保存位置，讓發行包只包含產品需要的內容。
離線可讀 `dev help ai-artifacts`，`dev help artifact` 與
`dev help specstory` 也會開啟同一份指南。

## 依用途決定

| 內容 | 用途 | 常見做法 |
|---|---|---|
| Code、tests、目前有效的 spec 與 docs | 實際行為與契約 | 隨專案維護 |
| 經審閱的 plans、決策與 pitfalls | 需求、理由與經驗 | 精簡保存，標示狀態與背景 |
| Raw transcripts、草稿與工具輸出 | 開發過程的證據 | 每個 repo 選擇進 Git、外存或只留本機 |
| Native sessions 與工具資料庫 | Resume、索引或知識整理狀態 | 依各工具的備份契約處理 |

AI 作者身分本身不決定檔案的用途。有價值的結論整理到既有 docs、
decisions、backlog 或 pitfalls。舊 transcript 是歷史證據，不是目前的
操作指令；不應要求維護者重讀全部聊天才能理解程式。

## 四個獨立選擇

1. 內容是否進 source repo 的 Git history？
2. 可長期保存的副本在哪裡，其他機器如何取得？
3. 保存前要檢查、脫敏，或明確選擇不掃描？
4. 內容是否應進入原始碼發行包或安裝成品？

Private 快速實驗可保留原稿；公開或長期專案可把審閱後的知識放在
source repo，聊天放在另一個 private Git repo。Private 目的地不會自動
改變 hygiene policy；未掃描的副本是保留下來的證據，不代表掃描通過。

## SpecStory：紀錄與發布分開

目前的 transcript 整合是 SpecStory。`codex:<uuid>` 或 `claude:<uuid>`
描述原始 agent，SpecStory 是 recorder。Markdown 匯出不等於 native
session，也不保證能 resume。

SpecStory 可在各 worktree 的 `.specstory/history/` 寫入，或明確指定
`--output-dir`。合併 histories 時保留 project/session 身分；單一平面
目錄不會自動保留專案歸屬。Lore 等工具另有 metadata 與知識整理紀錄，
不能假設所有資料庫都可由 Markdown 重建。

預設 `product-first` handoff 要求 product changes 已 commit 且 index 為空，
不會 stage 仍在變動中的 transcript：

```bash
dev git hygiene status
dev git hygiene scan --scope staged --json
dev agent artifact prepare --session codex:<uuid>
# 精確的 recorder 停止後，從 checkout 外執行：
dev agent artifact finalize --intent <id> --writer-stopped
```

Prepare 不會停止 writer。不要反覆脫敏仍被 recorder 從 native source
重寫的檔案。`dev git hygiene redact` 使用審閱後的精確 plan；artifact 修改
還需 writer 結束的證據。暫時穩定的 bytes 不能證明 process 已退出。

Hygiene redact／repair-encoding／restore／manage 與 artifact finalize／archive／
migrate 共用 artifact writer guard。涵蓋此 checkout 的其他已辨識 live agent
一律阻擋，包含 idle／done。只有 Herdr 回報呼叫者的確切 session ID（`agent_session`
kind 為 `id`，不是 title），且每個目標 artifact 實際由 SpecStory 產生的固定
preamble 都含合法且不同的 UUID 時，才豁免呼叫者。這項證明只適用 `.specstory/history/*.md`，不採信檔名或
內文後段提到的 UUID。Plans 或無法證明歸屬的 transcript 需全域
`--allow-shared-checkout`，明確聲明 writer 已退出且檔案歸屬互不重疊；不可自動
加上它重試。呼叫者身分未知時仍需這項明確聲明；已識別為呼叫者自己的 live
transcript 即使有 override 也拒絕。
Writer 已結束的證據與來源重新驗證仍不可省略；restore 檢查 receipt 的確切路徑。

Ignored history 不必參與一般 code commit 的掃描；但已 tracked 的檔案
不會因新增 `.gitignore` 就停止追蹤。移出 index 是獨立操作。刪除
worktree 前保留或封存 history；ignored 不等於可丟棄或已備份。

## 精確選取與 co-commit closeout（v0.2.40）

`dev agent artifact prepare --specstory-path PATH` 可在同一 session 有多份匯出時選取一個精確
Markdown 檔案。保存的選取必須符合嚴格 SpecStory preamble 中的 provider 與
UUID，且位於設定的 capture 範圍內：source commit 使用 `.specstory/history/`，
archive 則使用該 policy 的 capture root。路徑穿越、symlink 與身分不符都拒絕。
省略 selector 時，有歧義仍會阻擋，不猜最新檔案。Finalize 重新檢查選取與 live
writer guard；`--writer-stopped` 不可覆蓋 live writer。Native intent 更新使用
鎖內精確 revision 的 compare-and-update（CAS），archive handoff 亦同。
`artifact finalize --revision HASH` 可要求與審閱時的 native revision 完全一致。

Co-commit 是明確選用的 product-first 替代流程，不是新預設。它把審閱後 staged
的 product changes 與最終 transcript／可選 plan 放進同一個正常 commit。
目前只支援 `claude:UUID` 與 checkout 內的 SpecStory capture，不適用 external
archive。Canonical `agent-history-hygiene` v2 原始碼在 `agent-skills` 獨立維護；
安裝 dev 不會內附、安裝或升級 helper。Co-commit 要求已安裝的 helper 具備相容
v2 capabilities。工具缺失、不支援的 helper capability 或 helper／tool fingerprint
改變都會阻擋，不自動安裝。Native Windows co-commit 尚不支援，等待經驗證的
native backend；既有 v1 journal 不會默默升級。

1. 明確啟動真實的 canonical v2 wrapper，**不要加 `--allow-commit`**，保留由
   外部手動 finalize。Dev 不啟動或關閉 agent；自行編造環境變數、session ID 或
   receipt 不能取代真實 wrapper lifecycle。
2. Index 只 stage 已審閱的 product files，不含 transcript 與選定 plan。準備不含
   managed provenance trailers 的 base message file；選一個 `--plan`，或明確
   指定 `--no-plan`。
3. 在該 wrapper 啟動的 agent 內排入精確 handoff：

   ```bash
   dev agent artifact prepare --closeout co-commit --session 'claude:<uuid>' \
     --specstory-path .specstory/history/session.md \
     --plan .claude/plans/task.md --message-file /path/to/commit-message.txt --json
   ```

   只有要明確選取已安裝的 canonical helper 時，才加
   `--closeout-helper /path/to/agent-history-hygiene/scripts`。新增未 tracked 且
   大於 2 MiB 的 transcript 需審閱後以 `--allow-large` 同意。
4. 讓真實 recorder／session 保留命令輸出。JSON 使用 schema 1、kind
   `co_commit_handoff`，並保留 canonical v2 `queue_ack`；human output 則把 ACK
   保留為一行完整、緊湊的 JSON。這是真實 queue 證據，不可由 agent 重建。
   回報 **「finalization queued」** 後正常退出，**不再執行任何 repository 或
   index 操作**。`queued_but_not_bound` 也必須退出：保留 request ID，之後從外部
   以相同選取與 `--run-id` 修復該次 binding，不新增 queue request 或 ACK。
   Queue 結果未知時先檢查，不盲目重新排入。
5. 真實 wrapper 完成後，由外部 coordinator 審閱 handoff 並明確授權 finalize：

   ```bash
   dev agent artifact list --json
   dev agent artifact finalize --intent '<id>' --allow-commit --json
   ```

   可加 `--revision HASH` 綁定 native revision。Dev 先委派 canonical prepare-only，
   再重新檢查自己的 live writer／policy／source guards 與 native CAS revision，
   最後以精確 helper journal revision 綁定一次可 commit 的 helper 呼叫。Commit
   結果不確定時只允許 reconciliation，不重試 commit。`committed_but_not_recorded`
   需修復 receipt 紀錄，不再 commit。證明包含精確 parent、prepared tree、完整
   normalized message 與唯一 request 身分，不只檢查 trailers。

V2 helper 預設在 run、sync 與 export 全程使用 no-cloud。Finalize 需要 child
成功退出、process group 已靜止，以及精確 native export digest 與 request／session
關聯；idle、mtime 或暫時穩定的 bytes 都不足以證明。所有可掃描的 staged product
都以唯讀方式檢查；sanitation 只修改選定 artifacts。修改前先持久保存 private
beforeimages 與 receipts，綁定每個 finding 的所有 occurrences，以及 source／
index／tool／policy 身分。

Sanitation review 被阻擋時，先唯讀檢查：

```bash
dev agent artifact finalize --intent '<id>' --preview-review --json
```

Preview 不可搭配 `--allow-commit`、`--review-file` 或 `--rotation-confirmed`。
Raw findings 與 recovery 保留在 Git 外的 private state。逐項明確審閱後，外部
finalizer 可使用 `--allow-commit --review-file /absolute/path/to/review.json`，
並可附 preview 的 `--revision`。`reviewed_noncredential` 不等於實際憑證輪替：
fixture review 只解除該次已完成 sanitation 的精確 run 之 rotation gate，不會還原
secret bytes、建立全面豁免或繞過 Git hooks。只有實際完成必要憑證輪替後，才使用
`--rotation-confirmed`；unknown 或不完整 findings 仍阻擋。本流程不自動授權復原
任何既有真實 run。

`artifact list --json` 使用 schema 1、kind `artifact_handoffs`。每個 row 將
已保存 native Intent 欄位與 `co_commit_observation` 或 `observation_error`
分開。List、status 與 lifecycle readiness 都是唯讀；helper 觀測不會默默 finalize
或 reconcile native 紀錄。即使 runtime 顯示 done，不完整或失敗的證明仍是 blocker。

## 選擇並套用 repository policy

沒有 policy 時維持既有行為。`track` 使用原本的 source commit finalizer；
`archive` 把證據存進另一個 Git checkout；`unmanaged` 由使用者自行管理。
來源是 `specstory` Markdown，或明確選取匯出檔／plan 的 `files`。

```bash
# 先用一般 Git 建立 archive，設定好 Git identity。
git init -b main /path/to/history
# 明確選擇 private 原稿保存；也可選 check 或 redact。
dev agent artifact setup --mode archive --source specstory --archive /path/to/history \
  --protection off --json
dev agent artifact setup --apply --plan <id> --yes
dev agent artifact status
# Repository setup 使用同一個 planner：
dev repo setup --artifacts --mode unmanaged --json
```

Setup 只修改審閱過的 policy、本機 binding、ignore 與 export 規則，不會
untrack、commit、push 或安裝工具。`repo setup --artifacts` 與 scaffold 是
分開的 plan；可用 `--artifacts --apply --plan <id> --yes` 套用。共享 policy
變更由使用者審閱後自行 commit。

`.dev-cli/artifacts.toml` 保存穩定 project ID、策略、來源與 literal 路徑；
archive 路徑及保護設定留在 private host state。每台機器為 clone 綁定想用的
archive，不以 folder basename 或目前 branch 猜測 project 身分。

`--capture project` 沿用每個 worktree 的 recorder 目錄。`--capture external`
則設定 checkout 專屬、repo 外的 SpecStory 目錄，需要未 tracked 的 native
config，且本機 Markdown 匯出已啟用。Tracked 或複雜 config 需人工審閱；每個
新的 external-capture checkout 都要 setup。Cloud 設定與 native session
儲存仍由 SpecStory 管理。

## 封存副本與查找

```bash
dev agent artifact archive --session codex:<uuid> --json
# 私下審閱 proposal，recorder 結束後：
dev agent artifact archive --apply --plan <id> --yes --writer-stopped
dev agent artifact find --session codex:<uuid>
dev agent artifact find --commit <full-source-commit-id>
dev agent artifact find 'literal search text' --all
```

`--source files` 使用可重複的 `--file` 選取已設定範圍內的精確檔案。盤點上限
10,000 個檔案，每次最多選 256 個；目前 snapshot 上限每檔 128 MiB。過大或
不安全的輸入會明確失敗。Search 讀取 archive 目前 branch 的 committed
records；`--all` 包含其中其他 project。輸出只有 metadata、位置與行號，不顯示
snippet，不 fetch、不遍歷 native agent 目錄，也不 resume agent。

Archive 的 `projects/` 保存普通檔案與 metadata，關聯 project/session、原稿
及保存版本的 digest、source commit。原檔與 source index 不變。專案 archive
的 `.gitattributes` 停用 snapshot/record 的 text 與 ident 轉換，保留 CRLF
及原始 bytes；加密、LFS 或編碼 filters 需走該 archive 的 native workflow，
不會默默繞過。既有 Git hooks 仍會執行。Archive 的存取權與異機備份由使用者管理。

`off` 不要求 scanner，原樣保存；`check` 使用 source repo 已啟用的 hygiene
policy；`redact` 提出副本替換並重新檢查結果。停用的類別仍停用，skipped 不等於
clean。阻擋 report 提供安全 finding ID，可用既有 `dev git hygiene rules allow`
流程處理；fixture exception 仍需精確範圍、審閱與理由。Snapshot 支援 inline
規則及 useDefault，不支援外部 gitleaks rule-file includes。

只有 bytes、路徑、policy、exceptions、scanner inputs 與 executables 一致，
才重用有簽章的本機掃描結果。內容或規則改變就重掃，不完整掃描不能通過。原稿、
副本與 recovery 留在 Git 外的 `paths.state_dir`，不隨 cache clear 刪除。
檔名與 metadata 仍可能識別個人或系統，分享前應審閱。

Product／policy changes commit 後，`dev agent artifact prepare` 也支援 archive policy。
Off/check 可在 writer 結束後直接 finalize。Redact 要先審閱 `artifact archive`
的 plan，再用 `artifact finalize --archive-plan <id> --writer-stopped`。
經審閱的 plans 跟 product changes commit；prepared SpecStory archive handoff
只選一份精確 transcript。舊 intents 保留原目的地與要求，tracked-history
finalizer 仍使用 compatibility skill scripts。

Retirement 重新驗證 archive receipt 與目前 ignored capture bytes。新內容或
缺失 receipt 會阻擋清理。本機持久保存與 remote sync 是不同狀態；一般 status
與 readiness 不會查詢 remote。

Intent 未直接符合 checkout 路徑時，readiness 先核對 canonical Git common directory
身分，再要求分支以解析移動過的 intent。已證明屬於其他 repository
的 pending／finalized 紀錄，因此不會阻擋 detached checkout；pinned submodule
的 detached HEAD 是正常狀態。相關 pending intent、repository 或 moved-intent
身分有歧義，以及無法讀取 intent store，仍會阻擋。精確 finalized receipt 的
要求不變。子 artifact 觀測失敗會回報實際原因，不冒充 clean。

空 gitlink 子項目另以路徑檢查 task／artifact claims，不繼承 parent 的 Git
身分。任何符合路徑且非 discarded 的 artifact intent，即使已 finalized，仍
保守阻擋遞迴移除空子項目；discarded 紀錄也繼續綁定已審閱 plan 的 authority。

## 明確的 Git 同步與原版備份

```bash
dev agent artifact sync --push --json       # preview 會查詢設定的 remote
dev agent artifact sync --apply --plan <id> --yes
dev agent artifact sync --pull --json       # 另一個 preview，只允許 fast-forward

dev agent artifact migrate --mode untrack --path .specstory/history --json
dev agent artifact migrate --apply --plan <id> --yes --writer-stopped

dev agent artifact migrate --mode split --path .specstory/history --json
dev agent artifact migrate --apply --plan <id> --yes
# 可選：把原版 named refs 發布到另一個空目的地。
dev agent artifact backup <completed-migration-id> --remote <git-url> --json
dev agent artifact backup --apply --plan <backup-plan-id> --yes
```

Untrack 保存原始 refs 與選定的目前檔案、編輯 ignore、只移除選定的 index
entries。其他 staged／unstaged 工作保留；必須先處理既有 prepared handoffs。
Untrack 本身不設定今後的自動封存策略。

Split 需要 git-filter-repo 與完整本機歷史。產出經驗證的 `original.bundle`、
`original.git`、`history.git`、`filtered.git`、兩份 commit map、`current/`
工作檔 snapshot 與 manifest。每個 mapped tree 都與精確路徑投影比對，保留
empty commits、merge topology 與 message bytes；改寫後的簽章無法維持有效。
會實際從 bundle clone 驗證恢復。Source refs 與 remotes 均不修改。

Raw backup 不是 hygiene audit。其他 worktrees 的未提交檔案、reflogs、
unreachable objects、LFS payloads 與 submodule repositories 不在範圍內。
Detached tip 必須先有 recovery ref。

Backup publication 用 empty-ref lease 發布 frozen named refs；未提交工作檔
留在本機 `current/`，不會進入 Git remote backup。Sync 綁定審閱的目的地與
branch；push 先證明 ancestry 再用精確 lease，pull 僅 fast-forward。Divergence
交給 native Git 處理，dev 不會強制覆寫或生成 merge。

Private ledger 保留 partial effects、output 與 recovery 路徑。中斷後先檢查
資料與 remote，再建立新 plan，不盲目重試 partial operation。原 remote 切換、
release/tag 遷移，以及 filtered history 的 force-with-lease 發布另行協調。

## 發行排除、停止追蹤與歷史遷移

| 操作 | 效果 | 先前 commits |
|---|---|---|
| `.gitattributes` 的 `export-ignore` | 從 `git archive` 發行包排除指定路徑 | 不變 |
| 純紀錄目錄中的巢狀 `go.mod` | 從父 Go module ZIP 排除該目錄 | 不變 |
| Ignore 加上 untrack | 後續 source commit 不再加入新版本，工作檔保留 | 不變 |
| 在獨立副本過濾歷史 | 產生不含指定路徑的替代歷史 | 副本中的 commit ID 改變 |

dev-cli 的 release source archive 透過 `.gitattributes` 排除 repo root 的
`.specstory`，以及精確的 agent-plan 根目錄 `.claude/plans`、`.codex/plans`、
`.cursor/plans` 與 `.opencode/plans`。這只改變發行邊界，不停止 Git tracking 或
改寫 history；這些規則不排除其他 agent 設定與 skills。自 v0.2.41 起，既有的
純紀錄目錄另外加入附註解的 `go.mod` 邊界標記。Go 不使用 `export-ignore`，
但會從父 module ZIP 排除巢狀 module；這些標記沒有產品 package 或依賴。
不可把標記放在 embedded／generated build inputs 上，也不建立空的 agent 目錄。

`python3 scripts/check-distribution.py --version v0.3.0` 分別產生真正的 Git
archive 與使用固定 `golang.org/x/mod v0.38.0` 的 module ZIP；只有暫存 clone
會停用 module 測試的 export attributes。兩種 payload 都會解壓、編譯，並驗證
版本、help、completion 與 bundled skill。這不會縮小一般 Git clone 或舊 tags。

保留 Git history、排除 source archive 的例子：

```gitattributes
/.specstory export-ignore
/.specstory/** export-ignore
```

驗證匯出的原始碼仍可編譯，包含所有 embedded resources。其他打包系統
有自己的 inclusion rules；這不是所有 package 都適用的排除機制。
新規則影響新的 tagged snapshot，不改變已發布的 immutable tags。

實驗轉正式專案時，先盤點並驗證可回復的原版，再於獨立副本產生及驗證
過濾後歷史，保留 commit mapping。替換 remote、tags、releases 與其他
clones/worktrees 的切換另行協調。Git bundle 不自動包含未提交檔案、
LFS objects 或 submodule repositories。

同步、版本控制與備份各有不同用途。同步刪除仍是刪除；恢復 Markdown
也不是恢復 native agent state。確認憑證外洩時需撤銷或輪替，改寫 Git
不會讓憑證失效，也無法移除其他人或服務持有的副本。

掃描與脫敏操作見 [Repository hygiene](hygiene.md)。Writer 與清理契約
見 `dev help retirement`，setup 見 `dev help repositories`，持久資料與
快取分類見 `dev help storage`。
