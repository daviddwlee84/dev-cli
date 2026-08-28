---
description: 依 automation、knowledge、packaging、integration 或 embedded harness 需求選擇 hook、skill、plugin、MCP 或 Agent SDK。
authority: anthropic-docs
status: evolving
verified_on: 2026-08-28
tested_with: Claude Code 2.1.250
lang: zh-TW
---

# Hooks、Skills、Plugins 與 Agent SDK

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Claude Code 的 extension mechanisms 解決不同問題：reusable instructions 放 skill；deterministic lifecycle reaction 放 hook；external capability 放 MCP；可 distribution 的 components 放 plugin；programmatic agent loop 放 Agent SDK。

## 選擇 mechanism

| 需求 | Mechanism | 執行位置 |
|---|---|---|
| reusable procedure 或 domain instructions | skill | 按需載入目前 agent context |
| lifecycle event 前後的 deterministic reaction | hook | model turn 外或旁邊的 command/HTTP/MCP/model handler |
| database、browser、API 或 custom tool | MCP server/custom tool | 以 tool schema 暴露的 external capability |
| versioned distribution 的 skills/agents/hooks/MCP/workflows | plugin | 依 scope 安裝的 namespaced package |
| 在 application 中 embed Claude Code autonomous loop | Agent SDK | application-controlled session/process |
| 一個 project-specific experiment | standalone `.claude/` configuration | current project，沒有 packaging overhead |

## Skill

Skill 是有 discoverable description 與 optional supporting files 的 `SKILL.md` procedure。Claude 可自動選擇相關 skill，user 也可執行 `/skill-name`。Description 提早可見；body 與 references 只在使用時載入，因此長 procedure 不會占用每個 turn。

Custom commands 已合併到 skills：現有 `.claude/commands/*.md` 仍可運作；directory-based skill 另提供 references、scripts、invocation controls 與 progressive disclosure。

`allowed-tools` 只在 skill 執行期間預先核准列出的 tools；它不會創造新 tool，也不能繞過 parent/organization restrictions。Skill 應協調既有 capabilities，destructive/outward action 仍需 explicit consent。

反覆貼上相同 checklist、deploy recipe、review standard 或 multi-step procedure 時使用 skill；穩定 project fact 則留在 `CLAUDE.md`。

## Hook

Hook 可在 session setup、prompt submit、tool use 前後、permission request、agent/task transition、compaction、stop 與 worktree create/remove 等 event 執行。Handler 包括 command、HTTP、MCP tool、prompt evaluation 與 experimental agent handler。

Hook 適合 deterministic actions：

- format 或 validate changed files；
- execution 前拒絕 prohibited command；
- 附加 context 或 audit record；
- enforce task-completion/teammate-idle checks；
- 實作 non-Git worktree create/remove。

Hook 補充 permissions，但不取代。沒有 decision 的 successful hook 不會 approve tool call；best-effort matcher 不是 hard sandbox；許多 hook failure/timeout 會 fail open。Committed hook code 繼承 local environment authority，因此要像 executable project code 一樣 review。

## Plugin

Plugin 會 package 與 version reusable components：skills、custom agents、hooks、MCP/LSP config、workflows、monitors、binaries 與支援的 default settings。Plugin skill 以 `/plugin-name:skill` namespace 避免 collision，也可透過 marketplace distribution。

Project-specific workflow 或快速 experiment 使用 standalone `.claude/` config；需要 version、installation、cross-project sharing 或 release 時再轉 plugin。`plugin.json` manifest 提供 identity/version/metadata；component directories 位於 plugin root，不在 `.claude-plugin/` 內。

## MCP 與 custom tools

MCP server 以 typed tools/resources 暴露 external system。Definitions、permissions、network/credential access 與 output size 都會進入 harness security/context budget。盡量 defer 或 scope tool schema，正確標示真正 read-only custom tool，也不要因 tool 名含 “read” 就推定 backend 安全。

## Agent SDK

Claude Agent SDK embed 與 Claude Code 相同的 execution loop：

1. 以 prompt、system context 與 tool definitions 初始化 session；
2. 接收含 text/tool calls 的 assistant response；
3. 執行 approved tools 並回傳 results；
4. 重複直到 text-only completion 或 turn/budget/error limit；
5. 產生帶 status、usage/cost 與 session ID 的 result。

SDK 除了 Claude Code built-in tools，也提供 permissions、hooks、subagents、MCP/custom tools、context compaction、session resume/fork、model/effort control 與 turn/budget limits。它不只是 basic API tool-calling loop，因為 harness 會管理這些 coding-agent concerns 與 execution semantics。

Production agent 應：

- 設定 `maxTurns` 與 `maxBudgetUsd`；
- 使用 least-privilege tools 與 explicit allow/deny rules；
- 對 interactive high-trust actions 暴露 approval callback；
- 保存/檢查 session IDs 與 result subtypes；
- 明確處理 failure 與 missing subagent result；
- 隔離 execution environment；
- 記錄 verification evidence，不只 final prose。

## Security boundary 摘要

- Skill 是 instructions，不是 authority。
- Hook 是 executable automation，不是 permissions 替代品。
- Plugin 是 supply/distribution unit，安裝前要 review。
- MCP/custom tool 帶有 backend credentials 與 side effects。
- Agent SDK application 負責 permission callback、limits、storage 與 environment isolation。

## 來源

- [Skills](https://code.claude.com/docs/en/skills)
- [Hooks reference](https://code.claude.com/docs/en/hooks)
- [Create plugins](https://code.claude.com/docs/en/plugins)
- [MCP](https://code.claude.com/docs/en/mcp)
- [Agent SDK loop](https://code.claude.com/docs/en/agent-sdk/agent-loop)
