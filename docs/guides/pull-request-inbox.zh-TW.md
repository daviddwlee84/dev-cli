---
description: 列出你開的與等你 review 的 pull request，理解 provider cost 與 missing fields，並檢查 local checkout health。
authority: project
status: stable
verified_on: 2026-09-24
lang: zh-TW
---

# Pull request inbox

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev pr list` 顯示正在等你的 pull request／merge request；provider 有回報 head
branch 時，也顯示 matching local task/checkout。它不會改變任何東西。

!!! info "時效"
    **Authority：**`internal/forge`、`internal/prflow`、`internal/cli/pr*.go` 與其測試 ·
    **Status：**stable · **Verified：**2026-09-25。

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

## Repository 分頁與 REMOTE tree

在 REMOTE 選取 GitHub/GitLab repository，按 **Space** 載入 PR/MR child rows。
Repository inventory 包含個人專案與有權限存取的 organization repositories；這裡也能
看見其他人開的 request，作者是自己或正在等自己 review 會另以 badge／顏色區分。

每頁預設最多 50 筆。已知 open requests 超過 50 時，automatic view 切到自己提出的
requests 與等自己 review 的 requests。畫面持續顯示目前 scope、已載入筆數、overall
count 或其下限、stale 狀態及 **Load more**。**Ctrl+O** 可改選全部 open requests、
related requests、refresh 或下一頁。Related filter 在 provider 端執行，不會因為自己的
舊 PR 不在全部清單第一頁就漏掉。`/` 只搜尋已載入 rows；badge 與 summary 根據接受的
observations，未回報的 checks 保留 unknown。

CLI 可明確讀取 repository page：

```bash
dev pr list --scope repo --repo github:owner/api
dev pr list --scope repo --repo gitlab:group/api --role author,reviewer
dev pr list --scope repo --repo github:owner/api --page-size 50 --json
dev pr list --scope repo --repo github:owner/api --cursor '<next_cursor>' --json
```

此 scope 需要一個 provider-qualified repository 或 repository URL，支援
`--role all|author|reviewer|author,reviewer` 與所有 documented states。下一頁使用回傳的
opaque cursor，account、repository、state、role、page size 必須保持相同。JSON 在既有
schema-v1 object 新增 `pagination`（`next_cursor`、`complete`、`total`、
`total_lower_bound`、`relationship`）。仍有下一頁時，已載入頁面不是整個 repository。

## 檢查與試用 request

```bash
dev pr view https://github.com/owner/api/pull/12
dev pr view https://gitlab.com/group/api/-/merge_requests/12 --json
dev pr diff https://github.com/owner/api/pull/12
dev pr diff https://github.com/owner/api/pull/12 --no-pager > pr.diff
dev pr checkout https://github.com/owner/api/pull/12 --dry-run
```

`view` 讀取最新 checks、review／merge readiness、provider 可提供的變更大小，以及
local checkout 候選。Terminal 中 `diff` 使用選用的 `diffnav`；否則輸出 provider diff。
Terminal output 會處理 control sequences，pipe output 則保留 patch bytes。這是 live
remote preview，不是不可變的 approval evidence。Provider 限制、binary omission 與
file count 變動會明確回報；interactive 可以查看 partial preview，noninteractive 則
拒絕輸出可能被誤當成完整 patch 的內容。`--web` 在 browser 開啟 request 或 diff。

`checkout` 重用 exact matching checkout，不 reset 內容。Dirty files 或與目前 PR head
不同的 local tip 會回報並保留；沒有可重用 checkout 時，才 fetch 指定 request，從
verified head 建立 task-free worktree。多個 local matches 需要以 `--repo` 選定；既有
matching SSH remote 保留原本 transport，base remotes 有歧義時可用 `--source-remote`。
不會建立 personal fork。

```bash
dev pr checkout https://github.com/owner/api/pull/12 --repo ~/src/api
dev pr checkout https://github.com/owner/api/pull/12 --try
dev pr checkout https://github.com/owner/api/pull/12 --clone --path ~/src/api
dev pr checkout https://github.com/owner/api/pull/12 --try --provision
```

沒有 local repository 時，interactive checkout 提供獨立 dated Try clone 或 project
clone 加 worktree，預設建議 Try。Script 必須指定 `--try` 或 `--clone`。PR acquisition
預設不 provision、不複製 ignored files、不安裝 dependencies、不執行 project hooks，
也不初始化 submodules；需明確 `--provision`，既有 project trust 檢查仍適用。新 Try
保有 catalog identity 與自己的 clone。`--no-open` 只準備 checkout；`--json` 回報 paths
與 retained effects，不開 runtime。Task adoption 與日後 cleanup 仍是獨立 action。

## Merge 與更新 local base

```bash
dev pr merge https://github.com/owner/api/pull/12 --squash --dry-run
dev pr merge https://github.com/owner/api/pull/12 --squash
dev pr merge https://github.com/owner/api/pull/12 --squash --sync-base ff-only --repo ~/src/api
dev pr sync-base https://github.com/owner/api/pull/12 --repo ~/src/api --strategy rebase
```

Merge 先 review exact head，送出一次 immediate squash 前重新檢查 account、repository、
refs、permissions 與 provider readiness。已 review 的 noninteractive operation 使用
`--yes`。Checks passing 不等於 ready；draft、review requirements、unknown policy、
queue／auto-merge、GitLab merge trains 都可能阻擋。延後合併流程留在 provider UI，dev
不提供 admin override。Provider 的 branch-deletion policy 會在 merge 前顯示，dev 本身
不要求刪除 branch。

Private attempt/result receipts 位於 `<state_dir>/pr-merges/`。中斷的 write 可能已在
remote 完成；dev 保留 **unknown**、只透過 reads reconcile，不再送出 merge。Cached
ready badge 或之前看過的 diff 都不是新 write 的授權。

只有 confirmed merge 後才開始 base synchronization，目標是 PR 的實際 base branch，
不硬編碼 `main`。先 fetch，再建立新的 local synchronization plan。預設 fast-forward；
rebase 需要明確 strategy。Dirty、occupied、ambiguous 或已改變的 checkout 會阻擋
mutation；不隱含切 branch、stash、force reset 或 cleanup。Remote merge 與 local sync
分開回報：後續 sync 失敗仍保留已完成的 remote merge 與 recovery information。

`dev pr list --actions` 仍只列印 operator commands，不會執行。Approve、comment 留在
provider；comment suggestion 只含 generic `'...'`，不產生 AI-review trigger phrase。

## Native gh-dash

Dashboard action 透過 `gh dash` 開啟已明確安裝的 `dlvhdr/gh-dash`，保留它的設定與
keybindings。它傳入所選 GitHub host/repository；有 verified local checkout 就用其
目錄，否則使用 neutral temporary directory，避免讀到另一個專案的 local config。
它不安裝 extension、不改寫 `.gh-dash.yml`，也不保證在 native dashboard 內選中某一個
PR。GitLab request 繼續使用 dev MR actions 或 browser。

## 收掉 merged request 背後的 worktree

Forge 回報 request 已 merge 只是 evidence，不是 retirement authorization：

```bash
dev pr list --scope local --state merged   # candidates
dev work sweep --merged-worktrees               # 證明 containment，先 report
```

Squash merge 不會讓 local feature branch 成為 base 的 ancestor，因此 forge answer 無法
單獨證明 recovery。`dev work sweep --merged-worktrees` 在本地證明 containment；適用時
`dev work done --merged` 需要 explicit squash attestation。把 inbox 視為 inspect 的理由，
絕不是 delete permission。

要取得 deterministic agent-readable triage，請用 generic prompt surface，不是 PR
subcommand：

```bash
dev agent prompt render pr-triage
dev agent prompt run pr-triage --agent my-agent
dev agent prompt open pr-triage --agent my-agent
```

Recipe、configuration、transport、TTY、permission 與 runtime boundaries 請見
[Prompt handoff](prompt-handoffs.zh-TW.md)。

## Provider availability

`gh` 與 `glab` 是選用且彼此獨立的。Signed-out provider 會在 table 下方回報 exact login
command；另一個 ready provider 仍會提供 rows。`dev self doctor` 回報相同狀態。在 JSON
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
