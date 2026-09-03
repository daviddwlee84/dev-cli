---
description: 安裝 dev-cli、初始化開發機器，並完成 start、park、resume 到 integration 的第一條變更流。
authority: project
status: stable
verified_on: 2026-08-31
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
built-in `minimal` 或 `agent-ready` preset。`agent-ready` 會加入明確標示
bootstrap 尚未完成的 `AGENTS.md` starter 與 project-scoped Claude plans。Starter
提供安全的 repository-wide 與 handoff rules；未知的 purpose、verified commands、
architecture、invariants 會保留為 TODO，不會虛構 project facts。Common ignore block
只排除 derived `.specstory/statistics.json`；history、project identity 與 config 仍會被
Git 看見。

選取 `agent-history-hygiene`、`project-knowledge-harness` 等 optional skills 後，流程會
安裝它們，並由經 review 的內建 initializer 立即建立 project surfaces，不會執行剛
下載的 skill code，也不必等日後某個 agent trigger。同一 source 且 agent targets
相同的 skills 會共用一次 installer invocation。History initializer 會寫入
pre-commit/gitleaks config，並另外確保 `.specstory/.gitignore` 含有 `.project.json`、
`statistics.json` 規則，不會忽略應納入 review trail 的 `.specstory/history/`
transcripts。既有 custom ignore content 會保留，只補上缺少的 managed rules。

若要沿用既有內容、但不保留原 Git history，可使用 template snapshot：

```bash
dev repo new api --template owner/starters --template-ref v2 \
  --template-subdir go/service
```

`--template` 也接受 local directory/repository 或 Git URL；optional ref 可為 branch、
tag 或 commit，subdirectory 則必須留在 source 內。Dev 會排除 source `.git`
metadata、保留 regular-file modes，並在建立 destination 前拒絕 traversal、symlink 與
special files。未指定 ref 時，local Git working tree 只提供 tracked files 加上
untracked non-ignored files，不包含 ignored build/cache content；non-Git directory
則提供完整 current tree。Human confirmation/dry-run 會預覽 paths，並警告未 pin 的 local
source 是 live snapshot。URL userinfo 會 redact，held root/file handles 會把 read/write
限制在已驗證 roots 內。Preset 也可宣告 `template`、`template_ref`、
`template_subdir`，因此一個 catalog repository 可容納多個 starter folders。

取得既有 code 時，可將 owner/name、Git URL 或 local Git path 傳給 `repo new`/`create`，
也可使用明確的 `repo clone`。清楚的 clone reference 會直接走 clone acquisition，保留
source history 與 `origin`；無參數 wizard 的第一個欄位也會偵測同樣輸入。要把同一套
setup 套用至既有 checkout，則使用 setup：

```bash
dev repo clone owner/api
dev repo clone git@gitlab.example.com:group/api.git
dev repo new ../existing-repository
dev repo setup . --preset agent-ready
```

使用 `--check-in <auto|commit|stage|none>` 選擇 generated changes 的 check-in
方式。Interactive wizard 提供 `commit`、`stage`、`none`；在 `repo new` 中，`auto`
沿用 selected preset default，clone/setup 則不自動 check-in。`stage` 只執行
`git add -A`、不 commit，並 best-effort 為目前 lazygit 的小寫
`c` 準備 message；可用 `--message` 修改。Staged setup 在 review 並 commit 前不能
publish，也不能 handoff 到 `start`。既有 `repo setup --commit` 仍相容於
`--check-in=commit`。Optional lazygit draft 若寫入失敗，只會產生 warning，不會撤銷已
完成的 staging。

只有對應的 `gh` 或 `glab` CLI 已安裝且完成 authentication 時，wizard 才會提供
GitHub 或 GitLab publishing；預設仍是 local-only。最後的 handoff 可選擇留在原處、
`cd` 進 repository、開啟 configured terminal runtime，或接續 `dev start` wizard。
Bootstrap 與預設的 `dev start` 都不會啟動 coding agent。明確使用 worktree mode
的 `dev start --run '<shell command>'` 時，可以把一個 command dispatch 到新建
first-class Herdr worktree 的 exact root pane；dev 不會代替使用者選擇 agent
profile 或 permission mode。

Repository、`start` 與 `done` wizard 的 TTY text fields 支援 Left/Right、Home/End、
Delete/Backspace inline editing，以及 Esc/Ctrl-C cancellation。Piped input 對 scripts
與 tests 仍維持 line-oriented behavior。

選完 preset 後，bare `repo new` 會詢問預設為 no 的「Customize preset and template
options?」。正常 `agent-ready` flow 因此會略過個別 template/file/input/skill 問題並採用
reviewed defaults；回答 yes 才會展開。

若已經位於 repository 且不需要 setup，可直接繼續下一步。

## 3. 開始工作

在任何已探索到的 repository 中啟動具名 task。script 或 agent-driven command 應明確寫出 base：

```bash
dev start api --task "token refresh" --base main
```

預設模式會建立 branch、在設定路徑建立 linked worktree、完成 environment provisioning、開啟最佳可用 runtime，並記錄 HOT task。

Herdr 被選中且 worktree 為本次新建時，可以把明確 command dispatch 到 exact
new root pane，並選擇轉跳過去：

```bash
dev start api --task "token refresh" --base main \
  --run 'specstory run codex -c "codex"' --focus
```

`--run` 不能與 `--json` 或較輕量的 checkout modes 併用。它只確認 command
已 dispatch，不等待完成，也不回傳 command exit status。

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
