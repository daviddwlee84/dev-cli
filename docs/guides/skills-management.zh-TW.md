---
description: Manage project and global skills with scoped checks, reviewed updates, and native lock restoration.
authority: project
status: evolving
lang: zh-TW
verified_on: 2026-09-09
tested_with: skills 1.5.23 and 1.5.25 source contracts; isolated provider fixtures
---

# Skills 管理

`dev skill manage` 開啟獨立 wizard；REPOS 與 SKILLS 的 Ctrl+O 也能進入相同流程，
不新增 dashboard 大頁籤。

```bash
dev skill manage
dev skill manage --repo api
dev skill manage --all
dev skill list --all --check --json
```

可選 project、global、project 加 global，或多個 repository。Repository picker
列出有 `skills-lock.json` 的 checkout，即使 `.agents` 被 Git 忽略或尚未安裝也保留。
Global 只處理一次。Space 勾選，Ctrl+A 全選／取消可見項目，直接輸入文字篩選。

## 檢查與更新

檢查是明確的連線操作：合併相同 source/ref 查詢，比較來源內容與 lock，
不執行 `skills`、npm、Node 或專案程式。結果附時間，存於 `skill-checks-v1.json`；
lock 改變即失效。Skill list JSON 的 `update_checked_at` 是上次比較時間，
cache 不代表正在進行即時來源查詢。

更新先檢查，再預選確認有更新的 skills。缺失安裝、不支援的 lock、檢查失敗及
本地修改會分開呈現。預覽列出 checkout、scope、skill 與命令，確認後才執行。

管理操作需要全域安裝的 `skills` executable，支援依 1.5.23–1.5.25 已驗證契約
相容的 1.x 版本，最低 1.5.23。`dev doctor` 回報依賴狀態；可使用
`npm install -g skills` 安裝或更新。dev 不自動安裝依賴，也不 fallback 至 npx；
缺少 provider 時仍可列出及檢查 skills。

命令使用既有 provider mutation lease 依序執行。預覽後若 lock、安裝內容或
provider 改變，該操作會被拒絕。結果區分 completed、failed、skipped、stale、
canceled 與 unverified；exit 0 本身不足以證明成功，會重新讀取安裝與 lock。
私有 JSON receipt 存於 `<state_dir>/skills/runs/`。外部 installer 與原生 Git
不受 dev 的協調鎖約束；native update 也不是固定內容的 artifact transfer plan。

## 單 project 的 native 操作

選擇單一 project、未包含 global 時，提供以下進階選項：

| 選項 | Native command | 行為 |
|---|---|---|
| 依 lock 重裝 | `skills experimental_install` | 重新解析來源／ref，安裝到 `.agents/skills`，可能更新 lock；適合忽略或缺少 `.agents` 的下游，不保證等於舊 hash。 |
| 同步 dependency skills | `skills experimental_sync --yes --agent …` | 從現有 `node_modules` 讀取 skills，安裝至明確選取的 project agents；不代為安裝 npm dependencies。 |

Wizard 會檢查命令支援、project 目的地變動及本地內容衝突。同名 dependency skill
衝突需個別處理。兩項操作保留 native output 並檢查結果，本輪不提供跨 repo 批次。

`dev skill install` 與 `dev skill sync` 仍管理 bundled dev skill；原有
`dev skill update <skill> --project|--global` 保留作為自動化介面。
Transfer preparation 仍使用其獨立的 pinned provider 與 verified payload 契約。


Provider source contracts: [update](https://github.com/vercel-labs/skills/blob/v1.5.25/src/update.ts), [restore](https://github.com/vercel-labs/skills/blob/v1.5.25/src/install.ts), [dependency sync](https://github.com/vercel-labs/skills/blob/v1.5.25/src/sync.ts).
