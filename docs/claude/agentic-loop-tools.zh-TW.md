---
description: 把 Claude Code 理解成模型外圍的 agentic harness，包含 tools、context、permissions、sessions 與 verification loop。
authority: anthropic-docs
status: official
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
lang: zh-TW
---

# Agentic Harness 與 Tools

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Claude Code 是包在 Claude model 外圍的 **agentic harness**：model 負責推理，harness 提供 tools、context management、execution environment、permissions、sessions 與 orchestration。

!!! info "公開行為，不是 proprietary internals"
    這頁只記錄 Anthropic 公開的 architecture 與 contracts，不推測 model internals 或未公開 implementation details。

## Agentic loop

```text
你的 goal
   │
   ▼
收集 context ──► 採取 action ──► 驗證 results
      ▲                               │
      └──────── 學習並重複 ◄─────────┘
```

這些 phase 會互相交錯。回答問題可能只需要收集 context；修 bug 通常會執行 tests、read/search、edit，再次測試。每個 tool result 都會成為下次 model decision 的 context；user 可隨時 interrupt 與 steer。

## Model 與 harness 的責任

| Component | 責任 |
|---|---|
| model | 理解 prompt/code、reason、選擇下一個 response 或 tool call |
| harness | 提供 tool schema、執行 approved call、回傳 result、管理 context/session/environment |
| project instructions | `CLAUDE.md`、skills、settings 等持久 conventions 與 boundaries |
| user/permission system | approve、deny、interrupt 或 redirect actions |

純文字 model 無法 edit code 或執行 test。Harness 實際執行 tools 並把結果送回 loop 後，才產生 agency。

## Tool 類別

| 類別 | 常見 tools | 角色 |
|---|---|---|
| files | `Read`、`Edit`、`Write`、`NotebookEdit` | 檢查或修改 artifacts |
| search/intelligence | `Glob`、`Grep`、`LSP` | 探索 code 與 relationships |
| execution | `Bash`、`PowerShell`、`Monitor` | 執行 build、test、Git 與持續 observation |
| web/resources | `WebSearch`、`WebFetch`、MCP resource tools | 取得 external context |
| orchestration | `Agent`、`Workflow`、`SendMessage` | delegate 或協調 independent contexts |
| work tracking | `TaskCreate`、`TaskGet`、`TaskList`、`TaskUpdate` | metadata 與 dependencies；建立 task 不會啟動 worker |
| session/control | `AskUserQuestion`、plan/worktree/scheduling tools | 改變 harness state 或要求 human decision |
| reusable procedures | `Skill` | 按需載入 instructions 與 supporting material |

實際 availability 受 surface、model、organization policy、plugin 與 session config 影響；精確 schema 以即時 tools reference 為準。

## 容易混淆的名稱

- **`Agent`：**啟動 subagent；歷史資料可能仍使用舊名 `Task`。
- **Task-list tools：**只建立 coordination records。
- **`/tasks` 與 `TaskStop`：**查看或停止 background work，可能是 command 或 agent。
- **`Workflow`：**執行會協調多個 agents 的 Dynamic Workflow script。
- **`Skill`：**在目前 conversation 載入 reusable prompt/procedure。
- **`EnterWorktree`：**改變 checkout isolation，本身不建立另一個 agent。

## Context 與 session

Session 結合 system prompt、conversation、tool definitions/results、project instructions、loaded skills 與 configured extensions。Tool output 會占用 context，因此只有 concise result 應回到 main conversation 時適合使用 subagent。Automatic compaction 會摘要較舊 context；持久規則要放 project instructions，不要只放在早期 prompt。

Sessions 互相獨立並綁定 directories。Resume 延續同一 session ID；fork 建立新的 session/history branch。Git worktree 可讓 parallel sessions 各自有 checkout，但 repository history 仍共享。

## Safety controls

- **Permissions**決定 tool call 能否執行；allow/deny rules 與 permission modes 是 hard policy boundary。
- **Checkpoints**可 rewind file edits，但不能復原 remote API、deployment 或 database effect。
- **Hooks**觀察/自動化 lifecycle events，也能阻擋特定 calls，但不能取代 permissions 或 sandboxing。
- **Worktree enforcement**可防止 isolated session edit 或把 Git redirect 到 protected main checkout。
- **Verification**是 loop 的一部分：只有 changed file、沒有相關 tests 或 inspection，不算完成工程工作。

## 可靠的 harness contract

Embedding 或操作 coding agent 時，先定義：

1. goal 與 acceptance criteria；
2. trusted context 與 freshness；
3. available/read-only/mutating tools；
4. permission 與 outward-action gates；
5. filesystem、branch 與 external-resource isolation；
6. turn、cost 與 concurrency limits；
7. verification commands 與 evidence；
8. session persistence、handoff 與 cleanup ownership。

## 來源

- [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works)
- [Tools reference](https://code.claude.com/docs/en/tools-reference)
- [Permissions](https://code.claude.com/docs/en/permissions)
- [How the Agent SDK loop works](https://code.claude.com/docs/en/agent-sdk/agent-loop)
