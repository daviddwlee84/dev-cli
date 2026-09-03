---
description: 選擇每個 worktree 的 owner 與位置，再安全佈建 ignored files 與 dependencies。
authority: project
status: stable
verified_on: 2026-08-29
lang: zh-TW
---

# Worktree 與環境佈建

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Git worktree 一開始是乾淨 checkout。`dev` 擁有持久變更流 (change stream) 的 worktree，並建立可檢查的 provisioning plan，說明要加入哪些 ignored files 與 dependencies。

## 只選一個 lifecycle owner

| Worktree 類型 | Owner | 常見位置 | Lifetime |
|---|---|---|---|
| feature、fix、experiment、跨機器 handoff | `dev` | 設定的 `paths.worktree_path` | `dev done` 只記錄 MERGED；直到後續 `dev retire`、明確 `dev wt rm` 或核准的 sweep 才移除 |
| harness-scoped isolation | Claude Code 或其他 harness | harness-owned path，例如 `.claude/worktrees/<name>/` | 由該 harness 的 retention 與 safe-cleanup 規則管理；Flow 不會 Adopt/Remove |
| 外部建立的 linked worktree | adoption 前由外部管理 | tool-specific | `dev`/`dev flow` 可看見；只有明確 Adopt 後才有 managed lifecycle |

Code、history 或 plans 需要保持可 review，或人之後可能回來時，使用 `dev`。不要把長期 `dev` worktree 巢狀放在 repository 內；外層 checkout 的 file watcher、language server、backup tool 與搜尋會看到第二份 tree。

`dev` 使用 Git 在設定路徑建立 checkout；Herdr 只開啟現有 path，因此沒有 Herdr 的機器仍維持相同 placement policy。

`dev flow [repo]` 以 Git 的 authoritative worktree records 顯示 canonical、managed、unmanaged 與 strict `.claude/worktrees/` harness rows，另加入沒有 checkout 的 task-only rows。它不以 `worktree-*` branch prefix 猜 ownership；path/task binding 有歧義時標示 CONFLICT 並停止 lifecycle mutation。詳見 [Repository Flow 預覽](repository-flow.zh-TW.md)。

## 建立前先檢查

```bash
dev wt plan
dev wt plan --write          # 建立 repository-owned .dev-cli/config.toml
dev wt create feat/auth --base main
dev wt list
```

`dev wt plan` 會讀取 lockfile、tool availability、include/link 規則與 Git ignore state，但不修改 checkout。Plan 列出每個可執行或 skipped step，以及任何 safety downgrade。

## 用 allowlist 攜帶 ignored files

```toml
[worktree]
include = [".env", ".env.local", "config/local.json"]
link = []
post_create = "auto"
strategy = "reinstall"
```

只有明確列出、且 Git 確認為 ignored 的 path 才會被複製。Tracked file 已透過 branch 進入 checkout；用另一個 checkout 的版本覆蓋它會破壞 branch isolation。

Included files 以檔案方式複製，內容不寫入 log。現行 provisioning 在 open/copy 過程也會再次檢查 source 與 destination path shape，拒絕 source swap 與 symlinked destination parent。

不要全域加入 `.claude/settings.local.json`。只有刻意選擇且確實依賴它的 launcher 才加入該精確 path，並在 plan 中驗證。

## 依 correctness 選 dependency strategy

| Strategy | 效果 | 建議 |
|---|---|---|
| `reinstall` | 執行 lockfile 推導出的 install command | 安全預設 |
| `copy` | 複製 installed dependency directory | 只用於 path 可攜的情況 |
| `link` | 共用同一個 dependency directory | 最快，但通常不適合 concurrent writers |
| `skip` | 不安裝 dependencies | container 或 CI-driven development |

每個 ecosystem 的 override 可放在 project 或 global config：

```toml
[worktree.strategies]
node = "copy"
```

新的 project-owned overrides 應放在 `.dev-cli/config.toml`；legacy `.dev.toml`
仍以 compatibility behavior 讀取。來自 `.dev-cli/config.toml` 的 `post_create`
command 必須先用 `dev config trust . --yes` 核准其 exact executable-config hash
才會執行；command 變更後原核准立即失效。

重要內建判斷：

- Python virtual environment 內嵌 absolute path，不能 copy 或 link；`uv sync` 可重用 global cache。
- Node `node_modules` 可以 copy，但拒絕共享，因為任何 checkout 都可能修改它。
- Go 使用 global content-addressed module cache，沒有需要複製的 checkout-local dependency tree。
- Cargo `target/` 可以 copy，但拒絕讓 concurrent build 共用 output。

無效或不安全的設定會附 warning 縮限到 `reinstall`，不會偷偷建立壞環境。

## 自動偵測 setup

`post_create = "auto"` 依 priority 為每個 ecosystem 選一個 manager：

| Marker | Command |
|---|---|
| `uv.lock` | `uv sync` |
| `poetry.lock` | `poetry install` |
| `pnpm-lock.yaml` | `pnpm install --frozen-lockfile` |
| `package-lock.json` | `npm ci` |
| `yarn.lock` | `yarn install --immutable` |
| `go.mod` | `go mod download` |
| `Cargo.toml` | `cargo fetch` |
| `Gemfile.lock` | `bundle install` |

缺少 tool 時會回報並 skip。Setup command 失敗時 worktree 仍保留，讓 branch 與 checkout 能被修復，而不是直接丟棄。

## 重新 provision 或移除

```bash
dev wt provision /path/to/worktree --dry-run
dev wt provision /path/to/worktree
dev wt rm feat/auth
```

移除 worktree 與刪除 branch 是兩個決定。`dev wt rm` 會保留 branch，且沒有 explicit force 時拒絕 dirty checkout。Directory 若在 Git 之外消失，它會 prune stale administrative entry。

在 `dev flow` 中，UNMANAGED row 的 Remove Checkout 只對 exact、clean、unlocked、non-prunable、non-harness linked checkout 出現，並在移除前後 revalidate task claims、Git/agent/runtime/artifact facts；branch 與其 OID 必須保留。Flow 不提供 force/dirty-discard/unknown-runtime override。Managed DONE row 不是 Remove Checkout，而是最後才 reap task record 的 Retire plan；可選 branch deletion 是另一個 typed confirmation。Raw `git worktree remove --force` 會繞過這些 guarantees。

## 來源

- [`internal/wt/plan.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/plan.go)
- [`internal/wt/ecosystem.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/ecosystem.go)
- [`internal/wt/provision.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/provision.go)
- [`internal/wt/manager.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/manager.go)
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/taskflow/remove.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/taskflow/remove.go)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
