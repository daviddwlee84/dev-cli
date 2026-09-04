---
description: 安全地從外部 retire dev-cli 已整合的 worktree 與 runtime，而非在被移除的 workspace 內部執行。
authority: project
status: stable
verified_on: 2026-09-03
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
| `dev flow [repo]` | 在獨立 TTY preview 中顯示 DONE row 的 Retire (Keep Branch) 或 typed Retire + Delete Contained Branch plan；Enter 只規劃，第二次 approval 才套用。 |
| `dev artifact discard <intent> --yes` | 記錄某個 intent 永遠無法 finalize——transcript 從未被寫出，或 rebase 之後 HEAD 已不存在——使它不再阻擋 integration 與 retirement。它不會 commit 也不會復原任何東西，會先印出確切將被放棄的內容，並拒絕仍為 `armed` 的 intent，因為 finalize 才是保存 transcript 的路徑。 |
| `dev retire [task-or-worktree] [--close-unknown] [--assume-no-runtime] [--delete-branch] [--timeout <duration>]` | 重新解析每一個 covering runtime session，拒絕 active agent 與 mixed-purpose workspace，等待其關閉，重新驗證 Git state，才移除 linked worktree（不使用 force）。只有在所有要求的步驟都成功後，才刪除 task record。 |
| `dev sweep --merged-worktrees [--base <ref>] [--apply] [--yes] [--close-unknown] [--assume-no-runtime] [--delete-branches]` | 從 canonical checkout 執行，回報（加上 `--apply` 時則 retire）branch 已被 base 包含的 task-tracked 與 unmanaged linked worktree。 |
| `dev sweep --ephemeral-worktrees [--stale-days <n>] [--json]` | 從 canonical non-bare checkout 產生 strict Claude Workflow V1 report；JSON schema 1 僅供 report。 |
| `dev sweep --ephemeral-worktrees --apply [--delete-branches --base <ref>]` | 要求 TTY 與逐項 confirmation，接著在 common-dir cleanup lock 下重新驗證每個已核可 fingerprint，才用 plain non-force removal。 |

Dirty checkout 在這裡不會直接失敗：`dev done` 會先把它與 base 比對分類，在
interactive 時提供 commit 或 discard，在 script 中則接受明確的 `--dirty`
policy。該 wizard 詳見[變更流工作流程](change-stream-workflow.zh-TW.md)。
`dev done` 上的 `--keep-worktree` 與 `--delete-branch` 之所以仍被接受，只是為了
明確報錯並指向 `dev retire`。

Bare interactive `dev done` 到達 MERGED 後，cleanup step 會先做 read-only
retirement preview，再詢問保留、retire，或 retire 並刪除 contained branch。
核可前會列出 covering Herdr panes 與 recognized agent 狀態；caller-owned Herdr
workspace 改由 fresh exact-pane external coordinator 處理。Working、blocked、
waiting agent 與 mixed workspace 永遠不能被 override。

## Flow 中的 DONE 與 Retire

`dev flow` 把 persisted `DONE` intent 與 live cleanup evidence 分開。DONE/MERGED row 仍可顯示 runtime、worktree、branch 與 task record；只有 Retire result 完成最後的 CAS task deletion 才是 RETIRED。UNMANAGED row 使用的是永遠保留 branch 的 Remove Checkout，不會製造 DONE/RETIRED task milestone；canonical 與 harness row 不能 Remove Checkout。

Flow TUI 沒有 `--close-unknown`、`--assume-no-runtime`、dirty discard 或 generic force。`runtime=none` 會保留「session/agent unobserved」，不冒充已證明 closed。需要 expert acknowledgement 時，離開 Flow 並明確執行 plan 顯示的 fallback CLI，再接受該 command 自己的 guards。完整 row/action matrix 見 [Repository Flow 預覽](repository-flow.zh-TW.md)。

Action 前若需要 read-only explanation，從 exact checkout render 或 open generic
`workspace-closeout` recipe：

```bash
dev prompt render workspace-closeout .
dev prompt open workspace-closeout . --agent my-agent
```

Recipe 內含完整 retirement audit，但結果只是 advisory。`eligible` 與 merged PR 都不
授權 cleanup；外部 `dev retire` 仍會重新收集並驗證所有 gate。若 `dev done --ff` 或
`dev git pull-rebase` 停在 conflict，只用 `prompt open` 討論 semantic resolution，
接著明確 continue/abort Git，再重新執行 lifecycle command。詳見
[Prompt handoff](prompt-handoffs.zh-TW.md)。

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

`--close-unknown` 與 `--assume-no-runtime` 只放寬 fail-closed 的*觀測結果*（無法讀取的 status、無法列舉的 runtime list）。這兩個 flag 都絕不會 bypass caller containment 或 active-agent state。`retirement.Service.Retire` 在 runtime session 關閉後，也會重新驗證 target identity、Git ancestry、進行中的 Git operation（`gitx.InProgress`，檢查 `MERGE_HEAD`、`CHERRY_PICK_HEAD`、`REVERT_HEAD`、`BISECT_LOG`、`rebase-merge`、`rebase-apply`、`sequencer`；不含 `REBASE_HEAD`，因為 Git 在 rebase 完成後仍會保留它）以及 worktree 是否 clean——因為 runtime draining 期間現實可能已改變，先前的驗證結果不會跨越這個邊界沿用。

Taskflow Apply 也不信任畫面時的 snapshot：它鎖定 canonical repository/task store、重新讀取 task revision 與 authority fingerprint，並在 runtime closure、worktree removal、optional branch deletion 與最後 task reap 前逐段 revalidate。Result ledger 依序保留 `ATTEMPTED`/`COMPLETED`/`FAILED`；若前段 cleanup 已完成而後段失敗，會回報 partial success 與 recovery，而不宣稱 rollback。

## 用 sweep 做定期 cleanup

```bash
dev sweep --merged-worktrees
# 把確切的 candidates/blockers 呈現給使用者，並徵求核可。
dev sweep --merged-worktrees --apply --yes
```

這會同時列出 task-tracked 的 DONE worktree，以及 named branch 已被 base 包含的 unmanaged linked worktree——請從 canonical checkout 執行。它會先回報再套用：containment 本身絕不等於許可。Dirty 的 Git state、pending 或無法到達的 artifact、locked 或 prunable 的 worktree registration、進行中的 Git operation，以及與 `dev retire` 相同的 runtime 拒絕條件，都仍會阻擋 cleanup。Retirement 完成後預設保留 branch；只有在使用者另外核可刪除時，才加上 `--delete-branches`。

### 經驗證的 Claude Workflow ephemeral cleanup

Claude Workflow 的 turn-scoped worktree 使用另一條 strict V1 路徑：

```bash
dev sweep --ephemeral-worktrees --stale-days 14
dev sweep --ephemeral-worktrees --json
dev sweep --ephemeral-worktrees --apply
dev sweep --ephemeral-worktrees --apply --delete-branches --base main
```

此 command 只能從 canonical non-bare checkout 執行。Bounded、fixed-depth adapter
只讀取 `~/.claude/projects` 下的 private metadata，驗證 workflow/agent ID、exact
canonical worktree mapping、`spawnedWithWorktree`、`isolation=worktree` 與 matching
journal linkage；絕不 decode 或輸出 prompt、script、log、result body 或 transcript
content。Unknown add-only fields 可接受；required type 錯誤、path mismatch、duplicate
claim、不安全／symlink／reparse／group-or-world-writable metadata、讀取中 mutation、
bound exhaustion，以及 future/conflicting/unparseable time 都會 fail closed。

V1 terminal liveness 要求 workflow 為 `completed|killed`、matching agent 為 `done`、
journal 同時有一筆 `started` 與 `result`，且不存在 same-ID resumed transcript。
Killed 但沒有 result、progress，或已 resume 的狀態無論多舊都維持 `unknown`；沒有
attestation bypass。`--stale-days` 衡量 provider inactivity，預設 14，最小 1。

Path continuity 無法證明 current-provider ownership。Apply 另外要求 provider-observed
branch、HEAD、common-dir 與 opaque non-replayable registration generation 都和 live
registry 一致。Claude Code 2.1.259 不會記錄這些 Git identity facts，因此目前 Claude
claim 會回報 `provider-git-identity: unknown`，即使要求 `--apply` 也只供 report。
這可防止 stale terminal metadata 綁到同一路徑的 replacement checkout。Path、branch
convention 或可重用的 GitDir pathname 都不能取代缺少的 generation。

獨立 safety audit 還要求 worktree present、registered、non-main、named、unlocked、
non-prunable，並且 common-dir、live branch、registry HEAD、live HEAD 完全一致。
Staged、unstaged、conflicted、untracked、ignored 或 recursively inspected submodule
content 都會阻擋；Git operation、task claim、不安全 artifact intent、caller
containment、unknown runtime inventory 或任何 covering runtime 也會阻擋。Missing、
prunable、unregistered 與 orphan path 僅供 report。

JSON schema 1 只包含 normalized identity/state/time、Git/path/branch/HEAD facts、
checks、actions、diagnostics、fingerprints 與 counts，且只供 report。Apply 拒絕
`--yes`、`--close-unknown`、`--assume-no-runtime`、`--no-runtime` 與 JSON；它要求
terminal 並逐項確認。在 common-dir cleanup lock 下，每次 remove 前都會重新 discover
repository，並重新收集 provider、Git、task、artifact、runtime 與 caller proof；
fingerprint 改變時回報 `skipped-changed`。

Removal 使用 plain `git worktree remove`、不加 force，並驗證 path 與 registration 都已
消失。它不會關閉 session、prune、刪除 Claude metadata，或 rescue/stash/commit dirty
work。Named branch 預設保留，因此 unique commits 仍可復原。Optional deletion 另外要求
`--delete-branches --base <ref>`、unchanged base/branch tips、containment、zero unique
commits 與 ordinary `git branch -d`；remove 後任何 failure 都會保留 branch，並回報
partial completion。

## 安全邊界

原始的 `git worktree remove --force` 會完全繞過 dev——絕不要在佔用 target 的 agent 中執行它。dev 的保證僅止於：沒有任何 dev-mediated 路徑會執行 forced removal；它無法阻止 operator 或 script 直接呼叫 Git。Bare dashboard 的 configured `[[tui.tools]]` command 同樣是任意 external-tool escape boundary，不會繼承 Flow 的 PlanID、conditions、ledger 或 revalidation。

一般 task-backed retirement 使用 `internal/taskflow`。Explicit unmanaged path 形式的 `dev retire` 仍是隔離的 compatibility implementation，部分 `sweep` record-only/orphan-salvage actions 也仍在 taskflow 之外。既有 CLI acknowledgement flags 維持相容，但 Flow preview 不提供其中任何一個；不能把本頁的 shared planner claim 擴張到所有 historical cleanup path。

曾經真實發生過：一個 Codex session 從另一個 checkout 刪除了自己已註冊的 worktree 與 branch。Herdr 仍讓該 workspace 與 terminal 保持存活，因為 Unix process 在路徑被 unlink 之後仍可持有開啟中的 cwd inode；接著 SpecStory 又在同一路徑重新建立、但內容只剩 `.specstory/`。這個 shell 看起來還活著，但已經不再是一個 Git checkout：

```text
failed to reload config: No such file or directory
fatal: not a git repository (or any of the parent directories): .git
```

請把「runtime 存活 + Git registration 不存在 + artifact-only path」視為需要 transcript salvage 與外部 reconciliation 的 orphan——**絕不能**視為 RETIRED。

`dev sweep` 現在會自行辨識這個形狀，不再只能靠人眼察覺。當某個 task 記錄的 checkout 存在、但 Git 並未註冊它，且該目錄裡只有 agent artifact 資料夾時，sweep 會將它回報為 abandoned agent workspace。只有在其中每個檔案都與 repository 已有的檔案 byte-identical 時，sweep 才會提議移除；任何內容不同的檔案都會被列為 salvage 工作並保持原狀，`--apply` 也不會動它。

這裡採用 byte equality 而非僅檢查檔案是否存在，正是重點所在。比 worktree 活得更久的 transcript writer，通常會 flush 出比先前 commit 版本更長的最終稿，因此 repository 中存在同名檔案並不能證明結尾內容已被保存。

## 來源

- [`internal/skill/dev-cli/references/agent-retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/agent-retirement.md)
- [`internal/help/topics/retirement.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/retirement.md)
- [`internal/cli/retire.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/retire.go)
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/taskflow/retire.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/taskflow/retire.go)
- [`internal/cli/artifact.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/artifact.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
- [`internal/ephemeral`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/ephemeral)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/retire/safety.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/safety.go)
- [`internal/retire/service.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/service.go)
- [`internal/retire/audit.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/audit.go)
- [`internal/cli/prompt_command.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/prompt_command.go)
- [`internal/gitx/transactions.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/gitx/transactions.go)
