---
description: 讓 branch 保持短期、commit 有意義、pull request 可 review，並把 release semantics 與 message style 分開。
authority: git-and-project-policy
status: stable
verified_on: 2026-08-28
lang: zh-TW
---

# Branch、Commit 與 Pull Request

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Branch 是一段工作 episode，commit 是一個有意義且可逆的變更，pull request 是 review 與 integration boundary。

## 何時建立 branch

工作需要多個 commits、implementation 尚未確定、另一位 writer 可能重疊、很可能中途暫停，或 default branch 必須維持 releasable 時，就使用 branch。

現行 GitHub Flow 的每個變更都從 branch 開始。`dev` 另外允許 solo/project-policy workflow 把一個小型、安全且可逆的 commit 直接放進 trunk。不要把這個例外稱為 GitHub Flow；branch protection 或 team review 要求 pull request 時也不能使用。

## Branch naming 與 lifetime

建議使用具描述性的 namespace：

```text
feat/...  fix/...  refactor/...  docs/...  chore/...  exp/...
```

Change stream 結束時 branch 就應結束。Squash merge 後不要繼續用舊 branch 做新工作：原 commits 不是 squash commit 的 ancestors，後續比較可能再次出現舊變更。請從更新後的 default branch 重新開始。

長期 `release/*` 或 `maintenance/*` branch 必須有明確 owner、merge direction、purpose 與 retirement condition。

## 一個有意義的 commit

好 commit 可以單獨理解，最好也能單獨 revert；它不等於一個 feature、一個 file 或一天工作。

Feature-branch history 是 construction history；trunk history 是 product history。為了讓 handoff 可復原，private feature branch 可以使用暫時 `wip:` checkpoint；若它不傳達有用的 product change，整合時應 reword 或 squash。

## Conventional Commits：規格與政策

Normative shape 是：

```text
<type>[optional scope][optional !]: <description>

[optional body]

[optional footer(s)]
```

Conventional Commits 1.0.0 定義 `feat` 與 `fix`、允許其他 types，並以 `!`、`BREAKING CHANGE:` 或 `BREAKING-CHANGE:` 標示 breaking change。它**沒有**要求 English、imperative mood、lowercase、72-character limit 或固定 type allowlist。

本專案為了 log 一致且適合 tooling，建議 English imperative subject，並常用：

```text
feat fix docs style refactor perf test build ci chore revert
```

這是 house policy，不是 Git 或 Conventional Commits 規格。`dev park --wip` 刻意產生不在清單內的暫時 `wip:` type。

## Pull request

需要設計 feedback 時可提早開 draft；只有 acceptance criteria 可測試後才標記 ready。好的 description 包含：

- problem 與 intended outcome；
- scope 與 non-goals；
- behavior、migration 或 compatibility impact；
- automated 與 manual test evidence；
- user-visible change 的 screenshots/logs；
- linked issues 與 follow-up work；
- 相關 deployment 與 rollback notes。

不要讓多個 agents 在沒有單一 integration owner 時，各自修改 pull-request description、shared manifest、migration order 或 generated lockfiles。

## Merge strategy

| Strategy | 適用情境 | 取捨 |
|---|---|---|
| rebase 後 fast-forward | branch commits 是有意義的 product history | 保留 bisect/revert granularity |
| squash merge | construction history 吵雜或無法拆分 | 一個乾淨 commit，但失去內部 granularity |
| merge commit | 需要保留 branch topology | boundary 明確，但 history 非線性 |
| rebase merge | 需要線性的 individual commits | commit IDs 改變；重寫 published work 前要協調 |

沒有協調時不要重寫 shared/published branch。已核准 rewrite 時使用有 scope 的 `--force-with-lease`，不要 unguarded force push。

## Branch cleanup

`git branch --merged <base>` 證明 ancestry containment，但不會辨識 squash merge，因為原 commits 不是 squash commit 的 ancestors。同樣地，`[gone]` upstream 只表示 remote ref 消失，不代表已整合。

刪除前先檢查 pull request 並比較 patch 或 commit range。優先使用 `git branch -d` 而非 `-D`；不確定時先建立 backup ref。

## Release semantics

語意化版本 (Semantic Versioning) 描述已宣告 public API 的 compatibility：

- `PATCH`：相容 fixes；
- `MINOR`：相容 additions/deprecations；
- `MAJOR`：不相容 API changes；
- `0.y.z`：initial development，不保證 stability；
- prerelease identifier 的排序低於同版本 stable release。

把 commit type 映射成 version bump 是 automation policy，不是 SemVer 本身；仍要判斷真正 public API impact。Published release 與 tag 應視為 immutable；不要移動已發布 tag，應建立新版本。

## 來源

- [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
- [Semantic Versioning 2.0.0](https://semver.org/)
- [現行 GitHub Flow](https://docs.github.com/en/get-started/using-github/github-flow)
- [`internal/help/topics/commits.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/commits.md)
- [`internal/help/topics/branching.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/branching.md)
