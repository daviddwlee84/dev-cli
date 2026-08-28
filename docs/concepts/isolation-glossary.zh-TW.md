---
description: 分辨 branch、worktree、runtime、agent context、task 與它們仍會共享的 state。
authority: project
status: stable
verified_on: 2026-08-28
lang: zh-TW
---

# 隔離邊界與詞彙

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

隔離是分層的。另一個 worktree 不會自動建立另一個 repository、process namespace、service stack 或 coordination protocol。

## 邊界矩陣

| Primitive | 隔離內容 | 仍然共享 |
|---|---|---|
| Git branch | 具名 commit history | repository object database、refs namespace、remotes |
| linked worktree | working files、index、checked-out `HEAD` | objects、多數 refs/config、hooks、外部 services |
| runtime session | terminal process tree 與呈現 | checkout、Git state、ports、databases、credentials |
| subagent context | model conversation/context window | 未要求 isolation 時的 checkout、process environment |
| agent team | task/message coordination | 預設共享 files；teammate 不會自動使用 worktree isolation |
| Dynamic Workflow | 可重跑 orchestration 與 joins | 由 workflow author 選擇的 mutation boundaries |
| `dev` task | lifecycle intent 與 reconstruction metadata | 真正的 Git/runtime facts，會即時推導 |

## 核心詞彙

### 變更流 (change stream)

一條獨立、可 review 的改動線。在 `dev` 中通常對應一個 branch，並可選擇一個 worktree；它是持久 history 的 ownership 單位。

### Branch

指向 commit 的可移動 Git ref。它隔離 history，不隔離 working files。正常情況下，同一個 branch 不應同時被兩個 checkout 使用。

### Worktree

登記在同一個 Git repository 的 working directory。Linked worktree 有自己的 files、index 與 `HEAD`，但共享 repository storage 與許多 administrative state。

### Runtime

呈現 checkout 的即時 host 環境，例如 Herdr、tmux 或目前 shell。Runtime 消失不會改變 task 的 durability。

### Agent、subagent 與 teammate

- **Agent** 可以泛指 coding-agent session；在 Claude Code tool terminology 中，也可指由 `Agent` tool 啟動的 worker。
- **Subagent** 在 parent session 中以獨立 context 執行 delegated work。
- **Teammate** 是 experimental agent team 中的 peer Claude Code session，透過 lead、task list 與 messages 協調。

這些詞描述 context 與 coordination，不代表自動 file isolation。

### Task

這個詞有多個不同含義：

- `dev task`：專案持久的 change-stream record。
- `TaskCreate`、`TaskGet`、`TaskList`、`TaskUpdate`：session 或 team 使用的 structured coordination records。
- `/tasks` 與 `TaskStop`：background work 的查看與控制。
- 舊版 Claude Code `Task` tool：已改名為 `Agent`；舊文件仍可能使用原名。

### Workflow

這個詞也有多義：

- **GitHub Flow：**branch 與 pull request 的 collaboration model。
- **Common workflow：**非正式、可重複的 prompt 或操作 recipe。
- **Dynamic Workflow：**Claude Code 可重跑 multi-agent graph 的 JavaScript orchestration runtime。

## Worktree 不會隔離什麼

Working directory 之外的資源需要不同值或明確 ownership：

- TCP ports 與 Unix sockets；
- local databases 與 test containers；
- caches 與 generated artifact directories；
- cloud accounts、queues 與 deployment environments；
- repository hooks 與 shared refs；
- 多位 writer 都可能重新產生的 formatter/codegen output。

如果兩個 worker 都能修改其中一項，增加 worktree 仍不足以避免碰撞。

## 相關頁面

- [心智模型與生命週期](mental-model.md)
- [Worktree 語義與復原](../git/worktree-semantics-recovery.md)
- [平行工作決策指南](../claude/parallel-work-chooser.md)

## 來源

- [Git worktree 文件](https://git-scm.com/docs/git-worktree)
- [Claude Code tools reference](https://code.claude.com/docs/en/tools-reference)
- [Claude Code agent teams](https://code.claude.com/docs/en/agent-teams)
- [Claude Code Dynamic Workflows](https://code.claude.com/docs/en/workflows)
