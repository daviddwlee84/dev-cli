# Pre-commit redaction stack

The three-layer defense agents can rely on when committing agent
transcripts + plan files.

```
  Layer 0: SpecStory native        (specstory >= 2.4.0, ON BY DEFAULT)
            │  redacts as it WRITES .specstory/history/*.md (and cloud sync)
            │  emits [REDACTED:<betterleaks-rule-id>]
            ▼
  staged .md (transcript / plan)
            │
            ▼
  Layer 1: redact-agent-secrets   (pre-commit, pinned remote hook)
            │  rewrites file in place, re-stages via pre-commit
            ▼
  Layer 2: gitleaks-system         (pre-commit, github.com/gitleaks/gitleaks)
            │  blocks commit if a rule still matches
            ▼
  Layer 3: scan-staged.sh          (agent-invoked, belt-and-suspenders)
            │  run by the agent before `git commit` to branch on exit codes
            ▼
         git commit
```

Layers 1 and 2 are installed by `bootstrap-project.sh`. Layer 3 is the
wrapper agents are expected to call before committing so they can react
to structured exit codes without parsing pre-commit output.

Layer 0 is not ours — it is SpecStory doing the same job first, and only for
`.specstory/history/`. It is the reason Layer 1 usually has nothing to do.

## Layer 0: SpecStory native redaction

Since v2.4.0 the SpecStory CLI redacts secrets before writing markdown or
syncing to cloud, using the Betterleaks ruleset, on by default. Measured
coverage, the config knobs, and the classes it misses are in
[`specstory-native-redaction.md`](./specstory-native-redaction.md).

Two consequences for the layers below:

- **Layer 1 must not re-redact what Layer 0 cleaned.** Both write
  `[REDACTED:<rule-id>]`, and `.gitleaks.toml` allowlists that shape (at
  `regexTarget = "match"`, so a line holding both a placeholder and a live key
  still fires). A transcript SpecStory already cleaned passes through
  untouched — which is what stops the `git add` -> commit -> "files were
  modified by this hook" -> `git add` -> commit loop.
- **Layer 1 is still load-bearing.** Layer 0 covers none of the other artifact
  dirs (`.claude/plans/`, `.cursor/rules/`, …), none of our custom rules, and
  misses many classes when the token appears in prose rather than `KEY=value`.

Below SpecStory 2.4.0, or with `[redaction] enabled = false`, Layer 0 does not
exist and Layer 1 should run with `--legacy`.

## Layer 1: redact-agent-secrets

Implemented by `assets/redact_secrets.py` and published as a **pinned
remote pre-commit hook** via this repo's root `.pre-commit-hooks.yaml`.
Consuming repos reference it in their `.pre-commit-config.yaml`:

```yaml
- repo: https://github.com/daviddwlee84/agent-skills
  rev: ahh-v1.1.0
  hooks:
    - id: redact-agent-secrets
```

There is no vendored `scripts/redact_secrets.py`, so a fix here reaches
every repo via `pre-commit autoupdate` (bumps `rev:`). On each commit:

1. Pre-commit matches staged files against the hook's `files:` default
   (declared in `.pre-commit-hooks.yaml`, mirroring
   `assets/artifact-dirs.txt`). `language: script` runs the file
   directly via its `#!/usr/bin/env python3` shebang — stdlib-only, no
   env build, no uv/pip.
2. `redact_secrets.py --fix` runs gitleaks against those files in staged
   mode, gathers findings, and replaces each literal secret with
   `[REDACTED:<rule-id>]` — the same sentinel SpecStory >= 2.4.0 writes
   natively, so neither layer re-redacts the other's output. (`first3...last3`
   still appears in the *console report*, where a fingerprint helps identify
   which credential to rotate; `--legacy` writes it to the file instead.)
3. Private-key PEM blocks (`-----BEGIN ... PRIVATE KEY-----` … `-----END
   ... PRIVATE KEY-----`) are replaced wholesale with
   `[REDACTED:private-key]` — SpecStory's own label for this class — and any
   stray key **header** token (a truncated key, or a header quoted in prose)
   with `[REDACTED:private-key-header]`. These
   headers — *not* the bare phrase `PRIVATE KEY` — are exactly what the
   downstream `detect-private-key` hook greps for (its `BLACKLIST`:
   `BEGIN … PRIVATE KEY`, `PuTTY-User-Key-File-N`, `BEGIN OpenVPN Static
   key V1`), so the redactor scopes to them. Bare `PRIVATE KEY` prose is
   left intact on purpose: redacting it mangled legitimate text and,
   against a live transcript writer that re-appends the words on every
   diagnostic command, never converged (see the redact-loop pitfall in
   `SKILL.md`). The two placeholders contain neither a header token nor
   the bare phrase (they are lowercase; the header regex matches uppercase
   only), so a second pass is a no-op.
4. Any modified file is rewritten on disk. Pre-commit notices and exits
   non-zero with "files were modified by this hook" — the user then
   `git add`s the redacted files and recommits. Same UX as
   trailing-whitespace or end-of-file-fixer.

### Why redact (Layer 1) instead of block (Layer 2) for agent artifacts?

Because "blocking" a chat transcript on a leak is useless — the secret
is already in the file system, in the shell history, in the agent's
conversation context. The only defense left is "don't let it hit
`origin/`". Rewriting the file is the cheapest way to achieve that
while keeping the surrounding prose intact for future readers.

## Layer 2: gitleaks-system

The upstream `github.com/gitleaks/gitleaks` hook (pinned at `v8.22.1` in
the template). Runs after Layer 1 so it sees the redacted file. With
`.gitleaks.toml` in the repo root, custom rules apply automatically.

### Allowlist design

Four allowlists in `gitleaks.toml.template`:

- **Global** (`[[allowlists]]`, line-scoped): tolerates `*_REDACTED*`
  sentinels emitted by older Layer 1 runs, plus truncated example shapes like
  `sk-proj-abc...`.
- **Shared sentinel** (`[[allowlists]]`, `regexTarget = "match"`): the
  `[REDACTED:<rule-id>]` shape written by both Layer 0 and Layer 1. Scoped to
  the match rather than the line on purpose — at line scope, a line carrying
  both a placeholder and a live key would be allowlisted wholesale.
- **Skill test corpus** (`[[allowlists]]`, path-only): `npx skills add`
  installs this skill's `tests/` into the consuming repo, and that suite
  exists to be detected. Without this, every repo installing the skill fails
  its first commit on the skill's own test data. The `detect-private-key` hook
  has no allowlist mechanism, so the pre-commit template excludes the same
  path there instead.
- **Path-scoped** (`[[allowlists]]` with `paths`): inside agent
  artifact directories, tolerate example markers (`example-key`,
  `your-api-key-here`) and truncated example shapes. Both the **path
  AND a regex** must match — a real `sk-ant-api03-<95 chars>AA` inside
  a transcript still fires.

Real leaks take precedence over documentation because gitleaks evaluates
the finding's actual bytes, and truncated-example shapes don't have
enough entropy to match the strict rules in the first place.

## Layer 3: scan-staged.sh

Belt-and-suspenders wrapper:

```bash
bash /path/to/skills/local/agent-history-hygiene/scripts/scan-staged.sh
```

Runs `gitleaks git --staged` and translates the outcome into exit codes
agents can branch on:

| Exit | Meaning                              | Typical agent reaction                                  |
|-----:|--------------------------------------|---------------------------------------------------------|
|    0 | Clean                                | Proceed with `git commit`                               |
|   10 | Leaks found, `--redact` passed       | Run `redact_secrets.py --fix`, re-stage, re-run         |
|   20 | Leaks found, no redaction            | Rotate secret at provider, then redact + re-stage       |
|   30 | gitleaks not installed               | Surface install hint; fall back to bare pre-commit      |
|    2 | Not inside a git repo                | Abort or bootstrap a repo first                         |

Scripts always emit **JSON-lines findings on stdout** so the agent can
feed them to follow-up tooling without parsing prose.

## Releasing / updating the pinned hook

`assets/redact_secrets.py` is the single source; it's published to
consuming repos through this repo's root `.pre-commit-hooks.yaml`, pinned
by tag. To ship a fix:

```bash
# 1. edit assets/redact_secrets.py + tests, run the suite
uv run --with pytest pytest skills/local/agent-history-hygiene/tests

# 2. tag a new hook release (keep the ahh-v* series for this skill) and push
git tag ahh-v1.2.0 && git push origin main --tags

# 3. in each consuming repo, bump the rev (or `pre-commit autoupdate`)
#    - repo: https://github.com/daviddwlee84/agent-skills
#      rev: ahh-v1.2.0
```

**Monorepo caveat:** `pre-commit autoupdate` moves `rev:` to the newest
tag on this repo, so an unrelated skill's tag could bump this hook. Keep
hook releases on the `ahh-v*` series and, if you need strict isolation,
pin a commit SHA in `rev:` instead of a tag.

**Migrating an old vendored repo** (committed `scripts/redact_secrets.py`
+ a `- repo: local` redact hook) to the pinned hook:

```bash
bash skills/local/agent-history-hygiene/scripts/bootstrap-project.sh --migrate
```

`DEFAULT_PATHS` in the script and the `files:` default in
`.pre-commit-hooks.yaml` both mirror `assets/artifact-dirs.txt` — keep
the three in sync when adding an artifact directory.

## Inline allowlist pragmas

For one-off false positives that don't deserve a config change, gitleaks
accepts inline `#gitleaks:allow` on the same line as the match. Use
sparingly — every pragma is an opportunity for a real secret to ride
along in review.

```
# Example call pattern:
# curl -H "Authorization: Bearer sk-proj-fake1234..." #gitleaks:allow
```

For whole-file exceptions, prefer adding a path to `.gitleaksignore`
(one glob per line, gitignore syntax).

## Cross-reference

- [`transcript-session-discovery.md`](./transcript-session-discovery.md)
  — how we locate which files to scan.
- [`remediation.md`](./remediation.md) — what to do when Layer 3 fires
  on an already-pushed commit.
- [`../assets/artifact-dirs.txt`](../assets/artifact-dirs.txt) — the
  shared source of truth for "which directories contain agent artifacts".
