---
description: 整理未來 dev-cli、Git、harness、experiment、incident 與 source-review notes，不讓 main guides 失去穩定性。
authority: project-policy
status: maintained
verified_on: 2026-08-28
lang: zh-TW
---

# 筆記索引

!!! note "術語規則"
    有公認中文譯名且本文使用中文時，首次以「中文 (English original)」呈現。產品名稱與 Git／CLI／agent domain terms 可直接保留英文；沒有公認譯名不得自創。程式碼、API／tool 名稱、CLI flag、套件名與路徑一律不翻譯。

Note 是帶 evidence 的 working knowledge，之後可升格成 guide 或 reference page；不是放未驗證操作指令的 dumping ground。

## Taxonomy

| Note type | 記錄內容 | Graduation target |
|---|---|---|
| `harness` | Claude Code 或其他 coding agent 的 behavior/version research | provider guide 或 compatibility matrix |
| `experiment` | hypothesis、setup、observations、result、cleanup | best practice 或 discarded finding |
| `decision` | context、options、chosen rule、consequences | concepts/project policy |
| `incident` | symptom、evidence、recovery、prevention | troubleshooting/compatibility |
| `source-review` | external page 真正支持的 claim 與 date/status | sources matrix |
| `recipe` | 包含 safety/verification 的 repeatable sequence | user guide 或 skill |

未來 notes 放在 `docs/notes/`，檔名使用 descriptive kebab-case。只有多個 notes 已形成穩定 category 才新增 subdirectory；不要因一個 item 建 deep taxonomy。

## Lifecycle

```text
draft → verified → maintained
   └──────► superseded / historical
```

- **draft：**investigation 未完成；不要把 operational command 放入 happy path。
- **verified：**已有 evidence 與 reproduction/authority。
- **maintained：**有 owner/freshness expectation 的 active reference。
- **superseded：**只有 replacement link 或 historical reasoning 有價值時保留。
- **historical：**刻意描述舊 release 或 practice。

## Index entries

新增 note 時，於此列出 type、status、verification date 與一句價值說明。升格 guide 後，用 canonical page link 取代原 entry，不要保留兩份 source of truth。

| Note | Type | Status | Verified | Purpose |
|---|---|---|---|---|
| [撰寫筆記](authoring.md) | recipe | maintained | 2026-08-28 | page template、雙語/source rules 與 validation loop |

## 規則

- 分開 observation 與 recommendation。
- 快速變動 harness behavior 要記錄 exact product/version/date。
- 連結 primary sources 與 code/tests，不只 secondary summary。
- 盡量附 reproduction 與 verification commands。
- 說明 external side effects 與 cleanup。
- 不發布 secret、private absolute path、credential 或複製的 private log。
- Note promotion 成 main-site claim 時使用[來源與時效](../reference/sources-freshness.md)。

## 新增 page

建立 `Notes` nav section 後使用 project helper：

```bash
bash .claude/skills/mkdocs-site-bootstrap/scripts/add-docs-page.sh \
  --section Notes \
  --title "Topic title" \
  --slug topic-title
```

Configured languages 會建立 English page 與 zh-TW stub；merge 前要完成兩者。

## 相關頁面

- [撰寫筆記](authoring.md)
- [來源與時效](../reference/sources-freshness.md)
- [相容性與已知限制](../reference/compatibility.md)
