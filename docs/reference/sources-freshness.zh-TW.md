---
description: 定義 dev-cli、Git、GitHub 與 Claude Code claims 背後的 authority levels、freshness metadata 與 source matrix。
authority: project-policy
status: maintained
verified_on: 2026-08-28
lang: zh-TW
---

# 來源與時效

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

本站同時包含 product documentation、external specification、快速變動的 harness behavior、project policy 與 history。每頁都要標明 statement 類型與查核日期。

## Authority 順序

1. **dev-cli behavior：**repository code、tests、E2E 與 generated command reference。
2. **Git semantics：**符合 installed Git version 的現行 `git-scm.com` manual。
3. **GitHub collaboration：**現行 GitHub Docs。
4. **Claude Code behavior：**現行 Anthropic 文件，並標示 preview/experimental/version。
5. **Standards：**具名且有版本的規格，例如 Conventional Commits 1.0.0、SemVer 2.0.0。
6. **Historical context：**有日期的 archives 與舊 project essays，絕不當成 current operating rule。
7. **Project policy：**明確標籤的建議，不冒充 upstream guarantee。

較低層 authority 可解釋 motivation，但不能覆蓋較高層 implementation claim。

## 必要 page metadata

```yaml
---
description: 能在 search 與 llms.txt 獨立成立的一句話。
authority: 使用下方 authority table 的單一值
status: 使用下方 status table 的單一值
verified_on: YYYY-MM-DD
minimum_version: optional
tested_with: optional
---
```

| `authority` value | 意義 |
|---|---|
| `project` | 由 code/test 定義的 dev-cli behavior |
| `project-policy` | 本專案 recommendation |
| `git-scm`、`github-docs`、`anthropic-docs` | 一個現行 upstream authority |
| `git-and-project-policy`、`anthropic-docs-and-project-policy` | upstream semantics 加明確標示的 local recommendation |
| `project-and-upstream` | 同時涵蓋 implementation 與 upstream status 的 compatibility page |

| `status` value | 意義 |
|---|---|
| `stable`、`maintained` | 有 maintenance expectation 的 current project content |
| `evolving` | 可能快速變更的 current behavior |
| `official` | 現行 upstream normative/documented behavior |
| `research-preview-partial` | page 包含 research-preview surface |
| `experimental-and-versioned` | 有 explicit version boundary 的 experimental feature |
| `generated-plus-authored` | authored guidance 中 embed generated reference |

`verified_on` 必須是有效、且不晚於目前 UTC calendar date 的 ISO date。English/zh-TW sibling 的 authority、status、date、minimum version 與 tested version 必須相同。以 Anthropic docs 為 authority 或含 preview/experimental status 的頁面，必須有 `minimum_version` 或 `tested_with`。Docs checker 會 enforce 這些規則、nav membership 與 bilingual file parity。

## Claim/source matrix

| Topic 或 claim | Owning page | Primary authority | Checked status |
|---|---|---|---|
| HOT/WARM/COLD/DONE 與 checkout modes | [心智模型](../concepts/mental-model.md) | `internal/task/task.go`、lifecycle CLI/tests | repository snapshot 2026-08-28 |
| `done --pr` 保持 task active | [變更流 workflow](../guides/change-stream-workflow.md) | `internal/cli/done.go` | implemented |
| worktree provisioning safety | [Worktree 與 provisioning](../guides/worktrees-provisioning.md) | `internal/wt/plan.go`、`ecosystem.go`、`provision.go` | implemented |
| runtime fallback Herdr → tmux → Zellij → none | [Parallel agents 與 runtimes](../guides/parallel-agents-runtimes.md) | `internal/runtime/runtime.go` | implemented |
| 現行 GitHub Flow 有六個 branch/PR steps 且沒有 deployment step | [GitHub Flow](../git/github-flow.md) | [GitHub Docs](https://docs.github.com/en/get-started/using-github/github-flow) | official，2026-08-28 查核 |
| linked worktree 共享 repository data，但有自己的 files/index/HEAD | [Worktree semantics](../git/worktree-semantics-recovery.md) | [`git-worktree`](https://git-scm.com/docs/git-worktree) | official，2026-08-28 查核 |
| Conventional Commits structure | [Branch 與 commit](../git/branches-commits-prs.md) | [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/) | versioned standard |
| Claude Code 是 agentic harness | [Agentic harness](../claude/agentic-loop-tools.md) | [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works) | official，2026-08-28 查核 |
| parallel primitive selection/status | [Parallel chooser](../claude/parallel-work-chooser.md) | [Run agents in parallel](https://code.claude.com/docs/en/agents) | version-sensitive |
| Claude worktree path/base/cleanup | [Worktree isolation](../claude/worktree-isolation.md) | [Claude Code worktrees](https://code.claude.com/docs/en/worktrees) | version-sensitive，tested 2.1.250 |
| teams 與 Dynamic Workflows | [Teams 與 workflows](../claude/teams-dynamic-workflows.md) | [agent teams](https://code.claude.com/docs/en/agent-teams)、[workflows](https://code.claude.com/docs/en/workflows) | experimental/versioned |
| hooks/skills/plugins/SDK roles | [Extensions](../claude/extensions-agent-sdk.md) | Anthropic feature references | evolving |

## 歷史來源

- [`githubflow.github.io`](https://githubflow.github.io/) 保存早期「default branch 隨時可 deploy」模型。
- [2019 Wayback snapshot](https://web.archive.org/web/20191104103724/https://guides.github.com/introduction/flow/) 保存較早的 deploy-before-merge guide。

兩者對 deploy/merge order 意見不同，也使用舊 `master` terminology。它們只出現在 historical section；present-day GitHub Flow claims 由現行 GitHub Docs 負責。

## 改寫的 local material

Local `agent-skills/skills/local/git-workflow/` collection 協助探索 branch hygiene、Conventional Commits、release 與 worktree recovery 題目。內容混合 upstream rules 與 house policy，也有已知 stale claims，且沒有清楚 repository-level license grant。本站獨立改寫已驗證概念並引用 public upstream specification，不直接複製 skill。

## Refresh procedure

Code 或 upstream feature 改變時：

1. 重新閱讀 implementation/test 或 current official page。
2. 在同一 change 更新 English claim 與 zh-TW sibling。
3. 必要時更新 `verified_on`、`tested_with`、status 與這份 matrix。
4. 執行 source checker 與 strict site build。
5. 檢查 historical wording 沒有變成 normative。
6. 未解 uncertainty 要記成 limitation，不可猜測。

## Version-sensitive source set

- [Tools reference](https://code.claude.com/docs/en/tools-reference)
- [Subagents](https://code.claude.com/docs/en/sub-agents)
- [Agent view](https://code.claude.com/docs/en/agent-view)
- [Agent teams](https://code.claude.com/docs/en/agent-teams)
- [Dynamic Workflows](https://code.claude.com/docs/en/workflows)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
- [Hooks](https://code.claude.com/docs/en/hooks)
- [Skills](https://code.claude.com/docs/en/skills)
- [Plugins](https://code.claude.com/docs/en/plugins)
- [Agent SDK loop](https://code.claude.com/docs/en/agent-sdk/agent-loop)
