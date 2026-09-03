---
description: 使用 preview dev flow 介面檢查單一 repository，並套用綁定 revision 的 lifecycle、adoption、removal、retirement 與 remote-evidence plans。
authority: project
status: evolving
verified_on: 2026-09-01
lang: zh-TW
---

# Repository Lifecycle Flow

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

`dev flow [repo]` 是標示為 preview 的全螢幕 TTY-only 介面，專門處理單一 repository 的 lifecycle。它獨立於六個 view 的 `dev tui` dashboard；唯一任務是顯示 repository surfaces、observed evidence 與 exact guarded actions。

```bash
dev flow              # 目前 repository；在 Git 外則開啟 picker
dev flow api          # 明確指定 repository，覆蓋 cwd
```

從 canonical checkout 或 linked worktree 執行不帶 argument 的 `dev flow` 時，它會解析 canonical Git common-directory identity，並 focus 目前所在的 exact surface。在 Git 之外執行時，則非同步載入可過濾的 repository picker。由 task metadata 引用、但暫時 unavailable 的 repository，仍會以 unavailable task-only rows 供檢查。

## Repository surfaces

左側 panel 先採用 `git worktree list --porcelain` 的每一筆 record，再加入沒有 checkout 的 task records。因此，正常 COLD 與 DONE tasks 不會只因 directory 不存在就消失。

| Row kind | 意義 | Guarded actions |
|---|---|---|
| `canonical` | Git 標示為 Main 的 worktree，不受 branch name 影響 | Exact 綁定時提供合法 task actions；絕不移除 checkout |
| `managed` | 有一筆 exact task binding 的 non-main checkout | 依 mode/state 提供 lifecycle actions |
| `unmanaged` | 沒有 task 或 harness claim 的 registered linked checkout | Adopt；對 clean checkout 執行保留 branch 的 Remove Checkout |
| `harness` | Strict path evidence 顯示它位於 `.claude/worktrees` 下 | 僅供 inspect，以及符合條件時取得 remote evidence；cleanup 由 harness 負責 |
| `task-only` | Task metadata 沒有 registered checkout，通常是 COLD 或 DONE | 只有 exact task identity 允許時才能 Resume 或 Retire |
| `conflict` | Duplicate claims、mismatched identity、incomplete inventory 或其他 ambiguity | Inspect/remediate；destructive actions fail closed |

Branch name prefix 不是 harness ownership proof。Canonical、harness-owned、locked/prunable、detached、被 ambiguous claim 或 observation 不完整的 row，不會只因看似 inactive 就被視為可移除。

符合條件的 unmanaged row：

- **Adopt** 建立一筆 worktree-mode task record，不改 checkout content、refs、runtime layout 或 path。Dirty checkout 仍可 adopt，因為其 bytes 保持原狀。只有 fresh strict runtime evidence 證明恰有一個 stable、shell-only covering session，且沒有 recognized agent 時，才能 derive HOT；其他情況 adoption 都是 WARM。`runtime=none` 屬於 unobserved，因此 derive WARM。
- **Remove Checkout** 要求 exact named clean linked checkout、完整且沒有 claim 的 task inventory、ready artifacts、可從外部安全 cleanup 的 runtime，以及沒有 harness evidence。它以 non-force 方式移除，並驗證 local branch 仍在相同 OID。

Repository-wide prune 與 drift reconciliation 不是隱藏的 Remove variants。Flow 會回報這些 conditions，留待明確 recovery。

## Intent、facts 與 plans

三個 panels 刻意分開不同種類的 truth：

1. **Persisted intent** — task mode、HOT/WARM/COLD/DONE、owner、next action、branch/base、expected checkout 與 runtime hint。
2. **Observed facts** — repository/worktree identity、HEAD 與 refs、Git status 與進行中的 operations、artifact readiness、runtime sessions 與 recognized agents。Fact 會維持 known、unknown、error、skipped、loading 或 stale；failed observation 絕不會變成 false、clean 或 closed。
3. **Guarded plan** — 合法的 source/target edge、READY/NEEDS INPUT/BLOCKED/UNKNOWN/ERROR availability、ordered conditions/effects、remediation、retained resources、confirmation class、PlanID 與 CLI fallback。

`runtime=none` 無法觀察 session 或 agent occupancy。這不會把 observation 變成「closed」，但完整的 local Git/task snapshot 仍可對 non-runtime facts 提供 fresh 且有用的資訊。

## Managed lifecycle choices

Actions 是具體 variants，不是 generic force menu：

| Current intent | Flow choices |
|---|---|
| HOT/WARM worktree 或 branch task | Park Warm；Park Cold；Park Cold + Push；Complete FF；Review Handoff；Verify Merged |
| HOT/WARM direct task | Park Warm；Complete Direct |
| WARM task | Resume（包含明確 fetch effect） |
| COLD worktree/branch task | Resume 並 reconstruct/reopen；回到 HOT 前不能 completion |
| DONE task | Retire (Keep Branch)；符合條件的 non-direct task 另有 Retire + Delete Contained Branch |

Review Handoff 會 publish branch，且 available provider 支援時會建立 pull/merge request，但仍保留 HOT/WARM。Direct/FF/verified completion 最後寫入 DONE，並保留 branch、checkout 與 runtime resources。Retire 才是另一個可從外部安全執行的 close/wait/remove/task-reap operation。

Preview 一律使用 dirty 時 fail 的 completion policy。它刻意不提供 dirty commit/discard、WIP checkpoint、shared-writer、ownership takeover、force removal、`--close-unknown` 或 `--assume-no-runtime` choices。Blocked plan 會顯示精確 evidence、remediation 與相容的 CLI fallback，不會憑空製造 override。

## Keys 與 approval

```text
j/k 或 up/down         選擇 surface 或 picker/menu row
h/l 或 left/right      選擇已具體化的 action
Tab / Shift-Tab        移動 panel focus
/                      過濾 repository picker
Enter                  建立並檢查 plan；絕不立即 apply
y                      核准 READY 且非 typed 的 plan
r                      只重新載入 local facts
R                      選擇 Fetch Refs、Query Review 或 Both
?                      顯示 evidence 與 key semantics
Esc                    返回上一層
q                      離開；Apply 執行中會排隊，等 ledger 返回後才離開
```

Typed retirement 會顯示 exact `DELETE <branch>` token；輸入後按 Enter。任何 mismatch 都不會 mutation。不是 READY 的 plans 沒有 Apply path，但仍維持開啟供檢查。

## Local 與 manual remote evidence

Startup 與 `r` 不會 fetch 或聯絡 forge。只有已確認且明確宣告的 action 才執行 network work：Resume fetch、Park Cold + Push、Review Handoff，或其中一個 `R` choice。

`R` 提供三種獨立 plan variant：

- **Fetch Refs** 對 exact configured remote 執行 `git fetch --prune`。
- **Query Review** 不 fetch，向支援的 GitHub、GitLab 或 Azure CLI 查詢 exact head/base relationship。
- **Both** 先 fetch 再 query；fetch 失敗時不會嘗試 review query。

Remote result 只存在於目前 flow run。Ref evidence 包含 exact named-ref OIDs，或明確 absence/error。Portable review evidence 只包含 existence、provider state（open/draft/merged/closed）、draft flag、URL、provider 與 observation time。Unsupported provider、missing CLI/extension、authentication/provider failure、malformed 或 multiple matches，以及尚未要求取得的 evidence，都維持 UNKNOWN 或 ERROR。Flow 不會 query 或推斷 review decisions、approvals、comments 或 checks。

## Plan 與 Apply revalidation

Planning 不產生 side effect。Plan 會將 task record revision，以及 exact repository、Git common directory、checkout path/branch/HEAD、refs、status、runtime/agent occupancy、artifacts、remote identity、concrete options 與 ordered effects，封入 authority fingerprint 與 PlanID。

Apply 只接受目前 flow run 產生的 plan，以及綁定該 PlanID 的 approval。它依序鎖定 repository 與 task state、重新載入 task revision、重新 observe authority，且只要出現差異，就會在新 effect 前拒絕。Safety-critical checks 會在 runtime closure 後，以及 checkout 或 branch removal 前立即重做。Task state 最後才寫入；retirement 最後才刪除 DONE record。

Refresh、切換 row 或 quit 都不會取消 Apply。互相衝突的 navigation 會停用，quit/refresh 則等待 result。Result ledger 保存每一個 attempted/completed/failed effect，以及 warnings 與 recovery。Partial success 表示 completed effects 維持完成；不暗示 rollback。之後會載入新的 local generation，directory/runtime/URL handoff 也只在 alternate screen 結束後發生。

## Compatibility boundary

同一個 `internal/taskflow` authority 支援一般 task-managed park、resume、completion 與 retirement commands，以及 exact unmanaged linked-checkout 的 Adopt/Remove paths。既有 CLI flags 與 structured output 維持相容，包括 preview 不提供的 expert acknowledgements。

這不表示每一條 historical cleanup path 都已 migrate。Explicit unmanaged path retirement 仍是隔離的 compatibility implementation；`sweep` 的 record-only、orphan-salvage 與其他 narrowly scoped reconciliation paths 也仍在 taskflow 之外。`sweep` 仍維持先回報再套用。

Raw Git 與 configured external TUI tools 不參與 dev 的 task locks、PlanID approval、identity revalidation 或 result ledger。這些工具仍可使用，但位於 dev-mediated safety guarantees 之外。

## 來源

- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/flowtui`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/flowtui)
- [`internal/taskflow`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/taskflow)
- [`internal/inventory/repo_context.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/inventory/repo_context.go)
- [`internal/task/store.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/task/store.go)
- [`internal/forge/review.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/review.go)
