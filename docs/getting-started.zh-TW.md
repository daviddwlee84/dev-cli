---
description: 安裝 dev-cli、初始化開發機器，並完成 start、park、resume 到 integration 的第一條變更流。
authority: project
status: stable
verified_on: 2026-08-29
lang: zh-TW
---

# 快速開始

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

安裝 `dev`、建立或 clone repository，再用一個小型變更流 (change stream) 走完 `start`、`park`、`resume` 與 `done`。

## 1. 安裝與初始化

```bash
make install
dev config init
```

加入 shell integration，讓開啟 checkout 的命令能改變目前 shell 的目錄：

=== "zsh 或 bash"

    ```bash
    eval "$(dev shell-init zsh)"   # bash 請改用 bash
    ```

=== "fish"

    ```fish
    dev shell-init fish | source
    ```

檢查實際環境：

```bash
dev doctor
dev config show
```

只有 Git 是必要依賴。Herdr、tmux、Zellij、`gh` 與 `glab` 會啟用更完整的執行環境 (runtime) 或 forge 功能，缺少時則安全降級。

## 2. 建立或 clone repository

在任意 directory 開啟 repository bootstrap wizard：

```bash
dev repo new
```

它會在 configured `project_root` 下選擇 destination、預覽 plan，並可使用
built-in `minimal` 或 `agent-ready` preset。`agent-ready` 會加入 starter agent
guidance 與 project-scoped Claude plans；選取 `agent-history-hygiene`、
`project-knowledge-harness` 等 optional skills 後，流程會安裝它們，並由經 review 的
內建 initializer 立即建立 project surfaces，不會執行剛下載的 skill code，也不必等
日後某個 agent trigger。

取得既有 code 時，將 owner/name 或 Git URL 傳給 clone。要把同一套 setup 套用至既有
checkout，則使用 setup：

```bash
dev repo clone owner/api
dev repo clone git@gitlab.example.com:group/api.git
dev repo setup . --preset agent-ready
```

只有對應的 `gh` 或 `glab` CLI 已安裝且完成 authentication 時，wizard 才會提供
GitHub 或 GitLab publishing；預設仍是 local-only。最後的 handoff 可選擇留在原處、
`cd` 進 repository、開啟 configured terminal runtime，或接續 `dev start` wizard。
Bootstrap 與 `dev start` 都不會啟動 coding agent。

若已經位於 repository 且不需要 setup，可直接繼續下一步。

## 3. 開始工作

在任何已探索到的 repository 中啟動具名 task。script 或 agent-driven command 應明確寫出 base：

```bash
dev start api --task "token refresh" --base main
```

預設模式會建立 branch、在設定路徑建立 linked worktree、完成 environment provisioning、開啟最佳可用 runtime，並記錄 HOT task。

只有在工作確實適合時，才選用更輕量的 checkout mode：

```bash
dev start api --task "one-line typo" --direct
dev start api --task "small local branch" --branch-only --base main
```

- `--direct` 使用 canonical checkout，因此不能進入 COLD。
- `--branch-only` 建立 branch，但不建立 linked worktree。
- 預設 worktree mode 適合獨立或平行寫入。

## 4. Park 時留下可執行的下一步

```bash
dev park --next "reproduce the refresh race, then add a regression test"
```

這會關閉 runtime、把 task 標成 WARM，同時保留 branch 與 checkout。若 working tree 尚未乾淨，使用可復原的 checkpoint，而不是不易看見的 stash：

```bash
dev park --wip --next "finish the regression test"
```

跨機器 handoff 前，先 commit、push，再移除 checkout：

```bash
dev park --cold --push
```

## 5. Resume 與檢查

```bash
dev ls
dev resume "token refresh" --fetch
dev status
```

WARM task 會重開現有 checkout；COLD task 會從 remote branch 重建 worktree。Branch 是持久身分，directory 只是 cache。

## 6. 明確選擇整合方式

```bash
dev done --ff
```

`--ff` 先把 change branch rebase 到 base，再 fast-forward base，保留值得留下的 commits。若 review 或 CI 應決定 merge，改為建立 request：

```bash
dev done --pr
```

`--pr` 會 push 並開啟 pull request 或 merge request，但 review 尚未結束時不會改變 task，也不會標成 DONE。整合完成後再檢查 cleanup 建議：

```bash
dev sweep
dev sweep --apply
```

## 下一步

- [心智模型與生命週期](concepts/mental-model.md)
- [Repository bootstrap 與 project configuration](reference/commands-config.zh-TW.md#repository-bootstrap)
- [變更流工作流程](guides/change-stream-workflow.md)
- [Worktree 與環境佈建](guides/worktrees-provisioning.md)
- [命令與設定](reference/commands-config.md)

## 來源

- [`README.md`](https://github.com/daviddwlee84/dev-cli/blob/main/README.md)
- [`internal/cli/repo_create.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/repo_create.go)
- [`internal/help/topics/parking.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/parking.md)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
