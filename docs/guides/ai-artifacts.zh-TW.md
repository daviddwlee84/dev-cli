---
description: 選擇 agent 歷史的保存位置、保留有效決策，並避免聊天進入原始碼發行包。
lang: zh-TW
authority: project
status: evolving
verified_on: 2026-09-12
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

既有「history 進 Git」流程：

```bash
dev hygiene status
dev hygiene scan --scope staged --json
dev prepare --session codex:<uuid>
# 精確的 recorder 停止後，從 checkout 外執行：
dev artifact finalize --intent <id> --writer-stopped
```

Prepare 不會停止 writer。不要反覆脫敏仍被 recorder 從 native source
重寫的檔案。`dev hygiene redact` 使用審閱後的精確 plan；artifact 修改
還需 writer 結束的證據。暫時穩定的 bytes 不能證明 process 已退出。

Ignored history 不必參與一般 code commit 的掃描；但已 tracked 的檔案
不會因新增 `.gitignore` 就停止追蹤。移出 index 是獨立操作。刪除
worktree 前保留或封存 history；ignored 不等於可丟棄或已備份。

## 發行排除、停止追蹤與歷史遷移

| 操作 | 效果 | 先前 commits |
|---|---|---|
| `.gitattributes` 的 `export-ignore` | 從 `git archive` 發行包排除指定路徑 | 不變 |
| Ignore 加上 untrack | 後續 source commit 不再加入新版本，工作檔保留 | 不變 |
| 在獨立副本過濾歷史 | 產生不含指定路徑的替代歷史 | 副本中的 commit ID 改變 |

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
