---
name: agent-history-hygiene
description: Commit SpecStory chat transcripts (`.specstory/history/*.md`), Claude Code plan files (`.claude/plans/*.md`, `plansDirectory`), and other coding-agent artifacts (`.cursor/plans/`, `.cursor/rules/`, `.opencode/plans/`, `.specify/`, `.codex/`) alongside the feature diff they produced — without leaking `.env` contents, API keys, or private-key PEM blocks into git history. Use when the user says "commit my chat", "save this specstory session", "stage the plan file", "scrub the transcript", "my .env leaked in chat", "bootstrap pre-commit for this project", or when you notice untracked `.specstory/history/*.md` or `.claude/plans/*.md` files while running `git status`. Also use after an accidental push of a secret to enforce rotate-first, rewrite-last remediation instead of reflexive `git push --force`.
---

# Agent history hygiene

Keep exact agent transcripts and plans with their feature, without publishing
credentials or unwanted personal information. Repository protection must be
verified from the effective hook chain, not inferred from installed tools.

## Prefer the shared dev workflow

When the installed dev supports `hygiene`, use its domain for setup, scans,
private rules and reviewed text changes:

```bash
dev hygiene status
dev hygiene setup --json
dev hygiene setup --apply --plan <id> --yes
dev hygiene scan --scope staged --json
dev hygiene scan --scope history --audit --timeout 40m --json
```

`pre-commit` and `gitleaks` must be available. A global `core.hooksPath` can be
correct, but its script may skip all checks when repository configs are absent.
The shared setup preserves existing hooks/comments and refuses an unverified
chain. Repository new/setup's `agent-history-hygiene` initializer uses this same
service. New default hooks block; they do not rewrite or stage files.

Per-repo secret/known/generic policy supports block/warn/off, with private local
overrides and exact reasoned exceptions. Private visibility is advice, never an
automatic exemption. SSH imports are static and reviewed: no DNS, login, Match
exec, private-key reads or rule-value export. Personal rules stay outside Git;
CI checks public rules and must state the missing personal coverage.

Read the bundled dev reference `references/hygiene.md` through `dev help hygiene`
or the public hygiene guide before rule changes or redaction. The CLI supports
all regular text, rather than treating transcripts as the only leak surface.

## Preserve the review trail

1. Identify the exact provider/session UUID from the transcript preamble. Never
   select another session just because its file is newest.
2. Commit product changes and exact plans. Do not broadly stage every agent
   directory when multiple writers exist.
3. Use `dev prepare --session provider:uuid` and finalization after its writer
   exits when that lifecycle applies. Byte stability alone is not writer proof.
4. Scan exact staged content. Partial staging must not accidentally stage
   unrelated working-copy edits.
5. Keep histories visible to Git; only derived statistics and machine-local
   identity are ignored. Avoid blanket `.specstory/` ignores.

Do not close, restart or spawn agents to make hygiene checks pass without the
applicable authorization. A new agent without SpecStory does not stop an older
writer. Preserve the user's original task while offering additional cleanup.

## Reviewed redaction

```bash
dev hygiene redact --report <report-id> --file <reviewed-file> --json
# After reviewing, and after the exact artifact writer exited:
dev hygiene redact --apply --plan <plan-id> --yes --writer-stopped
```

Plans bind native file identity, contents, policy and checkout state. Stale,
modified or partial plans are not authority to overwrite current data. Keep
private recovery and rerun scans/tests after source or configuration changes.
The running transcript is left for a manual external trigger after its writer
stops. No automatic history rewrite, agent launch, commit, push or cleanup occurs.

## Legacy scripts

The scripts remain explicit compatibility utilities. Prefer dev for new setup
and partial-staging-safe edits; a separately published remote redactor hook has
its own version and is not silently updated by dev.

- `find-session.sh --json`: locate candidate transcript/plan metadata.
- `stage-agent-artifacts.sh --session-only --dry-run`: inspect exact staging;
  without session-only it may include other dirty artifacts, so use cautiously.
- `scan-staged.sh`: masked JSON-lines locations only, never raw Match/Secret.
  Exit 0 means clean; 20 means candidate findings; legacy `--redact` selects
  exit 10 but only masks output, never modifies files. Exit 30 means missing
  gitleaks; 40 means scanner/JSON/required-parser failure; 2 means not in Git.
- `assets/redact_secrets.py --fix`: explicit legacy artifact mutation. Scanner
  failure is an error, not an empty result; console output uses full sentinels.
  This utility can restage, so use the reviewed dev flow for partial staging.
- `bootstrap-project.sh`: legacy installer; new default templates require dev
  hygiene. Its old `--migrate` targets the separately published remote redactor,
  not the new dev policy migration. Prefer `dev hygiene setup`.

SpecStory may already redact on write, but provider behavior/version is not proof
that every secret class or artifact directory is covered. Preserve existing
`[REDACTED:<rule-id>]` markers. An inert marker must never exempt another secret
on the same line. `.gitleaksignore` contains exact finding fingerprints, not
file/path globs. Portable pattern exceptions need a narrow rule/path/match scope.

## Confirmed historical exposures

Read [remediation](references/remediation.md). First distinguish fixtures,
examples and scanner candidates from confirmed credentials. Revoke/rotate a
confirmed exposed credential at its provider before considering Git history.

Shared main and immutable release tags stay unchanged in ordinary hygiene work.
Correct HEAD in a new commit, and assess old commits, tags, PR references, forks,
clones, caches and distribution artifacts separately. A force push does not erase
all copies or revoke a credential. Personal names, Git author metadata and filenames
also need an explicit impact assessment; text replacement does not rewrite them.

Never echo matching bytes or raw process arguments into a chat to investigate a
finding. Use rule IDs, safe locations and private local reports. Keep raw reports,
recovery images and personal identity dictionaries outside Git and CI logs.

## Validation

Run the skill's fixture tests for redactor/scan-wrapper changes. For dev changes,
run native hygiene/storage/text-transaction tests and the real hook test:

```bash
python scripts/test-hygiene-hooks.py --dev <absolute-built-dev>
```

It uses an isolated HOME/repository and synthetic credentials. Validate same-line
placeholder bypasses, hidden files, partial staging, missing/malformed scanner
results, writer/stale-plan refusal and private recovery. Synchronize embedded
bootstrap assets and both locales of public documentation.
