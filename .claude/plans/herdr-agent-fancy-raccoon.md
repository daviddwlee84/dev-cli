# Context

這次要解決三個相連問題：

1. `dev gitignore` 的預設 managed block 仍加入 `.specstory/history/`，與「transcript／plan 應跟 feature diff 保存」的政策衝突。
2. 多個 agent 共用同一 checkout 時，SpecStory 與 Claude 都以**實體路徑**而非 Git branch 分區；`git switch` 不會隔離 raw sessions、untracked/dirty histories或 plans，bulk `specstory sync` 也會看到該 path 累積的所有 raw sessions。
3. Herdr 可以在背景跑多個 agent，但現有 `dev start` 沒有 machine-readable create result，且 runtime adapter 丟掉 Herdr 同次 `worktree open` 回傳的 `root_pane`。dev-cli skill 若要自動接著啟動 agent，只能猜 pane，無法保證不碰原 agent。

`agent-history-hygiene` 的 canonical source 是 `/Users/david/Documents/Program/agent-skills/skills/local/agent-history-hygiene/`；`dev-cli/.agents/skills/agent-history-hygiene` 是 `npx skills` 安裝後提交的 snapshot，且已落後上游一個 commit，不可直接手改。

目標是建立一套可操作且 fail-closed 的流程：

> **worktree per change stream; one SpecStory root per worktree; explicit pane per agent; exact transcript/plan staging.**

## Verified behavior that defines the design

- SpecStory 2.10.0 default output 是 wrapper/watch process 啟動 cwd 下的 `.specstory/history/`；不看 Git root或 branch。custom `--output-dir` 會在 setup 時固定成 absolute path。
- Claude raw project store同樣以 canonical absolute checkout path映射到 `~/.claude/projects/<encoded-path>`，不是 branch。
- 同 checkout執行 `git switch` 仍共用 raw/output dirs；untracked history會跨 branch留在 working tree，dirty tracked history若 Git容許 switch也會跟著走。
- `specstory sync` 的 input是該 checkout path下的 Claude raw JSONL，不是已 rendered Markdown；不加 `-s` 時會處理該 path累積的所有 sessions。同一 UUID還可能留下 slugless與slugged兩份 Markdown，因此 exact UUID仍可能 ambiguous。
- 不同 Git worktree有不同 absolute path、Claude project store、`.specstory/history`、index與 HEAD，才能讓尚未合併的 history/plan只存在於對應 feature checkout/branch。
- SpecStory watcher在啟動時固定 source/output context；沒有證據顯示 Claude `EnterWorktree` 會讓既有 watcher自動 rebind。不能把中途 `EnterWorktree` 當 transcript isolation方案。
- Herdr `worktree create/open` 建立/開啟 checkout + workspace/layout，不搬原 agent。`pane move` 只搬 terminal layout，不會改 process cwd。
- `dev start` 已正確由 `dev` 擁有 durable Git worktree，之後呼叫 `herdr worktree open --no-focus`；不需要新增另一個 worktree manager或 `dev start --agent`。

# User-facing SOP

## 現在立刻可用的做法

- **新的獨立工作不必等原 agent。** 用 `dev start <repo> --task "<name>" --base <explicit-committed-ref>` 建 sibling worktree；原 pane、cwd、index與 agent完全不動。
- 目前版本尚無 `start --json`，所以若要自動啟動新 agent，只能人工驗證 `dev start` 回報的 workspace內**恰有一個** shell pane、cwd等於新 worktree、且沒有 agent。任何不唯一或不一致就停止，不使用 current/focused/sidebar order猜測。
- 若新工作依賴原 checkout的 dirty state，先等 owning agent做 checkpoint commit，再用 exact commit/ref作 `--base`；不 stash、copy、reset、switch或 move原 pane。
- 如果原 agent已送 prompt/產生 plan：
  - **不同任務**：直接新 worktree + 新 session，原 agent繼續跑。
  - **同一任務要改成隔離工作**：先等原 session settled，並由使用者明確 handoff；沒有 code時可從目標 worktree `--resume <old-id> --fork-session` 建新 ID並重新產生/確認 plan；已有 dirty code時必須先 checkpoint。不要讓兩個 pane同時 resume同一 session ID。
  - `blocked`（例如 plan approval）不由自動化代答；使用者要先決定原 session是繼續、取消或 handoff。
- 不依賴 `EnterWorktree` 搬 SpecStory writer。需要 artifact locality時，在目標 worktree root重新啟動 SpecStory watcher/Claude session。

## 完成此功能後的 dev-cli skill workflow

1. Preflight：`HERDR_ENV=1`、`dev doctor`、`command -v specstory`、明示 task/base；確認新 stream不依賴未 committed state。
2. 執行 `dev start ... --json`，只接受 `mode=worktree`、absolute checkout、`runtime.name=herdr`、`runtime.opened=true`、`runtime.created=true`、非空 exact `root_pane_id`。
3. 用 `herdr pane get <root-pane-id>` 再確認 cwd等於 JSON checkout、是 available shell且無 agent；不符合即保留已建立的 task/worktree並停止，絕不 fallback到 pane list猜測。
4. 在同 workspace以 explicit root pane split出 watcher pane（`--cwd <checkout> --no-focus`），執行 `specstory watch claude --silent`；一個 worktree只需一個 watcher。
5. 在原 exact root pane執行 `herdr agent start <unique-name> --kind claude --pane <id>`。若是明示 handoff，native args使用 `--resume <uuid> --fork-session`；一般新工作啟動全新 session。
6. Agent ready後以 `herdr agent prompt <name> <explicit-prompt>` **不加 `--wait`**，讓它在背景工作；回報 worktree、branch、workspace、agent name。後續用 `agent get/read/wait`觀察，不 focus或送探測按鍵。
7. Commit前在該 worktree root執行 `specstory sync claude -s <exact-session-id>`，再用 exact transcript/plan paths stage；同 UUID多個 rendered candidates時要求明示 path。

若 SpecStory/Herdr缺失、runtime reuse/fallback、root pane缺失或驗證失敗，skill fail closed並留下可人工進入的 worktree，不偷偷改用原 pane。`working`、`blocked`、`unknown` 一律視為 active；`unknown` 不代表完成。

# Execution topology for this change

這次實作本身要遵守上述 boundary，而不是繼續在目前多人共用、已有 dirty changes的 `dev-cli` checkout寫碼：

1. **dev-cli change stream**：批准後以目前 committed HEAD作明示 base，用現有 `dev start`建立獨立 feature worktree。因 `--json` 尚不存在，bootstrap這一次人工驗證新 workspace唯一 root shell；從該 root啟動 worktree-local SpecStory watcher與新/forked Claude session。所有 dev-cli code、plan與 rendered history只在該 worktree stage。
2. 先完成可用的 `dev start --json`/root-pane contract。
3. **agent-skills change stream**：用新 dev-cli binary對 `/Users/david/Documents/Program/agent-skills` 的 clean `main`建立另一個 worktree/session，讓 generic history scripts、plan與SpecStory history全都在上游 feature branch；不從目前 dev-cli session跨 repo直接編輯。
4. 兩個 streams可獨立測試；dev-cli consumer snapshot更新必須等待 upstream已發布。未經明示要求不 commit/push；upstream push是 outward-facing gate，未批准時停在已測試的上游 worktree，dev-cli也不假裝 snapshot已更新。

# Implementation plan

## A. dev-cli: preserve review artifacts in generated `.gitignore`

- `internal/ignore/compose.go`
  - 從 `agentsSection` 移除 `.specstory/history/`。
  - 保留 `.claude/worktrees/`、`.claude/settings.local.json`、`.aider*`與 `.cursor/rules/_generated/`；它們是 ephemeral/local state，不是本次要求保存的 transcript/plan。
  - 將 `Extras.Agents`、section heading/comment改成「ephemeral coding-agent state」，明示 histories/plans保持可追蹤。
- `internal/ignore/ignore_test.go`
  - table-driven assertions：`.specstory/history/`、`.claude/plans/`、`.cursor/plans/`、`.opencode/plans/`、`.specify/`、`.codex/`不得被 common agent rules排除；worktrees/local settings仍要排除。
- 新增 `internal/cli/gitignore_contract_test.go`
  - temp repo執行實際 `dev gitignore --offline`，用 `git check-ignore --no-index`驗證 ignore語意；設 `GIT_CONFIG_GLOBAL=/dev/null`、`GIT_CONFIG_SYSTEM=/dev/null`隔離使用者 global excludes。
- 更新 `internal/cli/gitignore.go`、README與 `scripts/e2e.sh` 的說明/contract。
- root `.gitignore` 功能上已正確，只忽略 `.claude/worktrees/`，本次不加強制 `!` 規則去覆蓋使用者明示 opt-out。

## B. dev-cli: expose an atomic parallel-agent launch target

### Runtime contract

- 在 `internal/runtime/runtime.go` 新增一次性 `OpenResult`，至少包含：
  - `Handle`
  - `Surface`（如 `worktree`/`workspace`）
  - `Opened`
  - `Created`
  - `RootPaneID`
- `Runtime.Open`與 `WorktreeOpener.OpenWorktree` 改回傳 `OpenResult`；更新 `None`、Tmux與所有 callsites只取 `.Handle`。`Task.RuntimeHandle`仍只持久化 workspace/session handle；pane ID是易失 create result，不寫 task TOML。
- `internal/runtime/herdr.go`
  - 從同一次 `workspace create`/`worktree open` JSON解碼 `root_pane`。
  - 只有 response證明新 layout時設定 `Created/RootPaneID`；reuse不提供 pane。
  - worktree-open fallback需如實標 `Surface=workspace`，不冒充 first-class worktree。
  - 保持新建預設 `--no-focus`；不要使用 focused/current pane推導結果。
- `internal/wt/manager.go`
  - `CreateResult`傳遞 runtime result；worktree/runtime非致命失敗仍保留 checkout，但 machine result中 `opened=false`/pane空值。

### `dev start --json`

- 在 `internal/cli/start.go` 加 `--json`，不新增 `--agent`或新 command。
- stdout只輸出單一 JSON object；diagnostics/provision warnings留 stderr，`runtime=none`時不混入 shell `cd` directive。
- 最低 schema：

```json
{
  "task_id": "...",
  "repo": "...",
  "repo_path": "/absolute/repo",
  "branch": "feat/...",
  "base": "<explicit-ref>",
  "mode": "worktree",
  "worktree_path": "/absolute/worktree",
  "checkout": "/absolute/worktree",
  "runtime": {
    "name": "herdr",
    "handle": "w7",
    "surface": "worktree",
    "opened": true,
    "created": true,
    "root_pane_id": "w7:p12"
  }
}
```

- JSON path永遠 absolute，不使用 `config.Contract`；human output保持相容。
- 若 task save在 checkout/runtime side effects後失敗，維持非零且不輸出成功 JSON；skill不得盲目 retry，需報告已可能建立的 worktree供人工 reconcile。

### Tests

- 新增 `internal/runtime/herdr_test.go`：fake Herdr 0.8.2 envelopes，鎖 root pane解析、surface/created、`--no-focus`、reuse/fallback/malformed行為。
- 更新 `internal/runtime/runtime_test.go`、Tmux/None tests與全部 runtime callsites。
- 更新 `internal/wt/wt_test.go`：OpenResult傳遞、runtime failure不回滾 checkout但不宣稱 pane。
- 新增 `internal/cli/start_json_test.go`：schema、absolute paths、純 stdout、stderr diagnostics、none/tmux/failure/reuse無 launchable pane、human output不變。

## C. dev-cli skill: make parallel background work a first-class workflow

- 在 `internal/help/topics/agents.md` 加入「observe / spawn independent / handoff same task」decision table與 current workaround。
- 新增 `internal/skill/dev-cli/references/parallel-agents.md`，放完整 machine workflow：
  - exact `dev start --json` validation；
  - exact pane verification；
  - SpecStory watcher sibling pane；
  - Herdr `agent start`；
  - non-blocking `agent prompt`；
  - state handling與 exact artifact finalization。
- 更新 `internal/skill/dev-cli/SKILL.md`、`references/runtime-herdr.md`、`references/worktree-ownership.md`、`references/task-lifecycle.md`：
  - `dev`=durable checkout/task owner，Herdr=runtime/pane owner，SpecStory=worktree-rooted history writer。
  - 明示 `dev start`不自動啟動 agent，skill才負責跨 skill orchestration。
  - 校正「dev已 capture/resume agent session」的過度敘述：`Task.AgentSession`雖有欄位，production start/park/resume尚未自動寫入或 attach conversation。
  - 禁止中途 `EnterWorktree` 作為 SpecStory relocation保證。
- `internal/skill/dev-cli/references/commands.md` 只透過 `dev skill sync`生成，不手改。

## D. agent-skills upstream: exact session/history ownership

只修改 `/Users/david/Documents/Program/agent-skills/skills/local/agent-history-hygiene/`；不要直接編輯 dev-cli snapshot。

### `find-session.sh`

新增 exact selector contract：

```text
find-session.sh \
  [--session-id UUID] \
  [--specstory-path PATH] \
  [--newest] \
  [--format specstory|claude|both] \
  [--json] [--quiet]
```

- 先以 `git rev-parse --show-toplevel` anchor目前 worktree，避免 subdirectory錯誤。
- `--session-id`只比對 `<UUID>.jsonl`與 SpecStory header `<!-- Claude Code Session <UUID> ... -->`，不搜正文中的 UUID。
- `--specstory-path`用於 exact disambiguation，必須是本 worktree `.specstory/history/*.md` regular file且 marker一致。
- 同 UUID多個 raw/rendered候選回 `ambiguous`，不以 mtime決定。
- `--newest`是唯一允許 mtime heuristic的明示 fallback，與 exact selectors互斥並標示低信心。
- 無 selector回 `selector_required`；保留原三個 output keys，追加 status/confidence/source/candidates並正確 JSON-escape。
- exit：`0=unique resolved`、`1=usage/invalid`、`2=required/not-found/ambiguous`。

### `stage-agent-artifacts.sh`

```text
stage-agent-artifacts.sh --session-only \
  (--session-id UUID | --specstory-path PATH | --newest) \
  [--specstory-path PATH | --no-specstory] \
  (--plan PATH | --no-plan)
```

- session ID只接受唯一 SpecStory match；零匹配要求 `--no-specstory`，多匹配要求 exact path。
- plan沒有 session ID，永遠要求 `--plan`或 `--no-plan`；完全移除 newest-plan推斷。
- 全部 selector/path containment驗證完成後才以單次 `git add -- "${candidates[@]}"`修改 index；任一失敗不 partial stage。
- selector錯誤使用 exit 5；原 0–4維持。
- 不帶 `--session-only` 的 broad mode保持，但文件明說它是 branch/worktree-wide dirty artifact staging，不是 current-session inference。

### Hook, provenance, docs

- `bootstrap-project.sh --install-hook` 保留相容，但 generated hook只有在 caller提供 `AGENT_HISTORY_*` exact session/specstory/plan（或 explicit no-plan/no-specstory）環境時才 stage；不足時 visible warning + exit 0 + index不變。舊 hook呼叫新版 script也會在 `git add`前 exit 5。
- 保留並重用現有 `agent-commit-metadata.sh`：exact staging後由**staged snapshot**產生 `AI-Assisted-By`、`Agent-Transcript`、`Agent-Plan` trailers；不拿它反推 session。
- 更新 upstream `SKILL.md`、`references/transcript-session-discovery.md`、tests README，以及 public `docs/skills/agent-history-hygiene.md` / `.zh-TW.md`：
  - SpecStory output綁 wrapper launch checkout，不綁 branch；
  - worktree-first、一個 watcher per worktree；
  - commit前使用 `specstory sync claude -s <UUID>`，避免 bulk sync；
  - 同 UUID aliases與 exact path處理；
  - 不再宣稱 auto-hook能猜 current session。

### Upstream tests

新增 `tests/test_find_session.sh`、`tests/test_stage_agent_artifacts.sh`，並接入 upstream既有 `make test-skill`：

- 同 checkout branch switch仍看到相同 untracked/dirty histories，證明 branch不是 isolation boundary。
- worktree A/B各自 raw slug/history/index，exact UUID/path不可交叉 stage。
- unrelated較新 mtime不得蓋 exact較舊 session。
- 同 UUID slugless+slugged => ambiguous；explicit path可解。
- subdir、空白 path、outside-repo path、no match、marker mismatch。
- `--plan`/`--no-plan`與 `--specstory-path`/`--no-specstory`完整性。
- selector失敗時 cached index byte-for-byte不變；成功只 stage exact files。
- broad mode相容；generated hook無 identity no-op、有完整 env只 stage指定 artifacts。
- 現有 `test_agent_commit_metadata.sh`增加 assertion：trailers只反映 exact staged files。

## E. Publish and update the dev-cli consumer snapshot

Upstream tests通過後：

```bash
# agent-skills
make test-skill
make validate
uv sync --extra docs
make docs-build
```

本次不修改 `assets/redact_secrets.py`，因此不需要新 `ahh-v*` pre-commit hook tag。

只有使用者明示要求 commit/push後，才發布 upstream branch/main；發布後在 dev-cli feature worktree執行：

```bash
npx skills update agent-history-hygiene -p -y
```

review `.agents/skills/agent-history-hygiene`、`skills-lock.json`與 `.claude/skills` symlink；更新會一併帶入目前 upstream已比 consumer新的 provenance/realpath修正。`dev skill sync`只重生內建 dev-cli command reference，不能用來更新第三方 AHH snapshot。

# Approved refinements incorporated during implementation

The user extended the dev-cli A–C slice before completion with these constraints:

- Add optional pane-level Herdr `AgentActivity` discovery. Compare canonical Git
  worktree roots, resolve the current pane with a non-focus query before
  exclusion, and treat every recognized state (`working`, `blocked`, `idle`,
  `done`, `unknown`) as occupied. Guard writer claims (`start --direct`,
  `start --branch-only`, `resume`); pure repo/worktree/TUI open navigates to the
  live owner without claiming a writer. Default worktree creation stays allowed.
  The single `--allow-shared-checkout` override is only for coordinated writers.
- A later user smoke test superseded the proposed `--runtime-label` and origin
  metadata: use Herdr's native nested repo/worktree provenance instead.
  Worktree-mode `dev start` uses the same `repo/branch` label as `dev wt create`
  and Herdr open is pinned with Git-derived `--cwd <parent-root>`; JSON returns
  only the child workspace/root pane and `surface=worktree`. Herdr
  `already_open=true` is reuse and never exposes a launchable pane.
- Prefer profile-aware SpecStory launch in the exact root pane. Standard
  Claude/Codex put the complete native command inside the single quoted `-c`
  value; `*-copilot-once` wrappers run directly because they already invoke
  SpecStory and preserve argv. Claude's wrapper requires its proxy running and
  preserves an existing pin; Codex may auto-start its proxy path. No watcher.
- A child process does not inherit parent permissions, and resume/fork does not
  restore Claude bypass. Effective SpecStory/provider commands are authoritative
  and may already be dangerous; never append a conflicting safer mode. A safer
  launch needs a fully specified command/backend pin or verified safer wrapper.
- Do not universally copy `.claude/settings.local.json`. Only explicit
  sticky/plain-Claude profiles opt in via exact `worktree.include`; source leaf
  swaps, symlinked destination parents, stale existing destinations, and
  no-content logging are covered.
- Never auto-clean up on agent `done`. `dev park` closes runtime and keeps the
  worktree; `park --cold --push` closes/removes it; `done --ff` integrates and
  cleans up; `done --pr` leaves review state intact; `sweep` stays report-first.
  Reject `--cold --keep-session`; verified close failures stop before checkout
  deletion. Persist runtime backend provenance with handles, validate checkout
  coverage, and reopen stale handles rather than feeding them to another backend.
- The reproducible e2e sweep failure was a `pipefail`/`grep -q` SIGPIPE: preserved
  evidence showed task `done`, correct reap output, and `dev=141, grep=0`.
  Capture full sweep output and match the exact task action instead.

# Verification

## dev-cli worktree

```bash
gofmt -w <only touched Go files>
go test ./internal/ignore
go test ./internal/runtime ./internal/wt
go test ./internal/cli -run 'Gitignore|Start.*JSON'
make skill-sync
make skill-check
go test ./...
go vet ./...
./scripts/e2e.sh
```

用隔離 HOME/Git config驗證 `.gitignore`；用 fake Herdr envelopes驗 JSON contract。另做一次受控 Herdr smoke：建立 disposable task/worktree，確認 `dev start --json` 的 root pane與workspace相互對應、cwd正確、原 pane/focus不變，且只清理由本次建立的workspace/worktree。E2E最後連續執行至少兩次。

## agent-skills worktree

```bash
bash -n skills/local/agent-history-hygiene/scripts/{find-session,stage-agent-artifacts,bootstrap-project}.sh
make test-skill
make validate
make docs-build
```

最後各 repo只從各自 worktree以 exact UUID/transcript/plan paths stage，執行 `scan-staged.sh`與 provenance helper。未經明示要求不 commit/push。

# Explicitly out of scope

- 不新增 `dev start --agent`、新的 worktree manager或自動回答 agent approval。
- 不實作 `Task.AgentSession` production capture/resume、workspace-level agent aggregation或TUI pane detail。
- 不修現有 `--focus` wiring，parallel workflow固定 no-focus。
- 不依賴 Claude `EnterWorktree`、Git branch name或 mtime推導 SpecStory ownership。
- 不把 raw `~/.claude/projects/*.jsonl`提交到 repo；提交的是 worktree-local rendered transcript與plan。
- 不直接修改 `dev-cli/.agents/skills/agent-history-hygiene`，也不在上游未發布時偽造 consumer snapshot更新。

# Critical files

## dev-cli

- `internal/ignore/compose.go`
- `internal/runtime/runtime.go`
- `internal/runtime/herdr.go`
- `internal/wt/manager.go`
- `internal/cli/start.go`
- `internal/help/topics/agents.md`
- `internal/skill/dev-cli/references/parallel-agents.md`

## agent-skills upstream

- `skills/local/agent-history-hygiene/scripts/find-session.sh`
- `skills/local/agent-history-hygiene/scripts/stage-agent-artifacts.sh`
- `skills/local/agent-history-hygiene/scripts/bootstrap-project.sh`
- `skills/local/agent-history-hygiene/scripts/agent-commit-metadata.sh`（重用/測試）
- `skills/local/agent-history-hygiene/references/transcript-session-discovery.md`
- `docs/skills/agent-history-hygiene.md`
- `docs/skills/agent-history-hygiene.zh-TW.md`

# Sources

- [SpecStory output path resolution (v2.10.0)](https://github.com/specstoryai/getspecstory/blob/ec9c70fe5f7bb752d5e5080f0515f6f43d9dc9cb/specstory-cli/pkg/utils/path_utils.go#L50-L140)
- [SpecStory Claude path mapping](https://github.com/specstoryai/getspecstory/blob/ec9c70fe5f7bb752d5e5080f0515f6f43d9dc9cb/specstory-cli/pkg/providers/claudecode/path_utils.go#L76-L131)
- [SpecStory bulk session discovery](https://github.com/specstoryai/getspecstory/blob/ec9c70fe5f7bb752d5e5080f0515f6f43d9dc9cb/specstory-cli/pkg/providers/claudecode/provider.go#L279-L315)
- [SpecStory watcher lifecycle](https://github.com/specstoryai/getspecstory/blob/ec9c70fe5f7bb752d5e5080f0515f6f43d9dc9cb/specstory-cli/pkg/providers/claudecode/watcher.go#L116-L368)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
- [Claude Code sessions](https://code.claude.com/docs/en/sessions)
- [Herdr CLI reference v0.8.2](https://raw.githubusercontent.com/herdrdev/herdr/v0.8.2/docs/next/website/src/content/docs/cli-reference.mdx)
