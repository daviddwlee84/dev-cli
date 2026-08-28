---
description: 比較同一 conversation 內的 delegated subagent，與由 human 管理的 Agent view background sessions。
authority: anthropic-docs
status: research-preview-partial
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
lang: zh-TW
---

# Subagent 與 Agent View

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Subagent 讓 main conversation delegate 一個 focused task 並取得 result；Agent view 讓 human dispatch、monitor 並 attach 多個 independent background sessions。

!!! warning "Agent view 是 research preview"
    Commands、UI、lifecycle 與 defaults 都可能改變。請保留 manual sessions/worktrees fallback，並依已安裝 Claude Code version 驗證。

## 模型比較

| Dimension | Subagent | Agent view background session |
|---|---|---|
| owner/coordinator | parent conversation | 使用 `claude agents` 的 human |
| context | independent child context；fork 可繼承 parent conversation | independent full session |
| result path | summary/result 回到 parent | session 保留供 human review/attach |
| communication | parent 收集；named agents 可向支援的 peers message | human 或 cross-session messaging |
| lifetime | delegated work/session scope | supervised background process 與 persisted session state |
| file isolation | 預設共享；可選 `isolation: worktree` | Git repo 預設使用 isolated background worktree，除非已 isolated/明確停用 |
| best use | research、logs、tests、specialist review | independent backlog tasks 或 long-running investigation |

## Subagent

一般 subagent 有自己的 context、system prompt、tool/model/permission config、delegation prompt 與 project context。它不會收到 parent 的完整 conversation 或先前 read 的 tool outputs。若 task 依賴現有討論，**forked subagent** 會從 parent conversation copy 開始。

Subagent 適合：

- 避免高 volume search/log/file output 填滿 main context；
- 平行執行 independent research 或 review；
- 定義 reusable specialist role；
- 限制 tools、model 或 permissions；
- 在 worktree 中隔離 independent writer。

最小 read-only reviewer：

```markdown
---
name: code-reviewer
description: Reviews a completed change for actionable correctness issues.
tools: Read, Grep, Glob
---

Review the requested diff. Return only evidence-backed findings.
```

只有 agent 會獨立修改 files 時才加 `isolation: worktree`。沒有 changes 的 worktree 可自動移除；有 work 的則保留，直到 safe cleanup rules 允許。

Foreground subagent 會 block parent 並傳遞 permission prompt；background subagent 可 concurrently 執行，permission request 會出現在 main session。Parent denial 不能透過另一個 agent 繞過。

## Agent view

開啟 research-preview manager：

```bash
claude agents
```

或直接 dispatch：

```bash
claude --bg --name flaky-test-fix "investigate and fix the flaky test"
```

Agent view 依 needs input、working、ready for review 或 completed 分組。你可以 peek output、reply、attach full conversation、detach 讓它繼續、stop/respawn，或重新開啟先前 repository session。

Per-user supervisor 會 host background sessions，使其能在 view 或 shell 關閉後繼續。Persistence 不等於成功；接受 completed state 前仍要檢查 transcript/diff 與 verification。

### File isolation

Git repository 中，dispatched background session 通常會在 `.claude/worktrees/` 得到 worktree。Session 已在 linked worktree、directory 不是 Git 且沒有 VCS hook，或明確停用 background isolation 時可能跳過。

除非多個 writing sessions 的 file ownership 確定互不重疊，不要停用 isolation。即使有 worktree，non-file resources 仍需要 unique ownership。

## 操作 checklist

- 每位 worker 都要有 self-contained prompt、paths、acceptance criteria 與 test command。
- 明確指出 read-only，或允許 write/commit/push。
- 使用 Agent view 時，替 independent task 命名 session，並各自使用 branch/worktree。
- 只檢查 needs input 的 sessions，不要被動 polling 每份 transcript。
- Cleanup 前保存 useful commits，確認沒有 dirty/untracked work。
- Review 後 stop/close background runtimes；“completed” 不代表 resource cleanup。

## 來源

- [Create custom subagents](https://code.claude.com/docs/en/sub-agents)
- [Manage multiple agents with Agent view](https://code.claude.com/docs/en/agent-view)
- [Run agents in parallel](https://code.claude.com/docs/en/agents)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
