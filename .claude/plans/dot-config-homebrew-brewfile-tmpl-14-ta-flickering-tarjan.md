# Context

目前 `dev done --ff` 把 integration、runtime close、worktree removal 與 task persistence 綁在同一個 invocation；若 agent 正站在目標 worktree/runtime 內，關閉 workspace 會先殺掉 caller，而從其他 checkout 執行 `git worktree remove --force` 又確實能刪除 active process 的 cwd。這次已觀察到 Codex 在 `dev-cli/feat/copy-metadata-and-nested-wt` 自刪：Git worktree registration 與 branch 消失，但 Herdr `w2D/p1` 仍顯示 idle agent；SpecStory 隨後把原 path 重建成只含 `.specstory/` 的非 Git 空殼，畫面仍能動，卻已出現 `failed to reload config: No such file or directory`。這是 degraded orphan/zombie runtime，不是成功 cleanup。

同時，active SpecStory transcript 會在 agent 每一輪後繼續寫入；agent 無法在自己仍運行時 commit「最後版本」，也不能安全 commit後刪除自己的 cwd。正確模型必須拆成：

1. active agent **prepare/arm**，不 stage moving transcript、不關自己；
2. `specstory run` 返回後的外部 supervisor **finalize** exact UUID transcript並建立最後 artifact commit；
3. 外部 coordinator **integrate**，再 **retire** runtime/worktree/branch。

本次採 backward-compatible MVP：保留 task 的 `hot/warm/cold/done`，把 `done` 解釋為 MERGED（cleanup可能仍 pending），RETIRED則以 live reality推導並在完成後刪除 task。完整 orthogonal task phase/schema與 PR API query延後，避免 v0.1 舊 binary permissive decode後 silent抹掉新 task欄位。

另外新增 `dev git` transaction helpers，但不複製 oh-my-zsh 的純縮寫 alias：只封裝需要 preflight、receipt、exact stash OID與hook-aware recovery的複合操作。

# Implementation plan

## 1. 建立共用 retirement safety/service

- 新增 `internal/retire/`，提供可注入、可重試的 `ResolveTarget`、`Preflight`、`CloseAndWait`、`Retire` service；target可由 task ID或 explicit/current worktree path解析，讓未被 `dev start`追蹤的 worktree也能安全 retire。
- 重用：
  - Git identity/registration：`internal/gitx.Discover`、`Worktrees`、`WorktreeFor`、`StatusOf`、`RemoveWorktree`、`PruneWorktrees`；
  - 路徑 canonical/containment：`internal/pathx`與 `filepath.Rel`，不再用 raw string prefix；
  - worktree dirty/removal policy：`internal/wt.DirtyCheck`與 `Manager.Remove`；
  - transaction pattern：`internal/catalog.Store.WithLock`、atomic rename，以及 `internal/experiment` 的 intent→revalidate→apply→finalize/reconcile模式。
- Runtime observation改成 pane-aware snapshot：
  - `internal/runtime.Session`（或新 `WorkspaceSnapshot`）加入 pane ID、pane cwd、per-pane agent/session metadata；
  - Herdr沿用 `workspace list` + `pane list`並保留 `working/idle/done/blocked/waiting/unknown`；
  - tmux改用 `list-panes -a`聚合 `TMUX_PANE`、session與 `pane_current_path`，不能只看 `session_path`；
  - 讀取 `HERDR_WORKSPACE_ID`/`HERDR_PANE_ID`與 `TMUX_PANE`判斷 caller context；persisted `RuntimeHandle`只當 hint，每次重新依 canonical target path解析所有 covering sessions。
- 所有 dev-mediated destruction共用不可 bypass的 safety gate：
  - caller cwd在 target內、caller workspace/pane覆蓋 target、runtime含 target外其他 panes、或 path/branch registration不吻合時拒絕；
  - `working`、`blocked`、`waiting`永遠拒絕，沒有 `--force`；
  - `unknown`/空 status預設 fail closed，只有 **target外部** caller可加 `--close-unknown`；runtime list失敗只有外部 `--assume-no-runtime`可明確承擔；
  - close所有 eligible sessions後poll fresh runtime list直到 handle/path消失；timeout/close error時不移除 worktree；
  - close成功後再次檢查 caller、live panes、dirty state、Git operation、worktree→branch identity與merge ancestry，再以非 force remove；already-absent registration可prune後idempotently繼續。
- 在 `internal/gitx.RemoveWorktree`再加最底層 cwd containment guard；`park --cold`、`wt rm`、`sweep`、TUI actions與 `wt.Manager.Remove`全部路由同一 service，防止任何 `dev` command重現截圖中的 self-delete。Raw `git worktree remove --force`仍可繞過，bundled skill必須明確禁止。

## 2. 拆分 `done`（integration）與 `retire`（cleanup）

- 重構 `internal/cli/done.go`：
  - `dev done [task] --ff [--push]`只做 branch/worktree revalidation、必要 rebase、canonical base checkout的 `merge --ff-only`、可選push，然後先persist `Task.State=done`；不關runtime、不移除worktree、不刪branch；已被base包含時idempotently修復為done。
  - direct task的 `dev done`只標記done；cleanup同樣交給retire。
  - `--keep-worktree`保留為deprecated no-op warning（新語意永遠keep）；`--delete-branch`拒絕並指向 `dev retire`，不再把 integration與destruction混用。
  - `--pr`維持push/create PR並顯示 READY FOR REVIEW，不新增forge query；外部merge後用 `dev done --merged --base-ref <ref>`驗證 ancestry再標done。Squash merge只接受明確 `--confirm-squash <commit>` operator attestation，驗證該commit確實在base內並清楚標示非內容證明。
- 新增 `internal/cli/retire.go`：
  - `dev retire [task-or-path] [--close-unknown] [--assume-no-runtime] [--delete-branch]`只接受done task或已證明contained的explicit worktree；從target外執行；
  - preflight artifact intents→resolve all runtime sessions→self/mixed/agent-state guard→close→wait absent→revalidate→non-force worktree remove/prune→普通 `git branch -d`（optional；remote branch deletion不在MVP）→刪 task→輸出 RETIRED；
  - 每一步從 live state推導，partial crash可由重跑reconcile，不因stored handle錯誤刪到其他session。
- 修改 `internal/cli/sweep.go`：done且仍有 runtime/worktree/branch時不再直接reap task，而是報 `cleanup pending: dev retire …`；`sweep --apply`呼叫同一 retirement service。只有已真正retired的stale task record才可reap。
- `dev ls --json`僅 additive新增derived `milestone`、`cleanup_pending`、`artifact_status`、`retirement_blockers`；不改既有 `state`欄位/值。Text/TUI顯示 MERGED · CLEANUP PENDING，而不是把done誤說成已清空。

## 3. Versioned artifact intent與 post-writer finalizer

- 新增 `internal/artifact/`（Intent、strict versioned Store、Service、scanner/committer hooks），runtime資料放 `<state_dir>/artifact-intents/v1/`，舊 dev不會讀寫；使用cross-process lock、atomic temp+rename、sanitized failure code與commit receipt。
- 新增 commands：
  - `dev prepare [task-or-path] --session <provider:uuid> [--plan <exact-path>] [--allow-large]`：要求產品code已commit、index空、無rebase/merge conflict；canonicalize worktree/common-dir/branch/base/HEAD；記錄exact session UUID、可選plan、run ID、expected HEAD/index與runtime context；只arm intent，不stage transcript、不關session、不integrate/remove。
  - `dev artifact observe-session-end`：供Claude SessionEnd hook使用，只用hook的 `session_id/cwd/transcript_path/reason`更新已存在intent；沒有matching armed intent即no-op，不能finalize。
  - `dev artifact finalize --intent <id>`：只能在post-writer/external context執行；lock/revalidate後解析 `.specstory/history/*.md` **固定preamble**的完整UUID，不依filename/mtime，也不接受正文中引用的UUID；限制為expected repo下regular file。
- Finalizer quiescence/security：
  - wrapper已返回是主要證明；direct/IDE fallback需JSONL與Markdown size/mtime/hash在bounded settle interval穩定；active mutation就blocked/retry；
  - exact transcript與explicit plan以外一律不stage，其他Codex/Claude sessions保持原狀；
  - 掃描/修復不印secret bytes；呼叫repo/installed `agent-history-hygiene` exact-path redactor與staged scan，scanner缺失或失敗時fail closed、只unstage自身paths、保留intent與working bytes；
  - scan後再次確認staged blob hash等於stable final hash，再自動 commit同一feature branch，加入 `Agent-Artifact-Session:`與 `Dev-Artifact-Intent:` trailers；commit成功但intent更新crash時以trailers reconcile，避免重複commit；
  - artifact commit要求同一 branch/change series，不承諾同一 product commit，也絕不自動amend已push/shared commit。
- Large-file policy：tracked的大型 transcript可在scan後finalize；新untracked transcript >2MiB需prepare時 `--allow-large`，只warning/require acknowledgement，不截斷、不靜默拆分。
- `.specstory/statistics.json`視為derived noise：從dev-cli index移除並ignore；finalizer永不把它當code/artifact，writer結束後restore/remove其dirty變化。修正hygiene scripts：UUID-exact選取、自動模式exact-path staging、statistics不算code、scanner預設不輸出match內容；mtime newest只保留為明確標示的interactive heuristic。

## 4. Dotfiles post-SpecStory boundary

- 修改 `/Users/david/.local/share/chezmoi/dot_config/shell/22_sesh.sh`：wrapper-managed `specstory run <provider>`啟動時產生/傳入 `DEV_AGENT_RUN_ID`；inner process返回後、在shell/kill/restart處理之前呼叫 `dev artifact finalize --run-id … --writer-stopped`並保留原agent exit status。
- Restart mode只有finalize成功/無pending intent才啟動下一個writer；blocked intent停止auto-restart並留下shell，避免新session覆蓋舊index/branch。
- rollout capability check：舊dev沒有artifact command時保持現在plain SpecStory行為，不阻止agent啟動。
- 在managed Claude `SessionEnd` hook加入快速observer（只mark intent，不做Git/scan/wait）；direct/IDE/中斷wrapper則由外部 `dev artifact finalize`或 `dev sweep --apply`reconcile。
- 更新dotfiles的 agent-history-hygiene source/tests與相關SpecStory/agent overlay文件；不把finalizer硬綁到所有無SpecStory的agent launch。

## 5. 只封裝複合 transaction的 `dev git`

- 新增 `internal/cli/git.go` namespace與 `internal/gitx`高階operations；不重做 `gaa`/`gpr`等純縮寫。`dev git setup --print`只輸出可選git/shell alias片段供review，不修改 `~/.gitconfig`或rc。
- `dev git uncommit`：
  - preflight Git repo、single-parent HEAD、non-detached、非in-progress operation、預設index無既有staged混合；published HEAD需 `--rewrite-published`明確承擔後續force-with-lease；
  - atomic保存old commit OID於 checkout-specific `GitDir` receipt，再 `reset --soft HEAD^`；失敗不可被後續cleanup掩蓋。
- `dev git recommit`：讀per-worktree receipt，以 `git commit -C <old-oid>`完整重用multiline message並正常跑hooks；成功後才刪receipt；HEAD/index drift、object消失或hook failure保留receipt與index供retry。
- `dev git pull-rebase`：
  - preflight branch/upstream/unborn/detached/bare與任何merge/rebase/sequencer state；
  - 有local tracked+untracked變更時建立唯一tagged stash、立刻capture **OID**與原index；以 `-c rebase.autoStash=false pull --rebase`避免雙重autostash；成功後 `stash apply --index <oid>`；只在restore完整成功且重新找到同OID reflog entry時drop，否則保留exact stash並回報，不使用裸 `pop`/`stash@{0}`；
  - no-change時絕不碰既有stash；active artifact writer造成restore collision時fail並保留stash。
- `dev git amend-all`：
  - preflight HEAD、non-detached、conflict/in-progress operation與published rewrite acknowledgement；預設執行真正 `git add -A`後 `git commit --amend --no-edit`，**不使用 `--no-verify`**；
  - 若將包含agent artifacts但repo沒有可偵測scanner stack，需 `--allow-unscanned-artifacts`；hooks修改/失敗時不自動retry，保留index並列出狀態；
  - `--exclude-agent-artifacts`可選，以explicit pathspec排除 `.specstory/history`、statistics、`.claude/plans`等並列出排除檔；預設仍依使用者決策包含並交給pre-commit/redactor。
- completion/docs包含上述commands/flags；dotfiles不新增純縮寫，只可在 `setup --print`示例現有 `gundo`等如何改指向安全command。

## 6. 截圖 regression與測試

- Safety zombie regression：helper process `chdir`進temp linked worktree；從另一checkout嘗試dev remove/retire（含force-like input），assert refusal、Runtime.Close/RemoveWorktree未被呼叫，helper仍能 `getcwd`、讀config/skill、spawn child；raw Git bypass只在文件說明，不由test真的銷毀caller。
- Runtime/retire matrix：cwd與caller workspace/pane containment、canonical/symlink、Herdr/tmux caller IDs、stale handle、多sessions、mixed workspace、working/blocked/waiting、unknown flag、list error、delayed close、timeout、already-missing/prunable、partial retry、ordinary branch delete。
- Artifact matrix：exact preamble UUID勝過mtime/正文引用、foreign Codex不stage、statistics不算code、writer mutation、large tracked/untracked policy、scanner missing/failure無secret輸出、trailers/reconcile、SessionEnd只observe、wrapper child exit後才finalize且保留status。
- Git helpers：root/merge/detached/published/index-mixed；multiline message receipt/recommit；shared-worktree concurrent stash與existing stash；pull/restore conflicts與untracked collision；amend hooks success/fix/failure、artifact include/exclude與no-HEAD preflight。
- 更新現有 `scripts/e2e.sh`：`done --ff`驗證worktree仍在，外部 `retire`才remove；另加artifact fake-writer/finalizer流程。全套 `gofmt`、vet、race tests、skill-sync/check、Linux/macOS CI與真Herdr read-only smoke。

## 7. 文件與deferred roadmap

- 更新 `README.md`、help topics、bundled `dev-cli` skill與 `references/task-lifecycle.md`/`runtime-herdr.md`/`worktree-ownership.md`；新增 agent retirement runbook，明確說明 branch可在任意worktree操作，但 physical cleanup必須由target外coordinator執行。
- 把截圖症狀原文（`failed to reload config: No such file or directory`）與「SpecStory重建只有 `.specstory` 的非Git空殼」寫入 regression/runbook；不聲稱dev能阻止 raw Git故意繞過。
- 在既有 `TODO.md`新增 deferred items：full orthogonal task schema/migration、forge PR merge-status/squash proof；不為active sprint另建平行roadmap。
- `make skill-sync`更新 generated command reference並由CI檢查。

# Current-worktree dogfood / safe close procedure

這個目前running session是在舊wrapper啟動後才規劃新功能，因此即使本次更新dotfiles，也不能retrofit它的outer command；第一次示範採 **manual external finalize**，下一個agent session才測全自動wrapper。

1. 在目前 worktree從最新 `main`建立本功能branch；code/tests/docs分開commit，active Claude transcript、statistics與其他Codex transcripts全部保持unstaged。
2. 完成實作後執行 `dev prepare --session claude:72b5c55e-d964-45cd-b040-cb29d0d7af05 --plan <this-plan> --allow-large`，記下intent ID；不要關 `w10`、不要remove worktree。
3. 目前Claude正常exit，讓SpecStory完成final render。
4. target外的main/integration session手動執行 `dev artifact finalize --intent <id> --writer-stopped`；驗證trailers與final transcript commit存在於本功能branch。
5. 對 current untracked Codex transcript `01a0438b-…`另行確認owner/停止writer並建立獨立intent；絕不與Claude一起 `git add --all`。
6. 對已self-delete的Codex `w2D/p1`（session `codex:01a043c1-5e2a-74c1-b6e8-10640eda926f`）：先要求agent正常exit；外部finalizer從orphan path按UUID保存final transcript到正確change series/main receipt。Git branch/registration已不存在，不能假裝普通worktree；只有receipt hash覆蓋全部recognized artifacts且目錄無其他內容後，才可關 `w2D`並逐項刪除residual `.specstory` shell，否則保留現場。
7. 外部coordinator重新抓取當下topology（目前 `main=086dbcd`、比 `origin/main` ahead 2；completion branch落後main 2），將finalized feature branch rebase/FF進最新main並push；不能沿用舊snapshot假設。
8. 從target外執行 `dev retire <path/task>`；確認runtime已absent、worktree clean/contained、final artifact commit已在main後，以non-force remove及普通 `branch -d`完成。任何check失敗即停止，不進下一個destructive step。

# Verification acceptance

- 所有dev-mediated self-close/self-remove在caller/live agent下fail closed；截圖中的Git-absent/Herdr-alive/artifact-shell狀態能被report/reconcile，不被誤判RETIRED。
- Active writer期間沒有final transcript commit；writer exit後exact UUID transcript只commit一次，foreign transcripts與statistics不被誤stage，scanner failure零destruction。
- Local FF與PR流程都能到MERGED而保留runtime/worktree；只有外部retire能完成RETIRED。
- `dev git` failure paths不掩蓋status、不套錯共享stash、不跳hooks；receipt與stash在任何partial failure下可retry。
- Current manual dogfood完成後，main/remote包含final artifact receipt，Herdr workspace已外部關閉，worktree/branch以non-force安全移除；下一個SpecStory session驗證automatic post-writer finalization。
