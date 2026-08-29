---
description: 記錄 dev-cli dependencies、upstream preview status、documentation constraints 與刻意未完成的 behavior。
authority: project-and-upstream
status: evolving
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
lang: zh-TW
---

# 相容性與已知限制

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

這頁把 graceful degradation 與真正 limitation 分開。Command/runtime code 或 version-sensitive Claude Code 文件變動時必須重新驗證。

## dev-cli capability matrix

| Capability | Dependency | 缺少時 |
|---|---|---|
| 核心 repository/task/worktree 操作 | Git | 無法使用；Git 是唯一 hard runtime dependency |
| grouped runtime 與 agent activity | Herdr server + CLI | `auto` 改試 tmux、Zellij，最後 none |
| named terminal session | tmux 或相容 Zellij | `none` 保留核心 behavior 與 shell navigation |
| GitHub pull requests/remotes | authenticated `gh` | Git 可用時 branch 仍能 push，可能需 browser/manual flow |
| GitLab merge requests/remotes | authenticated `glab` | 同樣 graceful fallback |
| worktree dependency setup | ecosystem manager（`uv`、npm、Cargo 等） | plan 回報 missing tool 並保留 checkout |
| interactive dashboard | terminal input/output | 透過 pipe 執行 bare `dev` 時輸出 plain task list |
| repository-note search | linked `modernc.org/sqlite` 與 FTS5 | 不需要外部 `sqlite3` executable |

## 已確認的專案限制

### Note search 與 filesystem durability 依文字及平台而異

Latin note query 使用 term-wise prefix FTS 與 SQLite ranking。Non-ASCII query 改用 literal term-wise substring matching，因為 SQLite 的 `unicode61` tokenizer 不會切分任意 CJK substrings；這些結果不使用相同的 FTS ranking。

所有支援平台的 note write 都會 sync file 並 atomic rename。Unix 也會在 rename/delete 後 sync containing directory。Windows implementation 無法提供 directory-fsync 步驟，因此 sudden power loss 時的 durability guarantee 較窄。Cooperating `dev` processes 的 concurrent mutations 會被 serialized，且每次 Markdown replacement 都是 atomic；任意 external writer 不會參與這個 lock。

### Pull request completion 不會自動追蹤

`dev done --pr` push 並建立 pull/merge request，之後保留 task、runtime 與 worktree，因為 integration 由 review 負責。目前 `dev sweep` 不會 query forge 推斷 request 後來已 merge。請驗證 integration 後再刻意 finish/reconcile；不能假設 remote merge 代表 local DONE。

### Agent session capture 只有保留欄位，尚未接線

Task schema 有 `AgentSession`，Herdr inventory 也能顯示 live agent session ID；production start/park/resume path 尚未 capture 或 attach 該 ID。這個欄位與 live inventory 只能視為 observability/future integration，不能承諾 `dev resume` 會恢復 coding-agent conversation。

### Built-in forge cache TTL 與 generated config 不同

`dev config init` 會寫 `forge.cache_ttl = "15m"`。沒有 config file 時，目前 built-in `Forge.CacheTTL` 的 zero value 代表既有 valid cache 不會因 age 被拒絕；explicit `r` 仍會 refresh。Freshness 重要時請執行 `config init` 或設定 TTL。

較舊的 generated config 可能包含 `forge.remote_limit = 100`。此欄位仍可解析，
但 forge inventory 現在會完整 pagination，因此不再限制 synchronization。
`dev config init` 不再寫入它；`dev repo remote --limit` 只在完整 inventory 搜尋
完成後限制 rendered matches。

### Direct mode 的 lifecycle 較小

Direct task 使用 canonical checkout，不能進入 COLD，因為 cold cleanup 會移除 repository 必需的 directory。需要跨機器 reconstruction 時使用 branch-only 或 worktree mode。

## 已實作、不能再列為 limitation 的 behavior

以下是歷史缺口，現行版本已實作：

- `dev start --focus` 會在 non-JSON creation 後 activate runtime。
- TUI navigation 會拒絕開啟 checkout 不存在的 COLD task，並要求使用 `dev resume`。
- Runtime handle 現在保存 backend provenance，cleanup 前會重新驗證。
- `auto` runtime selection 已在 tmux 與 none 之間加入 Zellij。

- `dev done` 在省略 `--ff`/`--pr` 且為 TTY 時會開啟 interactive finish wizard，把 dirty checkout 拿去跟 base 比較（commit、discard 或 cancel），不再直接拒絕任何 uncommitted change；non-interactive caller 仍須明確傳入 `--dirty` policy，且 destructive discard 需要 `--yes`。
- Human-readable output 現在具備 semantic color（`--color auto|always|never`），在 `NO_COLOR` 已設定、`TERM=dumb`，或 stdout/stderr 不是 terminal 時會自動停用。
- `dev done` 只記錄 MERGED：它不會關閉呼叫端 runtime、移除 worktree 或刪除 branch。Cleanup 已移交 `dev retire`，後者必須從目標 workspace 之外執行，會拒絕 active agent 與 mixed-purpose workspace，並在每次關閉 runtime 之後重新驗證 Git state。`dev done --delete-branch` 現在會直接報錯並指向 `dev retire --delete-branch`，`--keep-worktree` 則以 no-op 警告。
- `dev sweep --merged-worktrees` 直接從 Git 列舉 linked worktrees，而非從 task registry，因此 branch 已被 base 包含的 unmanaged worktree 也能被 retire。Containment 本身絕不等於許可；dirty state、未 finalize 的 artifact、進行中的 Git operation 與 runtime 拒絕條件都仍會阻擋，且未加 `--delete-branches` 時 branch 一律保留。
- `dev sweep` 會把 branch 已不存在於 Git 的 branch-backed task 視為 dead，並提供 reap 該 record 的建議。這種 task 無法 finish、resume 或 retire，因為這些路徑都必須先解析 branch；在 `--apply` 之前該建議仍只是報告。
- 未知 command 會被回報，而不是被丟棄。`dev` 關閉了 cobra 自己的 error 輸出，先前又額外略過所有訊息開頭為 `unknown command` 的 error，因此打錯 command 時兩個 stream 都沒有任何輸出。現在會把訊息、cobra 的「Did you mean this?」建議，以及指向 `--help` 的提示寫到 stderr，exit status 為 1。
- 對 command family 傳入多餘的 argument 現在是 error，而不是安靜地印出 help。`dev wt bogus` 過去會印出 `dev wt` help 並 exit 0，因為 family 本身沒有 `Run`；現在每個 family node 都會回報未知 subcommand 並 exit 1，而單獨執行 family 仍會印出 help 並 exit 0。
- Argument 數量與 flag 錯誤會印出該 command 的 usage block。該 block 是否上色仍由 `--color` 決定。
- 每個 command family 的 help 都附上 ASCII orientation diagram 與 `See also: dev help <topic>` 指引，且 `dev help <command>` 會把 command 名稱或 alias 解析成對應 topic，因此 `dev help wt` 會連到 worktrees 頁面。

## Claude Code status matrix

| Surface | 2026-08-28 status | Compatibility note |
|---|---|---|
| core agentic loop/tools | official | exact tools 依 surface/model/policy 而異 |
| worktree isolation | official、快速演進 | cleanup、baseRef、resume 與 safety details 都有 version sensitivity |
| subagents | official | fork/background defaults 與 naming 在 2.1.x 中曾變更 |
| Agent view | research preview | 保留 manual sessions/worktrees fallback |
| agent teams | experimental、預設關閉 | 無 automatic worktree isolation；有 resumption/task/shutdown limitations |
| Dynamic Workflows | versioned feature | 需要 v2.1.154+；availability/config/limits 依 release 而異 |
| Agent SDK | official SDK | 不同 language SDK 的 parity 與 event availability 可能不同 |

`TeamCreate` 與 `TeamDelete` 是 v2.1.178 移除的 historical tools。舊 `Task` worker tool 已由 `Agent` 取代；兩者都不能與 Task-list metadata tools 混淆。

## Documentation stack 限制

- `mkdocs-static-i18n` 回報 `zh-TW` 不是 Lunr language，因此 Chinese search 沒有專用 Lunr stemmer/segmenter；navigation 與 pages 仍正常 build。
- `mkdocs-llmstxt` 在 static-i18n 重映射 pages 後會跳過內容；本專案以 deterministic local generator 取代空輸出，並恢復 strict build。
- copy-to-LLM plugin 在 zh-TW 頁的 button label 仍是 English；只有 cosmetic impact。
- MkDocs 與 Material pin 在 next major 以下，因目前 plugin stack 以 MkDocs 1.x/Material 9.x 為目標。

## 回報 compatibility change

更新 owning guide、兩種語言、這份 matrix 與[來源與時效](sources-freshness.md)。附 tested binary/version 與 code/test 或 official-source link。不能為了 documentation backward compatibility 保留已知錯誤敘述。

## 來源

- [`internal/runtime/runtime.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/runtime.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
- [`internal/task/task.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/task/task.go)
- [`internal/forge/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/cache.go)
- [`internal/note/index.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/index.go)
- [`internal/note/store.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/store.go)
- [`internal/note/sync_windows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/sync_windows.go)
- [Claude Code parallel agents](https://code.claude.com/docs/en/agents)
