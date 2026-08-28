---
description: 需要 communicating peers 時選 experimental agent team，需要可重跑 scripted orchestration 時選 Dynamic Workflow。
authority: anthropic-docs
status: experimental-and-versioned
verified_on: 2026-08-28
minimum_version: Claude Code 2.1.154 for Dynamic Workflows
tested_with: Claude Code 2.1.250
lang: zh-TW
---

# Agent Team 與 Dynamic Workflow

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Agent team 由 lead 管理能互相溝通的 peer sessions；Dynamic Workflow 把 orchestration 本身寫進 JavaScript，讓 fan-out、joins 與 verification stages 都能檢查與重跑。

!!! warning "Version-sensitive features"
    Agent teams 是 experimental 且預設關閉。Dynamic Workflows 需要 v2.1.154+，也可能需要 plan/config enablement。依賴兩者前，請確認 installed version 的 availability、limits 與 permission behavior。

## Agent team

Agent team 包含：

| Component | 角色 |
|---|---|
| lead | 分解工作、assign/coordinate、synthesize 與 integrate |
| teammates | 各自有 independent context 的 Claude Code sessions |
| task list | Task tools 可用時保存 pending/in-progress/completed 與 dependencies |
| mailbox/messages | lead 與 teammates 直接 communication |

使用 `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` 明確啟用。現行版本中，teams enabled 時具名 `Agent` call 可 launch teammate。歷史 `TeamCreate` 與 `TeamDelete` 已於 v2.1.178 移除，新 automation 不應再使用。

### 適合使用 team 的情況

- peers 必須分享 discoveries 並互相 challenge；
- debugging 需要 competing hypotheses；
- frontend/backend/tests 有獨立 ownership，但也有 shared decisions；
- lead 要隨工作變化重新 assign blocked tasks。

Sequential work、same-file change，或每一步都依賴前一步結果時，使用 single session 或 subagents。

### File 與 lifecycle safety

Teammates **不會**自動獲得 worktree isolation。Launch 前先分割 files/symbols 與 shared generated artifacts。Independent writers 需要 isolation 時，明確提供不同 branch/worktree，否則 serialize mutation。

Teammate message 不能代替 human 核准 permission 或 consent。Permission prompts 會透過 lead 顯示。Findings/diffs review 完成後要 graceful shutdown；task completion 不等於 process cleanup。

現行限制包括：不支援 nested teams、每 session 只有一個 lead/team、task status 可能不完整、active call 中 shutdown 較慢，以及 in-process teammate 無法完整 resume。請保留 subagent/manual-session fallback。

## Dynamic Workflow

Dynamic Workflow 是由 workflow runtime 執行的 JavaScript script。決定下一個 agent 的是 script，不是 Claude 目前 turn；intermediate results 保存在 variables。

```javascript
export const meta = {
  name: 'review-by-dimension',
  description: 'Review changed files and independently verify findings',
}

const reviews = await pipeline(dimensions, dimension =>
  agent(`Review for ${dimension}`, { label: dimension })
)
return reviews.filter(Boolean)
```

以下情況適合 workflow：

- 工作超過少量 delegated tasks；
- 同一 orchestration 需要重跑或 audit；
- 多個 files/items 需要同一 transformation 或 review；
- independent agents 應互相 verify/challenge findings；
- intermediate output 不應進入 main context。

小型 linear task、需要 mid-run human sign-off 的 conversation，或 unsupervised destructive/outward actions 都不適合。

### Runtime constraints

現行官方文件描述的是 background isolated script runtime；script 本身不能直接操作 shell/filesystem，實際工作由 agents 執行。上限是 16 concurrent agents（仍受 local resources 影響）與每 run 1,000 agents。這些是 version-sensitive ceiling，不是建議 size；應先用小範圍與預設 medium guideline，而非針對上限設計。

Stopped 或 unrecoverably failed 的 `agent()` 會形成 missing result。Workflow 必須 validate/deduplicate output，並把無法驗證的 claim 標成 unverified，而不是當成 success。Same-session resume 可重用未變更的 completed calls；較早 stage 改變/失敗時，後續工作可能重新執行。

## Coordination ownership 比較

| Surface | 誰持有 plan？ | 適合 scale | Communication |
|---|---|---:|---|
| subagents | main Claude 每個 turn 決定 | 少量 tasks | results 回到 caller |
| skill | Claude 依 reusable instructions | 少量 tasks | current context |
| agent team | lead agent 每個 turn 決定 | 少量 long-running peers | direct messages 加 tasks |
| Dynamic Workflow | JavaScript script | 數十或更多 | structured intermediate results |

## Quality pattern

1. 探索 independent units。
2. 用 structured output fan out bounded workers。
3. 每個 finding/result 出現後立刻由 independent agent 驗證。
4. Deduplicate、rank surviving results。
5. 由一位 integration owner 依 dependency order 套用 writes。
6. 執行完整 combined verification。
7. Stop workers 並 cleanup isolated resources。

## 來源

- [Agent teams](https://code.claude.com/docs/en/agent-teams)
- [Dynamic Workflows](https://code.claude.com/docs/en/workflows)
- [Run agents in parallel](https://code.claude.com/docs/en/agents)
- [Tools reference](https://code.claude.com/docs/en/tools-reference)
