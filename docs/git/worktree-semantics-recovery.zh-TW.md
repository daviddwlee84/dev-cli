---
description: 理解 Git worktree 隔離與共享的內容，再安全 add、lock、remove、prune、move 與 repair。
authority: git-scm
status: official
verified_on: 2026-08-28
lang: zh-TW
---

# Worktree 語義與復原

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

一個 repository 有一個 main worktree，以及零個或多個 linked worktrees。每個 linked worktree 都有自己的 working files、index 與 `HEAD`，但共享 repository object database 與大多數 refs/configuration。

!!! info "時效"
    **Authority：**`git-worktree(1)` · **Status：**官方 Git semantics · **Verified：**2026-08-28。`dev` 會增加 policy 與 guardrails，但不改變這些基本語義。

## 隔離與共享 state

| 每個 worktree 各自擁有 | 共享或在外部共享 |
|---|---|
| working directory | Git object database |
| index | `refs/` 下多數 refs |
| `HEAD` | remotes 與 repository-level config |
| 特定 per-worktree refs/config | 未另行設定時的 hooks |
| in-progress operation state | ports、databases、caches、services、deploy targets |

`refs/bisect`、`refs/worktree`、`refs/rewritten` 等部分 refs 是 per-worktree。不要猜測 `.git` 內的 physical path，應使用 `git rev-parse --git-path <name>`。

## Same-branch safeguard

Git 通常拒絕讓同一 branch 同時被多個 worktree checkout。`--force` 可以繞過，但兩個 checkout 都會推進同一 branch ref；concurrent writers 可能破壞彼此假設。每條 mutation stream 應使用不同 branch。

## 適合 script 的 inventory

```bash
git worktree list
git worktree list --verbose
git worktree list --porcelain -z   # 穩定 machine-readable form
```

`-z` 可避免 path/newline parsing ambiguity。Tool 應使用它，不要解析 human table。

## 明確地 add

```bash
git worktree add -b feat/auth ../auth-wt main
git worktree add ../existing existing-branch
git worktree add --detach ../review HEAD
```

Automation 應明確指出 base/commit。Detached worktree 適合 read-only review 或 testing，不適合未命名的持久 change stream。

使用 `dev` 時優先：

```bash
dev wt create feat/auth --base main
```

它會先套用 placement 與 provisioning policy，再開啟 runtime。

## Lock 暫時不可用的 worktree

Removable disk 或暫時離線的 network share 可用 lock 防止 prune：

```bash
git worktree lock --reason "external SSD" /path/to/worktree
git worktree unlock /path/to/worktree
```

Lock 是 administrative safety signal，不是 writers 之間的 file lock。

## Remove 與 prune 不同

透過 Git 移除 clean linked worktree：

```bash
git worktree remove /path/to/worktree
```

有 modified/untracked work 或 submodule case 時，Git 會拒絕，除非 force。考慮 force 前先檢查 `git status --short`，並保存 commits/files。

Prune 只清除 directory 已在外部消失的 stale administrative entry：

```bash
git worktree prune --dry-run --verbose
git worktree prune
```

`prune` 不能取代 `remove`；先 preview。Locked entry 會受到保護。

`dev wt rm` 保留 branch 並拒絕 dirty worktree；`dev sweep` 能回報 stale recorded path。

## Move 與 repair

在支援情況使用 Git-aware movement：

```bash
git worktree move /old/path /new/path
```

Main 或 linked worktree 若被手動移動，從 repository 修復 administrative links，或明確指定新 path：

```bash
git worktree repair /new/path/feature
```

Main worktree 與含 submodule 的 worktree 有額外 move restrictions。強制處理 locked 或特殊 layout 前，先讀目前 Git version 的 manual。

## 復原 checklist

1. 停止所有 writers，記錄每個 checkout 的 `git status --short` 與 branch。
2. 用 `--porcelain -z` 列出 worktrees；不要只看 directories 就推斷 registration。
3. 保存 dirty/untracked files，並為不確定 commits 建立 backup refs。
4. 只有 directory 移動時用 `repair`；directory 消失時 preview `prune`。
5. Branch 被刪時檢查 `git reflog`，在 cleanup expiry 前建立 recovery branch。
6. Rebase 進行中時，刻意選擇 `--continue`、`--abort`、`--skip` 或 `--quit`；不要移除其 worktree。
7. 稱 checkout 可丟棄前，先驗證 remote reachability。

## Squash 與 `[gone]` 陷阱

Branch 的 patch 可能已完整 squash merge，卻不符合 ancestry check。Upstream 標成 `[gone]` 也可能尚未 merge。刪除 branch 或 worktree 前，確認 pull request/patch 並保留 recovery ref。

## 來源

- [Git worktree 文件](https://git-scm.com/docs/git-worktree)
- [Git reflog 文件](https://git-scm.com/docs/git-reflog)
- [`internal/gitx/worktree.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/gitx/worktree.go)
- [`internal/wt/manager.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/wt/manager.go)
