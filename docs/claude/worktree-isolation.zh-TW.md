---
description: 以正確 path、base selection、ignored-file provisioning、retention 與 main-checkout enforcement 使用 Claude Code worktree。
authority: anthropic-docs
status: evolving
verified_on: 2026-08-29
tested_with: Claude Code 2.1.250
lang: zh-TW
---

# Worktree 隔離

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Claude Code 可以建立或進入 Git worktree，讓 session 或 subagent 修改不同 files 與 branch，同時共享 repository history。

!!! info "目前 naming"
    具名 session 使用 directory `.claude/worktrees/<name>/` 與 branch `worktree-<name>`。把 `worktree-` 也放進 directory name 的舊筆記已過時。

## Start 或 enter

從 shell：

```bash
claude --worktree feature-auth
claude -w feature-auth
```

Session 中可要求 Claude 在 worktree 工作，harness 會使用 `EnterWorktree`。`ExitWorktree` 讓 session 回到原 directory，並依明確 cleanup intent 保留或移除 harness-created worktree。

把 directory 加入 repository ignore rules：

```gitignore
/.claude/worktrees/
```

Claude Code 拒絕 symlinked `.claude`/worktree creation path，也會在使用 isolation checkout 前驗證 Git identity。

## 選擇 base

`worktree.baseRef` 接受：

| Value | 行為 |
|---|---|
| `fresh`（預設） | 優先使用 remote default branch；更新/cache `origin/HEAD`；沒有可用 remote ref 時 fallback 到 local `HEAD` |
| `head` | 從目前 checkout 的 local `HEAD` 建 branch，包含 committed in-progress state |

```json
{
  "worktree": {
    "baseRef": "head"
  }
}
```

Independent baseline work 使用 `fresh`；isolated worker 必須建立在目前 stream commits 上時使用 `head`。Uncommitted changes 不會自動出現在另一個 worktree。

需要特定 existing branch 或 custom external location 時，用 Git 或 `dev wt create` 建立，不要把 `baseRef` 當任意 ref selector。

## 攜帶 ignored files

建立 tracked `.worktreeinclude`，內容採 `.gitignore` patterns：

```text
.env
.env.local
config/secrets.json
```

Claude Code 只複製同時 match 且 Git-ignored 的 paths；tracked files 已透過 checkout 到達。List 應維持最小，review secret exposure，並在新 directory 安裝 dependencies。

這是 Claude Code 的 provisioning surface。`dev`-owned worktree 使用 `[worktree].include`、dependency strategies 與 `.dev-cli/config.toml`（並保留 legacy `.dev.toml` compatibility）；兩種格式不能互換。

## Cleanup 與 retention

Cleanup 取決於建立方式與是否含 work：

- interactive clean unnamed `--worktree` 可在 exit 時自動移除；
- named 或 changed session 會詢問 keep/remove；
- non-interactive `-p` 保留 worktree 供後續 cleanup；
- isolated subagent 未修改時可於完成後移除；
- 含 changed/untracked/unpushed work 的 worktree 會被 safe periodic cleanup 保留；
- 手動建立或沒有 Claude ownership marker 的 worktree 會保留；
- running agent 會持有 Git worktree lock，防止 concurrent cleanup。

不能簡化成「turn 結束 worktree 就消失」。Lifecycle 由 harness 管理，但是否保存取決於 changes、commits、execution mode、marker/lock state 與 versioned cleanup rules。

## Isolation 內的 enforcement

現行 Claude Code 會阻擋可能逃回 protected main checkout 的特定 tool call：

- 以 main-checkout path 為 target 的 `Edit`/`Write`/`NotebookEdit`；
- working directory 解析到 main checkout，或無法證明留在外部的 shell command；
- 使用 `git -C`、`GIT_DIR`、`GIT_WORK_TREE` 或同等 `cd` redirect 的 Git command；
- safety analyzer 無法追蹤的 command shape。

這些 checks 保護 checkout boundary，不保護 external services 或其他 local resources；permissions 與 sandboxing 仍適用。

## Subagent isolation

Custom subagent 可以要求：

```yaml
---
name: refactorer
isolation: worktree
---
```

適合 independent writer 或 experiment。Read-only agent，或已在同一 `dev` change stream 中依 ownership 協作的 writers，不應自動使用。

## dev-cli ownership rule

- Human 明天可能 review/resume：用 `dev` 建立 stream。
- Harness 擁有 disposable isolated experiment：使用 Claude Code worktree isolation。
- 已在 `dev` worktree：除非代表真正不同 mutation stream，否則直接在現有 checkout 啟動 agent。

這能避免 nested owners 與 ambiguous cleanup。

## 來源

- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
- [Tools reference](https://code.claude.com/docs/en/tools-reference)
- [Git worktree 文件](https://git-scm.com/docs/git-worktree)
- [`internal/skill/dev-cli/references/worktree-ownership.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/worktree-ownership.md)
