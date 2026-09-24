---
description: 了解 v0.3 命令分類、永久捷徑、命令樹說明，以及可退回 Try 的 graduation 流程。
authority: project
status: maintained
verified_on: 2026-09-24
minimum_version: v0.3.0
lang: zh-TW
---

# v0.3 命令導覽

Root help 顯示 17 個命令家族與獨立入口。既有頂層命令保留為永久捷徑，
arguments、flags、輸出與退出行為相同。既有 scripts 不必遷移，捷徑也不會
顯示棄用警告；新文件範例以分類後的正式路徑為主。

## 尋找命令

```bash
dev --help                         # 主要入口
dev help --tree                    # 正式命令樹，預設兩層
dev help --tree --depth 0          # 展開完整正式命令樹
dev help --tree agent artifact     # 聚焦指定命令家族
dev help --tree --aliases          # 同時顯示捷徑與 aliases
dev tries demote --help            # 精確 arguments 與 flags
dev help tries                    # 流程與安全條件
```

Root completion 優先列出主要入口；使用既有捷徑後，arguments 與 flags 的
completion 仍保留。升級後重新載入 `dev self shell-init <shell>` 產生的 shell
integration，取得分類後命令的目錄交接；shell completion 由
`dev self completion <shell>` 提供。

| 主要入口 | 責任 |
|---|---|
| `work` | Task 生命週期：`list`、`start`、`park`、`resume`、`done`、`adopt`、`retire`、`sweep` |
| `repo` | Repository、remote、`bootstrap`、`note` 與獨立 `flow` 預覽 |
| `tries` | 實驗：`try`、`open`、`list`、`graduate`、`demote` 與保留操作 |
| `git` | 受保護的 Git 操作、`ignore`、`worktree`、`submodule`、`hygiene` |
| `agent` | `skill`、`mcp`、`instructions`、`prompt`、`artifact`，其中包含 `prepare` |
| `activity` | `journal` 與持久的 `stats` |
| `self` | `config`、`cache`、`doctor`、`version`、`upgrade`、`completion`、`shell-init`、`feedback` |
| `snippet` | 跨 provider 的小檔案分享 |
| `ssh` | OpenSSH profiles、keys、discovery 與連線 |
| `fleet` | 已設定的遠端機器及各自 host-local 工作 |
| `dotfile` | 原生 chezmoi 設定與明確操作 |
| `pr` | Pull request／merge request 收件匣 |
| `summary` | 目前整台機器的專案快照 |
| `triage` | 跨 repository／Try 的待處理事項、報告與經審閱批次 |
| `status` | 目前 repository、checkout、task 與 runtime context |
| `tui` | 互動 dashboard 與已設定的 tool bindings |
| `help` | 流程主題與命令樹導覽 |

`work` 不新增儲存物件或 task state。Retire／sweep 保留處理 unmanaged checkout
的既有能力。`repo flow` 仍是獨立的 preview 介面；一般 `repo open` 不要求 task。
`activity stats` 仍是持久資料，不會被 `self cache clear` 刪除。

## 既有捷徑

| 既有寫法 | 正式路徑 |
|---|---|
| `ls`、`list` | `work list` |
| `start`、`park`、`resume`、`done`、`adopt`、`retire`、`sweep` | 對應的 `work` 命令 |
| `bootstrap`、`note`、`flow`、`browse` | 對應的 `repo` 命令 |
| `try` | `tries try` |
| `graduate` | `tries graduate` |
| `gitignore`、`ignore` | `git ignore` |
| `wt`、`worktree` | `git worktree` |
| `submodule`、`hygiene` | 對應的 `git` 命令 |
| `skill`、`mcp`、`instructions`、`prompt`、`artifact` | 對應的 `agent` 命令 |
| `prepare` | `agent artifact prepare` |
| `journal`、`stats` | 對應的 `activity` 命令 |
| `config`、`cache`、`doctor`、`version`、`upgrade`、`completion`、`shell-init`、`feedback` | 對應的 `self` 命令 |
| `edit` | `self config edit` |
| `gist` | Snippet 流程的 GitHub 專用捷徑 |

`dev try archive` 仍會建立或開啟名為 `archive` 的實驗；管理操作必須明確使用
`dev tries archive <ref>`。前者的分類後寫法是 `dev tries try archive`。
`gist` 捷徑保留 GitHub 專用 provider 行為，不會變成不限定 provider 的 snippet
請求。

已文件化的 JSON schemas、task files、catalog identity 與 TUI tab 名稱保持相容。
捷徑即使省略於精簡 root listing，仍能直接執行。`git ignore --stdout` 與
`--list` 仍可在 repository 外執行。

自 v0.3.1 起，terminal graduation 使用可審閱的名稱／發布 wizard；
`--yes`、非 TTY 與 dry-run 保留直接路徑。Flags 與記住的名稱見
[畢業指南](../guides/try-graduation.zh-TW.md)。

## 將已畢業的 repository 退回 Try

```bash
dev tries graduate scratch-parser
dev tries demote scratch-parser --dry-run
dev tries demote scratch-parser
dev tries demote <catalog-id> --to ~/src/tries/2026-09-24-parser
```

Demote 只接受曾從 Try graduate 的 repository。它把目前目錄搬回設定的 Try
root，保留 catalog ID、tags、notes、畢業紀錄、目前的 Git history／remotes，
以及所有現有檔案，包括 dirty、untracked、ignored 資料。結果是 active、present
的 Try；不撤銷 commit、push、repository publication 或 Git initialization，
也不在舊位置留下 symlink。

請先離開該 checkout 目錄再執行。目的地必須是目前 Try root 的直接可見子目錄，
runtime 觀察涵蓋所有可用的 backends；不完整 coverage 或 `none` 都不能證明
checkout 沒人使用。

預設目的地是記錄中的原 Try 路徑。若該位置已被占用，或已不在目前 `tries_root`
內，需用 `--to` 指定安全目的地。既有 task、runtime／agent、artifact 與
linked-worktree claims 不會被默默改指新路徑。不安全路徑、不完整觀察、過期
計畫都會阻擋操作。搬移延續同檔案系統、來源重新驗證與 recovery 保護。
CLI 顯示搬移計畫後套用；`--dry-run` 只預覽。REPOS 選單的 demote action
會先展示搬移計畫，經確認後才套用。

Windows 上的 demotion 與同時執行的 dev lifecycle writers 必須全部使用
v0.3.0 或更新版本，才能在目錄搬移期間參與同一個 lease。舊版 dev、直接執行的
Git 與外部工具不在這項搬移保證內。

| 面向 | 轉換 |
|---|---|
| 身份 | Try → `graduate` → Repo → `demote` → Try |
| 意圖 | active → `deprecate` → deprecated → `reactivate` → active |
| 存放位置 | Try → `archive` → Archive → `restore` → Try |
| 處置 | Try → `delete` → 系統 Trash；先從系統還原，再 `tries restore --from` |

Deprecate 不搬檔案。Trash 不是 archive 或已驗證的遠端備份，永久刪除也沒有
restore 路徑。光是存在 remote，不能證明本機 clone 可安全清除：ignored data、
local refs 與其他保留狀態仍需處理。

Repository open／browse／remote／search 體驗保持原樣。本版不新增遠端 Tab
搜尋或統一搜尋 wizard，也不加入 repository eviction、mirror 同步、
`tries new`／`clone` 或 graduation symlink。
