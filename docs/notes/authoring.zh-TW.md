---
description: 對每份新 documentation note 使用可重複的 metadata、source、bilingual writing 與 verification template。
authority: project-policy
status: maintained
verified_on: 2026-08-28
lang: zh-TW
---

# 撰寫筆記

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

有價值的 note 要回答：什麼是真的、evidence 為何支持、何時查核、如何安全操作，以及如何 verify/cleanup。

## Page template

```markdown
---
description: 能在 search 與 llms.txt 獨立成立的一句話。
authority: project | git-scm | github-docs | anthropic-docs | project-policy
status: draft | verified | maintained | evolving | historical
verified_on: YYYY-MM-DD
minimum_version: optional
tested_with: optional
---

# Title

一句話答案。

!!! info "Freshness"
    Authority、status、verification date 與 version boundary。

## Best-practice rule
## When to use it
## Decision table or mental model
## Minimal workflow
## Safety boundaries and known limitations
## Verification and cleanup
## Sources
## Related pages
```

Template section 沒有 decision value 時就省略，不要為了形式建立空 section。

## Source discipline

- Product behavior：連結 code 與 tests；可行時實際執行。
- Git：引用 current installed-version manual 或 `git-scm.com`。
- GitHub collaboration：引用現行 GitHub Docs。
- Claude Code：引用現行 Anthropic docs 並記錄 preview/experimental/version state。
- Standards：指出 exact specification version。
- Historical page：包含 snapshot/date 與 non-normative warning。
- Project recommendation：標示 `project-policy`，不暗示 upstream guarantee。

不要複製 external prose 或 local `git-workflow` skill；請獨立解釋 verified claim 並連結 primary source。

## 雙語 workflow

1. 完成聚焦、附 source/metadata 的 English page。
2. 把完整意思翻成 `*.zh-TW.md` sibling。
3. 使用有公認中文譯名的 technical term 時，以 `中文 (English original)` 引入；產品名稱與 Git／CLI／agent domain terms 若翻譯會降低精確度，就保留 English。
4. 不自創翻譯；code、tool/API name、CLI flag、package name 與 path 一律不翻譯。
5. 兩種語言都使用 canonical `.md` links；i18n plugin 負責 language context。
6. 在同一 change 更新兩頁與 `verified_on`。
7. 絕不 merge `Translation pending` placeholder。

## 新增與驗證 page

```bash
bash .claude/skills/mkdocs-site-bootstrap/scripts/add-docs-page.sh \
  --section Notes \
  --title "Topic title" \
  --slug topic-title

uv run python scripts/check-docs.py --source --generate-llms
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

Source check enforce metadata、nav membership、bilingual parity、snippet targets 與 generated LLM indexes；rendered check 驗證 local links/anchors、language targets 與 non-empty LLM outputs。

## Writing guidance

- 先寫 decision/rule，不先寫 chronology。
- Choices 使用 table；operations 使用 numbered steps。
- 精確說明 tool 隔離什麼、仍共享什麼。
- Destructive command 前要有 inspection/dry-run/recovery step。
- 分開 “not observed”、“unsupported” 與 “failed”。
- Version number 放在它限制的 claim 附近。
- 小型 reproducible example 優於大量 speculative edge case。
- 連到一個 canonical explanation，不要跨 pages 重複同一內容。

## Review checklist

- [ ] Description 可獨立成立。
- [ ] Authority/status/date/version 正確。
- [ ] Current behavior 已以 code/test 或 primary docs 查核。
- [ ] English 與 zh-TW 傳達相同規則與 caveats。
- [ ] 沒有 private path、secret 或未授權 copied prose。
- [ ] Command 說明 prerequisites、side effects 與 cleanup。
- [ ] Build 後 internal links 與 fragments 可解析。
- [ ] `llms.txt` 有該 page description。
- [ ] Superseded page 指向 replacement。

## 相關頁面

- [筆記索引](index.md)
- [來源與時效](../reference/sources-freshness.md)
- [最佳實務](../best-practices.md)
