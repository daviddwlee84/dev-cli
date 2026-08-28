---
description: 遵循現行 GitHub Flow，並把舊版 deploy/merge 變體清楚標示為歷史脈絡。
authority: github-docs
status: official
verified_on: 2026-08-28
lang: zh-TW
---

# GitHub Flow

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

現行 GitHub Flow 是輕量 branch 與 pull request workflow。官方步驟是 branch → changes/pushes → pull request → review → merge → delete branch。

!!! info "時效"
    **Authority：**現行 GitHub Docs · **Status：**官方 workflow guidance · **Verified：**2026-08-28。下方歷史來源只用於描述背景，不是今日 operating procedure。

## 現行流程

### 1. 建立 branch

從 repository 的 default branch 開始，使用簡短且具描述性的名稱。不相關改動要放在不同 branch。

```bash
git switch main
git pull --ff-only
git switch -c feat/token-refresh
```

### 2. 修改、commit 與 push

建立聚焦的 commits，並在工作進行中持續 push。Remote branch 不只用於最後發布，也是 collaboration 與 recovery boundary。

```bash
git add <paths>
git commit -m "feat(auth): add refresh token rotation"
git push -u origin feat/token-refresh
```

### 3. 建立 pull request

說明問題與變更。需要提早 feedback 時可先開 draft pull request；連結相關 issues 並附 test plan。

### 4. 處理 review

把 follow-up commits push 到同一 branch，pull request 會自動更新。Repository rules 可能要求 approvals、status checks 或 conflict resolution。

### 5. Merge

只有符合 repository 的 review 與 branch-protection requirements 才 merge。Merge commit、squash 或 rebase 的選擇是 project policy，不是 GitHub Flow 唯一規則。

### 6. 刪除 branch

確認 merge 且保留必要 recovery path 後，刪除已完成 branch。Pull request 與 repository history 仍保留。

## Deployment 是獨立政策

現行 GitHub Docs **沒有**把 deployment 定義成 GitHub Flow step。專案可以在 merge 前 deploy preview、merge 後 deploy、要求 staged promotion，或完全沒有 deployment。應把選擇寫在 CI/CD 文件，而不是歸因於 GitHub Flow。

## 歷史變體

| 來源 | 歷史說法 | 現在如何使用 |
|---|---|---|
| `githubflow.github.io` | `master` 隨時可 deploy；先 merge reviewed work，再立即 deploy | 設計歷史；正文改用 default branch，且不把 deployment order 當通則 |
| archived GitHub Guides（2019 snapshot、較早 guide design） | create branch、add commits、open PR、discuss/review、deploy、merge | deploy-before-merge validation 的歷史案例 |
| 現行 GitHub Docs | create branch、make/push changes、create PR、review、merge、delete branch | 本站目前的 normative source |

歷史頁面對 deploy-before-merge 與 merge-then-deploy 的說法彼此衝突；這正表示 deployment 是 project policy，不是永遠不變的 flow invariant。

## dev-cli 對應方式

```bash
dev start api --task "token refresh" --base main
# 在 task checkout commit 並測試
dev done --pr
```

`dev done --pr` 發布 branch 並建立 pull/merge request，之後刻意讓 task 在 review 期間維持 active。它不宣稱 request 已 merge，也不會標示 task DONE。

`dev` 另允許一個 project-policy exception：一個小型安全 commit 可直接進 trunk。這是輕量 trunk-based 選擇，**不是** GitHub Flow；需要 pull request 的 team 一律使用 branch。

## Pull request readiness checklist

- Branch 只包含一條 coherent change stream。
- Description 解釋 why、behavior changes 與 operational impact。
- 已列出 tests 與 manual verification。
- Shared generated files 與 migrations 有唯一 owner。
- Required checks 與 reviewers 都已完成。
- Merge strategy 符合預期 trunk history。
- Deployment 與 rollback 遵循專案自己的 policy。

## 來源

- [GitHub Docs：GitHub Flow](https://docs.github.com/en/get-started/using-github/github-flow)
- [歷史 GitHub Flow site](https://githubflow.github.io/)
- [Archived “Understanding the GitHub Flow”](https://web.archive.org/web/20191104103724/https://guides.github.com/introduction/flow/)
- [`internal/help/topics/branching.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/branching.md)
