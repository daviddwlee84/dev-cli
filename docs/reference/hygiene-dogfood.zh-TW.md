---
description: 人工 post-writer dogfood 與已發布 Git 歷史的獨立清理影響評估。
lang: zh-TW
authority: project
status: evolving
verified_on: 2026-09-11
---

# 人工 hygiene dogfood

先讓實作與活躍 writer 分開，最後再處理紀錄。另開沒有 SpecStory 的 agent，並不會
停止原本的 SpecStory writer；不得為了讓清理通過而關閉其他 session。

## 修改真實紀錄之前

1. 建置 feature binary、執行 native hygiene 測試，以及
   `python scripts/test-hygiene-hooks.py --dev <absolute-binary>`。後者使用隔離 HOME，
   驗證真正的 pre-commit：假 key、hidden 檔案、同一行 placeholder、alternate index、
   executable modes、Git config 不變，以及乾淨提交。
2. 執行 `dev hygiene status`、preview setup 並確認有效 hook 鏈。測試 session 的
   PATH 指向新 binary；不要手動覆寫套件管理器安裝的 binary。
3. 預覽 `rules import --from ssh` 與 `--from local`，分清楚私密值、公開／常見名稱
   後才選 ID。私人政策保留在 Git 外。
4. 執行 worktree 與 history scan，保留範圍、排除項與缺口。候選須分類後才建立
   有理由的精確例外；命中數量不是有效 credential 的數量。

## 最後人工觸發

確切 provider／wrapper 已退出並寫完 transcript 後，從外部終端執行。另開不記錄
SpecStory 的 agent 需要另外的適用授權，也仍需確認原本的 writer 已停止。

```bash
dev hygiene scan --scope worktree --json
dev hygiene redact --report <fresh-report-id> --file <reviewed-file> --json
dev hygiene redact --apply --plan <plan-id> --yes --writer-stopped
dev hygiene scan --scope worktree --json
git diff --check
# 檢視後只 stage 指定檔案，正常 hooks 保持啟用。
```

只選已檢視的 finding／檔案。可辨識的 live agent、來源／metadata 變動、plan 簽章
不符仍會阻擋。中斷後保留 ledger 與 recovery receipts，不盲目重跑；恢復前也要
檢視路徑。短暫 byte stability 不能替代 writer 已退出的證據。

若變更 source/config，執行對應測試及 skill/docs checks。HEAD 修正以新 commit
向前提交。還在寫入的 transcript 明確留為待處理，不反覆覆寫。

## 規劃時的稽核證據

機器已安裝 pre-commit、gitleaks 與 executable global hook，但 repo 缺少兩者設定，
所以一般 commit 會跳過該 gate；artifact finalize 的手動掃描是另一條路徑。

本機可達歷史的 gitleaks audit 有 416 個候選，其中 384 個來自較寬鬆的 password
規則，21 個位於測試檔。不能解讀為 416 個已確認 live secrets。以本機 SSH literal
比對目前檔案，找到六個已設定 IP、共 163 次出現；通用帳號／主機名則有大量非私密
命中，需要選擇與分類。

歷史約有 717 MB unique blob data，最大 transcript 約 70 MB。第一次三分鐘掃描
超時；後續完成的是該 gitleaks 指令的 patch scope。新實作另測 merge-only 內容，
並明確列出 unsupported／excluded 資料。

詳細報告只留本機私人儲存。Raw report、私人字典、recovery images 與 credential
原文都不進 Git／CI logs。

## 實作 dogfood 結果

新 setup 已加入三個 repo 設定檔並保留原本的全域 hook。本功能真正提交時的 gate
完成 70 個 staged 檔案掃描，0 個阻擋、36 個通用隱私警告。合成 hook 測試也驗證
阻擋時不會改 caller 的 Git config、index entries 或 executable modes。

新 CLI 對本機全部可達歷史的稽核達到明確的 40 分鐘上限，保存 `partial` receipt，
沒有證明歷史乾淨。CI range 驗證亦發現舊 transcript 含無效 UTF-8 或 NUL bytes；
這些是明確缺口，不是成功略過。Range 已限縮為變更版本，全量稽核仍會保留歷史缺口。
最後人工階段須分類候選，並處理或明確界定不支援的 artifacts，才能宣稱已涵蓋真實歷史。

## 已發布歷史的獨立評估

任何歷史變更前，先建立私人表格：每個確認項目的類型、HEAD 是否仍有、引入／包含
的 commit、本機可達 branches/tags 及已知遠端副本。遠端資料需明確 fetch/query；
不能僅靠本機 refs 宣称列完所有公開副本。

| 選項 | 效果與殘留 |
| --- | --- |
| 向前修正 HEAD、加強預防 | 保留 commit/tag 身分；舊 objects、PR、clone、cache 仍可能有資料。 |
| 撤銷／輪換 credential | 在 provider 使 credential 失效，不抹除舊 bytes；只記錄完成狀態，不公開值。 |
| 協調式歷史清理評估 | 會改 commit 身分／簽章，影響 PR、clone，需要另外的發版及分發遷移方案。一般 redact/setup 不授權 rewrite。 |
| Provider 協助移除 | 是否可行取決於 provider 與資料類型；不能從一次 force push 推定完成。 |

Dev-cli 的 immutable release tags 是版本 authority，且下游還有 Go module、
Homebrew/Scoop、已下載 archives。已發布 tag 不得移動或重用。提案前需盤點受影響
版本與產物；新版本無法移除已被下載的副本。

Git author 姓名／email、帶個資的檔名也列入評估；文字 redaction 不改 commit metadata
或 rename 檔案。GitHub 的[敏感資料移除說明](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository)
說明 rotation-first，以及 force push、fork、PR reference、cache 的限制。
