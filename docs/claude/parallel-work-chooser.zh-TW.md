---
description: 在單一 session、subagent、Agent view、agent team、Dynamic Workflow 與 worktree 之間選擇正確平行工作方式。
authority: anthropic-docs-and-project-policy
status: evolving
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
lang: zh-TW
---

# 平行工作決策指南

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

選擇能安全分離變動的最小 coordination surface。更多 agents 能增加 context capacity 與 throughput，也會成倍增加 tokens、integration cost 與 failure surfaces。

!!! info "時效"
    Agent view 是 **research preview**；agent teams 是**experimental 且預設關閉**；Dynamic Workflows 需要 Claude Code v2.1.154+。把這些 surface 納入 team standard 前，應重新核對官方文件。

## 先問四個問題

1. **誰負責協調？** 你、main Claude session、team lead，或 script？
2. **Workers 是否需要彼此溝通？** 只回傳 independent results 比維持 peer coordination 便宜。
3. **是否可能修改同一批 files 或 external state？** 是或未知時，要拆 branch/worktree 或 serialize。
4. **工作是 one-off 還是大規模可重跑？** 少量 tasks 很少需要 scripted workflow。

## 選擇 surface

| 需求 | 使用 | 原因 |
|---|---|---|
| 一個 coherent task、需要頻繁 feedback | main conversation | coordination overhead 最低 |
| 搜尋/test/review 的細節不應填滿 main context | subagent | independent context，回傳 concise result |
| 想 dispatch 與監看多個 independent sessions | Agent view | 一個 UI 管理 human-owned background sessions；research preview |
| peers 必須分享 findings、tasks 與 messages | agent team | lead 加 communicating teammates；experimental |
| 大規模可重跑 fan-out/join 或 adversarial verification | Dynamic Workflow | JavaScript script 擁有 orchestration 與 intermediate results |
| independent writers 需要 file-state separation | 在適當 surface 搭配 worktrees | files/index/HEAD 的 support layer，不是 coordination style |
| 一個長時間 build/log watcher | background `Bash` 或 `Monitor` | 不需要額外 reasoning context |

## 變動判斷

```text
read-only worker？
  是 ─► 通常可以共用 checkout
  否
   │
files 與 generated/external state 確定互不重疊？
  是 ─► 一個 branch/worktree 加明確 ownership
  否或未知 ─► 每條 change stream 使用獨立 branch + worktree
```

Worktree 分開仍可能因 port、database、cache、hook、shared ref、cloud account、queue 或 deployment target 碰撞；這些資源要另外指派。

## 建議 pattern

### Implementation 前先 research

執行多個 independent read-only subagents，收集 conclusions，再由一位 owner 實作。這能增加觀點，但不建立 merge work。

### 同一 feature、不同 modules

使用一個 `dev` worktree/branch 加多個 panes 或 subagents。每位 writer 都要有 path/symbol contract，並指定一位 agent 管理 shared interface 與 final tests。

### 互斥 hypothesis 或 design

使用獨立 contexts。Read-only debugging 可用 subagents 或 team 互相挑戰，不必增加 worktree；競爭 implementations 則每個方案一個 branch/worktree，review 後只保留選中結果。

### Independent backlog items

Human 要 dispatch 並稍後回來看多個 sessions 時使用 Agent view。每項工作應有自己的 branch/worktree 與 acceptance test。

### Cross-checked audit 或 migration

當 script 應 fan out 多個 items、收集 structured results、讓 independent agents 驗證 findings 並可重跑同一 graph 時，使用 Dynamic Workflow。擴大前先試小範圍。

## Agent team guardrails

Agent-team teammates 不會自動獲得 worktree isolation。Launch 前分割 files 與 shared generated artifacts。只有 peer communication 真有價值時才從 3–5 teammates 開始；否則使用 subagents。Lead 仍是 integration owner，必須等待 results、檢查 diffs、執行 combined verification 並 shutdown teammates。

## Workflow guardrails

- Workflow run 前要求 explicit human opt-in。
- Script 要可閱讀，phases 要可見。
- 限制 concurrency、total agents、turns 與 cost。
- Stopped/failed agent result 要當成 missing，不是 successful。
- 每個 dimension 完成 review 後立刻獨立驗證。
- Destructive 或 outward-facing actions 不放在 unsupervised fan-out。

## 與 dev-cli 的 worktree ownership

Human 明天可能 review/resume 的 durable stream 使用 `dev`；harness 應擁有 disposable isolated experiment 時使用 Claude Code worktree。不要只因為 launch 另一個 agent，就在 `dev` worktree 內再巢狀建立 harness worktree。

## 來源

- [Claude Code：平行執行 agents](https://code.claude.com/docs/en/agents)
- [Subagents](https://code.claude.com/docs/en/sub-agents)
- [Agent view](https://code.claude.com/docs/en/agent-view)
- [Agent teams](https://code.claude.com/docs/en/agent-teams)
- [Dynamic Workflows](https://code.claude.com/docs/en/workflows)
- [`internal/help/topics/agents.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/agents.md)
