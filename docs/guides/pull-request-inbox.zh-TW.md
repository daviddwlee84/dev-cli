---
description: 列出你開的與等你 review 的 pull request，理解 provider cost 與 missing fields，並檢查 local checkout health。
authority: project
status: stable
verified_on: 2026-09-02
lang: zh-TW
---

# Pull request inbox

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev pr list` 顯示正在等你的 pull request／merge request；provider 有回報 head
branch 時，也顯示 matching local task/checkout。它不會改變任何東西。

!!! info "時效"
    **Authority：**`internal/forge`、`internal/cli/pr*.go` 與其測試 ·
    **Status：**stable · **Verified：**2026-09-02。

## 問題

開 pull request 通常代表 worktree 的 active writing phase 結束，但本地沒有任何東西
會這樣說。Branch 已 push、後續由 review 決定，checkout 仍留在磁碟。同時 requests
從兩個方向累積：你開的，以及正在等你 review 的。

```bash
dev pr list
```

```text
PR                      TITLE          ROLE    STATE   CHECKS  REVIEW    LOCAL       UPDATED
github:owner/api#12     Add retry      mine    open    pass    approved  ~/Worktr…   2026-09-01
github:owner/web#31     Fix parse      review  open    fail    —         —           2026-08-30
```

## Account 與 local surface

Provider 提供 account-wide 與 per-repository lists，fields 與 cost 不同。

| | `--scope account` | `--scope local` |
|---|---|---|
| coverage | authenticated account 的 requests | selected local repositories |
| query cost | author 與 reviewer 是分開的 role query | 每個 repository 的**每個 requested role**最多一個 paginated query |
| default roles | author + reviewer | author + reviewer（因此每個 repository 最多兩個 queries） |
| repository set | 所有 account results，再由 `--repo` filter | 有 `dev` task 的 repositories；`--all-repos` 擴大 |
| states | open | open、merged、closed、all |

`--scope all` 是預設值，會 union 兩種 surface。同一 provider/repository/number 的
request 會由較完整的 row upgrade summary row。

Provider fields 並不對稱：

- GitHub account search 產生 `detail: "summary"`，無法回報 `head_branch`、
  `review_decision` 或 `checks`。
- GitHub per-repository list 產生 `detail: "full"`，包含這些 fields。
- GitLab 的 account/repository lists 都產生 full branch/merge detail，但都沒有
  `checks`；pipeline status 只存在 single-request endpoint。這些 list surface 也不回報
  `review_decision`。

Absent field 表示 surface 沒有回報，不表示 underlying value 為空。下結論前先讀
`detail` 與 provider capabilities。

```bash
dev pr list --scope account
dev pr list --scope local
dev pr list --repo owner/api
dev pr list --repo github:owner/api
dev pr list --linked
```

`--repo` 同時 filter account results 與 local query targets。它接受 `owner/name`、
`provider:owner/name` 或 forge URL；provider-qualified value 會 pin provider。
`--linked` 表示 request 的 expected branch 確實被 checkout 且 status 成功讀取，不只是
某個 task 提到該 branch。

Account search 無法區分 merged 與 closed。若用 account/all scope 要求 `--state
merged`、`closed` 或 `all`，collection 會 narrow 到 local surface。Structured output
回報這個 **effective** scope（`"local"`），不是原先要求的較廣 scope。

## 對 request 採取行動

`dev` 只印 command，永遠不執行：

```bash
dev pr list --actions
dev pr list --json | jq -r '.pull_requests[].actions.merge'
```

Approve、merge、comment、resume 與 retire 都仍是 operator action。目前 comment action
只含 generic body placeholder（`'...'`）；`dev` 不會合成 vendor review-trigger phrase。

## 收掉 merged request 背後的 worktree

Forge 回報 request 已 merge 只是 evidence，不是 retirement authorization：

```bash
dev pr list --scope local --state merged   # candidates
dev sweep --merged-worktrees               # 證明 containment，先 report
```

Squash merge 不會讓 local feature branch 成為 base 的 ancestor，因此 forge answer 無法
單獨證明 recovery。`dev sweep --merged-worktrees` 在本地證明 containment；適用時
`dev done --merged` 需要 explicit squash attestation。把 inbox 視為 inspect 的理由，
絕不是 delete permission。

要取得 deterministic agent-readable triage，請用 generic prompt surface，不是 PR
subcommand：

```bash
dev prompt render pr-triage
dev prompt run pr-triage --agent my-agent
dev prompt open pr-triage --agent my-agent
```

Recipe、configuration、transport、TTY、permission 與 runtime boundaries 請見
[Prompt handoff](prompt-handoffs.zh-TW.md)。

## Provider availability

`gh` 與 `glab` 是選用且彼此獨立的。Signed-out provider 會在 table 下方回報 exact login
command；另一個 ready provider 仍會提供 rows。`dev doctor` 回報相同狀態。在 JSON
中，先檢查 `providers`，不要直接把 empty `pull_requests` array 解讀成 empty inbox。

目前不列出 Azure DevOps pull request。Configured Azure target 會被回報為 unsupported，
不會讓成功的 GitHub/GitLab result 失敗。

## Structured output

`dev pr list --json` 輸出 schema-versioned object，不是 bare array：

```json
{
  "schema_version": 1,
  "generated_at": "2026-09-02T12:00:00Z",
  "scope": "local",
  "state": "open",
  "roles": ["author", "reviewer"],
  "repositories": ["github:owner/api"],
  "providers": [{"forge": "github", "status": "ready"}],
  "pull_requests": [{
    "forge": "github",
    "repo": "owner/api",
    "number": 12,
    "detail": "full",
    "head_branch": "feat/retry",
    "local": {
      "task_id": "retry",
      "task_state": "hot",
      "repo_path": "<repo-path>",
      "checkout": "<checkout-path>",
      "expected_branch": "feat/retry",
      "live_branch": "feat/retry",
      "branch_checked_out": true,
      "checkout_exists": true,
      "worktree_registered": true,
      "status_available": true,
      "git": {"dirty": false, "ahead": 0, "behind": 0, "upstream": "origin/feat/retry"}
    },
    "actions": {"comment": "gh pr comment 12 --repo owner/api --body '...'"}
  }]
}
```

Top-level `scope`、`state`、`roles`、`repositories` 描述 effective collection；
`providers` 用來區分 empty inbox 與 unavailable source。

Optional `local` object 把 durable task intent 與 live checkout facts 分開：

- `expected_branch` 是 task 記錄的 branch；`live_branch` 是 status 實際看到的 branch。
- `checkout_exists`、`worktree_registered` 與 `status_available` 是各自獨立的 health gate。
- 只有 checkout 存在、仍 registered、status available，且 live branch 等於 expected
  branch 時，`branch_checked_out` 才是 true。
- `status_error` 存在時說明 unavailable/missing/unregistered state。
- `git` 是 optional；只有 expected branch 被證明 live 後才出現，包含 `dirty`、
  `ahead`、`behind` 與 optional `upstream`。

Schema version 1 是 add-only：可新增 fields，但既有 field name 與 meaning 會保留。

## Scheduling

沒有 daemon 或 built-in scheduler。`dev pr list` 是 plain read-only query，recurrence
交給 cron、launchd 或其他 scheduler：

```bash
*/30 * * * * dev pr list --json > ~/.cache/pr-inbox.json
```

## 相關頁面

- [Prompt handoff](prompt-handoffs.zh-TW.md)
- [Agent-safe retirement](agent-safe-retirement.zh-TW.md)
- [變更流 workflow](change-stream-workflow.zh-TW.md)
- [相容性與已知限制](../reference/compatibility.zh-TW.md)
