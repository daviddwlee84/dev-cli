# Repository hygiene implementation

Approved: native hygiene domain and CLI status/setup/scan/rules/redact; configurable
secret/privacy block/warn/off policies; staged/worktree/history scopes; precise
exceptions; local-only reviewed SSH identity imports; public generic CI gates;
three-platform guarded text edits and private recovery; manual post-writer dogfood.

Current agent only. Preserve canonical live transcripts and the active
feat/tailscale-ssh stream. Implement from explicit main baseline 3c3504d.
Do not execute SSH, DNS, Match exec, key reads, automatic agent launch, history
rewrite, global hook replacement, or live transcript redaction during dogfood.

Audit: tools and global hook exist but root pre-commit/gitleaks configs are absent.
Manual history scan produced 416 candidates (not confirmed leaks); known SSH IPs
matched six distinct values, 163 occurrences. Same-line placeholder allowlists
hide synthetic secrets. Scanner errors and incomplete reads must never be clean.

Deliver source, tests, hooks, bilingual docs, bundled skill, history-impact
assessment, and retained manual dogfood steps. Follow repository release rules.
