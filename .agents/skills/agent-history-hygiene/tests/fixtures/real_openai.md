# real openai fixture

Contains one realistic-shape OpenAI project key. The `openai-project-key`
rule requires `sk-proj-` + at least 80 chars of `[A-Za-z0-9_-]`. The
filler below is exactly 100 chars, so the rule fires regardless of the
surrounding prose.

OPENAI_API_KEY=sk-proj-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa <!-- gitleaks:allow -->
