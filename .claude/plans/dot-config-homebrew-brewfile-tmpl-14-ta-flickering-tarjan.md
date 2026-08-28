# Context

`dev-cli` 目前只能從 source 以 `make install` 安裝；沒有公開 GitHub repo/tag、Homebrew formula、release 流程或已部署的 completion。CLI 實際是 Go/Cobra（不是 Tyro），而 Cobra 已自動提供 `dev completion <shell>`，但專案又另有未文件化的 `dev shell-init completion ...`，且兩者都沒有被 package manager 安裝或測試。現有 shell wrapper 以 command substitution 捕捉整段 stdout，會讓裸跑 `dev` 看不到 stdout TTY、從 TUI 退化成 plain list。

目標是把 `dev-cli` 以 public MIT 專案從 `v0.1.0` 開始發布；macOS 可用 `brew install daviddwlee84/tap/dev-cli` 安裝 executable `dev` 並立即取得 bash/zsh/fish completion，Linux 則沿用 dotfiles 的版本鎖定 `go_tools`。同時把 `dev` 納入 dotfiles 的預設開發工具、completion cache、shell directory-change integration 與工具文件，而且不讓 Formula 在安裝期寫入使用者的 agent skill 目錄。

# Implementation plan

## 1. 先在 `dev-cli` 收斂 shell integration 與 completion

- 在 `internal/cli/shell.go` 與 `internal/cli/app.go` 改寫 bash/zsh/fish wrapper：
  - 每次呼叫建立權限受限的暫存檔，透過 `DEV_SHELL_CD_FILE` 傳給真正的 `dev` process；binary 的 stdout/stderr 直接連到原 terminal，不再包在 `$()` 內。
  - `App.cdDirective` 在該環境變數存在時只把「原始目錄路徑」寫入檔案；wrapper 成功執行後讀取路徑並以 `cd -- "$path"` 切換，不 `eval` 檔案內容；未載入 wrapper 時保留目前輸出 shell-quoted `cd ...` 的行為。
  - 保留 `DEV_SHELL_INIT=1`、child exit status、即時 stdout/stderr、pipe 行為與可靠 cleanup；確認裸 `dev`/`dev tui` 仍看到真實 TTY。
- 移除 `newShellInitCmd` 下重複且未文件化的 `shell-init completion` 子命令；唯一公開介面採 Cobra 慣例：`dev completion bash|zsh|fish`（Cobra 現有 PowerShell 可保留，但本次安裝範圍為三種 shell）。
- 新增 `internal/cli/completion.go`，以可重用的 `ValidArgsFunction` / `RegisterFlagCompletionFunc` 接到既有 command constructors：
  - task：`park`、`resume`、`done`，重用 `internal/task.Store.List`，以穩定 task ID 為值並提供 title/state/repo/branch 描述；
  - repo：`start`、`repo open/sync` 與適用的 `--repo`，重用 `internal/repo.Discover`、`Repo.Display`、`resolveRepoRef`；
  - worktree：`wt open/rm`，重用 `repoContext`、`internal/gitx.Worktrees`，排除 detached，且 `rm` 排除 main checkout；
  - embedded/enum：`help` 重用 `internal/help.List`、`gitignore` 重用 `internal/ignore.BundledNames`，以及 `--runtime`、stats `--source` 等有限值；domain 值回傳 `ShellCompDirectiveNoFileComp`，真正的 path arguments 維持 filesystem completion。
  - completion 只讀本機 config/task/repo/worktree/embedded data；不在每次 Tab 查 forge/network、collect runtime/status 或寫入 cache。
- 修正 `internal/cli/root.go` 的 completion 載入時機：hidden `cobra.ShellCompRequestCmd` 進入 root `PersistentPreRunE` 時，目標 command 的 persistent flags（尤其 `--config`）尚未由 `getCompletions` parse，因此先略過一般 `App.Load`，由 dynamic callback 在 flags parse 後 lazy-load。載入/探索失敗時靜默回傳無 dynamic candidates，仍保留 Cobra 的 command/flag 靜態 completion；執行 `dev completion <shell>` 產生腳本時也不依賴使用者 config。
- 在 `internal/cli/cli_test.go`（必要時拆出 focused test file）使用既有 `NewRootCommandWithIO` harness 測試：三種 script generation、移除 duplicate path、直接呼叫 `__complete` 的 task/repo/worktree/help/gitignore/enum candidates、`--config` 生效、壞 config 仍能補 command/flag、NoFileComp directive；另以 subprocess shell tests 驗證 wrapper 的 stdout/stderr、exit status、directory change、cleanup 與 TTY-preserving 行為。
- 跑 `make skill-sync` 更新 `internal/skill/dev-cli/references/commands.md`，再以 `make skill-check` 防止 command tree drift。

## 2. 準備 public `v0.1.0` source release

- 新增專案層 MIT `LICENSE`。
- 更新 `README.md`：Homebrew 與版本化 `go install` 安裝、`dev completion bash|zsh|fish` 手動產生方式、shell-init、以及明確 opt-in 的 `dev skill install`；說明 Formula 不會修改 `~/.agents/skills`。
- 將 `Makefile` 的 `VERSION` 改為可由 `VERSION=v0.1.0 make build` 覆寫，並測試 injected `internal/cli.Version` 會由 `dev --version` 顯示；Homebrew source archive 沒有 `.git`，不能依賴 `git describe`。
- 新增最小 `.github/workflows/release.yml`：只在 `v*` tag 觸發，執行 format/vet/test/skill-check/E2E、以 tag 注入版本並 assert `dev --version`，成功後用 GitHub CLI 建立 generated-notes Release；不加入 GoReleaser、binary matrix 或跨 repo PAT automation，Formula 仍沿用 `translate` 的 source-build 慣例。
- 本地全部驗證通過後，另行確認再建立 public `github.com/daviddwlee84/dev-cli`、設定 `origin`、推 branch/PR。合併後再次確認，才在 exact merged commit 建立並 push immutable `v0.1.0` tag；若有錯誤發布新版本，不移動既有 tag。下載公開 archive 並計算 SHA-256，供 formula 使用。

## 3. 在既有 `daviddwlee84/homebrew-tap` 加 Formula

- 先在 `/usr/local/Homebrew/Library/Taps/daviddwlee84/homebrew-tap` 建 feature branch，再新增 `Formula/dev-cli.rb`，沿用 `Formula/translate.rb`：
  - class `DevCli`、public homepage/tag archive URL、實測 SHA-256、MIT、`head` main、`depends_on "go" => :build`；
  - 直接 `go build` `./cmd/dev` 到 `bin/"dev"`，以 `-X github.com/daviddwlee84/dev-cli/internal/cli.Version=v#{version}` 注入版本；絕不呼叫會執行 `dev skill install` 的 `make install`；
  - 使用 Homebrew 官方 Cobra 形式 `generate_completions_from_executable(bin/"dev", shell_parameter_format: :cobra)`，自動安裝 bash/zsh/fish completion；
  - test block 驗證 `--version`、`--help` 與 completion script 可生成。
- 更新 tap `README.md` 的安裝與人工升版順序（publish immutable tag → checksum → bump URL/SHA → lint/install/test → publish tap），保持與 `translate` 一樣不需要 cross-repo secret。
- formula 本地 build-from-source、audit、test 與 completion keg 檢查通過後，另行確認才 push/merge tap；再從 published tap 做 fresh install 驗證。

## 4. Formula 公開可用後再接入 chezmoi dotfiles

- `dot_config/homebrew/Brewfile.tmpl`：保留既有單一 `tap "daviddwlee84/tap"`，在 `translate` 旁新增 Darwin 的 `brew "daviddwlee84/tap/dev-cli"`；沿用現有 `trust_bundle_taps`，不新增 trust task，也不改 brew-bundle 的 profile/global skip semantics。
- `dot_ansible/roles/go_tools/defaults/main.yml`：新增 `github.com/daviddwlee84/dev-cli@v0.1.0` / binary `dev`，讓既有 role 在 Linux 安裝到 `~/.local/bin`、macOS 避免 shadow Homebrew；同步修正只提 `translate` 的註解/升級說明，不另改 playbook wiring。
- `scripts/generate_completions.sh`：新增 `regen dev "completion zsh" "completion bash"`。Homebrew completion 讓一般 brew 使用者安裝後立即可用；chezmoi cache 同時涵蓋 Linux 並統一兩平台。同步更新 `.chezmoiscripts/global/run_after_50_generate_completions.sh.tmpl` 與 completion 文件的既有 drift：加入目前漏列的 `translate` 與新 `dev`，總數 16、兩 shell stat checks 32。
- 新增 `dot_config/shell/39_dev.sh`，沿用 `dot_config/shell/37_worktrunk.sh`：`dev` 不存在即靜默 return，依 `$ZSH_VERSION` / `$BASH_VERSION` 執行 `command dev shell-init <shell>`，不依 `$SHELL`、不加 primary-shell gate；只有第 1 步的 TTY-safe wrapper 完成後才啟用。Fish 由 Homebrew 自動 completion 與手動 shell-init command 支援，不塞進目前只共享 bash/zsh 的 layer。
- 更新 dotfiles 工具 SSOT 與必要文件：
  - `dot_config/docs/tools/cli-tools.md` 加 `dev`（既有 zsh/Television picker 自動讀取，無需改 picker）；
  - `README.md`、`docs/this_repo/tool-managers.md`、`docs/this_repo/upgrades.md` 說明 macOS Homebrew / Linux go install 的 ownership 與升級路徑；
  - `docs/zsh/zsh-completions.md`、`docs/shells/aliases.md` 說明 generated completion 與 directory-changing function；
  - `dot_agents/skills/chezmoi-dotfiles/SKILL.md.tmpl` 把 `dev` 列為 managed personal CLI，但不誤列成 `~/.dotfiles/bin` script。
- dotfiles diff/check/docs build 都通過、tap 已公開後，再另行確認 `chezmoi apply` 與 dotfiles push；不在這次需求內更改 lean profile 原本會略過整個 Brew bundle 的政策。

## 5. 變更隔離與發布確認

- 三個 repo 各自使用 feature branch並只 stage 本次檔案；任何 push、PR、GitHub repo 建立、tag/Release、tap publish、dotfiles publish 與本機 `chezmoi apply` 都在執行當下再次確認。
- 目前 `dev-cli` 的 `.specstory`/plan 是 agent artifacts，不加入 ignore、也不一概丟棄。commit 前依 `agent-history-hygiene` 找出本 session 的 transcript 與最新 plan，與 feature diff 一起 stage、掃描 secrets；其他 session artifacts 保持不動。

# Verification

1. **Product tests**：`gofmt -l .`、`go vet ./...`、`go test -race ./...`、`make skill-sync && make skill-check`、`./scripts/e2e.sh`；`VERSION=v0.1.0 make build` 後 assert `./dev --version`。
2. **Completion**：生成 bash/zsh/fish scripts；用 `dev __complete ...` 驗證 command/flags、task/repo/worktree/help/gitignore/enums、描述與 directives；壞 config/空 state 不應噴候選錯誤或觸網。新 bash/zsh session 實際按 Tab，Homebrew fish completion 檔亦存在。
3. **Shell wrapper**：在 bash/zsh（fish 可用時一併）source 產生內容，驗證普通輸出與 stderr 即時傳遞、non-zero status、runtime `none` 可改 parent `$PWD`、pipe 維持 plain output；以真 PTY 裸跑 `dev` 能進 dashboard 並正常退出。
4. **Homebrew**：`ruby -c`、`brew style`、`brew audit --strict --online`、`brew install --build-from-source`、`brew test`；確認 `dev --version == v0.1.0`、keg/link 含 zsh `_dev`、bash `dev`、fish `dev.fish`，且 `~/.agents/skills` 未被建立/修改。tap publish 後再做 fresh `brew update && brew install daviddwlee84/tap/dev-cli`。
5. **Dotfiles**：`shellcheck`、`bash -n`、`zsh -n`、`chezmoi cat ~/.config/homebrew/Brewfile`、`chezmoi diff`、`just check`、`uv run mkdocs build --strict`；核准 apply 後驗證 macOS `command dev` 來自 Homebrew、Linux pin 可安裝、zsh/bash completion cache 存在、`type dev` 為 function、TUI 與 parent-directory change 都正常，最後 `chezmoi status/diff` 只含預期差異。
