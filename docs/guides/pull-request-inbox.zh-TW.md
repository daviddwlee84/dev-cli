---
description: 列出你開的與等你 review 的 pull request，把它們對應到本地 worktree，並交給你自己設定的 agent。
authority: project
status: stable
verified_on: 2026-09-01
lang: zh-TW
---

# Pull request inbox

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev pr` 顯示正在等你的 pull request 與 merge request，以及它們屬於你的哪一個 worktree。它不會改變任何東西。

!!! info "時效"
    **Authority：**`internal/forge`、`internal/cli/pr*.go` 與其測試 · **Status：**stable · **Verified：**2026-09-01。

## 問題

開一個 pull request 通常就是一個 worktree 使命的終點，但本地沒有任何東西會這樣告訴你。Branch 已經 push、後續由 review 決定，checkout 就留在磁碟上。同時 request 從兩個方向累積：你開的、在等別人的，以及在等你 review 的。

```bash
dev pr list
```

```text
PR                      TITLE          ROLE    STATE   CHECKS  REVIEW    LOCAL       UPDATED
github:owner/api#12     Add retry      mine    open    pass    approved  ~/Worktr…   2026-09-01
github:owner/web#31     Fix parse      review  open    fail    —         —           2026-08-30
```

## 兩個 surface，以及為什麼某一欄會是空的

Provider 提供兩種不同的列表，而這個差別不只是外觀問題。

| | `--scope account` | `--scope local` |
|---|---|---|
| 成本 | 2 次呼叫，涵蓋整個帳號 | 每個 repository 1 次呼叫 |
| 涵蓋範圍 | 帳號看得到的所有 repo | 有 `dev` task 的 repo |
| head branch、review、checks | **不提供** | 提供 |
| state | 只有 open | open、merged、closed、all |

`gh search prs` 根本無法回傳 head branch、review decision 或 check status。所以 account scope 的列在那幾欄顯示 `—`，代表**這個 surface 不知道**，而不是「那裡沒有東西」。每一列都在 `detail` 欄位記錄它是由哪個 surface 產生的：`summary` 或 `full`。

`--scope all` 是預設值，兩者都跑，同一個 request 由 per-repository 的列覆蓋 account 的列。

Per-repository 查詢只限於 `dev` 有 task 的 repository。若對 `paths.scan_roots` 底下所有 repository 都查一次，就是每個 repository 一個 subprocess；在檔案很多的機器上，那是為了你根本沒問的資料而發出數十次呼叫。要全掃描請用 `--all-repos`。

```bash
dev pr list --scope account          # 便宜、涵蓋整個帳號、沒有 branch
dev pr list --scope local            # 有 branch 與 checks，只含 engaged repo
dev pr list --repo owner/api         # 單一 repository
dev pr list --linked                 # 只列有本地 checkout 的 request
```

## 對一個 request 採取行動

`dev` 只印出指令，永遠不執行它們：

```bash
dev pr list --actions
dev pr list --json | jq -r '.pull_requests[].actions.merge'
```

Approve 與 merge 是決定，不是便利功能。觸發 review 的 comment 也一樣 — 各家 reviewer 期待的字串請見 [AI pull-request review options](../notes/ai-pr-review-options.zh-TW.md)。

## 收掉已 merge request 背後的 worktree

`dev pr` 會報告某個 request 已經 merge。它不會 retire 任何東西，而且 `dev sweep` 也不會去查它：

```bash
dev pr list --scope local --state merged   # 候選
dev sweep --merged-worktrees               # 先證明 containment，先報告
```

這個分離是刻意的。當一個 request 被 squash 之後，merge 出來的 commit 並不是本地 branch 的 ancestor，所以「forge 說 merged」無法證明這份工作能從 remote 復原。`dev sweep --merged-worktrees` 用 `git merge-base --is-ancestor` 在本地證明 containment，而 `dev done --merged` 需要明確的 `--confirm-squash` attestation。把 PR 清單當成「該去看一下」的提示，而不是「可以刪」的許可。

## 把佇列交給 agent

```bash
dev pr prompt                  # triage prompt 輸出到 stdout
dev pr prompt review           # 處理你的 review 佇列
dev pr prompt retire           # 哪些 checkout 可以收掉
```

Prompt 內含即時佇列的 JSON，所以可以 pipe 到任何地方：

```bash
dev pr prompt | pbcopy
dev pr prompt retire > /tmp/queue.md
```

加上 `--agent` 就會交給你設定的指令：

```toml
[[agent]]
name = "claude"
command = ["claude", "-p"]
default = true

[[agent]]
name = "codex"
command = ["codex", "exec", "--file", "{{prompt_file}}"]
input = "file"
timeout = "10m"
```

```bash
dev pr prompt retire --agent claude
dev pr prompt retire --agent codex --dry-run   # 顯示指令，不執行
```

這裡**沒有內建 agent，也沒有預設項目**。`dev` 渲染 prompt 並啟動你定義的指令；它不讀回應，也不做 loop。內建一個預設值會讓 `dev` 依賴某一個特定工具。

Prompt 預設從 agent 的 stdin 進入。`input = "file"` 會寫一個私有暫存檔並替換 `{{prompt_file}}`。`run = "…"` 接受 shell 指令字串而非 argv，但 prompt 永遠不會被插進去 — 這些 prompt 本來就內含 shell 指令，所以 `input = "argv"` 必須搭配 `command` 形式。

`[[agent]]` 屬於 host 設定。Repository 不能定義它：它指名一個 `dev` 會執行的指令，因此 `.dev-cli/config.toml` 會拒絕這個 section。

## 定時執行

這裡刻意沒有 daemon，也沒有內建排程。`dev pr list` 只是一個普通查詢，所以週期性執行交給你原本就在用的東西：

```bash
*/30 * * * * dev pr list --json > ~/.cache/pr-inbox.json
```

## 當某個 provider 未登入

`gh` 與 `glab` 是選用且彼此獨立的。未登入的那一個會在表格下方被報告，並附上修復指令，另一個仍然照常列出：

```text
  gitlab: signed out; run `glab auth login --hostname gitlab.com`
```

`dev doctor` 在你開始之前就會報告同樣的狀態。在 `--json` 中，這個資訊放在 `providers` 陣列 — 在斷定佇列是空的之前，先讀它。

## 結構化輸出

`dev pr list --json` 輸出的是物件而不是陣列，這樣使用者才能分辨「沒有 request」與「GitLab 未登入」：

```json
{
  "generated_at": "2026-09-01T12:00:00Z",
  "scope": "all", "state": "open",
  "providers":     [{"forge": "gitlab", "status": "unauthenticated", "action": "run `glab auth login ...`"}],
  "pull_requests": [{"forge": "github", "repo": "owner/api", "number": 12, "detail": "full",
                     "head_branch": "feat/retry", "review_decision": "approved", "checks": "passing",
                     "local": {"task_id": "retry", "checkout": "…", "dirty": false},
                     "actions": {"merge": "gh pr merge 12 --repo owner/api --squash"}}]
}
```

欄位只會新增，不會被改名或移除，與 `dev ls --json` 是同一份相容性契約。

## 已知限制

- GitLab 的列永遠沒有 `checks`：pipeline status 只出現在 GitLab 的單一 merge request endpoint，而 `dev` 不會為每個 request 各發一次請求。
- 不列出 Azure DevOps 的 request。已設定的 target 會被報告為 unsupported，而不是讓指令失敗。
- `--state merged` 與 `--state closed` 需要 per-repository surface，因此指定它們時會自動選用該 surface。

## 相關頁面

- [AI pull-request review options](../notes/ai-pr-review-options.zh-TW.md)
- [Agent-safe retirement](agent-safe-retirement.zh-TW.md)
- [Change-stream workflow](change-stream-workflow.zh-TW.md)
- [Compatibility and known limitations](../reference/compatibility.zh-TW.md)
