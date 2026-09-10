---
description: Manage project and global skills with scoped checks, reviewed updates, and native lock restoration.
authority: project
status: evolving
lang: zh-TW
verified_on: 2026-09-10
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

## Bundled dev-cli skill 的生命週期

內嵌的 `dev-cli` skill 跟隨 binary 更新，與外部 `skills` provider 管理的
其他 skills 分開處理：

```bash
dev skill install                 # 安裝或明確覆寫內嵌檔案
dev skill install --check         # 本機內容比較；不同或未安裝時回傳非零
dev skill install --if-installed  # 只刷新已存在的安裝
dev skill uninstall --dry-run     # 預覽確切的受管理檔案與相符連結
dev skill uninstall              # 確認後移除這些檔案與連結
```

`dev doctor` 與 `dev upgrade --check` 會回報預設安裝是否符合目前 binary，
不會更改內容。`dev upgrade` 成功後，由**新版執行檔**刷新已安裝的
`~/.agents/skills/dev-cli`。Homebrew 與 Scoop 使用各自的穩定安裝路徑，
不會選到 PATH 上其他 `dev`。未安裝 skill 時不會新增；binary 已是最新版時，
一般的 upgrade 也會修復既有 skill。Binary 更新成功後若 skill 刷新失敗，
會另外回報這個部分完成的結果。

安裝時以 `.dev-cli-install.json` 記錄內容 hash。自動刷新會拒絕覆寫已記錄的
本機修改、補回遺失檔案，並移除先前安裝記錄中未修改的過時檔案。明確執行
`skill install` 會覆寫內嵌檔案，保留其他檔案與既有的外部 agent 連結／目錄。
沒有 manifest 的舊安裝以 `dev-cli` frontmatter 辨識；第一次刷新會覆寫已知的
內嵌檔案並記錄 ownership。若有個人修改，請先保存再進行第一次遷移。

Uninstall 會預覽並重新驗證已記錄的檔案，以及仍指向該安裝的 agent symlink。
已修改的受管理檔案會阻止移除，其他檔案與外部連結則保留。不會遞迴刪除 skills
目錄，也不會修改原生 skills lock。自動化可用 `--yes` 確認顯示的移除範圍。
舊安裝需要先明確執行一次 `skill install`，讓 uninstall 能驗證檔案 ownership。

自訂 `--dir` 安裝需用 `skill install --dir PATH` 刷新、
`skill uninstall --dir PATH` 移除。直接透過套件管理器升級，或由 v0.2.23
以前的 binary 發起升級，都不會執行新的刷新 hook；這兩種情況請在更新後
用新版執行檔執行一次 `dev skill install`。
