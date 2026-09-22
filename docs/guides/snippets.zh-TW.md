---
lang: zh-TW
description: 透過 CLI 與 REMOTE dashboard 搜尋、分享 GitHub Gists 和 GitLab 個人或 project snippets。
authority: project-and-upstream
status: stable
verified_on: 2026-09-22
---

# Gists 與 snippets

`dev snippet` 適合分享或取回少量文字檔，不必建立 repository。
`dev gist` 提供相同操作，但固定使用 GitHub。Provider 沿用 `gh`、`glab`
的登入帳號，不會因目前所在 Git checkout 而改變發布目標。

## 搜尋與開啟

```bash
dev snippet list
dev gist search python
dev snippet search migration --forge gitlab
dev snippet list --project team/service
dev snippet open github:abc123 --print
dev snippet open https://gitlab.com/team/service/-/snippets/42
dev snippet list --json
```

預設列表涵蓋登入帳號建立的片段。GitLab 帳號 endpoint 包含你建立的個人及
project snippets；`--project` 才明確列出該 project 中可存取的 snippets，
不會遍歷所有專案。Host 使用 `GH_HOST`、`GITLAB_HOST`／`GLAB_HOST`，
預設為 github.com 與 gitlab.com。

普通搜尋要求每個查詢詞都出現在 metadata 中，包含標題、描述與檔名，
不下載檔案內容。結果依更新時間排序。`open` 不帶參數時提供互動 picker；
只有 ID 時必須指定 forge，或使用固定 GitHub 的 `gist` 命令。

```bash
dev snippet search "retry timeout" --content
```

全文搜尋需要明確啟用：每檔檢索文字最多 1 MiB、每次最多 16 MiB、四個並行
讀取，整體操作期限為 60 秒。文字 budget 不代表 provider CLI 的 HTTP 傳輸量上限。兩個平台的多檔案片段都會搜尋。截斷、缺少檔案或
網路失敗會標記 coverage 不完整，並保留已確認符合的結果。
JSON 包含 `complete` 與 `issues`，不包含全文；搜尋內容不會寫入 cache。
Metadata 同樣使用 60 秒期限，每頁 100 筆。

這是選定帳號／project 範圍的搜尋，不是 GitHub 全站 Gist 搜尋。
Provider 失敗不等同於空列表。

## 建立分享

```bash
dev gist create demo.py notes.md --description "Small reproduction"
cat demo.py | dev gist create - --filename demo.py
dev snippet create demo.py --forge gitlab --project team/service --title "Repro"
dev snippet create demo.py --forge github --dry-run --json
dev snippet create
```

明確的檔案參數會發布這些 UTF-8 文字檔，不遞迴上傳目錄，重複 basename
會拒絕。總文字上限為 16 MiB，GitLab 最多十個檔案。
`--filename` 命名 stdin 或 editor 草稿。`--title` 預設取第一個檔名；
GitHub 沒有指定 `--description` 時使用 title 作為描述。

在 terminal 中不提供檔案時，dev 詢問目標與檔名，依序選用
`--editor`／`$VISUAL`／`$EDITOR`（再 fallback 到 nvim/vim/vi），最後顯示
發布確認。GitLab 可選個人 snippet 或明確指定的 project。
完整指定的非互動命令直接發布；stdin 必須使用 `-`、`--filename` 與明確
平台，固定 GitHub 的 `dev gist` 除外。

| 平台 | 預設 | 分享方式 |
|---|---|---|
| GitHub | `secret` | 不列出，但任何持有連結的人皆可閱讀。 |
| GitLab 個人 | `private` | 只有本人可讀；公開連結分享請選 public。 |
| GitLab project | `private` | 依 project 成員權限存取。 |

`--visibility public` 使用平台的公開設定。GitLab `internal` 只適用支援的
host，GitLab.com 不提供。GitHub secret 不等於 GitLab private。
命令不設定自動到期時間。

Editor 草稿放在 repository 外的私人目錄
`paths.state_dir/snippets/drafts`。取消或失敗時保留草稿並回報路徑，發布
成功後才刪除 helper 建立的草稿。Dry-run 只顯示檔名、大小與目標 metadata，
不發布文字內容。

建立只向選定平台送出一次請求。無法確認的結果標為 `unknown`，不自動重試；
再次建立前先檢查列表。成功建立會回傳 ID 與 URL；之後 `--web` 開啟瀏覽器
失敗，仍視為已成功發布。

## Dashboard

REMOTE 預設顯示 repositories。從 `Ctrl+O` 選 **Show snippets** 或
**Show repositories** 切換。Snippets 保留獨立的選取與搜尋狀態，切換過去
才載入。普通 `/` 搜尋 metadata，全文搜尋需要另外明確執行。
選單也提供平台／project 範圍、重新整理、開啟或複製 URL，以及相同 CLI
建立 wizard。Snippets 沒有 repository clone、task 或 worktree 操作。

## 來源與替代方案

- [GitHub Gist API](https://docs.github.com/en/rest/gists/gists) 與
  [Gist visibility](https://docs.github.com/en/get-started/writing-on-github/editing-and-sharing-content-with-gists/creating-gists)。
- [GitLab snippets](https://docs.gitlab.com/user/snippets/) 與
  [project snippets API](https://docs.gitlab.com/api/project_snippets/)。
- [PrivateBin](https://privatebin.info/) 可依 instance 提供到期或閱後即焚，
  目前不是 dev provider。

Repository 起始模板仍使用 `dev repo new --template owner/starter`。
Gists 與 snippets 是獨立分享用途，不是 repository 生命週期記錄。
