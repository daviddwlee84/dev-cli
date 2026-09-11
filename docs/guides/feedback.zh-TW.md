---
description: 建立私有本機 feedback、發布已檢視的 GitHub issue，並將已驗證的隔離修復工作區交接給明確選定的 agent。
lang: zh-TW
authority: project
status: evolving
verified_on: 2026-09-11
---

# Feedback 與隔離修復

Dev 出錯時，先保留足以解釋問題的證據，再決定回報或修復。`dev feedback` 提供
簡單終端表單，明確的子命令供 agent 與腳本使用。指令失敗本身不會自動發布 issue、
建立修復工作區或啟動 agent。

## 盡量維持原任務

Agent 應先區分使用方式、環境問題與疑似 dev bug。SSH transport 問題可在授權
範圍內使用 [連線診斷](ssh-hosts.zh-TW.md#ssh-diagnosis)。Timeout、gh 未登入或
安全檢查拒絕，不直接代表 upstream 有缺陷。

遇到 panic、非預期內部錯誤或文件與行為不符，先準備最小本機草稿與可能的來源
checkout，再提供回報、修復、兩者或略過的選擇。原任務有安全替代方式時可繼續。
深入調查、修復及額外 subagent 都需要涵蓋新增範圍與成本的明確同意；同意後的
subagent 使用獨立 context／checkout，只帶回精簡結果。既有明確授權沿用，不重複
詢問同一件事。

## 準備與檢視本機草稿

```bash
dev feedback
dev feedback draft --title 'SSH menu panic' --body-file report.md --json
dev feedback draft --title 'SSH transport diagnosis' --body-file report.md \
  --diagnostic diagnosis.json --json
dev feedback issue <id> --json
```

`--body-file -` 從 stdin 讀取有大小上限的 Markdown。內容包含最小重現、預期與
實際行為。CLI 僅自動加入版本、OS／arch 與安裝方式，不自動收集 doctor stdout、
環境、設定或 session history。SSH 診斷會轉成有限的公開階段原因碼；endpoint、
user、key path 不進入附件。

報告位於 configured `paths.state_dir/feedback/<id>/`，預設為 XDG dev data
目錄。`public.md` 是可編輯公開草稿；`context.json` 保留明確提供的私有證據。
報告與操作紀錄是 durable state，不是 cache，也不自動清除。POSIX 使用 owner-only
權限，Windows 驗證受保護且只授予目前 user 與 SYSTEM 的 ACL。

已知的 credential、URL、endpoint、path 與 SSH option 格式會在 preview／publish
再次遮罩。任意私有人名或代號仍須檢視，不能假設自動遮罩辨識了所有名稱。Preview
顯示確切 operation、repository、body 與 content/target revision；編輯會改變
revision。Dev config 損壞時仍可建立草稿，會告知使用預設儲存位置。

## 明確發布至 GitHub

```bash
dev feedback issue <id> --search --json
dev feedback issue <id> --publish
# 已檢視並授權該確切 revision 後：
dev feedback issue <id> --publish --yes --revision <revision> --json
# 預覽要加入現有 thread 的 comment：
dev feedback issue <id> --existing 123 --json
```

預設 issue target 是 `github.com/daviddwlee84/dev-cli`；`--repo
[host/]owner/name` 才明確改變目的地，cwd 與 GH_HOST 不會暗中選擇目標。
`--search` 是獨立網路動作，只列出候選。`--existing` 必須指定正的 issue number，
不會自行選 thread。使用 `--publish --yes` 前，先檢視該目標與操作的 revision。

發布沿用 optional gh 的認證，以 argv-only API 與確切 JSON stdin 傳送；body
不插入 shell source 或錯誤 argv。缺少 gh、未登入、權限拒絕及未知遠端結果分開
回報，離線仍能使用本機草稿。

POST 前先保存 unknown 操作紀錄。回應遺失後重跑，會先查詢確切 report/revision
marker。查核不完整或未找到結果時維持 unknown，不自動重送 POST。已確認建立的
issue 重跑會回傳原 URL；修改過的草稿可經明確檢視後作為 comment 發布。

## 規劃修復工作區

```bash
dev feedback repair <id> --base main --json
dev feedback repair <id> --repo ~/Projects/dev-cli --base main --json
dev feedback repair <id> --apply --plan <plan-id> --yes --json
```

來源順序為明確 `--repo`、`[feedback].source_repo`，再使用 REPOS 的本機 discovery。
設定值須展開為絕對路徑。無效的 explicit／configured path 直接報錯，多個候選
回傳供選擇；資料夾名稱不是身份證明。本機 fork 需要確切的 dev-cli upstream
remote，只有 fork origin 不足以確認。

```toml
[feedback]
source_repo = "~/Projects/dev-cli"
```

沒有來源時，preview 建議使用既有 `dev try --clone`，明確取得會保留的 Try clone
後再指定該 checkout 重新預覽。重用 Try 前先確認身份與既有工作，不將獨有修復
放在會自動刪除的暫存 clone。

必須明確給 base ref。計畫固定其 commit、canonical repository identities、
目標 branch/path、相關設定與 report revision；Apply 在 Git lifecycle lock 內
重新觀察，拒絕 stale plan，再共用 start service 建立 HOT worktree task。
來源修改保留原處；準備時不開 runtime、不 provision、不下載 submodule、不啟動
agent。部分失敗保留 checkout，指出 task／store 恢復問題，不刪除工作。

## 交接給 agent

```bash
dev prompt render feedback-fix <id>
# 明確同意額外 agent 與 profile 後：
dev prompt open feedback-fix <id> --agent <profile>
```

目前 agent 可讀取 prompt 並在回傳的 checkout 工作。新 process 必須明確指定
profile，即使已設定 default profile 也不自動選擇。Prompt 綁定報告已準備的
checkout/task，拒絕變更、locked、missing 或 partial binding。此內容是私有
context，不是 issue body。

先讀來源 AGENTS.md、重現問題、做 focused change、跑相關測試並同步
changelog/help/docs/skill。交付 diff 摘要、測試結果、保留位置與原任務的下一步。
Fork、push 與 draft PR 另看既有明確授權，使用現有 Git／gh 與確切 body file。
不自動 merge、不替換 installed binary，也不自動 retire 工作區。
