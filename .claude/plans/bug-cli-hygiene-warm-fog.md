# Worktree 可見性小修正、helper FF 落地與 dev-cli v0.2.40

## Context

上輪 TUI／artifact guards／co-commit adapter 和 canonical helper v2 已實作，留在兩個外部 worktree，尚未提交。這些 worktree 是本 session 依核准計畫用 `git worktree add` 建立，不是讀取 `dev --skill` 的副作用；沒有經過 runtime opening，所以使用者不一定看得到。

本次使用者已選定：
- dev-cli：可加入小型可見性修正，開 PR、merge、發布新版。
- agent-skills：不開 PR、不發 release，整理 helper v2 後 **fast-forward 合併 main 並推送 main**。
- 解決 Babel 離線快取缺口，重跑真正完整的測試，不跳過 assertions 或調整 dependency pins。

## 已核對狀態

- dev-cli canonical main、cached origin/main 與 GitHub main 都為 `4dc2433`；最新 release `v0.2.39`，feature branch 尚無 PR。計畫使用下一個未占用 patch `v0.2.40`，實作時再核對。
- `/Users/zhouhanru/Worktrees/dev-cli/fix-closeout-review-tui`：既有產品／測試／文件改動未提交。canonical main index 另有 staged plan 和 staged＋持續追加的 live transcript；**不得混入發布、reset 或 amend**。
- agent-skills canonical main／cached origin/main 為 `25ddd176`、clean；helper worktree 在 `82bb367`，11 個修改＋2 個新增檔、index empty。main 新增的 Nautilus／Cloudflare commits 不碰 helper 變更路徑；先前使用者的 merge 沒有包含 helper v2。
- Herdr 即時資料：dev-cli feature worktree 已對應 `w11` shell；helper worktree 尚未開啟。agent-skills canonical `wC` 目前只有 shells。Apply／cleanup 前仍須重驗，不能用這份快照授權之後的操作。

## 邊界

- 只在既有 feature worktrees 編輯、提交產品檔；不改原 dev-cli index／live artifacts／本機 main 的歷史。
- 保留使用者已合併的 agent-skills commits；不可 reset、force-push、移動發布 tag 或重跑舊 finalizer／過期 recovery preview。
- 不啟動 agent、不安裝覆蓋 dev／skill／全域工具，不關閉 unrelated Herdr panes／workspaces。可見性只開 shell/runtime surface，不建立 task authority。
- Helper 可直接 FF main 並一般推送，dev-cli 走單一 PR merge commit。保留 remote feature branches；發布完成後僅清理已完成的本機 worktree／branch 及其已核對 runtime surface。

## 1. Babel：只補快取，完整重測

根因已證實：repo lock 使用 Aliyun mirror，fresh legacy MkDocs fixture 使用 PyPI；同版本 Babel 已在別的 cache key／venv，但 `UV_OFFLINE=1` 禁止取得該 PyPI wheel。它是 `py3-none-any`，目前不是 ABI／安裝權限問題。

- 以同一 uv cache，從公開 PyPI 精確 warm-up `Babel==2.18.0`，使用 isolated/no-project uv environment，不改 pyproject／uv.lock 或全域套件：

```sh
UV_OFFLINE=false uv run --no-project --isolated \
  --default-index https://pypi.org/simple --with 'Babel==2.18.0' \
  python -c 'import babel; print(babel.__version__)'
```

- 再用隔離 HOME／XDG、已驗證的 native tool context 重跑 **未修改的完整** `UV_OFFLINE=1 make test-skill`。
- Babel 是第一個已知 miss；若還有缺漏，精確補齊公開 dependency，或以一次線上 MkDocs fixture gate 暖好整個依賴集合，然後恢復離線完整重測。不得略過 prerequisites、改 lock／index source 或把缺 cache 說成產品測試成功。
- 再跑 helper `make validate`、strict bilingual docs，以及 opt-in real dev binary／canonical helper bridge test。

## 2. 最小可見性功能：`dev wt open --no-focus`

主要檔案：`internal/cli/worktree.go:newWtOpenCmd`。重用 `openCheckout` (`internal/cli/helpers.go`) 和現有 `runtime.Herdr.OpenWorktree`，不增加 watcher、runtime API、task adoption、agent launch、`repo open` 擴張或新 JSON mode。

- 新 bool flag 預設 false，維持既有直接導航行為。
- `--no-focus` 仍選取既有 registered checkout、開啟／重用 runtime，然後在 `activateRuntime` **及** no-runtime 的 `cdDirective` 之前返回。
- 保留 branch、path、backend／handle 既有輸出；新 flag 可另加一行有限 receipt，從真實 `OpenResult` 說明 runtime surface created/reused、未要求切換焦點。不能說 Git worktree 被建立；`none` 明確表示未開 runtime，也不發出 cd 指令。
- 不 stage／switch／provision／儲存 task，不要求 checkout clean。Runtime error 原樣失敗，不把 fallback/reuse 當成可發動 agent 的新 root pane。

測試重用 `activityRuntime`、`gittest.New` 與隔離 task store：default activates；flag opens/reuses but never activates／dispatches／annotates；dirty staged/unstaged/untracked bytes、index、HEAD/ref、task records 不變；none 不 cd；runtime failures 保留；label/path/handle 正確。

## 3. 使用者能看見的工作流程與文件

- 新 managed 工作優先用 `dev start`（明確 base）；已有 external worktree 只需可見性時用 `dev wt open <branch> --repo <repo> --runtime herdr --no-focus`，不拿 adopt 當可見性開關。
- 建立／開啟時立即回報 repo、branch、實際 path、runtime handle／結果；區分 durable implementation worktree 與 harness-owned temporary isolation。
- 保持 skill 入口 250–350 words，細節放 `references/worktree-ownership.md`、`runtime-herdr.md`。同步 embedded worktrees help、README、相關 EN／zh-TW worktree／runtime pages 與 changelog；flag 變更用 `make skill-sync`／`skill-check`，不手改 generated commands。
- 實際操作前驗證 HERDR_ENV、exact checkout 和 live workspace；dev feature 已有 `w11` 就重用，不重複開。helper 可開 exact native worktree surface，不送入任何 agent prompt。保留當下真正的 UI focus（最後觀察為 unrelated `wV`），不要把 caller `w3` 當成應還原的焦點。

## 4. agent-skills：保留後续 main，再 FF 合併與推送

- 先核對 canonical instruction／有效 hooks／writer occupancy、fresh remote main；remote fetch 是這次明確 publication 行為，不算被動 inventory refresh。
- 在 helper worktree 保存 reviewed product changes（含兩個新檔），正常 hook-enabled commit。不得提交 private receipts、原 repo history／plan 或測試環境；不使用 --no-verify／SKIP。
- 將尚未發布的 helper commit rebase 到 fresh main，保留使用者已合併的兩筆工作；若實際有衝突就停下處理並重跑受影響 gates，不丟棄內容。
- Canonical main 必須仍 clean、未被其他 writer 占用且 HEAD 符合剛審閱版本，才能 `git merge --ff-only <helper-branch>`。不自動關閉使用者 session。
- 一般 fast-forward push main；如 remote 前進造成拒絕，重新觀察／整合，不 force-push。
- 這只發布 helper 原始碼，不發 agent-skills release/tag，也不自動更新任何本機 installed skill；相容安裝仍由 native skill 流程管理。

## 5. dev-cli：單一 PR、merge、patch release

- 將 no-focus 與既有已完成工作一起整理在目前 feature branch，明確排除 canonical main staged artifacts 和 private previews。
- 新增 focused **required native Windows** gate，覆蓋新增 selector/CAS、filter、CLI no-focus 等跨平台合約；不能只依 broad Windows advisory 的綠色結果。POSIX co-commit helper 在 native Windows 仍明確 unsupported，不冒稱已驗證。
- 使用當下可用下一版（目前 `v0.2.40`），移動 `[Unreleased]` 條目到實際日期 release section、更新 comparison links、AGENTS baseline 與推薦安裝 pins；不改 docs tooling 的 0.0.0。Helper 對外依賴說明須反映已推送 main 的實際狀態，而非假稱自動內建／安裝。
- 正常 commit、push feature branch，建立一個 PR（base main）。PR body 不含 private paths／sessions／findings；用實際 test 結果，不將交叉編譯說成 native verification。
- 等 exact PR head 的 required CI／Docs／hygiene，包括 Windows required step；失敗要修，不 admin bypass。Merge commit 合併，不 squash、不刪 remote branch。
- 確認 merge SHA 已在 remote main，再建立全新 immutable stable tag 並 push。可從 feature worktree取得已驗證 remote main commit；**不必移動仍有 staged/live artifacts 的 canonical dev-cli main**。
- 等 release workflow完成：version assert、六個 platform assets、compact source archive、SHA256SUMS、GitHub release、required Homebrew publication。核對實際資產／checksum／版本；不移動或重用失敗 tag。

## 6. 驗證、清理與交付

- 既有完整測試不重做無關調查；針對 no-focus、修改後的 command/docs、helper rebase與最終 release snapshot 重跑必要 gates：focused CLI/runtime tests、full race、vet／format、E2E、skill sync/check、strict docs/source/site、hygiene publication-range check；CI 對最終 SHA 作平台驗證。
- Release 後才清理 completed local dev-cli worktree／branch；helper FF／push驗證後同樣可清其 completed local worktree／branch。先核對 clean、無未保存 artifacts／claims、fresh runtime occupants、可達的 exact commits，經既有 guarded removal，而非強制刪除。
- `w3`（此 session）、agent-skills `wC` 與 unrelated panes 保留。僅關閉對應 worktree 的 exact idle shell surface；有活躍 agent／command 就不自動關閉。
- Remote branches 保留。Canonical dev-cli local main 若因本 session live index 而落後 remote，明確回報；不為「看起來收乾淨」覆寫使用者 staged 資料。
- 最終提供 PR／release URL、helper main commit、實際測試結果、已清理與保留項目及任何未完成 gate。
