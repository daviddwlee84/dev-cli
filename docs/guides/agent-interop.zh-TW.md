---
lang: zh-TW
description: 複製、遷移、鏡像與重建選定的 agent 設定，同時分開處理憑證與所有權。
authority: project-and-upstream
status: evolving
verified_on: 2026-09-07
tested_with: skills 1.5.23 compatibility fixtures and synthetic MCP 2025-06-18 servers
---

# Agent 設定互通

Agent 原生檔案維持 runtime 設定來源。Transfer plan 顯示精確來源、目的地、
scope 與變更，`apply` 重新驗證後才套用。一般複製和 skill 相對連結，
不要求 agent 啟動時安裝 dev。

## 選擇關係

| 需求 | 操作 |
|---|---|
| 同 checkout 的多個 agent 共用 skill | 從 `.agents/skills` 建立逐項 `mirror` |
| 另一個 repo 重用 skill | 驗證上游的 `prepare`，再 `install` |
| 獨立內容 | `copy`，後續來源修改不會傳播 |
| 遷移選定內容 | `move`，先寫入並驗證目的地再清理來源 |
| 一份 MCP 定義供多個 client 使用 | `mirror`，經審閱的 `refresh` 更新投影 |
| 共用指令 | `CLAUDE.md -> AGENTS.md`，或 Claude 的 `@AGENTS.md` import |

各 repo 應保持獨立可重現。提交原生設定、相對 symlink 和 project
`skills-lock.json`；需要離線重現時，也提交安裝後的 skill tree。
跨 repo mirror 明確依賴另一個本機 checkout，並非可攜的套件安裝。
Codex 會從 `~/.agents/skills` 載入 user skills；只想選擇性分享的 skills，
可放在獨立私人 Git repo，再安裝到各專案。
[Codex skills](https://developers.openai.com/codex/skills/)

## Skills

```bash
dev skill transfer plan example --from-agent universal --to-agent claude-code --mode mirror
dev skill transfer apply --plan <id>

dev skill transfer prepare example --from-repo api --to-repo web
dev skill transfer plan example --from-repo api --to-repo web --mode install --prepared <id>
dev skill transfer apply --plan <id>
```

只有 `prepare` 會執行可信任、已安裝的 `skills@1.5.23`，並可能連網。
它使用私人暫存目錄，驗證 provider 版本、來源、原生 lock 與本機／暫存內容。
Plan/apply 不執行 installer。只有 lock、尚未安裝內容的 checkout 也能重建：
把來源與目的地選成同一 checkout；相符的既有 lock 保留原始 bytes。

Provider 版本、lock schema、skill 內容分開檢查。Project v1 使用內容 hash，
global v3 使用 Git tree hash。未知版本／欄位、無法重現的非 ASCII 排序、
上游內容漂移，以及 provider 無法 clone 的裸 commit ref 都會明確停止。
不會改用 latest、npx 或自行 fallback 成 copy。
既有 `skill add` wizard 與 `skill update` 仍是明確執行的原生 provider 操作，
不代表 frozen restore。
[Project lock](https://github.com/vercel-labs/skills/blob/v1.5.23/src/local-lock.ts)、
[global lock](https://github.com/vercel-labs/skills/blob/v1.5.23/src/skill-lock.ts)、
[上游 install](https://github.com/vercel-labs/skills/blob/v1.5.23/src/install.ts)

Move 會驗證 lock 管理的內容、建立目的端原生 membership，再清理相符的來源
membership 與精確的原生 symlink。其他 agent 的獨立副本保留。
仍有跨 repo mirror 使用來源時，必須先遷移那些 consumer。
本機 copy 不會捏造上游 hash。

## MCP：五種明確的 adapter

```bash
dev mcp transfer plan --server grafana --from-agent claude-code --to-agent codex --mode copy
dev mcp transfer apply --plan <id>

dev mcp transfer plan --server grafana --from-agent claude-code --to-agent codex \
  --mode mirror --secret-env-file .claude/settings.local.json
dev mcp transfer apply --plan <id>
dev mcp transfer refresh <relation-id>
```

五種 adapter 為 Claude Code、Codex、Cursor、Gemini CLI、OpenCode。
Scope 明確選 project 或 user/global；`--from-repo`、`--to-repo` 選精確 checkout，
並支援各 agent 原生 user-home override。Claude 在 `~/.claude.json` 內的
project-local entries，以及 plugin、system policy、managed config，
仍是 inventory 範圍，不是 transfer scope。

轉換支援共同子集：stdio command/arguments、相容的 env reference、
明確的 Streamable HTTP URL/header reference。無法靜態判定的 remote 宣告，
需明確選 `--transport streamable-http`。未知欄位、停用的來源、
相關 tool policy、SSE、OAuth 設定／token store、動態 helper 都不會被偷偷忽略。
OpenCode 支援 classic `mcp.<name>`；v2 的 `mcp.servers` 需要獨立相容設定。

Writer 只修改選定 JSON/JSONC member 或 TOML table range，保留其他設定與註解。
選定 server 使用 inline/dotted TOML，或整個 `mcp_servers` 是 inline container
時，需要原生編輯。相等的既有 stanza 是不接管所有權的 no-op；
可用 `--adopt --mode mirror` 接管。變更已管理的來源也需要明確 adoption。

MCP mirror 透過審閱更新投影，不跑背景同步。所有權以 stanza 為單位；
修改其他 model 設定或新增第二個 server，不會使原關係失效。
Undo 只還原選定 stanza，保留後來的無關修改，空的原生 container 檔案可能保留。

### 憑證與選配 launcher

Secret 值應留在版本控制內容之外，設定只放 env 名稱。
跨 scope 使用憑證需明確指定 `--bind SERVER_ENV=PROCESS_ENV`；
預覽顯示 reference 名稱，不顯示值。

優先使用原生 mapping。Codex 的 stdio 同名變數使用 `env_vars`，
HTTP 使用其原生 reference 欄位。Stdio 若需要本機 JSON `env` 來源或變數改名，
才選 `--bridge` 或 `--secret-env-file`。後者相對於目的 scope，
必須是 owner-private 的 regular file。這只相容既有本機檔案，
dev 不建立 secret 檔，也不把它複製到另一個 project。

Launcher 在啟動時解析選定 reference：process environment 優先，
再讀選定本機 `env`，最後才使用明確 fallback。空值表示已設定，不是缺值。
輪替在下次啟動生效，其他 model/provider 變數不會傳給 server。
Plan 可能解析來源 policy 檔案，但憑證的解析與注入只在啟動時發生。

Launcher 綁定本機 checkout、executable 與指定 dev 安裝。
搬動或 clone 後需重新建立 binding；來源執行／reference 結構改變需要 refresh，
一般 secret 輪替則不需要。Stdio launcher 無法注入已啟動的 HTTP client；
依賴 launcher 的宣告也不能透過 move 移除其來源。

Env 不是對同一使用者之其他程序的隔離。既有 secret manager 可以在 client
啟動前注入，例如 [1Password runtime injection](https://developer.1password.com/docs/cli/secrets-environment-variables/)。
尚未實作原生 Keychain／Vault／YubiKey adapter；OAuth 登入 store 與私鑰檔案
不會成為遷移 payload。

### 選配連線證據

```bash
dev mcp transfer check <applied-id> --json
```

這會明確啟動 stdio server 或連線 HTTP endpoint，協商 MCP 2025-06-18，
再送出 initialized notification，不呼叫 application tools。
結果區分 `configured` 與 `initialized`；
authentication 與 native-client loading 保持 `not-checked`。
一般 list/plan/apply 不探測 server、不安裝工具，也不聯絡 endpoint。
[MCP lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle)

## 指令與選配 recipe

```bash
dev instructions transfer plan --from AGENTS.md --to CLAUDE.md --mode mirror
dev instructions transfer plan --from AGENTS.md --to CLAUDE.md --mode mirror --style import
dev instructions transfer apply --plan <id>

dev skill transfer export <applied-id> > .agents/interop.toml
dev skill transfer recipe .agents/interop.toml --entry skill-example
dev skill transfer apply --plan <id>
```

Import 在前方加入共用來源，保留 Claude 專屬內容原始 bytes 與順序。
Symlink 取代相同 regular file 需要 `--adopt`，不同內容會衝突。
Move 不自動泛化專案假設，也不會留下另一個懸空的原生 instruction link。
[Claude 指令](https://code.claude.com/docs/en/memory)

Recipe 是選配 v1 TOML，使用命名的 `transfers` entries，每次 fresh plan 選一項。
只記錄 copy/mirror 意圖與原生來源 reference，不放 checkout 絕對路徑、憑證、
receipt 或本機 launcher binding。User entry 解析目前 agent 的原生 home。
未知欄位或 profile 會停止。跨 project 絕對路徑、move 與 prepared payload
保持為明確操作；上游安裝本來就有原生 lock。

## 復原與平台邊界

`transfer status` 顯示操作紀錄；`transfer undo <id>` 建立反向計畫。
Receipt 和 inventory 新增的 `interop` metadata 不是目前 client activation
或 health 的證據。Apply 加鎖並重新驗證檔案／checkout／Git 身分。
Stale plan 不產生新效果；後續失敗分開回報已確認步驟與尚未確認的 in-flight 步驟。

復原資料位於 `paths.state_dir/agent-interop`，目錄 0700、檔案 0600。
這是本機私人 durable state，不是 cache 或同步目錄。
既有混合設定的復原資料可能含無關憑證，因此拒絕 Git-backed state 位置。
目前沒有自動到期清理；launcher、refresh 或 undo 仍依賴時必須保留。
不要提交或同步此目錄。Crash 後未確認的效果需要檢查，dev 不會猜測所有權並刪除。

初始 mutation backend 為 POSIX。原生 Windows transfer 在驗證 protected-DACL／
recovery adapter 前，會於寫入 agent 檔案前停止；inventory 和既有原生 provider
命令仍可使用。Symlink 失敗不會默默變成 copy、junction 或要求提權。
外部 editor、raw Git 與原生 provider 命令仍在 dev 的協作式 transaction 保證之外。
