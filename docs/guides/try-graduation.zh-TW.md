---
description: 透過確認本機名稱與發布選項讓 Try 畢業，保留 demote 後的命名預設，並理解遠端操作部分失敗。
authority: project
status: maintained
verified_on: 2026-09-24
minimum_version: v0.3.1
lang: zh-TW
---

# 讓 Try 畢業

`dev tries graduate [try]` 將實驗搬到
`<project_root>/<category>/<name>`，保留 catalog identity 與目前檔案。
省略 Try argument 時，使用目前目錄所在的實驗。永久捷徑 `dev graduate`
提供相同行為。

## 選擇名稱與發布方式

在 terminal 執行會開啟 wizard：選擇專案名稱與可選的 category、選擇發布方式、
檢查目的地與效果，最後確認。預設是本機專案。TRY → Ctrl+O → graduate
使用同一個流程。取消會返回並重新整理 dashboard；成功時先離開 dashboard，
再開啟畢業專案或進行 shell 目錄交接。

| 選項 | 預設行為 |
|---|---|
| 本機專案 | 本機搬移，保留所有既有 remotes |
| 加入既有 URL | 將 URL 加為 origin；除非選取，否則不 push |
| 在 GitHub／GitLab 建立 | 建立 private repository，預設 push |

只有 Try 沒有任何 remote 時，才提供發布選項。若已有任一 remote，即使只有
`upstream`，graduation 也會保留全部 remotes；明確要求新增或建立 remote
會在搬移前失敗。改本機專案名稱不會改 package、module、原始碼或既有遠端
repository 的名稱。

沒有 Git 的 Try 會初始化 Git，並沿用既有 graduation 流程在需要時建立第一個
commit。搬移與發布仍是分開的效果。審閱計畫綁定來源 identity 與內容；
prompts 期間若計畫變動，必須重新審閱才可套用。

## Scripts 與預覽

`--yes`／`-y` 跳過 wizard 與確認；非互動輸入也直接使用明確 flags 與預設值。
`--dry-run` 只顯示計畫，不提示輸入、不檢查 forge authentication、不搬檔案、
不發布。取消不套用 graduation 或 publication。

```bash
dev tries graduate parser                 # 互動 wizard
dev tries graduate parser --name parser-core --category Tools --dry-run
dev tries graduate parser --name parser-core --category Tools --yes
dev tries graduate parser --remote-url git@github.com:example/parser-core.git --yes
dev tries graduate parser --remote-url git@github.com:example/parser-core.git --push --yes
dev tries graduate parser --forge github --namespace example --visibility private --yes
```

`--forge` 接受 `auto`、`github`、`gitlab`、`none`；明確指定 `github` 或
`gitlab` 代表建立 remote。既有 `--remote` 保留建立 remote 的用法，未覆寫時
自動選擇 provider。`--namespace` 指定新 remote 的 owner／group。
`--visibility` 接受 `private`、`public` 與 GitLab 的 `internal`；建立時預設
private。既有 `--private` 與 `--push` flags 繼續支援。

建立 remote 預設 push；加入 URL 預設不 push。明確 `--push` 或
`--push=false` 可控制這兩條路徑。本機 graduation 不 push 既有 remotes。
互斥的 remote 選項、對 URL 使用建立專用 flags、不相容的 visibility，或明確
`--private` 與 `--visibility` 矛盾，都會在本機搬移前失敗。

## Demote 後再次畢業

執行 `dev tries demote` 後，下次 graduation 的預設名稱依下列順序選取：

1. 本次明確的 `--name`。
2. 上次成功本機 graduation 的可選欄位 `graduated_name`。
3. 舊版 `graduated_path` metadata 中有效的專案名稱 basename。
4. 目前 Try 名稱去掉日期前綴。

舊 POSIX／Windows 路徑可在記憶體中提供 fallback，不會因讀取而改寫 catalog。
預覽或取消不改變記住的名稱。成功本機 graduation 會一起記錄名稱、時間與
目的地；後續發布失敗仍保留這些已完成的本機事實。Category 每次重新選擇，
不記住上一次的值。

Demote 保留目前檔案、Git history 與 remotes，不撤銷發布。因此再次 graduation
通常保留這些 remotes 並只在本機搬移。詳見
[demotion 與搬移條件](../reference/cli-v0.3.zh-TW.md)。

## 遠端操作失敗時

Provider／authentication preflight 失敗會在本機搬移前停止。搬移成功後，
add／create／push 失敗會回傳非零 exit code，並分別回報已完成的本機 graduation。
本機專案與已完成的遠端效果都會保留，不自動重試，也不回滾 publication 或本機
搬移。先檢查回報的目的地與遠端結果，再決定後續動作；再次執行 graduation
不是重試遠端操作的方法。

發布會在每個步驟前重新驗證畢業 checkout 與審閱過的 branch／commit。
若在 remote 建立後發生變動，會保留 remote 並跳過 push；結果分別回報
已完成與跳過的效果。
