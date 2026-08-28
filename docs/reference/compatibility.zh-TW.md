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

## 已確認的專案限制

### Pull request completion 不會自動追蹤

`dev done --pr` push 並建立 pull/merge request，之後保留 task、runtime 與 worktree，因為 integration 由 review 負責。目前 `dev sweep` 不會 query forge 推斷 request 後來已 merge。請驗證 integration 後再刻意 finish/reconcile；不能假設 remote merge 代表 local DONE。

### Agent session capture 只有保留欄位，尚未接線

Task schema 有 `AgentSession`，Herdr inventory 也能顯示 live agent session ID；production start/park/resume path 尚未 capture 或 attach 該 ID。這個欄位與 live inventory 只能視為 observability/future integration，不能承諾 `dev resume` 會恢復 coding-agent conversation。

### Built-in forge cache TTL 與 generated config 不同

`dev config init` 會寫 `forge.cache_ttl = "15m"`。沒有 config file 時，目前 built-in `Forge.CacheTTL` 的 zero value 代表既有 valid cache 不會因 age 被拒絕；explicit `r` 仍會 refresh。Freshness 重要時請執行 `config init` 或設定 TTL。

### Direct mode 的 lifecycle 較小

Direct task 使用 canonical checkout，不能進入 COLD，因為 cold cleanup 會移除 repository 必需的 directory。需要跨機器 reconstruction 時使用 branch-only 或 worktree mode。

## 已實作、不能再列為 limitation 的 behavior

以下是歷史缺口，現行版本已實作：

- `dev start --focus` 會在 non-JSON creation 後 activate runtime。
- TUI navigation 會拒絕開啟 checkout 不存在的 COLD task，並要求使用 `dev resume`。
- Runtime handle 現在保存 backend provenance，cleanup 前會重新驗證。
- `auto` runtime selection 已在 tmux 與 none 之間加入 Zellij。

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
- [Claude Code parallel agents](https://code.claude.com/docs/en/agents)
