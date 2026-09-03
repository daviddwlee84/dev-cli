---
description: 依觸發字串、是否需要 workflow、前置條件與費用，比較 Claude Code Review、Claude Code GitHub Action、Codex code review 與 CodeRabbit。
authority: anthropic-docs-and-project-policy
status: evolving
verified_on: 2026-09-01
tested_with: Claude Code Review research preview, claude-code-action v1, Codex GitHub integration, CodeRabbit docs as of 2026-09-01
lang: zh-TW
---

# AI pull-request review options

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

有四個產品會用模型 review pull request。它們之間有一個結構性差異，而這個差異決定了大部分的選擇：你需不需要自己維護一份 workflow 檔案。

!!! info "時效"
    **Authority：**各廠商官方文件，加上明確標示的本專案建議 · **Status：**evolving，這些產品變動很快 · **Verified：**2026-09-01。依賴這些資訊前請重新確認 trigger phrase 與 plan tier；下表每一列都是在該日期直接讀廠商官方文件而來。

## 決策表

| | 需要 workflow？ | Trigger | 前置條件 | 費用模式 |
|---|---|---|---|---|
| **Claude Code Review** | 否 | PR 的 top-level comment 以 `@claude review` 開頭，或自動 | Team 或 Enterprise plan；research preview；啟用 zero data retention 時無法使用 | 依 token usage 計費，與 plan 內含用量分開；一次 review 約 $15–25 |
| **Claude Code GitHub Action** | **是** | comment 中的 `@claude`（可設定），或用 `prompt` input 免 mention 直接執行 | Repository admin 權限；`ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN` | GitHub Actions 分鐘數加上 API tokens；或用 OAuth token 走你的 subscription |
| **Codex code review** | 否 | PR comment 中的 `@codex review`，或開啟 *Automatic reviews* 設定 | 在 Codex 中連接 GitHub | Codex 用量 |
| **CodeRabbit** | 否 | 對 primary branch 開 PR 時自動觸發；`@coderabbitai review` 或 `@coderabbitai full review` | 從 CodeRabbit dashboard 安裝 GitHub App | 依 plan；有 OSS plan |

## 真正的分界

**Managed GitHub App**（Claude Code Review、Codex code review、CodeRabbit）收到 webhook 就 review。你的 repository 裡沒有東西要維護，廠商改版時也沒有東西要跟著更新。

**GitHub Action**（`anthropics/claude-code-action@v1`）跑在你自己的 CI 裡。你要維護 workflow 檔案、要付 runner 分鐘數，換來的是 reviewer 做不到的事：跑你的 tests、push 一個修正、開一個 follow-up PR。它不是「順便會改東西的 reviewer」，而是「剛好在做 review 的 agent」。

用「finding 出現之後你想發生什麼」來選。如果答案是「有人去讀它」，選 managed app。如果答案是「修好並 push」，你要的是 Action。

## 把 trigger 下對

最常見的失敗是：一句看起來沒問題、但什麼都不會發生的話。

- Claude Code Review 要求 comment **以 `@claude review` 開頭**，而且必須是 top-level comment，不能是 inline comment。PR 必須是 open 且非 draft，留言者需要 owner、member 或 collaborator 權限。`@claude please review this` 不是官方文件所定義的 trigger。
- Claude Code GitHub Action 只對作為**完整單字**的 `@claude` 有反應，`/claude` 與 `@claude-bot` 都不算，而且留言者必須有 write access。預設字串可以用 `trigger_phrase` 修改。
- 這兩者是共用同一個 GitHub App 的**不同產品**。裝了 App 不代表啟用了 Code Review，啟用 Code Review 也不會給你 Action。如果只看到 `👀` reaction 卻沒有 review，通常代表 App 已安裝，但你以為會跑的那個產品並沒有啟用。
- CodeRabbit 區分 `@coderabbitai review`（只看自上次 review 後的變更）與 `@coderabbitai full review`（整份重看，忽略它自己先前的留言）。

## `dev` 在這裡的位置

`dev` 不安裝 App、不寫 workflow，也不 review 任何東西。它只告訴你哪些 request 在等，並產生觸發你所選 reviewer 的那句 comment：

```bash
dev pr list                    # 有哪些 open，各自屬於哪個 worktree
dev pr list --actions          # gh/glab 指令，包含 `pr comment`
```

送出 trigger 就只是一則普通 comment：

```bash
gh pr comment 12 --repo owner/name --body '@claude review'
gh pr comment 12 --repo owner/name --body '@codex review'
gh pr comment 12 --repo owner/name --body '@coderabbitai full review'
```

`dev` 只會印出這些指令，永遠不會替你執行。要求一次付費 review 是一個決定，而以 Claude Code Review 一次 $15–25 來說，這不是可以代替別人做的決定。

## 本文沒有主張的事

- 這裡不比較 review **品質**。品質取決於 codebase、diff 與 repository guidance 檔案的程度，高於取決於廠商。
- 價格與 plan tier 會變動。上表數字是廠商自己的文件在驗證日期當天的說法，不是報價。
- 有一個流傳很廣的說法：CodeRabbit 對 star 數低於某個門檻的 public repository 只能手動觸發。這在 CodeRabbit 官方文件中無法確認，因此本文刻意不收錄。

## 來源

- [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions)：setup、`@claude` trigger、`trigger_phrase`、secrets、誰可以觸發、費用模式。
- [Set up Code Review for Claude Code](https://support.claude.com/en/articles/14233555-set-up-code-review-for-claude-code)：Team/Enterprise research preview、`@claude review` 前置條件、頻率設定、token 計費。
- [Review GitHub pull requests with Codex](https://developers.openai.com/codex/integrations/github)：`@codex review`、Automatic reviews 設定。
- [CodeRabbit commands](https://docs.coderabbit.ai/guides/commands)：`review` 與 `full review` 的差別。
- [CodeRabbit FAQ](https://docs.coderabbit.ai/faq)：GitHub App 安裝、對 primary branch 自動 review、不需要 workflow。

## 相關頁面

- [Pull request inbox](../guides/pull-request-inbox.zh-TW.md)
- [GitHub Flow](../git/github-flow.zh-TW.md)
- [Sources and freshness](../reference/sources-freshness.zh-TW.md)
