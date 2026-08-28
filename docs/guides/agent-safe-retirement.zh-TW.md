---
description: 安全地從外部 retire dev-cli 已整合的 worktree 與 runtime，而非在被移除的 workspace 內部執行。
authority: project
status: stable
verified_on: 2026-08-28
lang: zh-TW
---

# Agent-safe retirement

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

一條 `dev` change stream 只有在外部呼叫者關閉其 runtime、移除其 worktree 之後，才算真正完成 retirement——feature agent 絕不能自行摧毀自己所在的 checkout 或 runtime。

## 三個里程碑，不是一個

Completion 分成三個獨立里程碑，且目前 persisted task state 的 `done` 對應的是中間那個，不是最後一個：

```text
READY     writer 離開後，commit 了 exact final transcript
MERGED    branch 已整合；runtime/worktree 可能仍存在
RETIRED   runtime 已消失、worktree 已移除、可選擇刪除 branch、task 已 reap
```

`hot`、`warm`、`cold`、`done` 仍是唯一的 persisted task state——`done` 代表 MERGED，cleanup 可能仍待執行，**不代表** runtime 或 worktree 已經被刪除。

## 命令

| 命令 | 作用 |
|---|---|
| `dev prepare --session <provider:uuid> --plan <path>` | 在不關閉目前執行中 agent 的情況下，arm post-writer artifact finalization。Product changes 必須已先 commit；transcript 本身刻意尚未 stage。 |
| `dev artifact finalize --run-id "$DEV_AGENT_RUN_ID" --if-pending --writer-stopped` | 在 writer 停止後，commit 唯一 exact、stable 的 transcript。`--if-pending` 在沒有對應 armed intent 時靜默 no-op；`--writer-stopped` 確認外層 wrapper 已 return。 |
| `dev done --ff` | 把 task branch rebase 到其 base 上並在本機 fast-forward。記錄為 MERGED；不會關閉 runtime、移除 worktree 或刪除 branch。 |
| `dev done --pr` | Push branch，並透過可用的 forge CLI 開啟 pull/merge request。Task 保持在 review 狀態，不是 MERGED。 |
| `dev done --merged --base-ref <ref>` | 驗證某個已在外部 merge 的 branch 是否被 `<ref>` 包含，並記錄為 MERGED。 |
| `dev done --merged --base-ref <ref> --confirm-squash <merge-commit>` | 同上，但用於 squash merge：attest（斷言）已證明被 `<ref>` 包含的該 commit 代表這條 feature branch。這是 dev 無法自行驗證的 operator assertion。 |
| `dev retire [task-or-worktree] [--close-unknown] [--assume-no-runtime] [--delete-branch] [--timeout <duration>]` | 重新解析每一個 covering runtime session，拒絕 active agent 與 mixed-purpose workspace，等待其關閉，重新驗證 Git state，才移除 linked worktree（不使用 force）。只有在所有要求的步驟都成功後，才刪除 task record。 |
| `dev sweep --merged-worktrees [--base <ref>] [--apply] [--yes] [--close-unknown] [--assume-no-runtime] [--delete-branches]` | 從 canonical checkout 執行，回報（加上 `--apply` 時則 retire）branch 已被 base 包含的 task-tracked 與 unmanaged linked worktree。 |

Dirty checkout 在這裡不會直接失敗：`dev done` 會先把它與 base 比對分類，在
interactive 時提供 commit 或 discard，在 script 中則接受明確的 `--dirty`
policy。該 wizard 詳見[變更流工作流程](change-stream-workflow.zh-TW.md)。
`dev done` 上的 `--keep-worktree` 與 `--delete-branch` 之所以仍被接受，只是為了
明確報錯並指向 `dev retire`。

## 一般 local flow

在 feature worktree 中：

```bash
# 先 commit product changes，不要 stage 仍在變動中的 transcript。
dev prepare --session claude:<uuid> --plan .claude/plans/task.md
# 正常結束 agent，讓 SpecStory 寫出最終 Markdown。
```

外層 `specstory run` wrapper 接著呼叫：

```bash
dev artifact finalize --run-id "$DEV_AGENT_RUN_ID" --if-pending --writer-stopped
```

外部的 main/integration workspace 完成剩下的工作：

```bash
dev done <task> --ff
dev retire <task> --delete-branch
```

`dev done` 只負責整合並記錄為 MERGED。`dev retire` 會重新解析每個 runtime pane、關閉符合條件的 session、等待它們消失、重新驗證 Git，再移除 worktree（不使用 force）。

## Pull-request 流程

```bash
dev done <task> --pr
# CI/review 完成，且以保留 commit 的方式 merge 之後
git fetch origin
dev done <task> --merged --base-ref origin/main
dev retire <task> --delete-branch
```

Squash merge 並不等同於 ancestry-equivalent，因此需要明確的 operator attestation：

```bash
dev done <task> --merged --base-ref origin/main --confirm-squash <merge-commit>
```

這只證明所指名的 squash commit 被包含在 base 中；operator 是在斷言它確實代表這條 feature branch。

## 拒絕條件

只要符合以下任一條件，`retire.Inspect` 就會拒絕繼續執行；且它會在關閉 session 後重新檢查，而不是信任過期的視圖：

- 呼叫者目前所在目錄就是 target checkout 或其下層；
- 呼叫者的 `HERDR_WORKSPACE_ID`、`HERDR_PANE_ID` 或 `TMUX_PANE` 對應到一個涵蓋 target 的 runtime session；
- 某個涵蓋 target 的 runtime workspace 同時也包含 target 以外的 pane（mixed-purpose workspace）；
- 任何涵蓋中的 agent status 為 `working`、`running`、`busy`、`blocked` 或 `waiting`——**任何 flag 都無法 override**；
- Agent status 為空或 `unknown`，**除非**呼叫者從 target 外部傳入 `--close-unknown`；
- Agent status 無法辨識（不在已知集合內）——一律阻擋；
- Runtime enumeration 本身失敗，**除非**呼叫者傳入 `--assume-no-runtime`。

`--close-unknown` 與 `--assume-no-runtime` 只放寬 fail-closed 的*觀測結果*（無法讀取的 status、無法列舉的 runtime list）。這兩個 flag 都絕不會 bypass caller containment 或 active-agent state。`retirement.Service.Retire` 在 runtime session 關閉後，也會重新驗證 target identity、Git ancestry、進行中的 Git operation（`gitx.InProgress`，檢查 `MERGE_HEAD`、`CHERRY_PICK_HEAD`、`REVERT_HEAD`、`rebase-merge`、`rebase-apply`、`sequencer`；不含 `REBASE_HEAD`，因為 Git 在 rebase 完成後仍會保留它）以及 worktree 是否 clean——因為 runtime draining 期間現實可能已改變，先前的驗證結果不會跨越這個邊界沿用。

## 用 sweep 做定期 cleanup

```bash
dev sweep --merged-worktrees
# 把確切的 candidates/blockers 呈現給使用者，並徵求核可。
dev sweep --merged-worktrees --apply --yes
```

這會同時列出 task-tracked 的 DONE worktree，以及 named branch 已被 base 包含的 unmanaged linked worktree——請從 canonical checkout 執行。它會先回報再套用：containment 本身絕不等於許可。Dirty 的 Git state、pending 或無法到達的 artifact、locked 或 prunable 的 worktree registration、進行中的 Git operation，以及與 `dev retire` 相同的 runtime 拒絕條件，都仍會阻擋 cleanup。Retirement 完成後預設保留 branch；只有在使用者另外核可刪除時，才加上 `--delete-branches`。

## 安全邊界

原始的 `git worktree remove --force` 會完全繞過 dev——絕不要在佔用 target 的 agent 中執行它。dev 的保證僅止於：沒有任何 dev-mediated 路徑會執行 forced removal；它無法阻止 operator 或 script 直接呼叫 Git。

曾經真實發生過：一個 Codex session 從另一個 checkout 刪除了自己已註冊的 worktree 與 branch。Herdr 仍讓該 workspace 與 terminal 保持存活，因為 Unix process 在路徑被 unlink 之後仍可持有開啟中的 cwd inode；接著 SpecStory 又在同一路徑重新建立、但內容只剩 `.specstory/`。這個 shell 看起來還活著，但已經不再是一個 Git checkout：

```text
failed to reload config: No such file or directory
fatal: not a git repository (or any of the parent directories): .git
```

請把「runtime 存活 + Git registration 不存在 + artifact-only path」視為需要 transcript salvage 與外部 reconciliation 的 orphan——**絕不能**視為 RETIRED。

## 來源

- [`internal/skill/dev-cli/references/agent-retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/agent-retirement.md)
- [`internal/help/topics/retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/retirement.md)
- [`internal/cli/retire.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/retire.go)
- [`internal/cli/artifact.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/artifact.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/retire/safety.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/safety.go)
- [`internal/retire/service.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/service.go)
- [`internal/gitx/transactions.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/gitx/transactions.go)
